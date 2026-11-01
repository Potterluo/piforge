package sandbox

import (
	"context"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// NormalizeSandboxID is the one place the two ids for a sandbox meet: the pair
// id the store row and the local provider use (`sbx_<hash>`) and the container
// name the docker provider stores (`piforge-<hash>`). Both carry the same hash,
// so the mapping is a prefix rewrite, and both have to reach the same container.
func TestNormalizeSandboxID(t *testing.T) {
	const hash = "0123456789abcdef"
	cases := []struct{ in, want string }{
		{"sbx_" + hash, "piforge-" + hash},
		{"piforge-" + hash, "piforge-" + hash},
		{hash, "piforge-" + hash},
		{"  sbx_" + hash + "  ", "piforge-" + hash},
		// An id in neither form is not guessed into something that exists: it
		// keeps the container prefix and fails as "not found", which is the
		// honest answer for a typo.
		{"sbx_nope", "piforge-nope"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeSandboxID(tc.in); got != tc.want {
			t.Fatalf("NormalizeSandboxID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The id the handler used to pass, run through the old resolve(), became
	// `piforge-sbx_<hash>`: a container that has never existed. Pin that it now
	// reaches the pair's container.
	provider := &Docker{}
	if got := provider.resolve("sbx_" + hash); got != "piforge-"+hash {
		t.Fatalf("resolve(sbx_<hash>) = %q, want the container name", got)
	}
}

// Pause and Resume must accept both id forms. The defect this pins was found by
// an adversarial acceptance run: `POST /v1/sessions/{id}/sandbox/pause` answered
// 422 `sandbox: not found: piforge-sbx_<hash>` while the sandbox was READY, so
// idle pause could never park anything (and resume failed identically).
func TestDockerPauseAndResumeAcceptBothIDForms(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_pause_forms", "prj_pause_forms"

	created, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Skipf("cannot start a container here: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), created.ID) })

	pairID := ID(userID, projectID)
	if pairID == created.ID {
		t.Fatalf("the two id forms are identical (%q); the test proves nothing", pairID)
	}

	// Pause by the pair id (what the HTTP handler passes through the row), then
	// read the state back by the container name.
	if err := provider.Pause(ctx, pairID); err != nil {
		t.Fatalf("Pause(sbx_<hash>): %v", err)
	}
	paused, err := provider.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after pause: %v", err)
	}
	if paused.State != contracts.SandboxPaused {
		t.Fatalf("state after pausing by the pair id = %q, want paused", paused.State)
	}

	// Resume by the pair id too: the handler's resume path is the same call.
	resumed, err := provider.Resume(ctx, pairID)
	if err != nil {
		t.Fatalf("Resume(sbx_<hash>): %v", err)
	}
	if resumed.State != contracts.SandboxReady {
		t.Fatalf("state after resuming by the pair id = %q, want ready", resumed.State)
	}

	// And the container-name form still works, so the fix is not "only the new
	// form works".
	if err := provider.Pause(ctx, created.ID); err != nil {
		t.Fatalf("Pause(piforge-<hash>): %v", err)
	}
	if got, err := provider.Resume(ctx, created.ID); err != nil {
		t.Fatalf("Resume(piforge-<hash>): %v", err)
	} else if got.State != contracts.SandboxReady {
		t.Fatalf("state after resuming by the container name = %q, want ready", got.State)
	}
}


