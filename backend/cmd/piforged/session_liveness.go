package main

import (
	"context"
	"log/slog"
	"path"
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
// that a container provider has a workspace the marker lives in.
//
// Since stage 4 there is exactly one writer and one reader, and both live inside
// the sandbox: the supervisor (internal/agentsupervisor) starts the heartbeat
// launcher there, and this probe reads the marker back through the sandbox's own
// API. Stage 4 deleted the other writer — an argv wrapper for a locally spawned
// agent of a container placement — which nothing produced any more.
//
// A reader without a writer would call every live remote session dead, which is
// why the probe is wired from the same remoteSB check below.

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
