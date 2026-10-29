package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// screenshotAttempt bounds one capture. A page that will not answer must not eat
// the whole request budget: the retry below needs time of its own, and a hung
// background tab (which is what produced the 422s) should fail in seconds.
const screenshotAttempt = 10 * time.Second

// isBlankCapture reports whether a capture failure is "there was nothing on the
// page", which is deterministic and must not be retried into a busy message.
func isBlankCapture(err error) bool {
	return errors.Is(err, browser.ErrBlankCapture)
}

// handleBrowserScreenshot captures the sandbox's browser and writes the PNG into
// the session's control-plane artifacts directory (PRD FR-11.8).
//
// The capture is still a real round trip — what the panel shows becomes a file
// the user can preview and download — but it must not become the user's
// untracked work. The checkout stays clean; `screenshots/` inside a checkout is
// only ever a path from before this split, and still previews.
func (s *server) handleBrowserScreenshot(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.featureDenied(w, r, user.ID, string(store.FeatureBrowser)) {
		return
	}
	if s.deps.ScreenshotCapture == nil && (s.deps.Browser == nil || !s.deps.Browser.Available()) {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "no chrome binary is configured on this server"))
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
	profileRoot := filepath.Dir(worktree.Path)
	if sb, sbErr := s.deps.Store.Sandbox(r.Context(), user.ID, worktree.SandboxID); sbErr == nil && sb.RootDir != "" {
		profileRoot = sb.RootDir
	}

	ctx, cancel := detach(r.Context(), 20*time.Second)
	defer cancel()
	// The page the panel is showing, not the target Chrome attached to at launch.
	// The viewer passes its tab explicitly; a bare POST still follows the tab the
	// panel last showed, because the socket handler records it.
	tabID := strings.TrimSpace(r.URL.Query().Get("tab"))

	captureFn := s.deps.ScreenshotCapture
	if captureFn == nil {
		shot, release, err := s.deps.Browser.Acquire(ctx, worktree.SandboxID, profileRoot)
		if err != nil {
			if errors.Is(err, browser.ErrNotConfigured) {
				s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "no chrome binary is configured on this server"))
				return
			}
			// A browser that would not come up is unavailable, not a defect in this
			// handler: a 500 here sent people looking for a bug that was a slow start.
			s.log.WarnContext(r.Context(), "browser unavailable for a screenshot", "error", err.Error())
			s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "浏览器没能启动，稍后再试"))
			return
		}
		// The capture is a short attach; when it ends the idle policy may collect the
		// browser again unless a viewer is still watching it.
		defer release()
		captureFn = func(attemptCtx context.Context, _, _, tab string) ([]byte, error) {
			return shot.ScreenshotTab(attemptCtx, tab)
		}
	}

	capture := func() ([]byte, error) {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, screenshotAttempt)
		defer attemptCancel()
		return captureFn(attemptCtx, worktree.SandboxID, profileRoot, tabID)
	}

	// One retry, because the failure is usually "the page is mid-frame": a
	// screenshot taken while a screencast is streaming occasionally loses the race,
	// and asking a person to click the camera twice is not a design. A blank page
	// is not retried: it will be blank again, and the answer should say so.
	png, err := capture()
	if err != nil && !isBlankCapture(err) {
		time.Sleep(250 * time.Millisecond)
		png, err = capture()
	}
	if err != nil {
		// A blank capture and a busy browser are different answers. The first one
		// used to be written to the workspace as an all-white PNG and reported as
		// success, which is how the agent ended up "reading" about:blank.
		var blank *browser.BlankCaptureError
		if errors.As(err, &blank) {
			s.log.WarnContext(r.Context(), "refusing a blank browser screenshot",
				"url", blank.URL, "colours", blank.Colours, "reason", blank.Reason)
			if blank.URL == "about:blank" {
				s.fail(w, r, apiErr(contracts.ErrSandboxUnavail,
					"截图是空白页（about:blank）：先把浏览器导航到一个真实页面，再截图"))
				return
			}
			s.fail(w, r, apiErr(contracts.ErrSandboxUnavail,
				"截图是空白页：页面还没有画出内容，等它加载完再截图"))
			return
		}
		// A busy or slow browser is not an internal defect: say so, so the panel can
		// suggest trying again instead of showing a stack-trace-shaped error.
		s.log.WarnContext(r.Context(), "browser screenshot failed", "error", err.Error())
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "浏览器正忙，稍后再试一次"))
		return
	}

	// Milliseconds because people take several screenshots in a row, and a
	// one-second name silently overwrote the previous capture — the panel said
	// "saved" while the file had changed. The file is written under the session's
	// artifacts directory, not the checkout: a capture is not the user's work.
	artifactPath, err := s.saveScreenshot(ctx, sess.ID, png, time.Now().UTC())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"path":  artifactPath,
		"bytes": len(png),
	})
}
