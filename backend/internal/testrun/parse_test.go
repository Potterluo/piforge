package testrun

import (
	"strings"
	"testing"
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
