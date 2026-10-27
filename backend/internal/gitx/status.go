package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Status returns the branch, ahead/behind counters and the per-path change
// list parsed from `git status --porcelain=v2 --branch`.
//
// It works on a repository with zero commits: branch.oid is then "(initial)"
// and Branch still reports the unborn branch name.
func (c *Client) Status(ctx context.Context, root string) (*contracts.GitStatus, error) {
	dir, err := repoDir(root)
	if err != nil {
		return nil, err
	}
	res, err := c.runner.Run(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal", "--ignore-submodules=dirty")
	if err != nil {
		return nil, fmt.Errorf("gitx: status %s: %w", dir, err)
	}
	st, perr := parsePorcelainV2(res.Stdout)
	if perr != nil {
		return nil, fmt.Errorf("gitx: status %s: %w", dir, perr)
	}
	return st, nil
}

// parsePorcelainV2 turns `--porcelain=v2 --branch` output into a GitStatus.
func parsePorcelainV2(out string) (*contracts.GitStatus, error) {
	st := &contracts.GitStatus{Clean: true}
	var detachedOID string
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			oid := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
			if oid == "(initial)" {
				oid = ""
			}
			detachedOID = oid
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			switch head {
			case "(detached)":
				// Handled once the oid is known (oid is always emitted first).
				st.Branch = shortSHA(detachedOID)
				if st.Branch == "" {
					st.Branch = "(detached)"
				}
			default:
				st.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			// Not part of the contract; ignored on purpose.
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			for _, f := range fields {
				if len(f) < 2 {
					continue
				}
				n, err := strconv.Atoi(f[1:])
				if err != nil {
					continue
				}
				switch f[0] {
				case '+':
					st.Ahead = n
				case '-':
					st.Behind = n
				}
			}
		case strings.HasPrefix(line, "1 "):
			fields := strings.SplitN(line, " ", 9)
			if len(fields) < 9 {
				continue
			}
			addChange(st, unquoteGitPath(fields[8]), fields[1])
		case strings.HasPrefix(line, "2 "):
			// 2 XY sub mH mI mW hH hI Xscore path\torigPath
			fields := strings.SplitN(line, " ", 10)
			if len(fields) < 10 {
				continue
			}
			path := fields[9]
			if i := strings.IndexByte(path, '\t'); i >= 0 {
				path = path[:i]
			}
			addChange(st, unquoteGitPath(path), fields[1])
		case strings.HasPrefix(line, "u "):
			fields := strings.SplitN(line, " ", 11)
			if len(fields) < 11 {
				continue
			}
			path := unquoteGitPath(fields[10])
			addChange(st, path, fields[1])
			st.Conflicts = append(st.Conflicts, path)
		case strings.HasPrefix(line, "? "):
			path := unquoteGitPath(line[2:])
			st.Changes = append(st.Changes, contracts.GitChange{Path: path, Index: "?", Worktree: "?"})
		case strings.HasPrefix(line, "! "):
			// Ignored files: never reported.
		}
	}
	st.Clean = len(st.Changes) == 0 && len(st.Conflicts) == 0
	return st, nil
}

func addChange(st *contracts.GitStatus, path, xy string) {
	index, worktree := ".", "."
	if len(xy) >= 2 {
		index = string(xy[0])
		worktree = string(xy[1])
	}
	if path == "" {
		return
	}
	if index == "?" || worktree == "?" {
		st.Changes = append(st.Changes, contracts.GitChange{Path: path, Index: "?", Worktree: "?"})
		return
	}
	st.Changes = append(st.Changes, contracts.GitChange{Path: path, Index: index, Worktree: worktree})
}

// Head returns the current HEAD commit hash, or "" on an unborn branch.
func (c *Client) Head(ctx context.Context, root string) (string, error) {
	dir, err := repoDir(root)
	if err != nil {
		return "", err
	}
	res, err := c.runner.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		if ExitCode(err) == 1 {
			return "", nil
		}
		return "", fmt.Errorf("gitx: head %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// HasCommits reports whether HEAD exists.
func (c *Client) HasCommits(ctx context.Context, root string) (bool, error) {
	head, err := c.Head(ctx, root)
	return head != "", err
}

// CurrentBranch returns the checked-out branch, or a short hash when detached.
func (c *Client) CurrentBranch(ctx context.Context, root string) (string, error) {
	dir, err := repoDir(root)
	if err != nil {
		return "", err
	}
	res, err := c.runner.Run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitx: branch %s: %w", dir, err)
	}
	name := strings.TrimSpace(res.Stdout)
	if name == "HEAD" {
		head, herr := c.Head(ctx, dir)
		if herr != nil {
			return "", herr
		}
		return shortSHA(head), nil
	}
	return name, nil
}

// TopLevel returns the repository top level that contains root.
func (c *Client) TopLevel(ctx context.Context, root string) (string, error) {
	dir, err := repoDir(root)
	if err != nil {
		return "", err
	}
	res, err := c.runner.Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("gitx: toplevel %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// IsRepo reports whether dir (or one of its parents) is a git repository. It
// runs `git rev-parse --is-inside-work-tree` and never returns an error.
func IsRepo(ctx context.Context, dir string) bool {
	if dir == "" {
		return false
	}
	r := NewRunner(WithTimeout(5e9))
	res, err := r.Run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == "true"
}

// repoDir makes root absolute and verifies it exists. It does not require root
// to be a repository: individual commands surface that error with context.
func repoDir(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("gitx: empty repository path")
	}
	return absClean(root)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// unquoteGitPath strips the C-style quoting git applies to unusual paths.
func unquoteGitPath(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if uq, err := strconv.Unquote(s); err == nil {
			return uq
		}
	}
	return s
}
