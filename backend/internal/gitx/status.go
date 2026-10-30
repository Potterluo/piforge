package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Status returns the branch, ahead/behind counters and the per-path change
// list parsed from `git status --porcelain=v2 --branch`.
//
// It works on a repository with zero commits: branch.oid is then "(initial)"
// and Branch still reports the unborn branch name.
func (r *Runner) Status(ctx context.Context, root string) (*contracts.GitStatus, error) {
	dir, err := repoPath(root)
	if err != nil {
		return nil, err
	}
	// `all`, not `normal`: the change list is per file (it draws one row per
	// path, with its own counts and risk marker), so an untracked directory must
	// not collapse its contents into a single `.github/` row.
	res, err := r.Run(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=dirty")
	if err != nil {
		return nil, fmt.Errorf("gitx: status %s: %w", dir, err)
	}
	st, perr := parsePorcelainV2(res.Stdout)
	if perr != nil {
		return nil, fmt.Errorf("gitx: status %s: %w", dir, perr)
	}
	if err := r.annotateChanges(ctx, dir, st); err != nil {
		return nil, fmt.Errorf("gitx: status %s: %w", dir, err)
	}
	return st, nil
}

// annotateChanges adds the per-path line counts and the risk marker to a parsed
// status. It is part of Status, not a second endpoint: the 变更 panel draws the
// change list and the counts together, and two snapshots taken at different
// instants would let the totals disagree with the rows.
func (r *Runner) annotateChanges(ctx context.Context, dir string, st *contracts.GitStatus) error {
	counts := map[string][2]int{}

	// One diff against HEAD covers staged and unstaged work. An unborn branch
	// has no HEAD to diff against, so the index and the worktree are counted
	// separately.
	hasHead, err := r.HasCommits(ctx, dir)
	if err != nil {
		return err
	}
	commands := [][]string{
		{"diff", "--numstat", "-z", "--"},
		{"diff", "--cached", "--numstat", "-z", "--"},
	}
	if hasHead {
		commands = [][]string{{"diff", "--numstat", "-z", "HEAD", "--"}}
	}
	for _, args := range commands {
		res, err := r.Run(ctx, dir, args...)
		if err != nil {
			return err
		}
		mergeNumstat(counts, res.Stdout)
	}

	for i := range st.Changes {
		change := &st.Changes[i]
		change.Risk = riskOf(change.Path)
		if isUntracked(*change) {
			additions, err := countLines(filepath.Join(dir, filepath.FromSlash(change.Path)))
			if err == nil {
				counts[change.Path] = [2]int{additions, 0}
			}
		}
		if n, ok := counts[change.Path]; ok {
			change.Additions, change.Deletions = n[0], n[1]
		}
		st.Additions += change.Additions
		st.Deletions += change.Deletions
	}
	return nil
}

func isUntracked(change contracts.GitChange) bool {
	return change.Index == "?" || change.Worktree == "?"
}

// mergeNumstat folds `git diff --numstat -z` output into counts.
//
// With -z the fields are NUL-terminated and a rename is followed by the old and
// new paths as separate records, so a path containing a tab or a non-ASCII
// character cannot be mis-split.
func mergeNumstat(counts map[string][2]int, out string) {
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		path := fields[2]
		if path == "" {
			// Rename: old path then new path in the following records.
			if i+2 >= len(records) {
				continue
			}
			path = records[i+2]
			i += 2
		}
		additions, deletions := 0, 0
		if fields[0] != "-" {
			additions, _ = strconv.Atoi(fields[0])
		}
		if fields[1] != "-" {
			deletions, _ = strconv.Atoi(fields[1])
		}
		current := counts[path]
		counts[path] = [2]int{current[0] + additions, current[1] + deletions}
	}
}

// countLines counts the lines of an untracked file. A binary file has no lines
// to count, and a file too large to be worth scanning is reported as zero rather
// than blocking the status call.
func countLines(file string) (int, error) {
	const maxScannedFile = 4 << 20
	info, err := os.Stat(file)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxScannedFile {
		return 0, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return 0, nil
	}
	lines := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	return lines, nil
}

// riskOf classifies a path the reviewer should look at twice. Both classes are
// spelled here rather than in the UI so every client agrees.
func riskOf(p string) string {
	clean := strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	base := path.Base(clean)
	if strings.HasPrefix(clean, ".github/workflows/") ||
		strings.HasPrefix(clean, ".circleci/") ||
		strings.HasPrefix(clean, ".buildkite/") {
		return contracts.RiskCI
	}
	switch base {
	case ".gitlab-ci.yml", "jenkinsfile", "azure-pipelines.yml",
		"bitbucket-pipelines.yml", "appveyor.yml", ".travis.yml":
		return contracts.RiskCI
	case "go.sum", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml",
		"yarn.lock", "bun.lockb", "cargo.lock", "poetry.lock", "pipfile.lock",
		"composer.lock", "gemfile.lock", "uv.lock", "gradle.lockfile",
		"packages.lock.json":
		return contracts.RiskLockfile
	}
	return ""
}

// parsePorcelainV2 turns `--porcelain=v2 --branch` output into a GitStatus.
//
// The change list is always allocated: `nil` marshals to JSON `null`, and a
// clean tree is the most common state, so returning null there would break every
// client that reads `changes.length`.
func parsePorcelainV2(out string) (*contracts.GitStatus, error) {
	st := &contracts.GitStatus{
		Clean:     true,
		Changes:   []contracts.GitChange{},
		Conflicts: []string{},
	}
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
func (r *Runner) Head(ctx context.Context, root string) (string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return "", err
	}
	res, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		if ExitCode(err) == 1 {
			return "", nil
		}
		return "", fmt.Errorf("gitx: head %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// HasCommits reports whether HEAD exists.
func (r *Runner) HasCommits(ctx context.Context, root string) (bool, error) {
	head, err := r.Head(ctx, root)
	return head != "", err
}

// CurrentBranch returns the checked-out branch, or a short hash when detached.
func (r *Runner) CurrentBranch(ctx context.Context, root string) (string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return "", err
	}
	// `symbolic-ref` answers on an unborn branch too. `rev-parse --abbrev-ref HEAD` fails
	// there with "unknown revision", which made an empty repository look like a broken
	// one — a project created from a remote with no commits is the ordinary new-repo case.
	if sym, serr := r.Run(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); serr == nil {
		if name := strings.TrimSpace(sym.Stdout); name != "" {
			return name, nil
		}
	}
	res, err := r.Run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitx: branch %s: %w", dir, err)
	}
	name := strings.TrimSpace(res.Stdout)
	if name == "HEAD" {
		head, herr := r.Head(ctx, dir)
		if herr != nil {
			return "", herr
		}
		return shortSHA(head), nil
	}
	return name, nil
}

// TopLevel returns the repository top level that contains root.
func (r *Runner) TopLevel(ctx context.Context, root string) (string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return "", err
	}
	res, err := r.Run(ctx, dir, "rev-parse", "--show-toplevel")
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
func repoPath(root string) (string, error) {
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
