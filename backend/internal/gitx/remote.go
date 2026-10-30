package gitx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Remote workflow (PRD FR-8.7).
//
// The workbench could produce commits that only existed on this machine: there was no
// push and no pull, so "commit" was the last step a person could take before dropping
// to a terminal — which is exactly what a web workbench is meant to avoid.
//
// Both operations are deliberately boring: no force, no rebase, no interactive
// anything. A pull that needs a decision fails with a message naming the decision,
// because resolving a conflict inside this UI would be a worse lie than saying no.

// ErrNoRemote means the checkout has no remote to talk to.
var ErrNoRemote = errors.New("gitx: the checkout has no remote")

// ErrDiverged means a pull would need a merge or rebase this UI cannot do.
var ErrDiverged = errors.New("gitx: the branch has diverged from its remote")

// ErrPushRejected means the remote refused the push because it is not a
// fast-forward (the remote has commits this branch does not).
var ErrPushRejected = errors.New("gitx: the remote rejected the push (non-fast-forward)")

// ErrNoCredentials means the remote answered but refused the identity: missing
// SSH key, expired token, a private repository the caller cannot see.
var ErrNoCredentials = errors.New("gitx: the remote refused the credentials")

// PushError carries the git stderr that made a push fail, so the caller can say
// what happened instead of "push failed". Unwrap reports the typed kind, which
// is what the HTTP layer switches on.
type PushError struct {
	Kind   error
	Stderr string
}

func (e *PushError) Error() string {
	detail := lastLine(e.Stderr)
	if detail == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + ": " + detail
}

func (e *PushError) Unwrap() error { return e.Kind }

// lastLine keeps the actionable half of git's multi-line refusal: the first
// lines usually explain, the last one names the cause ("fetch first", "terminal
// prompts disabled").
func lastLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// classifyPushFailure maps git's wording onto the two refusals a person can act
// on. Everything else stays a plain failure, because inventing a cause is worse
// than saying "it failed" with git's own text.
func classifyPushFailure(stderr string) error {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "non-fast-forward"),
		strings.Contains(lower, "fetch first"),
		strings.Contains(lower, "rejected"),
		strings.Contains(lower, "remote has commits"):
		return ErrPushRejected
	case strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "could not read username"),
		strings.Contains(lower, "could not read password"),
		strings.Contains(lower, "terminal prompts disabled"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "invalid username or password"),
		strings.Contains(lower, "access denied"),
		// GitHub answers a private repository without credentials this way:
		// "fatal: repository 'https://…' not found". It is an identity problem
		// disguised as a missing repo.
		(strings.Contains(lower, "repository") && strings.Contains(lower, "not found")):
		return ErrNoCredentials
	}
	return nil
}

// Push sends the current branch to its remote.
//
// `-u` is set on the first push so later ones need no arguments, and the branch name is
// read from the checkout rather than assumed: a session on a branch called `feat-x`
// must not push to `main` because that is what the project called its default.
func (r *Runner) Push(ctx context.Context, root string) (contracts.RemoteResult, error) {
	if !r.hasRemote(ctx, root) {
		return contracts.RemoteResult{}, ErrNoRemote
	}
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return contracts.RemoteResult{}, err
	}
	if branch == "" {
		return contracts.RemoteResult{}, fmt.Errorf("gitx: push: the checkout is not on a branch")
	}
	if _, err := r.Run(ctx, root, "push", "-u", "origin", branch); err != nil {
		stderr := StderrOf(err)
		if kind := classifyPushFailure(stderr); kind != nil {
			return contracts.RemoteResult{}, &PushError{Kind: kind, Stderr: stderr}
		}
		return contracts.RemoteResult{}, fmt.Errorf("gitx: push: %w", err)
	}
	sha, _ := r.Head(ctx, root)
	return contracts.RemoteResult{Branch: branch, Commit: sha}, nil
}

// Pull brings the remote branch into the checkout, fast-forward only.
func (r *Runner) Pull(ctx context.Context, root string) (contracts.RemoteResult, error) {
	if !r.hasRemote(ctx, root) {
		return contracts.RemoteResult{}, ErrNoRemote
	}
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return contracts.RemoteResult{}, err
	}
	if branch == "" {
		return contracts.RemoteResult{}, fmt.Errorf("gitx: pull: the checkout is not on a branch")
	}
	before, _ := r.Head(ctx, root)
	if _, err := r.Run(ctx, root, "pull", "--ff-only", "origin", branch); err != nil {
		stderr := StderrOf(err)
		if strings.Contains(stderr, "Not possible to fast-forward") || strings.Contains(stderr, "diverged") {
			return contracts.RemoteResult{}, ErrDiverged
		}
		return contracts.RemoteResult{}, fmt.Errorf("gitx: pull: %w", err)
	}
	after, _ := r.Head(ctx, root)
	return contracts.RemoteResult{Branch: branch, Commit: after, Changed: before != after}, nil
}

// Fetch updates the remote-tracking refs without touching the working tree, which is
// what "is there anything upstream?" needs.
func (r *Runner) Fetch(ctx context.Context, root string) error {
	if !r.hasRemote(ctx, root) {
		return ErrNoRemote
	}
	if _, err := r.Run(ctx, root, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("gitx: fetch: %w", err)
	}
	return nil
}

// AheadBehind returns how many commits the branch is ahead of and behind its remote.
func (r *Runner) AheadBehind(ctx context.Context, root string) (ahead, behind int, err error) {
	if !r.hasRemote(ctx, root) {
		return 0, 0, ErrNoRemote
	}
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return 0, 0, err
	}
	if branch == "" {
		return 0, 0, nil
	}
	out, err := r.Run(ctx, root, "rev-list", "--left-right", "--count", "origin/"+branch+"...HEAD")
	if err != nil {
		// No upstream yet: everything is ahead, which is what a first push means.
		if strings.Contains(StderrOf(err), "unknown revision") {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("gitx: ahead/behind: %w", err)
	}
	fields := strings.Fields(out.Stdout)
	if len(fields) != 2 {
		return 0, 0, nil
	}
	// `origin/branch...HEAD` counts left=behind, right=ahead.
	fmt.Sscanf(fields[0], "%d", &behind)
	fmt.Sscanf(fields[1], "%d", &ahead)
	return ahead, behind, nil
}

// hasRemote reports whether `origin` is configured.
func (r *Runner) hasRemote(ctx context.Context, root string) bool {
	out, err := r.Run(ctx, root, "remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out.Stdout, "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}
