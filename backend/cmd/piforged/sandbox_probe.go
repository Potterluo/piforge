package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// sandboxProbeTimeout bounds one liveness probe.
//
// It is deliberately short: the question is "can this sandbox still fork", and a
// sandbox that answers `true` in three seconds answers a prompt in the same
// window. The alternative — measuring with `du`, which is what found the wedge
// in adversarial acceptance #2 — takes up to 30 s and reports a timeout rather
// than "unresponsive", so it cannot be the detection signal.
const sandboxProbeTimeout = 3 * time.Second

// sandboxProbe answers "is this sandbox alive" with one trivial command through
// the sandbox's own API.
//
// Why the API and not `docker exec`: the API is the door every product feature
// already uses (the agent's Dial, the file panel, the disk measurement), and it
// runs as the sandbox user, so "the API cannot run a command" is exactly the
// condition the user experiences. `docker exec` as root is a different identity
// with a different failure mode: it can succeed while the tenant's own shell is
// dead, and it is not available to a deployment whose containers belong to a
// runner service.
//
// A wedged sandbox (a fork storm that exhausted the pids cgroup) fails here in
// one of two ways: the API returns an error because its own `fork` was refused,
// or the request does not complete inside the probe window. Both are "no".
func sandboxProbe(remote sandbox.Remote, keys sandboxKeys) func(ctx context.Context, sandboxID string) error {
	if remote == nil {
		return nil
	}
	return func(ctx context.Context, sandboxID string) error {
		ctx, cancel := context.WithTimeout(ctx, sandboxProbeTimeout)
		defer cancel()
		endpoint, err := remote.Endpoint(ctx, sandboxID)
		if err != nil {
			return fmt.Errorf("sandbox %s has no reachable endpoint: %w", sandboxID, err)
		}
		key, err := keys.For(ctx, sandboxID)
		if err != nil {
			return err
		}
		client, err := aio.New(aio.Config{
			BaseURL: endpoint,
			APIKey:  key,
			Root:    sandbox.ContainerWorkspace,
			Timeout: sandboxProbeTimeout,
		})
		if err != nil {
			return err
		}
		res, err := client.Exec(ctx, "true", sandbox.ContainerWorkspace)
		if err != nil {
			return fmt.Errorf("sandbox %s did not answer a trivial command in %s: %w", sandboxID, sandboxProbeTimeout, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("sandbox %s answered the liveness probe with exit %d: %s",
				sandboxID, res.ExitCode, strings.TrimSpace(res.Output))
		}
		return nil
	}
}
