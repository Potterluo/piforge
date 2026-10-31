package term

import (
	"strings"
	"testing"
)

// The terminal must not inherit a pager. execve passes duplicates through and
// libc's getenv resolves first-wins, so "append PAGER=cat" on top of an inherited
// PAGER=less is not an override — it is a coin flip that `git log` loses by
// parking in `less` and swallowing the next keystrokes.
func TestTerminalEnvReplacesInheritedPager(t *testing.T) {
	t.Setenv("PAGER", "less")
	t.Setenv("GIT_PAGER", "less")
	t.Setenv("TERM", "dumb")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	env := terminalEnv()

	seen := make(map[string]string, len(env))
	duplicates := make([]string, 0)
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			duplicates = append(duplicates, key)
		}
		seen[key] = value
	}
	if len(duplicates) > 0 {
		t.Fatalf("duplicate environment keys %v: the child would read the stale value", duplicates)
	}

	for key, want := range map[string]string{
		"PAGER":               "cat",
		"GIT_PAGER":           "cat",
		"GIT_TERMINAL_PROMPT": "0",
		"TERM":                "xterm-256color",
		"PIFORGE_TERM":        "1",
	} {
		if got := seen[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// An unrelated inherited variable must survive: this is the user's environment,
// not a replacement for it.
func TestTerminalEnvKeepsUnrelatedVariables(t *testing.T) {
	t.Setenv("PIFORGE_TEST_KEEP", "kept")
	env := terminalEnv()
	for _, kv := range env {
		if kv == "PIFORGE_TEST_KEEP=kept" {
			return
		}
	}
	t.Fatal("terminalEnv dropped an unrelated variable")
}

// Guard the TERM default: an inherited value (or "dumb") breaks curses programs
// that then refuse to draw, so the terminal always states its own.
func TestTerminalEnvAlwaysSetsTerm(t *testing.T) {
	t.Setenv("TERM", "dumb")
	env := terminalEnv()
	for _, kv := range env {
		if kv == "TERM=xterm-256color" {
			return
		}
	}
	t.Fatal("TERM was not set for the terminal")
}
