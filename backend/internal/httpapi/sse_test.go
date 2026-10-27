package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// seedEvents appends n message.delta events to a session.
func (e *testEnv) seedEvents(userID, sessionID string, n int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		if _, err := e.store.AppendEvent(context.Background(), userID, &contracts.Event{
			SessionID: sessionID,
			Type:      contracts.EvMsgDelta,
			Payload:   json.RawMessage(`{"chunk":"x"}`),
		}); err != nil {
			e.t.Fatalf("AppendEvent: %v", err)
		}
	}
}

// openStream starts an SSE request against a real server and returns the
// response plus a cancel func the caller must invoke.
func openStream(t *testing.T, srv *httptest.Server, path string, c *creds, lastEventID string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("stream request: %v", err)
	}
	return resp, cancel
}

// readUntil scans an event stream until want appears or the deadline passes.
func readUntil(t *testing.T, body io.Reader, want string, timeout time.Duration) string {
	t.Helper()
	scanner := bufio.NewScanner(body)
	found := make(chan string, 1)
	go func() {
		var seen strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			seen.WriteString(line)
			seen.WriteString("\n")
			if strings.Contains(line, want) {
				found <- seen.String()
				return
			}
		}
		found <- seen.String()
	}()
	select {
	case got := <-found:
		if !strings.Contains(got, want) {
			t.Fatalf("stream ended without %q; saw:\n%s", want, got)
		}
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q in the event stream", want)
		return ""
	}
}

func TestSSEReplayFromLastEventID(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")
	env.seedEvents(user.ID, sess.ID, 3)

	srv := httptest.NewServer(env.handler)
	defer srv.Close()

	resp, cancel := openStream(t, srv, "/v1/sessions/"+sess.ID+"/events", c, "1")
	defer cancel()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}

	// Events 2 and 3 must be replayed, and each frame carries the contracts
	// type as its SSE event name. Waiting on the data line (not the id line)
	// guarantees the whole frame has been read.
	frame := readUntil(t, resp.Body, `"seq":3`, 3*time.Second)
	if !strings.Contains(frame, "id: 2\n") || !strings.Contains(frame, "id: 3\n") {
		t.Fatalf("wrong replay window:\n%s", frame)
	}
	if strings.Contains(frame, "id: 1\n") {
		t.Fatalf("already-seen event 1 was replayed:\n%s", frame)
	}
	if !strings.Contains(frame, "event: "+contracts.EvMsgDelta) {
		t.Fatalf("missing event name:\n%s", frame)
	}
	if !strings.Contains(frame, `"seq":2`) {
		t.Fatalf("missing event 2:\n%s", frame)
	}
}

func TestSSESupportsAfterSeqQuery(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")
	env.seedEvents(user.ID, sess.ID, 2)

	srv := httptest.NewServer(env.handler)
	defer srv.Close()

	resp, cancel := openStream(t, srv, "/v1/sessions/"+sess.ID+"/events?after_seq=1", c, "")
	defer cancel()
	defer resp.Body.Close()

	frame := readUntil(t, resp.Body, "id: 2", 3*time.Second)
	if strings.Contains(frame, "id: 1\n") {
		t.Fatalf("after_seq was ignored:\n%s", frame)
	}
}

func TestSSEGapReturnsNeedFullResync(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")
	env.seedEvents(user.ID, sess.ID, 5)

	// Drop the head of the log, exactly like retention would.
	if _, err := env.store.PruneEvents(context.Background(), user.ID, sess.ID, 3); err != nil {
		t.Fatalf("PruneEvents: %v", err)
	}

	// A cursor older than the retained window must fail loudly.
	req := newTestRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/events", c)
	req.Header.Set("Last-Event-ID", "1")
	rec := serve(env.handler, req)
	env.mustErr(rec, http.StatusConflict, contracts.ErrNeedFullResync)

	// A cursor that is still in the retained window streams normally. Events
	// 1..3 are gone, so resuming from 4 yields exactly event 5.
	srv := httptest.NewServer(env.handler)
	defer srv.Close()
	resp, cancel := openStream(t, srv, "/v1/sessions/"+sess.ID+"/events", c, "4")
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	frame := readUntil(t, resp.Body, `"seq":5`, 3*time.Second)
	if strings.Contains(frame, "id: 4\n") {
		t.Fatalf("pruned event was replayed:\n%s", frame)
	}
}

func TestSSEKeepaliveComment(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	// Build a server with a short keepalive so the test does not wait 15s.
	deps := Deps{Store: env.store, Auth: env.auth, Logger: env.logger()}
	s := newServer(deps)
	s.ssePoll = 10 * time.Millisecond
	s.sseKeepalive = 30 * time.Millisecond
	srv := httptest.NewServer(s.recoverer(s.routes()))
	defer srv.Close()

	resp, cancel := openStream(t, srv, "/v1/sessions/"+sess.ID+"/events", c, "")
	defer cancel()
	defer resp.Body.Close()

	// The keepalive is an SSE comment, which clients ignore.
	if got := readUntil(t, resp.Body, ": keepalive", 3*time.Second); !strings.Contains(got, ": keepalive") {
		t.Fatalf("no keepalive comment:\n%s", got)
	}
}

func TestSSEClientDisconnectReleasesTheHandler(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")
	env.seedEvents(user.ID, sess.ID, 1)

	srv := httptest.NewServer(env.handler)
	defer srv.Close()

	// Warm up, then measure: every connect/disconnect cycle must give its
	// goroutine back rather than leaving a poller behind.
	baseline := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		resp, cancel := openStream(t, srv, "/v1/sessions/"+sess.ID+"/events", c, "")
		// Consume one frame so the handler is definitely inside its loop.
		readUntil(t, resp.Body, "id: 1", 2*time.Second)
		cancel()
		_ = resp.Body.Close()
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+4 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines grew from %d to %d after disconnects", baseline, runtime.NumGoroutine())
}
