package testrun

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ansiCSI strips the colour sequences test runners emit when stdout is a TTY.
// The terminal panel is a real PTY, so terminal runs are full of them; tool
// output usually is not, but stripping is always safe.
var ansiCSI = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

func stripANSI(s string) string { return ansiCSI.ReplaceAllString(s, "") }

// normalize flattens line endings and removes colour, which is the only
// preprocessing every parser shares.
func normalize(output string) string {
	output = stripANSI(output)
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = strings.ReplaceAll(output, "\r", "\n")
	return output
}

// Parse reads one runner's console output. Recognized is false when the output
// does not carry the runner's own summary; the caller must then record the run
// as 未识别格式 and keep the raw tail rather than trusting any of the fields.
func Parse(runner string, output string) Result {
	out := normalize(output)
	var res Result
	switch Runner(runner) {
	case RunnerGo:
		res = parseGo(out)
	case RunnerPytest:
		res = parsePytest(out)
	case RunnerNPM, RunnerJest, RunnerVitest:
		res = parseJS(out)
	case RunnerCargo:
		res = parseCargo(out)
	case RunnerMaven:
		res = parseMaven(out)
	case RunnerDotnet:
		res = parseDotnet(out)
	case RunnerMake:
		// A Makefile target is whatever it invokes, so parse the output with
		// every family and take the first one that actually recognises it.
		res = parseAny(out)
	default:
		res = parseAny(out)
	}
	res.Runner = string(runner)
	if !res.Recognized {
		// Nothing parsed means nothing is known. Keeping a half-guessed count
		// here is exactly the invented pass/fail the product must not show.
		res.Total, res.Passed, res.Failed, res.Skipped = 0, 0, 0, 0
		res.DurationMS = 0
		res.Failures = nil
		res.FailedRun = false
	}
	if res.Failures == nil {
		res.Failures = []Failure{}
	}
	if len(res.Failures) > MaxFailures {
		res.Failures = res.Failures[:MaxFailures]
	}
	return res
}

// parseAny tries every family in a fixed order and returns the first match.
// Order matters only for ambiguous output; the runner summaries do not overlap
// in practice.
func parseAny(out string) Result {
	for _, parser := range []func(string) Result{parseGo, parseCargo, parsePytest, parseJS, parseMaven, parseDotnet} {
		if res := parser(out); res.Recognized {
			return res
		}
	}
	return Result{}
}

// Complete reports whether output now contains the runner's final summary. The
// terminal tap uses it to decide that a typed test command has finished; it is
// a marker test, not a parse.
func Complete(runner string, output string) bool {
	out := normalize(output)
	switch Runner(runner) {
	case RunnerGo:
		return goComplete.MatchString(out)
	case RunnerPytest:
		return pytestBanner.MatchString(out) || pytestShort.MatchString(out)
	case RunnerNPM, RunnerJest, RunnerVitest:
		return jsComplete.MatchString(out)
	case RunnerCargo:
		return cargoResult.MatchString(out)
	case RunnerMaven:
		return mavenBuild.MatchString(out)
	case RunnerDotnet:
		return dotnetSummary.MatchString(out)
	case RunnerMake:
		return goComplete.MatchString(out) || cargoResult.MatchString(out) ||
			pytestBanner.MatchString(out) || jsComplete.MatchString(out) ||
			mavenBuild.MatchString(out) || dotnetSummary.MatchString(out)
	}
	return false
}

// Aborted reports output that proves the command failed before printing a
// summary — a missing binary, a directory with no tests, a command that does
// not exist. The terminal tap settles on it instead of leaving a row "running"
// until the idle timeout.
var abortedRE = regexp.MustCompile(`(?i)(command not found|: not found|no go files|no test files|cannot find package|no such file or directory|directory not found|no such directory|unknown command|could not find|is not recognized as)`)

// Aborted reports whether output says the command could not even start.
func Aborted(output string) bool { return abortedRE.MatchString(normalize(output)) }

// Tail keeps the last limit bytes of output on a rune boundary. It is what the
// panel shows for a run whose format could not be read.
func Tail(output string, limit int) string {
	output = normalize(output)
	if limit <= 0 {
		limit = 4000
	}
	if len(output) <= limit {
		return output
	}
	cut := len(output) - limit
	for cut < len(output) && output[cut]&0xC0 == 0x80 {
		cut++
	}
	return output[cut:]
}

// ---------------------------------------------------------------------------
// Shared failure helpers
// ---------------------------------------------------------------------------

// detail is the mutable builder a parser uses while it walks one failure block.
type detail struct {
	failure  Failure
	lines    []string
	maxLines int
}

func (d *detail) addLine(line string) {
	line = strings.TrimRight(line, " ")
	if line == "" || len(d.lines) >= d.maxLines {
		return
	}
	d.lines = append(d.lines, line)
}

func (d *detail) finish() Failure {
	f := d.failure
	if len(d.lines) > 0 {
		f.Message = strings.Join(d.lines, "\n")
	}
	return f
}

func secondsMS(raw string) int64 {
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int64(seconds * 1000)
}

// ---------------------------------------------------------------------------
// go test
// ---------------------------------------------------------------------------

var (
	goMarker = regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+)(?: \(([\d.]+)s\))?\s*$`)
	// `(cached)` is the result text for a cached package: it is still a real
	// `ok`, and treating it as unreadable hid a passing run.
	goPackage  = regexp.MustCompile(`^(ok|FAIL)\s+(\S+)(?:\s+([\d.]+)s|\s+\(cached\))?\s*$`)
	goNoTests  = regexp.MustCompile(`^\?\s+\S+\s+\[no test files\]`)
	goDetail   = regexp.MustCompile(`^([\w./\\-]+\.go):(\d+):\s*(.*)$`)
	goComplete = regexp.MustCompile(`(?m)^(?:ok|FAIL)\s+\S+|(?m)^\?\s+\S+\s+\[no test files\]`)
)

func parseGo(out string) Result {
	res := Result{Runner: string(RunnerGo)}
	var (
		current *detail
		dur     float64
		seenPkg bool
	)
	flush := func() {
		if current == nil {
			return
		}
		res.Failures = append(res.Failures, current.finish())
		current = nil
	}

	for _, line := range strings.Split(out, "\n") {
		if m := goMarker.FindStringSubmatch(line); m != nil {
			flush()
			switch m[1] {
			case "PASS":
				res.Passed++
				res.Total++
			case "FAIL":
				res.Failed++
				res.Total++
				current = &detail{failure: Failure{Name: m[2]}, maxLines: 6}
			case "SKIP":
				res.Skipped++
				res.Total++
			}
			continue
		}
		if m := goPackage.FindStringSubmatch(line); m != nil {
			flush()
			seenPkg = true
			if m[3] != "" {
				if seconds, err := strconv.ParseFloat(m[3], 64); err == nil {
					dur += seconds
				}
			}
			if m[1] == "FAIL" {
				res.FailedRun = true
			}
			continue
		}
		if goNoTests.MatchString(line) {
			flush()
			seenPkg = true
			continue
		}
		if current == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "=== ") {
			continue
		}
		// The package-level verdict is not part of the failure message.
		if trimmed == "FAIL" || trimmed == "PASS" || strings.HasPrefix(trimmed, "exit status ") {
			continue
		}
		if m := goDetail.FindStringSubmatch(trimmed); m != nil {
			if current.failure.File == "" {
				current.failure.File = m[1]
				if n, err := strconv.Atoi(m[2]); err == nil {
					current.failure.Line = n
				}
			}
			current.addLine(m[3])
			continue
		}
		current.addLine(trimmed)
	}
	flush()

	res.DurationMS = int64(dur * 1000)
	res.Recognized = seenPkg || res.Total > 0
	return res
}

// ---------------------------------------------------------------------------
// pytest
// ---------------------------------------------------------------------------

var (
	pytestBanner = regexp.MustCompile(`(?m)^=+\s+(.+?)\s+in\s+([\d.]+)s(?:\s+\([^)]*\))?\s+=+$`)
	pytestShort  = regexp.MustCompile(`(?m)^\s*((?:\d+\s+(?:passed|failed|skipped|error|errors|xfailed|xpassed|deselected)(?:,\s*)?)+)\s+in\s+([\d.]+)s`)
	pytestCount  = regexp.MustCompile(`(\d+)\s+(passed|failed|skipped|error|errors|xfailed|xpassed|deselected)`)
	pytestFailed = regexp.MustCompile(`(?m)^FAILED\s+(\S+)(?:\s+-\s+(.*))?$`)
	pytestFile   = regexp.MustCompile(`^(.+?\.py):(\d+):\s*(.*)$`)
	pytestHeader = regexp.MustCompile(`^_+\s*(.+?)\s*_+$`)
	pytestAssert = regexp.MustCompile(`^E\s+(.*)$`)
	pytestSource = regexp.MustCompile(`^>\s+(.*)$`)
)

func parsePytest(out string) Result {
	res := Result{Runner: string(RunnerPytest)}
	var recognized bool

	// The last summary wins: pytest prints one banner for the whole session,
	// but a retry plugin can print more than one.
	if m := pytestBanner.FindStringSubmatch(out); m != nil {
		applyPytestCounts(&res, m[1])
		res.DurationMS = secondsMS(m[2])
		recognized = true
	}
	for _, m := range pytestShort.FindAllStringSubmatch(out, -1) {
		applyPytestCounts(&res, m[1])
		res.DurationMS = secondsMS(m[2])
		recognized = true
	}

	res.Failures = pytestFailures(out)
	if len(res.Failures) > 0 && !recognized {
		// Failure sections are still a real signal even when the summary line
		// is missing; the run failed, the totals are simply unknown.
		res.Failed = len(res.Failures)
		res.Total = res.Failed
		res.FailedRun = true
		recognized = true
	}
	res.Recognized = recognized
	return res
}

// applyPytestCounts replaces (never adds) the counts from one summary string.
func applyPytestCounts(res *Result, summary string) {
	var passed, failed, errors, skipped, xfailed, xpassed int
	for _, m := range pytestCount.FindAllStringSubmatch(summary, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		switch m[2] {
		case "passed":
			passed = n
		case "failed":
			failed = n
		case "error", "errors":
			errors = n
		case "skipped":
			skipped = n
		case "xfailed":
			xfailed = n
		case "xpassed":
			xpassed = n
		}
	}
	res.Passed = passed
	res.Failed = failed + errors
	res.Skipped = skipped + xfailed + xpassed
	res.Total = res.Passed + res.Failed + res.Skipped
	if res.Failed > 0 {
		res.FailedRun = true
	}
}

func pytestFailures(out string) []Failure {
	// Locate the FAILURES section. pytest may also print ERRORS; both are
	// failures worth showing.
	lines := strings.Split(out, "\n")
	inSection := false
	var section []string
	for _, line := range lines {
		switch {
		case strings.Contains(line, " FAILURES ") || strings.Contains(line, " ERRORS "):
			inSection = true
			continue
		case inSection && strings.Contains(line, " short test summary info "),
			inSection && strings.Contains(line, " warnings summary "):
			inSection = false
		}
		if inSection {
			section = append(section, line)
		}
	}

	var failures []Failure
	var current *detail
	flush := func() {
		if current != nil {
			failures = append(failures, current.finish())
			current = nil
		}
	}
	for _, line := range section {
		if m := pytestHeader.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			flush()
			current = &detail{failure: Failure{Name: strings.TrimSpace(m[1])}, maxLines: 5}
			continue
		}
		if current == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := pytestFile.FindStringSubmatch(trimmed); m != nil {
			current.failure.File = m[1]
			if n, err := strconv.Atoi(m[2]); err == nil {
				current.failure.Line = n
			}
			if m[3] != "" {
				current.addLine(m[3])
			}
			continue
		}
		if m := pytestAssert.FindStringSubmatch(trimmed); m != nil {
			current.addLine(strings.TrimSpace(m[1]))
			continue
		}
		if m := pytestSource.FindStringSubmatch(trimmed); m != nil {
			current.addLine(strings.TrimSpace(m[1]))
		}
	}
	flush()

	if len(failures) > 0 {
		return failures
	}

	// `-q` prints only the short summary. It carries the node id and the
	// assertion text, which is enough for the panel's row.
	for _, m := range pytestFailed.FindAllStringSubmatch(out, -1) {
		nodeID := m[1]
		name := nodeID
		if idx := strings.LastIndex(nodeID, "::"); idx >= 0 {
			name = nodeID[idx+2:]
		}
		f := Failure{Name: name}
		if m[2] != "" {
			f.Message = strings.TrimSpace(m[2])
		}
		if idx := strings.Index(nodeID, "::"); idx > 0 {
			f.File = nodeID[:idx]
		}
		failures = append(failures, f)
	}
	return failures
}

// ---------------------------------------------------------------------------
// jest / vitest / npm test
// ---------------------------------------------------------------------------

var (
	jsTestsLine  = regexp.MustCompile(`(?m)^Tests:\s+(.+?)\s*$`)
	jsTestsBare  = regexp.MustCompile(`(?m)^\s*Tests\s{2,}(.+?)\s*$`)
	jsCount      = regexp.MustCompile(`(\d+)\s+(failed|passed|skipped|todo|pending)`)
	jsTotal      = regexp.MustCompile(`(\d+)\s+total`)
	jsParenTotal = regexp.MustCompile(`\((\d+)\)`)
	jsJestTime   = regexp.MustCompile(`(?m)^Time:\s+([\d.]+)\s*s`)
	jsVitestTime = regexp.MustCompile(`(?m)^\s*Duration\s+([\d.]+)\s*(ms|s)`)
	jsJestHead   = regexp.MustCompile(`(?m)^\s*●\s+(.+?)\s*$`)
	jsVitestFail = regexp.MustCompile(`(?m)^\s*[×✗]\s+(.+?)\s*$`)
	jsAt         = regexp.MustCompile(`\(([^()]+):(\d+):(\d+)\)`)
	jsFileLine   = regexp.MustCompile(`(?:❯|at\s+)(?:file://)?([\w./\\-]+\.(?:js|jsx|ts|tsx|mjs|cjs)):(\d+):(\d+)`)
	jsArrow      = regexp.MustCompile(`^\s*→\s*(.*)$`)
	jsVitestFile = regexp.MustCompile(`(?:[❯✓]|^FAIL)\s+(?:file://)?([\w./\\-]+\.(?:js|jsx|ts|tsx|mjs|cjs))`)
	jsVitestMs   = regexp.MustCompile(`\s+\d+(?:\.\d+)?\s*m?s$`)
	jsComplete   = regexp.MustCompile(`(?m)^Tests:\s+|(?m)^\s*Tests\s{2,}`)
)

func parseJS(out string) Result {
	res := Result{Runner: string(RunnerNPM)}
	var recognized bool

	for _, m := range jsTestsLine.FindAllStringSubmatch(out, -1) {
		applyJSCounts(&res, m[1], jsTotal)
		recognized = true
	}
	for _, m := range jsTestsBare.FindAllStringSubmatch(out, -1) {
		applyJSCounts(&res, m[1], jsParenTotal)
		recognized = true
	}
	if m := jsJestTime.FindStringSubmatch(out); m != nil {
		res.DurationMS = secondsMS(m[1])
	}
	if m := jsVitestTime.FindStringSubmatch(out); m != nil {
		if m[2] == "ms" {
			if ms, err := strconv.ParseFloat(m[1], 64); err == nil {
				res.DurationMS = int64(ms)
			}
		} else {
			res.DurationMS = secondsMS(m[1])
		}
	}

	if jsJestHead.MatchString(out) {
		res.Failures = jsJestFailures(out)
	} else if jsVitestFail.MatchString(out) {
		res.Failures = jsVitestFailures(out)
	}
	res.Recognized = recognized
	return res
}

func applyJSCounts(res *Result, summary string, totalRE *regexp.Regexp) {
	var passed, failed, skipped int
	for _, m := range jsCount.FindAllStringSubmatch(summary, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		switch m[2] {
		case "passed":
			passed = n
		case "failed":
			failed = n
		case "skipped", "todo", "pending":
			skipped += n
		}
	}
	res.Passed, res.Failed, res.Skipped = passed, failed, skipped
	if m := totalRE.FindStringSubmatch(summary); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			res.Total = n
		}
	}
	if res.Total == 0 {
		res.Total = passed + failed + skipped
	}
	if failed > 0 {
		res.FailedRun = true
	}
}

func jsJestFailures(out string) []Failure {
	lines := strings.Split(out, "\n")
	var failures []Failure
	var current *detail
	flush := func() {
		if current != nil {
			failures = append(failures, current.finish())
			current = nil
		}
	}
	for _, line := range lines {
		if m := jsJestHead.FindStringSubmatch(line); m != nil {
			flush()
			current = &detail{failure: Failure{Name: strings.TrimSpace(m[1])}, maxLines: 6}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Tests:") {
			flush()
			continue
		}
		if current == nil {
			continue
		}
		if m := jsAt.FindStringSubmatch(line); m != nil {
			current.failure.File = cleanJSPath(m[1])
			if n, err := strconv.Atoi(m[2]); err == nil {
				current.failure.Line = n
			}
			continue
		}
		if m := jsFileLine.FindStringSubmatch(line); m != nil {
			current.failure.File = cleanJSPath(m[1])
			if n, err := strconv.Atoi(m[2]); err == nil {
				current.failure.Line = n
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		current.addLine(trimmed)
	}
	flush()
	return failures
}

func jsVitestFailures(out string) []Failure {
	lines := strings.Split(out, "\n")
	var failures []Failure
	var current *detail
	currentFile := ""
	flush := func() {
		if current != nil {
			failures = append(failures, current.finish())
			current = nil
		}
	}
	for _, line := range lines {
		if m := jsVitestFile.FindStringSubmatch(line); m != nil {
			// The per-file line precedes its × rows, and the detailed FAIL
			// block repeats it; tracking the nearest one is what makes a
			// failure clickable even when vitest prints no code frame.
			currentFile = cleanJSPath(m[1])
		}
		if m := jsVitestFail.FindStringSubmatch(line); m != nil {
			flush()
			name := jsVitestMs.ReplaceAllString(strings.TrimSpace(m[1]), "")
			current = &detail{failure: Failure{Name: name, File: currentFile}, maxLines: 5}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Tests ") || strings.HasPrefix(strings.TrimSpace(line), "Duration") {
			flush()
			continue
		}
		if current == nil {
			continue
		}
		if m := jsArrow.FindStringSubmatch(line); m != nil {
			current.addLine(strings.TrimSpace(m[1]))
			continue
		}
		if m := jsFileLine.FindStringSubmatch(line); m != nil {
			current.failure.File = cleanJSPath(m[1])
			if n, err := strconv.Atoi(m[2]); err == nil {
				current.failure.Line = n
			}
		}
	}
	flush()
	return failures
}

func cleanJSPath(p string) string {
	p = strings.TrimPrefix(p, "file://")
	p = strings.TrimPrefix(p, "./")
	return p
}

// ---------------------------------------------------------------------------
// cargo test
// ---------------------------------------------------------------------------

var (
	cargoResult   = regexp.MustCompile(`(?m)^test result: (ok|FAILED)\. (\d+) passed; (\d+) failed; (\d+) ignored;`)
	cargoFinish   = regexp.MustCompile(`finished in ([\d.]+)s`)
	cargoBlock    = regexp.MustCompile(`^---- (.+?) stdout ----$`)
	cargoPanicAt  = regexp.MustCompile(`panicked at (.+):(\d+):(\d+)`)
	cargoTestLine = regexp.MustCompile(`^test (\S+) \.\.\. (ok|FAILED|ignored)`)
)

func parseCargo(out string) Result {
	res := Result{Runner: string(RunnerCargo)}
	var recognized bool

	for _, m := range cargoResult.FindAllStringSubmatch(out, -1) {
		recognized = true
		passed, _ := strconv.Atoi(m[2])
		failed, _ := strconv.Atoi(m[3])
		ignored, _ := strconv.Atoi(m[4])
		res.Passed += passed
		res.Failed += failed
		res.Skipped += ignored
		res.Total += passed + failed + ignored
		if m[1] == "FAILED" || failed > 0 {
			res.FailedRun = true
		}
	}
	for _, m := range cargoFinish.FindAllStringSubmatch(out, -1) {
		res.DurationMS += secondsMS(m[1])
	}

	// The per-test lines are the fallback when a summary is missing, but they
	// also make the failure names available.
	lines := strings.Split(out, "\n")
	var current *detail
	flush := func() {
		if current != nil {
			res.Failures = append(res.Failures, current.finish())
			current = nil
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := cargoBlock.FindStringSubmatch(trimmed); m != nil {
			flush()
			current = &detail{failure: Failure{Name: strings.TrimSpace(m[1])}, maxLines: 5}
			continue
		}
		if strings.HasPrefix(trimmed, "test result:") {
			flush()
			continue
		}
		if current != nil {
			if m := cargoPanicAt.FindStringSubmatch(trimmed); m != nil {
				if current.failure.File == "" {
					current.failure.File = m[1]
					if n, err := strconv.Atoi(m[2]); err == nil {
						current.failure.Line = n
					}
				}
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "failures:") {
				current.addLine(trimmed)
			}
		}
	}
	flush()

	if len(res.Failures) == 0 {
		// A panic before any block header still names the test on its line.
		for _, m := range cargoTestLine.FindAllStringSubmatch(out, -1) {
			if m[2] == "FAILED" {
				res.Failures = append(res.Failures, Failure{Name: m[1]})
			}
		}
	}
	res.Recognized = recognized
	return res
}

// ---------------------------------------------------------------------------
// mvn test
// ---------------------------------------------------------------------------

var (
	mavenSummary = regexp.MustCompile(`(?m)^\[(?:INFO|ERROR|WARNING)\]\s+Tests run: (\d+), Failures: (\d+), Errors: (\d+), Skipped: (\d+)`)
	mavenTime    = regexp.MustCompile(`(?m)^\[INFO\] Total time:\s+([\d.]+)\s*s`)
	mavenFailure = regexp.MustCompile(`(?m)^\[ERROR\]\s+(\S+?)\.([\w$]+):(\d+)\s*(.*)$`)
	mavenBuild   = regexp.MustCompile(`(?m)^\[INFO\] BUILD (?:SUCCESS|FAILURE)`)
)

func parseMaven(out string) Result {
	res := Result{Runner: string(RunnerMaven)}
	matches := mavenSummary.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return res
	}
	// The last summary is the reactor total; earlier ones are per module.
	m := matches[len(matches)-1]
	total, _ := strconv.Atoi(m[1])
	failures, _ := strconv.Atoi(m[2])
	errors, _ := strconv.Atoi(m[3])
	skipped, _ := strconv.Atoi(m[4])
	res.Total = total
	res.Failed = failures + errors
	res.Skipped = skipped
	res.Passed = total - res.Failed - res.Skipped
	if res.Passed < 0 {
		res.Passed = 0
	}
	res.FailedRun = res.Failed > 0
	if t := mavenTime.FindStringSubmatch(out); t != nil {
		res.DurationMS = secondsMS(t[1])
	}

	for _, f := range mavenFailure.FindAllStringSubmatch(out, -1) {
		class := f[1]
		simple := class
		if idx := strings.LastIndex(simple, "."); idx >= 0 {
			simple = simple[idx+1:]
		}
		line, _ := strconv.Atoi(f[3])
		res.Failures = append(res.Failures, Failure{
			Name:    simple + "." + f[2],
			File:    simple + ".java",
			Line:    line,
			Message: strings.TrimSpace(f[4]),
		})
	}
	res.Recognized = true
	return res
}

// ---------------------------------------------------------------------------
// dotnet test
// ---------------------------------------------------------------------------

var (
	dotnetSummary = regexp.MustCompile(`(?m)^(Passed|Failed)!\s+-\s+Failed:\s+(\d+), Passed:\s+(\d+), Skipped:\s+(\d+), Total:\s+(\d+), Duration:\s+([^-]+?)\s+-\s+.*$`)
	dotnetFailure = regexp.MustCompile(`(?m)^\s+(?:Failed|X)\s+(.+?)\s+\[`)
	dotnetFile    = regexp.MustCompile(`\bin\s+(.+?):line\s+(\d+)`)
	dotnetErrMsg  = regexp.MustCompile(`^\s*Error Message:\s*$`)
	dotnetStack   = regexp.MustCompile(`^\s*Stack Trace:\s*$`)
)

func parseDotnet(out string) Result {
	res := Result{Runner: string(RunnerDotnet)}
	summaries := dotnetSummary.FindAllStringSubmatch(out, -1)
	if len(summaries) == 0 {
		return res
	}
	for _, m := range summaries {
		failed, _ := strconv.Atoi(m[2])
		passed, _ := strconv.Atoi(m[3])
		skipped, _ := strconv.Atoi(m[4])
		total, _ := strconv.Atoi(m[5])
		res.Failed += failed
		res.Passed += passed
		res.Skipped += skipped
		res.Total += total
		res.DurationMS += dotnetDurationMS(m[6])
		if m[1] == "Failed" || failed > 0 {
			res.FailedRun = true
		}
	}

	lines := strings.Split(out, "\n")
	var current *detail
	inMessage := false
	flush := func() {
		if current != nil {
			res.Failures = append(res.Failures, current.finish())
			current = nil
		}
		inMessage = false
	}
	for _, line := range lines {
		if m := dotnetFailure.FindStringSubmatch(line); m != nil {
			flush()
			current = &detail{failure: Failure{Name: strings.TrimSpace(m[1])}, maxLines: 5}
			continue
		}
		if current == nil {
			continue
		}
		if dotnetErrMsg.MatchString(line) {
			inMessage = true
			continue
		}
		if dotnetStack.MatchString(line) {
			inMessage = false
			continue
		}
		if m := dotnetFile.FindStringSubmatch(line); m != nil {
			current.failure.File = strings.TrimSpace(m[1])
			if n, err := strconv.Atoi(m[2]); err == nil {
				current.failure.Line = n
			}
			continue
		}
		if inMessage {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				current.addLine(trimmed)
			}
		}
	}
	flush()
	res.Recognized = true
	return res
}

// dotnetDurationMS reads the Duration column of `dotnet test`: "12 ms",
// "1.2 s", "1 m 2 s". It is deliberately total: an unknown unit yields zero
// rather than a guess.
func dotnetDurationMS(raw string) int64 {
	re := regexp.MustCompile(`([\d.]+)\s*(ms|s|m|h)`)
	var total float64
	for _, m := range re.FindAllStringSubmatch(strings.ToLower(raw), -1) {
		value, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		switch m[2] {
		case "ms":
			total += value
		case "s":
			total += value * 1000
		case "m":
			total += value * 60_000
		case "h":
			total += value * 3_600_000
		}
	}
	return int64(total)
}

// SortedFailures is a deterministic ordering for tests and snapshots. The
// panel keeps the runner's order; this helper exists so a test can assert on
// a set without depending on map iteration elsewhere.
func SortedFailures(failures []Failure) []Failure {
	out := append([]Failure(nil), failures...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
