package pi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// buildFakePeer compiles cmd/fakepi into a temp dir. This is the only part of
// the suite that needs a Go toolchain; without one it skips rather than fails,
// so the tests stay runnable in a bare environment.
func buildFakePeer(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH; skipping the subprocess test")
	}
	name := "fakepi"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goTool, "build", "-o", out,
		"github.com/piforge/piforge/backend/cmd/fakepi")
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build cmd/fakepi (the tree may be mid-edit): %v\n%s", err, buildOut)
	}
	return out
}

// TestSubprocessEndToEnd is the only test that leaves the process: it builds
// cmd/fakepi, drives it through the same Runtime that cmd/piforged will use,
// then restarts the session and checks it resumed from its JSONL file.
func TestSubprocessEndToEnd(t *testing.T) {
	binary := buildFakePeer(t)
	worktree := t.TempDir()

	newRuntime := func() *Runtime {
		return NewRuntime(RuntimeConfig{Config: Config{
			BinaryPath:   binary,
			Args:         []string{"--script=ok", "--delay=0"},
			Dir:          worktree,
			CloseTimeout: 5 * time.Second,
		}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const sessionID = "ses_e2e"
	spec := contracts.SessionSpec{
		SessionID: sessionID,
		Kind:      contracts.KindMain,
		Worktree:  worktree,
	}

	first := newRuntime()
	ch, err := first.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A real child process must really be running.
	status, err := first.Status(ctx, sessionID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.ProcessID == 0 {
		t.Fatalf("Status reported no process id for a spawned session: %+v", status)
	}

	res, err := first.Prompt(ctx, contracts.PromptRequest{SessionID: sessionID, Text: "run the cache tests"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.Disposition != "started" || res.RunID == "" {
		t.Fatalf("PromptResult = %+v", res)
	}

	events := collectUntil(t, ch, settledMatcher)
	assertSubsequence(t, events,
		contracts.EvRunStarted,
		contracts.EvMsgDelta,
		contracts.EvToolStarted,
		contracts.EvToolProgress,
		contracts.EvToolEnded,
		contracts.EvMsgFinal,
		contracts.EvRunSettled,
	)
	assertSeqContiguous(t, events)

	final, ok := lastFinalOfRole(t, events, "assistant")
	if !ok || !strings.Contains(final.Text, "Tests pass") {
		t.Fatalf("final assistant text = %q", final.Text)
	}

	// The child was told which session file it owns, so the transcript exists
	// and contains the prompt.
	if _, err := os.Stat(status.SessionFile); err != nil {
		t.Fatalf("session file %s was not written: %v", status.SessionFile, err)
	}
	body, err := os.ReadFile(status.SessionFile)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	if !strings.Contains(string(body), "run the cache tests") {
		t.Errorf("session file does not contain the user message:\n%s", body)
	}

	treeBefore, err := first.Tree(ctx, sessionID)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	beforeCount := countEntries(treeBefore)

	// Stop must actually end the process.
	if err := first.Stop(ctx, sessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := first.Status(ctx, sessionID); err == nil {
		t.Fatal("Status after Stop should report not found")
	}

	// A restart resumes the same transcript instead of starting empty, which is
	// what "come back tomorrow and continue" depends on.
	second := newRuntime()
	defer func() { _ = second.Close() }()
	if _, err := second.Start(ctx, spec); err != nil {
		t.Fatalf("restart Start: %v", err)
	}
	treeAfter, err := second.Tree(ctx, sessionID)
	if err != nil {
		t.Fatalf("Tree after restart: %v", err)
	}
	if got := countEntries(treeAfter); got != beforeCount {
		t.Fatalf("restored tree has %d entries, want the original %d", got, beforeCount)
	}
}
