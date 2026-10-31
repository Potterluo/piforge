package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// quotaRefusingProvisioner is a provisioner whose tenant quota is exhausted. It
// returns exactly what internal/provision returns, so the test pins the HTTP
// contract and not a paraphrase of it.
type quotaRefusingProvisioner struct {
	limit   int
	current int
	calls   int
}

func (p *quotaRefusingProvisioner) EnsureWorktree(_ context.Context, _ string, _ *contracts.Project, _ *contracts.Session) (*contracts.Worktree, error) {
	p.calls++
	return nil, &contracts.APIError{
		Code:    contracts.ErrTenantSandboxLimit,
		Message: "this tenant already has 2 sandboxes, which is the limit (2): delete or pause a project's sandbox before creating another",
		Details: map[string]any{"limit": p.limit, "current": p.current, "tenant": "usr_test"},
	}
}

// A provisioning refusal is the answer to the request. It used to be logged and
// swallowed ("provision: no worktree for session"), so the caller saw a generic
// 500 three layers later; this pins that the typed error, its status and its
// numbers survive to the client.
func TestTenantQuotaRefusalSurfacesFromSessionCreation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	cookie := env.login("dev@piforge.local", "pw")
	project, _ := env.mkSessionFor(user.ID, "quota")
	refusing := &quotaRefusingProvisioner{limit: 2, current: 2}
	env.deps.Provisioner = refusing
	env.rebuild()

	rec := env.doAuthed(http.MethodPost, "/v1/projects/"+project.ID+"/sessions",
		map[string]any{"kind": "main", "title": "third"}, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	env.mustJSON(rec, &body)
	if body.Error.Code != contracts.ErrTenantSandboxLimit {
		t.Fatalf("code = %q, want %q", body.Error.Code, contracts.ErrTenantSandboxLimit)
	}
	if body.Error.Details["limit"] != float64(2) || body.Error.Details["current"] != float64(2) {
		t.Fatalf("details = %v", body.Error.Details)
	}
	if refusing.calls != 1 {
		t.Fatalf("the provisioner was called %d times, want 1", refusing.calls)
	}

	// A refused session must not linger: the client never learned its id, and the
	// next attempt would count a project it cannot open.
	sessions, err := env.store.ListSessions(t.Context(), user.ID, store.SessionFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	for _, sess := range sessions {
		if sess.Title == "third" {
			t.Fatalf("the refused session was kept: %+v", sess)
		}
	}
}
