package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// handleAdoptBranch carries a branch's work back to the session it came from
// (PRD FR-2.6). This is the second half of "try three approaches and pick one": the
// picking.
//
// It is deliberately not a merge. The two sessions live in separate worktrees of the
// same repository and may both have moved on; a cherry-pick of the branch's working
// tree is the operation a person means by "adopt this one".
func (s *server) handleAdoptBranch(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	branch, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if branch.Kind != contracts.KindBranch {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "只有分支可以采纳回主干"))
		return
	}
	if branch.ParentID == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "这个分支没有来源会话"))
		return
	}
	target, err := s.deps.Store.Session(r.Context(), user.ID, branch.ParentID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if branch.WorktreeID == "" || target.WorktreeID == "" {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "两边的 checkout 都还没有准备好"))
		return
	}
	sourceWT, err := s.deps.Store.Worktree(r.Context(), user.ID, branch.WorktreeID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	targetWT, err := s.deps.Store.Worktree(r.Context(), user.ID, target.WorktreeID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var in struct {
		Message string `json:"message"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		message = "adopt: " + strings.TrimSpace(branch.Label)
	}

	ctx, cancel := detach(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := s.deps.Workspace.Adopt(ctx, sourceWT.Path, targetWT.Path, message)
	if err != nil {
		switch {
		case errors.Is(err, gitx.ErrNothingToAdopt):
			s.fail(w, r, apiErr(contracts.ErrConflict, "这个分支没有可采纳的改动"))
		case errors.Is(err, gitx.ErrTargetDirty):
			s.fail(w, r, apiErr(contracts.ErrConflict, "目标 checkout 有未提交的改动，先提交或撤销再来"))
		default:
			s.fail(w, r, err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"adopted": result,
		"branch":  branch.ID,
		"target":  target.ID,
	})
}
