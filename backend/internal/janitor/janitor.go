// Package janitor removes things the control plane left behind.
//
// Today it has exactly one job: a sandbox directory with no project row is an
// orphan, and deleting the project row used to leave one on disk whenever the
// destroy failed (on Windows a running agent, a terminal or the sandbox browser
// holds a file inside the checkout, and `os.RemoveAll` cannot delete it). The
// repair for that path is "refuse the delete and keep the project" (see
// httpapi.handleDeleteProject); this package is the other half — it cleans the
// directories older builds already orphaned, and any that a crash between the
// two filesystem operations could produce.
//
// The criterion is deliberately the filesystem and the database, not a queue:
// a directory is an orphan when no project row names it, so a crash at any point
// is still recoverable by the next pass and there is no bookkeeping that can
// itself go stale.
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

// Projects is what the janitor asks the store. It returns every project id with
// its owner; one query for a pass, not one per directory.
type Projects interface {
	ProjectIndex(ctx context.Context) (map[string]string, error)
}

// Config wires a Janitor.
type Config struct {
	// Root is the local sandbox provider's root (…/sandboxes). Empty disables
	// the pass: there is nothing safe to scan.
	Root string
	// Projects is required.
	Projects Projects
	Logger   *slog.Logger
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
	Failures     int
}

// Janitor sweeps orphaned sandbox directories.
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
	return &Janitor{cfg: cfg}
}

// Start runs one pass immediately and then on every tick until ctx is done.
//
// The immediate pass is the point after a restart: a deployment that crashed
// mid-delete should not wait a whole interval before its leftovers go.
func (j *Janitor) Start(ctx context.Context) {
	if j.cfg.Root == "" || j.cfg.Projects == nil {
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
		j.cfg.Logger.Error("janitor: sandbox sweep failed", "error", err.Error())
	}
}

// RunOnce performs one pass: every `<root>/<user>/<project>` whose project row
// is missing (or owned by somebody else) is removed, together with its state
// file. A directory whose row exists is never touched, whatever its contents.
func (j *Janitor) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	if j.cfg.Root == "" || j.cfg.Projects == nil {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	index, err := j.cfg.Projects.ProjectIndex(ctx)
	if err != nil {
		return res, fmt.Errorf("janitor: project index: %w", err)
	}

	users, err := os.ReadDir(j.cfg.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("janitor: read %s: %w", j.cfg.Root, err)
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
	return res, nil
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
