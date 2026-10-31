package term

import (
	"context"
	"encoding/json"
	"errors"
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

	cwd      string
	writes   []fakeWrite
	views    int
	creates  int
	viewedID []string
	written  []string
	// outputAt returns the output for the nth view call.
	outputAt func(view int) string
	// statusAt returns the status for the nth view call.
	statusAt func(view int) string
	// failViews is how many leading view calls fail; -1 fails every one.
	failViews int
	// failedViews counts the view calls that failed.
	failedViews int
	// failStatus is the HTTP status a failing view answers (default 500).
	failStatus int
	// createdID, when set, is the session id the create answer reports — which
	// is not necessarily the id the caller asked for.
	createdID string
}

type fakeWrite struct {
	Input string `json:"input"`
	Enter bool   `json:"press_enter"`
}

func newFakeSandbox(t *testing.T) (*fakeSandbox, *httptest.Server) {
	t.Helper()
	fake := &fakeSandbox{
		outputAt:   func(int) string { return "" },
		statusAt:   func(int) string { return "running" },
		failStatus: http.StatusInternalServerError,
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
		fake.creates++
		id := body.ID
		if fake.createdID != "" {
			id = fake.createdID
		}
		fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": id, "working_dir": body.ExecDir},
		})
	})
	mux.HandleFunc("/v1/shell/write", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID    string `json:"id"`
			Input string `json:"input"`
			Enter bool   `json:"press_enter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.writes = append(fake.writes, fakeWrite{Input: body.Input, Enter: body.Enter})
		fake.written = append(fake.written, body.ID)
		fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "running"}})
	})
	mux.HandleFunc("/v1/shell/view", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.views++
		n := fake.views
		fake.viewedID = append(fake.viewedID, body.ID)
		fail := fake.failViews < 0 || n <= fake.failViews
		if fail {
			fake.failedViews++
		}
		status := fake.failStatus
		output := fake.outputAt(n)
		viewStatus := fake.statusAt(n)
		fake.mu.Unlock()
		if fail {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"success":false,"message":"session gone"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"session_id": body.ID,
				"output":     output,
				"status":     viewStatus,
			},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return fake, server
}

func newTestRemote(t *testing.T, server *httptest.Server, fake *fakeSandbox) *RemoteSession {
	t.Helper()
	return newTestRemoteWith(t, server, RemoteConfig{})
}

func newTestRemoteWith(t *testing.T, server *httptest.Server, overrides RemoteConfig) *RemoteSession {
	t.Helper()
	client, err := aio.New(aio.Config{BaseURL: server.URL, APIKey: "pf-test"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	cfg := RemoteConfig{
		Client:       client,
		SandboxID:    "wt-1",
		TabID:        "tab-remote",
		SessionID:    "s-1",
		Cwd:          "/workspace/repo",
		PollInterval: 10 * time.Millisecond,
		// Keep the real retry policy, shortened so a failing-path test costs
		// milliseconds instead of the 20 s a terminal is given in production.
		RetryBase:   2 * time.Millisecond,
		RetryMax:    10 * time.Millisecond,
		RetryBudget: 60 * time.Millisecond,
	}
	if overrides.PollInterval > 0 {
		cfg.PollInterval = overrides.PollInterval
	}
	if overrides.RetryBase > 0 {
		cfg.RetryBase = overrides.RetryBase
	}
	if overrides.RetryMax > 0 {
		cfg.RetryMax = overrides.RetryMax
	}
	if overrides.RetryBudget > 0 {
		cfg.RetryBudget = overrides.RetryBudget
	}
	remote, err := NewRemoteSession(cfg)
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

// Enter in a browser terminal is CR (xterm sends 0x0D), not LF. The adapter used
// to ignore CR and wait for LF, so a command typed in the panel was echoed and
// never run — the isolated terminal looked connected and did nothing.
func TestRemoteSessionExecutesOnCarriageReturn(t *testing.T) {
	fake, server := newFakeSandbox(t)
	remote := newTestRemote(t, server, fake)
	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := remote.Write("tab-remote", []byte("hostname\r")); err != nil {
		t.Fatalf("write CR: %v", err)
	}
	fake.mu.Lock()
	writes := append([]fakeWrite(nil), fake.writes...)
	fake.mu.Unlock()
	if len(writes) != 1 || writes[0].Input != "hostname" {
		t.Fatalf("a CR-terminated line did not reach the container: %+v", writes)
	}

	// A CRLF pair must not run the same line twice, which would print the
	// command's output twice and, for a write, perform it twice.
	if err := remote.Write("tab-remote", []byte("pwd\r\n")); err != nil {
		t.Fatalf("write CRLF: %v", err)
	}
	fake.mu.Lock()
	writes = append([]fakeWrite(nil), fake.writes...)
	fake.mu.Unlock()
	if len(writes) != 2 || writes[1].Input != "pwd" {
		t.Fatalf("a CRLF line was not executed exactly once: %+v", writes)
	}

	// A LF on its own is still a line end (a paste, or a socket client).
	if err := remote.Write("tab-remote", []byte("whoami\n")); err != nil {
		t.Fatalf("write LF: %v", err)
	}
	fake.mu.Lock()
	writes = append([]fakeWrite(nil), fake.writes...)
	fake.mu.Unlock()
	if len(writes) != 3 || writes[2].Input != "whoami" {
		t.Fatalf("an LF-terminated line did not reach the container: %+v", writes)
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

// The pump's decision is the whole defect: a failed view used to end the stream.
// Retrying has to recover the terminal, and giving up has to end it as a typed
// failure rather than as a shell that exited.
func TestRemoteSessionPumpDecision(t *testing.T) {
	base, max, budget := 100*time.Millisecond, time.Second, 3*time.Second

	// Backoff doubles, then caps.
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{5, max},
		{9, max},
	} {
		retry, wait := nextRetry(tc.failures, 0, base, max, budget)
		if !retry || wait != tc.want {
			t.Errorf("nextRetry(%d) = (%v, %s), want (true, %s)", tc.failures, retry, wait, tc.want)
		}
	}

	// Once the next wait would pass the budget, stop.
	if retry, _ := nextRetry(1, budget, base, max, budget); retry {
		t.Error("nextRetry retried after the budget was spent")
	}
	if retry, _ := nextRetry(1, budget-50*time.Millisecond, base, max, budget); retry {
		t.Error("nextRetry retried when the next wait would overrun the budget")
	}

	// A refused key does not heal: retrying it only hides the cause.
	if !isPermanentViewError(&aio.HTTPError{Status: http.StatusUnauthorized}) {
		t.Error("a 401 view was treated as retryable")
	}
	if !isPermanentViewError(&aio.HTTPError{Status: http.StatusForbidden}) {
		t.Error("a 403 view was treated as retryable")
	}
	if isPermanentViewError(&aio.HTTPError{Status: http.StatusNotFound}) {
		t.Error("a 404 view was treated as permanent; a session can appear a moment later")
	}
	if isPermanentViewError(context.DeadlineExceeded) {
		t.Error("a transport error was treated as permanent")
	}
}

// The measured shape of the defect: the shell is created, the stream says
// "ready", and the first poll fails. The terminal must survive that.
func TestRemoteSessionRetriesAFailedView(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.failViews = 3 // fail the first three views, then answer
	fake.outputAt = func(view int) string {
		if view < 5 {
			return ""
		}
		return "recovered\n"
	}
	// A budget large enough for the three backoff waits (2+4+8 ms here).
	remote := newTestRemoteWith(t, server, RemoteConfig{RetryBudget: 500 * time.Millisecond})

	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := readUntil(t, remote, "recovered", 2*time.Second)
	if !strings.Contains(out, "recovered") {
		t.Fatalf("output = %q", out)
	}
	if info, _ := remote.Info("tab-remote"); info.EOF || info.Err != "" {
		t.Fatalf("a recovered session ended: %+v", info)
	}
	fake.mu.Lock()
	views := fake.views
	fake.mu.Unlock()
	if views < 4 {
		t.Fatalf("the pump gave up after %d views; it was meant to retry", views)
	}
}

// A view that never recovers must reach the relay as a typed error. Before this
// change it ended with finish(1), which the relay turned into a clean close.
func TestRemoteSessionReportsAPersistentViewFailure(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.failViews = -1
	remote := newTestRemote(t, server, fake)

	_, err := remote.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded against a shell that cannot be viewed")
	}
	var failure *RemoteFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error is not a *RemoteFailure: %#v", err)
	}
	if failure.Attempts < 2 {
		t.Fatalf("attempts = %d; the first failure was not retried", failure.Attempts)
	}
	info, _ := remote.Info("tab-remote")
	if !info.EOF {
		t.Fatal("a failed session did not end")
	}
	if info.Err == "" {
		t.Fatal("the session reports no reason for the relay to send")
	}
	if !strings.Contains(info.Err, "500") && !strings.Contains(info.Err, "session gone") {
		t.Fatalf("the reported reason does not carry the sandbox answer: %q", info.Err)
	}
}

// A terminal that worked and then lost its shell must also end as an error, and
// the reason must be the view error — not "shell exited".
func TestRemoteSessionFailureAfterAWorkingStart(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.failViews = 2 // the first view (Start) and the second succeeds? no: 2 fail
	remote := newTestRemoteWith(t, server, RemoteConfig{PollInterval: 5 * time.Millisecond})
	// Fail the handshake's first view, succeed on the second, then fail forever.
	fake.mu.Lock()
	fake.failViews = 1
	fake.outputAt = func(view int) string {
		if view == 2 {
			return "hello\n"
		}
		return ""
	}
	fake.mu.Unlock()

	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	readUntil(t, remote, "hello", 2*time.Second)

	fake.mu.Lock()
	fake.failViews = -1
	fake.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := remote.Info("tab-remote"); info.EOF {
			if info.Err == "" {
				t.Fatal("the stream ended without a reason the relay can show")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the pump never ended after the views started failing")
}

// The retry budget starts when the shell breaks, not when the terminal opened.
// Measuring from the pump's start meant a terminal that had worked for a while
// gave up on its first failed view (measured live: attempts=1 after 25 s).
func TestRemoteSessionRetryBudgetStartsAtTheFirstFailure(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.outputAt = func(int) string { return "alive\n" }
	remote := newTestRemoteWith(t, server, RemoteConfig{
		PollInterval: 5 * time.Millisecond,
		RetryBudget:  120 * time.Millisecond,
	})

	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	readUntil(t, remote, "alive", 2*time.Second)
	// Healthy for longer than the whole retry budget.
	time.Sleep(200 * time.Millisecond)

	fake.mu.Lock()
	fake.failViews = -1
	fake.failedViews = 0
	fake.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := remote.Info("tab-remote"); info.EOF {
			fake.mu.Lock()
			failed := fake.failedViews
			fake.mu.Unlock()
			if failed < 2 {
				t.Fatalf("the shell was given %d failed view(s); the budget was consumed while it was healthy", failed)
			}
			if info.Err == "" {
				t.Fatal("the failure carries no reason")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the pump never ended after the views started failing")
}

// The container answers `session_id`, and it may differ from the id the caller
// asked for. Every later call has to use the id the container registered.
func TestRemoteSessionUsesTheSessionIDCreateRegistered(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.createdID = "registered-by-the-container"
	remote := newTestRemote(t, server, fake)

	if _, err := remote.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := remote.Write("tab-remote", []byte("hostname\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fake.mu.Lock()
	viewed := append([]string(nil), fake.viewedID...)
	written := append([]string(nil), fake.written...)
	fake.mu.Unlock()

	for _, id := range append(viewed, written...) {
		if id != "registered-by-the-container" {
			t.Fatalf("a follow-up call used %q, not the id create registered", id)
		}
	}
}

// Reopening a tab whose shell ended must start a new shell. Handing back the
// dead session is what made every reconnect replay its EOF.
func TestManagerReopensADeadRemoteTab(t *testing.T) {
	fake, server := newFakeSandbox(t)
	fake.outputAt = func(int) string { return "alive\n" }

	m := New(Config{
		RingBytes:         MinRingBytes,
		RemoteRetryBudget: 100 * time.Millisecond,
		RemoteRetryBase:   2 * time.Millisecond,
		RemoteRetryMax:    5 * time.Millisecond,
		Remote: func(context.Context, string) (*aio.Client, string, string, error) {
			client, err := aio.New(aio.Config{BaseURL: server.URL})
			return client, "s-manager", "/workspace", err
		},
	})
	defer m.CloseAll()

	if _, err := m.Open(context.Background(), "wt-remote", "tab-mgr", ""); err != nil {
		t.Fatalf("Open: %v", err)
	}
	waitForRemote(t, m, "tab-mgr", "alive", 2*time.Second)

	// The container shell goes away (this is what a failed view looks like).
	fake.mu.Lock()
	fake.failViews = -1
	fake.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := m.Info("tab-mgr"); ok && info.EOF {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if info, ok := m.Info("tab-mgr"); !ok || !info.EOF {
		t.Fatalf("the session never ended: %+v (ok=%v)", info, ok)
	}

	// Reopening is a new shell, not the dead one.
	fake.mu.Lock()
	fake.failViews = 0
	fake.creates = 0
	fake.mu.Unlock()
	info, err := m.Open(context.Background(), "wt-remote", "tab-mgr", "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if info.EOF || info.Err != "" {
		t.Fatalf("the reopened tab is dead: %+v", info)
	}
	fake.mu.Lock()
	creates := fake.creates
	fake.mu.Unlock()
	if creates != 1 {
		t.Fatalf("reopening created %d shells; it must create exactly one", creates)
	}
	waitForRemote(t, m, "tab-mgr", "alive", 2*time.Second)
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
