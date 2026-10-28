package main

import (
	"flag"
	"strings"
	"testing"
)

// The stand-in exists so the control plane can be tested without a model. It
// therefore has to survive every argument the real `pi` would survive — the
// failure mode otherwise is silent and total: the peer exits on an unknown flag
// before reading a byte, and every end-to-end test waits for a run that never
// starts.

func newParser() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("session", "", "")
	fs.String("script", "", "")
	fs.Bool("json-header", false, "")
	return fs
}

func TestWithoutUnknownDropsFlagsTheStandInDoesNotModel(t *testing.T) {
	// The order matters: a flag's *value* looks exactly like a positional
	// argument, so a filter that stops at the first bare word would let every
	// later flag through — which is how `--model` reached a peer that rejects it.
	got := withoutUnknown(newParser(), []string{
		"--session", "/tmp/s.jsonl",
		"--model", "piforge/deepseek-v4-flash",
		"--provider=piforge",
		"-c",
		"--json-header",
		"--script", "ok",
	})

	want := []string{"--session", "/tmp/s.jsonl", "--json-header", "--script", "ok"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestWithoutUnknownLeavesPositionalArgumentsAlone(t *testing.T) {
	// A prompt after the flags belongs to the peer, not to the parser; dropping
	// it would change what the agent is asked to do.
	got := withoutUnknown(newParser(), []string{"--model", "x", "--", "-not-a-flag"})
	if strings.Join(got, " ") != "-- -not-a-flag" {
		t.Fatalf("args = %v", got)
	}

	// And a prompt that never had a `--` before it still terminates filtering.
	got = withoutUnknown(newParser(), []string{"--script", "ok", "explain this repo"})
	if strings.Join(got, " ") != "--script ok explain this repo" {
		t.Fatalf("args = %v", got)
	}
}

func TestWithoutUnknownKeepsBooleanFlagsWithoutAValue(t *testing.T) {
	// `--json-header` takes no value, so the next word is not swallowed: eating
	// it would silently drop the flag after it.
	got := withoutUnknown(newParser(), []string{"--json-header", "--script", "ok"})
	if strings.Join(got, " ") != "--json-header --script ok" {
		t.Fatalf("args = %v", got)
	}
}
