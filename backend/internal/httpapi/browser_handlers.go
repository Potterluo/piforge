package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// browserWriteWait bounds a single frame write. A viewer that cannot absorb a
// frame in this long is gone, and holding the socket would stall the screencast
// for everyone else looking at the same browser.
const browserWriteWait = 5 * time.Second

type browserCommand struct {
	Type string `json:"type"`
	// URL is used by navigate.
	URL string `json:"url,omitempty"`
	// Input is used by input.
	Input *browser.Input `json:"input,omitempty"`
	// Width and Height are used by resize.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// handleSessionBrowser streams the sandbox's browser to a viewer and forwards
// its input back.
//
// One Chrome per sandbox (PRD §7.7): every viewer of a session watches the same
// page, because that page is where the cookies and the agent's own actions
// live. Frames go down as binary JPEG, commands come up as JSON.
func (s *server) handleSessionBrowser(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.featureDenied(w, r, user.ID, string(store.FeatureBrowser)) {
		return
	}
	if s.deps.Browser == nil || !s.deps.Browser.Available() {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail,
			"no chrome binary is configured on this server"))
		return
	}

	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if sess.WorktreeID == "" {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "the session has no checkout yet"))
		return
	}
	worktree, err := s.deps.Store.Worktree(r.Context(), user.ID, sess.WorktreeID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The browser belongs to the sandbox, not the worktree: two sessions in one
	// sandbox share one Chrome, which is the point of the granularity choice.
	sandboxID := worktree.SandboxID

	// The profile lives in the sandbox root, one level above the checkout. It
	// must not be inside the repository: a Chrome profile is thousands of files
	// and would show up as the user's untracked work on every page load.
	profileRoot := filepath.Dir(worktree.Path)
	if sb, sbErr := s.deps.Store.Sandbox(r.Context(), user.ID, sandboxID); sbErr == nil && sb.RootDir != "" {
		profileRoot = sb.RootDir
	}

	origin, ok := allowedOrigin(r)
	if !ok {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "origin not allowed for the browser socket"))
		return
	}
	acceptOpts := &websocket.AcceptOptions{}
	if origin != "" {
		acceptOpts.OriginPatterns = []string{origin}
	}
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	session, err := s.deps.Browser.Ensure(ctx, sandboxID, profileRoot)
	if err != nil {
		if errors.Is(err, browser.ErrNotConfigured) {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
				"type": "error", "code": contracts.ErrSandboxUnavail,
				"message": "no chrome binary is configured on this server",
			}))
			return
		}
		s.log.ErrorContext(ctx, "cannot start the browser", "sandbox", sandboxID, "error", err.Error())
		_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
			"type": "error", "code": contracts.ErrInternal, "message": "cannot start the browser",
		}))
		return
	}

	// The viewer names the page it wants (PRD FR-11.7). An empty `tab` means the one
	// the browser opened with, which is what a single-tab viewer wants.
	tabID := r.URL.Query().Get("tab")
	if tabID == "" {
		tabID = session.DefaultTabID()
	}
	// Remember what the panel is showing. A screenshot is "save what I see", and
	// with several pages open the only thing that knows which one that is, is the
	// viewer that just connected (FR-11.8).
	session.SetActiveTab(tabID)

	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
		"type":    "ready",
		"browser": session.Snapshot(),
		"tab":     tabID,
	}))

	// Screencast and input are independent halves of the same socket: frames go
	// out on one goroutine while commands come in on this one, which is what
	// keeps a slow viewer from blocking input.
	go func() {
		err := session.TabScreencast(ctx, tabID, func(jpeg []byte, meta browser.Meta) {
			if len(jpeg) == 0 {
				return
			}
			writeCtx, writeCancel := context.WithTimeout(ctx, browserWriteWait)
			defer writeCancel()
			if werr := conn.Write(writeCtx, websocket.MessageBinary, jpeg); werr != nil {
				cancel()
				return
			}
			// The frame's own size lets the viewer map clicks back to page
			// coordinates without guessing.
			_ = conn.Write(writeCtx, websocket.MessageText, mustJSON(map[string]any{
				"type": "frame", "width": meta.DeviceWidth, "height": meta.DeviceHeight,
			}))
		})
		if err != nil && ctx.Err() == nil {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
				"type": "error", "code": contracts.ErrInternal, "message": err.Error(),
			}))
		}
	}()

	for {
		kind, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			continue
		}
		var cmd browserCommand
		if json.Unmarshal(payload, &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "navigate":
			if err := session.TabNavigate(ctx, tabID, cmd.URL); err != nil {
				_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
					"type": "error", "code": contracts.ErrInvalidArgument, "message": err.Error(),
				}))
			}
		case "input":
			if cmd.Input != nil {
				// FR-11.2 / FR-11.4: observation is the default, and input is dropped
				// rather than queued. A viewer who clicks in a read-only panel should see
				// nothing happen — with the mode switch right there saying why.
				if browserModeOf(sess.ID) != browserModeTakeover {
					s.log.DebugContext(ctx, "browser input ignored in observe mode",
						"session", sess.ID)
					break
				}
				if err := session.TabDispatch(ctx, tabID, *cmd.Input); err != nil {
					s.log.DebugContext(ctx, "browser input failed", "error", err.Error())
				}
			}
		case "resize":
			// A viewer that cannot see the whole page is worse than a small one:
			// keep the render size close to the panel it is displayed in.
			if err := session.TabResize(ctx, tabID, cmd.Width, cmd.Height); err != nil {
				s.log.DebugContext(ctx, "browser resize failed", "error", err.Error())
			}
		case "ping":
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{"type": "pong"}))
		}
	}
}
