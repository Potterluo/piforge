// Package janitor removes things the control plane left behind.
//
// Every directory the server creates under <dataDir> has an owner row and an
// eager delete path; this package is the fallback for the case where that path
// failed or never ran. The inventory is internal/runtimedirs, and the test in
// that package fails when a directory is added without one.
//
// The criterion is deliberately "no row names it": a crash at any point is still
// recoverable by the next pass and there is no bookkeeping that can itself go
// stale. The sweeps are:
//
//   - <sandboxes>/<usr>/<prj> with no projects row (the destroy-failed case);
//   - <pi-agents>/<session> with no sessions row (models.json / mcp.json);
//   - <artifacts>/<session> with no sessions row (browser screenshots);
//   - <skill-snapshots>/<session> with no session (FR-14.2 frozen trees);
//   - <snapshots>/<sandbox> with no sandboxes row (the idle reaper's manifests);
//   - <skills>/<skl_…> with no skills row, including a stale skl_*.tmp staging
//     directory left by an interrupted bundle upload.
//
// A removal that fails (a live process holds a file, a mount is still attached)
// is logged and retried on the next pass; it never aborts the rest of the pass.
// An index that cannot be read aborts that half instead of sweeping against an
// empty set: "the store did not answer" must never become "everything on disk is
// garbage".
package janitor

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/skillsnap"
)

// stateDirName is the sandbox provider's own bookkeeping, next to the per-user
// directories. It is not a project, and it is cleaned one state file at a time
// when its directory goes.
const stateDirName = "_state"

// A directory name has to look like an identifier before this package will act
// on it. A deployment's data directory may hold anything an operator put there;
// removing a directory named like a project but created by hand would be this
// package overreaching.
var (
	userDirPattern    = regexp.MustCompile(`^usr_[A-Za-z0-9]+$`)
	projectDirPattern = regexp.MustCompile(`^prj_[A-Za-z0-9]+$`)
	// sandboxDirPattern is a sandboxes row id (the snapshot root's child), and
	// skillDirPattern is a skills row id. The optional .tmp suffix is the
	// staging directory writeSkillBundle renames into place.
	sandboxDirPattern = regexp.MustCompile(`^sbx_[A-Za-z0-9]+$`)
	skillDirPattern   = regexp.MustCompile(`^skl_[A-Za-z0-9]+(?:\.tmp)?$`)
)

// Projects is what the janitor asks the store about sandboxes. It returns every
// project id with its owner; one query for a pass, not one per directory.
type Projects interface {
	ProjectIndex(ctx context.Context) (map[string]string, error)
}

// Sessions is what the janitor asks the store about per-session directories. It
// returns every live session id; one query for a pass, so a directory with no
// session is visible without a query per directory.
type Sessions interface {
	SessionIDs(ctx context.Context) ([]string, error)
}

// Sandboxes is what the janitor asks the store about the snapshot root. The
// directories there are keyed by sandbox id, not by project: the idle reaper
// destroys a sandbox while its project stays.
type Sandboxes interface {
	SandboxIDs(ctx context.Context) ([]string, error)
}

// Skills is what the janitor asks the store about bundle directories. Shared
// and private rows both count; only "no row at all" is an orphan.
type Skills interface {
	SkillIDs(ctx context.Context) ([]string, error)
}

// Config wires a Janitor. A pass runs only for the halves that are configured:
// a deployment with a container provider has no sandbox directories here, a
// deployment that never took a screenshot has no artifacts, and so on.
type Config struct {
	// Root is the local sandbox provider's root (…/sandboxes). Empty disables
	// the sandbox half of the pass: there is nothing safe to scan.
	Root string
	// Projects is required for the sandbox half. Nil disables it.
	Projects Projects
	// SnapshotRoot is where FR-14.2's per-session skill snapshots live
	// (…/skill-snapshots). Empty disables that half.
	SnapshotRoot string
	// AgentRoot is …/pi-agents, one directory per session.
	AgentRoot string
	// ArtifactsRoot is …/artifacts, one directory per session.
	ArtifactsRoot string
	// SnapshotsRoot is …/snapshots, one directory per sandbox.
	SnapshotsRoot string
	// SkillsRoot is …/skills, one directory per skill.
	SkillsRoot string
	// Sessions is required for the session-keyed halves. Nil disables them.
	Sessions Sessions
	// Sandboxes is required for the sandbox-snapshot half. Nil disables it.
	Sandboxes Sandboxes
	// Skills is required for the skill-bundle half. Nil disables it.
	Skills Skills
	// Unmount detaches a directory that is still bind-mounted; nil uses the
	// platform's lazy detach. Tests use it to observe the order.
	Unmount func(string) error
	// RemoveSnapshot is the skill-snapshot removal itself; nil uses
	// skillsnap.RemoveDir. A test uses it to make one removal fail and assert
	// the sweep keeps going and retries on the next pass.
	RemoveSnapshot func(root, name string, unmount func(string) error) (skillsnap.Freed, error)
	// RemoveTree is the removal used by the other roots; nil uses
	// skillsnap.RemoveNamed (detach, RemoveAll, one writable retry). Tests use
	// it the same way as RemoveSnapshot.
	RemoveTree func(root, name string, unmount func(string) error) (skillsnap.Freed, error)
	Logger     *slog.Logger
	// Interval is how often Start runs after its first immediate pass. Defaults
	// to ten minutes.
	Interval time.Duration
	// DryRun reports what it would remove without removing it. Tests and an
	// operator poking at a live deployment use it.
	DryRun bool
}

// Result counts one pass.
type Result struct {
	RemovedDirs  int
	RemovedState int
	// RemovedSnapshots is how many session skill-snapshot directories went.
	RemovedSnapshots int
	// SnapshotBytes is the size of the files they held, so the log can say how
	// much disk the sweep reclaimed instead of only how many directories.
	SnapshotBytes int64
	// The other owned roots. Each pair is count and bytes for the same reason.
	RemovedAgents        int
	AgentBytes           int64
	RemovedArtifacts     int
	ArtifactBytes        int64
	RemovedSandboxShots  int
	SandboxSnapshotBytes int64
	RemovedSkillBundles  int
	SkillBytes           int64
	Failures             int
}

// Janitor sweeps every orphaned directory in the runtime inventory.
type Janitor struct {
	cfg Config
}

// New builds a janitor. A nil logger falls back to slog.Default.
func New(cfg Config) *Janitor {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.RemoveSnapshot == nil {
		cfg.RemoveSnapshot = skillsnap.RemoveDir
	}
	if cfg.RemoveTree == nil {
		cfg.RemoveTree = skillsnap.RemoveNamed
	}
	return &Janitor{cfg: cfg}
}

// enabled reports whether this pass has anything to do.
func (j *Janitor) enabled() bool {
	return (j.cfg.Root != "" && j.cfg.Projects != nil) ||
		(j.cfg.SnapshotRoot != "" && j.cfg.Sessions != nil) ||
		(j.cfg.AgentRoot != "" && j.cfg.Sessions != nil) ||
		(j.cfg.ArtifactsRoot != "" && j.cfg.Sessions != nil) ||
		(j.cfg.SnapshotsRoot != "" && j.cfg.Sandboxes != nil) ||
		(j.cfg.SkillsRoot != "" && j.cfg.Skills != nil)
}

// Start runs one pass immediately and then on every tick until ctx is done.
//
// The immediate pass is the point after a restart: a deployment that crashed
// mid-delete should not wait a whole interval before its leftovers go.
func (j *Janitor) Start(ctx context.Context) {
	if !j.enabled() {
		return
	}
	j.pass(ctx)
	ticker := time.NewTicker(j.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.pass(ctx)
		}
	}
}

func (j *Janitor) pass(ctx context.Context) {
	if _, err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
		j.cfg.Logger.Error("janitor: sweep failed", "error", err.Error())
	}
}

// RunOnce performs one pass.
//
// Every `<root>/<user>/<project>` whose project row is missing (or owned by
// somebody else) is removed, together with its state file. A directory whose row
// exists is never touched, whatever its contents. The same rule then applies to
// `<snapshot-root>/<session>`: a directory whose session still exists stays.
func (j *Janitor) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	if !j.enabled() {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if j.cfg.Root != "" && j.cfg.Projects != nil {
		if err := j.sweepSandboxes(ctx, &res); err != nil {
			return res, err
		}
	}
	if j.cfg.Sessions != nil && (j.cfg.SnapshotRoot != "" || j.cfg.AgentRoot != "" || j.cfg.ArtifactsRoot != "") {
		if err := j.sweepSessionDirs(ctx, &res); err != nil {
			return res, err
		}
	}
	if j.cfg.SnapshotsRoot != "" && j.cfg.Sandboxes != nil {
		if err := j.sweepSandboxSnapshots(ctx, &res); err != nil {
			return res, err
		}
	}
	if j.cfg.SkillsRoot != "" && j.cfg.Skills != nil {
		if err := j.sweepSkillBundles(ctx, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// sweepSandboxes is the project half: every sandbox directory without a row.
func (j *Janitor) sweepSandboxes(ctx context.Context, res *Result) error {
	index, err := j.cfg.Projects.ProjectIndex(ctx)
	if err != nil {
		return fmt.Errorf("janitor: project index: %w", err)
	}

	users, err := os.ReadDir(j.cfg.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("janitor: read %s: %w", j.cfg.Root, err)
	}

	for _, user := range users {
		if !user.IsDir() || user.Name() == stateDirName || !userDirPattern.MatchString(user.Name()) {
			continue
		}
		userID := user.Name()
		projectEntries, derr := os.ReadDir(filepath.Join(j.cfg.Root, userID))
		if derr != nil {
			res.Failures++
			j.cfg.Logger.Warn("janitor: cannot read a user's sandbox directory",
				"user", userID, "error", derr.Error())
			continue
		}
		for _, entry := range projectEntries {
			if !entry.IsDir() || !projectDirPattern.MatchString(entry.Name()) {
				continue
			}
			projectID := entry.Name()
			if owner, ok := index[projectID]; ok && owner == userID {
				continue
			}
			dir := filepath.Join(j.cfg.Root, userID, projectID)
			if j.cfg.DryRun {
				j.cfg.Logger.Info("janitor: would remove an orphaned sandbox directory",
					"user", userID, "project", projectID)
				res.RemovedDirs++
				continue
			}
			if rerr := removeTreeWritable(dir); rerr != nil {
				res.Failures++
				j.cfg.Logger.Warn("janitor: orphaned sandbox directory could not be removed; will retry",
					"user", userID, "project", projectID, "error", rerr.Error())
				continue
			}
			res.RemovedDirs++
			j.cfg.Logger.Info("janitor: removed an orphaned sandbox directory",
				"user", userID, "project", projectID)
			if serr := os.Remove(j.statePath(userID, projectID)); serr == nil {
				res.RemovedState++
			} else if !os.IsNotExist(serr) {
				res.Failures++
				j.cfg.Logger.Warn("janitor: could not remove the orphan's state file",
					"user", userID, "project", projectID, "error", serr.Error())
			}
		}
		// Only removes the directory when it is empty: a live project keeps it.
		_ = os.Remove(filepath.Join(j.cfg.Root, userID))
	}
	return nil
}

// sweepSessionDirs sweeps every directory keyed by session id: the frozen skill
// snapshots (FR-14.2), the agent configuration (models.json / mcp.json) and the
// browser screenshots. The live set is read once for all three, and a failure to
// read it aborts the whole half instead of sweeping against an empty set: "the
// store did not answer" must never become "every session is gone".
func (j *Janitor) sweepSessionDirs(ctx context.Context, res *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := j.cfg.Sessions.SessionIDs(ctx)
	if err != nil {
		return fmt.Errorf("janitor: session index: %w", err)
	}
	live := skillsnap.LiveNames(ids)
	isSessionDir := func(name string) bool { return skillsnap.IsSessionDir(name) }
	isLive := func(name string) bool { return live[name] }

	if j.cfg.SnapshotRoot != "" {
		if err := j.sweepOwned(ctx, "skill-snapshots", "orphaned skill snapshots",
			j.cfg.SnapshotRoot, isSessionDir, isLive, j.cfg.RemoveSnapshot,
			&res.RemovedSnapshots, &res.SnapshotBytes, res); err != nil {
			return err
		}
	}
	if j.cfg.AgentRoot != "" {
		if err := j.sweepOwned(ctx, "pi-agents", "orphaned agent directories",
			j.cfg.AgentRoot, isSessionDir, isLive, j.cfg.RemoveTree,
			&res.RemovedAgents, &res.AgentBytes, res); err != nil {
			return err
		}
	}
	if j.cfg.ArtifactsRoot != "" {
		if err := j.sweepOwned(ctx, "artifacts", "orphaned artifact directories",
			j.cfg.ArtifactsRoot, isSessionDir, isLive, j.cfg.RemoveTree,
			&res.RemovedArtifacts, &res.ArtifactBytes, res); err != nil {
			return err
		}
	}
	return nil
}

// sweepSandboxSnapshots reclaims <data>/snapshots/<sandbox> when the sandboxes
// row is gone. A sandbox is destroyed by the idle reaper while its project
// stays, so this cannot be keyed by project.
func (j *Janitor) sweepSandboxSnapshots(ctx context.Context, res *Result) error {
	ids, err := j.cfg.Sandboxes.SandboxIDs(ctx)
	if err != nil {
		return fmt.Errorf("janitor: sandbox index: %w", err)
	}
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		live[id] = true
	}
	return j.sweepOwned(ctx, "snapshots", "orphaned sandbox snapshots",
		j.cfg.SnapshotsRoot,
		func(name string) bool { return sandboxDirPattern.MatchString(name) },
		func(name string) bool { return live[name] },
		j.cfg.RemoveTree, &res.RemovedSandboxShots, &res.SandboxSnapshotBytes, res)
}

// sweepSkillBundles reclaims <data>/skills/<skl_…> when the skills row is gone,
// and the skl_*.tmp staging directory an interrupted upload can leave behind
// (its row exists only after the rename, so a staging directory has no owner by
// construction).
func (j *Janitor) sweepSkillBundles(ctx context.Context, res *Result) error {
	ids, err := j.cfg.Skills.SkillIDs(ctx)
	if err != nil {
		return fmt.Errorf("janitor: skill index: %w", err)
	}
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		live[id] = true
	}
	return j.sweepOwned(ctx, "skills", "orphaned skill bundles",
		j.cfg.SkillsRoot,
		func(name string) bool { return skillDirPattern.MatchString(name) },
		func(name string) bool {
			return live[name] || live[strings.TrimSuffix(name, ".tmp")]
		},
		j.cfg.RemoveTree, &res.RemovedSkillBundles, &res.SkillBytes, res)
}

// sweepOwned is the one sweep every owned root shares.
//
// It only touches directories whose name matches the caller's pattern, so an
// operator's hand-made directory is never removed. A removal failure (a live
// process, a mount) increments Failures and is retried on the next pass; it does
// not stop the rest of the root. The log line reports count and bytes, which is
// what makes growth and reclamation visible without a shell.
func (j *Janitor) sweepOwned(
	ctx context.Context,
	key, noun, root string,
	owned func(string) bool,
	live func(string) bool,
	remove func(root, name string, unmount func(string) error) (skillsnap.Freed, error),
	count *int,
	bytes *int64,
	res *Result,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("janitor: read %s: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !owned(entry.Name()) {
			continue
		}
		name := entry.Name()
		if live(name) {
			continue
		}
		if j.cfg.DryRun {
			j.cfg.Logger.Info("janitor: would remove a directory with no owner",
				"dir", key, "name", name)
			*count++
			continue
		}
		freed, rerr := remove(root, name, j.cfg.Unmount)
		if rerr != nil {
			res.Failures++
			j.cfg.Logger.Warn("janitor: a directory with no owner could not be removed; will retry",
				"dir", key, "name", name, "error", rerr.Error())
			continue
		}
		*count += freed.Dirs
		*bytes += freed.Bytes
	}
	if *count > 0 {
		verb := "removed"
		if j.cfg.DryRun {
			verb = "would remove"
		}
		j.cfg.Logger.Info("janitor: "+verb+" "+noun,
			"count", *count, "bytes", *bytes, "freed", humanBytes(*bytes))
	}
	return nil
}

// humanBytes renders a byte count the way the log line wants it: an operator
// reading "bytes=18342" should not have to do the division.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// statePath is the local provider's layout: <root>/_state/<user>/<project>.json.
func (j *Janitor) statePath(userID, projectID string) string {
	return filepath.Join(j.cfg.Root, stateDirName, userID, projectID+".json")
}

// removeTreeWritable deletes an orphan whose mounted skills are read-only.
//
// FR-14.2 makes `.pi/skills` read-only; on Windows that bit also refuses
// deletion, so a plain RemoveAll would report the orphan as unremovable forever.
// The same retry-with-writable-modes lives in the sandbox provider's Destroy,
// and this copy keeps the janitor independent of it.
func removeTreeWritable(root string) error {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		_ = os.Chmod(p, 0o644)
		return nil
	})
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		_ = os.Chmod(p, 0o755)
		return nil
	})
	return os.RemoveAll(root)
}
