package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/liveness"
	"github.com/piforge/piforge/backend/internal/pi"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// Liveness wiring: who is allowed to answer "an agent is serving this session".
//
// This file is the deployment's half of the contract. internal/liveness knows
// how to turn a heartbeat into an answer and internal/reconcile knows what to do
// with it; only this file knows that -sandbox local has an in-process agent and
// that a container provider has a workspace on a host path the marker can live
// in.
//
// The two halves must agree, which is why they are built in one place:
//
//   - sessionAgentLauncher is the writer: every agent spawned for a container
//     sandbox runs under a process that owns <sandbox>/.piforge/live/<session>.
//   - sessionLivenessProbe is the reader: the registry first, then that marker
//     through the sandbox's own API.
//
// A writer without a reader would leave stale files; a reader without a writer
// would call every live remote session dead. Both are wired from the same
// remoteSB check below.

// sessionAgentLauncher returns the argv wrapper that gives a container-backed
// agent a heartbeat, or nil for the local provider.
//
// Nil is not a degradation here: the local provider's agent is a child of this
// process, so a restart kills it and the registry's "dead" is the truth.
func sessionAgentLauncher(opt options, st *store.Store, remote sandbox.Remote, logger *slog.Logger) func(context.Context, contracts.SessionSpec, []string) ([]string, error) {
	if remote == nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		// Without the executable's own path the launcher cannot be re-executed,
		// and an agent without a heartbeat would be interrupted by the next
		// boot pass. Say it loudly: this is the one failure that silently
		// reverts the deployment to the old, wrong behaviour.
		logger.Error("session liveness: cannot resolve this executable, so agents will run without a heartbeat marker; a control-plane restart will interrupt their runs",
			"error", err.Error())
		return nil
	}
	logger.Info("session liveness: container agents run under a heartbeat launcher",
		"sandbox", opt.sandboxKind,
		"marker", path.Join(sandbox.ContainerWorkspace, liveness.MarkerDir, "<session>"),
		"beat", liveness.DefaultBeat.String(),
		"launcher", self+" "+liveness.LauncherCommand)

	return func(ctx context.Context, spec contracts.SessionSpec, argv []string) ([]string, error) {
		target, terr := st.SessionSandboxFor(ctx, spec.SessionID)
		if terr != nil {
			// Failing the spawn is deliberate. Running the agent unmarked would
			// make the probe call this session dead on the next pass, and the
			// run would be interrupted by reconciliation rather than by an
			// honest error here.
			return nil, fmt.Errorf("resolve the sandbox for the heartbeat marker: %w", terr)
		}
		if strings.TrimSpace(target.RootDir) == "" {
			return nil, fmt.Errorf("sandbox %s has no host workspace, so the heartbeat marker has nowhere to live", target.SandboxID)
		}
		return liveness.LauncherArgv(self, liveness.SupervisorConfig{
			Root:      target.RootDir,
			SessionID: spec.SessionID,
			Interval:  liveness.DefaultBeat,
			Logger:    logger,
		}, argv)
	}
}

// sessionLivenessProbe builds the authority reconciliation asks.
//
// The local provider gets the in-process registry alone. A container provider
// gets the registry *and* the sandbox marker: after a control-plane restart the
// registry is empty by definition, and only the sandbox can say whether the
// agent it hosts is still running. The composite reports alive if either
// proves it, and unknown if neither can answer — never dead from an unanswered
// probe.
func sessionLivenessProbe(opt options, st *store.Store, rt *pi.Runtime, remote sandbox.Remote, keys sandboxKeys, logger *slog.Logger) contracts.LivenessProbe {
	process := liveness.NewProcess(rt)
	if remote == nil {
		logger.Info("session liveness: the in-process agent registry",
			"sandbox", opt.sandboxKind,
			"probe", liveness.Describe(process),
			"detail", "the agent is a child of this process, so a restart kills it and an empty registry is the truth")
		return process
	}
	reader, err := liveness.NewAIO(liveness.AIOConfig{
		Locate: func(ctx context.Context, sessionID string) (string, error) {
			target, terr := st.SessionSandboxFor(ctx, sessionID)
			if terr != nil {
				return "", terr
			}
			return target.SandboxID, nil
		},
		Endpoint: remote.Endpoint,
		APIKey:   keys.For,
	})
	if err != nil {
		// A probe that cannot be built would answer "unknown" forever, which is
		// safe but silently useless: say so instead of pretending.
		logger.Warn("session liveness: the sandbox marker probe is unavailable; only the in-process registry will be asked",
			"error", err.Error())
		return process
	}
	marker := liveness.NewSandbox(liveness.SandboxConfig{
		Reader: reader,
		TTL:    liveness.DefaultTTL,
		Now:    time.Now,
	})
	probe := liveness.NewComposite(process, marker)
	logger.Info("session liveness: the in-process registry and the sandbox heartbeat marker",
		"sandbox", opt.sandboxKind,
		"marker", path.Join(sandbox.ContainerWorkspace, liveness.MarkerDir, "<session>"),
		"ttl", liveness.DefaultTTL.String(),
		"probe", liveness.Describe(probe),
		"detail", "an agent inside the sandbox outlives this process, so a probe error or a fresh marker keeps the session running")
	return probe
}
