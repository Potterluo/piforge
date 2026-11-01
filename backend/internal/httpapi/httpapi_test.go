package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/reconcile"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// testEnv is one fully wired router: a real SQLite database, a real
// authenticator (bcrypt and all), a fake agent runtime and a fake workspace.
// Only the parts owned by other packages are faked, which is exactly the
// contract this package depends on.
type testEnv struct {
	t         *testing.T
	store     *store.Store
	auth      *auth.Authenticator
	runtime   *fakeRuntime
	workspace *fakeWorkspace
	deps      Deps
	handler   http.Handler
	// skillsDir is set only by tests that exercise bundle uploads.
	skillsDir string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "piforge-httpapi-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "httpapi.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		// A streaming handler may still be returning its last query when the
		// test ends; on Windows the file stays locked until it does. Retry
		// instead of failing the test on cleanup noise.
		for i := 0; i < 100; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	authenticator, err := auth.New(st, st, auth.Config{SessionTTL: time.Hour})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	skillsDir := filepath.Join(dir, "skills")
	env := &testEnv{
		t:         t,
		store:     st,
		auth:      authenticator,
		runtime:   newFakeRuntime(),
		workspace: newFakeWorkspace(),
		skillsDir: skillsDir,
	}
	// Provider keys are sealed at rest, so every test server needs a box; a
	// server without one refuses to store a key by design.
	box, err := crypto.New(bytes.Repeat([]byte{4}, crypto.KeySize))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	env.deps = Deps{
		Store:     st,
		Auth:      authenticator,
		Runtime:   env.runtime,
		Workspace: env.workspace,
		Secrets:   box,
		// The outbound URL policy, with the one exception a test server needs:
		// its fixture upstreams listen on 127.0.0.1 on an ephemeral port, which
		// the deployment default refuses. This is the same mechanism an operator
		// uses for an internal MCP server (docs/SANDBOX-DEPLOY.md §1.9) — the
		// default is untouched, and TestTheDefaultPolicyRefusesTheReportsTargets
		// in outbound_policy_test.go proves it.
		URLPolicy: testURLPolicy(),
		// Creating a skill with a body writes its bundle, so every test server
		// needs somewhere to put it.
		SkillsDir: skillsDir,
		// A `file://` import is confined to the roots the deployment wrote down.
		// The fixtures stage their repositories under the OS temp directory, so
		// that is the one root a test server declares — the same thing an
		// operator of a self-hosted install does, and still a refusal for
		// everything else on the disk.
		SkillImportRoots: []string{os.TempDir()},
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// The delete guard asks the registry before trusting a "running" row. The
	// fake answers "alive" unless a test says otherwise (see markDead), so this
	// changes no existing expectation.
	env.deps.Reconciler = reconcile.New(reconcile.Config{
		Store:   st,
		Runtime: env.runtime,
		Logger:  env.deps.Logger,
	})
	env.rebuild()
	return env
}

// newTestEnvWithSkills is newTestEnv plus a real bundle directory, for the
// upload paths that write files.
func newTestEnvWithSkills(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	env.skillsDir = filepath.Join(t.TempDir(), "skills")
	env.deps.SkillsDir = env.skillsDir
	env.rebuild()
	return env
}

// testURLPolicy allows this host's loopback, which is where every fixture
// upstream listens. Nothing else is widened: private, link-local and the docker
// names stay refused, and the deployment (no allow file) never gets this policy.
func testURLPolicy() *urlguard.Policy {
	policy, err := urlguard.New([]urlguard.Allow{{CIDR: "127.0.0.1/32"}, {CIDR: "::1/128"}})
	if err != nil {
		panic("httpapi: test URL policy: " + err.Error())
	}
	return policy
}

// rebuild re-creates the router from the current deps, which is how tests swap
// in a provisioner.
func (e *testEnv) rebuild() {
	e.handler = NewRouter(e.deps)
}

// mkUser creates an account with a real bcrypt hash and returns it.
func (e *testEnv) mkUser(email, password string, role contracts.Role) *contracts.User {
	e.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		e.t.Fatalf("HashPassword: %v", err)
	}
	u, err := e.store.CreateUser(context.Background(), store.NewUser{
		Email: email, DisplayName: email, PasswordHash: hash, Role: role, Status: "active",
	})
	if err != nil {
		e.t.Fatalf("CreateUser: %v", err)
	}
	return u
}

// mkSessionFor creates a project + session for a user and returns both.
func (e *testEnv) mkSessionFor(userID, name string) (*contracts.Project, *contracts.Session) {
	e.t.Helper()
	ctx := context.Background()
	p, err := e.store.CreateProject(ctx, userID, store.NewProject{Name: name, DefaultBranch: "main"})
	if err != nil {
		e.t.Fatalf("CreateProject: %v", err)
	}
	// A session whose model reference names a provider that does not exist cannot
	// run, and the API now says so instead of returning "internal error". Tests
	// that only care about another behaviour still need a resolvable model.
	e.ensureProvider(userID, "shared", "gpt")
	sess, err := e.store.CreateSession(ctx, userID, store.NewSession{
		ProjectID: p.ID, Kind: contracts.KindMain, Title: "hello", ModelRef: "shared/gpt",
	})
	if err != nil {
		e.t.Fatalf("CreateSession: %v", err)
	}
	return p, sess
}

// creds is everything a logged-in browser needs.
type creds struct {
	cookies []*http.Cookie
	csrf    string
	user    *contracts.User
}

// login authenticates through the HTTP endpoint so the cookie and CSRF paths
// are exercised rather than bypassed.
func (e *testEnv) login(email, password string) *creds {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/v1/auth/login", map[string]string{
		"email": email, "password": password,
	}, nil, "")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login: status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		User *contracts.User `json:"user"`
		CSRF string          `json:"csrf"`
	}
	e.mustJSON(rec, &body)
	if body.User == nil || body.CSRF == "" {
		e.t.Fatalf("login body = %s", rec.Body.String())
	}
	return &creds{cookies: rec.Result().Cookies(), csrf: body.CSRF, user: body.User}
}

// do performs one request against the router.
func (e *testEnv) do(method, path string, body any, c *creds, csrf string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		for _, ck := range c.cookies {
			req.AddCookie(ck)
		}
	}
	if csrf != "" {
		req.Header.Set(auth.HeaderCSRF, csrf)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// doAuthed is do with the session cookie and a matching CSRF header.
func (e *testEnv) doAuthed(method, path string, body any, c *creds) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(method, path, body, c, c.csrf)
}

// doBearer sends a request with an API key instead of a cookie, and deliberately
// without a CSRF header: that is the whole point of a bearer token.
func (e *testEnv) doBearer(method, path string, body any, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// doRaw sends a raw byte body (file writes).
func (e *testEnv) doRaw(method, path, contentType string, body []byte, c *creds) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c != nil {
		for _, ck := range c.cookies {
			req.AddCookie(ck)
		}
		req.Header.Set(auth.HeaderCSRF, c.csrf)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// mustJSON decodes a response body into v.
func (e *testEnv) mustJSON(rec *httptest.ResponseRecorder, v any) {
	e.t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		e.t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// mustErr decodes a response and asserts the API error code and status.
func (e *testEnv) mustErr(rec *httptest.ResponseRecorder, status int, code string) {
	e.t.Helper()
	if rec.Code != status {
		e.t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	e.mustJSON(rec, &body)
	if body.Error == nil {
		e.t.Fatalf("missing error envelope: %s", rec.Body.String())
	}
	if body.Error.Code != code {
		e.t.Fatalf("code = %q, want %q (body %s)", body.Error.Code, code, rec.Body.String())
	}
	if body.Error.Message == "" {
		e.t.Fatal("error message must not be empty")
	}
}

// logger returns a silent logger for routers built outside newTestEnv.
func (e *testEnv) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestRequest builds a cookie-bearing request for a router built without
// the env's handler.
func newTestRequest(method, path string, c *creds) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		for _, ck := range c.cookies {
			req.AddCookie(ck)
		}
		req.Header.Set(auth.HeaderCSRF, c.csrf)
	}
	return req
}

// serve runs one request through an arbitrary handler.
func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Fake agent runtime
// ---------------------------------------------------------------------------

// fakeRuntime is a scripted contracts.AgentRuntime. It records the calls it
// receives so tests can assert that e.g. an idempotent replay does not prompt
// twice.
type fakeRuntime struct {
	mu     sync.Mutex
	starts []contracts.SessionSpec
	script []contracts.Event

	prompts     []contracts.PromptRequest
	promptRes   contracts.PromptResult
	promptErr   error
	startErr    error
	interrupt   contracts.InterruptResult
	interrupted []string
	forked      []contracts.ForkRequest
	compaction  []string
	// compactErr is what the runtime answers a compact call with. The no-op path
	// (contracts.ErrNothingToCompact) is a product behaviour, not a failure.
	compactErr error
	cloned     []string

	status contracts.RuntimeStatus
	// stayRunning keeps Status().Running true after Prompt, per session, which is
	// how a test models several branches of one session running at the same time.
	stayRunning bool
	prompted    map[string]bool
	tree        []*contracts.TreeEntry
	entri       []*contracts.TreeEntry
	leaf        string
	points      []contracts.ForkPoint

	// Model-switch bookkeeping. startedModel is what the live process was
	// launched with; modelLive says whether there is one. Zero values mean "no
	// live process", which is the state most tests do not care about.
	startedModel string
	modelLive    bool
	// modelConfigMissing models an agent spawned while no provider existed:
	// the process is alive but was never given a model configuration.
	modelConfigMissing bool
	stops              []string
	// alive is the in-process registry the reconciler consults. Nil means
	// "every session is alive", which keeps the seam invisible to tests that
	// never mention it; a non-nil map answers per session.
	alive map[string]bool
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		promptRes: contracts.PromptResult{Accepted: true, Disposition: "started", RunID: "run_scripted"},
		status:    contracts.RuntimeStatus{Running: true, Streaming: true, LastSeq: 7},
		prompted:  map[string]bool{},
		tree: []*contracts.TreeEntry{
			{EntryID: "e1", Type: "message", Role: "user", Preview: "hi"},
		},
		entri: []*contracts.TreeEntry{{EntryID: "e1", Type: "message"}},
		leaf:  "e1",
		points: []contracts.ForkPoint{
			{EntryID: "e1", Preview: "hi"},
		},
	}
}

func (f *fakeRuntime) Start(_ context.Context, spec contracts.SessionSpec) (<-chan contracts.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, spec)
	if f.startErr != nil {
		return nil, f.startErr
	}
	events := make(chan contracts.Event, len(f.script)+1)
	for _, ev := range f.script {
		events <- ev
	}
	close(events)
	return events, nil
}

func (f *fakeRuntime) Stop(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, sessionID)
	f.modelLive = false
	return nil
}

// StartedModelRef is the optional capability the model-switch path needs.
func (f *fakeRuntime) StartedModelRef(_ string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.modelLive {
		return "", false
	}
	return f.startedModel, true
}

// StartedModelConfig is the second optional capability the recovery path needs.
// Unless a test says otherwise, a live process counts as configured, so every
// other test keeps its single start/stop.
func (f *fakeRuntime) StartedModelConfig(_ string) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.modelLive {
		return false, false
	}
	return !f.modelConfigMissing, true
}

// Alive is the registry the reconciler treats as authoritative. An unset map
// answers "alive" so tests that never touch the reconciler see no change.
func (f *fakeRuntime) Alive(sessionID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive == nil {
		return true
	}
	return f.alive[sessionID]
}

// markAlive / markDead say what the registry knows about one session.
func (f *fakeRuntime) markAlive(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive == nil {
		f.alive = map[string]bool{}
	}
	f.alive[sessionID] = true
}

func (f *fakeRuntime) markDead(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive == nil {
		f.alive = map[string]bool{}
	}
	f.alive[sessionID] = false
}

func (f *fakeRuntime) Prompt(_ context.Context, req contracts.PromptRequest) (contracts.PromptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, req)
	if f.prompted == nil {
		f.prompted = map[string]bool{}
	}
	f.prompted[req.SessionID] = true
	// A prompt runs and then settles. Modelling that (instead of a permanently
	// running agent) is what lets a handler ask "is a turn in flight?" — the
	// sidecar promote does, and a stuck-true fake made every such check refuse.
	f.status.Running = f.stayRunning
	f.status.Streaming = f.stayRunning
	if f.promptErr != nil {
		return contracts.PromptResult{}, f.promptErr
	}
	return f.promptRes, nil
}

func (f *fakeRuntime) Interrupt(_ context.Context, sessionID string) (contracts.InterruptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupted = append(f.interrupted, sessionID)
	return f.interrupt, nil
}

func (f *fakeRuntime) Status(_ context.Context, sessionID string) (contracts.RuntimeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	st.SessionID = sessionID
	if f.stayRunning {
		// Only the branches that were actually prompted are running.
		st.Running = f.prompted[sessionID]
		st.Streaming = f.prompted[sessionID]
	}
	return st, nil
}

func (f *fakeRuntime) Fork(_ context.Context, req contracts.ForkRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forked = append(f.forked, req)
	return nil
}

func (f *fakeRuntime) Tree(_ context.Context, _ string) ([]*contracts.TreeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tree, nil
}

func (f *fakeRuntime) Entries(_ context.Context, _, _ string) ([]*contracts.TreeEntry, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entri, f.leaf, nil
}

func (f *fakeRuntime) ForkPoints(_ context.Context, _ string) ([]contracts.ForkPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.points, nil
}

func (f *fakeRuntime) Compact(_ context.Context, _, instructions string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compaction = append(f.compaction, instructions)
	return f.compactErr
}

func (f *fakeRuntime) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

func (f *fakeRuntime) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

// ---------------------------------------------------------------------------
// Fake workspace
// ---------------------------------------------------------------------------

// fakeWorkspace is a canned contracts.Workspace that records the root it was
// asked to operate on, so tests can prove the session -> worktree resolution.
type fakeWorkspace struct {
	mu sync.Mutex

	roots []string
	tree  []*contracts.FileNode
	// includeIgnored records the last tree request's switch state.
	includeIgnored bool
	adopted        []string
	// remote is what Push/Pull report and AheadBehind splits.
	remote    contracts.RemoteResult
	ahead     int
	behind    int
	remoteErr error
	// fetches counts the ahead/behind fetch the Git tab performs, so a test can
	// prove a refused remote never reached it.
	fetches int
	content *contracts.FileContent
	// readErr fails only the text preview, the way the remote workspace fails
	// when /v1/file/read hits a binary file. OpenFile deliberately ignores it:
	// a download must not depend on the preview decode.
	readErr error
	// fileBytes is what OpenFile streams when it is set; otherwise the preview
	// body stands in for the file.
	fileBytes []byte
	status    *contracts.GitStatus
	diffs     []contracts.FileDiff
	graph     *contracts.GitGraph
	prRange   contracts.PRRange
	results   []contracts.SearchResult
	err       error

	written   map[string][]byte
	deleted   []string
	staged    [][]string
	unStaged  [][]string
	committed []contracts.CommitRequest
	created   []string
	co        []string
}

func newFakeWorkspace() *fakeWorkspace {
	return &fakeWorkspace{
		tree: []*contracts.FileNode{
			{Name: "main.go", Path: "main.go", GitState: "M", Size: 12},
		},
		content: &contracts.FileContent{
			Path: "main.go", Size: 12, Body: "package main", Language: "go",
			MimeType: "text/x-go", Modified: time.Unix(1700000000, 0),
		},
		status: &contracts.GitStatus{
			Branch: "main", Clean: false,
			Changes: []contracts.GitChange{{Path: "main.go", Worktree: "M"}},
		},
		diffs: []contracts.FileDiff{{Path: "main.go", Status: "modified", Additions: 2, Deletions: 1, Patch: "@@"}},
		graph: &contracts.GitGraph{
			Head:    "abc123",
			Commits: []contracts.GitCommit{{Hash: "abc123", Short: "abc123", Author: "dev", Subject: "init"}},
		},
		results: []contracts.SearchResult{{Path: "main.go", Line: 1, Text: "package main"}},
		written: map[string][]byte{},
	}
}

func (f *fakeWorkspace) guard(root string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, root)
	return f.err
}

func (f *fakeWorkspace) Tree(_ context.Context, root, _ string, _ int) ([]*contracts.FileNode, error) {
	if err := f.guard(root); err != nil {
		return nil, err
	}
	return f.tree, nil
}

// TreeWithOptions records that the panel's "show ignored" switch reached the
// server, which is the only way it can reveal anything.
func (f *fakeWorkspace) TreeWithOptions(_ context.Context, root, _ string, _ int, includeIgnored bool) ([]*contracts.FileNode, error) {
	f.mu.Lock()
	f.includeIgnored = includeIgnored
	f.mu.Unlock()
	if err := f.guard(root); err != nil {
		return nil, err
	}
	return f.tree, nil
}

func (f *fakeWorkspace) ReadFile(_ context.Context, root, relPath string) (*contracts.FileContent, error) {
	if err := f.guard(root); err != nil {
		return nil, err
	}
	f.mu.Lock()
	readErr := f.readErr
	c := *f.content
	f.mu.Unlock()
	if readErr != nil {
		return nil, readErr
	}
	c.Path = relPath
	return &c, nil
}

// OpenFile serves raw bytes. It never consults readErr and never truncates,
// which is what the download handler is required to rely on.
func (f *fakeWorkspace) OpenFile(_ context.Context, root, _ string) (io.ReadCloser, int64, error) {
	if err := f.guard(root); err != nil {
		return nil, -1, err
	}
	f.mu.Lock()
	raw := append([]byte(nil), f.fileBytes...)
	if f.fileBytes == nil && f.content != nil {
		raw = []byte(f.content.Body)
	}
	f.mu.Unlock()
	return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
}

func (f *fakeWorkspace) WriteFile(_ context.Context, root, relPath string, body []byte) error {
	if err := f.guard(root); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written[relPath] = append([]byte(nil), body...)
	return nil
}

func (f *fakeWorkspace) DeleteFile(_ context.Context, root, relPath string) error {
	if err := f.guard(root); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, relPath)
	return nil
}

func (f *fakeWorkspace) Search(_ context.Context, root, _ string, _ bool) ([]contracts.SearchResult, error) {
	if err := f.guard(root); err != nil {
		return nil, err
	}
	return f.results, nil
}

func (f *fakeWorkspace) Status(_ context.Context, root string) (*contracts.GitStatus, error) {
	if err := f.guard(root); err != nil {
		return nil, err
	}
	return f.status, nil
}

func (f *fakeWorkspace) Diff(_ context.Context, root string, _ contracts.DiffRequest) ([]contracts.FileDiff, error) {
	if err := f.guard(root); err != nil {
		return nil, err
	}
	return f.diffs, nil
}

func (f *fakeWorkspace) Log(_ context.Context, root string, _ int, _ string) (*contracts.GitGraph, error) {
	if err := f.guard(root); err != nil {
		return nil, err
	}
	return f.graph, nil
}

func (f *fakeWorkspace) Stage(_ context.Context, root string, paths []string) error {
	if err := f.guard(root); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged = append(f.staged, paths)
	return nil
}

func (f *fakeWorkspace) Unstage(_ context.Context, root string, paths []string) error {
	if err := f.guard(root); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unStaged = append(f.unStaged, paths)
	return nil
}

func (f *fakeWorkspace) Commit(_ context.Context, root string, req contracts.CommitRequest) (string, error) {
	if err := f.guard(root); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, req)
	return "deadbeef", nil
}

func (f *fakeWorkspace) CreateBranch(_ context.Context, root, name string) error {
	if err := f.guard(root); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, name)
	return nil
}

func (f *fakeWorkspace) Checkout(_ context.Context, root, name string) error {
	if err := f.guard(root); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.co = append(f.co, name)
	return nil
}

func (f *fakeWorkspace) lastRoot() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.roots) == 0 {
		return ""
	}
	return f.roots[len(f.roots)-1]
}

// ensureProvider makes `name` resolvable for userID, ignoring a duplicate so it
// can be called from any fixture.
func (e *testEnv) ensureProvider(userID, name, model string) {
	e.t.Helper()
	// A keyed provider, because a keyless one is now reported as "no API key"
	// when a run fails, and these fixtures are about everything else.
	_, err := e.store.CreateProvider(context.Background(), userID, store.ProviderInput{
		Name:      name,
		API:       "openai-compat",
		BaseURL:   "https://example.invalid/v1",
		Models:    []contracts.ModelSpec{{ID: model}},
		KeyCipher: []byte("sealed-test-key"),
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		e.t.Fatalf("CreateProvider: %v", err)
	}
}

// cloned records CloneSession calls, so the promote test can prove the agent side
// was seeded and not only the display side.
func (f *fakeRuntime) CloneSession(_ context.Context, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cloned = append(f.cloned, from+"->"+to)
	return nil
}

// Logs is an optional capability: the fake runtime does not keep them, and the
// endpoint must say so rather than invent an empty file.
func (f *fakeRuntime) Logs(ctx context.Context, sessionID string) (*contracts.SessionLogs, error) {
	return &contracts.SessionLogs{
		SessionID: sessionID,
		Lines:     []string{"fake: agent started", "fake: nothing to report"},
	}, nil
}

// Adopt records the hand-off so the handler test can prove which worktrees were
// involved and what message the branch commit got.
func (w *fakeWorkspace) Adopt(_ context.Context, sourceRoot, targetRoot, message string) (contracts.AdoptResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return contracts.AdoptResult{}, w.err
	}
	w.adopted = append(w.adopted, sourceRoot+"->"+targetRoot+"#"+message)
	return contracts.AdoptResult{
		Commit:      "abc1234",
		Files:       []string{"feature.txt"},
		Mode:        "merge",
		Commits:     []string{"abc1234"},
		MergeCommit: "abc1234",
	}, nil
}
func (f *fakeWorkspace) Push(_ context.Context, root string) (contracts.RemoteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, "push:"+root)
	return f.remote, f.remoteErr
}

func (f *fakeWorkspace) Pull(_ context.Context, root string) (contracts.RemoteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, "pull:"+root)
	return f.remote, f.remoteErr
}

func (f *fakeWorkspace) Fetch(context.Context, string) error {
	f.mu.Lock()
	f.fetches++
	err := f.remoteErr
	f.mu.Unlock()
	return err
}

// remoteCalls reports how many times the remote operations were forwarded, so a
// guard can be proved to run *before* the dial rather than after it (G6).
func (f *fakeWorkspace) remoteCalls() (pushes, pulls, fetches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, root := range f.roots {
		switch {
		case strings.HasPrefix(root, "push:"):
			pushes++
		case strings.HasPrefix(root, "pull:"):
			pulls++
		}
	}
	return pushes, pulls, f.fetches
}

func (f *fakeWorkspace) AheadBehind(context.Context, string) (int, int, error) {
	return f.ahead, f.behind, f.remoteErr
}

// PRRange is the canned range the PR preview reads when the fake workspace is
// in use. Tests that need real commit content swap in the gitx-backed fsx.
func (f *fakeWorkspace) PRRange(_ context.Context, root, base string) (contracts.PRRange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, "pr-range:"+root+":"+base)
	out := f.prRange
	out.Base = base
	return out, f.err
}
func (f *fakeWorkspace) StageHunk(_ context.Context, root, path string, index int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, fmt.Sprintf("stage-hunk:%s:%s:%d", root, path, index))
	return f.err
}

func (f *fakeWorkspace) UnstageHunk(_ context.Context, root, path string, index int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, fmt.Sprintf("unstage-hunk:%s:%s:%d", root, path, index))
	return f.err
}

func (f *fakeWorkspace) DiscardHunk(_ context.Context, root, path string, index int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, fmt.Sprintf("discard-hunk:%s:%s:%d", root, path, index))
	return f.err
}

func (f *fakeWorkspace) DiscardFile(_ context.Context, root, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, fmt.Sprintf("discard-file:%s:%s", root, path))
	return f.err
}
