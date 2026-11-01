package llmgw_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/llmgw"
	"github.com/piforge/piforge/backend/internal/store"
)

// These tests use the real store, on a real file, and open it twice: the second
// open is the restart. They live outside package llmgw because the point is the
// wiring — a signer plus a database — not the claims.

const testKey = "0123456789abcdef0123456789abcdef"

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", path, err)
	}
	return st
}

// TestRevocationSurvivesAControlPlaneRestart is the defect the second
// adversarial acceptance run named: pause revoked the token, but the revocation
// was a process-local set, so a restart inside the one-hour TTL re-enabled every
// token minted before it.
func TestRevocationSurvivesAControlPlaneRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "piforge.db")
	st := openStore(t, path)
	now := time.Now()

	signer, err := llmgw.NewSigner([]byte(testKey), llmgw.DefaultTTL)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	signer.UseRevocations(st)

	const session = "ses_restart"
	stolen, err := signer.Issue(session, "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := signer.Verify(ctx, stolen, now); err != nil {
		t.Fatalf("a fresh token does not verify: %v", err)
	}

	// The session's agent stops (a pause), which revokes.
	signer.Revoke(session)
	if _, err := signer.Verify(ctx, stolen, now); err == nil {
		t.Fatal("the token still verifies after the revoke")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The restart. Nothing in memory survives — this is a new signer over a new
	// handle to the same file.
	st = openStore(t, path)
	t.Cleanup(func() { _ = st.Close() })
	restarted, err := llmgw.NewSigner([]byte(testKey), llmgw.DefaultTTL)
	if err != nil {
		t.Fatalf("NewSigner after restart: %v", err)
	}
	restarted.UseRevocations(st)

	// The new process starts with an empty in-memory set; if this were the
	// old (memory-only) behaviour the test below would pass for the wrong
	// reason. Reading the durable row is what fills the fast path afterwards.
	if restarted.RevokedCount() != 0 {
		t.Fatalf("the new process's in-memory set is not empty (%d); the test would pass for the wrong reason", restarted.RevokedCount())
	}
	if _, err := restarted.Verify(ctx, stolen, now); err == nil {
		t.Fatal("the revoked token works again after a restart: the revocation is not durable")
	}

	// A session that was never revoked is unaffected by the restart — the fix
	// must not be a global switch.
	alive, err := restarted.Issue("ses_alive", "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := restarted.Verify(ctx, alive, now); err != nil {
		t.Fatalf("an unrelated session's token does not survive the restart: %v", err)
	}
}

// TestResumedGenerationIsValidAfterARestart is the other half of the same
// mechanism: the generation is a counter, not a tombstone, so the token minted
// for the next spawn of a revoked session must carry the persisted number and
// work — including after a restart, where the counter has to come from disk.
func TestResumedGenerationIsValidAfterARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "piforge.db")
	st := openStore(t, path)
	now := time.Now()

	signer, err := llmgw.NewSigner([]byte(testKey), llmgw.DefaultTTL)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	signer.UseRevocations(st)
	const session = "ses_resume"

	before, err := signer.Issue(session, "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	signer.Revoke(session) // pause
	signer.Revoke(session) // and a second stop: the counter is now 2
	gen, err := st.GatewayRevocationGeneration(ctx, session)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if gen != 2 {
		t.Fatalf("durable generation = %d, want 2", gen)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st = openStore(t, path)
	t.Cleanup(func() { _ = st.Close() })
	resumedSigner, err := llmgw.NewSigner([]byte(testKey), llmgw.DefaultTTL)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	resumedSigner.UseRevocations(st)

	// The resume's spawn, after the restart.
	after, err := resumedSigner.Issue(session, "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue after restart: %v", err)
	}
	if _, err := resumedSigner.Verify(ctx, after, now); err != nil {
		t.Fatalf("the resumed session's token does not verify after a restart: %v", err)
	}
	if _, err := resumedSigner.Verify(ctx, before, now); err == nil {
		t.Fatal("the pre-pause token verifies after a restart")
	}
}

// TestSweepKeepsAResumedTokenAlive pins the interaction between the sweep and a
// token minted *after* the revoke: the revocation row's first deadline covered
// only the tokens that existed at the stop, so issuing a later token must extend
// it. Without the extension the sweep deletes the row and a perfectly valid
// resumed token is refused for the rest of its life.
func TestSweepKeepsAResumedTokenAlive(t *testing.T) {
	ctx := context.Background()
	st := openStore(t, filepath.Join(t.TempDir(), "piforge.db"))
	t.Cleanup(func() { _ = st.Close() })

	const ttl = time.Minute
	signer, err := llmgw.NewSigner([]byte(testKey), ttl)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	signer.UseRevocations(st)

	now := time.Now()
	const session = "ses_sweep"
	if _, err := signer.Issue(session, "usr_1", "volc", "", now); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	signer.Revoke(session) // row expires at ~now+1m

	resumedAt := now.Add(50 * time.Second)
	resumed, err := signer.Issue(session, "usr_1", "volc", "", resumedAt)
	if err != nil {
		t.Fatalf("Issue (resumed): %v", err)
	}
	// The sweep runs after the original deadline and before the resumed token's.
	swept, err := signer.Sweep(ctx, now.Add(95*time.Second))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 0 {
		t.Fatalf("the sweep removed %d rows while a live token depended on one", swept)
	}
	if _, err := signer.Verify(ctx, resumed, resumedAt); err != nil {
		t.Fatalf("the resumed token was refused after the sweep: %v", err)
	}
	// Once every affected token has expired by its own claim, the row goes.
	swept, err = signer.Sweep(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("the sweep removed %d rows after every affected token expired, want 1", swept)
	}
}

// A deployment with no store (a unit test, or a bare signer) must still work:
// the in-memory set is the pre-fix behaviour, and it is never weaker than before.
func TestSignerWithoutAStoreStillRevokes(t *testing.T) {
	ctx := context.Background()
	signer, err := llmgw.NewSigner([]byte(testKey), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	now := time.Now()
	token, err := signer.Issue("ses_memory", "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	signer.Revoke("ses_memory")
	if _, err := signer.Verify(ctx, token, now); err == nil {
		t.Fatal("a signer with no store did not revoke in memory")
	}
	if n, err := signer.Sweep(ctx, now); err != nil || n != 0 {
		t.Fatalf("Sweep with no store = %d, %v; want 0, nil", n, err)
	}
}
