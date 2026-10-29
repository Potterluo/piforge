package main

import (
	"strings"
	"testing"
)

// The placement flags are one decision in three spellings. These tests pin the
// three answers that matter: docker is all-in by default, an explicit "host" is
// obeyed and reported, and asking for remote without a container is refused
// instead of quietly degrading to the host.
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
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, docker: true},
		},
		{
			name: "docker accepts auto spelled out",
			kind: "docker", browser: "auto", terminal: "auto", files: "auto",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, docker: true},
		},
		{
			name: "docker provider name is case-insensitive",
			kind: " Docker ",
			want: sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeRemote, docker: true},
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
			name: "explicit host file panel is honoured and reported",
			kind: "docker", files: "host",
			want:     sandboxPlacement{Browser: placeRemote, Terminal: placeRemote, Files: placeHost, docker: true},
			wantHost: []string{"files"},
			mixed:    true,
		},
		{
			name: "explicit host on every feature is honoured",
			kind: "docker", browser: "host", terminal: "host", files: "host",
			want:     sandboxPlacement{Browser: placeHost, Terminal: placeHost, Files: placeHost, docker: true},
			wantHost: []string{"browser", "terminal", "files"},
			mixed:    true,
		},
		{
			name: "mixed host and remote is reported with every host feature",
			kind: "docker", browser: "HOST", terminal: "auto", files: "host",
			want:     sandboxPlacement{Browser: placeHost, Terminal: placeRemote, Files: placeHost, docker: true},
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
// container, so the process must not start. The message has to name the flag and
// the reason, because "invalid flag value" would send the operator to the wrong
// place.
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
