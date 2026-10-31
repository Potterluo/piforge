package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// Deleting a project used to destroy the sandbox on a best-effort basis and then
// delete the row anyway. On Windows the destroy fails while anything holds a file
// inside the checkout, so every such delete left `sandboxes/<user>/<project>`
// behind with no row pointing at it.
//
// These tests pin the replacement contract: the processes that hold the
// directory are stopped first, the destroy is retried, and a destroy that still
// fails makes the delete fail — with the project kept, so the row and the
// directory stay in agreement. No orphan.

// eventRecorder orders what the delete did. The components that do the work are
// separate fakes, so the shared log is the only way to assert "stop, then
// destroy" rather than "both happened".
type eventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *eventRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) index(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, got := range r.events {
		if got == event {
			return i
		}
	}
	return -1
}

// gatedSandbox delegates lifecycle calls to a real Local provider and can refuse
// Destroy, which is what a held file does on Windows without needing a platform
// specific lock.
type gatedSandbox struct {
	contracts.SandboxProvider
	rec      *eventRecorder
	fail     atomic.Bool
	attempts atomic.Int32
}

var errHeldFile = errors.New(
	"unlinkat C:\\data\\sandboxes\\usr_x\\prj_y\\repo\\SKILL.md: " +
		"The process cannot access the file because it is being used by another process")

func (g *gatedSandbox) Destroy(ctx context.Context, sandboxID string) error {
	g.attempts.Add(1)
	if g.fail.Load() {
		return errHeldFile
	}
	g.rec.add("destroy")
	return g.SandboxProvider.Destroy(ctx, sandboxID)
}

// recordingTerm records which worktree's terminals the delete closed.
type recordingTerm struct {
	rec    *eventRecorder
	closed []string
}

func (r *recordingTerm) Open(context.Context, string, string, string) (*TermSession, error) {
	return &TermSession{}, nil
}
func (r *recordingTerm) Write(string, []byte) error               { return nil }
func (r *recordingTerm) Resize(string, int, int) error            { return nil }
func (r *recordingTerm) Close(string) error                       { return nil }
func (r *recordingTerm) Read(string, int64) ([]byte, int64, bool) { return nil, 0, true }
func (r *recordingTerm) Info(string) (TermSession, bool)          { return TermSession{}, false }
func (r *recordingTerm) CloseSandbox(sandboxID string) int {
	r.closed = append(r.closed, sandboxID)
	r.rec.add("term:" + sandboxID)
	return 1
}

// heldProject wires a real local sandbox (a directory on disk) to a project and
// session, with the provider behind a gate and the terminal manager recording.
type heldProject struct {
	env      *testEnv
	user     *contracts.User
	project  *contracts.Project
	session  *contracts.Session
	worktree *contracts.Worktree
	root     string
	dir      string
	gated    *gatedSandbox
	term     *recordingTerm
	rec      *eventRecorder
}

func newHeldProject(t *testing.T) *heldProject {
	t.Helper()
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, session := env.mkSessionFor(user.ID, "held")
	ctx := context.Background()

	root := t.TempDir()
	local, err := sandbox.NewLocal(sandbox.LocalConfig{Root: root, InitialBranch: "main"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	live, err := local.Ensure(ctx, user.ID, project.ID, "", "main")
	if err != nil {
		t.Fatalf("sandbox.Ensure: %v", err)
	}
	if _, err := env.store.CreateSandbox(ctx, user.ID, store.NewSandbox{
		ID:        live.ID,
		ProjectID: project.ID,
		State:     contracts.SandboxReady,
		Provider:  "local",
		RootDir:   live.RootDir,
	}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, user.ID, store.NewWorktree{
		SandboxID: live.ID,
		Path:      filepath.Join(live.RootDir, "repo"),
		Branch:    "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, user.ID, session.ID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}

	rec := &eventRecorder{}
	gated := &gatedSandbox{SandboxProvider: local, rec: rec}
	term := &recordingTerm{rec: rec}
	env.deps.Sandbox = gated
	env.deps.Term = term
	env.rebuild()

	return &heldProject{
		env: env, user: user, project: project, session: session, worktree: wt,
		root: root, dir: filepath.Join(root, user.ID, project.ID),
		gated: gated, term: term, rec: rec,
	}
}

// A held file is a failed destroy. The delete must then refuse, keep the
// project, and say why — and it must have tried more than once.
func TestDeleteProjectKeepsTheProjectWhenTheSandboxIsHeld(t *testing.T) {
	held := newHeldProject(t)
	ctx := context.Background()
	client := held.env.login("dev@piforge.local", "pw")

	// An open handle inside the checkout is the scenario on Windows; the gate is
	// what fails the destroy here, so the test behaves the same on every OS.
	marker := filepath.Join(held.dir, "repo", "held.txt")
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open marker: %v", err)
	}
	defer func() { _ = file.Close() }()

	held.gated.fail.Store(true)
	rec := held.env.doAuthed(http.MethodDelete, "/v1/projects/"+held.project.ID, nil, client)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete with a held sandbox = %d %s, want 409", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "沙箱目录删不掉") || !strings.Contains(body, "项目已保留") {
		t.Fatalf("the refusal does not say what happened: %s", body)
	}
	if got := held.gated.attempts.Load(); got < 2 {
		t.Fatalf("destroy attempts = %d, want a retry (>= 2)", got)
	}

	// The project and its sandbox are both still there: nothing half-deleted,
	// nothing orphaned.
	if _, err := held.env.store.Project(ctx, held.user.ID, held.project.ID); err != nil {
		t.Fatalf("the project was deleted even though its sandbox could not be: %v", err)
	}
	if _, err := os.Stat(held.dir); err != nil {
		t.Fatalf("the sandbox directory was removed while the project was kept: %v", err)
	}

	// Release the holder and retry: the delete finishes, and it takes the
	// directory with it. This is what "reliable" means for the user.
	if err := file.Close(); err != nil {
		t.Fatalf("close marker: %v", err)
	}
	held.gated.fail.Store(false)
	rec = held.env.doAuthed(http.MethodDelete, "/v1/projects/"+held.project.ID, nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry delete = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if _, err := held.env.store.Project(ctx, held.user.ID, held.project.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the project survived a successful delete: %v", err)
	}
	if _, err := os.Stat(held.dir); !os.IsNotExist(err) {
		t.Fatalf("the sandbox directory is orphaned after a successful delete: %v", err)
	}
}

// The stop has to come first. If the destroy ran while the agent or a shell
// still held the checkout, the retry above would be the only thing that ever
// worked — and on a real lock it would not.
func TestDeleteProjectStopsHoldersBeforeDestroying(t *testing.T) {
	held := newHeldProject(t)
	client := held.env.login("dev@piforge.local", "pw")

	rec := held.env.doAuthed(http.MethodDelete, "/v1/projects/"+held.project.ID, nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}

	// The agent process for the session was stopped.
	held.env.runtime.mu.Lock()
	stops := append([]string(nil), held.env.runtime.stops...)
	held.env.runtime.mu.Unlock()
	if len(stops) != 1 || stops[0] != held.session.ID {
		t.Fatalf("agent stops = %v, want the project's session %s", stops, held.session.ID)
	}
	// And so were its terminals, by the key the PTY handler opens with.
	if len(held.term.closed) != 1 || held.term.closed[0] != held.worktree.ID {
		t.Fatalf("terminals closed for %v, want worktree %s", held.term.closed, held.worktree.ID)
	}
	// Ordering is the point, not just presence.
	termAt := held.rec.index("term:" + held.worktree.ID)
	destroyAt := held.rec.index("destroy")
	if termAt < 0 || destroyAt < 0 || termAt > destroyAt {
		t.Fatalf("events = %v: the terminal must be closed before the sandbox is destroyed", held.rec.events)
	}
}
