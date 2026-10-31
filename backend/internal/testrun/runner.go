// Package testrun turns a shell command plus its console output into a test
// result the UI can read: how many tests ran, how many passed, and what failed.
//
// It exists because the product's promise is that an agent edits code, and the
// only thing a user wants to know after that is whether the tests pass. The
// transcript already carries the command and its output (tool.started /
// tool.ended), so this package is the missing interpreter — not a second way to
// run tests.
//
// # Deliberate limits
//
// Detection is by command shape, matching the runners the PRD names: go test,
// pytest, npm test/jest/vitest, node --test, cargo test, make test, mvn test,
// dotnet test. Node's runner has two shapes and both count: the `--test` flag,
// and running a file that matches the naming convention `node --test` itself
// discovers (`*.test.js`, `*-test.js`, `test.js`, ...). A script outside that
// convention — `node check.js`, `./run-tests.sh` — is not recognised at all,
// which is better than recording it and guessing.
//
// Parsing is by the runner's own summary lines, never by prose. When a
// recognised command produces output this package cannot read — a compiler
// error, a runner that was interrupted before its summary — Recognized is
// false and the caller records the run as 未识别格式 with the raw tail. It never
// invents counts.
package testrun

import (
	"encoding/json"
	"path"
	"strings"
)

// Runner is the canonical name of a recognised test runner. It is stored on a
// run and shown in the panel, so it is the human label rather than an enum
// value: "go test", "pytest", "cargo test".
type Runner string

// The runner families the product recognises. A command from any other runner
// (gradle, tox, bazel, ...) is not recorded, so the panel never shows a number
// it cannot justify.
const (
	RunnerGo     Runner = "go test"
	RunnerPytest Runner = "pytest"
	RunnerNPM    Runner = "npm test"
	RunnerJest   Runner = "jest"
	RunnerVitest Runner = "vitest"
	// RunnerNode is Node's built-in test runner (`node --test`). Its console
	// output is TAP, not a jest/vitest summary, so it has its own parser.
	RunnerNode Runner = "node --test"
	// RunnerTSX is the TypeScript runner (`tsx --test`), which prints the same
	// TAP because it runs the same node:test harness.
	RunnerTSX    Runner = "tsx --test"
	RunnerCargo  Runner = "cargo test"
	RunnerMake   Runner = "make test"
	RunnerMaven  Runner = "mvn test"
	RunnerDotnet Runner = "dotnet test"
)

// Failure is one failing test. File/Line are the assertion site when the runner
// reports one; they are empty/zero when it does not, and the panel then shows a
// name and message only.
type Failure struct {
	Name    string `json:"name"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message,omitempty"`
}

// Result is the parsed shape of one run.
//
// FailedRun is the honest answer for output that says "the package failed" but
// carries no per-test markers (a panic, a compile error printed after a summary
// line). Counts stay zero in that case; the panel shows the failure state
// without inventing a denominator.
type Result struct {
	Runner     string    `json:"runner"`
	Recognized bool      `json:"recognized"`
	Total      int       `json:"total"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	DurationMS int64     `json:"duration_ms"`
	Failures   []Failure `json:"failures"`
	FailedRun  bool      `json:"-"`
}

// MaxFailures caps how many failing tests are kept for one run. A red suite
// with thousands of failures is a wall of text in a 300px panel; the first page
// is what a user acts on.
const MaxFailures = 50

// Detect reports whether command runs one of the recognised test runners, and
// which one. It looks at every segment of a compound command, so
// `cd backend && go test ./...` is recognised, and it ignores the wrappers
// people put in front of commands (env assignments, sudo, timeout, npx).
func Detect(command string) (Runner, bool) {
	for _, segment := range splitSegments(cleanCommand(command)) {
		if runner, ok := detectSegment(segment); ok {
			return runner, true
		}
	}
	return "", false
}

// ToolCommand extracts the shell command a tool call carried.
//
// Only command-executing tools are considered. A `read` or `write` call whose
// arguments happen to contain the word "go test" is not a test run, and
// treating it as one would put a fabricated row in the panel.
func ToolCommand(toolName string, args json.RawMessage) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch name {
	case "bash", "shell", "sh", "exec", "run_command", "run", "terminal", "command":
	default:
		// MCP and extension tools are named differently; only a name that
		// actually denotes a shell is accepted.
		if !strings.Contains(name, "bash") && !strings.Contains(name, "shell") {
			return "", false
		}
	}
	if len(args) == 0 {
		return "", false
	}
	var body map[string]any
	if err := json.Unmarshal(args, &body); err != nil {
		return "", false
	}
	for _, key := range []string{"command", "cmd", "script"} {
		value, ok := body[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}

// cleanCommand normalises the things a shell prompt or a multi-line command
// adds: ANSI colours, a leading `$`, backslash continuations.
func cleanCommand(command string) string {
	command = stripANSI(command)
	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\r", "\n")
	command = strings.ReplaceAll(command, "\\\n", " ")
	lines := strings.Split(command, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "$ ")
		trimmed = strings.TrimPrefix(trimmed, "> ")
		lines[i] = trimmed
	}
	return strings.Join(lines, "\n")
}

// splitSegments splits a compound command into its simple commands, respecting
// quotes so that `echo "a; b"` stays one segment. The split is best-effort: it
// only has to be good enough to find the executable at the start of a segment.
func splitSegments(command string) []string {
	var (
		out     []string
		current strings.Builder
		quote   rune
		escaped bool
	)
	flush := func() {
		if seg := strings.TrimSpace(current.String()); seg != "" {
			out = append(out, seg)
		}
		current.Reset()
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			current.WriteRune(r)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			current.WriteRune(r)
		case ';', '|', '&', '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// detectSegment looks at the executable at the start of one simple command,
// after dropping the wrappers that do not change which program runs.
func detectSegment(segment string) (Runner, bool) {
	fields := strings.Fields(segment)
	i := 0
	for i < len(fields) {
		field := fields[i]
		token := strings.Trim(field, `"'`)
		if isEnvAssignment(token) {
			i++
			continue
		}
		switch path.Base(token) {
		case "sudo", "command", "nohup", "exec", "time", "nice":
			i++
			for i < len(fields) && strings.HasPrefix(fields[i], "-") {
				i++
			}
			continue
		case "env":
			i++
			for i < len(fields) && (strings.HasPrefix(fields[i], "-") || isEnvAssignment(fields[i])) {
				i++
			}
			continue
		case "timeout":
			i++
			if i < len(fields) && !strings.HasPrefix(fields[i], "-") {
				i++ // the duration
			}
			for i < len(fields) && strings.HasPrefix(fields[i], "-") {
				i++
			}
			continue
		case "poetry", "uv", "pipenv", "pdm", "hatch", "rye":
			// `poetry run pytest` / `uv run pytest`: the tool manages the
			// environment, the next words still name the runner.
			i++
			if i < len(fields) && strings.Trim(fields[i], `"'`) == "run" {
				i++
			}
			continue
		}
		break
	}
	if i >= len(fields) {
		return "", false
	}

	program := path.Base(strings.Trim(fields[i], `"'`))
	rest := fields[i+1:]
	first := ""
	if len(rest) > 0 {
		first = strings.Trim(rest[0], `"'`)
	}

	switch program {
	case "go":
		if first == "test" {
			return RunnerGo, true
		}
	case "pytest", "py.test":
		return RunnerPytest, true
	}
	if program == "python" || strings.HasPrefix(program, "python3") {
		if first == "-m" && len(rest) > 1 && strings.Trim(rest[1], `"'`) == "pytest" {
			return RunnerPytest, true
		}
	}
	switch program {
	case "npm", "pnpm", "yarn", "bun":
		if first == "test" || (first == "run" && len(rest) > 1 && strings.HasPrefix(strings.Trim(rest[1], `"'`), "test")) {
			return RunnerNPM, true
		}
		// A package manager can also run the binary directly (`pnpm vitest`,
		// `pnpm exec jest`), which is how most of these projects run tests.
		if target := managerTarget(rest); target == "jest" {
			return RunnerJest, true
		} else if target == "vitest" {
			return RunnerVitest, true
		}
	case "npx", "pnpx", "bunx":
		if target := managerTarget(rest); target == "jest" {
			return RunnerJest, true
		} else if target == "vitest" {
			return RunnerVitest, true
		} else if target == "tsx" {
			return RunnerTSX, true
		}
	case "jest":
		return RunnerJest, true
	case "vitest":
		return RunnerVitest, true
	case "node", "nodejs":
		// `node --test`, `node --test path/to/x.test.js`, and the flag after
		// other node options. A script that merely takes --test is not a test
		// run: the flag has to appear before the first positional argument.
		if hasTestFlag(rest) {
			return RunnerNode, true
		}
		// `node primes.test.js` is the other way node:test runs: the file
		// registers its tests and the harness executes them in-process. Node's
		// own discovery is by exactly this naming convention, so a file that
		// matches it *is* a test file rather than a script that happens to
		// print test-looking output.
		if nodeTestScript(rest) {
			return RunnerNode, true
		}
	case "tsx":
		if hasTestFlag(rest) || nodeTestScript(rest) {
			return RunnerTSX, true
		}
	case "cargo":
		if first == "test" {
			return RunnerCargo, true
		}
	case "make":
		if first == "test" {
			return RunnerMake, true
		}
	case "mvn", "mvnw":
		if first == "test" || containsGoal(rest, "test") {
			return RunnerMaven, true
		}
	case "dotnet":
		if first == "test" {
			return RunnerDotnet, true
		}
	}
	return "", false
}

// hasTestFlag reports whether a node/tsx argv selects the built-in test runner.
//
// The scan stops at the first positional argument: `node script.js --test` is a
// script that happens to take that flag, while `node --test script.js` is the
// test runner. Node options that take a value (`--require x`) would otherwise
// make `x` look positional, so the loop also skips a flag's obvious value — but
// only when the next token is not itself the --test flag, which is what keeps
// `node --require ./setup.cjs --test` detected.
func hasTestFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := strings.Trim(args[i], `"'`)
		if arg == "--test" || strings.HasPrefix(arg, "--test=") {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			// First positional argument: everything after it belongs to the
			// program, not to node.
			return false
		}
		if i+1 < len(args) && !strings.HasPrefix(strings.Trim(args[i+1], `"'`), "-") {
			i++ // skip a flag's value
		}
	}
	return false
}

// nodeValueFlags are the node/tsx options that consume the next token. Without
// them a value like `./setup.cjs` would be mistaken for the script being run,
// and the real script after it would never be looked at.
var nodeValueFlags = map[string]bool{
	"-r": true, "--require": true, "--import": true, "--loader": true,
	"--experimental-loader": true, "--env-file": true, "--test-reporter": true,
	"--test-reporter-destination": true, "--test-name-pattern": true,
	"--test-skip-pattern": true, "--test-timeout": true, "--test-concurrency": true,
}

// nodeTestScript reports whether the argv names a test file that Node's own
// runner would have discovered.
//
// The acceptance pass watched an agent run `node primes.test.js`: node:test
// executed the file and printed the same TAP summary `node --test` prints, but
// the panel stayed empty because the command did not carry the `--test` flag.
// A file named `*.test.js` is a test file by the runner's own convention (that
// is the glob `node --test` uses), so recognising it is not a guess about what
// the script does.
func nodeTestScript(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := strings.Trim(args[i], `"'`)
		if strings.HasPrefix(arg, "-") {
			// A flag whose value is the next token must not be read as the script.
			// `--test` itself is handled before this function is called.
			if nodeValueFlags[arg] && i+1 < len(args) {
				i++
			}
			continue
		}
		// The first positional argument is the program node runs. Anything
		// after it is an argument to that program.
		return isNodeTestFile(arg)
	}
	return false
}

// isNodeTestFile mirrors the name patterns node --test uses to find tests:
// `*.test.*`, `*.spec.*`, `*-test.*`, `*_test.*`, `test-*.*` and `test.*`.
// Only script extensions are considered, so `node notes.md` stays a non-test.
func isNodeTestFile(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".mts", ".cts":
	default:
		return false
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	switch {
	case stem == "test", stem == "spec",
		strings.HasSuffix(stem, ".test"), strings.HasSuffix(stem, ".spec"),
		strings.HasSuffix(stem, "-test"), strings.HasSuffix(stem, "-spec"),
		strings.HasSuffix(stem, "_test"), strings.HasSuffix(stem, "_spec"),
		strings.HasPrefix(stem, "test-"), strings.HasPrefix(stem, "spec-"):
		return true
	}
	return false
}

// managerTarget finds the program a package-manager invocation runs, skipping
// flags and an `exec`/`dlx` verb: `npx --yes vitest run` and
// `pnpm exec jest --watch` both name the runner next.
func managerTarget(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.Trim(args[i], `"'`)
		switch arg {
		case "exec", "dlx", "x", "run-script":
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

// containsGoal reports whether a Maven goal appears before any other lifecycle
// goal. It is how `mvn -q test` and `mvn -pl backend test` are recognised
// without guessing at flags: a token that names another lifecycle phase
// (install, package, verify, ...) wins, because then the command is not a test
// run first.
func containsGoal(args []string, goal string) bool {
	other := map[string]bool{
		"install": true, "package": true, "verify": true, "deploy": true,
		"compile": true, "clean": true, "site": true,
	}
	for i := 0; i < len(args) && i < 8; i++ {
		arg := strings.Trim(args[i], `"'`)
		if arg == goal {
			return true
		}
		if other[arg] {
			return false
		}
	}
	return false
}

// isEnvAssignment reports whether a token is `KEY=value` (possibly prefixed by
// `export`), which is a wrapper rather than the program being run.
func isEnvAssignment(token string) bool {
	token = strings.TrimPrefix(token, "export ")
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := token[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
