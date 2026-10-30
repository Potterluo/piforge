package httpapi

import (
	"errors"
	"github.com/piforge/piforge/backend/internal/gitx"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
)

// maxFileBytes bounds a manual file write. The workspace service is the real
// gatekeeper, but the HTTP layer must not buffer an arbitrary body.
const maxFileBytes = 8 << 20 // 8 MiB

// Every endpoint below resolves the session first and then hands the worktree
// root to contracts.Workspace. Path traversal protection lives inside the
// workspace implementation (PRD §6.3-坑6), not here — this layer only decides
// *which* root is legitimate.

// handleFileTree returns one directory level (or `depth` levels) of the file
// tree.
func (s *server) handleFileTree(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	depth := queryInt(r, "depth", 2)
	if depth <= 0 {
		depth = 1
	}
	// "显示忽略文件" is a server-side decision: the tree is filtered here, so the
	// switch must reach this call or it only ever hides more than the default.
	includeIgnored := r.URL.Query().Get("include_ignored") == "1"
	nodes, err := s.deps.Workspace.TreeWithOptions(
		r.Context(), root, r.URL.Query().Get("path"), depth, includeIgnored)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if nodes == nil {
		nodes = []*contracts.FileNode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// handleReadFile returns a preview payload, or the raw bytes when the client
// asks for a download.
func (s *server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "path is required"))
		return
	}

	// `download=1` streams the whole file; `inline=1` streams it too but lets the
	// browser render it (the PDF preview is an iframe of exactly this URL). Both
	// must open the raw bytes first: a preview is decoded as UTF-8 and truncated,
	// so routing a binary download through it fails before the download branch is
	// ever reached (and a large text download would be silently shortened).
	if queryBool(r, "download") || queryBool(r, "inline") {
		s.streamFile(w, r, root, path, queryBool(r, "inline"))
		return
	}

	content, err := s.deps.Workspace.ReadFile(r.Context(), root, path)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, content)
}

// streamFile serves one file byte-for-byte, without the preview's UTF-8 decode
// or truncation. It copies the reader into the response instead of buffering,
// so a download larger than memory still works.
func (s *server) streamFile(w http.ResponseWriter, r *http.Request, root, path string, inline bool) {
	body, size, err := s.deps.Workspace.OpenFile(r.Context(), root, path)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", fsx.GuessMime(path, true))
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if !inline {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+dispositionName(path)+`"`)
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		// The status and headers are already on the wire, so s.fail would append
		// a JSON error to the partial file. Log the broken stream instead.
		s.log.Warn("file download interrupted", "path", path, "error", err)
	}
}

// dispositionName is the file name for a Content-Disposition header. It keeps
// the last path element and drops the characters that could break out of the
// quoted string or the header line itself.
func dispositionName(p string) string {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '"', '\r', '\n', 0:
			return -1
		}
		return r
	}, pathBase(p))
	if name == "" {
		return "download"
	}
	return name
}

// handleWriteFile saves a manual edit. The body is raw text/plain, not JSON,
// because it is a file.
func (s *server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "path is required"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFileBytes+1))
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "could not read body: %v", err))
		return
	}
	if len(body) > maxFileBytes {
		s.fail(w, r, apiErr(contracts.ErrFileTooLarge, "file exceeds %d bytes", maxFileBytes))
		return
	}
	if err := s.deps.Workspace.WriteFile(r.Context(), root, path, body); err != nil {
		s.fail(w, r, err)
		return
	}

	writeOK(w)
}

// handleDeleteFile removes a file from the worktree.
func (s *server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "path is required"))
		return
	}
	if err := s.deps.Workspace.DeleteFile(r.Context(), root, path); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

// handleSearch runs a filename or content search.
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	query := r.URL.Query().Get("q")
	if strings.TrimSpace(query) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "q is required"))
		return
	}
	var byContent bool
	switch r.URL.Query().Get("type") {
	case "", "content":
		byContent = true
	case "name":
		byContent = false
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "type must be content or name"))
		return
	}
	results, err := s.deps.Workspace.Search(r.Context(), root, query, byContent)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if results == nil {
		results = []contracts.SearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleGitStatus returns branch, ahead/behind and the change list.
func (s *server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	status, err := s.deps.Workspace.Status(r.Context(), root)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleGitDiff returns a structured diff for one scope.
func (s *server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	scope, err := parseDiffScope(r.URL.Query().Get("scope"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A range without a left side has no meaning, and defaulting it here would
	// hide a client that forgot to send one. The right side defaults to HEAD.
	if scope == contracts.ScopeRange && strings.TrimSpace(r.URL.Query().Get("from")) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "from is required for scope=range"))
		return
	}
	diffs, err := s.deps.Workspace.Diff(r.Context(), root, contracts.DiffRequest{
		Scope:  scope,
		Path:   r.URL.Query().Get("path"),
		Commit: r.URL.Query().Get("commit"),
		Base:   r.URL.Query().Get("base"),
		From:   r.URL.Query().Get("from"),
		To:     r.URL.Query().Get("to"),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if diffs == nil {
		diffs = []contracts.FileDiff{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"diffs": diffs})
}

// handleGitLog returns a page of the commit DAG.
func (s *server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	limit := queryInt(r, "limit", 50)
	graph, err := s.deps.Workspace.Log(r.Context(), root, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

// handleGitStage stages the given paths.
func (s *server) handleGitStage(w http.ResponseWriter, r *http.Request) {
	s.gitPathsOp(w, r, func(root string, paths []string) error {
		return s.deps.Workspace.Stage(r.Context(), root, paths)
	})
}

// handleGitUnstage unstages the given paths.
func (s *server) handleGitUnstage(w http.ResponseWriter, r *http.Request) {
	s.gitPathsOp(w, r, func(root string, paths []string) error {
		return s.deps.Workspace.Unstage(r.Context(), root, paths)
	})
}

// gitPathsOp is the shared body of stage/unstage.
func (s *server) gitPathsOp(w http.ResponseWriter, r *http.Request, op func(root string, paths []string) error) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(in.Paths) == 0 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "paths is required"))
		return
	}
	if err := op(root, in.Paths); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

// handleGitCommit creates a commit and returns its hash.
func (s *server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	var in struct {
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
		Amend   bool     `json:"amend"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "message is required"))
		return
	}
	sha, err := s.deps.Workspace.Commit(r.Context(), root, contracts.CommitRequest{
		Message: in.Message, Paths: in.Paths, Amend: in.Amend,
	})
	if err != nil {
		// "Nothing to commit" is a state the user can fix by staging something, not a
		// server fault, and git's own sentence does not belong in a 500.
		if errors.Is(err, gitx.ErrNothingToCommit) {
			s.fail(w, r, apiErr(contracts.ErrConflict, "没有可提交的改动：先暂存一些文件，或修改后再试"))
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"commit": sha})
}

// handleGitBranch creates a branch.
func (s *server) handleGitBranch(w http.ResponseWriter, r *http.Request) {
	name, root, ok := s.branchRequest(w, r)
	if !ok {
		return
	}
	if err := s.deps.Workspace.CreateBranch(r.Context(), root, name); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

// handleGitCheckout switches branches.
func (s *server) handleGitCheckout(w http.ResponseWriter, r *http.Request) {
	name, root, ok := s.branchRequest(w, r)
	if !ok {
		return
	}
	if err := s.deps.Workspace.Checkout(r.Context(), root, name); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

// branchRequest parses the {"name": "..."} body shared by branch/checkout.
func (s *server) branchRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return "", "", false
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return "", "", false
	}
	if strings.TrimSpace(in.Name) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "name is required"))
		return "", "", false
	}
	return in.Name, root, true
}

// rootOrFail resolves {id} -> session -> worktree root, writing the error
// response itself when it cannot.
func (s *server) rootOrFail(w http.ResponseWriter, r *http.Request) (string, bool) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
	root, err := s.worktreeRoot(r.Context(), user.ID, sess)
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
	return root, true
}

// parseDiffScope validates ?scope= against the DiffScope constants.
func parseDiffScope(raw string) (contracts.DiffScope, error) {
	switch contracts.DiffScope(raw) {
	case "":
		return contracts.ScopeUnstaged, nil
	case contracts.ScopeUnstaged, contracts.ScopeStaged, contracts.ScopeBranch,
		contracts.ScopeCommit, contracts.ScopeRange:
		return contracts.DiffScope(raw), nil
	}
	return "", apiErr(contracts.ErrInvalidArgument,
		"scope must be unstaged, staged, branch, commit or range")
}

// pathBase returns the last path element for a Content-Disposition filename.
func pathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 && i+1 < len(p) {
		return p[i+1:]
	}
	return p
}
