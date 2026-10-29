package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// FR-8.7: a session can send its work to the remote, and refusals have to be sentences
// a person can act on rather than a raw git failure.
func TestGitRemoteEndpoints(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "piforge-dev-123", contracts.RoleUser)
	creds := env.login("dev@piforge.local", "piforge-dev-123")
	project, session := env.mkSessionFor(user.ID, "remote")
	// The handlers ask which branch to push and from where, so the session needs a
	// checkout — the same sandbox-backed one the product gives it.
	env.withWorktree(user.ID, project.ID, session.ID)

	// Ahead/behind is reported so the UI can hide a push button with nothing behind it.
	env.workspace.ahead = 2
	env.workspace.behind = 1
	var remote struct {
		Remote bool `json:"remote"`
		Ahead  int  `json:"ahead"`
		Behind int  `json:"behind"`
	}
	env.mustJSON(env.doAuthed("GET", "/v1/sessions/"+session.ID+"/git/remote", nil, creds), &remote)
	if !remote.Remote || remote.Ahead != 2 || remote.Behind != 1 {
		t.Fatalf("remote = %+v", remote)
	}

	// No remote configured is an answer, not an error.
	env.workspace.remoteErr = gitx.ErrNoRemote
	remote = struct {
		Remote bool `json:"remote"`
		Ahead  int  `json:"ahead"`
		Behind int  `json:"behind"`
	}{Remote: true}
	env.mustJSON(env.doAuthed("GET", "/v1/sessions/"+session.ID+"/git/remote", nil, creds), &remote)
	if remote.Remote {
		t.Fatalf("a checkout without a remote reported one: %+v", remote)
	}

	// Push reports the branch and the commit it moved.
	env.workspace.remoteErr = nil
	env.workspace.remote = contracts.RemoteResult{Branch: "feat", Commit: "0123456789abcdef"}
	var pushed struct {
		Branch string `json:"branch"`
		Commit string `json:"commit"`
	}
	env.mustJSON(env.doAuthed("POST", "/v1/sessions/"+session.ID+"/git/push", nil, creds), &pushed)
	if pushed.Branch != "feat" || pushed.Commit != "0123456789abcdef" {
		t.Fatalf("push = %+v", pushed)
	}

	// A diverged pull is a 409 with an explanation, not a 500.
	env.workspace.remoteErr = gitx.ErrDiverged
	rec := env.doAuthed("POST", "/v1/sessions/"+session.ID+"/git/pull", nil, creds)
	if rec.Code != http.StatusConflict {
		t.Fatalf("diverged pull status = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "分叉") {
		t.Fatalf("the message does not explain the divergence: %s", rec.Body.String())
	}

	// A pull that works says whether anything changed.
	env.workspace.remoteErr = nil
	env.workspace.remote = contracts.RemoteResult{Branch: "feat", Commit: "abc", Changed: true}
	var pulled struct {
		Changed bool `json:"changed"`
	}
	env.mustJSON(env.doAuthed("POST", "/v1/sessions/"+session.ID+"/git/pull", nil, creds), &pulled)
	if !pulled.Changed {
		t.Fatal("the pull did not report the change")
	}

}
