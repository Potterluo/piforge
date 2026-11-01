package main

import (
	"flag"
	"strings"
	"testing"
)

// Host placement is not a default any more (adversarial round 6): an operator who
// runs the binary with no -sandbox at all used to get the agent, the terminal, the
// browser and the files on the host, and nothing in the log said so. The rule
// these tests pin is the whole of it:
//
//   - no flag and no PIFORGE_SANDBOX: refuse, naming both ways to say it on
//     purpose;
//   - -sandbox local (or PIFORGE_SANDBOX=local): start, and only with a WARN
//     that says what the deployment gives up;
//   - -sandbox docker / -sandbox runner: start, no warning.
//
// The flag/environment precedence is part of the rule too: a flag on the command
// line beats an exported variable, which is how every other knob in this binary
// behaves.
func TestResolveSandboxKind(t *testing.T) {
	cases := []struct {
		name    string
		flag    string // "" = not passed, which is what the binary used to default from
		env     string
		want    string // "" = the start must be refused
		wantErr []string
	}{
		{
			name:    "no flag and no environment refuses",
			wantErr: []string{"-sandbox local", "-sandbox runner", "-sandbox docker"},
		},
		{
			name: "local is accepted when asked for explicitly",
			flag: "local", want: "local",
		},
		{
			name: "the environment counts as asking for local",
			env:  "local", want: "local",
		},
		{
			name: "docker is accepted",
			flag: "docker", want: "docker",
		},
		{
			name: "runner is accepted",
			flag: "runner", want: "runner",
		},
		{
			name: "the provider name is case-insensitive",
			flag: " Runner ", want: "runner",
		},
		{
			name: "the flag beats the environment",
			flag: "docker", env: "local", want: "docker",
		},
		{
			name:    "a blank environment is not an explicit request",
			env:     "   ",
			wantErr: []string{"-sandbox local"},
		},
		{
			name:    "an explicitly empty flag names the valid providers",
			flag:    " ",
			wantErr: []string{"-sandbox must be local, docker, runner or k8s"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			value := ""
			fs.StringVar(&value, "sandbox", "", "")
			args := []string{}
			if tc.flag != "" {
				args = append(args, "-sandbox="+tc.flag)
				value = tc.flag
			}
			if err := fs.Parse(args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			got, err := resolveSandboxKind(fs, value, tc.env)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("resolveSandboxKind accepted flag=%q env=%q as %q; host placement must be asked for", tc.flag, tc.env, got)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q does not name %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSandboxKind(flag=%q, env=%q) = %v, want %q", tc.flag, tc.env, err, tc.want)
			}
			if got != tc.want {
				t.Fatalf("resolveSandboxKind(flag=%q, env=%q) = %q, want %q", tc.flag, tc.env, got, tc.want)
			}
		})
	}
}

// A local placement has to be loud: the boot log is the only place an operator
// learns that this deployment has no container boundary. The message is checked
// by its claims, not its wording.
func TestHostPlacementWarning(t *testing.T) {
	for _, kind := range []string{"docker", "runner", " Runner ", "Docker"} {
		if warning := hostPlacementWarning(kind); warning != "" {
			t.Fatalf("hostPlacementWarning(%q) = %q; a container placement must not warn about host placement", kind, warning)
		}
	}
	warning := hostPlacementWarning("local")
	if warning == "" {
		t.Fatal("hostPlacementWarning(local) is empty; a host placement must warn at boot")
	}
	for _, want := range []string{"no container boundary", "on this host", "development"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning %q does not say %q", warning, want)
		}
	}
	// The warning is the one place the four things that leave the boundary are
	// named, because "unsandboxed" alone does not tell an operator what runs where.
	for _, feature := range []string{"agent", "terminal", "browser", "files"} {
		if !strings.Contains(warning, feature) {
			t.Fatalf("warning %q does not name the %s", warning, feature)
		}
	}
}

// The isolation flags that only make sense with a container are still refused on
// a local placement, and asking for local does not soften that: -sandbox local
// answers "where the agent runs", not "may the panels be remote".
func TestLocalPlacementStillRefusesIsolationFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	kind := ""
	fs.StringVar(&kind, "sandbox", "", "")
	if err := fs.Parse([]string{"-sandbox=local"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	resolved, err := resolveSandboxKind(fs, kind, "")
	if err != nil {
		t.Fatalf("resolveSandboxKind(local) = %v", err)
	}
	for _, feature := range []string{"browser", "terminal", "files"} {
		req := map[string]string{"browser": "", "terminal": "", "files": ""}
		req[feature] = "remote"
		if _, err := resolveSandboxPlacement(resolved, req["browser"], req["terminal"], req["files"]); err == nil {
			t.Fatalf("-sandbox %s remote was accepted under -sandbox local", feature)
		}
	}
}
