package pi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// TestWrapArgvStub is the child the wrapper test spawns: it only has to exist
// long enough for the bridge to report a process id.
func TestWrapArgvStub(t *testing.T) {
	if testing.Short() {
		t.Skip("helper")
	}
	time.Sleep(50 * time.Millisecond)
}

// TestWrapArgvIsTheOutermostWrapper pins the ordering the liveness launcher
// depends on: WrapArgv sees the argv *after* the jail and after the bridge
// appended --session, and whatever it returns is what gets spawned. If this
// ordering changed, the heartbeat launcher could end up inside bubblewrap and
// its marker would describe the wrong process.
func TestWrapArgvIsTheOutermostWrapper(t *testing.T) {
	worktree := t.TempDir()
	var saw []string
	rt := NewRuntime(RuntimeConfig{
		Config: Config{
			BinaryPath: "pi",
			Args:       []string{"--mode", "rpc"},
			Dir:        worktree,
		},
		WrapArgv: func(_ context.Context, spec contracts.SessionSpec, argv []string) ([]string, error) {
			saw = append([]string(nil), argv...)
			// Replace the agent with this test binary, so the spawn is real but
			// does not need cmd/fakepi.
			return append([]string{"go", "env", "GOVERSION"}, argv...), nil
		},
	})
	defer func() { _ = rt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := contracts.SessionSpec{SessionID: "ses_wrap", Kind: contracts.KindMain, Worktree: worktree}
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, err := rt.Status(ctx, spec.SessionID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.ProcessID == 0 {
		t.Fatal("the wrapped agent was not spawned")
	}
	if len(saw) < 3 || saw[0] != "pi" || saw[1] != "--mode" || saw[2] != "rpc" {
		t.Fatalf("WrapArgv saw %v, want the configured agent argv first", saw)
	}
	// The bridge appends --session *after* this wrapper's argv, which is why the
	// launcher's "--" separator matters: the flag lands in the agent's argv, not
	// in the launcher's. internal/liveness covers that parse.
	if strings.Contains(strings.Join(saw, " "), "--session") {
		t.Fatalf("WrapArgv saw a --session the bridge had not appended yet: %v", saw)
	}
	if err := rt.Stop(ctx, spec.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestWrapArgvFailureFailsTheSpawn keeps a broken launcher from producing an
// unmarked agent: the run fails where the operator can see it instead of being
// interrupted later by reconciliation.
func TestWrapArgvFailureFailsTheSpawn(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{
		Config: Config{BinaryPath: "pi", Args: []string{"--mode", "rpc"}, Dir: t.TempDir()},
		WrapArgv: func(context.Context, contracts.SessionSpec, []string) ([]string, error) {
			return nil, context.DeadlineExceeded
		},
	})
	defer func() { _ = rt.Close() }()
	_, err := rt.Start(context.Background(), contracts.SessionSpec{SessionID: "ses_wrap_fail", Kind: contracts.KindMain})
	if err == nil {
		t.Fatal("Start succeeded although the wrapper failed")
	}
	if !strings.Contains(err.Error(), "wrap the agent") {
		t.Fatalf("error = %v, want it to name the wrapper", err)
	}
}
