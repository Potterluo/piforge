package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// compareRow is one branch's column on the compare screen (PRD FR-2.5).
//
// "Tests" is deliberately absent: this deployment does not run a test suite, and a
// column that always said "—" would be a lie dressed as a feature. What it shows
// instead is what the agent actually did — how many turns, how many tool calls, how
// much it cost, and how big the change is.
type compareRow struct {
	SessionID    string    `json:"session_id"`
	Title        string    `json:"title"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	ModelRef     string    `json:"model_ref,omitempty"`
	LastActiveAt time.Time `json:"last_active_at"`
	UserMessages int       `json:"user_messages"`
	ToolCalls    int       `json:"tool_calls"`
	TokensIn     int64     `json:"tokens_in"`
	TokensOut    int64     `json:"tokens_out"`
	CostUSD      float64   `json:"cost_usd"`
	FilesChanged int       `json:"files_changed"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	// DiffError says why the change size is missing, instead of reporting zero.
	DiffError string `json:"diff_error,omitempty"`
}

// handleCompareSessions puts two or three branches side by side.
//
// The scope is one project and the sessions must belong to the caller: comparing
// across projects would compare different repositories, which means nothing.
func (s *server) handleCompareSessions(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	raw := r.URL.Query().Get("ids")
	ids := make([]string, 0, 4)
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "至少选两个会话才能对比"))
		return
	}
	if len(ids) > 3 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "一次最多对比三个会话"))
		return
	}

	rows := make([]compareRow, 0, len(ids))
	projectID := ""
	for _, id := range ids {
		sess, err := s.deps.Store.Session(r.Context(), user.ID, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if projectID == "" {
			projectID = sess.ProjectID
		} else if sess.ProjectID != projectID {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "只能对比同一个项目里的会话"))
			return
		}

		row := compareRow{
			SessionID:    sess.ID,
			Title:        sess.Title,
			Kind:         string(sess.Kind),
			Status:       string(sess.Status),
			ModelRef:     sess.ModelRef,
			LastActiveAt: sess.LastActiveAt,
		}
		if st, serr := s.deps.Store.SessionStats(r.Context(), user.ID, sess.ID); serr == nil && st != nil {
			row.UserMessages = st.UserMessages
			row.ToolCalls = st.ToolCalls
			row.TokensIn = st.TokensIn
			row.TokensOut = st.TokensOut
			row.CostUSD = st.CostUSD
		}

		// The change size is the one number a person actually compares on, and it has
		// to cover everything the branch did: staged work and new files are the common
		// case, and counting only unstaged tracked diffs reported 0/+0/-0 for a branch
		// full of work.
		if sess.WorktreeID != "" && s.deps.Workspace != nil {
			wt, werr := s.deps.Store.Worktree(r.Context(), user.ID, sess.WorktreeID)
			if werr == nil {
				ctx, cancel := detach(r.Context(), 20*time.Second)
				row.FilesChanged, row.Additions, row.Deletions, row.DiffError = s.changeSize(ctx, wt.Path)
				cancel()
			} else {
				row.DiffError = "没有 checkout"
			}
		} else {
			row.DiffError = "没有 checkout"
		}
		rows = append(rows, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// changeSize counts everything uncommitted in a checkout: staged, unstaged and new
// files. A branch's work is usually untracked or staged, so a diff scope of "unstaged"
// alone reported an empty change for a branch that had done a lot.
func (s *server) changeSize(ctx context.Context, root string) (files, additions, deletions int, diffErr string) {
	status, err := s.deps.Workspace.Status(ctx, root)
	if err != nil {
		return 0, 0, 0, "读取改动失败"
	}
	paths := map[string]bool{}
	for _, change := range status.Changes {
		paths[change.Path] = true
	}
	for _, scope := range []contracts.DiffScope{contracts.ScopeStaged, contracts.ScopeUnstaged} {
		diffs, derr := s.deps.Workspace.Diff(ctx, root, contracts.DiffRequest{Scope: scope})
		if derr != nil {
			return 0, 0, 0, "读取改动失败"
		}
		for _, diff := range diffs {
			paths[diff.Path] = true
			additions += diff.Additions
			deletions += diff.Deletions
		}
	}
	return len(paths), additions, deletions, ""
}
