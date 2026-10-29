package workspaceremote

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// ---------------------------------------------------------------------------
// File tree
// ---------------------------------------------------------------------------

// Tree lists one directory up to depth levels.
func (w *Workspace) Tree(ctx context.Context, root, relPath string, depth int) ([]*contracts.FileNode, error) {
	return w.TreeWithOptions(ctx, root, relPath, depth, false)
}

// TreeWithOptions is Tree with the panel's "show ignored files" flag.
func (w *Workspace) TreeWithOptions(ctx context.Context, root, relPath string, depth int, includeIgnored bool) ([]*contracts.FileNode, error) {
	relClean, err := fsx.CleanRel(relPath)
	if err != nil {
		return nil, err
	}
	tgt, err := w.target(ctx, root)
	if err != nil {
		return nil, err
	}
	if depth <= 0 {
		depth = 1
	}
	relDir := filepath.ToSlash(relClean)
	states := w.gitStates(ctx, root)
	nodes, _, err := w.listDir(ctx, root, tgt, joinContainer(tgt.Root, relDir), relDir, depth, states, w.cfg.ShowIgnored || includeIgnored)
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// listDir is the recursive listing. It mirrors fsx.readDir: same ordering, same
// truncation signal, same ignore filtering, so the tree cannot look different
// just because it came from a container.
func (w *Workspace) listDir(
	ctx context.Context,
	hostRoot string,
	tgt Target,
	containerDir, relDir string,
	depth int,
	states map[string]string,
	showIgnored bool,
) ([]*contracts.FileNode, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("workspaceremote: list %q: %w", relDir, err)
	}
	entries, err := tgt.Client.List(ctx, containerDir)
	if err != nil {
		if aio.IsNotFound(err) {
			return nil, false, fmt.Errorf("%w: %q", fsx.ErrNotExist, relDir)
		}
		return nil, false, fmt.Errorf("workspaceremote: list %q: %w", relDir, err)
	}

	type candidate struct {
		node aio.Node
		rel  string
	}
	cands := make([]candidate, 0, len(entries))
	for _, n := range entries {
		name := n.Name
		if name == "" {
			continue
		}
		if name == ".git" {
			continue // never expose repository metadata
		}
		if !showIgnored {
			if n.IsDir && name == "node_modules" {
				continue
			}
			if n.IsDir && strings.HasPrefix(name, ".") {
				continue
			}
		}
		rel := name
		if relDir != "" {
			rel = relDir + "/" + name
		}
		cands = append(cands, candidate{node: n, rel: rel})
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].node.IsDir != cands[j].node.IsDir {
			return cands[i].node.IsDir
		}
		return strings.ToLower(cands[i].node.Name) < strings.ToLower(cands[j].node.Name)
	})

	truncated := false
	if len(cands) > w.cfg.MaxEntriesPerDir {
		cands = cands[:w.cfg.MaxEntriesPerDir]
		truncated = true
	}

	if !showIgnored && len(cands) > 0 {
		rels := make([]string, 0, len(cands))
		for _, c := range cands {
			rels = append(rels, c.rel)
		}
		ignored := w.checkIgnore(ctx, hostRoot, rels)
		kept := cands[:0]
		for _, c := range cands {
			if ignored[c.rel] {
				continue
			}
			kept = append(kept, c)
		}
		cands = kept
	}

	nodes := make([]*contracts.FileNode, 0, len(cands))
	for _, c := range cands {
		node := &contracts.FileNode{
			Name:     c.node.Name,
			Path:     c.rel,
			IsDir:    c.node.IsDir,
			Modified: c.node.Modified,
			GitState: states[c.rel],
		}
		if !c.node.IsDir {
			node.Size = c.node.Size
			nodes = append(nodes, node)
			continue
		}
		if depth > 1 {
			children, childTruncated, cerr := w.listDir(ctx, hostRoot, tgt,
				path.Join(containerDir, c.node.Name), c.rel, depth-1, states, showIgnored)
			if cerr != nil {
				node.Truncated = true
			} else {
				if children == nil {
					node.Children = []*contracts.FileNode{}
				} else {
					node.Children = children
				}
				node.Truncated = childTruncated
				if node.GitState == "" {
					node.GitState = aggregateGitState(children)
				}
			}
		} else {
			node.Truncated = true
		}
		nodes = append(nodes, node)
	}
	return nodes, truncated, nil
}

// gitStates asks the container's git for the per-path letters the tree renders.
// A broken repository must not break browsing, so any failure is "no letters".
func (w *Workspace) gitStates(ctx context.Context, hostRoot string) map[string]string {
	if w.cfg.Git == nil {
		return nil
	}
	st, err := w.cfg.Git.Status(ctx, hostRoot)
	if err != nil || st == nil {
		return nil
	}
	out := make(map[string]string, len(st.Changes))
	for _, ch := range st.Changes {
		p := strings.TrimSuffix(filepath.ToSlash(ch.Path), "/")
		if p == "" {
			continue
		}
		if letter := fsx.GitLetter(ch); letter != "" {
			out[p] = letter
		}
	}
	return out
}

// checkIgnore asks the container's git which of rels it ignores. Like fsx's
// batched check it never fails the caller: a repository that is not a repository
// simply hides nothing.
func (w *Workspace) checkIgnore(ctx context.Context, hostRoot string, rels []string) map[string]bool {
	if w.cfg.Git == nil || len(rels) == 0 {
		return nil
	}
	stdin := []byte(strings.Join(rels, "\x00") + "\x00")
	res, err := w.cfg.Git.RunInput(ctx, hostRoot, stdin, "check-ignore", "-z", "--stdin", "--no-index")
	if err != nil {
		// Exit 1 is git's "none of these are ignored", not a failure.
		if gitx.ExitCode(err) == 1 {
			return nil
		}
		return nil
	}
	out := make(map[string]bool)
	for _, entry := range strings.Split(res.Stdout, "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		out[filepath.ToSlash(entry)] = true
	}
	return out
}

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
// File content
// ---------------------------------------------------------------------------

// previewSampleBytes is the window the preview sniffs for binary content.
// fsx.ReadFile uses the same 8 KiB head, so the two workspace implementations
// classify a file the same way.
const previewSampleBytes = 8192

// ReadFile returns a preview payload classified by size (PRD FR-7.8).
func (w *Workspace) ReadFile(ctx context.Context, root, relPath string) (*contracts.FileContent, error) {
	relClean, err := fsx.CleanRel(relPath)
	if err != nil {
		return nil, err
	}
	if relClean == "" {
		return nil, fmt.Errorf("fsx: read %q: %w", relPath, fsx.ErrIsDir)
	}
	tgt, err := w.target(ctx, root)
	if err != nil {
		return nil, err
	}
	container := joinContainer(tgt.Root, filepath.ToSlash(relClean))
	parent, name := path.Split(container)

	meta, err := w.stat(ctx, tgt, path.Clean(parent), name)
	if err != nil {
		return nil, err
	}
	if meta.IsDir {
		return nil, fmt.Errorf("fsx: read %q: %w", relPath, fsx.ErrIsDir)
	}

	content := &contracts.FileContent{
		Path:     filepath.ToSlash(relClean),
		Size:     meta.Size,
		Modified: meta.Modified,
		Language: fsx.GuessLanguage(container),
	}
	switch {
	case meta.Size > w.cfg.MaxPreviewBytes:
		sample, serr := w.readSample(ctx, tgt, container)
		if serr != nil {
			return nil, serr
		}
		// Metadata only: the UI offers a download instead (FR-7.8).
		content.Binary = fsx.LooksBinary(sample)
		content.Truncated = true
		content.Language = ""
		content.MimeType = fsx.GuessMime(container, content.Binary)
		return content, nil

	case meta.Size > w.cfg.MaxInlineBytes:
		head, tail, serr := w.readHeadTail(ctx, tgt, container)
		if serr != nil {
			return nil, serr
		}
		head, tail = trimHeadTail(head, tail)
		content.Binary = fsx.LooksBinary(head)
		if content.Binary {
			content.Truncated = true
		} else {
			content.Body = string(head) + fsx.TruncationMarker + string(tail)
			content.Truncated = true
		}
		content.MimeType = fsx.GuessMime(container, content.Binary)
		return content, nil

	default:
		// Sniff the first bytes before asking /v1/file/read for text: that
		// endpoint decodes as UTF-8 and fails a PNG at byte 0. fsx classifies
		// the same window with LooksBinary, so a local and a remote preview
		// now agree on what "binary" means. A failed probe is not a preview
		// failure — fall through to the text read, which reports the real
		// error if there is one.
		if head, herr := tgt.Client.ReadPrefix(ctx, container, previewSampleBytes); herr == nil {
			if fsx.LooksBinary(head) {
				content.Binary = true
				content.MimeType = fsx.GuessMime(container, true)
				return content, nil
			}
		}
		file, rerr := tgt.Client.ReadFile(ctx, container)
		if rerr != nil {
			// A file the probe could not classify (no NUL in its first 8 KiB)
			// can still be undecodable, e.g. a JPEG. The image types exactly
			// that case as error_type "decode_error"; anything else is a real
			// failure and stays one.
			if aio.IsDecodeError(rerr) {
				content.Binary = true
				content.MimeType = fsx.GuessMime(container, true)
				return content, nil
			}
			if aio.IsNotFound(rerr) {
				return nil, fmt.Errorf("%w: %q", fsx.ErrNotExist, relPath)
			}
			return nil, fmt.Errorf("workspaceremote: read %q: %w", relPath, rerr)
		}
		content.Body = file.Content
		content.Binary = fsx.LooksBinary([]byte(file.Content))
		if content.Binary {
			content.Body = ""
		}
		content.MimeType = fsx.GuessMime(container, content.Binary)
		return content, nil
	}
}

// OpenFile opens a file for a raw byte stream through the sandbox's download
// endpoint. ReadFile is the wrong tool for a binary file: it reads through
// /v1/file/read, whose UTF-8 codec rejects a PNG at the first byte. The live
// image's download endpoint (probed on 1.11.0) returns the bytes with a
// Content-Length and supports Range, so no exec/base64 fallback is needed and
// none is used: a base64 round-trip would buffer the whole file in memory.
func (w *Workspace) OpenFile(ctx context.Context, root, relPath string) (io.ReadCloser, int64, error) {
	relClean, err := fsx.CleanRel(relPath)
	if err != nil {
		return nil, -1, err
	}
	if relClean == "" {
		return nil, -1, fmt.Errorf("fsx: read %q: %w", relPath, fsx.ErrIsDir)
	}
	tgt, err := w.target(ctx, root)
	if err != nil {
		return nil, -1, err
	}
	container := joinContainer(tgt.Root, filepath.ToSlash(relClean))
	body, size, err := tgt.Client.OpenFile(ctx, container)
	if err != nil {
		if aio.IsNotFound(err) {
			return nil, -1, fmt.Errorf("%w: %q", fsx.ErrNotExist, relPath)
		}
		// A directory is a 400 from the download endpoint ("Path is not a
		// file"). Its metadata is the cheap way to keep that a client error
		// instead of letting the generic 500 path swallow it.
		var httpErr *aio.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusBadRequest {
			if meta, serr := w.stat(ctx, tgt, path.Dir(container), path.Base(container)); serr == nil && meta.IsDir {
				return nil, -1, fmt.Errorf("fsx: read %q: %w", relPath, fsx.ErrIsDir)
			}
		}
		return nil, -1, fmt.Errorf("workspaceremote: open %q: %w", relPath, err)
	}
	return body, size, nil
}

// stat reads one path's metadata with an exact-name glob. The file API reports
// size and mtime only in a listing, and globbing the parent for the name is the
// cheapest listing that cannot be confused by a sibling with the same prefix.
func (w *Workspace) stat(ctx context.Context, tgt Target, parent, name string) (aio.Node, error) {
	nodes, err := tgt.Client.Glob(ctx, parent, name, aio.GlobOptions{
		IncludeHidden: true,
		FilesOnly:     false,
		MaxResults:    10,
	})
	if err != nil {
		return aio.Node{}, fmt.Errorf("workspaceremote: stat %q: %w", path.Join(parent, name), err)
	}
	for _, n := range nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return aio.Node{}, fmt.Errorf("%w: %q", fsx.ErrNotExist, strings.TrimPrefix(path.Join(parent, name), "/"))
}

// readSample returns the first 8 KiB exactly, base64-decoded, for the binary sniff.
func (w *Workspace) readSample(ctx context.Context, tgt Target, container string) ([]byte, error) {
	res, err := tgt.Client.Exec(ctx, "head -c 8192 -- "+aio.Quote(container)+" | base64 | tr -d '\\n'", tgt.Root)
	if err != nil {
		return nil, fmt.Errorf("workspaceremote: sample %q: %w", container, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("workspaceremote: sample %q: exit %d: %s", container, res.ExitCode, strings.TrimSpace(res.Output))
	}
	raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(res.Output))
	if derr != nil {
		return nil, fmt.Errorf("workspaceremote: sample %q: decode: %w", container, derr)
	}
	return raw, nil
}

// readHeadTail returns the head and the tail of a large file: two exact byte
// reads in one shell call.
func (w *Workspace) readHeadTail(ctx context.Context, tgt Target, container string) ([]byte, []byte, error) {
	q := aio.Quote(container)
	script := strings.Join([]string{
		`d=$(mktemp -d) || exit 97`,
		`head -c ` + fmt.Sprint(w.cfg.HeadBytes) + ` -- ` + q + ` > "$d/h"`,
		`tail -c ` + fmt.Sprint(w.cfg.TailBytes) + ` -- ` + q + ` > "$d/t"`,
		`printf 'PFHEAD:'; base64 < "$d/h" | tr -d '\n'; printf '\n'`,
		`printf 'PFTAIL:'; base64 < "$d/t" | tr -d '\n'; printf '\n'`,
		`rm -rf "$d"`,
	}, "; ")
	res, err := tgt.Client.Exec(ctx, script, tgt.Root)
	if err != nil {
		return nil, nil, fmt.Errorf("workspaceremote: preview %q: %w", container, err)
	}
	if res.ExitCode != 0 {
		return nil, nil, fmt.Errorf("workspaceremote: preview %q: exit %d: %s", container, res.ExitCode, strings.TrimSpace(res.Output))
	}
	var headB64, tailB64 string
	for _, line := range strings.Split(res.Output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PFHEAD:"):
			headB64 = strings.TrimPrefix(line, "PFHEAD:")
		case strings.HasPrefix(line, "PFTAIL:"):
			tailB64 = strings.TrimPrefix(line, "PFTAIL:")
		}
	}
	head, err := base64.StdEncoding.DecodeString(headB64)
	if err != nil {
		return nil, nil, fmt.Errorf("workspaceremote: preview %q: decode head: %w", container, err)
	}
	tail, err := base64.StdEncoding.DecodeString(tailB64)
	if err != nil {
		return nil, nil, fmt.Errorf("workspaceremote: preview %q: decode tail: %w", container, err)
	}
	return head, tail, nil
}

// trimHeadTail cuts the head back to the last complete line and the tail
// forward to the first one, so a preview never ends in half a character.
func trimHeadTail(head, tail []byte) ([]byte, []byte) {
	if i := strings.LastIndexByte(string(head), '\n'); i >= 0 && i < len(head)-1 {
		head = head[:i+1]
	}
	if i := strings.IndexByte(string(tail), '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return head, tail
}

// WriteFile creates or replaces a file. The sandbox creates parent directories.
func (w *Workspace) WriteFile(ctx context.Context, root, relPath string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fsx: write %q: %w", relPath, err)
	}
	relClean, err := fsx.CleanRel(relPath)
	if err != nil {
		return err
	}
	if err := guardWrite(relClean); err != nil {
		return err
	}
	tgt, err := w.target(ctx, root)
	if err != nil {
		return err
	}
	container := joinContainer(tgt.Root, filepath.ToSlash(relClean))
	if m, serr := w.stat(ctx, tgt, path.Dir(container), path.Base(container)); serr == nil && m.IsDir {
		return fmt.Errorf("fsx: write %q: %w", relPath, fsx.ErrIsDir)
	}
	if werr := tgt.Client.WriteBytes(ctx, container, body); werr != nil {
		return fmt.Errorf("workspaceremote: write %q: %w", relPath, werr)
	}
	w.audit(fsx.AuditEvent{
		Action: "write", Path: filepath.ToSlash(relClean),
		Size: int64(len(body)), At: time.Now().UTC(),
	})
	return nil
}

// DeleteFile removes a file, or a directory tree.
func (w *Workspace) DeleteFile(ctx context.Context, root, relPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fsx: delete %q: %w", relPath, err)
	}
	relClean, err := fsx.CleanRel(relPath)
	if err != nil {
		return err
	}
	if err := guardWrite(relClean); err != nil {
		return err
	}
	tgt, err := w.target(ctx, root)
	if err != nil {
		return err
	}
	container := joinContainer(tgt.Root, filepath.ToSlash(relClean))
	var size int64
	if m, serr := w.stat(ctx, tgt, path.Dir(container), path.Base(container)); serr == nil && !m.IsDir {
		size = m.Size
	}
	if derr := tgt.Client.Delete(ctx, container, tgt.Root); derr != nil {
		if aio.IsNotFound(derr) {
			return fmt.Errorf("%w: %q", fsx.ErrNotExist, relPath)
		}
		return fmt.Errorf("workspaceremote: delete %q: %w", relPath, derr)
	}
	w.audit(fsx.AuditEvent{
		Action: "delete", Path: filepath.ToSlash(relClean),
		Size: size, At: time.Now().UTC(),
	})
	return nil
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Search finds files by name (byContent=false) or by content (byContent=true).
//
// Content search is the sandbox's ripgrep, which honours .gitignore exactly like
// the host one. Name search globs and then applies the same subsequence matcher
// the local implementation uses, so "srcrd" still finds "src/readme.md".
func (w *Workspace) Search(ctx context.Context, root, query string, byContent bool) ([]contracts.SearchResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("fsx: search: empty query")
	}
	tgt, err := w.target(ctx, root)
	if err != nil {
		return nil, err
	}
	if byContent {
		return w.searchContent(ctx, tgt, q)
	}
	return w.searchNames(ctx, tgt, q)
}

func (w *Workspace) searchContent(ctx context.Context, tgt Target, q string) ([]contracts.SearchResult, error) {
	// Regex first (what users expect), literal fallback when the pattern does
	// not compile — the same order fsx uses.
	result, err := tgt.Client.Grep(ctx, tgt.Root, q, false, w.cfg.SearchLimit)
	if err != nil {
		result, err = tgt.Client.Grep(ctx, tgt.Root, q, true, w.cfg.SearchLimit)
		if err != nil {
			return nil, fmt.Errorf("workspaceremote: search %q: %w", q, err)
		}
	}
	results := make([]contracts.SearchResult, 0, len(result.Matches))
	for _, m := range result.Matches {
		results = append(results, contracts.SearchResult{
			Path: relFrom(tgt.Root, m.File),
			Line: m.Line,
			Text: m.Text,
		})
		if len(results) >= w.cfg.SearchLimit {
			break
		}
	}
	return results, nil
}

func (w *Workspace) searchNames(ctx context.Context, tgt Target, q string) ([]contracts.SearchResult, error) {
	// List and then match, in that order: the panel's name search accepts an
	// ordered subsequence ("srcrd" finds "src/readme.md"), which no glob can
	// express. `rg --files` does the same on the host side, so this is the
	// equivalent, not a shortcut.
	nodes, err := tgt.Client.Glob(ctx, tgt.Root, "**/*", aio.GlobOptions{
		IncludeHidden: true,
		FilesOnly:     true,
	})
	if err != nil {
		return nil, fmt.Errorf("workspaceremote: search %q: %w", q, err)
	}
	results := make([]contracts.SearchResult, 0, w.cfg.SearchLimit)
	for _, n := range nodes {
		rel := relFrom(tgt.Root, n.Path)
		if rel == "" || rel == ".git" || strings.HasPrefix(rel, ".git/") ||
			rel == "node_modules" || strings.Contains(rel, "/node_modules/") {
			continue
		}
		if !fsx.MatchName(rel, q) {
			continue
		}
		results = append(results, contracts.SearchResult{Path: rel})
		if len(results) >= w.cfg.SearchLimit {
			break
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// joinContainer joins a workspace-relative slash path onto the container root.
func joinContainer(root, rel string) string {
	if rel == "" || rel == "." {
		return path.Clean(root)
	}
	return path.Join(root, rel)
}

// relFrom turns a path the sandbox reported back into a workspace-relative one.
// The API has answered both absolute and already-relative paths across versions.
func relFrom(root, reported string) string {
	r := strings.TrimSuffix(filepath.ToSlash(root), "/")
	p := filepath.ToSlash(reported)
	if p == r {
		return ""
	}
	if strings.HasPrefix(p, r+"/") {
		return strings.TrimPrefix(p, r+"/")
	}
	return strings.TrimPrefix(p, "./")
}

// guardWrite rejects mutations of the repository metadata or of the root, the
// same two refusals fsx.guardWrite makes.
func guardWrite(rel string) error {
	relSlash := filepath.ToSlash(rel)
	if relSlash == "" || relSlash == "." {
		return fmt.Errorf("fsx: refusing to modify the workspace root itself")
	}
	if relSlash == ".git" || strings.HasPrefix(relSlash, ".git/") {
		return fmt.Errorf("fsx: refusing to modify git metadata (%q)", relSlash)
	}
	return nil
}
