package httpapi

import (
	"errors"
	"github.com/piforge/piforge/backend/internal/store"
	"net/http"
	"path"
	"path/filepath"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// handleBrowserScreenshot captures the sandbox's browser and writes the PNG into
// the checkout (PRD FR-11.8).
//
// The point is the round trip: what you see in the panel becomes a file the agent
// can read, in the same workspace it is already working in. It is therefore a
// mutation, and it is refused when the browser feature is off.
func (s *server) handleBrowserScreenshot(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.featureDenied(w, r, user.ID, string(store.FeatureBrowser)) {
		return
	}
	if s.deps.Browser == nil || !s.deps.Browser.Available() {
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
	shot, err := s.deps.Browser.Ensure(ctx, worktree.SandboxID, profileRoot)
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
	// One retry, because the failure is usually "the page is mid-frame": a
	// screenshot taken while a screencast is streaming occasionally loses the race,
	// and asking a person to click the camera twice is not a design.
	png, err := shot.Screenshot(ctx)
	if err != nil {
		time.Sleep(250 * time.Millisecond)
		png, err = shot.Screenshot(ctx)
	}
	if err != nil {
		// A busy or slow browser is not an internal defect: say so, so the panel can
		// suggest trying again instead of showing a stack-trace-shaped error.
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "浏览器正忙，稍后再试一次"))
		return
	}

	// `screenshots/` at the top of the checkout: predictable for the agent, and
	// easy to gitignore if a user wants to.
	rel := path.Join("screenshots", time.Now().UTC().Format("20060102-150405")+".png")
	if err := s.deps.Workspace.WriteFile(ctx, worktree.Path, rel, png); err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"path":  rel,
		"bytes": len(png),
	})
}
