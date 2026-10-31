package main

import (
	"flag"
	"strings"
	"testing"
)

// The placement flags are one decision in three spellings. These tests pin the
// three answers that matter: a container provider is all-in by default, an
// explicit "host" is obeyed and reported, and asking for remote without a
// container is refused instead of quietly degrading to the host.
//
// docker and runner are the same boundary through different processes, so they
// have to resolve identically — otherwise a deployment that moved the Docker CLI
// into a service would silently move the panels too.
func TestResolveSandboxPlacement(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		browser  string
		terminal string
		files    string
		want     sandboxPlacement
		wantHost []string
		mixed    bool
	}{
		{
			name: "docker defaults to remote",
			kind: "docker",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, container: true},
		},
		{
			name: "runner defaults to remote",
			kind: "runner",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, container: true},
		},
		{
			name: "runner provider name is case-insensitive",
			kind: " Runner ",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, container: true},
		},
		{
			name: "docker accepts auto spelled out",
			kind: "docker", browser: "auto", terminal: "auto", files: "auto",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, container: true},
		},
		{
			name: "docker provider name is case-insensitive",
			kind: " Docker ",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, container: true},
		},
		{
			name: "local defaults to host",
			kind: "local",
			want: sandboxPlacement{Browser: placeHost, Terminal: placeHost, Files: placeHost},
			// Every feature is on the host, but that is the whole deployment, not
			// half of one.
			wantHost: []string{"browser", "terminal", "files"},
		},
		{
			name:     "an empty provider name means local",
			kind:     "",
			want:     sandboxPlacement{Browser: placeHost, Terminal: placeHost, Files: placeHost},
			wantHost: []string{"browser", "terminal", "files"},
		},
		{
			name: "explicit host file panel is honoured and reported under docker",
			kind: "docker", files: "host",
			want:     sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeHost, container: true},
			wantHost: []string{"files"},
			mixed:    true,
		},
		{
			name: "explicit host file panel is honoured and reported under the runner",
			kind: "runner", files: "host",
			want:     sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeHost, container: true},
			wantHost: []string{"files"},
			mixed:    true,
		},
		{
			name: "explicit host on every feature is honoured",
			kind: "docker", browser: "host", terminal: "host", files: "host",
			want:     sandboxPlacement{Browser: placeHost, Terminal: placeHost, Files: placeHost, container: true},
			wantHost: []string{"browser", "terminal", "files"},
			mixed:    true,
		},
		{
			name: "mixed host and remote is reported with every host feature",
			kind: "docker", browser: "HOST", terminal: "auto", files: "host",
			want:     sandboxPlacement{Browser: placeHost, Terminal: placeRemote, Files: placeHost, container: true},
			wantHost: []string{"browser", "files"},
			mixed:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSandboxPlacement(tc.kind, tc.browser, tc.terminal, tc.files)
			if err != nil {
				t.Fatalf("resolveSandboxPlacement(%q, %q, %q, %q): %v", tc.kind, tc.browser, tc.terminal, tc.files, err)
			}
			if got != tc.want {
				t.Fatalf("placement = %+v, want %+v", got, tc.want)
			}
			if host := got.HostFeatures(); !equalStrings(host, tc.wantHost) {
				t.Fatalf("HostFeatures() = %v, want %v", host, tc.wantHost)
			}
			if got.Mixed() != tc.mixed {
				t.Fatalf("Mixed() = %v, want %v", got.Mixed(), tc.mixed)
			}
		})
	}
}

// Remote under local is not a degraded mode, it is a contradiction: there is no
// container, so the process must not start. The message has to name the flag, the
// providers that would work, and the reason, because "invalid flag value" would
// send the operator to the wrong place.
func TestResolveSandboxPlacementLocalRemoteIsRefused(t *testing.T) {
	requests := []struct {
		feature  string
		browser  string
		terminal string
		files    string
	}{
		{"browser", "remote", "", ""},
		{"terminal", "", "remote", ""},
		{"files", "", "", "remote"},
	}

	for _, req := range requests {
		t.Run(req.feature, func(t *testing.T) {
			got, err := resolveSandboxPlacement("local", req.browser, req.terminal, req.files)
			if err == nil {
				t.Fatalf("resolveSandboxPlacement accepted -sandbox-%s remote with -sandbox local: %+v", req.feature, got)
			}
			msg := err.Error()
			if !strings.Contains(msg, "-sandbox-"+req.feature+" remote") {
				t.Fatalf("error does not name the offending flag: %q", msg)
			}
			if !strings.Contains(msg, "-sandbox docker or -sandbox runner") {
				t.Fatalf("error does not name the providers that would work: %q", msg)
			}
			if !strings.Contains(msg, "no container") {
				t.Fatalf("error does not say why: %q", msg)
			}
		})
	}
}

func TestResolveSandboxPlacementRejectsUnknownValue(t *testing.T) {
	_, err := resolveSandboxPlacement("docker", "", "elsewhere", "")
	if err == nil {
		t.Fatal("resolveSandboxPlacement accepted an unknown placement")
	}
	if !strings.Contains(err.Error(), "-sandbox-terminal") {
		t.Fatalf("error does not name the offending flag: %q", err.Error())
	}
}

// The runner credentials are a second decision about the same deployment. They
// must agree with the provider before anything is created: a runner URL under
// -sandbox local, or a missing token under -sandbox runner, would otherwise start
// a process that looks healthy and fails on the first session.
func TestValidateSandboxFlags(t *testing.T) {
	const url = "http://sandbox-runner:7801"
	const token = "shared-token"

	cases := []struct {
		name    string
		kind    string
		url     string
		token   string
		wantErr string
	}{
		{name: "local without runner flags is fine", kind: "local"},
		{name: "docker without runner flags is fine", kind: "docker"},
		{name: "runner with a url and a token is fine", kind: "runner", url: url, token: token},
		{name: "runner without a url is refused", kind: "runner", token: token, wantErr: "-sandbox-runner-url"},
		{name: "runner without a token is refused", kind: "runner", url: url, wantErr: "-sandbox-runner-token"},
		{name: "local with a runner url is refused", kind: "local", url: url, token: token, wantErr: "-sandbox-runner-url"},
		{name: "local with only a runner token is refused", kind: "local", token: token, wantErr: "-sandbox-runner-token"},
		{name: "docker with runner credentials is refused", kind: "docker", url: url, token: token, wantErr: "-sandbox-runner-url"},
		{name: "an unknown provider is refused", kind: "podman", wantErr: "-sandbox must be local, docker or runner"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSandboxFlags(tc.kind, tc.url, tc.token)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSandboxFlags(%q, %q, %q) = %v, want nil", tc.kind, tc.url, tc.token, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSandboxFlags(%q, %q, %q) accepted the combination", tc.kind, tc.url, tc.token)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Stage 2 gives each sandbox a workspace volume of its own, so the file panel has
// nothing on this disk to read. Stage 4 removed the flag that used to make this
// conditional: a container placement always has a detached workspace, so `host`
// is a contradiction and `local` is untouched.
func TestValidateContainerWorkspace(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		files   string
		wantErr bool
	}{
		{name: "local keeps the host panel", kind: "local", files: "host"},
		{name: "local is unaffected by the rule", kind: "local", files: "remote"},
		{name: "docker with a remote panel", kind: "docker", files: "remote"},
		{name: "runner with a remote panel", kind: "runner", files: "remote"},
		{name: "docker with a host panel is refused", kind: "docker", files: "host", wantErr: true},
		{name: "runner with a host panel is refused", kind: "runner", files: "host", wantErr: true},
		{name: "the request value is refused", kind: "runner", files: "auto", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateContainerWorkspace(tc.kind, tc.files)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateContainerWorkspace(%q, %q) = nil, want an error", tc.kind, tc.files)
				}
				if !strings.Contains(err.Error(), "must be remote") {
					t.Fatalf("error = %v, want it to name the file panel", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateContainerWorkspace(%q, %q) = %v", tc.kind, tc.files, err)
			}
		})
	}
}

// Stage 4 deleted the switch, not the intent behind it: `false` on a container
// placement is refused at startup (it is the unsandboxed multi-tenant instance
// stages 1–3 eliminated), `true` is a no-op so an existing compose file keeps
// working, and -sandbox local — the development path — still starts, with one
// warning when a retired variable asks for something local cannot have.
func TestRejectDisabledIsolation(t *testing.T) {
	cases := []struct {
		name        string
		kind        string
		flag        string // "" = not passed
		env         string
		wantErr     string // "" = must start
		wantWarning bool
	}{
		{name: "docker with no switch", kind: "docker"},
		{name: "runner with no switch", kind: "runner"},
		{name: "docker with the switch on", kind: "docker", flag: "true"},
		{name: "runner with the environment on", kind: "runner", env: "1"},
		{name: "local with no switch", kind: "local"},
		{name: "local with the switch off", kind: "local", flag: "false"},
		{name: "local with the environment off", kind: "local", env: "0"},
		{name: "docker with the switch off is refused", kind: "docker", flag: "false", wantErr: "always runs inside the sandbox"},
		{name: "runner with the environment off is refused", kind: "runner", env: "0", wantErr: "always runs inside the sandbox"},
		{name: "the flag beats the environment", kind: "docker", flag: "true", env: "0"},
		{name: "local ignores the retired switch, loudly", kind: "local", flag: "true", wantWarning: true},
		{name: "local ignores it from the environment too", kind: "local", env: "1", wantWarning: true},
		{name: "a nonsense environment value is an error", kind: "runner", env: "maybe", wantErr: "must be 0 or 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIFORGE_AGENT_IN_SANDBOX", tc.env)
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			value := true
			fs.BoolVar(&value, "agent-in-sandbox", true, "")
			args := []string{}
			if tc.flag != "" {
				args = append(args, "-agent-in-sandbox="+tc.flag)
			}
			if err := fs.Parse(args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			warning, err := rejectDisabledIsolation(fs, value, tc.kind)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("kind=%s flag=%q env=%q: %v, want it to start", tc.kind, tc.flag, tc.env, err)
				}
				if tc.wantWarning && warning == "" {
					t.Fatalf("kind=%s flag=%q env=%q: started silently; the retired switch has to be reported", tc.kind, tc.flag, tc.env)
				}
				if !tc.wantWarning && warning != "" {
					t.Fatalf("kind=%s flag=%q env=%q: unexpected warning %q", tc.kind, tc.flag, tc.env, warning)
				}
				return
			}
			if err == nil {
				t.Fatalf("kind=%s flag=%q env=%q: accepted, want %q", tc.kind, tc.flag, tc.env, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}
