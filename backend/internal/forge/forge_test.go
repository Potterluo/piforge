package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeForge is the forge the tests point at. It implements the two endpoints
// this product uses and records every request shape, so the assertions are
// about what actually crossed the wire rather than about a function call.
type fakeForge struct {
	t *testing.T

	mu sync.Mutex
	// open is what GET .../pulls returns.
	open []map[string]any
	// openAfterPost hides `open` from the pre-check and reveals it once a POST
	// has happened — the duplicate race, deterministically.
	openAfterPost bool
	// gitlab makes POST answer with GitLab's merge-request shape.
	gitlab bool
	// createStatus/body override the create response (0 = success).
	createStatus int
	createBody   string
	// failFirstCreate makes the first POST fail with createStatus and later
	// ones succeed.
	failFirstCreate bool

	gets      int
	posts     int
	lastQuery string
	lastPath  string
	lastAuth  string
	lastPRIV  string
	created   map[string]string
	listed    bool
}

func newFakeForge(t *testing.T) (*fakeForge, *httptest.Server) {
	t.Helper()
	f := &fakeForge{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeForge) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastPath = r.URL.EscapedPath()
	f.lastQuery = r.URL.RawQuery
	f.lastAuth = r.Header.Get("Authorization")
	f.lastPRIV = r.Header.Get("PRIVATE-TOKEN")

	switch r.Method {
	case http.MethodGet:
		f.gets++
		f.listed = true
		if f.openAfterPost && f.posts == 0 {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode(f.open)
	case http.MethodPost:
		f.posts++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("create body is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.created = body
		if f.createStatus != 0 && !(f.failFirstCreate && f.posts > 1) {
			w.WriteHeader(f.createStatus)
			w.Write([]byte(f.createBody))
			return
		}
		w.WriteHeader(http.StatusCreated)
		if f.gitlab {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iid":           7,
				"web_url":       "https://gitlab.example/group/sub/proj/-/merge_requests/7",
				"title":         body["title"],
				"source_branch": body["source_branch"],
				"target_branch": body["target_branch"],
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number":   7,
			"html_url": "https://forge.example/acme/widgets/pull/7",
			"title":    body["title"],
			"head":     map[string]string{"ref": strings.TrimPrefix(body["head"], "acme:")},
			"base":     map[string]string{"ref": body["base"]},
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	c, err := New(Config{
		Kind:    "github",
		APIBase: srv.URL,
		Repo:    "acme/widgets",
		Token:   token,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// FR-12.6: the create call carries the title/body a person typed, the branch
// and base the server computed, and the token as a header — never in the URL.
func TestCreateSendsTheExpectedRequest(t *testing.T) {
	fake, srv := newFakeForge(t)
	client := newClient(t, srv, "sekret-token")

	pr, err := client.Create(context.Background(), CreateInput{
		Title: "Add cache eviction",
		Body:  "Fixes the leak.\n\nSecond line.",
		Head:  "piforge/fix-cache-1a2b3c",
		Base:  "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pr.Number != 7 || pr.URL == "" || pr.Existing {
		t.Fatalf("pull request = %+v", pr)
	}
	if fake.created["title"] != "Add cache eviction" {
		t.Errorf("title = %q", fake.created["title"])
	}
	if fake.created["body"] != "Fixes the leak.\n\nSecond line." {
		t.Errorf("body = %q", fake.created["body"])
	}
	if fake.created["head"] != "acme:piforge/fix-cache-1a2b3c" {
		t.Errorf("head = %q, want the owner-qualified ref", fake.created["head"])
	}
	if fake.created["base"] != "main" {
		t.Errorf("base = %q", fake.created["base"])
	}
	if fake.lastAuth != "Bearer sekret-token" {
		t.Errorf("Authorization = %q", fake.lastAuth)
	}
	if !fake.listed {
		t.Error("the client created without first asking whether a PR already existed")
	}
	// The token must not be able to leak through a URL: query strings end up in
	// access logs and error messages.
	if strings.Contains(fake.lastQuery, "sekret-token") {
		t.Errorf("the token appeared in the query string: %s", fake.lastQuery)
	}
	if fake.lastPath != "/repos/acme/widgets/pulls" {
		t.Errorf("path = %q", fake.lastPath)
	}
}

// Idempotency: an open PR for the branch is returned as-is, and no second one
// is created.
func TestCreateReturnsTheExistingPullRequest(t *testing.T) {
	fake, srv := newFakeForge(t)
	fake.open = []map[string]any{{
		"number":   12,
		"html_url": "https://forge.example/acme/widgets/pull/12",
		"title":    "already here",
		"head":     map[string]string{"ref": "piforge/fix"},
		"base":     map[string]string{"ref": "main"},
	}}
	client := newClient(t, srv, "tok")

	pr, err := client.Create(context.Background(), CreateInput{Title: "different", Head: "piforge/fix", Base: "main"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !pr.Existing || pr.Number != 12 || pr.URL != "https://forge.example/acme/widgets/pull/12" {
		t.Fatalf("existing PR = %+v", pr)
	}
	if fake.posts != 0 {
		t.Fatalf("a second PR was created (%d POSTs)", fake.posts)
	}
	// The lookup asks for this branch, not for every open PR.
	if !strings.Contains(fake.lastQuery, "head=acme%3Apiforge%2Ffix") ||
		!strings.Contains(fake.lastQuery, "base=main") {
		t.Fatalf("lookup query = %q", fake.lastQuery)
	}
}

// A 422 "already exists" that loses the pre-check race is resolved by asking
// again, not surfaced as a failure.
func TestCreateRecoversFromADuplicate422(t *testing.T) {
	fake, srv := newFakeForge(t)
	fake.createStatus = http.StatusUnprocessableEntity
	fake.createBody = `{"message":"A pull request already exists for acme:piforge/fix."}`
	fake.failFirstCreate = true
	fake.openAfterPost = true
	fake.open = []map[string]any{{
		"number": 3, "html_url": "https://forge.example/acme/widgets/pull/3",
		"title": "dup", "head": map[string]string{"ref": "piforge/fix"},
		"base": map[string]string{"ref": "main"},
	}}
	client := newClient(t, srv, "tok")

	pr, err := client.Create(context.Background(), CreateInput{Title: "t", Head: "piforge/fix", Base: "main"})
	if err != nil {
		t.Fatalf("duplicate recovery: %v", err)
	}
	if pr.Number != 3 || !pr.Existing {
		t.Fatalf("recovered PR = %+v", pr)
	}
	if fake.gets != 2 {
		t.Fatalf("lookup count = %d, want the pre-check plus one recovery", fake.gets)
	}
}

// When the recovery lookup finds nothing, the forge's own refusal is returned
// rather than an invented "already exists".
func TestCreateKeepsTheRefusalWhenNoDuplicateExists(t *testing.T) {
	fake, srv := newFakeForge(t)
	fake.createStatus = http.StatusUnprocessableEntity
	fake.createBody = `{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"base"}]}`
	client := newClient(t, srv, "tok")

	_, err := client.Create(context.Background(), CreateInput{Title: "t", Head: "piforge/fix", Base: "nope"})
	if err == nil {
		t.Fatal("a 422 was reported as success")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not an APIError: %v", err)
	}
	if apiErr.Status != 422 {
		t.Fatalf("status = %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "Validation Failed") || !strings.Contains(apiErr.Message, "base") {
		t.Fatalf("the forge text was not preserved: %q", apiErr.Message)
	}
	if strings.Contains(err.Error(), "tok") {
		t.Fatalf("the token leaked into the error: %v", err)
	}
}

// Gitea and GitLab answer with 409 for a duplicate; the same recovery applies.
func TestCreateRecoversFromAConflict(t *testing.T) {
	fake, srv := newFakeForge(t)
	fake.createStatus = http.StatusConflict
	fake.createBody = `{"message":"pull request already exists"}`
	fake.failFirstCreate = true
	fake.openAfterPost = true
	fake.open = []map[string]any{{
		"number": 9, "html_url": "https://forge.example/acme/widgets/pull/9",
		"title": "dup", "head": map[string]string{"ref": "piforge/fix"},
		"base": map[string]string{"ref": "main"},
	}}
	client := newClient(t, srv, "tok")
	pr, err := client.Create(context.Background(), CreateInput{Title: "t", Head: "piforge/fix", Base: "main"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pr.Number != 9 || !pr.Existing {
		t.Fatalf("PR = %+v", pr)
	}
}

// GitLab is the second dialect: PRIVATE-TOKEN, merge_requests and its own field
// names. The test exists so "supported" is a verified claim, not a comment.
func TestGitLabDialect(t *testing.T) {
	fake, srv := newFakeForge(t)
	fake.gitlab = true
	client, err := New(Config{
		Kind: "gitlab", APIBase: srv.URL + "/api/v4", Repo: "group/sub/proj",
		Token: "glpat", HTTP: srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pr, err := client.Create(context.Background(), CreateInput{Title: "t", Body: "b", Head: "feat", Base: "main"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pr.Number != 7 || pr.URL == "" {
		t.Fatalf("PR = %+v", pr)
	}
	if fake.lastPRIV != "glpat" {
		t.Fatalf("PRIVATE-TOKEN = %q", fake.lastPRIV)
	}
	if fake.lastAuth != "" {
		t.Fatalf("GitLab request also sent Authorization: %q", fake.lastAuth)
	}
	if fake.created["source_branch"] != "feat" || fake.created["target_branch"] != "main" ||
		fake.created["description"] != "b" {
		t.Fatalf("GitLab body = %#v", fake.created)
	}
	if fake.lastPath != "/api/v4/projects/group%2Fsub%2Fproj/merge_requests" {
		t.Fatalf("GitLab path = %q (the project id must be one URL-encoded segment)", fake.lastPath)
	}
}

// Configuration is validated when it is saved, so a typo cannot be discovered
// only after a push.
func TestNewRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no base", Config{Kind: "github", Repo: "a/b", Token: "t"}, "api_base is required"},
		{"bad scheme", Config{Kind: "github", APIBase: "ftp://x", Repo: "a/b"}, "http or https"},
		{"credentials in URL", Config{Kind: "github", APIBase: "https://u:p@x", Repo: "a/b"}, "must not embed credentials"},
		{"query", Config{Kind: "github", APIBase: "https://x/api?token=1", Repo: "a/b"}, "query or fragment"},
		{"no repo", Config{Kind: "github", APIBase: "https://x"}, "repo is required"},
		{"github nested repo", Config{Kind: "github", APIBase: "https://x", Repo: "a/b/c"}, "owner/name"},
		{"unknown kind", Config{Kind: "bitbucket", APIBase: "https://x", Repo: "a/b"}, "unknown kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatal("accepted an invalid configuration")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
		})
	}
}
