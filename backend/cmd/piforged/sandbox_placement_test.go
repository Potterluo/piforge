package main

import (
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
