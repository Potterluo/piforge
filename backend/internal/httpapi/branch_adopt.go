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
// (PRD FR-2.6). This is the second half of "try three approaches and pick one":
// the picking.
//
// The checkout semantics live in gitx.Adopt: commits are merged (or
// fast-forwarded) into the target first, and only the branch's remaining
// uncommitted work is applied as a working-tree patch. That ordering is what makes
// adopt work after the user presses the product's own commit button — the case
// that used to answer 409 "这个分支没有可采纳的改动".
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
		var conflict *gitx.MergeConflictError
		switch {
		case errors.Is(err, gitx.ErrNothingToAdopt):
			s.fail(w, r, apiErr(contracts.ErrConflict, "这个分支没有可采纳的改动：既没有领先基准的提交，也没有未提交的文件"))
		case errors.Is(err, gitx.ErrTargetDirty):
			s.fail(w, r, apiErr(contracts.ErrConflict,
				"目标会话的工作区有未提交的改动：先在目标会话里提交或撤销这些改动，再重试采纳（避免覆盖你正在写的内容）"))
		case errors.Is(err, gitx.ErrNoCommonBase):
			s.fail(w, r, apiErr(contracts.ErrConflict, "这个分支和目标的 checkout 没有共同历史，无法采纳"))
		case errors.As(err, &conflict):
			s.fail(w, r, apiErr(contracts.ErrConflict, "采纳时发生冲突：%s。%s",
				strings.Join(conflict.Files, "、"), conflictAdvice(conflict.Stage)))
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

// conflictAdvice says what actually happened to the target, because "conflict" in
// the target worktree and "conflict" in the branch's loose files leave the target
// in different states. A user must not be told "nothing changed" when half of the
// adopt did land.
func conflictAdvice(stage string) string {
	if stage == "working-tree" {
		return "分支的提交已经合并进目标，但这些未提交的改动没有应用，目标停在合并后的状态。请在分支上提交或撤销这些文件后再采纳。"
	}
	return "目标没有改动（这次合并已回滚）。请先在分支上解决这些文件的冲突，再重试采纳。"
}
