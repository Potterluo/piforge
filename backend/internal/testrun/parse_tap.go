package testrun

import (
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// node --test / tsx --test (TAP)
// ---------------------------------------------------------------------------
//
// Node's built-in runner prints TAP when stdout is not a TTY, which is exactly
// the case for an agent's tool call. Its summary is a sequence of `# name N`
// comments, and each failed test is a top-level `not ok N - name` followed by a
// YAML block whose `location` is the assertion site:
//
//	TAP version 13
//	# Subtest: adds
//	ok 1 - adds
//	  ---
//	  duration_ms: 0.6
//	  ...
//	not ok 2 - subtracts
//	  ---
//	  duration_ms: 0.7
//	  location: '/repo/math.test.js:12:5'
//	  error: '1 == 2'
//	  ...
//	1..2
//	# tests 2
//	# pass 1
//	# fail 1
//	# skipped 0
//	# duration_ms 42.5
//
// A plan-only stream (`1..N`) is also valid TAP v13: the plan states how many
// tests ran, and the top-level ok/not ok lines state their outcome. It is
// parsed, but a missing `# tests` is never replaced with pass+fail: those are
// different facts, and inventing the total is the bug this parser exists to
// avoid.
var (
	tapCount = regexp.MustCompile(`(?m)^#\s+(pass|fail|skipped|tests)\s+(\d+)\s*$`)
	tapTime  = regexp.MustCompile(`(?m)^#\s+duration_ms\s+([0-9]+(?:\.[0-9]+)?)\s*$`)
	tapPlan  = regexp.MustCompile(`(?m)^1\.\.(\d+)\s*$`)
	// Top-level only: a nested subtest is indented, and counting those would
	// report subtests as tests.
	tapResult   = regexp.MustCompile(`^(ok|not ok)\s+(\d+)(?:\s+-\s*(.*))?$`)
	tapNotOK    = regexp.MustCompile(`^not ok\s+\d+(?:\s+-\s*(.*))?$`)
	tapLocation = regexp.MustCompile(`(?m)^\s+location:\s*'([^']*)'\s*$`)
	tapError    = regexp.MustCompile(`(?m)^\s+error:\s*'((?:[^']|'')*)'\s*$`)
	tapPathLine = regexp.MustCompile(`^(.*):(\d+):(\d+)$`)
	// A block ends at a YAML document end or at the next top-level statement.
	tapBlockEnd = regexp.MustCompile(`^(?:\.\.\.|ok\s+\d+|not ok\s+\d+|1\.\.\d+|#)`)
	// A summary line that proves the run finished. The plan alone is not used:
	// hand-written TAP can put the plan first, before any result.
	tapSummaryComplete = regexp.MustCompile(`(?m)^#\s+(?:pass|fail|tests)\s+\d+\s*$`)
)

func parseTAP(out string) Result {
	res := Result{Runner: string(RunnerNode)}
	lines := strings.Split(out, "\n")

	var (
		havePass, haveFail bool
		haveTotal          bool
		havePlan           bool
		recognized         bool
	)
	for _, m := range tapCount.FindAllStringSubmatch(out, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		switch m[1] {
		case "pass":
			res.Passed, havePass, recognized = n, true, true
		case "fail":
			res.Failed, haveFail, recognized = n, true, true
		case "skipped":
			res.Skipped, recognized = n, true
		case "tests":
			res.Total, haveTotal, recognized = n, true, true
		}
	}
	if m := tapTime.FindStringSubmatch(out); m != nil {
		if ms, err := strconv.ParseFloat(m[1], 64); err == nil {
			res.DurationMS = int64(ms)
		}
	}
	if m := tapPlan.FindStringSubmatch(out); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			havePlan = true
			recognized = true
			if !haveTotal {
				// The plan is the runner's own statement of how many tests ran.
				res.Total = n
			}
		}
	}

	// A summary without totals is honest only if the counts it does carry are
	// kept as they are. Total and skipped stay zero (unknown) unless the
	// output said them; pass+fail is not the total.
	if (!havePass || !haveFail) && (havePlan || !haveTotal) {
		passed, failed, skipped, sawResult := tapResultCounts(lines)
		if sawResult {
			if !havePass {
				res.Passed = passed
			}
			if !haveFail {
				res.Failed = failed
			}
			if res.Skipped == 0 {
				res.Skipped = skipped
			}
		}
	}

	res.Failures = tapFailures(lines)
	if res.Failed == 0 && len(res.Failures) > 0 {
		// `not ok` lines are failures even when the summary is truncated before
		// `# fail`. The count follows the evidence the parser actually saw.
		res.Failed = len(res.Failures)
	}
	res.FailedRun = res.Failed > 0 || len(res.Failures) > 0
	res.Recognized = recognized
	return res
}

// tapResultCounts counts top-level ok/not ok lines. It is only used when the
// summary did not state pass/fail itself, and it ignores nested (indented)
// subtests so a file-level TAP tree cannot inflate the number.
func tapResultCounts(lines []string) (passed, failed, skipped int, saw bool) {
	for _, line := range lines {
		m := tapResult.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		saw = true
		name := m[3]
		switch m[1] {
		case "ok":
			if tapDirective(name) == "SKIP" {
				skipped++
			} else {
				passed++
			}
		case "not ok":
			if tapDirective(name) == "SKIP" || tapDirective(name) == "TODO" {
				skipped++
			} else {
				failed++
			}
		}
	}
	return passed, failed, skipped, saw
}

func tapFailures(lines []string) []Failure {
	failures := []Failure{}
	for i := 0; i < len(lines); i++ {
		m := tapNotOK.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		f := Failure{Name: strings.TrimSpace(tapDirectiveName(m[1]))}

		var block []string
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				block = append(block, line)
				continue
			}
			if tapBlockEnd.MatchString(line) {
				i = j - 1
				break
			}
			block = append(block, line)
		}

		text := strings.Join(block, "\n")
		if lm := tapLocation.FindStringSubmatch(text); lm != nil {
			f.File, f.Line = tapFileLine(lm[1])
		}
		if em := tapError.FindStringSubmatch(text); em != nil {
			f.Message = tapUnquote(em[1])
		}
		if f.Name == "" && f.Message == "" && f.File == "" {
			// `not ok 1` with no name, location or error carries nothing the
			// panel can show; the failed count still reports it.
			continue
		}
		failures = append(failures, f)
	}
	return failures
}

// tapDirectiveName drops the `# SKIP` / `# TODO` directive from a test name.
func tapDirectiveName(name string) string {
	if i := strings.Index(name, " # "); i >= 0 {
		return strings.TrimSpace(name[:i])
	}
	return strings.TrimSpace(name)
}

// tapDirective returns SKIP, TODO or "" for a TAP result name.
func tapDirective(name string) string {
	upper := strings.ToUpper(name)
	for _, directive := range []string{"SKIP", "TODO"} {
		if i := strings.Index(upper, "# "+directive); i >= 0 {
			return directive
		}
	}
	return ""
}

// tapFileLine turns a TAP location value into a file and line. Windows paths are
// double-escaped inside TAP's single-quoted YAML, so the value is unescaped
// before the trailing :line:column is split off.
func tapFileLine(raw string) (string, int) {
	value := tapUnquote(raw)
	value = strings.TrimPrefix(value, "file://")
	value = strings.ReplaceAll(value, "\\\\", "\\")
	if m := tapPathLine.FindStringSubmatch(value); m != nil {
		line, err := strconv.Atoi(m[2])
		if err != nil {
			return strings.TrimSpace(value), 0
		}
		return strings.TrimSpace(m[1]), line
	}
	return strings.TrimSpace(value), 0
}

// tapUnquote decodes the subset of YAML single-quoted scalars TAP uses.
func tapUnquote(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = value[1 : len(value)-1]
	}
	return strings.ReplaceAll(value, "''", "'")
}
