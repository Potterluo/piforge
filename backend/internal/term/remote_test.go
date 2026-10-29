package term

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
)

// fakeSandbox is the smallest shell service that behaves like the AIO image:
// create, write and view, with view returning the whole accumulated output.
type fakeSandbox struct {
	mu sync.Mutex

	cwd    string
	writes []fakeWrite
	views  int
	// outputAt returns the output for the nth view call.
	outputAt func(view int) string
	// statusAt returns the status for the nth view call.
	statusAt func(view int) string
	failView bool
}

type fakeWrite struct {
	Input string `json:"input"`
	Enter bool   `json:"press_enter"`
}

func newFakeSandbox(t *testing.T) (*fakeSandbox, *httptest.Server) {
	t.Helper()
	fake := &fakeSandbox{
		outputAt: func(int) string { return "" },
		statusAt: func(int) string { return "running" },
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID      string `json:"id"`
			ExecDir string `json:"exec_dir"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.cwd = body.ExecDir
		fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body.ID, "working_dir": body.ExecDir},
		})
	})
	mux.HandleFunc("/v1/shell/write", func(w http.ResponseWriter, r *http.Request) {
		var body fakeWrite
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.writes = append(fake.writes, body)
		fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "running"}})
	})
	mux.HandleFunc("/v1/shell/view", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.views++
		n := fake.views
		fail := fake.failView
		output := fake.outputAt(n)
		status := fake.statusAt(n)
		fake.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"message":"session gone"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"session_id": "s-1",
				"output":     output,
				"status":     status,
			},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return fake, server
}

func newTestRemote(t *testing.T, server *httptest.Server, fake *fakeSandbox) *RemoteSession {
	t.Helper()
	client, err := aio.New(aio.Config{BaseURL: server.URL, APIKey: "pf-test"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	remote, err := NewRemoteSession(RemoteConfig{
		Client:       client,
		SandboxID:    "wt-1",
		TabID:        "tab-remote",
		SessionID:    "s-1",
		Cwd:          "/workspace/repo",
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRemoteSession: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close("tab-remote") })
	return remote
}

func readUntil(t *testing.T, remote *RemoteSession, marker string, timeout time.Duration) string {
	t.Helper()
	var collected strings.Builder
	var next int64
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, n, eof := remote.Read("tab-remote", next)
		if len(data) > 0 {
			collected.Write(data)
			next = n
		}
		if strings.Contains(collected.String(), marker) {
			return collected.String()
		}
		if eof {
			t.Fatalf("session ended before %q; got %q", marker, collected.String())
		}
		select {
		case <-remote.Wake("tab-remote"):
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("timeout waiting for %q; got %q", marker, collected.String())
	return ""
}

// The image returns the whole transcript on every ViewShell call. The pump must
// append only the suffix, or the terminal repeats itself on every poll.
func TestRemoteSessionAppendsOnlyNewOutput(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.outputAt = func(view int) string {
		if view < 3 {
			return "first-chunk"
		}
		return "first-chunksecond-chunk"
	}
	remote := newTestRemote(t, server, fake)

	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	out := readUntil(t, remote, "second-chunk", 3*time.Second)
	// A few more polls of the same output must add nothing.
	time.Sleep(40 * time.Millisecond)
	rest, _, _ := remote.Read("tab-remote", int64(len(out)))
	out += string(rest)

	if out != "first-chunksecond-chunk" {
		t.Fatalf("replayed output = %q", out)
	}
	if got := strings.Count(out, "first-chunk"); got != 1 {
		t.Fatalf("the first chunk appeared %d times", got)
	}
	if fake.cwd != "/workspace/repo" {
		t.Errorf("create cwd = %q", fake.cwd)
	}
}

// A trailing backspace edits the local line; it is never sent as a byte.
func TestRemoteSessionBackspaceIsNotSent(t *testing.T) {
	fake, server := newFakeSandbox(t)
	remote := newTestRemote(t, server, fake)
	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := remote.Write("tab-remote", []byte("echo zz")); err != nil {
		t.Fatalf("write prefix: %v", err)
	}
	fake.mu.Lock()
	before := len(fake.writes)
	fake.mu.Unlock()
	if before != 0 {
		t.Fatalf("a partial line was executed: %+v", fake.writes)
	}

	if err := remote.Write("tab-remote", []byte("\x7f")); err != nil {
		t.Fatalf("backspace: %v", err)
	}
	if err := remote.Write("tab-remote", []byte("ok\n")); err != nil {
		t.Fatalf("write line: %v", err)
	}

	fake.mu.Lock()
	writes := append([]fakeWrite(nil), fake.writes...)
	fake.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("writes = %+v, want one completed line", writes)
	}
	if writes[0].Input != "echo zok" {
		t.Fatalf("input = %q, want the backspaced line", writes[0].Input)
	}
	if !writes[0].Enter {
		t.Error("the completed line was written without press_enter")
	}
	for _, w := range writes {
		if strings.ContainsRune(w.Input, '\x7f') {
			t.Fatalf("a DEL byte reached the container: %q", w.Input)
		}
	}

	// The user sees what they typed even though the container never echoes it.
	echo, _, _ := remote.Read("tab-remote", 0)
	if !strings.Contains(string(echo), "echo zz\b \bok\r\n") {
		t.Fatalf("local echo = %q", echo)
	}
}

// A failing ViewShell must end the stream, not spin forever.
func TestRemoteSessionPollErrorEndsTheStream(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.failView = true
	remote := newTestRemote(t, server, fake)
	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, ok := remote.Info("tab-remote")
		if !ok {
			t.Fatal("info lost the session")
		}
		if info.EOF {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the poller kept running after ViewShell failed")
}

// The image joins command outputs without a newline. A completed command should
// end its line once, not on every subsequent poll.
func TestRemoteSessionTerminatesACompletedLine(t *testing.T) {
	fake, server := newFakeSandbox(t)
	// Nothing is printed until a line has been executed; this keeps the test
	// from racing the poller before Write marks the command as running.
	fake.outputAt = func(int) string {
		if len(fake.writes) == 0 {
			return ""
		}
		return "hello"
	}
	fake.statusAt = func(int) string {
		if len(fake.writes) == 0 {
			return "running"
		}
		return "completed"
	}
	remote := newTestRemote(t, server, fake)
	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := remote.Write("tab-remote", []byte("echo hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	out := readUntil(t, remote, "hello\n", 2*time.Second)
	time.Sleep(40 * time.Millisecond)
	rest, _, _ := remote.Read("tab-remote", int64(len(out)))
	out += string(rest)

	if out != "echo hello\r\nhello\n" {
		t.Fatalf("output = %q", out)
	}
	if strings.Count(out, "hello\n") != 1 {
		t.Fatalf("the completed line was terminated more than once: %q", out)
	}
}

// Manager.Open must route to the remote implementation when the hook returns a
// client, and the rest of the Manager surface must keep working unchanged.
func TestManagerOpensARemoteTerminal(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.outputAt = func(int) string { return "manager-remote-ok" }

	m := New(Config{
		RingBytes: MinRingBytes,
		Remote: func(ctx context.Context, sandboxID string) (*aio.Client, string, string, error) {
			if sandboxID != "wt-remote" {
				t.Errorf("remote hook id = %q", sandboxID)
			}
			client, err := aio.New(aio.Config{BaseURL: server.URL})
			return client, "s-manager", "/workspace", err
		},
	})
	defer m.CloseAll()

	info, err := m.Open(context.Background(), "wt-remote", "tab-mgr", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if info.Shell != "aio" || info.PID != 0 {
		t.Fatalf("remote info = %+v", info)
	}
	if err := m.Resize("tab-mgr", 100, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if info, _ := m.Info("tab-mgr"); info.Cols != 100 || info.Rows != 40 {
		t.Errorf("resize was not recorded: %+v", info)
	}
	if m.Len() != 1 || len(m.List("wt-remote")) != 1 {
		t.Fatalf("len=%d list=%d", m.Len(), len(m.List("wt-remote")))
	}

	out, _ := waitForRemote(t, m, "tab-mgr", "manager-remote-ok", 2*time.Second)
	if fake.cwd != "/workspace" {
		t.Errorf("cwd = %q", fake.cwd)
	}
	_ = out

	if err := m.Close("tab-mgr"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if m.Len() != 0 {
		t.Fatalf("live sessions = %d after Close", m.Len())
	}
}

func waitForRemote(t *testing.T, m *Manager, tabID, marker string, timeout time.Duration) (string, int64) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var collected strings.Builder
	var next int64
	for time.Now().Before(deadline) {
		data, n, eof := m.Read(tabID, next)
		if len(data) > 0 {
			collected.Write(data)
			next = n
		}
		if strings.Contains(collected.String(), marker) {
			return collected.String(), next
		}
		if eof {
			t.Fatalf("session ended before %q; got %q", marker, collected.String())
		}
		select {
		case <-m.Wake(tabID):
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("timeout waiting for %q; got %q", marker, collected.String())
	return "", 0
}
