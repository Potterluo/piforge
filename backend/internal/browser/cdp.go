package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// cdp is a minimal Chrome DevTools Protocol client: enough to start a
// screencast, drive input and navigate. It is not a general CDP library — every
// command the platform needs is small, and a dependency that wraps the whole
// protocol would be far larger than the handful of frames we actually send.
type cdp struct {
	conn *websocket.Conn

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan json.RawMessage
	// sessionID is the flattened target session Page/Input commands go to.
	sessionID string
	lastErr   error

	events chan rawEvent
	closed chan struct{}
	err    error
}

type rawEvent struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rawResult struct {
	ID     int64           `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func dialCDP(ctx context.Context, wsURL string) (*cdp, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("browser: connect to devtools: %w", err)
	}
	// Screencast frames are base64 JPEG and can be large.
	conn.SetReadLimit(32 << 20)

	c := &cdp{
		conn:    conn,
		pending: map[int64]chan json.RawMessage{},
		events:  make(chan rawEvent, 64),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *cdp) readLoop() {
	defer close(c.closed)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.fail(err)
			return
		}
		var msg rawResult
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					// Keep the protocol's own words: "Page.enable failed" tells
					// nobody why, "not attached to a page" does.
					c.mu.Lock()
					if c.err == nil {
						c.lastErr = fmt.Errorf("%s", msg.Error.Message)
					}
					c.mu.Unlock()
					ch <- nil
				} else {
					ch <- msg.Result
				}
				close(ch)
			}
			continue
		}
		if msg.Method == "" {
			continue
		}
		select {
		case c.events <- rawEvent{Method: msg.Method, Params: msg.Params}:
		case <-c.closed:
			return
		}
	}
}

func (c *cdp) fail(err error) {
	c.mu.Lock()
	c.err = err
	pending := c.pending
	c.pending = map[int64]chan json.RawMessage{}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// call sends one command and waits for the matching result.
func (c *cdp) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("browser: devtools is gone: %w", err)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	// Page/Input domains only exist inside a target session (flatten mode), so
	// every command after attach carries the session id.
	if sessionID := c.session(); sessionID != "" {
		payload["sessionId"] = sessionID
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("browser: encode %s: %w", method, err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = c.conn.Write(writeCtx, websocket.MessageText, data)
	cancel()
	if err != nil {
		return fmt.Errorf("browser: send %s: %w", method, err)
	}

	select {
	case result, ok := <-ch:
		if !ok {
			return fmt.Errorf("browser: connection closed during %s", method)
		}
		if result == nil {
			c.mu.Lock()
			detail := c.lastErr
			c.lastErr = nil
			c.mu.Unlock()
			if detail != nil {
				return fmt.Errorf("browser: %s failed: %w", method, detail)
			}
			return fmt.Errorf("browser: %s failed", method)
		}
		if out != nil {
			if err := json.Unmarshal(result, out); err != nil {
				return fmt.Errorf("browser: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("browser: %s timed out: %w", method, ctx.Err())
	case <-c.closed:
		return fmt.Errorf("browser: connection closed during %s", method)
	}
}

// next returns the next protocol event.
func (c *cdp) next(ctx context.Context) (rawEvent, error) {
	select {
	case ev, ok := <-c.events:
		if !ok {
			return rawEvent{}, errors.New("browser: event stream closed")
		}
		return ev, nil
	case <-c.closed:
		return rawEvent{}, errors.New("browser: connection closed")
	case <-ctx.Done():
		return rawEvent{}, ctx.Err()
	}
}

// session returns the attached page session, if any.
func (c *cdp) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// attachToPage finds (or creates) a page target and attaches to it, which is
// what makes the Page and Input domains usable. Chrome's browser endpoint
// exposes only Target.* until this happens.
func (c *cdp) attachToPage(ctx context.Context) error {
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := c.call(ctx, "Target.getTargets", nil, &targets); err != nil {
		return err
	}

	targetID := ""
	for _, target := range targets.TargetInfos {
		if target.Type == "page" {
			targetID = target.TargetID
			break
		}
	}
	if targetID == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := c.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return fmt.Errorf("browser: create a page: %w", err)
		}
		targetID = created.TargetID
	}

	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "Target.attachToTarget",
		map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return fmt.Errorf("browser: attach to the page: %w", err)
	}
	if attached.SessionID == "" {
		return errors.New("browser: attach returned no session")
	}
	c.mu.Lock()
	c.sessionID = attached.SessionID
	c.mu.Unlock()
	return nil
}

func (c *cdp) close() {
	_ = c.conn.Close(websocket.StatusNormalClosure, "shutdown")
}
