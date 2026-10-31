// Package runtimedirs is the inventory of every path the control plane creates
// under its data directory.
//
// The product rule this package exists for: a directory that the server creates
// needs an owner, a deletion path, and a fallback for the case where the eager
// deletion fails. Two leaks of exactly that kind were found by hand (frozen skill
// snapshots and e2e data); this is the whole-set check, written down as data so a
// test can fail when a new directory is added without a lifecycle.
//
// The inventory is deliberately declarative: it does not remove anything. The
// janitor reads the same names when it sweeps, the HTTP delete handlers use the
// same ownership rule, and GET /v1/admin/status reports the same per-directory
// usage. One table, three consumers.
package runtimedirs

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Kind distinguishes a directory from a file that happens to live under the
// same data directory. Files are included so the literal-coverage test can
// demand a lifecycle for piforge.db and the two key files too, but only
// directories are swept or reported as storage.
type Kind string

const (
	KindDir  Kind = "dir"
	KindFile Kind = "file"
)

// Owner is the row that decides whether a path is still live. Every directory
// in the inventory has exactly one owner scope.
type Owner string

const (
	// OwnerDeployment is owned by the installation itself: it exists as long as
	// the database exists, and no row can make it garbage.
	OwnerDeployment Owner = "deployment"
	// OwnerSession is owned by one sessions row (branch and sidecar sessions
	// included).
	OwnerSession Owner = "session"
	// OwnerProject is owned by one projects row. The local sandbox provider's
	// <user>/<project> directory is one of these.
	OwnerProject Owner = "project"
	// OwnerSandbox is owned by one sandboxes row. A project can outlive its
	// sandbox (the idle reaper destroys it), so this is a separate scope.
	OwnerSandbox Owner = "sandbox"
	// OwnerSkill is owned by one skills row (private or shared).
	OwnerSkill Owner = "skill"
)

// Entry is one row of the inventory.
type Entry struct {
	// Name is the path relative to the data directory, using forward slashes.
	// It is also the key the tests and the status endpoint use.
	Name string
	Kind Kind
	// Creator names the code path that creates the entry. It is prose on
	// purpose: the point is that an operator can find it.
	Creator string
	// Owner is the scope whose row keeps this entry alive.
	Owner Owner
	// DeletePath is the eager path: which request removes it, and when.
	DeletePath string
	// Fallback is what reclaims it when the eager path fails or never ran. A
	// deployment-owned file says "never reclaimed on purpose" here, which is a
	// decision rather than an omission.
	Fallback string
	// Janitor is the sweep's short name. Empty for entries that are not swept
	// (deployment files, and the sandbox provider root whose sweep predates
	// this table and has its own name).
	Janitor string
}

// entries is the inventory. Keep it sorted by Name: the admin page and the
// tests both render it in this order, and a stable order makes a diff readable.
var entries = []Entry{
	{
		Name:       "artifacts",
		Kind:       KindDir,
		Creator:    "httpapi.saveScreenshot (browser screenshot)",
		Owner:      OwnerSession,
		DeletePath: "DELETE /v1/sessions/{id} and DELETE /v1/projects/{id} remove <artifacts>/<session> after the row is gone",
		Fallback:   "janitor artifact sweep: a directory whose session id has no sessions row",
		Janitor:    "artifacts",
	},
	{
		Name:       "master.key",
		Kind:       KindFile,
		Creator:    "crypto.NewFromEnv when PIFORGE_MASTER_KEY is unset",
		Owner:      OwnerDeployment,
		DeletePath: "never: losing it makes every sealed provider key unreadable",
		Fallback:   "none by design; it is part of the deployment backup, not session state",
	},
	{
		Name:       "pi-agents",
		Kind:       KindDir,
		Creator:    "pimodel.Write / pimodel.WriteMCP from the agent factory",
		Owner:      OwnerSession,
		DeletePath: "DELETE /v1/sessions/{id} and DELETE /v1/projects/{id} remove <pi-agents>/<session>",
		Fallback:   "janitor agent-directory sweep: a directory whose session id has no sessions row",
		Janitor:    "pi-agents",
	},
	{
		Name:       "piforge.db",
		Kind:       KindFile,
		Creator:    "store.Open (SQLite database; -wal/-shm are SQLite siblings)",
		Owner:      OwnerDeployment,
		DeletePath: "never: it is the deployment's state",
		Fallback:   "none by design; the -wal/-shm files are bounded by SQLite checkpoints",
	},
	{
		Name:       "sandboxes",
		Kind:       KindDir,
		Creator:    "sandbox.Manager.Ensure (local provider: <root>/<usr>/<prj>)",
		Owner:      OwnerProject,
		DeletePath: "DELETE /v1/projects/{id} stops every holder, retries Destroy, and refuses the delete if the directory cannot go",
		Fallback:   "janitor sandbox sweep: <usr>/<prj> with no projects row (state file removed with it)",
		Janitor:    "sandboxes",
	},
	{
		Name:       "secret.key",
		Kind:       KindFile,
		Creator:    "store.secretBoxFor when PIFORGE_SECRET_KEY is unset",
		Owner:      OwnerDeployment,
		DeletePath: "never: losing it makes stored MCP/provider secrets unreadable",
		Fallback:   "none by design; it is part of the deployment backup",
	},
	{
		Name:       "skill-snapshots",
		Kind:       KindDir,
		Creator:    "provision/skillsnap (frozen skill tree for one session)",
		Owner:      OwnerSession,
		DeletePath: "DELETE /v1/sessions/{id} and DELETE /v1/projects/{id} remove both the current and the legacy name",
		Fallback:   "janitor skill-snapshot sweep: a snapshot name that no live session id maps to",
		Janitor:    "skill-snapshots",
	},
	{
		Name:       "skills",
		Kind:       KindDir,
		Creator:    "httpapi.writeSkillBundle, seed skills, and the import handlers",
		Owner:      OwnerSkill,
		DeletePath: "DELETE /v1/skills/{id} removes <skills>/<skill> after the row is gone",
		Fallback:   "janitor skill-bundle sweep: a skl_* directory (or a stale skl_*.tmp staging dir) with no skills row",
		Janitor:    "skills",
	},
	{
		Name:       "snapshots",
		Kind:       KindDir,
		Creator:    "snapshot.Writer.Take (the idle reaper's pre-destroy manifest)",
		Owner:      OwnerSandbox,
		DeletePath: "DELETE /v1/projects/{id} removes <snapshots>/<sandbox> after the sandbox row cascades",
		Fallback:   "janitor sandbox-snapshot sweep: a directory whose sandbox id has no sandboxes row",
		Janitor:    "snapshots",
	},
}

// Inventory returns a copy of the table, so a caller cannot mutate the single
// source of truth.
func Inventory() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// Lookup returns the entry for a data-directory-relative name.
func Lookup(name string) (Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Dirs returns only the directory rows, in inventory order. This is what the
// status endpoint reports: a file's size is useful, but "how many sessions does
// this directory hold" is the question growth makes people ask.
func Dirs() []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == KindDir {
			out = append(out, e)
		}
	}
	return out
}

// Usage is one directory's measured footprint. Entries counts the direct
// children (sessions, projects, skills); Files and Bytes walk the whole tree,
// because a single session's checkout can dominate the disk.
type Usage struct {
	Name string `json:"name"`
	// Entries is the number of direct children. A missing directory is zero.
	Entries int `json:"entries"`
	// Files is the number of regular files below the directory.
	Files int `json:"files"`
	// Bytes is the sum of their sizes.
	Bytes int64 `json:"bytes"`
	// Truncated is set when MaxFiles cut the walk short, so an operator knows
	// the number is a lower bound rather than silently wrong.
	Truncated bool `json:"truncated,omitempty"`
}

// DefaultMaxFiles bounds one directory's walk. A sandbox with node_modules can
// hold hundreds of thousands of files; the admin page polls, so an unbounded
// walk would turn an idle screen into a disk scan. The cap is high enough that
// a normal deployment is never truncated.
const DefaultMaxFiles = 200000

// Measure walks one root and returns its usage. A missing root is zero, not an
// error: a deployment that never took a screenshot has no artifacts directory.
func Measure(name, root string, maxFiles int) Usage {
	u := Usage{Name: name}
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	children, err := os.ReadDir(root)
	if err != nil {
		return u
	}
	u.Entries = len(children)
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if u.Files >= maxFiles {
			u.Truncated = true
			return fs.SkipAll
		}
		u.Files++
		if info, ierr := d.Info(); ierr == nil {
			u.Bytes += info.Size()
		}
		return nil
	})
	return u
}

// MeasureAll measures every directory row under dataDir. The result is sorted
// by name because the inventory already is; callers can rely on it for stable
// output.
func MeasureAll(dataDir string, maxFiles int) []Usage {
	dirs := Dirs()
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	out := make([]Usage, 0, len(dirs))
	for _, e := range dirs {
		out = append(out, Measure(e.Name, filepath.Join(dataDir, filepath.FromSlash(e.Name)), maxFiles))
	}
	return out
}
