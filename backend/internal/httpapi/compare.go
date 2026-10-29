package httpapi

import (
	"context"
	"errors"
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
//
// The change size is split into two numbers on purpose. A branch that committed its
// work and a branch that left it in the working tree are both real attempts, and
// the screen used to report "0 个文件 / +0 -0" for the first one. FilesChanged /
// Additions / Deletions are the totals (union of paths); the Committed* and
// Uncommitted* fields say which half each number came from so the UI can label it.
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
	// Inherited* is the part of the rows above that came from the conversation a
	// clone or fork started from. Runs live on the session that ran them, so a
	// freshly cloned branch used to report 0 turns / 0 tokens while showing a full
	// transcript.
	InheritedUserMessages int     `json:"inherited_user_messages"`
	InheritedToolCalls    int     `json:"inherited_tool_calls"`
	InheritedTokensIn     int64   `json:"inherited_tokens_in"`
	InheritedTokensOut    int64   `json:"inherited_tokens_out"`
	InheritedCostUSD      float64 `json:"inherited_cost_usd"`
	FilesChanged          int     `json:"files_changed"`
	Additions             int     `json:"additions"`
	Deletions             int     `json:"deletions"`
	CommittedFiles        int     `json:"committed_files"`
	CommittedAdditions    int     `json:"committed_additions"`
	CommittedDeletions    int     `json:"committed_deletions"`
	UncommittedFiles      int     `json:"uncommitted_files"`
	UncommittedAdditions  int     `json:"uncommitted_additions"`
	UncommittedDeletions  int     `json:"uncommitted_deletions"`
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
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "不能重复对比同一个会话"))
			return
		}
		seen[id] = struct{}{}
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
		// A cloned or forked session copies the transcript but not the runs, so its
		// own usage starts at zero. Add the ancestors' usage once each; the row then
		// describes the attempt the user is looking at, not only the turns it ran by
		// itself.
		if inherited, ierr := s.inheritedStats(r.Context(), user.ID, sess); ierr != nil {
			s.log.WarnContext(r.Context(), "could not read inherited usage",
				"session", sess.ID, "error", ierr.Error())
		} else {
			row.InheritedUserMessages = inherited.UserMessages
			row.InheritedToolCalls = inherited.ToolCalls
			row.InheritedTokensIn = inherited.TokensIn
			row.InheritedTokensOut = inherited.TokensOut
			row.InheritedCostUSD = inherited.CostUSD
			row.UserMessages += inherited.UserMessages
			row.ToolCalls += inherited.ToolCalls
			row.TokensIn += inherited.TokensIn
			row.TokensOut += inherited.TokensOut
			row.CostUSD += inherited.CostUSD
		}

		// The change size is the one number a person actually compares on. It has to
		// be "the branch versus the trunk it came from", not "the files that happen
		// to be uncommitted right now": committed work was invisible, so a branch
		// full of commits read as "0 个文件".
		if sess.WorktreeID != "" && s.deps.Workspace != nil {
			wt, werr := s.deps.Store.Worktree(r.Context(), user.ID, sess.WorktreeID)
			if werr == nil {
				ctx, cancel := detach(r.Context(), 30*time.Second)
				committed, dirty, baseErr := s.compareChanges(ctx, user.ID, sess, wt.Path)
				cancel()
				row.CommittedFiles = committed.files()
				row.CommittedAdditions = committed.additions
				row.CommittedDeletions = committed.deletions
				row.UncommittedFiles = dirty.files()
				row.UncommittedAdditions = dirty.additions
				row.UncommittedDeletions = dirty.deletions
				union := make(map[string]bool, len(committed.paths)+len(dirty.paths))
				for path := range committed.paths {
					union[path] = true
				}
				for path := range dirty.paths {
					union[path] = true
				}
				row.FilesChanged = len(union)
				row.Additions = committed.additions + dirty.additions
				row.Deletions = committed.deletions + dirty.deletions
				switch {
				case baseErr != nil:
					row.DiffError = "读取基准分支失败，改动只含未提交部分"
				case committed.err != nil:
					row.DiffError = "读取已提交改动失败"
				case dirty.err != nil:
					row.DiffError = "读取未提交改动失败"
				}
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

// changeCount is one half of a comparison: a set of paths plus line totals.
type changeCount struct {
	paths     map[string]bool
	additions int
	deletions int
	err       error
}

func (c changeCount) files() int { return len(c.paths) }

// compareChanges measures a session's branch against the trunk it grew from.
//
//	committed   = git diff <base>...HEAD  (three-dot: changes since the merge base)
//	uncommitted = staged + unstaged + untracked files in the working tree
//
// The two are deliberately separate: the screen has to be able to say "1 个文件
// 已提交 · 1 个未提交" instead of collapsing a committed branch to zero. baseErr
// means the trunk could not be resolved, in which case the committed half is empty
// and the caller says so rather than pretending the branch changed nothing.
func (s *server) compareChanges(ctx context.Context, userID string, sess *contracts.Session, root string) (committed, dirty changeCount, baseErr error) {
	committed = newChangeCount()
	dirty = newChangeCount()

	baseRef, baseErr := s.compareBase(ctx, userID, sess)
	if baseErr != nil || baseRef == "" {
		baseErr = errNoCompareBase
	} else if diffs, derr := s.deps.Workspace.Diff(ctx, root, contracts.DiffRequest{
		Scope: contracts.ScopeBranch,
		Base:  baseRef,
	}); derr != nil {
		committed.err = derr
	} else {
		addDiffs(&committed, diffs)
	}

	// Uncommitted tracked changes: staged is index-vs-HEAD, unstaged is
	// worktree-vs-index. Together they cover every tracked edit.
	for _, scope := range []contracts.DiffScope{contracts.ScopeStaged, contracts.ScopeUnstaged} {
		if diffs, derr := s.deps.Workspace.Diff(ctx, root, contracts.DiffRequest{Scope: scope}); derr != nil {
			dirty.err = derr
		} else {
			addDiffs(&dirty, diffs)
		}
	}
	// Files git has never seen produce no `git diff` output at all, so a branch that
	// only created files would still read as zero. Count them from the status list.
	status, serr := s.deps.Workspace.Status(ctx, root)
	if serr != nil {
		dirty.err = serr
		return committed, dirty, baseErr
	}
	for _, change := range status.Changes {
		if change.Index != "?" && change.Worktree != "?" {
			continue
		}
		s.countUntracked(ctx, root, change.Path, &dirty)
	}
	return committed, dirty, baseErr
}

// compareBase returns the commit a session should be compared against: the HEAD of
// the root session's checkout. For the root session itself that is its own HEAD, so
// its column shows only what is uncommitted. Returning "" means "no trunk
// available" and the caller reports the missing half instead of a fake zero.
func (s *server) compareBase(ctx context.Context, userID string, sess *contracts.Session) (string, error) {
	root := sess
	if sess.RootID != "" && sess.RootID != sess.ID {
		loaded, err := s.deps.Store.Session(ctx, userID, sess.RootID)
		if err != nil {
			return "", err
		}
		root = loaded
	}
	if root.WorktreeID == "" {
		return "", errNoCompareBase
	}
	wt, err := s.deps.Store.Worktree(ctx, userID, root.WorktreeID)
	if err != nil {
		return "", err
	}
	graph, err := s.deps.Workspace.Log(ctx, wt.Path, 1, "")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(graph.Head), nil
}

// inheritedStats sums the usage of every ancestor session once. Walks the parent
// chain so a clone of a fork gets both generations; a cycle (which should not
// exist) is bounded rather than trusted.
func (s *server) inheritedStats(ctx context.Context, userID string, sess *contracts.Session) (contracts.SessionStats, error) {
	var total contracts.SessionStats
	seen := map[string]bool{sess.ID: true}
	parentID := sess.ParentID
	for depth := 0; parentID != "" && depth < 32; depth++ {
		if seen[parentID] {
			break
		}
		seen[parentID] = true
		parent, err := s.deps.Store.Session(ctx, userID, parentID)
		if err != nil {
			return total, err
		}
		st, serr := s.deps.Store.SessionStats(ctx, userID, parent.ID)
		if serr != nil {
			return total, serr
		}
		if st != nil {
			total.UserMessages += st.UserMessages
			total.ToolCalls += st.ToolCalls
			total.TokensIn += st.TokensIn
			total.TokensOut += st.TokensOut
			total.CostUSD += st.CostUSD
		}
		parentID = parent.ParentID
	}
	return total, nil
}

func newChangeCount() changeCount {
	return changeCount{paths: map[string]bool{}}
}

func addDiffs(c *changeCount, diffs []contracts.FileDiff) {
	for _, diff := range diffs {
		path := strings.TrimSpace(diff.Path)
		if path == "" {
			path = strings.TrimSpace(diff.OldPath)
		}
		if path != "" {
			c.paths[path] = true
		}
		c.additions += diff.Additions
		c.deletions += diff.Deletions
	}
}

// countUntracked adds one untracked path. git reports an untracked directory as a
// single "dir/" entry, so a branch that created a folder would count as one file
// with zero lines; expand it into the files it contains.
func (s *server) countUntracked(ctx context.Context, root, path string, c *changeCount) {
	if !strings.HasSuffix(path, "/") {
		c.paths[path] = true
		c.additions += s.countLines(ctx, root, path)
		return
	}
	nodes, err := s.deps.Workspace.Tree(ctx, root, strings.TrimSuffix(path, "/"), 64)
	if err != nil {
		c.paths[path] = true
		return
	}
	walkUntracked(ctx, s, root, nodes, c, 0)
}

func walkUntracked(ctx context.Context, s *server, root string, nodes []*contracts.FileNode, c *changeCount, depth int) {
	if depth > 32 {
		return
	}
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.IsDir {
			walkUntracked(ctx, s, root, node.Children, c, depth+1)
			continue
		}
		path := strings.TrimSpace(node.Path)
		if path == "" {
			continue
		}
		c.paths[path] = true
		c.additions += s.countLines(ctx, root, path)
	}
}

// countLines counts the lines an untracked file would add. A binary file or one too
// large to preview contributes its path but no line count, which is honest: git
// would say "binary files differ" rather than count lines too.
func (s *server) countLines(ctx context.Context, root, path string) int {
	content, err := s.deps.Workspace.ReadFile(ctx, root, path)
	if err != nil || content == nil || content.Binary {
		return 0
	}
	if content.Body == "" {
		return 0
	}
	lines := strings.Count(content.Body, "\n")
	if !strings.HasSuffix(content.Body, "\n") {
		lines++
	}
	return lines
}

// errNoCompareBase is a sentinel: the trunk this session grew from has no usable
// checkout, so the committed half cannot be measured.
var errNoCompareBase = errors.New("no compare base")
