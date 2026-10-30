package gitx

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Diff returns structured patches for the requested scope.
//
// Scopes:
//
//	unstaged → `git diff`               (worktree vs index)
//	staged   → `git diff --cached`      (index vs HEAD)
//	commit   → `git show <sha> --format= --patch`
//	branch   → `git diff <base>...HEAD` (merge-base semantics)
//	range    → `git diff <from>..<to>`  (two explicit revisions)
func (r *Runner) Diff(ctx context.Context, root string, req contracts.DiffRequest) ([]contracts.FileDiff, error) {
	dir, err := repoPath(root)
	if err != nil {
		return nil, err
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", "-U3", "--submodule=short"}
	var relPath string

	switch req.Scope {
	case "", contracts.ScopeUnstaged:
		// plain `git diff`
	case contracts.ScopeStaged:
		args = append(args, "--cached")
	case contracts.ScopeCommit:
		if err := safeRevArg(req.Commit); err != nil {
			return nil, fmt.Errorf("gitx: diff commit: %w", err)
		}
		args = []string{"show", "--no-color", "--no-ext-diff", "--find-renames", "-U3",
			"--format=", "--patch", "--submodule=short", req.Commit}
	case contracts.ScopeBranch:
		base := strings.TrimSpace(req.Base)
		if base == "" {
			base, err = r.defaultBase(ctx, dir)
			if err != nil {
				return nil, err
			}
		}
		if err := safeRevArg(base); err != nil {
			return nil, fmt.Errorf("gitx: diff branch: %w", err)
		}
		args = append(args, base+"...HEAD")
	case contracts.ScopeRange:
		from, to := strings.TrimSpace(req.From), strings.TrimSpace(req.To)
		if from == "" {
			return nil, fmt.Errorf("gitx: diff range: from is required")
		}
		if to == "" {
			to = "HEAD"
		}
		// Each end is checked on its own: safeRevArg refuses `..`, so a range can
		// never be smuggled in as one revision string.
		if err := safeRevArg(from); err != nil {
			return nil, fmt.Errorf("gitx: diff range from: %w", err)
		}
		if err := safeRevArg(to); err != nil {
			return nil, fmt.Errorf("gitx: diff range to: %w", err)
		}
		args = append(args, from+".."+to)
	default:
		return nil, fmt.Errorf("gitx: diff: unknown scope %q", req.Scope)
	}

	if path := strings.TrimSpace(req.Path); path != "" {
		rel, perr := CleanRel(path)
		if perr != nil {
			return nil, perr
		}
		relPath = rel
		args = append(args, "--", rel)
	}

	res, err := r.Run(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("gitx: diff %s: %w", dir, err)
	}
	files := ParsePatch(res.Stdout)
	// `git diff` says nothing about a path the index has never seen, so a brand
	// new file reviewed before staging reported "no difference" — the one file a
	// reviewer most wants to read. `--no-index` against the null device prints
	// the same patch git would print once the file were added.
	if len(files) == 0 && relPath != "" && (req.Scope == "" || req.Scope == contracts.ScopeUnstaged) {
		files = r.untrackedDiff(ctx, dir, relPath)
	}
	if files == nil {
		files = []contracts.FileDiff{}
	}
	return files, nil
}

// untrackedDiff renders a never-added file as an addition.
//
// Only a path git reports as untracked is eligible: a tracked file with no diff
// is genuinely unchanged, and `--no-index` would happily invent a patch for it.
// The file must also be a regular file — status collapses an untracked
// directory into a single "dir/" row, and diffing a directory against the null
// device fails with "Could not access".
func (r *Runner) untrackedDiff(ctx context.Context, dir, rel string) []contracts.FileDiff {
	if rel == "" {
		return nil
	}
	info, err := os.Stat(filepath.Join(dir, rel))
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	listed, err := r.Run(ctx, dir, "ls-files", "--others", "--exclude-standard", "--", rel)
	if err != nil || strings.TrimSpace(listed.Stdout) == "" {
		return nil
	}
	// `--no-index` exits 1 whenever the two sides differ, which is the expected
	// outcome here; only any other failure means the diff could not be produced.
	res, err := r.Run(ctx, dir,
		"diff", "--no-color", "--no-ext-diff", "--no-index", "-U3",
		"--", os.DevNull, rel)
	if err != nil && ExitCode(err) != 1 {
		return nil
	}
	files := ParsePatch(res.Stdout)
	for i := range files {
		// A file that is not in the index can only be an addition, whatever the
		// patch header happened to call it.
		files[i].Status = "added"
		files[i].OldPath = ""
		// `--no-index` is given OS paths and echoes them back with the local
		// separator; every other path in this package is slash-separated.
		files[i].Path = filepath.ToSlash(files[i].Path)
	}
	return files
}

// DiffPatch returns the raw unified diff for the same scopes as Diff.
func (r *Runner) DiffPatch(ctx context.Context, root string, req contracts.DiffRequest) (string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return "", err
	}
	var args []string
	switch req.Scope {
	case "", contracts.ScopeUnstaged:
		args = []string{"diff", "--no-color", "--no-ext-diff", "--find-renames"}
	case contracts.ScopeStaged:
		args = []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", "--cached"}
	case contracts.ScopeCommit:
		if err := safeRevArg(req.Commit); err != nil {
			return "", fmt.Errorf("gitx: diff commit: %w", err)
		}
		args = []string{"show", "--no-color", "--no-ext-diff", "--find-renames", "--format=", "--patch", req.Commit}
	case contracts.ScopeBranch:
		base := strings.TrimSpace(req.Base)
		if base == "" {
			base, err = r.defaultBase(ctx, dir)
			if err != nil {
				return "", err
			}
		}
		if err := safeRevArg(base); err != nil {
			return "", fmt.Errorf("gitx: diff branch: %w", err)
		}
		args = []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", base + "...HEAD"}
	case contracts.ScopeRange:
		from, to := strings.TrimSpace(req.From), strings.TrimSpace(req.To)
		if from == "" {
			return "", fmt.Errorf("gitx: diff range: from is required")
		}
		if to == "" {
			to = "HEAD"
		}
		if err := safeRevArg(from); err != nil {
			return "", fmt.Errorf("gitx: diff range from: %w", err)
		}
		if err := safeRevArg(to); err != nil {
			return "", fmt.Errorf("gitx: diff range to: %w", err)
		}
		args = []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", from + ".." + to}
	default:
		return "", fmt.Errorf("gitx: diff: unknown scope %q", req.Scope)
	}
	if path := strings.TrimSpace(req.Path); path != "" {
		rel, perr := CleanRel(path)
		if perr != nil {
			return "", perr
		}
		args = append(args, "--", rel)
	}
	return r.runStdout(ctx, dir, args...)
}

// defaultBase picks the branch to compare against for ScopeBranch.
func (r *Runner) defaultBase(ctx context.Context, dir string) (string, error) {
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
		res, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err == nil && strings.TrimSpace(res.Stdout) != "" {
			return ref, nil
		}
	}
	// Fall back to the first parent of HEAD so the call still returns a diff.
	res, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^")
	if err == nil && strings.TrimSpace(res.Stdout) != "" {
		return "HEAD^", nil
	}
	return "HEAD", nil
}

// ParsePatch splits a unified diff into one contracts.FileDiff per file and
// counts additions/deletions from the hunk bodies.
func ParsePatch(patch string) []contracts.FileDiff {
	var (
		files   []contracts.FileDiff
		cur     *contracts.FileDiff
		body    strings.Builder
		inHunk  bool
		added   int
		removed int
	)

	flush := func() {
		if cur == nil {
			return
		}
		cur.Additions = added
		cur.Deletions = removed
		cur.Patch = body.String()
		if cur.Status == "" {
			cur.Status = "modified"
		}
		files = append(files, *cur)
		cur = nil
		body.Reset()
		added, removed = 0, 0
		inHunk = false
	}

	sc := bufio.NewScanner(strings.NewReader(patch))
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			a, b := splitDiffGitHeader(line)
			cur = &contracts.FileDiff{Path: b, OldPath: a, Status: "modified"}
			body.WriteString(line)
			body.WriteByte('\n')
			continue
		case cur == nil:
			continue
		}

		body.WriteString(line)
		body.WriteByte('\n')

		switch {
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
			cur.OldPath = ""
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(line, "rename from "):
			cur.OldPath = unquoteGitPath(strings.TrimPrefix(line, "rename from "))
			cur.Status = "renamed"
		case strings.HasPrefix(line, "rename to "):
			cur.Path = unquoteGitPath(strings.TrimPrefix(line, "rename to "))
			cur.Status = "renamed"
		case strings.HasPrefix(line, "copy from "):
			cur.OldPath = unquoteGitPath(strings.TrimPrefix(line, "copy from "))
			cur.Status = "added"
		case strings.HasPrefix(line, "copy to "):
			cur.Path = unquoteGitPath(strings.TrimPrefix(line, "copy to "))
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "--- "):
			p := unquoteGitPath(strings.TrimPrefix(line, "--- "))
			if p != "/dev/null" {
				cur.OldPath = stripDiffPrefix(p)
			} else if cur.Status != "added" {
				cur.Status = "deleted"
			}
		case strings.HasPrefix(line, "+++ "):
			p := unquoteGitPath(strings.TrimPrefix(line, "+++ "))
			if p != "/dev/null" {
				cur.Path = stripDiffPrefix(p)
			}
			if cur.Status == "deleted" {
				cur.Path = cur.OldPath
			}
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			added++
		case inHunk && strings.HasPrefix(line, "-"):
			removed++
		}
	}
	flush()
	return files
}

// splitDiffGitHeader extracts the a/ and b/ paths from "diff --git a/x b/y".
// It is only a fallback: the ---/+++/rename lines are authoritative.
func splitDiffGitHeader(line string) (string, string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	// Prefer the last " b/" separator so paths with spaces survive.
	if i := strings.LastIndex(rest, " b/"); i > 0 && strings.HasPrefix(rest, "a/") {
		return unquoteGitPath(rest[2:i]), unquoteGitPath(rest[i+3:])
	}
	fields := strings.SplitN(rest, " ", 2)
	if len(fields) != 2 {
		return "", stripDiffPrefix(rest)
	}
	return stripDiffPrefix(fields[0]), stripDiffPrefix(fields[1])
}

func stripDiffPrefix(p string) string {
	if len(p) > 2 && (p[0] == 'a' || p[0] == 'b') && p[1] == '/' {
		return p[2:]
	}
	return p
}
