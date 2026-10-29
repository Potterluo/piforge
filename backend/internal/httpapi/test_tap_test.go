package httpapi

import (
	"reflect"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/testrun"
)

// The terminal path is heuristic by nature, so it gets the same honesty tests
// as the parsers: only a typed runner command starts a run, the output is
// recognised when the runner printed a summary, and a replay cannot duplicate
// it.
type tapStub struct {
	created []string
	runners []string
	settled []testrun.Result
	raw     []string
}

func newStubTap() (*testTap, *tapStub) {
	stub := &tapStub{}
	tap := &testTap{
		create: func(command string, runner testrun.Runner) (string, bool) {
			stub.created = append(stub.created, command)
			stub.runners = append(stub.runners, string(runner))
			return "tst_1", true
		},
		settle: func(_ string, res testrun.Result, raw string, _ time.Time) {
			stub.settled = append(stub.settled, res)
			stub.raw = append(stub.raw, raw)
		},
	}
	return tap, stub
}

func TestTestTapStartsOnATypedTestCommand(t *testing.T) {
	tap, stub := newStubTap()
	tap.input([]byte("go test ./...\r"))
	if !reflect.DeepEqual(stub.created, []string{"go test ./..."}) {
		t.Fatalf("created = %v", stub.created)
	}
	if len(stub.runners) != 1 || stub.runners[0] != "go test" {
		t.Fatalf("runners = %v", stub.runners)
	}
}

func TestTestTapIgnoresOtherCommands(t *testing.T) {
	tap, stub := newStubTap()
	tap.input([]byte("git status\r"))
	tap.input([]byte("npm install\r"))
	tap.input([]byte("\r"))
	if len(stub.created) != 0 {
		t.Fatalf("created = %v, want none", stub.created)
	}
}

func TestTestTapSettlesOnTheRunnerSummary(t *testing.T) {
	tap, stub := newStubTap()
	tap.input([]byte("pytest -q\r"))
	tap.output(0, []byte("tests/test_calc.py ..F [100%]\n"))
	if len(stub.settled) != 0 {
		t.Fatalf("settled before a summary: %+v", stub.settled)
	}
	tap.output(int64(len("tests/test_calc.py ..F [100%]\n")), []byte("1 failed, 2 passed in 0.12s\n"))
	if len(stub.settled) != 1 {
		t.Fatalf("settled = %d, want 1", len(stub.settled))
	}
	res := stub.settled[0]
	if !res.Recognized || res.Failed != 1 || res.Passed != 2 || res.Total != 3 {
		t.Fatalf("result = %+v", res)
	}
}

func TestTestTapSettlesUnrecognisedOnTheNextCommand(t *testing.T) {
	tap, stub := newStubTap()
	tap.input([]byte("go test ./...\r"))
	tap.output(0, []byte("something the parser cannot read\n"))
	if len(stub.settled) != 0 {
		t.Fatalf("settled before the next prompt: %+v", stub.settled)
	}
	tap.input([]byte("ls\r"))
	if len(stub.settled) != 1 {
		t.Fatalf("settled = %d, want 1", len(stub.settled))
	}
	res := stub.settled[0]
	if res.Recognized {
		t.Fatalf("unreadable output must not be reported as a recognised result: %+v", res)
	}
	if stub.raw[0] == "" {
		t.Fatal("the raw tail was dropped")
	}
}

func TestTestTapSettlesWhenTheCommandCannotStart(t *testing.T) {
	tap, stub := newStubTap()
	tap.input([]byte("go test ./missing\r"))
	tap.output(0, []byte("go: no Go files in /work/missing\n"))
	if len(stub.settled) != 1 {
		t.Fatalf("settled = %d, want 1 (a command that never started must not linger)", len(stub.settled))
	}
	if stub.settled[0].Recognized {
		t.Fatalf("result = %+v, want unrecognized", stub.settled[0])
	}
}

func TestTestTapSkipsReplayedOutput(t *testing.T) {
	tap, stub := newStubTap()
	tap.input([]byte("pytest\r"))
	chunk := []byte("collected 1 item\n")
	tap.output(0, chunk)
	// A reconnecting socket replays from offset 0. The tap has already seen
	// these bytes; appending them again would double the counts.
	tap.output(0, chunk)
	tap.output(int64(len(chunk)), []byte("1 passed in 0.01s\n"))
	if len(stub.settled) != 1 {
		t.Fatalf("settled = %d, want 1", len(stub.settled))
	}
	if got := stub.settled[0]; got.Total != 1 || got.Passed != 1 {
		t.Fatalf("result = %+v (replayed output was counted twice)", got)
	}
}

func TestTestTapControlKeysDoNotFabricateCommands(t *testing.T) {
	tap, stub := newStubTap()
	// Up-arrow history recall, then Enter: the recalled command is invisible to
	// the tap, so nothing may start.
	tap.input([]byte("\x1b[A"))
	tap.input([]byte("\r"))
	if len(stub.created) != 0 {
		t.Fatalf("created = %v, want none", stub.created)
	}

	// Tab completion is dropped rather than guessed at.
	tap.input([]byte("go tes\t\r"))
	if len(stub.created) != 0 {
		t.Fatalf("tab completion produced %v", stub.created)
	}
}
