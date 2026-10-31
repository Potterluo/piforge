// Package janitor removes things the control plane left behind.
//
// It has two jobs, both decided by the filesystem and the database rather than
// by a queue:
//
//   - a sandbox directory with no project row is an orphan. Deleting the project
//     row used to leave one on disk whenever the destroy failed (on Windows a
//     running agent, a terminal or the sandbox browser holds a file inside the
//     checkout, and `os.RemoveAll` cannot delete it). The repair for that path is
//     "refuse the delete and keep the project" (see httpapi.handleDeleteProject);
//     this package is the other half — it cleans the directories older builds
//     already orphaned, and any a crash between the two filesystem operations
//     could produce.
//   - a skill snapshot directory with no session row is an orphan (FR-14.2). The
//     snapshot freezes a session's skill tree, so it belongs to that session; the
//     delete handler removes it eagerly, and this sweep is what catches the ones
//     a crash, an ephemeral-session expiry or an older build left behind.
//
// The criterion is deliberately "no row names it": a crash at any point is still
// recoverable by the next pass and there is no bookkeeping that can itself go
// stale.
package janitor

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
)

// Projects is what the janitor asks the store about sandboxes. It returns every
// project id with its owner; one query for a pass, not one per directory.
type Projects interface {
	ProjectIndex(ctx context.Context) (map[string]string, error)
}

// Sessions is what the janitor asks the store about skill snapshots. It returns
// every live session id; one query for a pass, so a snapshot with no session is
// visible without a query per directory.
type Sessions interface {
	SessionIDs(ctx context.Context) ([]string, error)
}

// Config wires a Janitor. A pass runs only for the halves that are configured:
// a deployment with a container provider has no sandbox directories here, and a
// deployment that never mounts skills has no snapshots.
type Config struct {
	// Root is the local sandbox provider's root (…/sandboxes). Empty disables
	// the sandbox half of the pass: there is nothing safe to scan.
	Root string
	// Projects is required for the sandbox half. Nil disables it.
	Projects Projects
	// SnapshotRoot is where FR-14.2's per-session skill snapshots live
	// (…/skill-snapshots). Empty disables the snapshot half.
	SnapshotRoot string
	// Sessions is required for the snapshot half. Nil disables it.
	Sessions Sessions
	// Unmount detaches a snapshot that is still bind-mounted; nil uses the
	// platform's lazy detach. Tests use it to observe the order.
	Unmount func(string) error
	// RemoveSnapshot is the removal itself; nil uses skillsnap.RemoveDir. A
	// test uses it to make one removal fail and assert the sweep keeps going
	// and retries on the next pass.
	RemoveSnapshot func(root, name string, unmount func(string) error) (skillsnap.Freed, error)
	Logger         *slog.Logger
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
	// RemovedSnapshots is how many session snapshot directories went.
	RemovedSnapshots int
	// SnapshotBytes is the size of the files they held, so the log can say how
	// much disk the sweep reclaimed instead of only how many directories.
	SnapshotBytes int64
	Failures      int
}

// Janitor sweeps orphaned sandbox directories and skill snapshots.
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
	return &Janitor{cfg: cfg}
}

// enabled reports whether this pass has anything to do.
func (j *Janitor) enabled() bool {
	return (j.cfg.Root != "" && j.cfg.Projects != nil) ||
		(j.cfg.SnapshotRoot != "" && j.cfg.Sessions != nil)
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
	if j.cfg.SnapshotRoot != "" && j.cfg.Sessions != nil {
		if err := j.sweepSnapshots(ctx, &res); err != nil {
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

// sweepSnapshots is the skill-snapshot half (FR-14.2): every session snapshot
// directory whose session no longer exists.
//
// The live set is read first, and a failure to read it aborts this half instead
// of sweeping against an empty set: "the store did not answer" must never become
// "every snapshot is garbage".
func (j *Janitor) sweepSnapshots(ctx context.Context, res *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := j.cfg.Sessions.SessionIDs(ctx)
	if err != nil {
		return fmt.Errorf("janitor: session index: %w", err)
	}
	live := skillsnap.LiveNames(ids)

	entries, err := os.ReadDir(j.cfg.SnapshotRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("janitor: read %s: %w", j.cfg.SnapshotRoot, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || !skillsnap.IsSessionDir(entry.Name()) {
			continue
		}
		name := entry.Name()
		if live[name] {
			continue
		}
		if j.cfg.DryRun {
			j.cfg.Logger.Info("janitor: would remove an orphaned skill snapshot", "snapshot", name)
			res.RemovedSnapshots++
			continue
		}
		freed, rerr := j.cfg.RemoveSnapshot(j.cfg.SnapshotRoot, name, j.cfg.Unmount)
		if rerr != nil {
			// One stuck snapshot must not stop the rest of the pass, and the
			// next tick tries it again: the criterion is still "no session
			// names it", so nothing has to be remembered.
			res.Failures++
			j.cfg.Logger.Warn("janitor: an orphaned skill snapshot could not be removed; will retry",
				"snapshot", name, "error", rerr.Error())
			continue
		}
		res.RemovedSnapshots += freed.Dirs
		res.SnapshotBytes += freed.Bytes
	}

	if res.RemovedSnapshots > 0 {
		j.cfg.Logger.Info("janitor: removed orphaned skill snapshots",
			"count", res.RemovedSnapshots, "bytes", res.SnapshotBytes, "freed", humanBytes(res.SnapshotBytes))
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
