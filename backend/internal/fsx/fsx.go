// Package fsx is the filesystem side of a PiForge workspace.
//
// Every entry point takes a workspace root and a caller-supplied relative path.
// Making that pair safe is the whole point of this package (PRD §6.3-坑6):
//
//   - lexical cleaning rejects traversal, absolute and rooted paths, NUL bytes;
//   - symlinks are resolved and re-checked, so a link pointing out of the root
//     cannot be used to read or write outside it;
//   - the repository metadata (.git) and the root itself are never writable.
//
// Reads follow the size classification of PRD FR-7.8: whole body up to 2 MiB,
// head+tail up to 20 MiB, metadata only above that.
//
// The package depends only on the standard library and internal/contracts. Git
// status letters and git operations are injected through Config (GitState /
// Git), so the server can wire in internal/gitx without fsx importing it.
package fsx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Defaults for Config.
const (
	DefaultMaxEntries   = 2000
	DefaultMaxInline    = 2 << 20  // 2 MiB: whole file
	DefaultMaxPreview   = 20 << 20 // 20 MiB: download only
	DefaultHeadBytes    = 256 << 10
	DefaultTailBytes    = 256 << 10
	DefaultSearchLimit  = 200
	DefaultMaxWalkFiles = 20000
	DefaultMaxScanBytes = 1 << 20
	DefaultGitBinary    = "git"
)

// truncationMarker separates the head and the tail of a large file preview.
const truncationMarker = "\n\n… (truncated) …\n\n"

// ErrNoGitBackend is returned by the git methods when Config.Git is nil.
var ErrNoGitBackend = errors.New("fsx: no git backend configured")

// AuditEvent describes one mutating filesystem operation. The session layer
// forwards it so the agent learns that a human touched a file (PRD FR-7.10).
type AuditEvent struct {
	Action string // "write" or "delete"
	Path   string // workspace-relative, slash separated
	Size   int64  // bytes written or removed
	At     time.Time
}

// AuditFunc receives every mutation. It must not block.
type AuditFunc func(AuditEvent)

// GitOps is the git half of contracts.Workspace. *gitx.Runner satisfies it
// structurally; fsx never imports gitx.
type GitOps interface {
	Status(ctx context.Context, root string) (*contracts.GitStatus, error)
	Diff(ctx context.Context, root string, req contracts.DiffRequest) ([]contracts.FileDiff, error)
	Log(ctx context.Context, root string, limit int, cursor string) (*contracts.GitGraph, error)
	Stage(ctx context.Context, root string, paths []string) error
	Unstage(ctx context.Context, root string, paths []string) error
	Commit(ctx context.Context, root string, req contracts.CommitRequest) (string, error)
	CreateBranch(ctx context.Context, root, name string) error
	Checkout(ctx context.Context, root, name string) error
	Adopt(ctx context.Context, sourceRoot, targetRoot, message string) (contracts.AdoptResult, error)
}

// Config configures a Service. The zero value is usable: defaults apply and
// git_state stays empty until a backend is injected.
type Config struct {
	// ShowIgnored disables ignore filtering and reveals dot directories.
	ShowIgnored bool
	// MaxEntriesPerDir caps how many entries one directory listing returns.
	MaxEntriesPerDir int
	// MaxInlineBytes / MaxPreviewBytes are the thresholds of PRD FR-7.8.
	MaxInlineBytes  int64
	MaxPreviewBytes int64
	// HeadBytes / TailBytes bound the head+tail preview of a large file.
	HeadBytes int64
	TailBytes int64
	// SearchLimit caps hits per search; MaxWalkFiles caps the fallback walk.
	SearchLimit  int
	MaxWalkFiles int
	// MaxScanBytes caps the per-file scan of the walk fallback.
	MaxScanBytes int64
	// GitBinary is used for `git check-ignore` (default "git").
	GitBinary string
	// Audit is called after every successful write/delete.
	Audit AuditFunc
	// GitState injects the per-path git status letters for the file tree
	// ("M", "A", "D", "U", "!", ""). When nil it is derived from Git if set.
	GitState func(ctx context.Context, root string) (map[string]string, error)
	// Git is the git backend behind the Workspace methods.
	Git GitOps
}

func (c Config) normalise() Config {
	if c.MaxEntriesPerDir <= 0 {
		c.MaxEntriesPerDir = DefaultMaxEntries
	}
	if c.MaxInlineBytes <= 0 {
		c.MaxInlineBytes = DefaultMaxInline
	}
	if c.MaxPreviewBytes <= c.MaxInlineBytes {
		c.MaxPreviewBytes = DefaultMaxPreview
	}
	if c.HeadBytes <= 0 {
		c.HeadBytes = DefaultHeadBytes
	}
	if c.TailBytes <= 0 {
		c.TailBytes = DefaultTailBytes
	}
	if c.SearchLimit <= 0 {
		c.SearchLimit = DefaultSearchLimit
	}
	if c.MaxWalkFiles <= 0 {
		c.MaxWalkFiles = DefaultMaxWalkFiles
	}
	if c.MaxScanBytes <= 0 {
		c.MaxScanBytes = DefaultMaxScanBytes
	}
	if strings.TrimSpace(c.GitBinary) == "" {
		c.GitBinary = DefaultGitBinary
	}
	return c
}

// Service implements contracts.Workspace for one workspace root per call. It
// holds no mutable state and is safe for concurrent use.
type Service struct {
	cfg Config
}

var _ contracts.Workspace = (*Service)(nil)

// New builds a Service, applying defaults.
func New(cfg Config) *Service {
	return &Service{cfg: cfg.normalise()}
}

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// ---------------------------------------------------------------------------
// File tree
// ---------------------------------------------------------------------------

// TreePage is a directory listing plus the truncation signal, which the plain
// Tree signature has nowhere to report for the root level.
type TreePage struct {
	Nodes []*contracts.FileNode
	// Truncated is true when the directory had more entries than
	// MaxEntriesPerDir.
	Truncated bool
}

// Tree lists one directory up to depth levels.
//
// A directory whose children were not loaded (depth exhausted) carries
// Truncated=true so react-arborist renders a lazy arrow instead of an empty
// folder.
func (s *Service) Tree(ctx context.Context, root, relPath string, depth int) ([]*contracts.FileNode, error) {
	page, err := s.TreeWithMeta(ctx, root, relPath, depth)
	if err != nil {
		return nil, err
	}
	return page.Nodes, nil
}

// TreeOptions are the per-request knobs of one listing.
type TreeOptions struct {
	// IncludeIgnored lists what git ignores and what the service normally hides
	// (node_modules, dot directories). The file panel's "显示忽略文件" switch is
	// the only caller: a switch that cannot reveal anything is a lie, and it used
	// to be exactly that because the filtering happened server-side only.
	IncludeIgnored bool
}

// TreeWithMeta is Tree plus the root-level truncation flag.
func (s *Service) TreeWithMeta(ctx context.Context, root, relPath string, depth int) (*TreePage, error) {
	return s.TreePage(ctx, root, relPath, depth, TreeOptions{})
}

// TreeWithOptions lists a directory, honouring per-request options.
func (s *Service) TreePage(ctx context.Context, root, relPath string, depth int, opt TreeOptions) (*TreePage, error) {
	rootAbs, err := absRoot(root)
	if err != nil {
		return nil, err
	}
	dir, err := Resolve(rootAbs, relPath)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %q", ErrNotExist, relPath)
		}
		return nil, fmt.Errorf("fsx: stat %q: %w", relPath, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("fsx: tree %q: %w", relPath, ErrIsDir)
	}
	if depth <= 0 {
		depth = 1
	}
	relDir, err := Rel(rootAbs, dir)
	if err != nil {
		return nil, err
	}
	states := s.gitStates(ctx, rootAbs)
	showIgnored := s.cfg.ShowIgnored || opt.IncludeIgnored
	return s.readDir(ctx, rootAbs, dir, relDir, depth, states, showIgnored)
}

func (s *Service) readDir(ctx context.Context, rootAbs, absDir, relDir string, depth int, states map[string]string, showIgnored bool) (*TreePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fsx: list %q: %w", relDir, err)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("fsx: list %q: %w", relDir, err)
	}

	type candidate struct {
		name  string
		rel   string
		isDir bool
		entry os.DirEntry
	}
	cands := make([]candidate, 0, len(entries))
	for _, de := range entries {
		name := de.Name()
		if name == ".git" {
			continue // never expose repository metadata
		}
		isDir := de.IsDir()
		if !showIgnored {
			if isDir && name == "node_modules" {
				continue
			}
			if isDir && strings.HasPrefix(name, ".") {
				continue
			}
		}
		rel := name
		if relDir != "" {
			rel = relDir + "/" + name
		}
		cands = append(cands, candidate{name: name, rel: rel, isDir: isDir, entry: de})
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].isDir != cands[j].isDir {
			return cands[i].isDir
		}
		return strings.ToLower(cands[i].name) < strings.ToLower(cands[j].name)
	})

	truncated := false
	if len(cands) > s.cfg.MaxEntriesPerDir {
		cands = cands[:s.cfg.MaxEntriesPerDir]
		truncated = true
	}

	if !showIgnored && len(cands) > 0 {
		rels := make([]string, 0, len(cands))
		for _, c := range cands {
			rels = append(rels, c.rel)
		}
		set := s.checkIgnoreBatch(ctx, rootAbs, rels)
		kept := cands[:0]
		for _, c := range cands {
			if set.ignored(c.rel) {
				continue
			}
			kept = append(kept, c)
		}
		cands = kept
	}

	page := &TreePage{Truncated: truncated}
	for _, c := range cands {
		info, ierr := c.entry.Info()
		if ierr != nil {
			continue // raced with a delete; skip the entry
		}
		node := &contracts.FileNode{
			Name:     c.name,
			Path:     c.rel,
			IsDir:    c.isDir,
			Modified: info.ModTime(),
		}
		if !c.isDir {
			node.Size = info.Size()
			node.GitState = states[c.rel]
		} else {
			node.GitState = states[c.rel]
			if depth > 1 {
				child, cerr := s.readDir(ctx, rootAbs, filepath.Join(absDir, c.name), c.rel, depth-1, states, showIgnored)
				if cerr != nil {
					node.Truncated = true
				} else {
					// Always an array on the wire: the tree client iterates it.
					if child.Nodes == nil {
						node.Children = []*contracts.FileNode{}
					} else {
						node.Children = child.Nodes
					}
					node.Truncated = child.Truncated
					if node.GitState == "" {
						node.GitState = aggregateGitState(child.Nodes)
					}
				}
			} else {
				node.Truncated = true
			}
		}
		page.Nodes = append(page.Nodes, node)
	}
	return page, nil
}

// ---------------------------------------------------------------------------
// File content
// ---------------------------------------------------------------------------

// ReadFile returns a preview payload classified by size (PRD FR-7.8).
func (s *Service) ReadFile(ctx context.Context, root, relPath string) (*contracts.FileContent, error) {
	rootAbs, err := absRoot(root)
	if err != nil {
		return nil, err
	}
	abs, err := Resolve(rootAbs, relPath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %q", ErrNotExist, relPath)
		}
		return nil, fmt.Errorf("fsx: open %q: %w", relPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("fsx: stat %q: %w", relPath, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("fsx: read %q: %w", relPath, ErrIsDir)
	}

	size := fi.Size()
	head := make([]byte, min64(8192, size))
	n, rerr := io.ReadFull(f, head)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
		return nil, fmt.Errorf("fsx: read %q: %w", relPath, rerr)
	}
	head = head[:n]
	binary := looksBinary(head)

	content := &contracts.FileContent{
		Path:     filepath.ToSlash(trimRel(relPath)),
		Size:     size,
		Modified: fi.ModTime(),
		Binary:   binary,
		Language: GuessLanguage(abs),
		MimeType: GuessMime(abs, binary),
	}
	if binary {
		return content, nil
	}

	switch {
	case size > s.cfg.MaxPreviewBytes:
		// Metadata only: the UI offers a download instead (FR-7.8).
		content.Truncated = true
		content.Language = ""
		return content, nil
	case size > s.cfg.MaxInlineBytes:
		body, berr := readHeadTail(f, size, s.cfg.HeadBytes, s.cfg.TailBytes)
		if berr != nil {
			return nil, fmt.Errorf("fsx: preview %q: %w", relPath, berr)
		}
		content.Body = body
		content.Truncated = true
	default:
		if _, serr := f.Seek(0, io.SeekStart); serr != nil {
			return nil, fmt.Errorf("fsx: seek %q: %w", relPath, serr)
		}
		body, berr := io.ReadAll(f)
		if berr != nil {
			return nil, fmt.Errorf("fsx: read %q: %w", relPath, berr)
		}
		content.Body = string(body)
	}
	return content, nil
}

// WriteFile creates or replaces a file, creating parent directories.
func (s *Service) WriteFile(ctx context.Context, root, relPath string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fsx: write %q: %w", relPath, err)
	}
	rootAbs, err := absRoot(root)
	if err != nil {
		return err
	}
	abs, err := Resolve(rootAbs, relPath)
	if err != nil {
		return err
	}
	relClean, err := CleanRel(relPath)
	if err != nil {
		return err
	}
	if err := guardWrite(rootAbs, abs, relClean); err != nil {
		return err
	}
	if fi, serr := os.Stat(abs); serr == nil && fi.IsDir() {
		return fmt.Errorf("fsx: write %q: %w", relPath, ErrIsDir)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("fsx: mkdir for %q: %w", relPath, err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		return fmt.Errorf("fsx: write %q: %w", relPath, err)
	}
	s.audit(AuditEvent{Action: "write", Path: filepath.ToSlash(relClean), Size: int64(len(body)), At: time.Now().UTC()})
	return nil
}

// DeleteFile removes a file, or a directory tree.
func (s *Service) DeleteFile(ctx context.Context, root, relPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fsx: delete %q: %w", relPath, err)
	}
	rootAbs, err := absRoot(root)
	if err != nil {
		return err
	}
	abs, err := Resolve(rootAbs, relPath)
	if err != nil {
		return err
	}
	relClean, err := CleanRel(relPath)
	if err != nil {
		return err
	}
	if relClean == "" {
		return fmt.Errorf("fsx: refusing to delete the workspace root")
	}
	if err := guardWrite(rootAbs, abs, relClean); err != nil {
		return err
	}
	fi, serr := os.Lstat(abs)
	if serr != nil {
		if os.IsNotExist(serr) {
			return fmt.Errorf("%w: %q", ErrNotExist, relPath)
		}
		return fmt.Errorf("fsx: stat %q: %w", relPath, serr)
	}
	var size int64
	if !fi.IsDir() {
		size = fi.Size()
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("fsx: delete %q: %w", relPath, err)
	}
	s.audit(AuditEvent{Action: "delete", Path: filepath.ToSlash(relClean), Size: size, At: time.Now().UTC()})
	return nil
}

func (s *Service) audit(ev AuditEvent) {
	if s.cfg.Audit != nil {
		s.cfg.Audit(ev)
	}
}

// ---------------------------------------------------------------------------
// Git status letters
// ---------------------------------------------------------------------------

// gitStates resolves the path → letter map for the tree. It prefers the
// injected GitState hook and falls back to the injected git backend.
func (s *Service) gitStates(ctx context.Context, rootAbs string) map[string]string {
	if s.cfg.GitState != nil {
		m, err := s.cfg.GitState(ctx, rootAbs)
		if err == nil {
			return m
		}
		return nil
	}
	if s.cfg.Git == nil {
		return nil
	}
	st, err := s.cfg.Git.Status(ctx, rootAbs)
	if err != nil {
		return nil // a broken repository must not break browsing
	}
	out := make(map[string]string, len(st.Changes))
	for _, ch := range st.Changes {
		p := strings.TrimSuffix(filepath.ToSlash(ch.Path), "/")
		if p == "" {
			continue
		}
		if letter := GitLetter(ch); letter != "" {
			out[p] = letter
		}
	}
	return out
}

// GitLetter folds index+worktree porcelain letters into the single letter the
// file tree renders (PRD FR-6.2: M A D U !).
func GitLetter(c contracts.GitChange) string {
	if c.Index == "?" || c.Worktree == "?" {
		return "U"
	}
	if c.Index == "U" || c.Worktree == "U" {
		return "!"
	}
	if c.Worktree != "." && c.Worktree != "" {
		return letterOf(c.Worktree)
	}
	if c.Index != "." && c.Index != "" {
		return letterOf(c.Index)
	}
	return ""
}

func letterOf(s string) string {
	switch s {
	case "M", "T":
		return "M"
	case "A":
		return "A"
	case "D":
		return "D"
	case "R", "C":
		return "M"
	case "U":
		return "!"
	}
	return "M"
}

// aggregateGitState folds child states into one letter for a directory.
func aggregateGitState(children []*contracts.FileNode) string {
	rank := map[string]int{"!": 5, "M": 4, "D": 3, "A": 2, "U": 1}
	best, bestRank := "", 0
	for _, c := range children {
		st := c.GitState
		if c.IsDir {
			st = aggregateGitState(c.Children)
		}
		if r, ok := rank[st]; ok && r > bestRank {
			best, bestRank = st, r
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Git operations (delegated to Config.Git so *Service satisfies Workspace)
// ---------------------------------------------------------------------------

// Status delegates to Config.Git.
func (s *Service) Status(ctx context.Context, root string) (*contracts.GitStatus, error) {
	if s.cfg.Git == nil {
		return nil, ErrNoGitBackend
	}
	return s.cfg.Git.Status(ctx, root)
}

// Diff delegates to Config.Git.
func (s *Service) Diff(ctx context.Context, root string, req contracts.DiffRequest) ([]contracts.FileDiff, error) {
	if s.cfg.Git == nil {
		return nil, ErrNoGitBackend
	}
	return s.cfg.Git.Diff(ctx, root, req)
}

// Log delegates to Config.Git.
func (s *Service) Log(ctx context.Context, root string, limit int, cursor string) (*contracts.GitGraph, error) {
	if s.cfg.Git == nil {
		return nil, ErrNoGitBackend
	}
	return s.cfg.Git.Log(ctx, root, limit, cursor)
}

// Stage delegates to Config.Git.
func (s *Service) Stage(ctx context.Context, root string, paths []string) error {
	if s.cfg.Git == nil {
		return ErrNoGitBackend
	}
	return s.cfg.Git.Stage(ctx, root, paths)
}

// Unstage delegates to Config.Git.
func (s *Service) Unstage(ctx context.Context, root string, paths []string) error {
	if s.cfg.Git == nil {
		return ErrNoGitBackend
	}
	return s.cfg.Git.Unstage(ctx, root, paths)
}

// Commit delegates to Config.Git.
func (s *Service) Commit(ctx context.Context, root string, req contracts.CommitRequest) (string, error) {
	if s.cfg.Git == nil {
		return "", ErrNoGitBackend
	}
	return s.cfg.Git.Commit(ctx, root, req)
}

// CreateBranch delegates to Config.Git.
func (s *Service) CreateBranch(ctx context.Context, root, name string) error {
	if s.cfg.Git == nil {
		return ErrNoGitBackend
	}
	return s.cfg.Git.CreateBranch(ctx, root, name)
}

// Checkout delegates to Config.Git.
func (s *Service) Checkout(ctx context.Context, root, name string) error {
	if s.cfg.Git == nil {
		return ErrNoGitBackend
	}
	return s.cfg.Git.Checkout(ctx, root, name)
}

// Adopt carries one checkout's work into another (PRD FR-2.6).
func (s *Service) Adopt(ctx context.Context, sourceRoot, targetRoot, message string) (contracts.AdoptResult, error) {
	if s.cfg.Git == nil {
		return contracts.AdoptResult{}, ErrNoGitBackend
	}
	return s.cfg.Git.Adopt(ctx, sourceRoot, targetRoot, message)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// looksBinary reports whether the sample contains a NUL byte, which is the
// sniff git and most editors use.
func looksBinary(sample []byte) bool {
	return bytes.IndexByte(sample, 0) >= 0
}

// readHeadTail returns roughly head+tail bytes of a large file, cut on line
// boundaries so Shiki never highlights half a token. It reads through ReadAt so
// the caller's file offset does not matter.
func readHeadTail(f *os.File, size, headBytes, tailBytes int64) (string, error) {
	headN := min64(headBytes, size)
	head := make([]byte, headN)
	if _, err := f.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// Trim the head back to the last complete line.
	if i := bytes.LastIndexByte(head, '\n'); i >= 0 && int64(i) < headN-1 {
		head = head[:i+1]
	}

	tailN := min64(tailBytes, size)
	offset := size - tailN
	tail := make([]byte, tailN)
	if _, err := f.ReadAt(tail, offset); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// Start the tail at a line boundary.
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}

	var b strings.Builder
	b.Grow(len(head) + len(tail) + len(truncationMarker))
	b.Write(head)
	b.WriteString(truncationMarker)
	b.Write(tail)
	return b.String(), nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// trimRel normalises a caller path for display (forward slashes, no "./").
func trimRel(p string) string {
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return "."
	}
	return p
}

// TreeWithOptions implements contracts.Workspace: the panel's switch decides
// whether ignored entries are listed at all.
func (s *Service) TreeWithOptions(ctx context.Context, root, relPath string, depth int, includeIgnored bool) ([]*contracts.FileNode, error) {
	page, err := s.TreePage(ctx, root, relPath, depth, TreeOptions{IncludeIgnored: includeIgnored})
	if err != nil {
		return nil, err
	}
	return page.Nodes, nil
}
