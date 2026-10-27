package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// ptySection is the WebSocket endpoint behind the terminal panel.
//
// The PTY master lives inside the sandbox (a tmux/shell process), and this
// handler is only a byte relay: it never interprets what flows through it, and
// it never holds the master fd (PRD §6.3-坑2 / §7.6.1). That is what lets a
// sandbox pause and resume without severing the user's shell.
//
// Wire protocol:
//   - binary frames  = raw terminal bytes in both directions
//   - text frames    = JSON control messages, client→server only:
//     {"type":"resize","cols":120,"rows":32}
//     {"type":"input","data":"ls\n"}      (an alternative to binary frames)
//
// Replay: a client passes ?from_offset=N (or omits it) and receives the ring
// buffer from that offset before live bytes, so a reconnect after a dropped
// connection shows the output it missed instead of a blank terminal.

const (
	ptyTabDefault = "main"
	ptyWriteWait  = 5 * time.Second
)

type ptyControl struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (s *server) handleSessionPTY(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.deps.Term == nil {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "terminal is not configured"))
		return
	}

	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	tabID := strings.TrimSpace(r.URL.Query().Get("tab"))
	if tabID == "" {
		tabID = ptyTabDefault
	}
	// The tab id doubles as part of the manager key, so keep it to something
	// that cannot collide across sessions.
	tabID = sess.ID + ":" + sanitizeTab(tabID)

	root, err := s.worktreeRoot(r.Context(), user.ID, sess)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	fromOffset := int64(-1)
	if raw := r.URL.Query().Get("from_offset"); raw != "" {
		if parsed, perr := strconv.ParseInt(raw, 10, 64); perr == nil && parsed >= 0 {
			fromOffset = parsed
		}
	}

	origin, ok := allowedOrigin(r)
	if !ok {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "origin not allowed for the terminal socket"))
		return
	}
	acceptOpts := &websocket.AcceptOptions{}
	if origin != "" {
		// coder/websocket matches this against the Origin header's host:port.
		acceptOpts.OriginPatterns = []string{origin}
	}
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	// A terminal is a long-lived byte stream; ping/pong keeps proxies honest.
	conn.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	info, err := s.deps.Term.Open(ctx, sess.WorktreeID, tabID, "")
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
			"type": "error", "message": err.Error(),
		}))
		return
	}

	// Tell the client where the stream starts so a reconnect can resume.
	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
		"type": "ready", "tab": tabID, "shell": info.Shell, "pid": info.PID,
		"start": info.Start, "end": info.End,
	}))

	go s.pumpPTYToWS(ctx, conn, tabID, fromOffset)
	s.readPTYFromWS(ctx, conn, tabID, root)
}

// pumpPTYToWS copies terminal output to the socket, starting at fromOffset.
func (s *server) pumpPTYToWS(ctx context.Context, conn *websocket.Conn, tabID string, offset int64) {
	ticker := time.NewTicker(12 * time.Millisecond)
	defer ticker.Stop()

	cursor := offset
	if cursor < 0 {
		cursor = 0
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		data, next, eof := s.deps.Term.Read(tabID, cursor)
		if len(data) > 0 {
			writeCtx, cancel := context.WithTimeout(ctx, ptyWriteWait)
			err := conn.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				return
			}
			cursor = next
		}
		if eof && len(data) == 0 {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{"type": "exit"}))
			conn.Close(websocket.StatusNormalClosure, "shell exited")
			return
		}
	}
}

// readPTYFromWS forwards keystrokes and control messages into the PTY.
func (s *server) readPTYFromWS(ctx context.Context, conn *websocket.Conn, tabID, root string) {
	for {
		kind, payload, err := conn.Read(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) && websocket.CloseStatus(err) == -1 {
				s.log.DebugContext(ctx, "pty socket closed", "error", err.Error())
			}
			return
		}

		switch kind {
		case websocket.MessageBinary:
			if werr := s.deps.Term.Write(tabID, payload); werr != nil {
				return
			}
		case websocket.MessageText:
			var ctl ptyControl
			if json.Unmarshal(payload, &ctl) != nil {
				continue
			}
			switch ctl.Type {
			case "input":
				if werr := s.deps.Term.Write(tabID, []byte(ctl.Data)); werr != nil {
					return
				}
			case "resize":
				if ctl.Cols > 0 && ctl.Rows > 0 {
					_ = s.deps.Term.Resize(tabID, ctl.Cols, ctl.Rows)
				}
			case "ping":
				_ = wsjson.Write(ctx, conn, map[string]string{"type": "pong"})
			case "close":
				_ = s.deps.Term.Close(tabID)
				return
			}
		}
	}
}

// sanitizeTab keeps a client-supplied tab name to a safe identifier.
func sanitizeTab(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return ptyTabDefault
	}
	return b.String()
}

// allowedOrigin decides whether a browser origin may open a terminal socket,
// and returns the exact Origin host to hand coder/websocket.
//
// The session cookie is the credential, so a cross-site socket would be a
// confused-deputy hole and the check is a real one: an Origin is accepted only
// when its hostname matches this server's hostname. Ports are ignored because
// the web app is normally served from a different port (dev) or through the
// Next.js proxy, and coder/websocket compares the *whole* Origin host — port
// included — so the caller must pass the exact value it saw.
//
// Deployments behind TLS termination can pin the allowed set with
// PIFORGE_ALLOWED_ORIGINS (comma separated, exact Origin hosts).
func allowedOrigin(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("Origin"))
	if raw == "" {
		// Non-browser clients (our own probe, tests) send no Origin.
		return "", true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}

	if configured := os.Getenv("PIFORGE_ALLOWED_ORIGINS"); configured != "" {
		for _, candidate := range strings.Split(configured, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), parsed.Host) {
				return parsed.Host, true
			}
		}
		return "", false
	}

	serverHost := r.Host
	if h, _, splitErr := net.SplitHostPort(serverHost); splitErr == nil {
		serverHost = h
	}
	originHost := parsed.Host
	if h, _, splitErr := net.SplitHostPort(originHost); splitErr == nil {
		originHost = h
	}
	if !strings.EqualFold(originHost, serverHost) {
		return "", false
	}
	return parsed.Host, true
}

// mustJSON encodes a control frame. A failure here is impossible for the small
// maps used above, and must never break the relay, so it degrades to "{}".
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
