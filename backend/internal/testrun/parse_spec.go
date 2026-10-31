package testrun

import (
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// node --test / tsx --test (spec reporter)
// ---------------------------------------------------------------------------
//
// Node 24 prints the *spec* reporter, not TAP, when a test file is executed
// directly (`node x.test.js`) — the shape the acceptance pass's agent produced.
// The terminal panel only knew how to read TAP, so a real, passing run was
// recorded as 未识别格式 and the panel showed raw output instead of counts.
//
// The summary is a block of `ℹ name N` lines; suites are `▶ name`, passing
// tests `✔ name (0.4ms)`, failing tests `✖ name (0.5ms)`, and the failures are
// repeated in a `✖ failing tests:` section with their location and message:
//
//	▶ math
//	  ✔ adds (0.3477ms)
//	  ✖ subtracts (0.4732ms)
//	✖ math (2.0235ms)
//	ℹ tests 3
//	ℹ suites 2
//	ℹ pass 2
//	ℹ fail 1
//	ℹ cancelled 0
//	ℹ skipped 0
//	ℹ todo 0
//	ℹ duration_ms 5.1855
//
//	✖ failing tests:
//
//	test at suite.test.js:5:3
//	✖ subtracts (0.4732ms)
//	  AssertionError [ERR_ASSERTION]: Expected values to be strictly equal:
//
//	  1 !== 3
//
// Only the summary counts are trusted for pass/fail. The per-test lines exist
// in both the run log and the failure section (and a suite line also starts with
// ✖), so counting them would report suites as tests. Failures are read from the
// section, which is the reporter's own list of failed tests.
var (
	specCount    = regexp.MustCompile(`(?m)^ℹ\s+(pass|fail|skipped|todo|tests|cancelled)\s+(\d+)\s*$`)
	specTime     = regexp.MustCompile(`(?m)^ℹ\s+duration_ms\s+([0-9]+(?:\.[0-9]+)?)\s*$`)
	specSection  = regexp.MustCompile(`(?m)^✖ failing tests:\s*$`)
	specFailAt   = regexp.MustCompile(`^test at (.+):(\d+):(\d+)\s*$`)
	specFailLine = regexp.MustCompile(`^✖\s+(.+?)\s*$`)
	// The duration suffix is not part of the test's name.
	specDuration = regexp.MustCompile(`\s+\([0-9.]+m?s\)\s*$`)
	// A summary that proves the reporter finished. `ℹ pass` and `ℹ fail` are
	// printed together, so seeing either is enough to know the run is over.
	specComplete = regexp.MustCompile(`(?m)^ℹ\s+(?:pass|fail|tests)\s+\d+\s*$`)
)

func parseSpec(out string) Result {
	res := Result{Runner: string(RunnerNode)}

	var havePass, haveFail, haveTotal bool
	for _, m := range specCount.FindAllStringSubmatch(out, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		switch m[1] {
		case "pass":
			res.Passed, havePass = n, true
		case "fail":
			res.Failed, haveFail = n, true
		case "skipped":
			res.Skipped = n
		case "todo":
			// Node reports todo tests separately from skipped, but both are
			// "present and not a result"; the panel has one bucket for them.
			res.Skipped += n
		case "tests":
			res.Total, haveTotal = n, true
		}
	}
	if m := specTime.FindStringSubmatch(out); m != nil {
		if ms, err := strconv.ParseFloat(m[1], 64); err == nil {
			res.DurationMS = int64(ms)
		}
	}

	res.Failures = specFailures(out)
	if !haveTotal && (havePass || haveFail) {
		// The reporter prints `ℹ tests` alongside pass/fail; if a truncated log
		// dropped it, the parts it did state add up. This is not a guess about
		// subtests: the spec summary counts top-level tests only.
		res.Total = res.Passed + res.Failed + res.Skipped
	}
	res.FailedRun = res.Failed > 0 || len(res.Failures) > 0
	// Both lines are part of one summary block; accepting a lone `ℹ pass 1`
	// would claim a run finished on output that merely mentioned one.
	res.Recognized = havePass && haveFail
	return res
}

// specFailures reads the reporter's failure section. A run without failures has
// no section, which is not an error: the summary already said fail 0.
func specFailures(out string) []Failure {
	loc := specSection.FindStringIndex(out)
	if loc == nil {
		return []Failure{}
	}
	lines := strings.Split(out[loc[1]:], "\n")

	failures := []Failure{}
	var (
		current    *detail
		file       string
		line       int
		haveTarget bool
	)
	flush := func() {
		if current != nil {
			failures = append(failures, current.finish())
			current = nil
		}
	}
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if m := specFailAt.FindStringSubmatch(trimmed); m != nil {
			flush()
			file = m[1]
			if n, err := strconv.Atoi(m[2]); err == nil {
				line = n
			}
			haveTarget = true
			continue
		}
		if strings.HasPrefix(trimmed, "✖") {
			flush()
			name := specDuration.ReplaceAllString(strings.TrimSpace(strings.TrimPrefix(trimmed, "✖")), "")
			f := Failure{Name: name}
			if haveTarget {
				f.File, f.Line = file, line
			}
			current = &detail{failure: f, maxLines: 6}
			haveTarget = false
			continue
		}
		if current == nil {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "at ") {
			// Blank lines separate sections; `at …` lines are stack frames and
			// would push the assertion sentence out of the six-line window.
			continue
		}
		current.addLine(trimmed)
	}
	flush()
	return failures
}
