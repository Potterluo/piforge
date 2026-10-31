package testrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		command string
		want    Runner
		ok      bool
	}{
		{command: "go test ./...", want: RunnerGo, ok: true},
		{command: "cd backend && go test -count=1 ./...", want: RunnerGo, ok: true},
		{command: "pytest -q", want: RunnerPytest, ok: true},
		{command: "python -m pytest tests/", want: RunnerPytest, ok: true},
		{command: "python3 -m pytest", want: RunnerPytest, ok: true},
		{command: "npm test", want: RunnerNPM, ok: true},
		{command: "npm run test -- --watch=false", want: RunnerNPM, ok: true},
		{command: "pnpm test", want: RunnerNPM, ok: true},
		{command: "npx jest src", want: RunnerJest, ok: true},
		{command: "npx vitest run", want: RunnerVitest, ok: true},
		{command: "npx --yes vitest run", want: RunnerVitest, ok: true},
		{command: "pnpm vitest run", want: RunnerVitest, ok: true},
		{command: "pnpm exec jest", want: RunnerJest, ok: true},
		{command: "node --test", want: RunnerNode, ok: true},
		{command: "node --test test/math.test.js", want: RunnerNode, ok: true},
		{command: "node --experimental-strip-types --test src/", want: RunnerNode, ok: true},
		// The acceptance pass's real invocation: the agent wrote a node:test file
		// and ran it directly. node:test runs it and prints the same TAP summary,
		// but there is no --test flag to see, so the file name is the signal.
		{command: "node primes.test.js", want: RunnerNode, ok: true},
		{command: "node primes.js && node primes.test.js", want: RunnerNode, ok: true},
		{command: "cd app && node ./src/math.spec.mjs", want: RunnerNode, ok: true},
		{command: "node --require ./setup.cjs --env-file .env test/math.test.js", want: RunnerNode, ok: true},
		{command: "node test.js", want: RunnerNode, ok: true},
		// Plain scripts are not test runs, whatever their output later looks like.
		{command: "node primes.js", ok: false},
		{command: "node scripts/check.js", ok: false},
		{command: "node --version", ok: false},
		{command: "node notes.md", ok: false},
		{command: "tsx --test", want: RunnerTSX, ok: true},
		{command: "tsx --test src/math.test.ts", want: RunnerTSX, ok: true},
		{command: "tsx src/math.spec.ts", want: RunnerTSX, ok: true},
		{command: "npx tsx --test", want: RunnerTSX, ok: true},
		{command: "node script.js --test", ok: false},
		{command: "poetry run pytest -q", want: RunnerPytest, ok: true},
		{command: "uv run pytest", want: RunnerPytest, ok: true},
		{command: "CI=1 cargo test --all", want: RunnerCargo, ok: true},
		{command: "make test", want: RunnerMake, ok: true},
		{command: "mvn -q test", want: RunnerMaven, ok: true},
		{command: "./mvnw -pl backend test", want: RunnerMaven, ok: true},
		{command: "dotnet test tests/Foo.csproj", want: RunnerDotnet, ok: true},
		{command: "timeout 30 go test ./...", want: RunnerGo, ok: true},
		{command: "sudo -E env CI=1 pytest", want: RunnerPytest, ok: true},

		{command: "git status", ok: false},
		{command: "echo 'go test ./...'", ok: false},
		{command: "go build ./...", ok: false},
		{command: "make build", ok: false},
		{command: "cargo build", ok: false},
		{command: "mvn package", ok: false},
		{command: "npm install", ok: false},
		{command: "grep -r 'pytest' .", ok: false},
		{command: "", ok: false},
	}
	for _, tc := range tests {
		got, ok := Detect(tc.command)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Detect(%q) = (%q, %v), want (%q, %v)", tc.command, got, ok, tc.want, tc.ok)
		}
	}
}

func TestToolCommand(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want string
		ok   bool
	}{
		{name: "bash command", tool: "bash", args: `{"command":"go test ./..."}`, want: "go test ./...", ok: true},
		{name: "shell cmd", tool: "shell", args: `{"cmd":"pytest"}`, want: "pytest", ok: true},
		{name: "read tool is not a command", tool: "read", args: `{"path":"go test"}`, ok: false},
		{name: "no command", tool: "bash", args: `{"cwd":"/tmp"}`, ok: false},
		{name: "bad json", tool: "bash", args: `{`, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ToolCommand(tc.tool, []byte(tc.args))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ToolCommand(%q, %s) = (%q, %v), want (%q, %v)", tc.tool, tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseGo(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		recognized bool
		total      int
		passed     int
		failed     int
		skipped    int
		durationMS int64
		wantFail   *Failure
	}{
		{
			name: "pass",
			output: "=== RUN   TestAdd\n" +
				"--- PASS: TestAdd (0.01s)\n" +
				"=== RUN   TestSkip\n" +
				"--- SKIP: TestSkip (0.00s)\n" +
				"    calc_test.go:30: not on this platform\n" +
				"PASS\n" +
				"ok  \texample.com/calc\t0.021s\n",
			recognized: true, total: 2, passed: 1, skipped: 1, durationMS: 21,
		},
		{
			name: "fail",
			output: "=== RUN   TestAdd\n" +
				"--- FAIL: TestAdd (0.00s)\n" +
				"    calc_test.go:12: 1 + 2 = 3, want 4\n" +
				"--- FAIL: TestSub (0.00s)\n" +
				"    calc_test.go:21: got 2, want 3\n" +
				"FAIL\n" +
				"exit status 1\n" +
				"FAIL\texample.com/calc\t0.003s\n",
			recognized: true, total: 2, failed: 2, durationMS: 3,
			wantFail: &Failure{Name: "TestAdd", File: "calc_test.go", Line: 12, Message: "1 + 2 = 3, want 4"},
		},
		{
			name: "subtests",
			output: "--- FAIL: TestGroup (0.00s)\n" +
				"    --- FAIL: TestGroup/one (0.00s)\n" +
				"        group_test.go:9: boom\n" +
				"FAIL\tpkg/group\t0.004s\n",
			recognized: true, total: 2, failed: 2, durationMS: 4,
			wantFail: &Failure{Name: "TestGroup", File: "", Line: 0},
		},
		{
			name:       "package with no test files",
			output:     "?   \texample.com/calc\t[no test files]\n",
			recognized: true, total: 0,
		},
		{
			name:       "cached package is still a pass",
			output:     "ok  \texample.com/calc\t(cached)\n",
			recognized: true, total: 0,
		},
		{
			name:       "build failure is not guessed",
			output:     "# example.com/calc\n./calc.go:5:2: undefined: add\nFAIL\texample.com/calc [build failed]\n",
			recognized: false,
		},
		{
			name:       "plain prose is not a result",
			output:     "I ran the tests and they seemed fine.\n",
			recognized: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(string(RunnerGo), tc.output)
			assertSummary(t, got, tc.recognized, tc.total, tc.passed, tc.failed, tc.skipped, tc.durationMS)
			if tc.wantFail != nil {
				if len(got.Failures) == 0 {
					t.Fatalf("no failures parsed")
				}
				first := got.Failures[0]
				if first.Name != tc.wantFail.Name || first.File != tc.wantFail.File || first.Line != tc.wantFail.Line {
					t.Fatalf("first failure = %+v, want name=%q file=%q line=%d",
						first, tc.wantFail.Name, tc.wantFail.File, tc.wantFail.Line)
				}
				if tc.wantFail.Message != "" && !strings.Contains(first.Message, tc.wantFail.Message) {
					t.Fatalf("message = %q, want it to contain %q", first.Message, tc.wantFail.Message)
				}
				if strings.Contains(first.Message, "FAIL") {
					t.Fatalf("the package verdict leaked into the failure message: %q", first.Message)
				}
			}
		})
	}
}

func TestParsePytest(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		recognized bool
		total      int
		passed     int
		failed     int
		skipped    int
		durationMS int64
		wantFail   *Failure
	}{
		{
			name: "pass",
			output: "============================= test session starts ==============================\n" +
				"collected 3 items\n\n" +
				"tests/test_calc.py ..s                                                   [100%]\n\n" +
				"=========================== 2 passed, 1 skipped in 0.21s ========================\n",
			recognized: true, total: 3, passed: 2, skipped: 1, durationMS: 210,
		},
		{
			name: "fail with failures section",
			output: "============================= test session starts ==============================\n" +
				"collected 3 items\n\n" +
				"tests/test_calc.py ..F                                                   [100%]\n\n" +
				"=================================== FAILURES ===================================\n" +
				"__________________________________ test_add ____________________________________\n\n" +
				"    def test_add():\n" +
				">       assert add(1, 2) == 4\n" +
				"E       assert 3 == 4\n" +
				"E        +  where 3 = add(1, 2)\n\n" +
				"tests/test_calc.py:8: AssertionError\n" +
				"=========================== short test summary info ============================\n" +
				"FAILED tests/test_calc.py::test_add - assert 3 == 4\n" +
				"========================= 1 failed, 2 passed in 0.12s =========================\n",
			recognized: true, total: 3, passed: 2, failed: 1, durationMS: 120,
			wantFail: &Failure{Name: "test_add", File: "tests/test_calc.py", Line: 8, Message: "assert 3 == 4"},
		},
		{
			name: "quiet short summary",
			output: ".F                                                                       [100%]\n" +
				"FAILED tests/test_calc.py::test_add - assert 3 == 4\n" +
				"1 failed, 1 passed in 0.03s\n",
			recognized: true, total: 2, passed: 1, failed: 1, durationMS: 30,
			wantFail: &Failure{Name: "test_add", File: "tests/test_calc.py", Message: "assert 3 == 4"},
		},
		{
			name: "unparseable crash before any summary",
			output: "Traceback (most recent call last):\n" +
				"  File \"<string>\", line 1, in <module>\n" +
				"ImportError: cannot import name 'add'\n",
			recognized: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(string(RunnerPytest), tc.output)
			assertSummary(t, got, tc.recognized, tc.total, tc.passed, tc.failed, tc.skipped, tc.durationMS)
			if tc.wantFail != nil {
				if len(got.Failures) == 0 {
					t.Fatalf("no failures parsed")
				}
				first := got.Failures[0]
				if first.Name != tc.wantFail.Name || first.File != tc.wantFail.File || first.Line != tc.wantFail.Line {
					t.Fatalf("first failure = %+v, want %+v", first, *tc.wantFail)
				}
				if tc.wantFail.Message != "" && !strings.Contains(first.Message, tc.wantFail.Message) {
					t.Fatalf("message = %q, want it to contain %q", first.Message, tc.wantFail.Message)
				}
			}
		})
	}
}

func TestParseJest(t *testing.T) {
	output := "PASS src/calc.test.js\n" +
		"FAIL src/format.test.js\n" +
		"  ● format › adds\n\n" +
		"    expect(received).toBe(expected)\n\n" +
		"    Expected: 4\n" +
		"    Received: 3\n\n" +
		"      2 | test('adds', () => {\n" +
		"    > 3 |   expect(add(1, 2)).toBe(4)\n" +
		"        |                    ^\n\n" +
		"      at Object.<anonymous> (src/format.test.js:3:20)\n\n" +
		"Tests:       1 failed, 1 passed, 2 total\n" +
		"Snapshots:   0 total\n" +
		"Time:        0.52 s\n"
	got := Parse(string(RunnerJest), output)
	assertSummary(t, got, true, 2, 1, 1, 0, 520)
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(got.Failures))
	}
	f := got.Failures[0]
	if f.Name != "format › adds" || f.File != "src/format.test.js" || f.Line != 3 {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.Contains(f.Message, "Expected: 4") {
		t.Fatalf("message = %q", f.Message)
	}
}

func TestParseVitest(t *testing.T) {
	output := " ✓ test/calc.test.ts (2 tests) 5ms\n" +
		" ❯ test/format.test.ts (1 test | 1 failed) 8ms\n" +
		"   × adds 3ms\n" +
		"     → expected 3 to be 4\n" +
		" ✓ test/skip.test.ts (1 test | 1 skipped)\n\n" +
		" Test Files  1 failed | 2 passed (3)\n" +
		"      Tests  1 failed | 2 passed | 1 skipped (4)\n" +
		"   Duration  512ms\n"
	got := Parse(string(RunnerVitest), output)
	assertSummary(t, got, true, 4, 2, 1, 1, 512)
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(got.Failures))
	}
	f := got.Failures[0]
	if f.Name != "adds" || !strings.Contains(f.Message, "expected 3 to be 4") {
		t.Fatalf("failure = %+v", f)
	}
	if f.File != "test/format.test.ts" {
		t.Fatalf("file = %q, want the file the × block belongs to", f.File)
	}
}

// vitest's detailed block points at the failing line as " ❯ tests/x.test.js:12:5".
// The space after ❯ used to defeat the pattern, so the panel offered a file with
// no line and the click landed at the top of the file (QA-16 minor).
func TestParseVitestKeepsTheFailingLine(t *testing.T) {
	output := " ❯ tests/math.test.js (1 test | 1 failed) 12ms\n" +
		"   × adds two numbers 4ms\n" +
		"     → expected 2 to be 3 // Object.is equality\n\n" +
		"⎯⎯⎯⎯⎯⎯⎯ Failed Tests 1 ⎯⎯⎯⎯⎯⎯⎯\n\n" +
		" FAIL  tests/math.test.js > adds two numbers\n" +
		"AssertionError: expected 2 to be 3 // Object.is equality\n\n" +
		" ❯ tests/math.test.js:12:5\n" +
		"     10| \n" +
		"     11| test('adds two numbers', () => {\n" +
		"     12|   expect(add(1, 2)).toBe(3)\n" +
		"       |                    ^\n\n" +
		" Test Files  1 failed (1)\n" +
		"      Tests  1 failed (1)\n" +
		"   Duration  210ms\n"
	got := Parse(string(RunnerVitest), output)
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %d, want 1: %+v", len(got.Failures), got.Failures)
	}
	f := got.Failures[0]
	if f.File != "tests/math.test.js" {
		t.Fatalf("file = %q", f.File)
	}
	if f.Line != 12 {
		t.Fatalf("line = %d, want 12 from the ❯ file:line:col pointer", f.Line)
	}
}

func TestParseCargo(t *testing.T) {
	output := "running 3 tests\n" +
		"test calc::tests::passes ... ok\n" +
		"test calc::tests::fails ... FAILED\n" +
		"test calc::tests::skips ... ignored\n\n" +
		"failures:\n\n" +
		"---- calc::tests::fails stdout ----\n" +
		"thread 'calc::tests::fails' panicked at src/lib.rs:10:5:\n" +
		"assertion `left == right` failed\n" +
		"  left: 1\n" +
		" right: 2\n\n" +
		"failures:\n" +
		"    calc::tests::fails\n\n" +
		"test result: FAILED. 1 passed; 1 failed; 1 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"
	got := Parse(string(RunnerCargo), output)
	assertSummary(t, got, true, 3, 1, 1, 1, 0)
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(got.Failures))
	}
	f := got.Failures[0]
	if f.Name != "calc::tests::fails" || f.File != "src/lib.rs" || f.Line != 10 {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.Contains(f.Message, "assertion") {
		t.Fatalf("message = %q", f.Message)
	}
}

func TestParseMaven(t *testing.T) {
	output := "[INFO] Running com.example.AppTest\n" +
		"[ERROR] testAdd(com.example.AppTest)  Time elapsed: 0.01 s  <<< FAILURE!\n" +
		"java.lang.AssertionError: expected:<4> but was:<3>\n" +
		"\tat com.example.AppTest.testAdd(AppTest.java:12)\n" +
		"[INFO] \n" +
		"[ERROR] Failures: \n" +
		"[ERROR]   AppTest.testAdd:12 expected:<4> but was:<3>\n" +
		"[INFO] \n" +
		"[ERROR] Tests run: 3, Failures: 1, Errors: 0, Skipped: 1\n" +
		"[INFO] BUILD FAILURE\n" +
		"[INFO] Total time:  2.345 s\n"
	got := Parse(string(RunnerMaven), output)
	assertSummary(t, got, true, 3, 1, 1, 1, 2345)
	if len(got.Failures) == 0 {
		t.Fatalf("no failures parsed")
	}
	f := got.Failures[0]
	if f.Name != "AppTest.testAdd" || f.File != "AppTest.java" || f.Line != 12 {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.Contains(f.Message, "expected:<4>") {
		t.Fatalf("message = %q", f.Message)
	}
}

func TestParseDotnet(t *testing.T) {
	output := "  Failed TestAdd [12 ms]\n" +
		"  Error Message:\n" +
		"   Assert.Equal() Failure: Values differ\n" +
		"Expected: 4\n" +
		"Actual:   3\n" +
		"  Stack Trace:\n" +
		"     at Calc.Tests.TestAdd() in /src/tests/CalcTests.cs:line 12\n\n" +
		"Failed!  - Failed:     1, Passed:     2, Skipped:     0, Total:     3, Duration: 15 ms - Calc.Tests.dll\n"
	got := Parse(string(RunnerDotnet), output)
	assertSummary(t, got, true, 3, 2, 1, 0, 15)
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(got.Failures))
	}
	f := got.Failures[0]
	if f.Name != "TestAdd" || f.File != "/src/tests/CalcTests.cs" || f.Line != 12 {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.Contains(f.Message, "Expected: 4") {
		t.Fatalf("message = %q", f.Message)
	}
}

// TestParseTAP is the table the drill's fragile point needed: each shape Node's
// built-in runner emits, including the ones that must stay unrecognized rather
// than show an invented zero.
func TestParseTAP(t *testing.T) {
	full := "TAP version 13\n" +
		"# Subtest: adds\n" +
		"ok 1 - adds\n" +
		"  ---\n" +
		"  duration_ms: 0.6\n" +
		"  ...\n" +
		"# Subtest: subtracts\n" +
		"ok 2 - subtracts\n" +
		"  ---\n" +
		"  duration_ms: 0.4\n" +
		"  ...\n" +
		"# Subtest: skips\n" +
		"ok 3 - skips # SKIP not today\n" +
		"  ---\n" +
		"  ...\n" +
		"1..3\n" +
		"# tests 3\n" +
		"# suites 0\n" +
		"# pass 2\n" +
		"# fail 0\n" +
		"# cancelled 0\n" +
		"# skipped 1\n" +
		"# todo 0\n" +
		"# duration_ms 42.75\n"

	failing := "TAP version 13\n" +
		"# Subtest: adds\n" +
		"not ok 1 - adds one and one\n" +
		"  ---\n" +
		"  duration_ms: 0.9\n" +
		"  location: '/repo/math.test.js:12:5'\n" +
		"  failureType: 'testCodeFailure'\n" +
		"  error: '1 == 2'\n" +
		"  ...\n" +
		"1..1\n" +
		"# tests 1\n" +
		"# pass 0\n" +
		"# fail 1\n" +
		"# skipped 0\n" +
		"# duration_ms 12.5\n"

	// TAP v13 plan only: no `#` summary at all. The plan states the total and
	// the top-level result lines state the outcomes.
	planOnly := "TAP version 13\n" +
		"1..2\n" +
		"ok 1 - adds\n" +
		"not ok 2 - subtracts\n" +
		"  ---\n" +
		"  duration_ms: 0.2\n" +
		"  error: '2 != 1'\n" +
		"  ...\n"

	// Only the counts the runner actually printed. total and skipped must stay
	// zero (unknown), not become pass+fail.
	countsOnly := "TAP version 13\n" +
		"# pass 2\n" +
		"# fail 1\n"

	malformed := "TAP version 13\n" +
		"# Subtest: adds\n" +
		"not ok - no test number\n" +
		"# pass nope\n"

	// The exact bytes the deployment's agent produced when it ran
	// `node primes.test.js` (node v22.23.4, boxlite sandbox, piped stdout). The
	// names and numbers are copied verbatim; only the Chinese assertions are
	// shortened to keep the fixture readable. This is the shape the detection
	// change exists for: no `--test` flag, but a TAP summary all the same.
	agentRun := "TAP version 13\n" +
		"# Subtest: firstPrimes(10)\n" +
		"ok 1 - firstPrimes(10)\n" +
		"  ---\n" +
		"  duration_ms: 0.95619\n" +
		"  type: 'test'\n" +
		"  ...\n" +
		"# Subtest: firstPrimes(0)\n" +
		"ok 2 - firstPrimes(0)\n" +
		"  ---\n" +
		"  duration_ms: 0.077841\n" +
		"  type: 'test'\n" +
		"  ...\n" +
		"# Subtest: isPrime recognizes a prime\n" +
		"ok 3 - isPrime recognizes a prime\n" +
		"  ---\n" +
		"  duration_ms: 0.104025\n" +
		"  type: 'test'\n" +
		"  ...\n" +
		"1..3\n" +
		"# tests 3\n" +
		"# suites 0\n" +
		"# pass 3\n" +
		"# fail 0\n" +
		"# cancelled 0\n" +
		"# skipped 0\n" +
		"# todo 0\n" +
		"# duration_ms 6.447074\n"

	tests := []struct {
		name       string
		output     string
		runner     Runner
		recognized bool
		total      int
		passed     int
		failed     int
		skipped    int
		durationMS int64
		failure    *Failure
	}{
		{name: "full summary", output: full, runner: RunnerNode, recognized: true,
			total: 3, passed: 2, failed: 0, skipped: 1, durationMS: 42},
		{name: "failing test with location", output: failing, runner: RunnerNode, recognized: true,
			total: 1, passed: 0, failed: 1, skipped: 0, durationMS: 12,
			failure: &Failure{Name: "adds one and one", File: "/repo/math.test.js", Line: 12, Message: "1 == 2"}},
		{name: "plan only", output: planOnly, runner: RunnerNode, recognized: true,
			total: 2, passed: 1, failed: 1, skipped: 0, durationMS: 0,
			failure: &Failure{Name: "subtracts", Message: "2 != 1"}},
		{name: "counts only keep total unknown", output: countsOnly, runner: RunnerNode, recognized: true,
			total: 0, passed: 2, failed: 1, skipped: 0, durationMS: 0},
		{name: "malformed", output: malformed, runner: RunnerNode, recognized: false},
		// A `node x.test.js` run: detected without `--test`, parsed by the same
		// TAP summary. This is the acceptance pass's empty-panel case.
		{name: "agent ran the test file directly", output: agentRun, runner: RunnerNode, recognized: true,
			total: 3, passed: 3, failed: 0, skipped: 0, durationMS: 6},
		{name: "tsx reads the same tap", output: failing, runner: RunnerTSX, recognized: true,
			total: 1, passed: 0, failed: 1, skipped: 0, durationMS: 12,
			failure: &Failure{Name: "adds one and one", File: "/repo/math.test.js", Line: 12, Message: "1 == 2"}},
		// The drill's recorded case: the command is `npm test`, but the script
		// underneath is node --test. The row keeps the npm runner label and now
		// carries the TAP counts instead of 未识别格式.
		{name: "npm wrapper around node tap", output: "> primes@1.0.0 test\n> node --test\n\n" + full,
			runner: RunnerNPM, recognized: true,
			total: 3, passed: 2, failed: 0, skipped: 1, durationMS: 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(string(tc.runner), tc.output)
			assertSummary(t, got, tc.recognized, tc.total, tc.passed, tc.failed, tc.skipped, tc.durationMS)
			if !tc.recognized {
				if len(got.Failures) != 0 {
					t.Fatalf("unrecognised TAP kept failures: %+v", got.Failures)
				}
				return
			}
			if tc.failure == nil {
				if len(got.Failures) != 0 {
					t.Fatalf("failures = %+v, want none", got.Failures)
				}
				return
			}
			if len(got.Failures) != 1 {
				t.Fatalf("failures = %+v, want one", got.Failures)
			}
			if got.Failures[0] != *tc.failure {
				t.Fatalf("failure = %+v, want %+v", got.Failures[0], *tc.failure)
			}
		})
	}
}

// TestParseRealNodeTestRun is the "not a fixture" half: it runs the installed
// node against a throwaway file and parses the bytes the runner really printed.
// Node 24's default reporter is spec, so the TAP reporter is requested
// explicitly; it is still the same node:test run and the same TAP the container
// emits by default on Node 22.
func TestParseRealNodeTestRun(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	passing := "const test = require('node:test');\n" +
		"const assert = require('node:assert');\n" +
		"test('adds', () => { assert.equal(1 + 1, 2); });\n"
	failing := "const test = require('node:test');\n" +
		"const assert = require('node:assert');\n" +
		"test('subtracts', () => { assert.equal(2 - 1, 2); });\n"
	if err := os.WriteFile(filepath.Join(dir, "pass.test.js"), []byte(passing), 0o644); err != nil {
		t.Fatalf("write passing test: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fail.test.js"), []byte(failing), 0o644); err != nil {
		t.Fatalf("write failing test: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "--test-reporter=tap")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	// A failing test makes node exit non-zero; the output is the evidence, not
	// the exit code.
	if ctx.Err() != nil {
		t.Fatalf("node --test timed out: %v\n%s", ctx.Err(), out)
	}
	_ = err

	got := Parse(string(RunnerNode), string(out))
	if !got.Recognized {
		t.Fatalf("real node --test output was unrecognized:\n%s", out)
	}
	if got.Total != 2 || got.Passed != 1 || got.Failed != 1 {
		t.Fatalf("real node --test counts = total:%d passed:%d failed:%d\n%s",
			got.Total, got.Passed, got.Failed, out)
	}
	if len(got.Failures) != 1 || got.Failures[0].Name != "subtracts" {
		t.Fatalf("real node --test failures = %+v\n%s", got.Failures, out)
	}
	if got.Failures[0].Line != 3 || !strings.HasSuffix(got.Failures[0].File, "fail.test.js") {
		t.Fatalf("real node --test failure location = %s:%d\n%s",
			got.Failures[0].File, got.Failures[0].Line, out)
	}

	// The acceptance pass's agent did not pass `--test`: it ran the file
	// directly. Node 24 prints the spec reporter for that (TAP on older nodes,
	// when stdout is a pipe), and both shapes must be recorded with counts
	// instead of 未识别格式.
	for _, tc := range []struct {
		file           string
		passed, failed int
	}{
		{"pass.test.js", 1, 0},
		{"fail.test.js", 0, 1},
	} {
		direct := exec.CommandContext(ctx, node, tc.file)
		direct.Dir = dir
		raw, _ := direct.CombinedOutput()
		got := Parse(string(RunnerNode), string(raw))
		if !got.Recognized || got.Total != 1 || got.Passed != tc.passed || got.Failed != tc.failed {
			t.Fatalf("direct `node %s` = recognized:%v total:%d passed:%d failed:%d\n%s",
				tc.file, got.Recognized, got.Total, got.Passed, got.Failed, raw)
		}
	}
}

func TestParseMakeWrapsTheUnderlyingRunner(t *testing.T) {
	output := "go test ./...\n" +
		"--- PASS: TestAdd (0.01s)\n" +
		"ok  \texample.com/calc\t0.012s\n"
	got := Parse(string(RunnerMake), output)
	assertSummary(t, got, true, 1, 1, 0, 0, 12)
	if got.Runner != string(RunnerMake) {
		t.Fatalf("runner = %q, want %q", got.Runner, RunnerMake)
	}
}

func TestParseUnrecognisedKeepsNoCounts(t *testing.T) {
	output := "make: *** [Makefile:2: test] Error 1\n"
	got := Parse(string(RunnerMake), output)
	if got.Recognized {
		t.Fatalf("recognised output that carries no summary: %+v", got)
	}
	if got.Total != 0 || got.Passed != 0 || got.Failed != 0 || got.Skipped != 0 || len(got.Failures) != 0 {
		t.Fatalf("unrecognised run invented numbers: %+v", got)
	}
}

func TestComplete(t *testing.T) {
	tests := []struct {
		runner Runner
		output string
		want   bool
	}{
		{RunnerGo, "--- PASS: TestAdd (0.00s)\n", false},
		{RunnerGo, "--- PASS: TestAdd (0.00s)\nPASS\nok  \tpkg\t0.1s\n", true},
		{RunnerPytest, "FAILED tests/test_x.py::test_a - boom\n", false},
		{RunnerPytest, "1 failed, 2 passed in 0.12s\n", true},
		{RunnerJest, "Tests:       1 failed, 1 passed, 2 total\n", true},
		{RunnerVitest, "      Tests  2 passed (2)\n", true},
		{RunnerNode, "# pass 2\n", true},
		{RunnerNode, "1..2\n", false}, // a plan alone is not a finished run
		{RunnerNode, "ℹ pass 2\n", true},
		{RunnerNode, "ℹ fail 0\n", true},
		{RunnerTSX, "# fail 0\n", true},
		{RunnerTSX, "ℹ tests 2\n", true},
		{RunnerNPM, "ℹ pass 2\nℹ fail 0\n", true},
		{RunnerCargo, "test result: ok. 2 passed; 0 failed; 0 ignored; finished in 0.01s\n", true},
		{RunnerMaven, "[INFO] BUILD SUCCESS\n", true},
		{RunnerDotnet, "Passed!  - Failed: 0, Passed: 3, Skipped: 0, Total: 3, Duration: 8 ms - A.dll\n", true},
		{RunnerMake, "[INFO] BUILD FAILURE\n", true},
	}
	for _, tc := range tests {
		if got := Complete(string(tc.runner), tc.output); got != tc.want {
			t.Errorf("Complete(%q, %q) = %v, want %v", tc.runner, tc.output, got, tc.want)
		}
	}
}

func TestAborted(t *testing.T) {
	if !Aborted("go: no Go files in /tmp/x") {
		t.Fatal("a missing package should count as aborted")
	}
	if !Aborted("stat ./nope: directory not found") {
		t.Fatal("a missing directory should count as aborted")
	}
	if Aborted("--- FAIL: TestAdd (0.00s)") {
		t.Fatal("a real failure is not an aborted command")
	}
}

func TestTail(t *testing.T) {
	long := strings.Repeat("a", 5000) + "TAIL"
	got := Tail(long, 100)
	if len(got) > 100 || !strings.HasSuffix(got, "TAIL") {
		t.Fatalf("tail = %d bytes, suffix %q", len(got), got[len(got)-4:])
	}
}

func assertSummary(t *testing.T, got Result, recognized bool, total, passed, failed, skipped int, durationMS int64) {
	t.Helper()
	if got.Recognized != recognized {
		t.Fatalf("recognized = %v, want %v (result %+v)", got.Recognized, recognized, got)
	}
	if got.Total != total || got.Passed != passed || got.Failed != failed || got.Skipped != skipped {
		t.Fatalf("counts = total:%d passed:%d failed:%d skipped:%d, want total:%d passed:%d failed:%d skipped:%d",
			got.Total, got.Passed, got.Failed, got.Skipped, total, passed, failed, skipped)
	}
	if got.DurationMS != durationMS {
		t.Fatalf("duration = %dms, want %dms", got.DurationMS, durationMS)
	}
}
