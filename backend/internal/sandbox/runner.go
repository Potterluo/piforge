package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The runner provider: container lifecycle over HTTP, no Docker CLI.
//
// The Docker provider owns the daemon the way a single-host deployment always
// did: it shells out to the CLI. That is exactly the dependency a multi-service
// deployment must not hand the control plane (docs/SANDBOX-DEPLOY.md §1), because
// /var/run/docker.sock is host root. Runner is the same provider with the CLI
// moved one process away: cmd/sandbox-runner holds the socket and exposes six
// operations, and this type calls them.
//
// What stays the same is everything the rest of the product relies on:
//
//   - the container name is still derived from (user, project) and the workspace
//     is still <DataRoot>/<user>/<project>, because the runner runs the same
//     sandbox.Docker code — this file implements none of those rules;
//   - Ensure is idempotent per pair (the runner adopts or creates; a retry is
//     a no-op);
//   - Get reports the provider's state, with ErrNotFound for a sandbox the
//     runner does not have;
//   - Destroy removes the container and keeps the data directory;
//   - RootDir is the host directory of the pair and ContainerPath maps it into
//     the container, so provision, the file panel and the terminal keep working
//     against a shared data directory.
//
// What it does not do: Exec. The runner deliberately exposes no exec endpoint
// (a shell over the lifecycle service would be a second, weaker copy of the
// sandbox's own /v1/shell/exec), and the two production paths that need a
// command in the sandbox — the checkout and the branch worktree — run inside
// the runner through sandbox.Docker. Exec is debug-only (docs/SANDBOX-DEPLOY.md
// §0.6), and this implementation says so instead of pretending.

// Remote is the part of a container-backed provider the panels need outside the
// lifecycle: the endpoint of a sandbox's HTTP API, and the path one of its host
// files has inside the container. *Docker and *Runner both implement it, and
// cmd/piforged holds this interface instead of the concrete type so the browser,
// the terminal and the file panel do not care which process created the
// container.
type Remote interface {
	Endpoint(ctx context.Context, sandboxID string) (string, error)
	ContainerPath(hostPath string) (string, error)
}

var (
	_ contracts.SandboxProvider = (*Runner)(nil)
	_ contracts.WorktreeSandbox = (*Runner)(nil)
	_ Remote                    = (*Runner)(nil)
)

// Timeouts on the client side. They are not retries: a sandbox that takes longer
// than this is reported as a timeout with the step named, because a panel request
// that hangs is worse than one that fails.
const (
	// runnerRequestTimeout bounds one call that only reads or changes state.
	runnerRequestTimeout = 2 * time.Minute
	// runnerEnsureTimeout bounds a create: the runner may run `git clone` inside
	// the new container and waits up to checkoutAPIWait for its API to answer.
	runnerEnsureTimeout = 15 * time.Minute
	// runnerWorktreeTimeout bounds one branch worktree: git in an existing
	// container, no clone.
	runnerWorktreeTimeout = 5 * time.Minute
	// runnerMaxResponse bounds a response body. Records are tiny; this only stops
	// a broken runner from being read into memory forever.
	runnerMaxResponse = 1 << 20
)

// RunnerConfig configures the HTTP provider.
type RunnerConfig struct {
	// BaseURL is the runner's base URL, e.g. http://sandbox-runner:7801. Required.
	BaseURL string
	// Token is the shared bearer token the runner checks. Required.
	Token string
	// DataRoot is this control plane's view of the sandbox root
	// (<dataDir>/sandboxes). ContainerPath answers from it, so it has to name the
	// same directory the runner reports in RootDir — one shared volume, one path.
	DataRoot string
	// Image is reported in contracts.Sandbox.Image and sent with every create.
	Image string
	// APIKey is an optional *fixed* key for every sandbox this deployment creates
	// (-sandbox-api-key). Empty — the default — means the runner generates a key
	// per container and reports it in the create record, which is what makes one
	// leaked key useless against another tenant (see key.go).
	//
	// A fixed value is for debugging one deployment, not for tenants: it is sent
	// with every create and with a worktree request.
	APIKey string
	// CPUs and Memory cap the container per sandbox; empty leaves the runner's
	// deployment defaults in charge.
	CPUs   string
	Memory string
	// Network is the Docker network a created container joins. Empty leaves the
	// runner's default in charge; when the runner reports container endpoints this
	// network has to be one it can resolve names on.
	Network string
	// APIPort is the container port that serves the sandbox API (8080). Zero
	// leaves the runner's default in charge.
	APIPort int
	// Timeout bounds one read/change call (default runnerRequestTimeout).
	Timeout time.Duration
	// EnsureTimeout bounds one create (default runnerEnsureTimeout).
	EnsureTimeout time.Duration
	// WorktreeTimeout bounds one worktree creation (default runnerWorktreeTimeout).
	WorktreeTimeout time.Duration
	// Client overrides the HTTP client. Tests set it; production does not.
	Client *http.Client
}

// Runner implements contracts.SandboxProvider against a sandbox-runner service.
type Runner struct {
	cfg     RunnerConfig
	baseURL string
	client  *http.Client
}

// NewRunner builds the HTTP provider.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("sandbox: runner: a base URL is required")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("sandbox: runner: base URL %q: %w", cfg.BaseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("sandbox: runner: base URL %q must be http or https", cfg.BaseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("sandbox: runner: base URL %q has no host", cfg.BaseURL)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("sandbox: runner: a token is required (the runner rejects unauthenticated requests)")
	}
	if strings.TrimSpace(cfg.DataRoot) == "" {
		return nil, fmt.Errorf("sandbox: runner: a data root is required")
	}
	root, err := filepath.Abs(cfg.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("sandbox: runner: abs data root %q: %w", cfg.DataRoot, err)
	}
	cfg.DataRoot = filepath.Clean(root)
	if cfg.Timeout <= 0 {
		cfg.Timeout = runnerRequestTimeout
	}
	if cfg.EnsureTimeout <= 0 {
		cfg.EnsureTimeout = runnerEnsureTimeout
	}
	if cfg.WorktreeTimeout <= 0 {
		cfg.WorktreeTimeout = runnerWorktreeTimeout
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	return &Runner{cfg: cfg, baseURL: cfg.BaseURL, client: client}, nil
}

// Name identifies the provider in the sandbox row.
func (r *Runner) Name() string { return "runner" }

// Ensure asks the runner for the pair's sandbox and returns its state.
//
// The runner runs the same Docker provider, so this is idempotent per (user,
// project) for the same reason: the container name is derived from the pair.
func (r *Runner) Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.EnsureTimeout)
	defer cancel()

	request := RunnerRequest{
		UserID:    userID,
		ProjectID: projectID,
		Image:     r.cfg.Image,
		RepoURL:   repoURL,
		Branch:    branch,
		APIPort:   r.cfg.APIPort,
		APIKey:    r.cfg.APIKey,
		CPUs:      r.cfg.CPUs,
		Memory:    r.cfg.Memory,
		Network:   r.cfg.Network,
	}
	var record RunnerRecord
	if err := r.do(ctx, http.MethodPost, "/v1/sandboxes", request, &record); err != nil {
		return nil, fmt.Errorf("sandbox: runner: create %s/%s: %w", userID, projectID, err)
	}
	sb, err := r.sandbox(record)
	if err != nil {
		return nil, err
	}
	sb.UserID, sb.ProjectID = userID, projectID
	// The key the runner's provider created the container with. It is persisted
	// with the sandbox row (sealed) and is the only copy this process will ever
	// need: every later call presents it to the container.
	sb.SandboxAPIKey = record.APIKey
	now := time.Now().UTC()
	sb.CreatedAt, sb.LastActiveAt = now, now
	r.warnAboutDataRoot(record, userID, projectID)
	return sb, nil
}

// Get reports the runner's state for one sandbox.
func (r *Runner) Get(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	record, err := r.fetch(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return r.sandbox(record)
}

// Pause stops the container. The runner leaves its filesystem and its workspace
// alone, which is what "paused, not deleted" has to mean.
func (r *Runner) Pause(ctx context.Context, sandboxID string) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	if err := r.do(ctx, http.MethodPost, sandboxResource(sandboxID, "pause"), nil, nil); err != nil {
		return fmt.Errorf("sandbox: runner: pause %s: %w", sandboxID, err)
	}
	return nil
}

// Resume starts a stopped container and reports its state.
func (r *Runner) Resume(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	var record RunnerRecord
	if err := r.do(ctx, http.MethodPost, sandboxResource(sandboxID, "resume"), nil, &record); err != nil {
		return nil, fmt.Errorf("sandbox: runner: resume %s: %w", sandboxID, err)
	}
	return r.sandbox(record)
}

// Destroy removes the container and keeps the data directory: deleting somebody's
// uncommitted work is not something a sandbox teardown should decide.
func (r *Runner) Destroy(ctx context.Context, sandboxID string) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	if err := r.do(ctx, http.MethodDelete, sandboxResource(sandboxID, ""), nil, nil); err != nil {
		return fmt.Errorf("sandbox: runner: destroy %s: %w", sandboxID, err)
	}
	return nil
}

// Exec is not part of the runner contract.
func (r *Runner) Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error) {
	return "", fmt.Errorf("sandbox: runner: Exec is not exposed by the runner API; commands inside a sandbox go through its own shell API (internal/aio), and the lifecycle service deliberately has no exec endpoint")
}

// AddWorktree creates a branch session's linked worktree inside the container, as
// the sandbox user (contracts.WorktreeSandbox).
func (r *Runner) AddWorktree(ctx context.Context, sandboxID, repoDir, worktreePath, branch string) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WorktreeTimeout)
	defer cancel()
	request := RunnerWorktreeRequest{
		RepoDir: repoDir,
		Path:    worktreePath,
		Branch:  branch,
		APIKey:  r.cfg.APIKey,
	}
	if err := r.do(ctx, http.MethodPost, sandboxResource(sandboxID, "worktrees"), request, nil); err != nil {
		return fmt.Errorf("sandbox: runner: add worktree %s: %w", worktreePath, err)
	}
	return nil
}

// Endpoint reports the address that serves the sandbox API.
//
// It asks the runner rather than remembering: the address is decided when the
// container is created and survives a control-plane restart, and a value kept in
// memory would be wrong after either.
func (r *Runner) Endpoint(ctx context.Context, sandboxID string) (string, error) {
	record, err := r.fetch(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(record.Endpoint) == "" {
		return "", fmt.Errorf("sandbox: runner: sandbox %s has no endpoint (the runner published no API port)", sandboxID)
	}
	return record.Endpoint, nil
}

// ContainerPath maps a host path inside one project's workspace to the path the
// same file has inside that project's container. It is the shared rule from
// docker.go: the runner does not answer this, and does not have to.
func (r *Runner) ContainerPath(hostPath string) (string, error) {
	mapped, err := containerPathFor(r.cfg.DataRoot, hostPath)
	if err != nil {
		return "", fmt.Errorf("sandbox: runner: %w", err)
	}
	return mapped, nil
}

// fetch reads one sandbox record.
func (r *Runner) fetch(ctx context.Context, sandboxID string) (RunnerRecord, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return RunnerRecord{}, fmt.Errorf("%w: empty sandbox id", ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	var record RunnerRecord
	if err := r.do(ctx, http.MethodGet, sandboxResource(sandboxID, ""), nil, &record); err != nil {
		return RunnerRecord{}, fmt.Errorf("sandbox: runner: get %s: %w", sandboxID, err)
	}
	return record, nil
}

// sandbox turns a wire record into the contract the rest of the code uses.
func (r *Runner) sandbox(record RunnerRecord) (*contracts.Sandbox, error) {
	if strings.TrimSpace(record.ID) == "" {
		return nil, fmt.Errorf("sandbox: runner: the runner returned a record with no id")
	}
	if root := strings.TrimSpace(record.ContainerPathRoot); root != "" && path.Clean(root) != ContainerWorkspace {
		// The control plane maps host paths into the container itself; a runner
		// that mounts somewhere else would make every panel address the wrong tree.
		return nil, fmt.Errorf("sandbox: runner: sandbox %s mounts its workspace at %s, but this build maps paths to %s", record.ID, root, ContainerWorkspace)
	}
	return &contracts.Sandbox{
		ID:       record.ID,
		State:    record.State,
		Provider: r.Name(),
		Image:    r.cfg.Image,
		RootDir:  record.RootDir,
	}, nil
}

// warnAboutDataRoot reports the one deployment mistake this provider cannot
// detect by itself: the runner's workspace path and this control plane's data
// root naming different directories. The workspace is a bind mount, so both
// services have to see the data directory at the same path; if they do not, the
// control plane writes a tree the container never mounts and every request that
// crosses the boundary fails later, far from the cause.
func (r *Runner) warnAboutDataRoot(record RunnerRecord, userID, projectID string) {
	if strings.TrimSpace(record.RootDir) == "" {
		return
	}
	want := filepath.Join(r.cfg.DataRoot, userID, projectID)
	if sameHostDir(record.RootDir, want) {
		return
	}
	slog.Warn("sandbox: runner: the runner reports a workspace the control plane names differently; the workspace is a bind mount, so both services must mount the data directory at the same path",
		"sandbox", record.ID,
		"runner_root", record.RootDir,
		"control_plane_root", want)
}

// do performs one JSON request against the runner.
func (r *Runner) do(ctx context.Context, method, path string, request, out any) error {
	var body io.Reader
	if request != nil {
		payload, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("sandbox: runner: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("sandbox: runner: %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("sandbox: runner: %s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, runnerMaxResponse))
	if err != nil {
		return fmt.Errorf("sandbox: runner: %s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return r.statusError(method, path, resp.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return fmt.Errorf("sandbox: runner: %s %s: empty response, expected a sandbox record", method, path)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("sandbox: runner: %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// statusError maps the runner's HTTP status onto the errors callers branch on.
func (r *Runner) statusError(method, path string, status int, payload []byte) error {
	message := strings.TrimSpace(string(payload))
	var envelope struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) == nil && strings.TrimSpace(envelope.Error) != "" {
		message = strings.TrimSpace(envelope.Error)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	switch status {
	case http.StatusNotFound:
		// The store distinguishes "the sandbox is gone" from "the provider is
		// broken" with ErrNotFound; a 404 is the runner saying the former.
		return fmt.Errorf("%w: %s", ErrNotFound, message)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("sandbox: runner: %s %s: the runner refused the token (HTTP %d): %s", method, path, status, message)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("sandbox: runner: %s %s: %s", method, path, message)
	case http.StatusGatewayTimeout:
		return fmt.Errorf("sandbox: runner: %s %s: the runner gave up waiting (HTTP %d): %s", method, path, status, message)
	default:
		return fmt.Errorf("sandbox: runner: %s %s: HTTP %d: %s", method, path, status, message)
	}
}

// sandboxResource builds a path under one sandbox, escaping the id because it
// arrives from the store and a runner URL must not be able to grow segments.
func sandboxResource(sandboxID, suffix string) string {
	resource := "/v1/sandboxes/" + url.PathEscape(sandboxID)
	if suffix == "" {
		return resource
	}
	return resource + "/" + suffix
}

// RunnerRequest is the body of POST /v1/sandboxes: one (user, project) pair's
// container.
//
// It lives here, beside the client, because it is the wire contract between this
// package and cmd/sandbox-runner; the handler decodes the same struct, so the two
// sides cannot drift.
type RunnerRequest struct {
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
	// Image is the sandbox image. Empty leaves the runner's default in charge.
	Image string `json:"image,omitempty"`
	// RepoURL is cloned into the checkout when non-empty; empty means `git init`.
	RepoURL string `json:"repo_url,omitempty"`
	// Branch is checked out (and created when missing).
	Branch string `json:"branch,omitempty"`
	// APIPort is the container port that serves the sandbox API.
	APIPort int `json:"api_port,omitempty"`
	// APIKey is an optional fixed SANDBOX_API_KEY for this create
	// (-sandbox-api-key). Empty means "give this container its own key"; the
	// runner reports the key it used back in the record, because a stateless
	// runner cannot remember it and the control plane must persist it.
	APIKey string `json:"api_key,omitempty"`
	// CPUs and Memory cap the container; empty means "no limit".
	CPUs   string `json:"cpus,omitempty"`
	Memory string `json:"memory,omitempty"`
	// Network is the Docker network the container joins.
	Network string `json:"network,omitempty"`
}

// RunnerWorktreeRequest is the body of POST /v1/sandboxes/{id}/worktrees.
//
// APIKey is the one addition to the lifecycle fields: creating a worktree runs
// git inside the container through its own API, so the runner has to present that
// container's key. Empty is the normal case with per-sandbox keys: the runner
// reads the key out of the container's own environment (`docker inspect`), which
// is what keeps it stateless. A non-empty value is the fixed override.
type RunnerWorktreeRequest struct {
	RepoDir string `json:"repo_dir"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	APIKey  string `json:"api_key,omitempty"`
}

// RunnerRecord is what every lifecycle call answers with.
//
// APIKey is present only on a create's answer: it is the key the container was
// started with, returned once so the control plane can persist it. The channel is
// the runner's bearer-token-protected internal API, and the secret is not logged
// (see sandboxrunner's logRequests).
type RunnerRecord struct {
	// ID is the container name, derived from (user, project) by the runner.
	ID string `json:"id"`
	// State is the contracts.SandboxState the runner observed.
	State contracts.SandboxState `json:"state"`
	// RootDir is the host directory mounted at /workspace for this pair.
	RootDir string `json:"root_dir"`
	// ContainerPathRoot is where RootDir is mounted inside the container
	// (/workspace). The client checks it so the two sides agree on the layout.
	ContainerPathRoot string `json:"container_path_root"`
	// Endpoint is the base URL of the sandbox API, or empty when the runner
	// published none.
	Endpoint string `json:"endpoint,omitempty"`
	// APIKey is the SANDBOX_API_KEY the container was created with. It is
	// returned once, on create, and never logged.
	APIKey string `json:"api_key,omitempty"`
}
