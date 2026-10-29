package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The per-sandbox API key is a secret that the control plane holds so it can talk
// to one container. It must never travel back out through the product's own API:
// a browser tab, a curl and a log scraper all see these responses, and the key
// opens the tenant's shell and files.
//
// This walks the endpoints that render a sandbox and asserts the key is absent
// from every one of them. If it ever gains a JSON tag, these are the routes that
// would leak it first.
func TestSandboxAPIKeyNeverAppearsInAnAPIResponse(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")

	const key = "6f1c2d3e4a5b6c7d8e9f00112233445566778899aabbccddeeff001122334455"
	ctx := context.Background()
	sb, err := env.store.CreateSandbox(ctx, user.ID, store.NewSandbox{
		ProjectID: project.ID, State: contracts.SandboxReady, Provider: "runner", APIKey: key,
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, user.ID, store.NewWorktree{
		SandboxID: sb.ID, Path: "/srv/wt/repo", Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, user.ID, sess.ID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}

	// The store really holds the key: the assertions below would pass vacuously
	// otherwise.
	if recorded, err := env.store.SandboxAPIKey(ctx, sb.ID); err != nil || recorded != key {
		t.Fatalf("SandboxAPIKey = %q, %v", recorded, err)
	}

	paths := []string{
		"/v1/projects",
		"/v1/projects/" + project.ID,
		"/v1/sessions",
		"/v1/sessions/" + sess.ID,
		"/v1/sessions/" + sess.ID + "/sandbox",
		"/v1/sessions/" + sess.ID + "/sandbox/snapshots",
	}
	for _, path := range paths {
		rec := env.doAuthed(http.MethodGet, path, nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d body %s", path, rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); strings.Contains(body, key) {
			t.Fatalf("GET %s returned the sandbox API key: %s", path, body)
		}
	}

	// The same shape that the runner's answer carries into this process must not
	// serialize the key either — a future handler that writes a sandbox directly
	// would leak it otherwise.
	raw, err := json.Marshal(sb)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("contracts.Sandbox serialized its API key: %s", raw)
	}
}
