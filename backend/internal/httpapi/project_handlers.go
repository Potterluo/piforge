package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// scpLikeRepo is git's scp syntax: git@github.com:acme/repo.git. It has no
// scheme to parse, so it needs its own shape check.
var scpLikeRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

// validateRepoURL refuses a repository address the sandbox cannot clone.
//
// Empty is allowed on purpose (a blank workspace). Otherwise the product
// supports an https URL and git's scp-like user@host:path. An absolute local
// path is also accepted: the local sandbox provider clones those directly, and
// the git push/pull tests drive a bare repository on this machine with no
// network. A relative fragment such as "not-a-repo-url" matches none of them.
func validateRepoURL(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\t\r\n") {
		return apiErr(contracts.ErrInvalidArgument, "repo_url must be an https URL or git@host:path")
	}
	if filepath.IsAbs(value) {
		return nil
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return apiErr(contracts.ErrInvalidArgument, "repo_url must be an https URL or git@host:path")
	}
	if strings.HasPrefix(strings.ToLower(value), "https://") {
		parsed, err := url.Parse(value)
		if err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.Hostname() != "" && parsed.User == nil {
			return nil
		}
	}
	if scpLikeRepo.MatchString(value) {
		return nil
	}
	return apiErr(contracts.ErrInvalidArgument, "repo_url must be an https URL or git@host:path")
}

// projectsResponse is the list envelope; the client expects an object rather
// than a bare array so that pagination fields can be added later without a
// breaking change.
type projectsResponse struct {
	Projects []*contracts.Project `json:"projects"`
}

type projectResponse struct {
	Project *contracts.Project `json:"project"`
}

// handleListProjects lists the caller's projects with their sandbox state and
// session count.
func (s *server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	projects, err := s.deps.Store.ListProjects(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if projects == nil {
		projects = []*contracts.Project{}
	}
	writeJSON(w, http.StatusOK, projectsResponse{Projects: projects})
}

// handleCreateProject creates one project owned by the caller.
func (s *server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var in struct {
		Name          string `json:"name"`
		RepoURL       string `json:"repo_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "name is required"))
		return
	}
	if err := validateRepoURL(in.RepoURL); err != nil {
		s.fail(w, r, err)
		return
	}
	project, err := s.deps.Store.CreateProject(r.Context(), user.ID, store.NewProject{
		Name:          in.Name,
		RepoURL:       strings.TrimSpace(in.RepoURL),
		DefaultBranch: in.DefaultBranch,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectResponse{Project: project})
}

// handlePatchProject edits a project's name, repository address or base branch.
//
// The repository URL is accepted here for the recovery path: a project created
// before validation existed (or with a typo) has sessions that never got a
// worktree, and correcting the address is the action those sessions offer.
func (s *server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var in struct {
		Name          *string `json:"name"`
		RepoURL       *string `json:"repo_url"`
		DefaultBranch *string `json:"default_branch"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "name is required"))
		return
	}
	if in.RepoURL != nil {
		if err := validateRepoURL(*in.RepoURL); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	project, err := s.deps.Store.UpdateProject(r.Context(), user.ID, pathID(r), store.ProjectPatch{
		Name:          in.Name,
		RepoURL:       in.RepoURL,
		DefaultBranch: in.DefaultBranch,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse{Project: project})
}

// handleDeleteProject removes a project and everything that hangs off it.
//
// The sandbox container is torn down first on a best-effort basis: deleting the
// row without it would leave a running container nobody can reach from the UI,
// and failing the whole delete because Docker is unhappy would trap the user in
// exactly the broken state they are trying to leave.
func (s *server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	projectID := pathID(r)
	if _, err := s.deps.Store.Project(r.Context(), user.ID, projectID); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.deps.Sandbox != nil {
		if sandbox, err := s.deps.Store.SandboxForProject(r.Context(), user.ID, projectID); err == nil {
			ctx, cancel := detach(r.Context(), 30*time.Second)
			if derr := s.deps.Sandbox.Destroy(ctx, sandbox.ID); derr != nil {
				s.log.WarnContext(r.Context(), "delete project: sandbox destroy failed",
					"project", projectID, "sandbox", sandbox.ID, "error", derr.Error())
			}
			cancel()
		}
	}
	if err := s.deps.Store.DeleteProject(r.Context(), user.ID, projectID); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody{OK: true})
}

// handleGetProject loads one project. A project owned by another user reads as
// 404 (store.Project returns ErrNotFound), so the endpoint cannot be used to
// enumerate ids.
func (s *server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	project, err := s.deps.Store.Project(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse{Project: project})
}

// handleCreateSession creates a session under a project.
func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	projectID := pathID(r)

	// Prove ownership before parsing the body so a stranger gets the same 404
	// regardless of what they sent.
	if _, err := s.deps.Store.Project(r.Context(), user.ID, projectID); err != nil {
		s.fail(w, r, err)
		return
	}

	var in struct {
		Kind          string `json:"kind"`
		Title         string `json:"title"`
		Label         string `json:"label"`
		ModelRef      string `json:"model_ref"`
		ThinkingLevel string `json:"thinking_level"`
		WorktreeID    string `json:"worktree_id"`
		ParentID      string `json:"parent_id"`
		ForkEntryID   string `json:"fork_entry_id"`
		// Ephemeral creates scratch space (PRD FR-1.10): kept out of the sidebar and
		// deleted after a few hours.
		Ephemeral bool `json:"ephemeral"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	kind := contracts.SessionKind(in.Kind)
	if kind == "" {
		kind = contracts.KindMain
	}
	switch kind {
	case contracts.KindMain, contracts.KindBranch, contracts.KindSidecar:
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "unknown kind %q", in.Kind))
		return
	}

	sess, err := s.deps.Store.CreateSession(r.Context(), user.ID, store.NewSession{
		ProjectID:     projectID,
		WorktreeID:    in.WorktreeID,
		Kind:          kind,
		ParentID:      in.ParentID,
		ForkEntryID:   in.ForkEntryID,
		Label:         in.Label,
		Title:         in.Title,
		ModelRef:      in.ModelRef,
		ThinkingLevel: in.ThinkingLevel,
		Ephemeral:     in.Ephemeral,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Attach a checkout before answering so the UI can open the file tree
	// immediately. Best effort: a session that exists but has no worktree yet
	// is still usable for prompting, and the workspace endpoints say so.
	if refreshed, ok := s.provisionWorktree(r.Context(), user.ID, projectID, sess); ok {
		sess = refreshed
	}
	writeJSON(w, http.StatusCreated, sessionResponse{Session: sess})
}

// provisionWorktree gives the session a working directory through the optional
// Provisioner and returns the re-read session so worktree_id is populated.
//
// The provisioning work must survive the client hanging up (it can involve a
// clone), so it runs on a detached context with a generous bound. Errors are
// logged and swallowed on purpose.
func (s *server) provisionWorktree(ctx context.Context, userID, projectID string, sess *contracts.Session) (*contracts.Session, bool) {
	if s.deps.Provisioner == nil || sess == nil {
		return sess, false
	}
	project, err := s.deps.Store.Project(ctx, userID, projectID)
	if err != nil {
		s.log.WarnContext(ctx, "provision: project lookup failed",
			"session", sess.ID, "error", err.Error())
		return sess, false
	}

	pctx, cancel := detach(ctx, 5*time.Minute)
	_, perr := s.deps.Provisioner.EnsureWorktree(pctx, userID, project, sess)
	cancel()
	if perr != nil {
		s.log.WarnContext(ctx, "provision: no worktree for session",
			"session", sess.ID, "error", perr.Error())
		return sess, false
	}

	refreshed, err := s.deps.Store.Session(ctx, userID, sess.ID)
	if err != nil {
		return sess, false
	}
	return refreshed, true
}
