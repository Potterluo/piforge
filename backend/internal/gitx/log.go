package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// logFormat is tab separated so a subject containing spaces (or even an emoji)
// survives parsing. %D carries the ref decoration.
const logFormat = "%H%x09%P%x09%an%x09%ae%x09%at%x09%s%x09%D"

// Log returns one page of the commit DAG in --date-order.
//
// `cursor` is an opaque page token: it is the number of commits already
// returned, which maps directly onto `git log --skip`. The frontend
// virtualises the list, so pages never need to overlap.
func (r *Runner) Log(ctx context.Context, root string, limit int, cursor string) (*contracts.GitGraph, error) {
	dir, err := repoPath(root)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	skip := 0
	if strings.TrimSpace(cursor) != "" {
		n, cerr := strconv.Atoi(strings.TrimSpace(cursor))
		if cerr != nil || n < 0 {
			return nil, refuseRequest("log: bad cursor %q", cursor)
		}
		skip = n
	}

	head, _ := r.Head(ctx, dir)

	args := []string{
		"log", "--all", "--date-order", "--parents", "--no-color",
		"--pretty=format:" + logFormat,
		"--max-count=" + strconv.Itoa(limit),
	}
	if skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(skip))
	}

	res, err := r.Run(ctx, dir, args...)
	if err != nil {
		// A repository with zero commits has no revisions to walk. That is a
		// normal state (fresh `git init`), not a failure.
		if empty, emptyErr := r.isEmptyRepo(ctx, dir); emptyErr == nil && empty {
			return &contracts.GitGraph{Commits: []contracts.GitCommit{}, Head: head}, nil
		}
		return nil, fmt.Errorf("gitx: log %s: %w", dir, err)
	}

	commits := parseLog(res.Stdout)
	graph := &contracts.GitGraph{Commits: commits, Head: head}
	if len(commits) == limit {
		graph.NextCursor = strconv.Itoa(skip + len(commits))
	}
	return graph, nil
}

// isEmptyRepo reports whether the repository has no refs at all.
func (r *Runner) isEmptyRepo(ctx context.Context, dir string) (bool, error) {
	res, err := r.Run(ctx, dir, "for-each-ref", "--count=1", "--format=%(refname)", "refs/heads", "refs/tags", "refs/remotes")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) == "", nil
}

func parseLog(out string) []contracts.GitCommit {
	commits := make([]contracts.GitCommit, 0, 128)
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) < 7 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		if hash == "" {
			continue
		}
		commit := contracts.GitCommit{
			Hash:    hash,
			Short:   shortSHA(hash),
			Parents: splitFields(fields[1]),
			Author:  fields[2],
			Email:   fields[3],
			Subject: fields[5],
			Refs:    parseRefs(fields[6]),
		}
		if ts, terr := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64); terr == nil {
			commit.TS = time.Unix(ts, 0).UTC()
		}
		commits = append(commits, commit)
	}
	return commits
}

// splitFields splits a space separated list.
//
// It returns an empty slice rather than nil for the empty case: a root commit
// has no parents, and `nil` marshals to JSON `null`, which a client iterating
// `parents.length` cannot survive. Empty containers on the wire are always
// arrays.
func splitFields(s string) []string {
	out := make([]string, 0, 2)
	if strings.TrimSpace(s) == "" {
		return out
	}
	return append(out, strings.Fields(s)...)
}

// parseRefs classifies the %D decoration list ("HEAD -> main, tag: v1.0,
// origin/main") into the four ref kinds the UI badges.
func parseRefs(decoration string) []contracts.GitRef {
	decoration = strings.TrimSpace(decoration)
	refs := make([]contracts.GitRef, 0, 4)
	if decoration == "" {
		return refs
	}
	seen := map[string]bool{}
	add := func(kind, name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := kind + "\x00" + name
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, contracts.GitRef{Type: kind, Name: name})
	}
	for _, part := range strings.Split(decoration, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case part == "HEAD":
			add("head", "HEAD")
		case strings.HasPrefix(part, "HEAD -> "):
			branch := strings.TrimPrefix(part, "HEAD -> ")
			add("head", "HEAD")
			add("branch", branch)
		case strings.HasPrefix(part, "tag: "):
			add("tag", strings.TrimPrefix(part, "tag: "))
		case strings.Contains(part, "/"):
			add("remote", part)
		default:
			add("branch", part)
		}
	}
	return refs
}
