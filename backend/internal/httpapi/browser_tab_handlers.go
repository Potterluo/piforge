package httpapi

import (
	"net/http"
	"path/filepath"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// browserFor resolves the sandbox's browser session for a request, the same way the
// socket handler does: one Chrome per sandbox, profile outside the checkout.
func (s *server) browserFor(r *http.Request) (*browser.Session, string, error) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		return nil, "", err
	}
	if sess.WorktreeID == "" {
		return nil, "", apiErr(contracts.ErrSandboxUnavail, "the session has no checkout yet")
	}
	worktree, err := s.deps.Store.Worktree(r.Context(), user.ID, sess.WorktreeID)
	if err != nil {
		return nil, "", err
	}
	profileRoot := filepath.Dir(worktree.Path)
	if sb, sbErr := s.deps.Store.Sandbox(r.Context(), user.ID, worktree.SandboxID); sbErr == nil && sb.RootDir != "" {
		profileRoot = sb.RootDir
	}
	ctx, cancel := detach(r.Context(), 25*time.Second)
	defer cancel()
	bn, err := s.deps.Browser.Ensure(ctx, worktree.SandboxID, profileRoot)
	return bn, worktree.SandboxID, err
}

// handleListBrowserTabs lists the pages of the sandbox browser (PRD FR-11.7).
func (s *server) handleListBrowserTabs(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.featureDenied(w, r, user.ID, string(store.FeatureBrowser)) {
		return
	}
	if s.deps.Browser == nil || !s.deps.Browser.Configured() {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "no chrome binary is configured on this server"))
		return
	}
	bn, _, err := s.browserFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx, cancel := detach(r.Context(), 10*time.Second)
	defer cancel()
	tabs, err := bn.Tabs(ctx)
	if err != nil {
		s.log.WarnContext(r.Context(), "cannot list browser tabs", "error", err.Error())
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "浏览器暂时不可用，稍后再试"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tabs": tabs, "default_tab": bn.DefaultTabID()})
}

// handleOpenBrowserTab opens a page.
func (s *server) handleOpenBrowserTab(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.featureDenied(w, r, user.ID, string(store.FeatureBrowser)) {
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	bn, _, err := s.browserFor(r)
	if err != nil {
		s.log.WarnContext(r.Context(), "browser unavailable for a new tab", "error", err.Error())
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "浏览器没能启动，稍后再试"))
		return
	}
	ctx, cancel := detach(r.Context(), 20*time.Second)
	defer cancel()
	tab, err := bn.OpenTab(ctx, in.URL)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"tab": tab})
}

// handleCloseBrowserTab closes one page, refusing to close the last one.
func (s *server) handleCloseBrowserTab(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if s.featureDenied(w, r, user.ID, string(store.FeatureBrowser)) {
		return
	}
	tabID := r.PathValue("tab")
	if tabID == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "which tab?"))
		return
	}
	bn, _, err := s.browserFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx, cancel := detach(r.Context(), 10*time.Second)
	defer cancel()
	if err := bn.CloseTab(ctx, tabID); err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
