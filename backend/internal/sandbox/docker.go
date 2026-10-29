package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// containerWorkspace is where one project's host directory is mounted.
const containerWorkspace = "/workspace"

// containerRepoDir is the main checkout inside the container. It is the container
// side of provision.RepoDir(sb): the provider mounts <pair dir> at /workspace and
// reports <pair dir> as the sandbox's RootDir, so <RootDir>/repo is /workspace/repo.
const containerRepoDir = containerWorkspace + "/" + repoSubdir

// sandboxUser is the unprivileged account the sandbox image creates for its shell,
// its file service and its terminal. The checkout has to be created by that same
// user: files written through the bind mount (by the host, or by root in the
// container) belong to somebody else in the container's view, and the panel's git
// then dies with
//
//	fatal: Unable to create '/workspace/repo/.git/index.lock': Permission denied
//
// or, with git run as root, "fatal: detected dubious ownership". Creating the
// repository as gem is the fix; a chown afterwards would leave the same failure
// one container restart away.
const sandboxUser = "gem"

// sandboxUserWait bounds how long the checkout step waits for the image's
// entrypoint to create sandboxUser.
//
// `docker run -d` returns as soon as the container starts, while the AIO image's
// gem.sh runs useradd on the way up, so the first `docker exec -u gem` can answer
// "unable to find user gem: no matching entries in passwd file". Measured on
// 1.11.0: the account exists ~0.4s after start. Waiting is deliberate — falling
// back to root here is exactly the ownership bug this step exists to avoid.
const sandboxUserWait = 20 * time.Second

// sandboxUserPoll is how often that account is retried.
const sandboxUserPoll = 100 * time.Millisecond

// The identity the container's own git records for the bootstrap commit, and the
// one the panel's later commits inherit (gitx.identityArgs reads config before
// adding a fallback). It is deliberately container-specific: `git log` then shows
// which machine made the commit, and no host account leaks into the history.
const (
	sandboxGitName  = "PiForge Sandbox"
	sandboxGitEmail = "sandbox@piforge.local"
	// initialCommit is the message Local writes too (sandbox.go); both providers
	// leave one commit behind so worktrees have a base (PRD §6.3-坑1).
	initialCommit = "piforge: initial commit"
)

// Docker is a sandbox provider that runs each (user, project) in its own container.
//
// This exists because the local provider runs the agent, the browser and the terminal on
// the host: isolation is a directory and a convention. A container makes the boundary a
// real one, and an all-in-one image (browser + shell + file service) puts the page the
// human watches and the files the agent edits on the same machine — the property the
// panel currently gets by remembering to keep two sets of paths aligned.
//
// The control plane still never executes untrusted code: it talks to Docker, and the
// container gets a short-lived per-session token rather than a provider key.
//
// Scope of this provider: the lifecycle (create / pause / resume / destroy / exec) and
// the workspace layout. The browser and terminal can attach to the container's services
// (see internal/browser and internal/term); the control plane's workspace IO reads the
// bind-mounted host directory unless -sandbox-files remote selects the container's file
// API (see internal/workspaceremote).
type Docker struct {
	cfg DockerConfig
}

// DockerConfig configures the container provider.
type DockerConfig struct {
	// Image is the sandbox image to run.
	Image string
	// Network is the Docker network name; empty uses the default bridge.
	Network string
	// CPUs and Memory cap the container. Empty means "no limit", which is the honest
	// default for a self-hosted single-user box and a bad one for a shared deployment.
	CPUs   string
	Memory string
	// DataRoot is the host directory that holds one workspace per (user, project):
	// <DataRoot>/<userID>/<projectID>. Only a project's own directory is mounted at
	// /workspace for that project, so a page refresh or a pause does not lose the
	// checkout and two projects never share one working tree.
	DataRoot string
	// APIPort is the container port that serves the sandbox HTTP API (8080 for AIO
	// Sandbox). Zero means "do not publish anything", which is what the lifecycle-only
	// mode wants.
	APIPort int
	// APIKey is passed to the container as SANDBOX_API_KEY when non-empty, so every
	// service in it requires the header.
	APIKey string
}

// NewDocker builds a container provider.
func NewDocker(cfg DockerConfig) (*Docker, error) {
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("sandbox: docker: image is required")
	}
	if strings.TrimSpace(cfg.DataRoot) == "" {
		return nil, fmt.Errorf("sandbox: docker: data root is required")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("sandbox: docker: the docker CLI is not on PATH: %w", err)
	}
	// The mount source is compared with what Docker reports back after a restart, so
	// the configured root has to be the same absolute, cleaned path every time.
	root, err := filepath.Abs(cfg.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker: abs data root %q: %w", cfg.DataRoot, err)
	}
	cfg.DataRoot = filepath.Clean(root)
	return &Docker{cfg: cfg}, nil
}

// Name identifies the provider in the sandbox row.
func (d *Docker) Name() string { return "docker" }

// containerName is derived from (user, project), for the same reason the local provider
// does it: two sessions in one project must share one sandbox, and the name has to be
// recoverable after a restart of the control plane.
func (d *Docker) containerName(userID, projectID string) string {
	sum := sha256.Sum256([]byte(userID + "/" + projectID))
	return "piforge-" + hex.EncodeToString(sum[:8])
}

// workspacePath is the host directory bind-mounted at /workspace for one (user,
// project) pair: <DataRoot>/<userID>/<projectID>.
//
// It is derived from exactly the pair the container name is derived from, so the
// same container always sees the same directory across restarts, and two projects
// (or two users on one project) can never resolve to one checkout. The layout
// matches the local provider's, which keeps the two providers interchangeable on
// one data directory.
func (d *Docker) workspacePath(userID, projectID string) (string, error) {
	if err := safeSegment(userID); err != nil {
		return "", err
	}
	if err := safeSegment(projectID); err != nil {
		return "", err
	}
	p := filepath.Join(d.cfg.DataRoot, userID, projectID)
	if !isInside(d.cfg.DataRoot, p) {
		return "", fmt.Errorf("%w: workspace path escapes the data root", ErrInvalidID)
	}
	return p, nil
}

// Ensure creates the container if it is missing and returns its state.
//
// It also makes sure the container has a usable checkout before reporting READY.
// The checkout is part of the sandbox, not something a later panel request can be
// allowed to discover is missing: without .git, every git endpoint answers 500,
// and the failure surfaces far from the step that caused it.
func (d *Docker) Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	dir, err := d.workspacePath(userID, projectID)
	if err != nil {
		return nil, err
	}
	name := d.containerName(userID, projectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: docker: create workspace %s: %w", dir, err)
	}

	created := false
	state, inspectErr := d.inspect(ctx, name)
	switch {
	case inspectErr == nil:
		mount, mountErr := d.workspaceMount(ctx, name)
		if mountErr != nil || !sameHostDir(mount, dir) {
			// A container created before this provider gave each pair its own
			// directory mounts the shared data root (or something else entirely).
			// Reusing it would show this project another tenant's working tree, so
			// replace the container. The old tree is left on disk: tearing down a
			// sandbox must not decide that somebody's uncommitted work is disposable.
			slog.Warn("sandbox: docker: container workspace does not belong to this project; recreating the container",
				"container", name,
				"want", dir,
				"got", mount,
				"mount_error", mountErr)
			if _, err := d.docker(ctx, "rm", "-f", name); err != nil {
				return nil, err
			}
			created = true
			if err := d.runContainer(ctx, name, userID, projectID, dir); err != nil {
				return nil, err
			}
		} else if state != "running" {
			if _, err := d.docker(ctx, "start", name); err != nil {
				return nil, err
			}
		}
	case errors.Is(inspectErr, ErrNotFound):
		created = true
		if err := d.runContainer(ctx, name, userID, projectID, dir); err != nil {
			return nil, err
		}
	default:
		return nil, inspectErr
	}

	// Idempotent by construction (see planCheckout), so a container adopted after a
	// control-plane restart, or started by an older build, gets the same treatment.
	if err := d.ensureCheckout(ctx, name, repoURL, branch); err != nil {
		if created {
			// A container whose checkout could not be provisioned is not a sandbox:
			// remove it so the next Ensure starts from a clean one instead of
			// re-adopting a broken workspace.
			_, _ = d.docker(ctx, "rm", "-f", name)
		}
		return nil, err
	}
	return d.describe(ctx, userID, projectID, name)
}

// runContainer starts the per-(user, project) container, mounting only that pair's
// host directory at /workspace.
func (d *Docker) runContainer(ctx context.Context, name, userID, projectID, workspace string) error {
	args := []string{
		"run", "-d",
		"--name", name,
		"--label", "piforge.user=" + userID,
		"--label", "piforge.project=" + projectID,
		"--workdir", containerWorkspace,
		"-v", workspace + ":" + containerWorkspace,
		"--restart", "unless-stopped",
	}
	if d.cfg.Network != "" {
		args = append(args, "--network", d.cfg.Network)
	}
	if d.cfg.CPUs != "" {
		args = append(args, "--cpus", d.cfg.CPUs)
	}
	if d.cfg.Memory != "" {
		args = append(args, "--memory", d.cfg.Memory)
	}
	if d.cfg.APIPort > 0 {
		// Publish on a free host port: two sandboxes on one machine must not fight over
		// 8080, and the control plane reads the assigned port back from `docker port`.
		args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", d.cfg.APIPort))
		// The image starts its Chromium under Docker's default seccomp profile, and the
		// browser dies before CDP opens: "Failed to move to new namespace ... Operation
		// not permitted" (zygote_host_impl_linux.cc:207). BROWSER_NO_SANDBOX is the
		// image's own switch (gem.sh folds it into BROWSER_EXTRA_ARGS), and turning
		// Chrome's sandbox off is the smaller change than --security-opt
		// seccomp=unconfined: the container keeps its syscall filter, and the renderer
		// stays behind the container boundary, which is the boundary this provider
		// promises.
		args = append(args, "-e", "BROWSER_NO_SANDBOX=--no-sandbox")
	}
	if d.cfg.APIKey != "" {
		args = append(args, "-e", "SANDBOX_API_KEY="+d.cfg.APIKey)
	}
	args = append(args, d.cfg.Image)
	// The image may or may not have an entrypoint that idles; a sandbox has to stay up
	// between prompts, so keep it alive explicitly.
	args = append(args, "sleep", "infinity")

	_, err := d.docker(ctx, args...)
	return err
}

// checkoutStep is one command of the container-side checkout plan.
type checkoutStep struct {
	// name is what an error names: init, clone, checkout or commit.
	name string
	// argv is the command, "git" first, run with /workspace/repo as its directory.
	argv []string
	// fallback runs when argv exits non-zero, and replaces it. It exists for one
	// case: cloning a repository whose requested branch is not a local branch yet.
	// Git's checkout DWIM usually handles that, but Local's gitx.CreateAndCheckout
	// creates the branch rather than failing, so the container does too.
	fallback []string
}

// planCheckout is the argv list that turns /workspace/repo into a checkout on
// branch, given what the container already has.
//
// It is pure so each case's exact argv is pinned by a test that needs no Docker.
// ready is the idempotency gate: an existing repository that already has a commit
// is left alone — no re-init, no reset, no second empty commit — because Ensure is
// called repeatedly and moving a HEAD the user is building on would be the worse
// bug. A .git without a commit is not "ready": the empty commit is what worktrees
// branch from, so a half-provisioned checkout is resumed instead of trusted.
func planCheckout(ready bool, repoURL, branch string) []checkoutStep {
	if ready {
		return nil
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = DefaultInitialBranch
	}
	if url := strings.TrimSpace(repoURL); url != "" {
		return []checkoutStep{
			// "." because the checkout directory already exists (and must, so that
			// the bind mount cannot be replaced by a clone volume silently).
			{name: "clone", argv: []string{"git", "clone", "--quiet", "--", url, "."}},
			{
				name:     "checkout",
				argv:     []string{"git", "checkout", "-q", branch},
				fallback: []string{"git", "checkout", "-q", "-b", branch},
			},
		}
	}
	return []checkoutStep{
		{name: "init", argv: []string{"git", "init", "-q", "-b", branch}},
		// The identity is written into the checkout inside the container instead of
		// being passed with -c on the commit: every later commit from the panel
		// (gitx.identityArgs reads config before adding a fallback) then records the
		// same author, and `git log` says which machine produced it.
		{name: "init", argv: []string{"git", "config", "user.name", sandboxGitName}},
		{name: "init", argv: []string{"git", "config", "user.email", sandboxGitEmail}},
		{name: "commit", argv: []string{"git", "commit", "--allow-empty", "--no-gpg-sign", "-q", "-m", initialCommit}},
	}
}

// ensureCheckout makes /workspace/repo a git checkout owned by the sandbox user.
//
// Every command runs as sandboxUser, so the .git directory and its objects belong
// to the account that later runs stage and commit through the panel. The host-side
// provisioning keeps writing into the same bind-mounted directory (the mount layout
// is untouched); the two see one filesystem, and this only decides who owns the
// repository metadata.
func (d *Docker) ensureCheckout(ctx context.Context, name, repoURL, branch string) error {
	ready, err := d.checkoutReady(ctx, name)
	if err != nil {
		return fmt.Errorf("sandbox: docker: inspect %s: %w", containerRepoDir, err)
	}
	steps := planCheckout(ready, repoURL, branch)
	if len(steps) == 0 {
		return nil
	}
	if _, err := d.execAsSandboxUser(ctx, name, containerWorkspace, "mkdir", "-p", containerRepoDir); err != nil {
		return fmt.Errorf("sandbox: docker: prepare %s: %w", containerRepoDir, err)
	}
	for _, step := range steps {
		_, err := d.execAsSandboxUser(ctx, name, containerRepoDir, step.argv...)
		if err == nil {
			continue
		}
		if step.fallback != nil {
			_, ferr := d.execAsSandboxUser(ctx, name, containerRepoDir, step.fallback...)
			if ferr == nil {
				continue
			}
			return fmt.Errorf("sandbox: docker: %s %s: %w", step.name, containerRepoDir, ferr)
		}
		return fmt.Errorf("sandbox: docker: %s %s: %w", step.name, containerRepoDir, err)
	}
	return nil
}

// checkoutReady reports whether /workspace/repo already holds a repository with a
// commit.
//
// The probe runs as the sandbox user and always exits 0, so a non-zero status here
// means "the container could not answer", which is an error — not "there is no
// repository", which is the normal first-run answer.
func (d *Docker) checkoutReady(ctx context.Context, name string) (bool, error) {
	out, err := d.execAsSandboxUser(ctx, name, containerWorkspace, "sh", "-c",
		`if [ -d `+containerRepoDir+`/.git ] && git -C `+containerRepoDir+
			` rev-parse --verify --quiet HEAD >/dev/null 2>&1; then echo ready; else echo absent; fi`)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "ready", nil
}

// execAsSandboxUser runs argv inside the container as the sandbox user, in dir.
//
// The account is created by the image's own entrypoint (AIO's gem.sh runs useradd),
// so in the first moments after `docker run -d` it does not exist yet and docker
// answers "unable to find user gem". That one condition is retried until the
// account exists; every other failure is returned unchanged. Nothing here ever
// falls back to root: root-owned checkout metadata is the defect, not a shortcut.
func (d *Docker) execAsSandboxUser(ctx context.Context, name, dir string, argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: docker: exec needs a command")
	}
	args := []string{"exec", "-u", sandboxUser}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	args = append(args, name)
	args = append(args, argv...)

	deadline := time.Now().Add(sandboxUserWait)
	for {
		out, err := d.docker(ctx, args...)
		if err == nil {
			return out, nil
		}
		if !strings.Contains(err.Error(), "unable to find user") {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("sandbox: docker: this image never created the %q user the sandbox shell runs as, "+
				"so the checkout cannot be owned by it: %w", sandboxUser, err)
		}
		timer := time.NewTimer(sandboxUserPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

// Get reports the current state of the container.
//
// RootDir is read back from the container's own /workspace mount rather than
// remembered: after a restart that mount is the only durable statement of which
// host directory this sandbox actually sees, and the control plane must write to
// the same one.
func (d *Docker) Get(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	name := d.resolve(sandboxID)
	state, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	out := &contracts.Sandbox{
		ID:       name,
		Provider: d.Name(),
		Image:    d.cfg.Image,
	}
	if mount, merr := d.workspaceMount(ctx, name); merr == nil {
		out.RootDir = mount
	}
	out.State = dockerState(state)
	return out, nil
}

// Pause stops the container. A stopped container keeps its filesystem and its volumes,
// which is what "paused, not deleted" has to mean.
func (d *Docker) Pause(ctx context.Context, sandboxID string) error {
	name := d.resolve(sandboxID)
	if _, err := d.docker(ctx, "stop", "--time", "10", name); err != nil {
		return err
	}
	return nil
}

// Resume starts a stopped container.
func (d *Docker) Resume(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	name := d.resolve(sandboxID)
	if _, err := d.docker(ctx, "start", name); err != nil {
		return nil, err
	}
	return d.Get(ctx, sandboxID)
}

// Destroy removes the container. The workspace volume is left alone on purpose: deleting
// somebody's uncommitted work is not something a sandbox teardown should decide.
func (d *Docker) Destroy(ctx context.Context, sandboxID string) error {
	name := d.resolve(sandboxID)
	if _, err := d.docker(ctx, "rm", "-f", name); err != nil {
		return err
	}
	return nil
}

// Exec runs a command and returns its combined output.
func (d *Docker) Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: docker: exec needs a command")
	}
	name := d.resolve(sandboxID)
	args := []string{"exec"}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	args = append(args, name)
	args = append(args, argv...)
	return d.docker(ctx, args...)
}

// describe builds the sandbox record the store keeps.
func (d *Docker) describe(ctx context.Context, userID, projectID, name string) (*contracts.Sandbox, error) {
	state, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	dir, err := d.workspacePath(userID, projectID)
	if err != nil {
		return nil, err
	}
	return &contracts.Sandbox{
		ID:        name,
		UserID:    userID,
		ProjectID: projectID,
		State:     dockerState(state),
		Provider:  d.Name(),
		Image:     d.cfg.Image,
		// RootDir is the host side of this pair's bind mount, because the control
		// plane's own git and file IO runs here and has to write where the container
		// looks. Reporting the shared DataRoot (the old behaviour) made every project
		// resolve to <DataRoot>/repo and therefore to one checkout. Reporting the
		// container path was the bug before that: provisioning wrote to "/workspace"
		// on the host. ContainerPath translates this directory to /workspace for
		// anything that speaks to the container.
		RootDir:      dir,
		CreatedAt:    time.Now().UTC(),
		LastActiveAt: time.Now().UTC(),
	}, nil
}

func (d *Docker) resolve(sandboxID string) string {
	if strings.HasPrefix(sandboxID, "piforge-") {
		return sandboxID
	}
	return "piforge-" + sandboxID
}

// inspect returns the container's status string ("running", "exited", ...).
func (d *Docker) inspect(ctx context.Context, name string) (string, error) {
	out, err := d.docker(ctx, "inspect", "--format", "{{json .State}}", name)
	if err != nil {
		return "", err
	}
	var state struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &state); err != nil {
		return "", fmt.Errorf("sandbox: docker: inspect %s: %w", name, err)
	}
	if state.Running {
		return "running", nil
	}
	return state.Status, nil
}

// docker runs the CLI and turns a non-zero exit into an error carrying its output.
func (d *Docker) docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		// Docker 29 answers "error: no such object: ..." in lower case while older
		// releases wrote "No such object"; both mean the container is absent, which
		// Ensure must be able to tell from a daemon failure.
		lower := strings.ToLower(text)
		if strings.Contains(lower, "no such object") || strings.Contains(lower, "no such container") {
			return "", fmt.Errorf("%w: %s", ErrNotFound, d.lastArg(args))
		}
		return "", fmt.Errorf("sandbox: docker %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (d *Docker) lastArg(args []string) string {
	for index := len(args) - 1; index >= 0; index-- {
		if !strings.HasPrefix(args[index], "-") && args[index] != "inspect" && args[index] != "start" {
			return args[index]
		}
	}
	return ""
}

// workspaceMount returns the host directory the container has mounted at
// /workspace. It is the container's own statement of what it can see, which is
// what Ensure compares against the pair's directory before reusing a container.
func (d *Docker) workspaceMount(ctx context.Context, name string) (string, error) {
	out, err := d.docker(ctx, "inspect", "--format", "{{json .Mounts}}", name)
	if err != nil {
		return "", err
	}
	var mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts); err != nil {
		return "", fmt.Errorf("sandbox: docker: inspect mounts %s: %w", name, err)
	}
	for _, mount := range mounts {
		if path.Clean(filepath.ToSlash(mount.Destination)) == containerWorkspace {
			return mount.Source, nil
		}
	}
	return "", fmt.Errorf("sandbox: docker: %s has no %s mount", name, containerWorkspace)
}

// sameHostDir reports whether two host paths name the same directory. Docker
// echoes the mount source it was given, which on Windows may differ only by
// case; a symlinked data root may differ by resolution. Both are the same tree.
func sameHostDir(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	aa, bb = filepath.Clean(aa), filepath.Clean(bb)
	if equalHostPath(aa, bb) {
		return true
	}
	ra, err := filepath.EvalSymlinks(aa)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(bb)
	if err != nil {
		return false
	}
	return equalHostPath(filepath.Clean(ra), filepath.Clean(rb))
}

func equalHostPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// dockerState maps Docker's status to our state machine.
func dockerState(status string) contracts.SandboxState {
	switch strings.TrimSpace(status) {
	case "running":
		return contracts.SandboxReady
	case "created", "restarting":
		return contracts.SandboxStarting
	case "paused":
		return contracts.SandboxPaused
	case "exited", "dead":
		return contracts.SandboxPaused
	case "":
		return contracts.SandboxMissing
	default:
		return contracts.SandboxReady
	}
}

// Endpoint reports the host address that serves the sandbox API for one sandbox.
//
// It asks Docker rather than remembering: the port is assigned at creation and survives
// a control-plane restart, and a number kept in memory would be wrong after either.
func (d *Docker) Endpoint(ctx context.Context, sandboxID string) (string, error) {
	if d.cfg.APIPort <= 0 {
		return "", fmt.Errorf("sandbox: docker: no api port is published for this provider")
	}
	name := d.resolve(sandboxID)
	out, err := d.docker(ctx, "port", name, fmt.Sprintf("%d/tcp", d.cfg.APIPort))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.Split(out, "\n")[0])
	address := strings.TrimPrefix(line, "0.0.0.0:")
	address = strings.TrimPrefix(address, "127.0.0.1:")
	address = strings.TrimPrefix(address, ":::")
	if address == "" {
		return "", fmt.Errorf("sandbox: docker: %s publishes nothing on %d", name, d.cfg.APIPort)
	}
	return "http://127.0.0.1:" + address, nil
}

// ContainerPath maps a host path inside one project's workspace to the path the
// same file has inside that project's container.
//
// The terminal needs it: the store knows a worktree's host path, while the
// container shell must start in the mounted path. Each container mounts only
// <DataRoot>/<userID>/<projectID>, so that pair of leading segments is stripped.
// Anything outside a project workspace is an error rather than a best guess: the
// shared DataRoot and its immediate children are not mounted anywhere any more,
// and mapping them to /workspace would silently address the wrong tree.
func (d *Docker) ContainerPath(hostPath string) (string, error) {
	// A sandbox record may already carry a container path: provision lays out
	// repositories from RootDir, which is containerWorkspace.
	if clean := path.Clean(filepath.ToSlash(strings.TrimSpace(hostPath))); clean == containerWorkspace ||
		strings.HasPrefix(clean, containerWorkspace+"/") {
		return clean, nil
	}

	root, err := filepath.Abs(d.cfg.DataRoot)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: data root %q: %w", d.cfg.DataRoot, err)
	}
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: path %q: %w", hostPath, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: %q relative to %q: %w", abs, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: docker: %q is outside the data root %q", abs, root)
	}
	segments := strings.Split(filepath.ToSlash(rel), "/")
	if rel == "." || len(segments) < 2 {
		return "", fmt.Errorf("sandbox: docker: %q is not inside a project workspace under %q", abs, root)
	}
	for _, segment := range segments[:2] {
		if err := safeSegment(segment); err != nil {
			return "", fmt.Errorf("sandbox: docker: %q: %w", abs, err)
		}
	}
	if len(segments) == 2 {
		return containerWorkspace, nil
	}
	return path.Join(containerWorkspace, path.Join(segments[2:]...)), nil
}
