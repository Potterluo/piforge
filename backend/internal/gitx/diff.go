package gitx

import (
	"bufio"
	"context"
	"fmt"
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
func (c *Client) Diff(ctx context.Context, root string, req contracts.DiffRequest) ([]contracts.FileDiff, error) {
	dir, err := repoDir(root)
	if err != nil {
		return nil, err
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", "-U3", "--submodule=short"}

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
			base, err = c.defaultBase(ctx, dir)
			if err != nil {
				return nil, err
			}
		}
		if err := safeRevArg(base); err != nil {
			return nil, fmt.Errorf("gitx: diff branch: %w", err)
		}
		args = append(args, base+"...HEAD")
	default:
		return nil, fmt.Errorf("gitx: diff: unknown scope %q", req.Scope)
	}

	if path := strings.TrimSpace(req.Path); path != "" {
		rel, perr := CleanRel(path)
		if perr != nil {
			return nil, perr
		}
		args = append(args, "--", rel)
	}

	res, err := c.runner.Run(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("gitx: diff %s: %w", dir, err)
	}
	files := ParsePatch(res.Stdout)
	if files == nil {
		files = []contracts.FileDiff{}
	}
	return files, nil
}

// DiffPatch returns the raw unified diff for the same scopes as Diff.
func (c *Client) DiffPatch(ctx context.Context, root string, req contracts.DiffRequest) (string, error) {
	dir, err := repoDir(root)
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
			base, err = c.defaultBase(ctx, dir)
			if err != nil {
				return "", err
			}
		}
		if err := safeRevArg(base); err != nil {
			return "", fmt.Errorf("gitx: diff branch: %w", err)
		}
		args = []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", base + "...HEAD"}
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
	return c.runStdout(ctx, dir, args...)
}

// defaultBase picks the branch to compare against for ScopeBranch.
func (c *Client) defaultBase(ctx context.Context, dir string) (string, error) {
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
		res, err := c.runner.Run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err == nil && strings.TrimSpace(res.Stdout) != "" {
			return ref, nil
		}
	}
	// Fall back to the first parent of HEAD so the call still returns a diff.
	res, err := c.runner.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^")
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
