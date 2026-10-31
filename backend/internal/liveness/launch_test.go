package liveness

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLivenessHelperAgent is not a test: it is the child process the launcher
// tests run. `go test` runs it in the normal suite too, where the env var is
// absent and it skips immediately.
func TestLivenessHelperAgent(t *testing.T) {
	mode := os.Getenv("PIFORGE_LIVENESS_HELPER")
	switch mode {
	case "":
		t.Skip("helper process for the launcher tests")
	case "beat":
		time.Sleep(700 * time.Millisecond)
	case "exit3":
		os.Exit(3)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func quietSupervisor(t *testing.T, root string) SupervisorConfig {
	t.Helper()
	return SupervisorConfig{
		Root:      root,
		SessionID: "ses_agent",
		Interval:  25 * time.Millisecond,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Stdin:     strings.NewReader(""),
		Stdout:    io.Discard,
		Stderr:    io.Discard,
	}
}

func helperArgv() []string {
	return []string{os.Args[0], "-test.run=^TestLivenessHelperAgent$"}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSupervisorWritesAndRefreshesTheMarker is the writer half of the contract:
// while the agent process runs, its marker exists and keeps moving; when the
// agent exits, the marker is gone so the very next probe answers dead.
func TestSupervisorWritesAndRefreshesTheMarker(t *testing.T) {
	t.Setenv("PIFORGE_LIVENESS_HELPER", "beat")
	root := t.TempDir()
	cfg := quietSupervisor(t, root)
	marker, err := MarkerPath(root, cfg.SessionID)
	if err != nil {
		t.Fatalf("MarkerPath: %v", err)
	}

	type outcome struct {
		code int
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		code, serr := Supervise(context.Background(), cfg, helperArgv())
		done <- outcome{code: code, err: serr}
	}()

	waitFor(t, "the marker to appear", func() bool {
		_, statErr := os.Stat(marker)
		return statErr == nil
	})
	first, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	waitFor(t, "the marker to be refreshed", func() bool {
		info, statErr := os.Stat(marker)
		return statErr == nil && info.ModTime().After(first.ModTime())
	})
	// The content names the agent, which is what an operator reads during an
	// incident.
	payload, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !strings.Contains(string(payload), "pid=") {
		t.Fatalf("marker content = %q", string(payload))
	}

	result := <-done
	if result.err != nil {
		t.Fatalf("Supervise: %v", result.err)
	}
	if result.code != 0 {
		t.Fatalf("agent exit code = %d, want 0", result.code)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker survived the agent: %v", err)
	}
}

// TestSupervisorPropagatesTheAgentExitCode keeps the launcher transparent: the
// bridge's notion of "the agent exited with N" must not become "the launcher
// failed".
func TestSupervisorPropagatesTheAgentExitCode(t *testing.T) {
	t.Setenv("PIFORGE_LIVENESS_HELPER", "exit3")
	root := t.TempDir()
	cfg := quietSupervisor(t, root)
	code, err := Supervise(context.Background(), cfg, helperArgv())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	marker, _ := MarkerPath(root, cfg.SessionID)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker survived a failed agent: %v", err)
	}
}

// TestSupervisorRemovesTheMarkerWhenTheAgentIsKilled is the crash path: a
// killed agent must not leave a fresh-looking heartbeat behind.
func TestSupervisorRemovesTheMarkerWhenTheAgentIsKilled(t *testing.T) {
	t.Setenv("PIFORGE_LIVENESS_HELPER", "beat")
	root := t.TempDir()
	cfg := quietSupervisor(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Supervise(ctx, cfg, helperArgv())
		done <- err
	}()
	marker, _ := MarkerPath(root, cfg.SessionID)
	waitFor(t, "the marker to appear", func() bool {
		_, statErr := os.Stat(marker)
		return statErr == nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the launcher did not return after cancellation")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a cancelled agent left its marker behind: %v", err)
	}
}

// TestMaybeLaunchIsTheSameLauncherEndToEnd covers the argv contract the control
// plane relies on: `piforged agent-launcher -root … -session … -- <agent>`
// parses, runs the agent, and owns the marker. The argv is built here by hand
// because that is exactly what cmd/piforged/agent_sandbox.go builds inside the
// sandbox since stage 4 — there is no second builder to drift from it.
func TestMaybeLaunchIsTheSameLauncherEndToEnd(t *testing.T) {
	t.Setenv("PIFORGE_LIVENESS_HELPER", "beat")
	root := t.TempDir()
	cfg := quietSupervisor(t, root)
	agent := helperArgv()
	argv := append([]string{
		os.Args[0], LauncherCommand,
		"-root", cfg.Root,
		"-session", cfg.SessionID,
		"-interval", cfg.Interval.String(),
		"--",
	}, agent...)
	if argv[1] != LauncherCommand {
		t.Fatalf("argv = %v", argv)
	}
	marker, _ := MarkerPath(root, cfg.SessionID)
	done := make(chan struct{})
	code := -1
	var handled bool
	go func() {
		code, handled = MaybeLaunch(context.Background(), argv)
		close(done)
	}()
	waitFor(t, "the marker to appear", func() bool {
		_, statErr := os.Stat(marker)
		return statErr == nil
	})
	<-done
	if !handled {
		t.Fatal("MaybeLaunch did not claim the launcher argv")
	}
	if code != 0 {
		t.Fatalf("launcher exit code = %d, want 0", code)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker survived: %v", err)
	}
}

// TestSuperviseRefusesIncompleteConfiguration: a launcher without a root or a
// session cannot write a marker, and starting one anyway would produce an agent
// the probe cannot see. Since stage 4 the argv is the only way in, so this drives
// Supervise directly (the same validation MaybeLaunch reaches through it).
func TestSuperviseRefusesIncompleteConfiguration(t *testing.T) {
	agent := []string{"pi", "--mode", "rpc"}
	if _, err := Supervise(context.Background(), SupervisorConfig{Root: "/x"}, agent); err == nil {
		t.Fatal("Supervise accepted an empty session id")
	}
	if _, err := Supervise(context.Background(), SupervisorConfig{SessionID: "ses_1"}, agent); err == nil {
		t.Fatal("Supervise accepted an empty root")
	}
}
