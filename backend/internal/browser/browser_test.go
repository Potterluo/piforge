package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeCDP is a stand-in for Chrome's devtools endpoint.
type fakeCDP struct {
	server *httptest.Server
	wsURL  string

	mu       sync.Mutex
	received []string
	params   []map[string]any
	// frames is how many screencast frames to push to every client.
	frames int
	// badFrame makes the first frame payload invalid base64.
	badFrame bool
	// silent stops pushing frames, which is what a paused page looks like.
	silent bool
}

func newFakeCDP(t *testing.T) *fakeCDP {
	t.Helper()
	f := &fakeCDP{frames: 2}

	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"webSocketDebuggerUrl": f.wsURL,
		})
	})
	mux.HandleFunc("/devtools/browser", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		f.serve(t, conn)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	f.wsURL = "ws" + strings.TrimPrefix(f.server.URL, "http") + "/devtools/browser"
	return f
}

func (f *fakeCDP) serve(t *testing.T, conn *websocket.Conn) {
	ctx := context.Background()
	screencasting := false
	var ackCount int

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}

		f.mu.Lock()
		f.received = append(f.received, msg.Method)
		f.params = append(f.params, msg.Params)
		silent := f.silent
		f.mu.Unlock()

		reply := func(result any) {
			payload, _ := json.Marshal(map[string]any{"id": msg.ID, "result": result})
			_ = conn.Write(ctx, websocket.MessageText, payload)
		}

		switch msg.Method {
		case "Page.enable", "Input.dispatchMouseEvent", "Input.dispatchKeyEvent",
			"Input.insertText", "Page.navigate":
			reply(map[string]any{})
		case "Page.startScreencast":
			screencasting = true
			reply(map[string]any{})
			if !silent {
				go f.pushFrames(conn)
			}
		case "Page.stopScreencast":
			screencasting = false
			reply(map[string]any{})
		case "Page.screencastFrameAck":
			// Chrome answers the ack; a fake that stays silent makes the client
			// block here, which looks exactly like a frozen page.
			ackCount++
			reply(map[string]any{})
		default:
			reply(map[string]any{})
		}
		_ = screencasting
	}
}

func (f *fakeCDP) pushFrames(conn *websocket.Conn) {
	ctx := context.Background()
	for i := 0; i < f.frames; i++ {
		payload := base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, byte(i)})
		if f.badFrame && i == 0 {
			payload = "!!!not-base64!!!"
		}
		event, _ := json.Marshal(map[string]any{
			"method": "Page.screencastFrame",
			"params": map[string]any{
				"data":      payload,
				"sessionId": i + 1,
				"metadata":  map[string]any{"deviceWidth": 1280, "deviceHeight": 800},
			},
		})
		if err := conn.Write(ctx, websocket.MessageText, event); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *fakeCDP) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.received))
	copy(out, f.received)
	return out
}

func (f *fakeCDP) has(method string) bool {
	for _, m := range f.methods() {
		if m == method {
			return true
		}
	}
	return false
}

func TestManagerReportsNotConfigured(t *testing.T) {
	m := NewManager(Config{})
	if m.Configured() {
		t.Fatal("a manager with no binary reports itself as configured")
	}
	if _, err := m.Ensure(context.Background(), "sb_1", t.TempDir()); err != ErrNotConfigured {
		t.Fatalf("Ensure err = %v, want ErrNotConfigured", err)
	}
}

func TestScreencastForwardsFramesAndAcks(t *testing.T) {
	fake := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := dialCDP(ctx, fake.wsURL)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	session := &Session{client: client, stopped: make(chan struct{})}

	var got [][]byte
	err = session.Screencast(ctx, func(jpeg []byte, _ Meta) {
		got = append(got, jpeg)
		if len(got) == fake.frames {
			cancel()
		}
	})
	if err != nil && ctx.Err() == nil {
		t.Fatalf("Screencast: %v", err)
	}

	if len(got) != fake.frames {
		t.Fatalf("frames = %d, want %d", len(got), fake.frames)
	}
	for i, frame := range got {
		if len(frame) != 3 || frame[0] != 0xff || frame[1] != 0xd8 || frame[2] != byte(i) {
			t.Fatalf("frame %d = %v", i, frame)
		}
	}
	// Without the ack Chrome stops sending, so its absence is a hanging page.
	if !fake.has("Page.screencastFrameAck") {
		t.Fatal("frames were never acknowledged")
	}
}

func TestScreencastSurvivesAMalformedFrame(t *testing.T) {
	fake := newFakeCDP(t)
	fake.badFrame = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := dialCDP(ctx, fake.wsURL)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	session := &Session{client: client, stopped: make(chan struct{})}

	frames := 0
	_ = session.Screencast(ctx, func(jpeg []byte, _ Meta) {
		frames++
		if frames >= fake.frames {
			cancel()
		}
	})
	if frames != fake.frames {
		t.Fatalf("frames = %d, want %d (a bad frame killed the stream)", frames, fake.frames)
	}
}

func TestDispatchSendsTheExpectedProtocolCalls(t *testing.T) {
	fake := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := dialCDP(ctx, fake.wsURL)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	session := &Session{client: client, stopped: make(chan struct{})}

	if err := session.Dispatch(ctx, Input{Type: "click", X: 10, Y: 20}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if err := session.Dispatch(ctx, Input{Type: "text", Text: "hello"}); err != nil {
		t.Fatalf("text: %v", err)
	}
	if err := session.Dispatch(ctx, Input{Type: "key", Key: "Enter", Code: "Enter"}); err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := session.Dispatch(ctx, Input{Type: "unknown"}); err == nil {
		t.Fatal("an unknown input type was accepted")
	}

	methods := fake.methods()
	counts := map[string]int{}
	for _, m := range methods {
		counts[m]++
	}
	// A click is press + release, and a key is down + up.
	if counts["Input.dispatchMouseEvent"] < 2 {
		t.Fatalf("mouse events = %d, want at least 2 (%v)", counts["Input.dispatchMouseEvent"], methods)
	}
	if counts["Input.dispatchKeyEvent"] != 2 {
		t.Fatalf("key events = %d, want 2", counts["Input.dispatchKeyEvent"])
	}
	if counts["Input.insertText"] != 1 {
		t.Fatalf("insertText = %d, want 1", counts["Input.insertText"])
	}

	// The second key event must not carry text: keyUp with text inserts it.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var keyParams []map[string]any
	for i, method := range fake.received {
		if method == "Input.dispatchKeyEvent" {
			keyParams = append(keyParams, fake.params[i])
		}
	}
	if got := keyParams[1]["type"]; got != "keyUp" {
		t.Fatalf("second key event type = %v", got)
	}
	if _, ok := keyParams[1]["text"]; ok {
		t.Fatal("keyUp carried text")
	}
}

func TestNavigateOnlyAllowsHTTP(t *testing.T) {
	fake := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := dialCDP(ctx, fake.wsURL)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	session := &Session{client: client, stopped: make(chan struct{})}

	if err := session.Navigate(ctx, "https://example.com"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if !fake.has("Page.navigate") {
		t.Fatal("Page.navigate was never sent")
	}
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", ""} {
		if err := session.Navigate(ctx, bad); err == nil {
			t.Fatalf("Navigate(%q) was allowed", bad)
		}
	}
}

func TestWaitForDevToolsTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Port 1 is not going to answer.
	if _, err := waitForDevTools(ctx, 1, 400*time.Millisecond); err == nil {
		t.Fatal("waitForDevTools succeeded against nothing")
	}
}

func TestManagerLifecycle(t *testing.T) {
	m := NewManager(Config{Binary: "chrome"})
	if !m.Configured() {
		t.Fatal("Configured() = false with a binary")
	}
	if m.Len() != 0 {
		t.Fatal("a fresh manager has sessions")
	}
	if m.Close("nope") {
		t.Fatal("Close reported closing a session that did not exist")
	}
	m.CloseAll()
	if _, err := m.Ensure(context.Background(), "sb_1", t.TempDir()); err == nil {
		t.Fatal("Ensure succeeded after CloseAll")
	}
}
