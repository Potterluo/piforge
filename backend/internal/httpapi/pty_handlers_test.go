package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/term"
)

// fakeShellAPI is the sandbox's shell endpoints with a scripted view: it answers
// `okViews` times and fails every view after that. It is what the isolated
// deployment looked like when the panel died — a ready terminal whose first
// later view broke.
type fakeShellAPI struct {
	mu       sync.Mutex
	views    int
	okViews  int
	failWith int
}

func (f *fakeShellAPI) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body["id"], "working_dir": "/workspace"},
		})
	})
	mux.HandleFunc("/v1/shell/view", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.views++
		ok := f.views <= f.okViews
		status := f.failWith
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"success":false,"message":"session not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": "", "status": "running"},
		})
	})
	return mux
}

// managerTerm adapts *term.Manager to the interface the router declares. The
// production adapter lives in cmd/piforged; this is the same conversion, local
// to the test so the HTTP package still does not import the terminal in its
// non-test code.
type managerTerm struct{ m *term.Manager }

func (a managerTerm) Open(ctx context.Context, sandboxID, tabID, shell string) (*TermSession, error) {
	info, err := a.m.Open(ctx, sandboxID, tabID, shell)
	if err != nil {
		return nil, err
	}
	return &TermSession{TabID: info.TabID, Shell: info.Shell, PID: info.PID, Start: info.Start, End: info.End}, nil
}
func (a managerTerm) Write(tabID string, p []byte) error        { return a.m.Write(tabID, p) }
func (a managerTerm) Resize(tabID string, cols, rows int) error { return a.m.Resize(tabID, cols, rows) }
func (a managerTerm) Close(tabID string) error                  { return a.m.Close(tabID) }
func (a managerTerm) Read(tabID string, from int64) ([]byte, int64, bool) {
	return a.m.Read(tabID, from)
}
func (a managerTerm) Info(tabID string) (TermSession, bool) {
	info, ok := a.m.Info(tabID)
	if !ok {
		return TermSession{}, false
	}
	return TermSession{TabID: info.TabID, Shell: info.Shell, PID: info.PID, Start: info.Start, End: info.End,
		Err: info.Err, Retrying: info.Retrying, RetrySeq: info.RetrySeq, RetryWhy: info.RetryWhy}, true
}
func (a managerTerm) CloseSandbox(sandboxID string) int { return a.m.CloseSandbox(sandboxID) }

func newManagerTerm(cfg term.Config) managerTerm { return managerTerm{m: term.New(cfg)} }

// dialPTY opens the relay socket against a real HTTP server, with the session's
// own cookie — the same handshake the browser makes.
func dialPTY(t *testing.T, base, sessionID string, cookies []*http.Cookie) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	header := http.Header{}
	for _, c := range cookies {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	conn, _, err := websocket.Dial(ctx, base+"/v1/sessions/"+sessionID+"/pty?tab=t1", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		t.Fatalf("dial pty: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx
}

// readFrames collects the text frames of one socket until it closes.
func readFrames(t *testing.T, conn *websocket.Conn, ctx context.Context) (frames []string, closeStatus websocket.StatusCode) {
	t.Helper()
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return frames, websocket.CloseStatus(err)
		}
		frames = append(frames, string(payload))
		if len(frames) > 50 {
			t.Fatal("the relay sent more than 50 frames without ending")
		}
	}
}

// The measured isolated shape: the relay said "ready" and then closed as if the
// shell had exited. A terminal whose sandbox shell stops answering must instead
// end with an `error` frame carrying the reason, and a close status that says so.
func TestPTYRelayReportsASandboxShellFailure(t *testing.T) {
	held := newHeldProject(t)
	fake := &fakeShellAPI{okViews: 2, failWith: http.StatusInternalServerError}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	// The remote hook answers for this session's worktree; the manager retries a
	// failing view for far less time than production, so the test is quick and
	// still exercises the real pump.
	held.env.deps.Term = newManagerTerm(term.Config{
		RingBytes:         term.MinRingBytes,
		RemoteRetryBudget: 300 * time.Millisecond, RemotePollInterval: 10 * time.Millisecond,
		RemoteRetryBase: 5 * time.Millisecond,
		RemoteRetryMax:  20 * time.Millisecond,
		Remote: func(context.Context, string) (*aio.Client, string, string, error) {
			client, err := aio.New(aio.Config{BaseURL: srv.URL})
			return client, "shell-1", "/workspace", err
		},
	})
	held.env.rebuild()

	app := httptest.NewServer(held.env.handler)
	defer app.Close()

	creds := held.env.login("dev@piforge.local", "pw")
	conn, ctx := dialPTY(t, app.URL, held.session.ID, creds.cookies)
	frames, status := readFrames(t, conn, ctx)

	if len(frames) == 0 || !strings.Contains(frames[0], `"type":"ready"`) {
		t.Fatalf("first frame = %v, want ready", frames)
	}
	var reported string
	sawExit, sawRetry := false, false
	for _, frame := range frames {
		var msg struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(frame), &msg); err != nil {
			continue // terminal bytes arriving as text would be a different bug
		}
		switch msg.Type {
		case "error":
			reported = msg.Message
		case "exit":
			sawExit = true
		case "reconnecting":
			sawRetry = true
		}
	}
	if sawExit {
		t.Fatal("a failed sandbox shell was reported as a clean exit")
	}
	if !sawRetry {
		t.Fatalf("the panel was never told the terminal was reconnecting: %v", frames)
	}
	if reported == "" {
		t.Fatalf("no error frame in %v", frames)
	}
	if !strings.Contains(reported, "session not found") {
		t.Fatalf("the error frame does not carry the sandbox's reason: %q", reported)
	}
	if status != websocket.StatusInternalError {
		t.Fatalf("close status = %d, want %d", status, websocket.StatusInternalError)
	}
}

// A shell that cannot be created at all must fail at open with an error frame —
// never a `ready` for a terminal that has no shell behind it.
func TestPTYRelayReportsAnUnopenableShell(t *testing.T) {
	held := newHeldProject(t)
	fake := &fakeShellAPI{okViews: 0, failWith: http.StatusUnauthorized}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	held.env.deps.Term = newManagerTerm(term.Config{
		RingBytes:         term.MinRingBytes,
		RemoteRetryBudget: 100 * time.Millisecond,
		RemoteRetryBase:   5 * time.Millisecond,
		RemoteRetryMax:    10 * time.Millisecond,
		Remote: func(context.Context, string) (*aio.Client, string, string, error) {
			client, err := aio.New(aio.Config{BaseURL: srv.URL})
			return client, "shell-1", "/workspace", err
		},
	})
	held.env.rebuild()

	app := httptest.NewServer(held.env.handler)
	defer app.Close()

	creds := held.env.login("dev@piforge.local", "pw")
	conn, ctx := dialPTY(t, app.URL, held.session.ID, creds.cookies)
	frames, status := readFrames(t, conn, ctx)

	for _, frame := range frames {
		if strings.Contains(frame, `"type":"ready"`) {
			t.Fatalf("a ready frame was sent for a terminal with no shell: %v", frames)
		}
	}
	if len(frames) == 0 || !strings.Contains(frames[0], `"type":"error"`) {
		t.Fatalf("frames = %v, want an error frame", frames)
	}
	if status != websocket.StatusInternalError {
		t.Fatalf("close status = %d, want %d", status, websocket.StatusInternalError)
	}
}
