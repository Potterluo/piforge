package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/piforge/piforge/backend/internal/auth"
	"net/http"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// Push and pull (PRD FR-8.7).
//
// Commits only existed on this machine: there was no way to send one to the remote or
// bring one down, so the last step of every task happened in a terminal — the one thing
// a web workbench exists to avoid.

// remoteRoot resolves the checkout a session is allowed to act on.
func (s *server) remoteRoot(w http.ResponseWriter, r *http.Request) (string, *contracts.Session, bool) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return "", nil, false
	}
	if sess.WorktreeID == "" {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "这个会话还没有 checkout，先发一条消息让它建起来"))
		return "", nil, false
	}
	wt, err := s.deps.Store.Worktree(r.Context(), user.ID, sess.WorktreeID)
	if err != nil {
		s.fail(w, r, err)
		return "", nil, false
	}
	return wt.Path, sess, true
}

// refuseRemoteDials is the G6 boundary: before git dials a checkout's remote,
// the deployment gets to say no.
//
// The remote is repository configuration, which the agent can rewrite inside the
// sandbox (`git remote set-url origin …`, or a pushurl, or a url.<base>.insteadOf
// rewrite). In host placement this process runs the push, so without this check
// the control plane dials a URL the tenant chose — from its own network
// position, with its own credentials. Deps.RemoteCheck is the deployment's
// answer (urlguard + the cross-tenant path rule, wired in cmd/piforged); nil
// means this deployment does not run git on the host, and there is nothing to
// check here.
func (s *server) refuseRemoteDials(w http.ResponseWriter, r *http.Request, userID, root string) bool {
	if s.deps.RemoteCheck == nil {
		return true
	}
	if err := s.deps.RemoteCheck(r.Context(), userID, root); err != nil {
		s.fail(w, r, err)
		return false
	}
	return true
}

// handleGitPush sends the session's branch to origin.
func (s *server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	root, sess, ok := s.remoteRoot(w, r)
	if !ok {
		return
	}
	if !s.refuseRemoteDials(w, r, sess.UserID, root) {
		return
	}
	ctx, cancel := detach(r.Context(), 120*time.Second)
	defer cancel()

	result, err := s.deps.Workspace.Push(ctx, root)
	if err != nil {
		s.failRemote(w, r, err, "推送失败")
		return
	}
	s.recordGitEvent("git.pushed", sess, result.Branch+" "+shortSHA(result.Commit))
	writeJSON(w, http.StatusOK, map[string]any{
		"branch": result.Branch,
		"commit": result.Commit,
	})
}

// handleGitPull brings the remote branch down, fast-forward only.
func (s *server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	root, sess, ok := s.remoteRoot(w, r)
	if !ok {
		return
	}
	if !s.refuseRemoteDials(w, r, sess.UserID, root) {
		return
	}
	ctx, cancel := detach(r.Context(), 120*time.Second)
	defer cancel()

	result, err := s.deps.Workspace.Pull(ctx, root)
	if err != nil {
		s.failRemote(w, r, err, "拉取失败")
		return
	}
	s.recordGitEvent("git.pulled", sess, result.Branch+" "+shortSHA(result.Commit))
	writeJSON(w, http.StatusOK, map[string]any{
		"branch":  result.Branch,
		"commit":  result.Commit,
		"changed": result.Changed,
	})
}

// handleGitRemote reports how far the branch is from its remote, so the UI can show a
// push button only when there is something to push.
func (s *server) handleGitRemote(w http.ResponseWriter, r *http.Request) {
	root, sess, ok := s.remoteRoot(w, r)
	if !ok {
		return
	}
	// The panel's "is there anything upstream?" fetches, so the same boundary
	// applies before the fetch: a rewritten origin must not be dialled just
	// because the user opened the Git tab.
	if !s.refuseRemoteDials(w, r, sess.UserID, root) {
		return
	}
	ctx, cancel := detach(r.Context(), 30*time.Second)
	defer cancel()

	// Fetch first, because ahead/behind is computed against the *tracking* ref: without
	// it, a remote commit nobody has fetched reads as "nothing behind" forever, and the
	// pull button always looks like it would do nothing. A failed fetch (offline, no
	// credentials) is not fatal — the numbers are then what the last fetch knows.
	_ = s.deps.Workspace.Fetch(ctx, root)

	ahead, behind, err := s.deps.Workspace.AheadBehind(ctx, root)
	if errors.Is(err, gitx.ErrNoRemote) {
		writeJSON(w, http.StatusOK, map[string]any{"remote": false, "ahead": 0, "behind": 0})
		return
	}
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "读取远端状态失败：%v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"remote": true, "ahead": ahead, "behind": behind})
}

// failRemote turns the two refusals a person can act on into sentences, and everything
// else into the honest "it failed" it is.
func (s *server) failRemote(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, gitx.ErrNoRemote):
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"这个 checkout 没有配置远端（origin），所以没有地方可以同步"))
	case errors.Is(err, gitx.ErrDiverged):
		s.fail(w, r, apiErr(contracts.ErrConflict,
			"本地分支和远端已经分叉，不能用快进方式拉取。请先在终端里合并或变基。"))
	default:
		s.log.ErrorContext(r.Context(), what, "error", err.Error())
		s.fail(w, r, apiErr(contracts.ErrInternal, "%s：%v", what, err))
	}
}

// recordGitEvent puts a line in the session's history so a push that happened at 3am is
// still visible in the transcript afterwards.
func (s *server) recordGitEvent(kind string, sess *contracts.Session, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.deps.Store.AppendEvent(ctx, sess.UserID, &contracts.Event{
		SessionID: sess.ID,
		Type:      kind,
		Payload:   json.RawMessage(`{"detail":"` + jsonEscape(detail) + `"}`),
		TS:        time.Now(),
	})
}

// shortSHA keeps an event readable; the full hash is in git.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func jsonEscape(in string) string {
	out := make([]rune, 0, len(in))
	for _, r := range in {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
