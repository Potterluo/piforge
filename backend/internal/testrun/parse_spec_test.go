package testrun

import (
	"strings"
	"testing"
)

// The bytes Node 24.11 actually prints for `node suite.test.js` (stdout is a
// pipe, as it is for an agent's tool call). Three tests in two suites, one
// fails. Captured from a real run, not hand-written: the spec reporter's exact
// spacing (`ℹ tests 3`) and the repeated `✖` in the failure section are the
// details a hand-written fixture usually gets wrong.
const node24SpecFailure = `▶ math
  ✔ adds (0.3477ms)
  ✖ subtracts (0.4732ms)
  ▶ nested
    ✔ multiplies (0.5188ms)
  ✔ nested (0.611ms)
✖ math (2.0235ms)
ℹ tests 3
ℹ suites 2
ℹ pass 2
ℹ fail 1
ℹ cancelled 0
ℹ skipped 0
ℹ todo 0
ℹ duration_ms 5.1855

✖ failing tests:

test at suite.test.js:5:3
✖ subtracts (0.4732ms)
  AssertionError [ERR_ASSERTION]: Expected values to be strictly equal:
  
  1 !== 3
  
      at TestContext.<anonymous> (file:///repo/suite.test.js:5:36)
      at Test.runInAsyncScope (node:async_hooks:214:14)
      at Test.run (node:internal/test_runner/test:1106:25)
`

func TestParseNodeSpecReporter(t *testing.T) {
	got := Parse(string(RunnerNode), node24SpecFailure)
	if !got.Recognized {
		t.Fatalf("node 24 spec output was unrecognized:\n%s", node24SpecFailure)
	}
	assertSummary(t, got, true, 3, 2, 1, 0, 5)
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %+v, want one", got.Failures)
	}
	f := got.Failures[0]
	if f.Name != "subtracts" {
		t.Fatalf("failure name = %q, want subtracts", f.Name)
	}
	if f.File != "suite.test.js" || f.Line != 5 {
		t.Fatalf("failure location = %s:%d, want suite.test.js:5", f.File, f.Line)
	}
	if !strings.Contains(f.Message, "1 !== 3") {
		t.Fatalf("failure message = %q, want the assertion text", f.Message)
	}
	if strings.Contains(f.Message, "node:async_hooks") {
		t.Fatalf("the stack frames leaked into the message: %q", f.Message)
	}
	if !got.FailedRun {
		t.Fatal("FailedRun = false on a run with one failed test")
	}
}

func TestParseNodeSpecReporterPassingRun(t *testing.T) {
	output := `✔ adds (0.4053ms)
✔ subtracts (0.473ms)
ℹ tests 2
ℹ suites 0
ℹ pass 2
ℹ fail 0
ℹ cancelled 0
ℹ skipped 0
ℹ todo 0
ℹ duration_ms 4.7499
`
	got := Parse(string(RunnerNode), output)
	assertSummary(t, got, true, 2, 2, 0, 0, 4)
	if len(got.Failures) != 0 {
		t.Fatalf("a passing run has failures: %+v", got.Failures)
	}
}

// The same summary reaches the panel through an npm script or a Makefile
// target, and those parsers cascade into the node family only after their own
// summaries miss.
func TestParseNodeSpecReporterThroughWrappers(t *testing.T) {
	got := Parse(string(RunnerNPM), node24SpecFailure)
	assertSummary(t, got, true, 3, 2, 1, 0, 5)

	got = Parse(string(RunnerMake), node24SpecFailure)
	assertSummary(t, got, true, 3, 2, 1, 0, 5)
}

// Skipped and todo are both "present, not a result"; node reports them
// separately and its `tests` count includes both.
func TestParseNodeSpecReporterCountsSkippedAndTodo(t *testing.T) {
	output := `✔ one (0.39ms)
✔ three (0.0496ms) # TODO
ℹ tests 3
ℹ suites 0
ℹ pass 1
ℹ fail 0
ℹ cancelled 0
ℹ skipped 1
ℹ todo 1
ℹ duration_ms 4.0782
`
	got := Parse(string(RunnerNode), output)
	assertSummary(t, got, true, 3, 1, 0, 2, 4)
}

// A half summary is not a finished run: both `ℹ pass` and `ℹ fail` belong to
// the same block, and treating one as proof of completion would settle a
// terminal command that is still printing.
func TestParseNodeSpecReporterNeedsTheWholeSummary(t *testing.T) {
	got := Parse(string(RunnerNode), "some output\nℹ pass 1\n")
	if got.Recognized {
		t.Fatalf("a lone `ℹ pass` was accepted: %+v", got)
	}
	if got.Total != 0 || got.Passed != 0 || got.Failed != 0 {
		t.Fatalf("unrecognized output invented counts: %+v", got)
	}
}
