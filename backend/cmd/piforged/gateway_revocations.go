package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/piforge/piforge/backend/internal/llmgw"
)

// Gateway token revocation: the durable half's two pieces of housekeeping.
//
// internal/llmgw signs a claim, so the only way to end a token before its TTL is
// a generation counter the verifier compares against. That counter lives in the
// `gateway_revocations` table (store/gateway_revocations.go) because the second
// adversarial acceptance run showed what a memory-only counter costs: a
// control-plane restart inside the token's lifetime re-enabled every token
// minted before it.
//
// Two things in this file, and one rule:
//
//   - the sweep, which keeps the table proportional to the revocations that are
//     still load-bearing;
//   - the stop hook the agent runtime calls, with one deliberate exception for
//     shutdown (below).

// revocationSweepInterval is how often expired revocation rows are collected. A
// row lives for at least one token TTL after the last token it covers, so the
// interval only decides how long a dead row lingers; ten minutes keeps the table
// small without making the sweep part of anything's critical path.
const revocationSweepInterval = 10 * time.Minute

// sweepGatewayRevocations deletes revocation rows whose last affected token has
// expired, until ctx is done. A failure is logged and retried on the next tick:
// the sweep is housekeeping, and a control plane that cannot sweep is still
// correct — it just keeps rows longer.
func sweepGatewayRevocations(ctx context.Context, signer *llmgw.Signer, logger *slog.Logger) {
	ticker := time.NewTicker(revocationSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			removed, err := signer.Sweep(sweepCtx, time.Now())
			cancel()
			if err != nil {
				logger.Warn("could not sweep expired model-gateway revocations; they will be retried",
					"error", err.Error())
				continue
			}
			if removed > 0 {
				logger.Info("swept expired model-gateway revocations",
					"removed", removed,
					"detail", "their tokens have passed their own expiry, so the generation is no longer load-bearing")
			}
		}
	}
}

// revocationHook is the stop hook pi.Runtime calls for every session whose agent
// ended (pause, archive, delete, model switch, idle reap, shutdown).
//
// Shutdown is the exception, and it is deliberate. A container placement's agent
// outlives the control plane by design (internal/agentsupervisor): its process,
// and the token in its models.json, keep running through a restart, and the
// reattached process cannot mint a new one without a new prompt. Persisting a
// revocation at shutdown would therefore break the very property stage 3 built —
// a surviving agent would answer 401 until the user sent another message.
//
// The in-memory bump is skipped too: it would be dead on the next line, and
// skipping both keeps the two halves consistent instead of "revoked in a process
// that is exiting". Every stop the *user* or the platform causes (pause,
// archive, delete, model switch, idle) still writes the row.
type revocationHook struct {
	signer *llmgw.Signer
	// draining is set once the process starts shutting down.
	draining atomic.Bool
}

// onStop is the func(string) the agent runtime takes.
func (h *revocationHook) onStop(sessionID string) {
	if h.draining.Load() {
		return
	}
	h.signer.Revoke(sessionID)
}

// drain marks the process as shutting down. It must run *before* pi.Runtime.Close
// (so register it after `defer rt.Close()`), which is the only caller that stops
// agents during shutdown.
func (h *revocationHook) drain() { h.draining.Store(true) }
