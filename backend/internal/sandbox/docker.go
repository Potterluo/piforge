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
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// containerWorkspace is where one project's host directory is mounted.
const containerWorkspace = "/workspace"

// ContainerWorkspace is containerWorkspace for other packages: the sandbox-runner
// reports it in its records so the control plane can check that both sides agree
// on the mount root instead of hard-coding the same string twice.
const ContainerWorkspace = containerWorkspace

// containerRepoDir is the main checkout inside the container. It is the container
// side of provision.RepoDir(sb): the provider mounts <pair dir> at /workspace and
// reports <pair dir> as the sandbox's RootDir, so <RootDir>/repo is /workspace/repo.
const containerRepoDir = containerWorkspace + "/" + repoSubdir

// checkoutAPIWait bounds how long the checkout step waits for the container's
// HTTP API after `docker run -d` returns.
//
// Starting a container and starting the services inside it are two different
// events: the AIO image's entrypoint needs a moment before /v1/shell/exec
// answers. The checkout is provisioned over that API now, so the first probe is
// retried until it answers. Measured on 1.11.0: a few hundred milliseconds;
// 45s leaves room for a loaded host without turning a broken container into a
// hang.
const checkoutAPIWait = 45 * time.Second

// checkoutAPIPoll is how often that API is retried while it comes up.
const checkoutAPIPoll = 250 * time.Millisecond

// checkoutProbeTimeout bounds one readiness probe. A refused connection fails
// immediately; this only stops a socket that accepts and then never answers
// from eating the whole checkoutAPIWait budget in one attempt.
const checkoutProbeTimeout = 5 * time.Second

// checkoutRequestTimeout bounds one checkout command. `git clone` of a real
// repository is not instant, and the default client timeout (60s) is too close
// to a slow clone.
const checkoutRequestTimeout = 3 * time.Minute

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

// checkoutShell runs one command inside the sandbox through its HTTP API. It is
// the slice of *aio.Client the checkout needs, and a seam so a test can record
// the commands without a container.
type checkoutShell interface {
	Exec(ctx context.Context, command, cwd string) (aio.ShellResult, error)
}

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
	// volumePrefix is DataRoot's path inside WorkspaceVolume, slash-separated and
	// relative to the volume's root (".", or "sandboxes" for a volume mounted at
	// /data with the data root at /data/sandboxes). It is resolved once, at
	// construction, because the subpath a sandbox mounts is volumePrefix plus the
	// pair — and a container created with the wrong one shows a project an empty
	// directory rather than an error.
	volumePrefix string
	// openCheckout builds the shell the checkout runs on. Nil means the
	// container's published AIO endpoint; tests replace it to fake or record the
	// commands, which also pins that no checkout command is a `docker exec`.
	openCheckout func(ctx context.Context, sandboxID string) (checkoutShell, error)
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
	// Sandbox). It is how the checkout is provisioned as the sandbox user, so a
	// provider that will call Ensure must publish it; zero is only useful for
	// lifecycle-only calls, and Ensure then reports that no API is published
	// rather than falling back to `docker exec`.
	APIPort int
	// APIKey is a *fixed* API key for every container this provider creates, and
	// the key used to reach a container whose own key cannot be read (a container
	// from a build that predates per-sandbox keys). Empty — the default — means
	// each container gets a fresh key from crypto/rand when it is created; see
	// key.go for why the key is per sandbox rather than per deployment.
	//
	// Empty is the secure default and the value a production deployment should
	// use. A fixed value is for debugging one deployment, never for a tenant.
	APIKey string
	// ContainerEndpoint reports a sandbox's address as http://<container>:<APIPort>
	// on Network instead of reading back a host port. It is what a multi-service
	// deployment needs: when the control plane is in a container of its own,
	// 127.0.0.1:<published> is its own loopback, not the sandbox. With it set the
	// container publishes no port at all, and Network must name a network the
	// control plane (and this process, which reaches the API during checkout) can
	// resolve container names on.
	//
	// False keeps the single-host behaviour byte for byte: publish on a free
	// loopback port and read it back from `docker port`.
	ContainerEndpoint bool
	// WorkspaceVolume names a Docker volume whose directory tree *is* DataRoot,
	// for a deployment where this process cannot name a host path.
	//
	// A bind mount's source is resolved by the daemon, on the daemon's host. When
	// the control plane and the runner are themselves containers, DataRoot is a
	// path inside their mount namespace ("/data/sandboxes"), and the daemon
	// resolves it against its own root: on Docker Desktop that is the VM's
	// filesystem, not the volume the two services share. Docker creates the
	// directory there without a word, so the sandbox gets an empty /workspace
	// while the control plane keeps writing into the volume — the exact silent
	// split the data-root check exists to catch, and one it cannot catch because
	// both sides name the same string.
	//
	// With a volume the daemon resolves the mount itself, so no host path is
	// involved: <WorkspaceVolume> is mounted at /workspace with the pair's
	// directory — DataRoot/<userID>/<projectID>, expressed relative to the volume's
	// root — as the volume subpath. The same volume is mounted at DataRoot in every
	// container, which is what makes /data/sandboxes/<user>/<project> one directory
	// on all three sides.
	//
	// Empty keeps the bind mount, which is the single-host shape and still the
	// right one where this process is a host process.
	WorkspaceVolume string
	// WorkspaceVolumeRoot is where that volume is mounted in *this* process, so the
	// subpath a sandbox needs can be computed: volume subpaths are relative to the
	// volume's root, while DataRoot is a directory somewhere inside it
	// ("/data/sandboxes" under a volume mounted at "/data"). Required with
	// WorkspaceVolume, ignored without it.
	WorkspaceVolumeRoot string
}

// NewDocker builds a container provider. It needs the Docker CLI and an image,
// because it can create containers.
func NewDocker(cfg DockerConfig) (*Docker, error) {
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("sandbox: docker: image is required")
	}
	provider, err := newDocker(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("sandbox: docker: the docker CLI is not on PATH: %w", err)
	}
	return provider, nil
}

// NewDockerAPI builds a provider that only talks to an existing container's HTTP
// API: no Docker CLI, no image.
//
// It exists for the sandbox-runner's adapter. A request that adds a branch
// worktree names a container that already exists, and the runner must not demand
// an image for it (the image arrived with an earlier request, and the runner is
// stateless by design) or resolve a CLI that the call never uses. The container
// rules are the same ones NewDocker returns; only those two creation-time
// preconditions are dropped.
func NewDockerAPI(cfg DockerConfig) (*Docker, error) { return newDocker(cfg) }

// newDocker validates and normalises what every method needs.
func newDocker(cfg DockerConfig) (*Docker, error) {
	if strings.TrimSpace(cfg.DataRoot) == "" {
		return nil, fmt.Errorf("sandbox: docker: data root is required")
	}
	if cfg.ContainerEndpoint && strings.TrimSpace(cfg.Network) == "" {
		return nil, fmt.Errorf("sandbox: docker: container endpoints need a network to resolve container names on")
	}
	// The mount source is compared with what Docker reports back after a restart, so
	// the configured root has to be the same absolute, cleaned path every time.
	root, err := filepath.Abs(cfg.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker: abs data root %q: %w", cfg.DataRoot, err)
	}
	cfg.DataRoot = filepath.Clean(root)
	d := &Docker{cfg: cfg}
	if cfg.WorkspaceVolume != "" {
		if strings.TrimSpace(cfg.WorkspaceVolumeRoot) == "" {
			return nil, fmt.Errorf("sandbox: docker: workspace volume %q needs the path it is mounted at here (WorkspaceVolumeRoot)", cfg.WorkspaceVolume)
		}
		volumeRoot, err := filepath.Abs(cfg.WorkspaceVolumeRoot)
		if err != nil {
			return nil, fmt.Errorf("sandbox: docker: abs workspace volume root %q: %w", cfg.WorkspaceVolumeRoot, err)
		}
		d.cfg.WorkspaceVolumeRoot = filepath.Clean(volumeRoot)
		rel, err := filepath.Rel(d.cfg.WorkspaceVolumeRoot, cfg.DataRoot)
		if err != nil {
			return nil, fmt.Errorf("sandbox: docker: data root %q relative to the workspace volume at %q: %w", cfg.DataRoot, d.cfg.WorkspaceVolumeRoot, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("sandbox: docker: data root %q is not inside the workspace volume mounted at %q", cfg.DataRoot, d.cfg.WorkspaceVolumeRoot)
		}
		d.volumePrefix = filepath.ToSlash(rel)
	}
	return d, nil
}

// Name identifies the provider in the sandbox row.
func (d *Docker) Name() string { return "docker" }

// A branch session's worktree is created inside the container, as the sandbox
// user; see AddWorktree.
var _ contracts.WorktreeSandbox = (*Docker)(nil)

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

// volumeSubpath is this pair's directory relative to the workspace volume's root,
// which is what `--mount volume-subpath=` takes. It is derived from the same two
// segments as workspacePath, so the two shapes name one directory.
func (d *Docker) volumeSubpath(userID, projectID string) string {
	return path.Join(d.volumePrefix, userID, projectID)
}

// Ensure creates the container if it is missing and returns its state.
//
// It also makes sure the container has a usable checkout before reporting READY.
// The checkout is part of the sandbox, not something a later panel request can be
// allowed to discover is missing: without .git, every git endpoint answers 500,
// and the failure surfaces far from the step that caused it.
//
// The returned sandbox carries the key the container's API was started with, so
// the control plane can persist it with the row. A container that has no key (one
// created before per-sandbox keys) is replaced rather than adopted: its API
// accepts anything that can reach it, and "it was already like that" is not a
// reason to keep calling it unauthenticated. The workspace is a bind mount, so
// replacing the container keeps every file.
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
		key, keyErr := d.containerAPIKey(ctx, name)
		switch {
		case mountErr != nil || !sameHostDir(mount, dir):
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
			if err := d.replaceContainer(ctx, name, userID, projectID, dir); err != nil {
				return nil, err
			}
			created = true
		case keyErr != nil:
			return nil, keyErr
		case key == "":
			// No SANDBOX_API_KEY in the container's environment: its API is open
			// to anything that can reach it, which on a shared network is every
			// other sandbox. Replace it with a keyed one; the workspace survives.
			slog.Warn("sandbox: docker: container was started without an API key; recreating it with a per-sandbox key",
				"container", name)
			if err := d.replaceContainer(ctx, name, userID, projectID, dir); err != nil {
				return nil, err
			}
			created = true
		case state != "running":
			if _, err := d.docker(ctx, "start", name); err != nil {
				return nil, err
			}
		}
	case errors.Is(inspectErr, ErrNotFound):
		created = true
		if err := d.runContainer(ctx, name, userID, projectID, dir); err != nil {
			// `docker run` can leave a container object behind in "created" state
			// when the daemon refuses the mount (a volume subpath that does not
			// exist, an image it cannot access): the object exists, so the next
			// Ensure finds the right mount and the right key, retries
			// `docker start`, and fails the same way — forever, while the leftover
			// object also keeps the workspace volume from being removed. Remove
			// what this attempt half-created.
			_, _ = d.docker(ctx, "rm", "-f", name)
			return nil, err
		}
	default:
		return nil, inspectErr
	}

	// Read the key back from the container instead of remembering the one just
	// generated: a container adopted from an earlier control plane, or created
	// with the fixed -sandbox-api-key override, has to be addressed with the key
	// it actually runs with.
	apiKey, err := d.containerAPIKey(ctx, name)
	if err != nil {
		return nil, err
	}
	if apiKey == "" {
		return nil, fmt.Errorf("sandbox: docker: container %s has no %s; refusing to call its API unauthenticated", name, SandboxAPIKeyEnv)
	}

	// Idempotent by construction (see planCheckout), so a container adopted after a
	// control-plane restart, or started by an older build, gets the same treatment.
	if err := d.ensureCheckout(ctx, name, apiKey, repoURL, branch); err != nil {
		if created {
			// A container whose checkout could not be provisioned is not a sandbox:
			// remove it so the next Ensure starts from a clean one instead of
			// re-adopting a broken workspace.
			_, _ = d.docker(ctx, "rm", "-f", name)
		}
		return nil, err
	}
	return d.describe(ctx, userID, projectID, name, apiKey)
}

// replaceContainer removes a container and starts it again with a fresh key (or
// the fixed override). The workspace is a bind mount, so only the container — and
// the processes in it — is replaced.
func (d *Docker) replaceContainer(ctx context.Context, name, userID, projectID, workspace string) error {
	if _, err := d.docker(ctx, "rm", "-f", name); err != nil {
		return err
	}
	return d.runContainer(ctx, name, userID, projectID, workspace)
}

// runContainer starts the per-(user, project) container, mounting only that pair's
// host directory at /workspace.
func (d *Docker) runContainer(ctx context.Context, name, userID, projectID, workspace string) error {
	apiKey := strings.TrimSpace(d.cfg.APIKey)
	if apiKey == "" {
		generated, err := NewAPIKey()
		if err != nil {
			return err
		}
		apiKey = generated
	}
	_, err := d.docker(ctx, d.containerArgs(name, userID, projectID, workspace, apiKey)...)
	return err
}

// containerArgs is the `docker run` argv for one sandbox.
//
// It is a function of the config, the pair and the key alone so the two endpoint
// shapes — a published loopback port, or a name on a shared network — are pinned
// by a test that needs no daemon. Everything else here is a rule with a bug
// behind it: the mount is one pair's directory, the labels are how an orphan is
// found after a crash, and BROWSER_NO_SANDBOX is why the image's Chromium starts
// under the default seccomp profile.
//
// apiKey is the key this container's API will require. It is a parameter rather
// than a field because every container gets its own, and a value remembered on
// the provider would be one container's key handed to the next.
func (d *Docker) containerArgs(name, userID, projectID, workspace, apiKey string) []string {
	args := []string{
		"run", "-d",
		"--name", name,
		"--label", "piforge.user=" + userID,
		"--label", "piforge.project=" + projectID,
		"--workdir", containerWorkspace,
	}
	if d.cfg.WorkspaceVolume != "" {
		// The daemon resolves a volume itself; nothing here depends on a host
		// path. The subpath is the pair's directory inside the shared volume,
		// derived exactly like the bind mount's path (see workspacePath), so
		// both shapes mount one project and only that project.
		args = append(args, "--mount", fmt.Sprintf(
			"type=volume,source=%s,target=%s,volume-subpath=%s",
			d.cfg.WorkspaceVolume, containerWorkspace, d.volumeSubpath(userID, projectID)))
	} else {
		args = append(args, "-v", workspace+":"+containerWorkspace)
	}
	args = append(args, "--restart", "unless-stopped")
	// The image treats $WORKSPACE as "the workspace directory (global for all
	// services)" and chowns it to its sandbox user at startup (gem.sh:
	// `mkdir -p "$WORKSPACE"; chown "$USER:$USER" "$WORKSPACE"`). Its default is
	// /home/gem, while PiForge mounts the project at /workspace: without this the
	// mount stays root-owned and the first `mkdir /workspace/repo` through the
	// sandbox API fails with "Permission denied" — which is what a volume mount
	// (correctly owned by root) does, and what a Windows bind mount only hides
	// because it ignores ownership.
	args = append(args, "-e", "WORKSPACE="+containerWorkspace)
	if d.cfg.Network != "" {
		args = append(args, "--network", d.cfg.Network)
	}
	if d.cfg.CPUs != "" {
		args = append(args, "--cpus", d.cfg.CPUs)
	}
	if d.cfg.Memory != "" {
		args = append(args, "--memory", d.cfg.Memory)
	}
	if d.cfg.APIPort > 0 && !d.cfg.ContainerEndpoint {
		// Publish on a free host port: two sandboxes on one machine must not fight over
		// 8080, and the control plane reads the assigned port back from `docker port`.
		// A deployment whose control plane is a container of its own sets
		// ContainerEndpoint and gets no published port at all: it reaches the sandbox
		// by name on the shared network instead (see DockerConfig.ContainerEndpoint).
		args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", d.cfg.APIPort))
	}
	if d.cfg.APIPort > 0 {
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
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		// One key per container. The image requires it as X-AIO-API-Key on every
		// request, which is what keeps a sibling sandbox on the shared network out
		// (see key.go).
		args = append(args, "-e", SandboxAPIKeyEnv+"="+apiKey)
	}
	args = append(args, d.cfg.Image)
	// The image may or may not have an entrypoint that idles; a sandbox has to stay up
	// between prompts, so keep it alive explicitly.
	args = append(args, "sleep", "infinity")
	return args
}

// sandboxStep is one command of a provisioning plan that runs inside the
// sandbox: the main checkout (planCheckout) or a branch session's linked
// worktree (planBranchWorktree).
type sandboxStep struct {
	// name is what an error names: init, clone, checkout, commit,
	// check-ref-format or worktree.
	name string
	// argv is the command, "git" first.
	argv []string
	// dir is the working directory inside the container. Empty means the main
	// checkout: every step of planCheckout runs there. Worktree steps name the
	// repository explicitly because `git worktree add` is a repository command.
	dir string
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
//
// The last step opens the tree to the *other* writer. A container deployment has
// two: `gem` (uid 1000, the sandbox user) owns the workspace because the sandbox
// created it, and root in the control plane materialises skills/MCP config into it,
// spawns the agent there and writes that agent's heartbeat marker into
// `<pair>/.piforge/live/<session>`. A tree created 0755 by gem is not writable by a
// control plane that dropped CAP_DAC_OVERRIDE (the documented compose does, so
// `copy-ro` skills are real), and the session then dies on the first write:
//
//	provision: mount tooling: materialize: create skills root: mkdir
//	/data/sandboxes/<user>/<project>/repo/.pi: permission denied
//	liveness: create the marker directory ... mkdir .../.piforge: permission denied
//
// `chmod -R a+rwX` gives both identities the write bit the shared tree needs while
// leaving ownership with gem, which is what the panel's git and shell rely on.
// X (not x) keeps it from making plain files executable; directories get 0777 and
// files 0666. The whole pair directory, not only `repo`: the marker and any later
// tooling live beside the checkout, not inside it. It runs only when the checkout
// is created, never on the ready path: a later pass would also loosen the 0444/0555
// skill copies materialise wrote, and those are the read-only guarantee itself.
func planCheckout(ready bool, repoURL, branch string) []sandboxStep {
	if ready {
		return nil
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = DefaultInitialBranch
	}
	open := sandboxStep{name: "permissions", argv: []string{"chmod", "-R", "a+rwX", containerWorkspace}}
	if url := strings.TrimSpace(repoURL); url != "" {
		return []sandboxStep{
			// "." because the checkout directory already exists (and must, so that
			// the bind mount cannot be replaced by a clone volume silently).
			{name: "clone", argv: []string{"git", "clone", "--quiet", "--", url, "."}},
			{
				name:     "checkout",
				argv:     []string{"git", "checkout", "-q", branch},
				fallback: []string{"git", "checkout", "-q", "-b", branch},
			},
			open,
		}
	}
	return []sandboxStep{
		{name: "init", argv: []string{"git", "init", "-q", "-b", branch}},
		// The identity is written into the checkout inside the container instead of
		// being passed with -c on the commit: every later commit from the panel
		// (gitx.identityArgs reads config before adding a fallback) then records the
		// same author, and `git log` says which machine produced it.
		{name: "init", argv: []string{"git", "config", "user.name", sandboxGitName}},
		{name: "init", argv: []string{"git", "config", "user.email", sandboxGitEmail}},
		{name: "commit", argv: []string{"git", "commit", "--allow-empty", "--no-gpg-sign", "-q", "-m", initialCommit}},
		open,
	}
}

// planBranchWorktree is the argv list that makes worktreeDir a linked worktree of
// repoDir on branch.
//
// It is pure for the same reason planCheckout is: the commands, their order and
// the directory each one runs in are the contract, and a test pins them without a
// container. ready is the idempotency gate — a retry after `git worktree add`
// succeeded but its store row did not must not add the worktree twice ("already
// exists") — and an existing worktree is left exactly as it is.
//
// check-ref-format comes first so an invalid branch name fails before git creates
// anything; `worktree add -b` is then the single command that creates the branch
// and the checkout, in the repository (a worktree cannot be added from a
// subdirectory of the repository).
func planBranchWorktree(ready bool, repoDir, worktreeDir, branch string) ([]sandboxStep, error) {
	if ready {
		return nil, nil
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, fmt.Errorf("a branch name is required to add a worktree")
	}
	// git would read a leading dash as an option to `worktree add -b`, and
	// check-ref-format would not catch it because the argv never reaches the
	// branch-name parser.
	if strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("invalid branch name %q: it starts with a dash", branch)
	}
	return []sandboxStep{
		{name: "check-ref-format", dir: repoDir, argv: []string{"git", "check-ref-format", "--branch", branch}},
		{name: "worktree", dir: repoDir, argv: []string{"git", "worktree", "add", "-b", branch, "--", worktreeDir}},
		// Same two writers as the main checkout (see planCheckout): gem owns the
		// worktree, and the control plane materialises tooling into it as root with
		// CAP_DAC_OVERRIDE dropped. Without the write bit for the second identity,
		// a branch session fails provisioning on `mkdir <worktree>/.pi`.
		{name: "permissions", dir: repoDir, argv: []string{"chmod", "-R", "a+rwX", worktreeDir}},
	}, nil
}

// ensureCheckout makes /workspace/repo a git checkout owned by the sandbox user.
//
// The commands run through the container's AIO API (/v1/shell/exec), not through
// `docker exec`. The image's shell already runs as the sandbox user (`gem`, uid
// 1000), so the .git directory and its objects still belong to the account that
// later stages and commits through the panel. That ownership is the point: a
// checkout created by the host (or as root) makes the panel's git die with
// "Unable to create '.git/index.lock': Permission denied", and chowning it
// afterwards leaves the same failure one container restart away. What changes is
// the dependency: provisioning needs only the endpoint the provider publishes,
// not a docker CLI on the control plane and not a daemon on the same host, so it
// also works when the container was created somewhere else (compose, k8s).
// planCheckout still decides which commands run, in what order, and what an
// error names; this is only their execution.
//
// There is deliberately no `docker exec` fallback: a deployment that cannot
// reach the API gets a clear failure instead of a checkout that only works on
// the machine holding the daemon.
func (d *Docker) ensureCheckout(ctx context.Context, name, apiKey, repoURL, branch string) error {
	shell, ready, err := d.waitForCheckoutShell(ctx, name, apiKey)
	if err != nil {
		return fmt.Errorf("sandbox: docker: checkout %s: %w", containerRepoDir, err)
	}
	steps := planCheckout(ready, repoURL, branch)
	if len(steps) == 0 {
		return nil
	}
	if res, err := shell.Exec(ctx, "mkdir -p -- "+aio.Quote(containerRepoDir), containerWorkspace); err != nil {
		return fmt.Errorf("sandbox: docker: prepare %s: %w", containerRepoDir, err)
	} else if res.ExitCode != 0 {
		return fmt.Errorf("sandbox: docker: prepare %s: %s", containerRepoDir, commandFailure(res))
	}
	for _, step := range steps {
		err := runSandboxStep(ctx, shell, step)
		if err == nil {
			continue
		}
		if step.fallback != nil {
			ferr := runSandboxStep(ctx, shell, sandboxStep{name: step.name, dir: step.dir, argv: step.fallback})
			if ferr == nil {
				continue
			}
			return fmt.Errorf("sandbox: docker: %s %s: %w", step.name, containerRepoDir, ferr)
		}
		return fmt.Errorf("sandbox: docker: %s %s: %w", step.name, containerRepoDir, err)
	}
	return nil
}

// AddWorktree creates a branch session's linked worktree inside the sandbox, as
// the sandbox user, through the container's own API (contracts.WorktreeSandbox).
//
// The defect this exists for: provisioning used to call `git worktree add` on the
// host, through the bind mount. The worktree directory and
// `<main>/.git/worktrees/<slug>` then belonged to the host user while the panel's
// shell runs as `gem` (uid 1000), so the first `git stage` in a branch session
// died with
//
//	fatal: Unable to create '.../.git/worktrees/<slug>/index.lock': Permission denied
//
// (and on a host whose absolute paths do not exist inside the container, git
// answered "not a git repository" instead, because the worktree's `.git` file
// recorded a host path). Running the same command inside the container gives the
// administrative files to `gem` and makes the `.git` file point at
// `/workspace/repo/.git/worktrees/<slug>`, which is where the container's git
// looks.
//
// planBranchWorktree decides which commands run, in what order; this is only
// their execution, and it uses exactly the rules ensureCheckout established: the
// image's shell already runs as the sandbox user, cwd is prefixed as
// `cd -- <dir> &&` by aio.Client.Exec because the image ignores the field, every
// git command carries `-c safe.directory=<repo>`, and no command is `exit`.
func (d *Docker) AddWorktree(ctx context.Context, sandboxID, repoDir, path, branch string) error {
	name := d.resolve(sandboxID)
	repo, err := d.ContainerPath(repoDir)
	if err != nil {
		return fmt.Errorf("sandbox: docker: add worktree: repository %q: %w", repoDir, err)
	}
	worktree, err := d.ContainerPath(path)
	if err != nil {
		return fmt.Errorf("sandbox: docker: add worktree: %q: %w", path, err)
	}

	// The worktree is created through the sandbox API, so the endpoint has to
	// answer first; an already-running container answers immediately.
	shell, err := d.waitForSandboxShell(ctx, name, "", probeSandboxAPI)
	if err != nil {
		return fmt.Errorf("sandbox: docker: add worktree %s: %w", worktree, err)
	}
	ready, err := probeWorktreeReady(ctx, shell, worktree)
	if err != nil {
		return fmt.Errorf("sandbox: docker: add worktree %s: %w", worktree, err)
	}
	steps, err := planBranchWorktree(ready, repo, worktree, branch)
	if err != nil {
		return fmt.Errorf("sandbox: docker: add worktree %s: %w", worktree, err)
	}
	for _, step := range steps {
		if err := runSandboxStep(ctx, shell, step); err != nil {
			return fmt.Errorf("sandbox: docker: %s %s: %w", step.name, worktree, err)
		}
	}
	return nil
}

// probeWorktreeReady reports whether a session worktree already exists as a
// usable linked worktree. It is the idempotency gate of planBranchWorktree: a
// retry sees "ready" and leaves the checkout alone, while a path that is missing
// or not a repository is (re)created.
func probeWorktreeReady(ctx context.Context, shell checkoutShell, worktreeDir string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, checkoutProbeTimeout)
	defer cancel()
	quoted := aio.Quote(worktreeDir)
	script := `if [ -e ` + quoted + `/.git ] && git -c ` + aio.Quote("safe.directory="+worktreeDir) +
		` -C ` + quoted + ` rev-parse --verify --quiet HEAD >/dev/null 2>&1; then echo ready; else echo absent; fi`
	res, err := shell.Exec(probeCtx, script, containerWorkspace)
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return strings.TrimSpace(res.Output) == "ready", nil
}

// probeSandboxAPI is the liveness probe for a container whose API is needed but
// whose checkout already exists: it proves a command actually ran, which is what
// "the image's services are listening" means here.
func probeSandboxAPI(ctx context.Context, shell checkoutShell) error {
	probeCtx, cancel := context.WithTimeout(ctx, checkoutProbeTimeout)
	defer cancel()
	const marker = "piforge-api-ready"
	res, err := shell.Exec(probeCtx, "echo "+marker, containerWorkspace)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	if !strings.Contains(res.Output, marker) {
		return fmt.Errorf("the sandbox API answered without running the probe: %s", strings.TrimSpace(res.Output))
	}
	return nil
}

// waitForCheckoutShell waits until the container's HTTP API answers, then
// reports whether the checkout is already usable.
//
// The readiness answer comes from the same probe used by every earlier build,
// just executed by the sandbox's own shell this time. The retry window itself is
// waitForSandboxShell's: `docker run -d` returns before the image's services are
// listening.
func (d *Docker) waitForCheckoutShell(ctx context.Context, name, apiKey string) (checkoutShell, bool, error) {
	var ready bool
	shell, err := d.waitForSandboxShell(ctx, name, apiKey, func(probeCtx context.Context, shell checkoutShell) error {
		isReady, probeErr := probeCheckoutReady(probeCtx, shell)
		if probeErr != nil {
			return probeErr
		}
		ready = isReady
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return shell, ready, nil
}

// waitForSandboxShell waits until the container's HTTP API answers and returns a
// shell that runs commands inside it, as the sandbox user. probe is the liveness
// test: an error means "the API is not serving yet, try again"; any answer means
// it is up.
//
// `docker run -d` returns before the image's services are listening, so the first
// calls can fail with connection refused. Only that startup window is retried,
// and it is bounded: a container that never serves the API is an error naming the
// step, not an invitation to shell out to Docker.
func (d *Docker) waitForSandboxShell(ctx context.Context, name, apiKey string, probe func(context.Context, checkoutShell) error) (checkoutShell, error) {
	if d.cfg.APIPort <= 0 && d.openCheckout == nil {
		return nil, fmt.Errorf("the checkout runs through the sandbox API, but this provider publishes no api port (DockerConfig.APIPort); " +
			"the docker CLI is not a fallback")
	}
	deadline := time.Now().Add(checkoutAPIWait)
	var shell checkoutShell
	var lastErr error
	for {
		// Resolve the endpoint once. It cannot move while this container exists
		// (the port is assigned at creation), and `docker port` is an exec per
		// call: retrying the probe must not spawn one every 250ms while the
		// image's services come up.
		if shell == nil {
			opened, err := d.shellFor(ctx, name, apiKey)
			if err == nil {
				shell = opened
			} else {
				lastErr = err
			}
		}
		if shell != nil {
			if probeErr := probe(ctx, shell); probeErr == nil {
				return shell, nil
			} else {
				lastErr = probeErr
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the sandbox API did not answer within %s: %w", checkoutAPIWait, lastErr)
		}
		timer := time.NewTimer(checkoutAPIPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// probeCheckoutReady reports whether /workspace/repo already holds a repository
// with a commit.
//
// The probe always exits 0 when the shell answers, so an error here means "the
// container could not answer" — the startup condition waitForCheckoutShell
// retries — not "there is no repository", which is the normal first-run answer.
func probeCheckoutReady(ctx context.Context, shell checkoutShell) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, checkoutProbeTimeout)
	defer cancel()
	script := `if [ -d ` + containerRepoDir + `/.git ] && git -C ` + containerRepoDir +
		` rev-parse --verify --quiet HEAD >/dev/null 2>&1; then echo ready; else echo absent; fi`
	res, err := shell.Exec(probeCtx, script, containerWorkspace)
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return strings.TrimSpace(res.Output) == "ready", nil
}

// runSandboxStep runs one planned argv in the step's directory (the main
// checkout when the step names none).
func runSandboxStep(ctx context.Context, shell checkoutShell, step sandboxStep) error {
	dir := step.dir
	if strings.TrimSpace(dir) == "" {
		dir = containerRepoDir
	}
	command, err := checkoutCommand(step.argv, dir)
	if err != nil {
		return err
	}
	// The image accepts cwd but ignores it, so aio.Client.Exec prefixes the
	// command with `cd -- <dir> &&`; the directory is in the command line that
	// actually crosses the API, not only in JSON the image may drop.
	res, err := shell.Exec(ctx, command, dir)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return errors.New(commandFailure(res))
	}
	return nil
}

// checkoutCommand turns one planned argv into the shell string the sandbox API
// runs, as the sandbox user.
//
// git gets `-c safe.directory=<dir>`. A checkout arrived through a bind mount,
// and on a Docker Desktop/drvfs host the mount can show it as owned by somebody
// other than the shell account; git 2.34 then refuses the repository with
// "detected dubious ownership", which turned every panel git view into a 500. The
// path is exactly the directory this command was told to run in — a workspace the
// sandbox owns — so trusting it is narrower than `safe.directory=*`.
//
// Only a git step gets the flag: a plan step is not necessarily git (the
// `permissions` step runs chmod), and prefixing those turns the mode string into
// `-c safe.directory=…`, which chmod rejects as an invalid mode.
func checkoutCommand(argv []string, dir string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: docker: checkout step has no command")
	}
	quoted := make([]string, 0, len(argv)+3)
	quoted = append(quoted, aio.Quote(argv[0]))
	if argv[0] == "git" {
		quoted = append(quoted, aio.Quote("-c"), aio.Quote("safe.directory="+dir))
	}
	for _, arg := range argv[1:] {
		quoted = append(quoted, aio.Quote(arg))
	}
	return strings.Join(quoted, " "), nil
}

// commandFailure renders a non-zero sandbox shell result for an error.
func commandFailure(res aio.ShellResult) string {
	if out := strings.TrimSpace(res.Output); out != "" {
		return fmt.Sprintf("exit %d: %s", res.ExitCode, out)
	}
	return fmt.Sprintf("exit %d", res.ExitCode)
}

// shellFor returns the command runner for a container: the injected one in a
// test, otherwise a client pointed at the container's published AIO endpoint.
func (d *Docker) shellFor(ctx context.Context, sandboxID, apiKey string) (checkoutShell, error) {
	if d.openCheckout != nil {
		return d.openCheckout(ctx, sandboxID)
	}
	return d.sandboxShell(ctx, sandboxID, apiKey)
}

// sandboxShell builds the AIO client for one container.
//
// It asks Endpoint() rather than remembering a port: the published port is
// assigned at creation and survives a control-plane restart, and a number kept
// in memory would be wrong after either.
func (d *Docker) sandboxShell(ctx context.Context, sandboxID, apiKey string) (checkoutShell, error) {
	endpoint, err := d.Endpoint(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s has no reachable API endpoint: %w", sandboxID, err)
	}
	if strings.TrimSpace(apiKey) == "" {
		// A caller with no key in hand (the branch-worktree path) still has the
		// container: read the key it was started with rather than calling it open.
		apiKey, err = d.apiKeyFor(ctx, d.resolve(sandboxID))
		if err != nil {
			return nil, err
		}
	}
	client, err := aio.New(aio.Config{
		BaseURL: endpoint,
		APIKey:  apiKey,
		Root:    containerWorkspace,
		Timeout: checkoutRequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: %w", sandboxID, err)
	}
	return client, nil
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

// containerAPIKey reads the SANDBOX_API_KEY a container was started with, out of
// its own environment.
//
// The container is the authority: a control plane that restarted, a provider
// adopting a container it did not create, and a runner asked for a branch
// worktree all have to address the container with the key it actually runs with,
// and the only place that survives every restart is `docker inspect .Config.Env`.
// Empty means the container predates per-sandbox keys (or was created by
// something that did not set one), which Ensure repairs by recreating it.
func (d *Docker) containerAPIKey(ctx context.Context, name string) (string, error) {
	out, err := d.docker(ctx, "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return "", err
	}
	return parseSandboxAPIKeyEnv(out), nil
}

// parseSandboxAPIKeyEnv picks SANDBOX_API_KEY out of the one-variable-per-line
// output of `docker inspect .Config.Env`. It is pure so the parsing is pinned
// without a daemon; a key may legitimately contain '=' (hex does not, but the
// operator's fixed override might), so only the first one splits.
func parseSandboxAPIKeyEnv(out string) string {
	prefix := SandboxAPIKeyEnv + "="
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// apiKeyFor resolves the key for a container's API when the caller has none in
// hand (the branch-worktree path, where the runner is stateless and only knows
// the sandbox id).
//
// The container's own value wins over the configured one: a fixed override only
// applies to containers this provider started, and an adopted container may
// carry a per-sandbox key from before the override was set.
func (d *Docker) apiKeyFor(ctx context.Context, name string) (string, error) {
	key, err := d.containerAPIKey(ctx, name)
	if err != nil {
		return "", err
	}
	if key != "" {
		return key, nil
	}
	if fixed := strings.TrimSpace(d.cfg.APIKey); fixed != "" {
		return fixed, nil
	}
	return "", fmt.Errorf("sandbox: docker: container %s has no %s; refusing to call its API unauthenticated (recreate the sandbox, or set -sandbox-api-key for a fixed key)", name, SandboxAPIKeyEnv)
}

// describe builds the sandbox record the store keeps.
//
// apiKey is reported in contracts.Sandbox.SandboxAPIKey so the control plane can
// persist it with the row; the field is json:"-" and never leaves the process as
// JSON (store.NewSandbox seals it at rest).
func (d *Docker) describe(ctx context.Context, userID, projectID, name, apiKey string) (*contracts.Sandbox, error) {
	state, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	dir, err := d.workspacePath(userID, projectID)
	if err != nil {
		return nil, err
	}
	return &contracts.Sandbox{
		ID:            name,
		UserID:        userID,
		ProjectID:     projectID,
		State:         dockerState(state),
		Provider:      d.Name(),
		Image:         d.cfg.Image,
		SandboxAPIKey: apiKey,
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
		return "", dockerFailure(args, text, err)
	}
	return text, nil
}

// sandboxKeyPattern matches one SANDBOX_API_KEY=... assignment. The value runs to
// whitespace because `docker run` argv is space-joined in the error below.
var sandboxKeyPattern = regexp.MustCompile(SandboxAPIKeyEnv + `=[^\s"']+`)

// redactSandboxKeys removes key values from text that may be logged or returned.
// Sandbox keys travel in `docker run`'s argv, so any error that echoes the command
// would otherwise print one — in the runner's log, in the runner's HTTP error, and
// from there in the control plane's error.
func redactSandboxKeys(text string) string {
	if !strings.Contains(text, SandboxAPIKeyEnv+"=") {
		return text
	}
	return sandboxKeyPattern.ReplaceAllString(text, SandboxAPIKeyEnv+"=<redacted>")
}

// dockerFailure renders a failed `docker` invocation. It is a separate function so
// the redaction is pinned by a test without a daemon: the argv of a create contains
// the sandbox key.
func dockerFailure(args []string, output string, err error) error {
	return fmt.Errorf("sandbox: docker %s: %w: %s",
		redactSandboxKeys(strings.Join(args, " ")), err, redactSandboxKeys(output))
}

func (d *Docker) lastArg(args []string) string {
	for index := len(args) - 1; index >= 0; index-- {
		if !strings.HasPrefix(args[index], "-") && args[index] != "inspect" && args[index] != "start" {
			return args[index]
		}
	}
	return ""
}

// workspaceMount returns the directory the container has mounted at /workspace,
// as this process names it. It is the container's own statement of what it can
// see, which is what Ensure compares against the pair's directory before reusing
// a container.
//
// A bind mount reports its source directly. A volume mount reports the volume and
// the subpath it was given; the same volume is at DataRoot here, so the subpath
// re-roots to the directory this process reads — the same string the bind shape
// would have produced, which is what keeps Ensure's comparison one comparison.
func (d *Docker) workspaceMount(ctx context.Context, name string) (string, error) {
	if d.cfg.WorkspaceVolume != "" {
		return d.volumeWorkspaceMount(ctx, name)
	}
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

// volumeWorkspaceMount reads the mount *requested* for /workspace out of the
// container's HostConfig, because inspect's runtime Mounts do not report a
// volume's subpath. HostConfig is still the container's own record — written when
// it was created, and the thing a later `docker start` preserves — so a container
// from another project (or from the bind-mount shape) is still recognised and
// still replaced rather than adopted.
func (d *Docker) volumeWorkspaceMount(ctx context.Context, name string) (string, error) {
	out, err := d.docker(ctx, "inspect", "--format", "{{json .HostConfig.Mounts}}", name)
	if err != nil {
		return "", err
	}
	return parseVolumeWorkspaceMount(out, d.cfg.WorkspaceVolume, d.cfg.WorkspaceVolumeRoot)
}

// parseVolumeWorkspaceMount is the half of the above that can be tested without a
// daemon: given one HostConfig.Mounts document, which directory does this container
// have at /workspace?
func parseVolumeWorkspaceMount(out, volume, volumeRoot string) (string, error) {
	var mounts []struct {
		Type          string `json:"Type"`
		Source        string `json:"Source"`
		Target        string `json:"Target"`
		VolumeOptions *struct {
			Subpath string `json:"Subpath"`
		} `json:"VolumeOptions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts); err != nil {
		return "", fmt.Errorf("sandbox: docker: inspect mount config: %w", err)
	}
	for _, mount := range mounts {
		if path.Clean(filepath.ToSlash(mount.Target)) != containerWorkspace {
			continue
		}
		subpath := ""
		if mount.VolumeOptions != nil {
			subpath = strings.TrimSpace(mount.VolumeOptions.Subpath)
		}
		if mount.Type != "volume" || mount.Source != volume {
			return "", fmt.Errorf("sandbox: docker: mounts %s %q at %s, want volume %q",
				mount.Type, mount.Source, containerWorkspace, volume)
		}
		if subpath == "" {
			// Every sandbox mounts its project's subdirectory, never the volume
			// root: the root is every project's parent and must not be a workspace.
			return "", fmt.Errorf("sandbox: docker: mounts volume %q at %s without a subpath",
				mount.Source, containerWorkspace)
		}
		return filepath.Join(volumeRoot, filepath.FromSlash(path.Clean(subpath))), nil
	}
	return "", fmt.Errorf("sandbox: docker: no %s mount", containerWorkspace)
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

// Endpoint reports the address that serves the sandbox API for one sandbox.
//
// With ContainerEndpoint it is the container's name on the shared network, and
// the container is inspected first so a missing sandbox is ErrNotFound rather
// than a hostname that fails later. Otherwise it is the host port Docker
// assigned, read back rather than remembered: the port is assigned at creation
// and survives a control-plane restart, and a number kept in memory would be
// wrong after either.
func (d *Docker) Endpoint(ctx context.Context, sandboxID string) (string, error) {
	if d.cfg.APIPort <= 0 {
		return "", fmt.Errorf("sandbox: docker: no api port is published for this provider")
	}
	name := d.resolve(sandboxID)
	if d.cfg.ContainerEndpoint {
		if _, err := d.inspect(ctx, name); err != nil {
			return "", err
		}
		return fmt.Sprintf("http://%s:%d", name, d.cfg.APIPort), nil
	}
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
	mapped, err := containerPathFor(d.cfg.DataRoot, hostPath)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: %w", err)
	}
	return mapped, nil
}

// containerPathFor is that mapping, shared with the runner provider so the process
// that owns the Docker CLI and the process that asks it over HTTP answer
// identically. It is pure path arithmetic over the configured data root: no
// daemon, no CLI, and nothing to keep in sync.
func containerPathFor(dataRoot, hostPath string) (string, error) {
	// A sandbox record may already carry a container path: provision lays out
	// repositories from RootDir, which is containerWorkspace.
	if clean := path.Clean(filepath.ToSlash(strings.TrimSpace(hostPath))); clean == containerWorkspace ||
		strings.HasPrefix(clean, containerWorkspace+"/") {
		return clean, nil
	}

	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", fmt.Errorf("data root %q: %w", dataRoot, err)
	}
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", hostPath, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("%q relative to %q: %w", abs, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the data root %q", abs, root)
	}
	segments := strings.Split(filepath.ToSlash(rel), "/")
	if rel == "." || len(segments) < 2 {
		return "", fmt.Errorf("%q is not inside a project workspace under %q", abs, root)
	}
	for _, segment := range segments[:2] {
		if err := safeSegment(segment); err != nil {
			return "", fmt.Errorf("%q: %w", abs, err)
		}
	}
	if len(segments) == 2 {
		return containerWorkspace, nil
	}
	return path.Join(containerWorkspace, path.Join(segments[2:]...)), nil
}
