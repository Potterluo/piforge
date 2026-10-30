package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// The PR flow seen through the routes (FR-12.6). A real bare repository and the
// real gitx runner back the checkout, and a fake forge answers the create call,
// so the test can assert what crossed the wire and what it did to the remote.

type prFixture struct {
	env      *testEnv
	user     *contracts.User
	creds    *creds
	project  *contracts.Project
	session  *contracts.Session
	checkout string
	bare     string
	seed     string
}

// prForge is the forge side of the test: it records the request and keeps the
// PRs it created, so a second run can find the first one.
type prForge struct {
	mu           sync.Mutex
	posts        int
	gets         int
	auth         string
	created      map[string]string
	open         []map[string]any
	createStatus int
	createBody   string
}

func (f *prForge) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	switch r.Method {
	case http.MethodGet:
		f.gets++
		_ = json.NewEncoder(w).Encode(f.open)
	case http.MethodPost:
		f.posts++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.created = body
		if f.createStatus != 0 {
			w.WriteHeader(f.createStatus)
			w.Write([]byte(f.createBody))
			return
		}
		pr := map[string]any{
			"number":   1,
			"html_url": "http://forge.test/acme/widgets/pull/1",
			"title":    body["title"],
			"head":     map[string]string{"ref": strings.TrimPrefix(body["head"], "acme:")},
			"base":     map[string]string{"ref": body["base"]},
		}
		f.open = append(f.open, pr)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// newPRFixture builds a project whose checkout is a real clone of a local bare
// repository, on a branch called piforge/fix with one commit on top of main.
func newPRFixture(t *testing.T) *prFixture {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	runGitIn(t, root, "init", "--bare", "-b", "main", bare)
	runGitIn(t, root, "clone", bare, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, seed, "add", "-A")
	runGitIn(t, seed, "commit", "-m", "seed")
	runGitIn(t, seed, "push", "origin", "main")

	env := newTestEnv(t)
	user := env.mkUser("pr@piforge.local", "pw", contracts.RoleUser)
	c := env.login("pr@piforge.local", "pw")
	project, session := env.mkSessionFor(user.ID, "pr-project")
	checkout := attachScreenshotWorktree(t, env, user.ID, project, session)
	// The helper made an empty directory; the checkout has to be a clone of the
	// project's remote for a push to mean anything.
	if err := os.RemoveAll(checkout); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, root, "clone", bare, checkout)
	runGitIn(t, checkout, "checkout", "-b", "piforge/fix")
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, checkout, "add", "-A")
	runGitIn(t, checkout, "commit", "-m", "add the feature")

	// The PR path needs real commit content and a real push.
	env.deps.Workspace = fsx.New(fsx.Config{Git: gitx.NewRunner()})
	env.rebuild()

	return &prFixture{env: env, user: user, creds: c, project: project,
		session: session, checkout: checkout, bare: bare, seed: seed}
}

// configureForge points the project at a fake forge and asserts the save
// response never contains the token.
func (f *prFixture) configureForge(t *testing.T, srv *httptest.Server) {
	t.Helper()
	rec := f.env.doAuthed(http.MethodPut, "/v1/projects/"+f.project.ID+"/forge", map[string]any{
		"kind": "github", "api_base": srv.URL, "repo": "acme/widgets",
		"base_branch": "main", "token": "sekret-forge-token",
	}, f.creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT forge: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sekret-forge-token") {
		t.Fatalf("the token was echoed back: %s", rec.Body.String())
	}
	var body struct {
		Project *contracts.Project `json:"project"`
	}
	f.env.mustJSON(rec, &body)
	if body.Project.Forge == nil || !body.Project.Forge.HasToken {
		t.Fatalf("forge = %+v", body.Project.Forge)
	}
}

func (f *prFixture) createPR(t *testing.T, title string) *httptest.ResponseRecorder {
	t.Helper()
	return f.env.doAuthed(http.MethodPost, "/v1/sessions/"+f.session.ID+"/git/pr",
		map[string]any{"title": title, "body": "from a test"}, f.creds)
}

// TestCreatePullRequestPushesThenCreates is the happy path: the branch exists
// on the remote afterwards, the forge got the title/body/head/base, and the
// response has the link.
func TestCreatePullRequestPushesThenCreates(t *testing.T) {
	f := newPRFixture(t)
	fake := &prForge{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	f.configureForge(t, srv)

	// Preview first: what would be pushed, without touching the remote.
	var preview struct {
		Branch      string                 `json:"branch"`
		BaseBranch  string                 `json:"base_branch"`
		Remote      bool                   `json:"remote"`
		CommitCount int                    `json:"commit_count"`
		Files       []contracts.PRFile     `json:"files"`
		Title       string                 `json:"title"`
		Body        string                 `json:"body"`
		Forge       *contracts.ForgeConfig `json:"forge"`
		Warnings    []string               `json:"warnings"`
	}
	rec := f.env.doAuthed(http.MethodGet, "/v1/sessions/"+f.session.ID+"/git/pr", nil, f.creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	f.env.mustJSON(rec, &preview)
	if preview.Branch != "piforge/fix" || preview.BaseBranch != "main" {
		t.Fatalf("preview branch = %q base = %q", preview.Branch, preview.BaseBranch)
	}
	if !preview.Remote || preview.CommitCount != 1 {
		t.Fatalf("preview remote=%v commits=%d", preview.Remote, preview.CommitCount)
	}
	if len(preview.Files) != 1 || preview.Files[0].Path != "feature.txt" ||
		preview.Files[0].Status != "added" || preview.Files[0].Additions != 2 {
		t.Fatalf("preview files = %+v", preview.Files)
	}
	// Prefilled from the session title (no user turn was recorded in this test)
	// and the latest commit subject.
	if preview.Title != "hello" {
		t.Fatalf("preview title = %q", preview.Title)
	}
	if preview.Body != "add the feature" {
		t.Fatalf("preview body = %q", preview.Body)
	}
	if preview.Forge == nil || !preview.Forge.HasToken {
		t.Fatalf("preview forge = %+v", preview.Forge)
	}
	if fake.posts != 0 {
		t.Fatal("the preview created a PR")
	}

	// Create: push, then the forge call.
	created := f.createPR(t, "Add feature")
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var out createPRResponse
	f.env.mustJSON(created, &out)
	if !out.Created || out.PullRequest == nil || out.PullRequest.URL == "" {
		t.Fatalf("create response = %+v", out)
	}
	if out.Pushed.Branch != "piforge/fix" || out.Pushed.Commit == "" {
		t.Fatalf("pushed = %+v", out.Pushed)
	}
	if fake.created["title"] != "Add feature" || fake.created["body"] != "from a test" {
		t.Fatalf("forge body = %#v", fake.created)
	}
	if fake.created["head"] != "acme:piforge/fix" || fake.created["base"] != "main" {
		t.Fatalf("forge refs = %#v", fake.created)
	}
	if fake.auth != "Bearer sekret-forge-token" {
		t.Fatalf("auth = %q", fake.auth)
	}
	// The push really reached the remote.
	log := runGitIn(t, f.bare, "--git-dir", f.bare, "log", "--format=%s", "piforge/fix")
	if !strings.Contains(log, "add the feature") {
		t.Fatalf("the branch is not on the remote:\n%s", log)
	}

	// Second run: the existing PR is returned, and no second one is created.
	again := f.createPR(t, "Add feature")
	if again.Code != http.StatusOK {
		t.Fatalf("second create: %d %s", again.Code, again.Body.String())
	}
	var second createPRResponse
	f.env.mustJSON(again, &second)
	if second.Created {
		t.Fatal("the second run claimed to have created a PR")
	}
	if second.PullRequest.URL != out.PullRequest.URL || !second.PullRequest.Existing {
		t.Fatalf("second PR = %+v", second.PullRequest)
	}
	if fake.posts != 1 {
		t.Fatalf("the forge received %d creates, want 1", fake.posts)
	}

	// The receipt survives a reload: the preview shows it.
	var after struct {
		PullRequest *contracts.PullRequest `json:"pull_request"`
	}
	rec = f.env.doAuthed(http.MethodGet, "/v1/sessions/"+f.session.ID+"/git/pr", nil, f.creds)
	f.env.mustJSON(rec, &after)
	if after.PullRequest == nil || after.PullRequest.URL != out.PullRequest.URL {
		t.Fatalf("the preview lost the PR: %+v", after.PullRequest)
	}

	// The transcript says what happened.
	events, err := f.env.store.ListEvents(context.Background(), f.user.ID, f.session.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == "pr.created" && strings.Contains(string(ev.Payload), "pull/1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pr.created event in %d events", len(events))
	}
}

// No remote: the preview explains it and the create response names the push
// step instead of failing generically.
func TestCreatePullRequestNamesThePushStepWithoutARemote(t *testing.T) {
	f := newPRFixture(t)
	fake := &prForge{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	f.configureForge(t, srv)
	runGitIn(t, f.checkout, "remote", "remove", "origin")

	var preview struct {
		Remote   bool     `json:"remote"`
		Warnings []string `json:"warnings"`
	}
	f.env.mustJSON(f.env.doAuthed(http.MethodGet, "/v1/sessions/"+f.session.ID+"/git/pr", nil, f.creds), &preview)
	if preview.Remote {
		t.Fatal("a checkout without origin reported a remote")
	}
	if !strings.Contains(strings.Join(preview.Warnings, "\n"), "没有远端") {
		t.Fatalf("warnings = %v", preview.Warnings)
	}

	rec := f.createPR(t, "No remote")
	f.env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	f.env.mustJSON(rec, &body)
	if body.Error.Details["step"] != "push" {
		t.Fatalf("step = %v", body.Error.Details["step"])
	}
	if !strings.Contains(body.Error.Message, "第 2 步（推送）") || !strings.Contains(body.Error.Message, "没有远端") {
		t.Fatalf("message = %q", body.Error.Message)
	}
	if fake.posts != 0 {
		t.Fatal("the forge was called without a pushed branch")
	}
}

// A non-fast-forward push is a 409 that says "pull first", not a 500.
func TestCreatePullRequestReportsARejectedPush(t *testing.T) {
	f := newPRFixture(t)
	fake := &prForge{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	f.configureForge(t, srv)

	// Somebody else pushed this branch first, so the local commit is not a
	// fast-forward of what the remote has.
	other := filepath.Join(t.TempDir(), "other")
	runGitIn(t, filepath.Dir(f.bare), "clone", f.bare, other)
	runGitIn(t, other, "checkout", "-b", "piforge/fix")
	if err := os.WriteFile(filepath.Join(other, "remote-only.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, other, "add", "-A")
	runGitIn(t, other, "commit", "-m", "remote work")
	runGitIn(t, other, "push", "origin", "piforge/fix")

	rec := f.createPR(t, "Rejected")
	f.env.mustErr(rec, http.StatusConflict, contracts.ErrConflict)
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	f.env.mustJSON(rec, &body)
	if body.Error.Details["step"] != "push" || body.Error.Details["reason"] != "non_fast_forward" {
		t.Fatalf("details = %+v", body.Error.Details)
	}
	if !strings.Contains(body.Error.Message, "快进") || !strings.Contains(body.Error.Message, "rejected") {
		t.Fatalf("message = %q", body.Error.Message)
	}
	if fake.posts != 0 {
		t.Fatal("a PR was created after a rejected push")
	}
}

// The forge's 422 is surfaced with its own sentence, and the step is "create".
func TestCreatePullRequestSurfacesTheForgeError(t *testing.T) {
	f := newPRFixture(t)
	fake := &prForge{
		createStatus: http.StatusUnprocessableEntity,
		createBody:   `{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"base","code":"invalid"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	f.configureForge(t, srv)

	rec := f.createPR(t, "Bad base")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), want the forge's 422 mirrored", rec.Code, rec.Body.String())
	}
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	f.env.mustJSON(rec, &body)
	if body.Error.Code != contracts.ErrForge {
		t.Fatalf("code = %q", body.Error.Code)
	}
	if body.Error.Details["step"] != "create" {
		t.Fatalf("step = %v", body.Error.Details["step"])
	}
	if body.Error.Details["forge_status"] != float64(422) {
		t.Fatalf("forge_status = %v", body.Error.Details["forge_status"])
	}
	if !strings.Contains(body.Error.Message, "Validation Failed") ||
		!strings.Contains(body.Error.Message, "base") {
		t.Fatalf("the forge text was not preserved: %q", body.Error.Message)
	}
	if !strings.Contains(body.Error.Message, "第 3 步（创建 PR）") {
		t.Fatalf("the step is not named: %q", body.Error.Message)
	}
}

// Configuration missing: the refusal points at step 1 before any push happens.
func TestCreatePullRequestNeedsForgeConfiguration(t *testing.T) {
	f := newPRFixture(t)
	rec := f.createPR(t, "Needs config")
	f.env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	f.env.mustJSON(rec, &body)
	if body.Error.Details["step"] != "config" {
		t.Fatalf("step = %v", body.Error.Details["step"])
	}
	if !strings.Contains(body.Error.Message, "第 1 步（配置）") {
		t.Fatalf("message = %q", body.Error.Message)
	}
}

// The token is a write-only secret: sealed in the row, never echoed, never
// logged, and kept when the form is saved without retyping it.
func TestForgeTokenIsSealedAndNeverLogged(t *testing.T) {
	f := newPRFixture(t)
	fake := &prForge{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()

	var logs bytes.Buffer
	f.env.deps.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.env.rebuild()

	const token = "ghp_very-secret-token"
	rec := f.env.doAuthed(http.MethodPut, "/v1/projects/"+f.project.ID+"/forge", map[string]any{
		"kind": "github", "api_base": srv.URL, "repo": "acme/widgets", "token": token,
	}, f.creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT forge: %d %s", rec.Code, rec.Body.String())
	}

	cipherBlob, err := f.env.store.ProjectForgeTokenCipher(context.Background(), f.user.ID, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cipherBlob) == 0 || strings.Contains(string(cipherBlob), token) {
		t.Fatalf("the token is not sealed at rest (%d bytes)", len(cipherBlob))
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("the save response echoed the token: %s", rec.Body.String())
	}

	// Saving again without the token keeps it.
	rec = f.env.doAuthed(http.MethodPut, "/v1/projects/"+f.project.ID+"/forge", map[string]any{
		"kind": "github", "api_base": srv.URL, "repo": "acme/widgets",
	}, f.creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT forge (keep): %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Project *contracts.Project `json:"project"`
	}
	f.env.mustJSON(rec, &body)
	if body.Project.Forge == nil || !body.Project.Forge.HasToken {
		t.Fatal("saving without a token cleared the saved one")
	}

	// A create call uses it, and neither the error nor the log gains the token.
	fake.createStatus = http.StatusUnprocessableEntity
	fake.createBody = `{"message":"Validation Failed"}`
	if rec := f.createPR(t, "t"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("the token was logged:\n%s", logs.String())
	}
	if !strings.Contains(fake.auth, token) {
		t.Fatalf("the forge did not receive the token: %q", fake.auth)
	}

	// An explicit empty token clears it.
	rec = f.env.doAuthed(http.MethodPut, "/v1/projects/"+f.project.ID+"/forge", map[string]any{
		"kind": "github", "api_base": srv.URL, "repo": "acme/widgets", "token": "",
	}, f.creds)
	f.env.mustJSON(rec, &body)
	if body.Project.Forge.HasToken {
		t.Fatal("an explicit empty token did not clear the saved one")
	}
}

// A forge 5xx is a gateway problem, not a validation problem: the status the
// client sees is 502 with the step and the forge's text.
func TestCreatePullRequestMapsForge5xxTo502(t *testing.T) {
	f := newPRFixture(t)
	fake := &prForge{createStatus: http.StatusInternalServerError, createBody: `{"message":"boom"}`}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	f.configureForge(t, srv)

	rec := f.createPR(t, "Server error")
	f.env.mustErr(rec, http.StatusBadGateway, contracts.ErrForge)
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("the forge text is missing: %s", rec.Body.String())
	}
}
