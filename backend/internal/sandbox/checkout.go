package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
)

// Checkout and worktree provisioning, shared by every provider whose sandbox can
// run a command through its own HTTP API.
//
// The *plans* (planCheckout, planBranchWorktree) are pure and pinned by tests of
// their own. What used to be Docker-only is their *execution* — and the execution
// is the same wherever the sandbox is: a Kubernetes sandbox runs the same image,
// behind the same /v1/shell/exec, with the same /workspace layout and the same
// detached volume. A second copy of these functions would be a second set of
// rules about what a checkout is, which is exactly the drift this package has
// spent several rounds removing (docs/EXECUTION-BOUNDARY.md §3.1: "the projection
// is the same code, and only the tree it writes into differs").
//
// The provider name is a parameter only because it belongs in the error: an
// operator reading "sandbox: k8s: checkout /workspace/repo" knows which shape
// failed to provision.

// awaitSandboxShell waits until a sandbox's HTTP API answers and returns a shell
// that runs commands inside it.
//
// open resolves the endpoint and the key (it is retried on every attempt, because
// a sandbox that has just been created may not have an address yet); probe is the
// liveness test — an error from it means "the API is not serving yet, try again",
// any answer means it is up. `docker run -d` and a Kubernetes pod both return
// before the image's services are listening, so both providers share this one
// retry window and one bound: a sandbox that never serves the API is an error
// naming the step, not an invitation to fall back to the runtime's own exec.
func awaitSandboxShell(ctx context.Context, open func(context.Context) (checkoutShell, error), probe func(context.Context, checkoutShell) error) (checkoutShell, error) {
	deadline := time.Now().Add(checkoutAPIWait)
	var shell checkoutShell
	var lastErr error
	for {
		// Resolve the endpoint once per attempt, not once per poll: it cannot move
		// while the sandbox exists, and resolving it is a round trip (`docker port`,
		// or a pod read) that must not happen every 250 ms while the image's
		// services come up.
		if shell == nil {
			opened, err := open(ctx)
			if err == nil {
				shell = opened
			} else {
				lastErr = err
			}
		}
		if shell != nil {
			if probeErr := probe(ctx, shell); probeErr == nil {
				return shell, nil
			} else {
				lastErr = probeErr
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the sandbox API did not answer within %s: %w", checkoutAPIWait, lastErr)
		}
		timer := time.NewTimer(checkoutAPIPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// runCheckoutPlan makes /workspace/repo a checkout on branch through the
// sandbox's own API.
//
// shared says the workspace may have a second writer (a bind mount, a shared
// volume), which is what decides whether git is told the checkout is trusted and
// whether the tree is chowned permissive; a detached workspace — Docker's
// per-sandbox volume and every Kubernetes sandbox — is one writer's tree and gets
// neither (see planCheckout and checkoutCommand).
func runCheckoutPlan(ctx context.Context, shell checkoutShell, ready bool, repoURL, branch string, shared bool, provider string) error {
	steps := planCheckout(ready, repoURL, branch, shared)
	if len(steps) == 0 {
		return nil
	}
	if res, err := shell.Exec(ctx, "mkdir -p -- "+aio.Quote(containerRepoDir), containerWorkspace); err != nil {
		return fmt.Errorf("sandbox: %s: prepare %s: %w", provider, containerRepoDir, err)
	} else if res.ExitCode != 0 {
		return fmt.Errorf("sandbox: %s: prepare %s: %s", provider, containerRepoDir, commandFailure(res))
	}
	for _, step := range steps {
		err := runSandboxStep(ctx, shell, step, shared)
		if err == nil {
			continue
		}
		if step.fallback != nil {
			ferr := runSandboxStep(ctx, shell, sandboxStep{name: step.name, dir: step.dir, argv: step.fallback}, shared)
			if ferr == nil {
				continue
			}
			return fmt.Errorf("sandbox: %s: %s %s: %w", provider, step.name, containerRepoDir, ferr)
		}
		return fmt.Errorf("sandbox: %s: %s %s: %w", provider, step.name, containerRepoDir, err)
	}
	return nil
}

// runWorktreePlan creates one branch session's linked worktree through the
// sandbox's own API, as the sandbox user.
//
// repo and worktree are sandbox paths (the caller has already mapped them). The
// rules are runCheckoutPlan's: the image's shell runs as the sandbox user, cwd is
// prefixed by aio.Client.Exec because the image ignores the field, and every git
// command carries `-c safe.directory=<repo>` when the tree may have another owner.
func runWorktreePlan(ctx context.Context, shell checkoutShell, ready bool, repo, worktree, branch string, shared bool, provider string) error {
	steps, err := planBranchWorktree(ready, repo, worktree, branch, shared)
	if err != nil {
		return fmt.Errorf("sandbox: %s: add worktree %s: %w", provider, worktree, err)
	}
	for _, step := range steps {
		if err := runSandboxStep(ctx, shell, step, shared); err != nil {
			return fmt.Errorf("sandbox: %s: %s %s: %w", provider, step.name, worktree, err)
		}
	}
	return nil
}
