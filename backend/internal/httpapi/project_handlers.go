package httpapi

import (
	"context"
	"errors"
	"fmt"
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

// repoBasename is the repository's own name: the last segment of an https URL,
// a git@host:path address or a local path, without a trailing `.git`.
func repoBasename(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if cut := strings.IndexAny(value, "?#"); cut >= 0 {
		value = value[:cut]
	}
	value = strings.TrimRight(value, "/")
	if len(value) >= 4 && strings.EqualFold(value[len(value)-4:], ".git") {
		value = value[:len(value)-4]
	}
	index := strings.LastIndexAny(value, `/\:`)
	name := strings.TrimSpace(value[index+1:])
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// deriveProjectName fills the name the user did not have to invent. A repository
// address already carries one; a blank workspace falls back to its date so two
// empty projects are still distinguishable in a list.
//
// The caller passes a UTC clock: the dialog previews this same name from the
// browser, and a deployment whose TZ is not UTC used to create a name one day
// off from the one the dialog promised (QA-16 minor).
func deriveProjectName(repoURL string, now time.Time) string {
	if name := repoBasename(repoURL); name != "" {
		return name
	}
	return "空白工作区 · " + now.Format("01-02")
}

// maxProjectNameSuffix bounds the search for a free derived name. A thousand
// projects of the same repository is not a case worth a query storm; past it the
// answer is a clear conflict rather than a request that never returns.
const maxProjectNameSuffix = 1000

// projectCreateAttempts is how many times a derived name is re-allocated after
// losing the insert race to another create. Two tabs creating the same
// repository at the same instant is the case; five tries is already generous.
const projectCreateAttempts = 5

// freeProjectName returns base when it is free, otherwise the first free
// base-2, base-3, … .
//
// The predicate, rather than the store, is what decides "free" so the same rule
// is used by tests and by the handler (the store compares case-insensitively).
func freeProjectName(base string, taken func(string) (bool, error)) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", apiErr(contracts.ErrInvalidArgument, "name is required")
	}
	for suffix := 1; suffix <= maxProjectNameSuffix; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		used, err := taken(candidate)
		if err != nil {
			return "", err
		}
		if !used {
			return candidate, nil
		}
	}
	return "", apiErr(contracts.ErrConflict,
		"项目名称「%s」及 %d 个带序号的名字都被占用了，请手动填一个名称", base, maxProjectNameSuffix-1)
}

// projectNameConflict is the 409 for a name the user typed themselves.
//
// A derived name gets a suffix instead (see freeProjectName); an explicit one
// does not, because silently creating "frontend-2" is not what "frontend" asked
// for. Naming the conflict is the useful half of refusing.
func projectNameConflict(name string) error {
	return withStatus(http.StatusConflict, contracts.ErrConflict,
		fmt.Sprintf("项目名称「%s」已存在（不区分大小写），请换一个名称", name))
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
	if err := validateRepoURL(in.RepoURL); err != nil {
		s.fail(w, r, err)
		return
	}
	repoURL := strings.TrimSpace(in.RepoURL)
	name := strings.TrimSpace(in.Name)
	// The name is optional on purpose: a repository URL is already a name, and
	// making the field required is how a project ends up called "test2".
	if name != "" {
		s.createNamedProject(w, r, user.ID, name, repoURL, in.DefaultBranch)
		return
	}

	// A derived name is the product's job to make unique: a second repository
	// with the same basename (`frontend`) used to be a dead end with a 409 the
	// user could not act on. Allocate the first free suffix instead, and
	// re-allocate if another create takes it between the check and the insert.
	base := deriveProjectName(repoURL, time.Now().UTC())
	var project *contracts.Project
	for attempt := 0; attempt < projectCreateAttempts; attempt++ {
		candidate, err := freeProjectName(base, func(candidate string) (bool, error) {
			return s.deps.Store.ProjectNameTaken(r.Context(), user.ID, candidate)
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		project, err = s.deps.Store.CreateProject(r.Context(), user.ID, store.NewProject{
			Name:          candidate,
			RepoURL:       repoURL,
			DefaultBranch: in.DefaultBranch,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrConflict) {
			s.fail(w, r, err)
			return
		}
		// The row someone else wrote is now visible; the next loop sees it.
		project = nil
	}
	if project == nil {
		s.fail(w, r, withStatus(http.StatusConflict, contracts.ErrConflict,
			fmt.Sprintf("项目名称「%s」已被占用，请重新提交或手动填写名称", base)))
		return
	}
	writeJSON(w, http.StatusCreated, projectResponse{Project: project})
}

// createNamedProject handles the branch where the user typed the name: a
// duplicate is refused by name rather than silently suffixed.
func (s *server) createNamedProject(w http.ResponseWriter, r *http.Request, userID, name, repoURL, branch string) {
	taken, err := s.deps.Store.ProjectNameTaken(r.Context(), userID, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if taken {
		s.fail(w, r, projectNameConflict(name))
		return
	}
	project, err := s.deps.Store.CreateProject(r.Context(), userID, store.NewProject{
		Name:          name,
		RepoURL:       repoURL,
		DefaultBranch: branch,
	})
	if err != nil {
		// The check above is a courtesy to the user; the unique index is the
		// gate. A row inserted in between still gets the named conflict instead
		// of the raw storage error.
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, projectNameConflict(name))
			return
		}
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
		// A duplicate name is the one conflict the user can fix in this dialog.
		// The store only knows it is a unique violation; naming the conflicting
		// project is the API's job, and it is the same sentence create answers
		// with — otherwise rename showed 「操作失败，请稍后重试」 and threw away the
		// only actionable fact (QA-16, FIX B).
		if in.Name != nil && errors.Is(err, store.ErrConflict) {
			s.fail(w, r, projectNameConflict(strings.TrimSpace(*in.Name)))
			return
		}
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
	project, err := s.deps.Store.Project(r.Context(), user.ID, projectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A live turn is not a sandbox state: deleting the row underneath it kills
	// work the user is watching and there is no undo. Refuse and say what to
	// stop first instead of stopping it for them.
	if running, err := s.deps.Store.RunningSessionCount(r.Context(), user.ID, projectID); err != nil {
		s.fail(w, r, err)
		return
	} else if running > 0 {
		s.fail(w, r, withStatus(http.StatusConflict, contracts.ErrConflict,
			fmt.Sprintf("项目「%s」还有 %d 个会话正在运行，请先停止这些回合再删除项目", project.Name, running)))
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
