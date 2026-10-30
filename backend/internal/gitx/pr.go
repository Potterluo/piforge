package gitx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Pull-request range (PRD FR-12.6).
//
// "Open a PR" needs to say what it would send before it sends it. That is one
// question — what is on this branch and not on its base — and it is answered by
// git, not by the UI: the commit list is `base..HEAD`, the files are a
// three-dot diff (merge-base semantics), and both are computed on the machine
// that owns the checkout. A preview built from the branch diff *view* would be
// a different question (it compares tips, not histories) and would include a
// reverse change for every commit the base gained after the branch was cut.

// ErrBaseMissing means the target branch does not exist in this checkout.
// A PR needs a real base; guessing one would compare against the wrong history.
var ErrBaseMissing = errors.New("gitx: the pull request base branch does not exist")

// maxPRCommits caps the commit list a preview carries. The count stays exact,
// so a hundred-commit branch is described by "128 commits" plus the newest 50
// subjects rather than by a truncated count.
const maxPRCommits = 50

// PRRange returns the commits and files between base and HEAD.
func (r *Runner) PRRange(ctx context.Context, root, base string) (contracts.PRRange, error) {
	dir, err := repoPath(root)
	if err != nil {
		return contracts.PRRange{}, err
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = "HEAD"
	}
	if err := safeRevArg(base); err != nil {
		return contracts.PRRange{}, fmt.Errorf("gitx: pull request base: %w", err)
	}
	// A PR targets the branch *on the remote*. In an ordinary clone only the
	// checked-out branch is local, so `main` usually exists as `origin/main`
	// and nowhere else; preferring the remote-tracking ref also avoids
	// describing a stale local base as the review target.
	resolved, rerr := r.resolveBase(ctx, dir, base)
	if rerr != nil {
		return contracts.PRRange{}, fmt.Errorf("%w: %s", ErrBaseMissing, base)
	}

	headSHA, _ := r.Head(ctx, dir)
	out := contracts.PRRange{Base: base, HeadSHA: headSHA,
		Commits: []contracts.GitCommit{}, Files: []contracts.PRFile{}}

	if branch, berr := r.CurrentBranch(ctx, dir); berr == nil {
		out.Head = branch
	}

	// Exact count first: the display list is capped, the number is not.
	if res, cerr := r.Run(ctx, dir, "rev-list", "--count", resolved+"..HEAD"); cerr == nil {
		out.CommitCount, _ = strconv.Atoi(strings.TrimSpace(res.Stdout))
	} else {
		return contracts.PRRange{}, fmt.Errorf("gitx: count commits %s..HEAD: %w", resolved, cerr)
	}

	if res, lerr := r.Run(ctx, dir, "log", "--no-color", "--no-decorate",
		"--pretty=format:"+logFormat, "--max-count="+strconv.Itoa(maxPRCommits),
		resolved+"..HEAD"); lerr == nil {
		out.Commits = parseLog(res.Stdout)
	} else {
		return contracts.PRRange{}, fmt.Errorf("gitx: log %s..HEAD: %w", resolved, lerr)
	}

	files, additions, deletions, ferr := r.prFiles(ctx, dir, resolved)
	if ferr != nil {
		return contracts.PRRange{}, ferr
	}
	out.Files, out.Additions, out.Deletions = files, additions, deletions
	return out, nil
}

// resolveBase returns the ref to compare against: the remote-tracking branch
// when it exists, otherwise the local one (a repository with no remote, or a
// base the user checked out explicitly).
func (r *Runner) resolveBase(ctx context.Context, dir, base string) (string, error) {
	candidates := []string{base}
	if !strings.HasPrefix(base, "origin/") {
		candidates = append([]string{"origin/" + base}, candidates...)
	}
	for _, candidate := range candidates {
		if _, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
			return candidate, nil
		}
	}
	return "", ErrBaseMissing
}

// prFiles reads a three-dot diff's file list. Status and line counts come from
// two porcelain formats because neither one carries both.
func (r *Runner) prFiles(ctx context.Context, dir, base string) ([]contracts.PRFile, int, int, error) {
	nameRes, err := r.Run(ctx, dir, "diff", "--name-status", "-z", "--find-renames",
		"--no-ext-diff", "--no-color", base+"...HEAD")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("gitx: diff %s...HEAD: %w", base, err)
	}
	numRes, err := r.Run(ctx, dir, "diff", "--numstat", "-z", "--find-renames",
		"--no-ext-diff", "--no-color", base+"...HEAD")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("gitx: numstat %s...HEAD: %w", base, err)
	}

	files := parseNameStatus(nameRes.Stdout)
	counts := parseNumstat(numRes.Stdout)
	totalAdd, totalDel := 0, 0
	out := make([]contracts.PRFile, 0, len(files))
	for _, f := range files {
		if c, ok := counts[f.Path]; ok {
			f.Additions, f.Deletions = c.additions, c.deletions
		}
		totalAdd += f.Additions
		totalDel += f.Deletions
		out = append(out, f)
	}
	return out, totalAdd, totalDel, nil
}

// parseNameStatus parses `git diff --name-status -z`. Each record is a status
// token followed by one path, except renames/copies which carry old and new.
func parseNameStatus(raw string) []contracts.PRFile {
	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	out := make([]contracts.PRFile, 0, len(fields)/2)
	for i := 0; i < len(fields); {
		status := strings.TrimSpace(fields[i])
		i++
		if status == "" || i >= len(fields) {
			continue
		}
		letter := status[0]
		path := fields[i]
		i++
		if letter == 'R' || letter == 'C' {
			if i >= len(fields) {
				break
			}
			path = fields[i]
			i++
			out = append(out, contracts.PRFile{Path: path, Status: "renamed"})
			continue
		}
		out = append(out, contracts.PRFile{Path: path, Status: statusWord(letter)})
	}
	return out
}

// statusWord maps git's letter to the wire word the client renders.
func statusWord(letter byte) string {
	switch letter {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R', 'C':
		return "renamed"
	default:
		return "modified"
	}
}

type lineCounts struct {
	additions int
	deletions int
}

// parseNumstat parses `git diff --numstat -z`.
//
// A rename is three tokens: "add\tdel\t", old path, new path. The key is the
// new path, matching parseNameStatus, so the two lists join on one name.
func parseNumstat(raw string) map[string]lineCounts {
	out := make(map[string]lineCounts)
	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for i := 0; i < len(fields); {
		record := fields[i]
		i++
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		counts := lineCounts{parseCount(parts[0]), parseCount(parts[1])}
		if parts[2] != "" {
			out[parts[2]] = counts
			continue
		}
		// Rename: the record's path field is empty and the next two tokens are
		// the old and new path. The new path is the key.
		if i >= len(fields) {
			break
		}
		i++ // old path
		if i >= len(fields) {
			break
		}
		out[fields[i]] = counts
		i++
	}
	return out
}

// parseCount is `-` for binary files, where a line count would be a fiction.
func parseCount(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
