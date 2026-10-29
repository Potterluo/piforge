// Package snapshot records what a sandbox contained before the idle reaper is
// allowed to destroy it.
//
// # Why this is an index, not a copy
//
// The provider's data directory is the durable half of a sandbox: Docker's
// Destroy removes the container and deliberately leaves the bind-mounted
// workspace on disk (sandbox/docker.go), and the local provider's directory is
// the workspace itself. Copying every file again would double the disk cost of
// every tenant and would still race an agent that writes while the copy runs.
// So the snapshot here answers the question a user actually asks afterwards —
// "which files existed, how big were they, what branch and commit was I on, and
// what was uncommitted?" — with a manifest that points at the surviving tree.
//
// It is deliberately not a tar of the workspace. A real filesystem snapshot and
// its restore path are FR-5.6 work; this package only makes the reclaim in
// FR-5.3 safe by fixation (a durable record) before release (Destroy).
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/store"
)

// DefaultMaxFiles bounds how many entries one manifest lists. A workspace with
// node_modules can contain hundreds of thousands of files; the index is meant to
// be readable, not to become a second copy of the tree by weight of JSON.
const DefaultMaxFiles = 20000

// Config wires the writer.
type Config struct {
	// Root is where manifests are written: <dataDir>/snapshots.
	Root string
	// Store receives one row per snapshot. Required.
	Store *store.Store
	// Git reads branch, HEAD and the uncommitted list. Nil uses the platform git.
	Git *gitx.Runner
	// Now is injectable for tests.
	Now func() time.Time
	// MaxFiles overrides DefaultMaxFiles when positive.
	MaxFiles int
}

// Writer takes manifests. It is safe to reuse; each Take is independent.
type Writer struct {
	root     string
	store    *store.Store
	git      *gitx.Runner
	now      func() time.Time
	maxFiles int
}

// Manifest is the JSON document written into
// <root>/<sandbox-id>/<timestamp>/manifest.json.
type Manifest struct {
	Version   int        `json:"version"`
	SandboxID string     `json:"sandbox_id"`
	UserID    string     `json:"user_id"`
	ProjectID string     `json:"project_id"`
	Provider  string     `json:"provider"`
	CreatedAt time.Time  `json:"created_at"`
	RootDir   string     `json:"root_dir"`
	Checkouts []Checkout `json:"checkouts"`
	// FileCount and Bytes describe the files listed below, which is capped by
	// MaxFiles. Truncated says whether the cap was hit.
	FileCount int   `json:"file_count"`
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated"`
}

// Checkout is one git checkout inside the sandbox: the main `repo` checkout or a
// per-branch worktree.
type Checkout struct {
	// Path is the host path; RelPath is its place inside the sandbox ("repo",
	// "worktrees/fix-cache").
	Path    string `json:"path"`
	RelPath string `json:"rel_path"`
	Kind    string `json:"kind"` // main | worktree
	Branch  string `json:"branch,omitempty"`
	Head    string `json:"head,omitempty"`
	// Uncommitted lists every path git reports as modified, staged, untracked
	// or conflicted.
	Uncommitted []string `json:"uncommitted"`
	Files       []File   `json:"files"`
	// Truncated says the file cap cut this checkout's list short.
	Truncated bool `json:"truncated,omitempty"`
	// GitError is set when the checkout exists but git could not read it. The
	// manifest still succeeds: the files are the part that must not be lost.
	GitError string `json:"git_error,omitempty"`
}

// File is one regular file (or symlink) below a checkout.
type File struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Symlink bool   `json:"symlink,omitempty"`
}

// New builds a writer. Root is created lazily by Take so a deployment that
// disables reclaim never grows an empty directory.
func New(cfg Config) (*Writer, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, fmt.Errorf("snapshot: Config.Root is required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("snapshot: Config.Store is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("snapshot: resolve root %q: %w", cfg.Root, err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	git := cfg.Git
	if git == nil {
		git = gitx.New(gitx.Config{})
	}
	maxFiles := cfg.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	return &Writer{
		root:     filepath.Clean(root),
		store:    cfg.Store,
		git:      git,
		now:      now,
		maxFiles: maxFiles,
	}, nil
}

// Root reports the manifest root.
func (w *Writer) Root() string { return w.root }

// Take writes the manifest for sb and records its row. It returns only after
// both the file and the row exist; the reaper uses that as the permission to
// destroy.
func (w *Writer) Take(ctx context.Context, sb *contracts.Sandbox) (*store.Snapshot, error) {
	if sb == nil {
		return nil, fmt.Errorf("snapshot: sandbox is nil")
	}
	if sb.ID == "" {
		return nil, fmt.Errorf("snapshot: sandbox id is empty")
	}
	if !safeSegment(sb.ID) {
		return nil, fmt.Errorf("snapshot: unsafe sandbox id %q", sb.ID)
	}
	if strings.TrimSpace(sb.RootDir) == "" {
		return nil, fmt.Errorf("snapshot: sandbox %s has no root directory", sb.ID)
	}
	if fi, err := os.Stat(sb.RootDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("snapshot: sandbox %s root %q is not readable: %w", sb.ID, sb.RootDir, err)
	}

	checkouts, err := w.readCheckouts(ctx, sb.RootDir)
	if err != nil {
		return nil, err
	}
	if len(checkouts) == 0 {
		return nil, fmt.Errorf("snapshot: sandbox %s has no repo/ or worktrees/ checkout", sb.ID)
	}

	now := w.now().UTC()
	man := Manifest{
		Version:   1,
		SandboxID: sb.ID,
		UserID:    sb.UserID,
		ProjectID: sb.ProjectID,
		Provider:  sb.Provider,
		CreatedAt: now,
		RootDir:   sb.RootDir,
		Checkouts: checkouts,
	}
	head := ""
	for _, c := range checkouts {
		man.FileCount += len(c.Files)
		for _, f := range c.Files {
			man.Bytes += f.Size
		}
		if c.Truncated {
			man.Truncated = true
		}
		if head == "" && c.Head != "" {
			head = c.Head
		}
	}

	body, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("snapshot: marshal manifest for %s: %w", sb.ID, err)
	}
	body = append(body, '\n')

	// RFC3339 with colons cannot be a Windows directory name, so the stamp is
	// the same instant in a filesystem-safe layout.
	stamp := now.Format("20060102T150405.000000000Z")
	rel := path.Join(sb.ID, stamp, "manifest.json")
	full := filepath.Join(w.root, sb.ID, stamp, "manifest.json")
	if err := writeFileAtomic(full, body); err != nil {
		return nil, fmt.Errorf("snapshot: write manifest for %s: %w", sb.ID, err)
	}

	sum := sha256.Sum256(body)
	row, err := w.store.CreateSnapshot(ctx, sb.UserID, store.Snapshot{
		SandboxID:   sb.ID,
		Kind:        "fs",
		GitCommit:   head,
		ContentHash: hex.EncodeToString(sum[:]),
		SizeBytes:   man.Bytes,
		BlobKey:     filepath.ToSlash(rel),
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot: record manifest for %s: %w", sb.ID, err)
	}
	return row, nil
}

// readCheckouts finds repo/ and worktrees/* and builds one index per checkout.
func (w *Writer) readCheckouts(ctx context.Context, root string) ([]Checkout, error) {
	type spec struct {
		path    string
		relPath string
		kind    string
	}
	specs := make([]spec, 0, 4)

	repoDir := filepath.Join(root, "repo")
	if fi, err := os.Stat(repoDir); err == nil && fi.IsDir() {
		specs = append(specs, spec{path: repoDir, relPath: "repo", kind: "main"})
	}
	wtRoot := filepath.Join(root, "worktrees")
	if entries, err := os.ReadDir(wtRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			specs = append(specs, spec{
				path:    filepath.Join(wtRoot, entry.Name()),
				relPath: path.Join("worktrees", entry.Name()),
				kind:    "worktree",
			})
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("snapshot: read %s: %w", wtRoot, err)
	}

	out := make([]Checkout, 0, len(specs))
	remaining := w.maxFiles
	for _, s := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := Checkout{Path: s.path, RelPath: s.relPath, Kind: s.kind}
		files, truncated, err := listFiles(ctx, s.path, remaining)
		if err != nil {
			return nil, fmt.Errorf("snapshot: list %s: %w", s.relPath, err)
		}
		if truncated {
			remaining = 0
		} else {
			remaining -= len(files)
		}
		c.Files = files
		c.Truncated = truncated

		st, serr := w.git.Status(ctx, s.path)
		if serr != nil {
			c.GitError = serr.Error()
		} else {
			c.Branch = st.Branch
			if st.Branch != "" || len(st.Changes) > 0 {
				if head, herr := w.git.Head(ctx, s.path); herr == nil {
					c.Head = head
				} else {
					c.GitError = herr.Error()
				}
			}
			c.Uncommitted = uncommitted(st)
		}
		out = append(out, c)
	}
	return out, nil
}

// listFiles walks one checkout, skipping .git and recording sizes. limit <= 0
// means "already truncated"; the caller still gets whatever was read.
func listFiles(ctx context.Context, root string, limit int) ([]File, bool, error) {
	files := make([]File, 0, 64)
	truncated := limit <= 0
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			return nil
		}
		if len(files) >= limit {
			truncated = true
			return fs.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		f := File{Path: rel}
		if info.Mode()&os.ModeSymlink != 0 {
			f.Symlink = true
		} else {
			f.Size = info.Size()
		}
		files = append(files, f)
		return nil
	})
	if walkErr != nil {
		return nil, false, walkErr
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, truncated, nil
}

// uncommitted returns the sorted, de-duplicated list of paths git calls dirty.
func uncommitted(st *contracts.GitStatus) []string {
	seen := make(map[string]struct{}, len(st.Changes)+len(st.Conflicts))
	out := make([]string, 0, len(st.Changes)+len(st.Conflicts))
	add := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, c := range st.Changes {
		add(c.Path)
	}
	for _, c := range st.Conflicts {
		add(c)
	}
	sort.Strings(out)
	return out
}

// writeFileAtomic writes body to a temp file beside p and renames it, so a
// reader never sees a half-written manifest.
func writeFileAtomic(p string, body []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	return s == filepath.Base(s) && !strings.ContainsAny(s, `/\`)
}
