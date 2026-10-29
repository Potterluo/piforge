package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "piforge.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkUser(t *testing.T, s *Store, email string, role contracts.Role) *contracts.User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), NewUser{
		Email: email, DisplayName: email, PasswordHash: "hash", Role: role,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", email, err)
	}
	return u
}

func mkProject(t *testing.T, s *Store, userID, name string) *contracts.Project {
	t.Helper()
	p, err := s.CreateProject(context.Background(), userID, NewProject{
		Name: name, RepoURL: "https://example.com/x.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p
}

func mkSession(t *testing.T, s *Store, userID, projectID string, kind contracts.SessionKind) *contracts.Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), userID, NewSession{
		ProjectID: projectID, Kind: kind, Title: "t", ModelRef: "p/m",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess
}

// ---------------------------------------------------------------------------
// IDs and migration
// ---------------------------------------------------------------------------

func TestNewIDIsSortableAndPrefixed(t *testing.T) {
	a := NewID("ses")
	time.Sleep(2 * time.Millisecond)
	b := NewID("ses")

	if len(a) != len("ses_")+idLen {
		t.Fatalf("len(%q) = %d", a, len(a))
	}
	if a[:4] != "ses_" {
		t.Fatalf("prefix missing: %q", a)
	}
	if a >= b {
		t.Fatalf("ids must sort by creation time: %q >= %q", a, b)
	}
	if NewID("") == NewID("") {
		t.Fatal("ids must carry randomness")
	}
}

func TestOpenCreatesEveryTable(t *testing.T) {
	s := newTestStore(t)
	want := []string{
		"users", "providers", "projects", "sandboxes", "worktrees", "sessions",
		"runs", "events", "tool_calls", "mcp_servers", "skills", "snapshots",
		"auth_sessions", "messages",
	}
	rows, err := s.r.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("missing table %q", name)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "piforge.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := first.CreateUser(context.Background(), NewUser{Email: "a@b.c"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()
	n, err := second.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if n != 1 {
		t.Fatalf("users = %d, want 1 (schema must not be wiped)", n)
	}
}

// ---------------------------------------------------------------------------
// Users / projects / sessions
// ---------------------------------------------------------------------------

func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, NewUser{
		Email: "Alice@Example.com", DisplayName: "Alice", PasswordHash: "h",
		Role: contracts.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("email = %q, want normalized", u.Email)
	}
	if u.Role != contracts.RoleAdmin || u.Status != "active" {
		t.Fatalf("user = %+v", u)
	}

	// Case-insensitive uniqueness.
	if _, err := s.CreateUser(ctx, NewUser{Email: "ALICE@example.com"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate email: err = %v, want ErrConflict", err)
	}

	got, hash, err := s.UserByEmail(ctx, "alice@EXAMPLE.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if got.ID != u.ID || hash != "h" {
		t.Fatalf("got %q hash %q", got.ID, hash)
	}
	if _, _, err := s.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: err = %v", err)
	}

	if err := s.SetUserStatus(ctx, u.ID, "disabled"); err != nil {
		t.Fatalf("SetUserStatus: %v", err)
	}
	if err := s.SetUserStatus(ctx, u.ID, "bogus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: err = %v", err)
	}
	if err := s.SetPasswordHash(ctx, u.ID, "h2"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	if err := s.SetPasswordHash(ctx, "usr_missing", "h2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user update: err = %v", err)
	}
}

func TestProjectsAndSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)

	p := mkProject(t, s, u.ID, "repo")
	if _, err := s.CreateProject(ctx, u.ID, NewProject{Name: "repo"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate project: err = %v", err)
	}

	sess := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	if sess.RootID != sess.ID {
		t.Fatalf("main session root = %q, want self %q", sess.RootID, sess.ID)
	}

	// A project with a pending sandbox and two sessions decorates the list.
	if _, err := s.CreateSandbox(ctx, u.ID, NewSandbox{ProjectID: p.ID, State: contracts.SandboxReady}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	projects, err := s.ListProjects(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %d", len(projects))
	}
	if projects[0].SandboxState != contracts.SandboxReady {
		t.Fatalf("sandbox state = %q", projects[0].SandboxState)
	}
	if projects[0].SessionCount != 1 {
		t.Fatalf("session count = %d", projects[0].SessionCount)
	}

	// Patch round-trip.
	title := "renamed"
	status := contracts.StatusArchived
	patched, err := s.UpdateSession(ctx, u.ID, sess.ID, SessionPatch{Title: &title, Status: &status})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if patched.Title != "renamed" || patched.Status != contracts.StatusArchived {
		t.Fatalf("patched = %+v", patched)
	}

	// Filtering.
	time.Sleep(2 * time.Millisecond)
	other := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	time.Sleep(2 * time.Millisecond)
	_ = other
	latest := mkSession(t, s, u.ID, p.ID, contracts.KindSidecar)
	if err := s.TouchSession(ctx, u.ID, latest.ID); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	list, err := s.ListSessions(ctx, u.ID, SessionFilter{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("sessions = %d, want 3", len(list))
	}
	if list[0].ID != latest.ID {
		t.Fatalf("newest first: got %q want %q", list[0].ID, latest.ID)
	}

	page1, err := s.ListSessions(ctx, u.ID, SessionFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListSessions page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 = %d", len(page1))
	}
	page2, err := s.ListSessions(ctx, u.ID, SessionFilter{Limit: 2, Cursor: page1[1].ID})
	if err != nil {
		t.Fatalf("ListSessions page2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID == page1[0].ID || page2[0].ID == page1[1].ID {
		t.Fatalf("page2 = %+v", page2)
	}

	if _, err := s.ListSessions(ctx, u.ID, SessionFilter{Cursor: "ses_missing"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor: err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Isolation: the core security property
// ---------------------------------------------------------------------------

func TestCrossUserIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	alice := mkUser(t, s, "alice@example.com", contracts.RoleUser)
	bob := mkUser(t, s, "bob@example.com", contracts.RoleUser)

	aliceProject := mkProject(t, s, alice.ID, "alice-repo")
	aliceSession := mkSession(t, s, alice.ID, aliceProject.ID, contracts.KindMain)
	if _, err := s.AppendEvent(ctx, alice.ID, &contracts.Event{
		SessionID: aliceSession.ID, Type: contracts.EvMsgFinal,
		Payload: json.RawMessage(`{"role":"user","text":"secret"}`),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Bob must not be able to reach Alice's project, session or events. Every
	// one of these has to look like "does not exist", never "forbidden".
	if _, err := s.Project(ctx, bob.ID, aliceProject.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Project: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Session(ctx, bob.ID, aliceSession.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Session: err = %v, want ErrNotFound", err)
	}
	if _, err := s.ListEvents(ctx, bob.ID, aliceSession.ID, 0, 50); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListEvents: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.EventSeqRange(ctx, bob.ID, aliceSession.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("EventSeqRange: err = %v, want ErrNotFound", err)
	}
	if _, err := s.SessionStats(ctx, bob.ID, aliceSession.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("SessionStats: err = %v, want ErrNotFound", err)
	}
	title := "hacked"
	if _, err := s.UpdateSession(ctx, bob.ID, aliceSession.ID, SessionPatch{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSession: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.SaveMessage(ctx, bob.ID, aliceSession.ID, "cm_x", "hi", contracts.PromptResult{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SaveMessage: err = %v, want ErrNotFound", err)
	}
	if _, err := s.AppendEvent(ctx, bob.ID, &contracts.Event{SessionID: aliceSession.ID, Type: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("AppendEvent: err = %v, want ErrNotFound", err)
	}
	if _, err := s.PruneEvents(ctx, bob.ID, aliceSession.ID, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("PruneEvents: err = %v, want ErrNotFound", err)
	}

	// Bob's own listings must be empty, not "everything".
	if projects, err := s.ListProjects(ctx, bob.ID); err != nil || len(projects) != 0 {
		t.Errorf("ListProjects = %d, %v", len(projects), err)
	}
	if sessions, err := s.ListSessions(ctx, bob.ID, SessionFilter{}); err != nil || len(sessions) != 0 {
		t.Errorf("ListSessions = %d, %v", len(sessions), err)
	}

	// Alice can still read her own data.
	if events, err := s.ListEvents(ctx, alice.ID, aliceSession.ID, 0, 50); err != nil || len(events) != 1 {
		t.Fatalf("owner ListEvents = %d, %v", len(events), err)
	}
}

func TestAdminPathsRequireAdmin(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	admin := mkUser(t, s, "admin@example.com", contracts.RoleAdmin)
	user := mkUser(t, s, "dev@example.com", contracts.RoleUser)

	if _, err := s.AdminListUsers(ctx, user.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin AdminListUsers: err = %v, want ErrForbidden", err)
	}
	users, err := s.AdminListUsers(ctx, admin.ID)
	if err != nil {
		t.Fatalf("AdminListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d, want 2", len(users))
	}

	shared := ProviderInput{Name: "shared", API: "openai-compat", Models: []contracts.ModelSpec{{ID: "m1"}}}
	if _, err := s.UpsertAdminProvider(ctx, user.ID, shared); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin UpsertAdminProvider: err = %v", err)
	}
	if _, err := s.UpsertAdminProvider(ctx, admin.ID, shared); err != nil {
		t.Fatalf("UpsertAdminProvider: %v", err)
	}
	// Upsert must update in place, not accumulate.
	if _, err := s.UpsertAdminProvider(ctx, admin.ID, shared); err != nil {
		t.Fatalf("second UpsertAdminProvider: %v", err)
	}
	admins, err := s.AdminListProviders(ctx, admin.ID)
	if err != nil {
		t.Fatalf("AdminListProviders: %v", err)
	}
	if len(admins) != 1 {
		t.Fatalf("shared providers = %d, want 1", len(admins))
	}

	// Every user sees the shared provider as inherited.
	inherited, err := s.InheritedProviders(ctx, user.ID)
	if err != nil {
		t.Fatalf("InheritedProviders: %v", err)
	}
	if len(inherited) != 1 || inherited[0].Source != contracts.SourceInherited {
		t.Fatalf("inherited = %+v", inherited)
	}
	if inherited[0].Models[0].ID != "m1" {
		t.Fatalf("models were not round-tripped: %+v", inherited[0].Models)
	}

	// A user provider is not visible to anybody else.
	mine, err := s.CreateProvider(ctx, user.ID, ProviderInput{Name: "mine", API: "openai-compat", KeyCipher: []byte("cipher")})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if !mine.HasKey || mine.Source != contracts.SourceCustom {
		t.Fatalf("provider = %+v", mine)
	}
	if _, err := s.CreateProvider(ctx, user.ID, ProviderInput{Name: "mine", API: "openai-compat"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate provider: err = %v", err)
	}
	if other, _ := s.ListProviders(ctx, admin.ID); len(other) != 0 {
		t.Fatalf("admin sees %d of the user's providers", len(other))
	}
	if err := s.DeleteProvider(ctx, admin.ID, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user delete: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProvider(ctx, user.ID, mine.ID); err != nil {
		t.Fatalf("own delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Events: sequence allocation, replay, gap
// ---------------------------------------------------------------------------

func TestEventSequenceIsMonotonicUnderConcurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	sess := mkSession(t, s, u.ID, p.ID, contracts.KindMain)

	const workers, perWorker = 8, 25
	got := make([]int64, 0, workers*perWorker)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				ev, err := s.AppendEvent(ctx, u.ID, &contracts.Event{
					SessionID: sess.ID,
					Type:      contracts.EvMsgDelta,
					Payload:   json.RawMessage(`{"n":1}`),
				})
				if err != nil {
					t.Errorf("AppendEvent: %v", err)
					return
				}
				mu.Lock()
				got = append(got, ev.Seq)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if len(got) != workers*perWorker {
		t.Fatalf("seqs = %d, want %d", len(got), workers*perWorker)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, seq := range got {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, want %d (duplicates or holes)", i, seq, i+1)
		}
	}

	// And the stored rows agree.
	minSeq, maxSeq, err := s.EventSeqRange(ctx, u.ID, sess.ID)
	if err != nil {
		t.Fatalf("EventSeqRange: %v", err)
	}
	if minSeq != 1 || maxSeq != int64(workers*perWorker) {
		t.Fatalf("range = [%d,%d]", minSeq, maxSeq)
	}
}

func TestEventReplayAndGap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	sess := mkSession(t, s, u.ID, p.ID, contracts.KindMain)

	var seqs []int64
	for i := 0; i < 5; i++ {
		ev, err := s.AppendEvent(ctx, u.ID, &contracts.Event{
			SessionID: sess.ID,
			Type:      contracts.EvMsgDelta,
			Payload:   json.RawMessage(`{"chunk":"x"}`),
		})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		seqs = append(seqs, ev.Seq)
	}

	// Full replay.
	all, err := s.ListEvents(ctx, u.ID, sess.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("events = %d", len(all))
	}
	if all[0].Kind != contracts.KindMain {
		t.Fatalf("kind = %q, want main (joined from the session)", all[0].Kind)
	}
	if all[0].BranchID != sess.ID {
		t.Fatalf("branch id = %q", all[0].BranchID)
	}

	// Replay from Last-Event-ID.
	tail, err := s.ListEvents(ctx, u.ID, sess.ID, seqs[2], 100)
	if err != nil {
		t.Fatalf("ListEvents tail: %v", err)
	}
	if len(tail) != 2 || tail[0].Seq != seqs[3] {
		t.Fatalf("tail = %+v", tail)
	}

	// Bounded pages.
	page, err := s.ListEvents(ctx, u.ID, sess.ID, 0, 2)
	if err != nil || len(page) != 2 {
		t.Fatalf("page = %d, %v", len(page), err)
	}

	// Pruning the head creates the replay gap the SSE handler reports.
	if n, err := s.PruneEvents(ctx, u.ID, sess.ID, 3); err != nil || n != 3 {
		t.Fatalf("PruneEvents = %d, %v", n, err)
	}
	minSeq, maxSeq, err := s.EventSeqRange(ctx, u.ID, sess.ID)
	if err != nil {
		t.Fatalf("EventSeqRange: %v", err)
	}
	if minSeq != 4 || maxSeq != 5 {
		t.Fatalf("range after prune = [%d,%d]", minSeq, maxSeq)
	}
}

func TestEventsRequireValidPayload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	sess := mkSession(t, s, u.ID, p.ID, contracts.KindMain)

	if _, err := s.AppendEvent(ctx, u.ID, &contracts.Event{
		SessionID: sess.ID, Type: "x", Payload: json.RawMessage(`{not json`),
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid payload: err = %v", err)
	}
	if _, err := s.AppendEvent(ctx, u.ID, &contracts.Event{SessionID: sess.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing type: err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Messages, runs, stats
// ---------------------------------------------------------------------------

func TestMessageIdempotency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	sess := mkSession(t, s, u.ID, p.ID, contracts.KindMain)

	first, created, err := s.SaveMessage(ctx, u.ID, sess.ID, "cm_1", "hello",
		contracts.PromptResult{Accepted: true, Disposition: "started", RunID: "run_1"})
	if err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if !created {
		t.Fatal("first submit must create the record")
	}

	// Replaying returns the original result and does not create a second row.
	again, created, err := s.SaveMessage(ctx, u.ID, sess.ID, "cm_1", "hello",
		contracts.PromptResult{Accepted: true, Disposition: "queued", RunID: "run_2"})
	if err != nil {
		t.Fatalf("SaveMessage replay: %v", err)
	}
	if created {
		t.Fatal("replay must not create a record")
	}
	if again.ID != first.ID || again.Disposition != "started" || again.RunID != "run_1" {
		t.Fatalf("replay returned %+v, want the original %+v", again, first)
	}

	if _, _, err := s.SaveMessage(ctx, u.ID, sess.ID, "", "hello", contracts.PromptResult{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty client_msg_id: err = %v", err)
	}
	if _, err := s.Message(ctx, u.ID, sess.ID, "cm_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing message: err = %v", err)
	}
}

func TestRunsAndStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	sess := mkSession(t, s, u.ID, p.ID, contracts.KindMain)

	run, err := s.CreateRun(ctx, u.ID, NewRun{SessionID: sess.ID, Trigger: "user"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.Status != "started" {
		t.Fatalf("run status = %q", run.Status)
	}
	if err := s.SettleRun(ctx, u.ID, run.ID, RunSettle{
		TokensIn: 100, TokensOut: 40, TokensCacheR: 10, CostUSD: 0.0125,
	}); err != nil {
		t.Fatalf("SettleRun: %v", err)
	}
	if err := s.SettleRun(ctx, u.ID, run.ID, RunSettle{Status: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad settle status: err = %v", err)
	}

	if _, _, err := s.SaveMessage(ctx, u.ID, sess.ID, "cm_1", "hello", contracts.PromptResult{Accepted: true}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if _, err := s.AppendEvent(ctx, u.ID, &contracts.Event{
		SessionID: sess.ID, Type: contracts.EvMsgFinal,
		Payload: json.RawMessage(`{"role":"assistant"}`),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := s.AppendEvent(ctx, u.ID, &contracts.Event{
		SessionID: sess.ID, Type: contracts.EvContextUsage,
		Payload: json.RawMessage(`{"context_tokens":900,"context_window":200000}`),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := s.CreateToolCall(ctx, u.ID, ToolCall{ID: "call_1", SessionID: sess.ID, Name: "bash"}); err != nil {
		t.Fatalf("CreateToolCall: %v", err)
	}
	exit := 0
	dur := 42
	if err := s.FinishToolCall(ctx, u.ID, "call_1", ToolCallFinish{
		IsError: false, ExitCode: &exit, DurationMS: &dur,
	}); err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}
	if calls, err := s.ListToolCalls(ctx, u.ID, sess.ID); err != nil || len(calls) != 1 || calls[0].ExitCode == nil {
		t.Fatalf("tool calls = %d, %v", len(calls), err)
	}

	st, err := s.SessionStats(ctx, u.ID, sess.ID)
	if err != nil {
		t.Fatalf("SessionStats: %v", err)
	}
	if st.UserMessages != 1 || st.AssistantMsgs != 1 || st.ToolCalls != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TokensIn != 100 || st.TokensOut != 40 || st.CostUSD != 0.0125 {
		t.Fatalf("token totals = %+v", st)
	}

	tok, window, ok, err := s.LastContextUsage(ctx, u.ID, sess.ID)
	if err != nil || !ok || tok != 900 || window != 200000 {
		t.Fatalf("context usage = %d/%d ok=%v err=%v", tok, window, ok, err)
	}

	runs, err := s.ListRuns(ctx, u.ID, sess.ID)
	if err != nil || len(runs) != 1 || runs[0].SettledAt == nil {
		t.Fatalf("runs = %d, %v", len(runs), err)
	}
}

// ---------------------------------------------------------------------------
// Sandboxes, worktrees, auth sessions, seed
// ---------------------------------------------------------------------------

func TestSandboxAndWorktree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")

	sb, err := s.CreateSandbox(ctx, u.ID, NewSandbox{ProjectID: p.ID, Image: "base:1", RootDir: "/tmp/x"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if sb.State != contracts.SandboxCreating {
		t.Fatalf("state = %q", sb.State)
	}
	// One sandbox per project.
	if _, err := s.CreateSandbox(ctx, u.ID, NewSandbox{ProjectID: p.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second sandbox: err = %v", err)
	}
	if got, err := s.SandboxForProject(ctx, u.ID, p.ID); err != nil || got.ID != sb.ID {
		t.Fatalf("SandboxForProject = %v", err)
	}
	if err := s.UpdateSandboxState(ctx, u.ID, sb.ID, contracts.SandboxPaused, "idle"); err != nil {
		t.Fatalf("UpdateSandboxState: %v", err)
	}

	wt, err := s.CreateWorktree(ctx, u.ID, NewWorktree{SandboxID: sb.ID, Path: "/tmp/x/main", Branch: "main", BaseCommit: "abc"})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if wts, err := s.ListWorktrees(ctx, u.ID, sb.ID); err != nil || len(wts) != 1 {
		t.Fatalf("worktrees = %d, %v", len(wts), err)
	}
	if got, err := s.Worktree(ctx, u.ID, wt.ID); err != nil || got.Path != "/tmp/x/main" {
		t.Fatalf("Worktree = %v", err)
	}

	snap, err := s.CreateSnapshot(ctx, u.ID, Snapshot{SandboxID: sb.ID, ContentHash: "h"})
	if err != nil || snap.Kind != "fs" {
		t.Fatalf("CreateSnapshot = %v", err)
	}
	if snaps, err := s.ListSnapshots(ctx, u.ID, sb.ID); err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots = %d, %v", len(snaps), err)
	}
}

func TestSandboxIdleQueriesSeparateActivityFromReaperWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "idle@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "idle")
	sb, err := s.CreateSandbox(ctx, u.ID, NewSandbox{ProjectID: p.ID, State: contracts.SandboxReady})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	created := sb.LastActiveAt

	// A message (real activity) moves the clock forward.
	time.Sleep(5 * time.Millisecond)
	if err := s.TouchSandbox(ctx, u.ID, sb.ID); err != nil {
		t.Fatalf("TouchSandbox: %v", err)
	}
	touched, err := s.Sandbox(ctx, u.ID, sb.ID)
	if err != nil {
		t.Fatalf("Sandbox: %v", err)
	}
	if !touched.LastActiveAt.After(created) {
		t.Fatalf("last_active_at = %v, want after %v", touched.LastActiveAt, created)
	}

	// ReapableSandboxes is state-scoped and strict about the cutoff.
	if got, err := s.ReapableSandboxes(ctx, contracts.SandboxReady, time.Now().Add(time.Hour)); err != nil || len(got) != 1 {
		t.Fatalf("reapable far future = %d, %v", len(got), err)
	}
	if got, err := s.ReapableSandboxes(ctx, contracts.SandboxReady, time.Now().Add(-time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("reapable far past = %d, %v", len(got), err)
	}
	if got, err := s.ReapableSandboxes(ctx, contracts.SandboxPaused, time.Now().Add(time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("reapable wrong state = %d, %v", len(got), err)
	}

	// The reaper's transition must not refresh last_active_at, or the reclaim
	// countdown would restart on every pass.
	if err := s.UpdateSandboxStateIdle(ctx, u.ID, sb.ID, contracts.SandboxPaused, "idle pause"); err != nil {
		t.Fatalf("UpdateSandboxStateIdle: %v", err)
	}
	paused, err := s.Sandbox(ctx, u.ID, sb.ID)
	if err != nil {
		t.Fatalf("Sandbox after pause: %v", err)
	}
	if paused.State != contracts.SandboxPaused {
		t.Errorf("state = %q, want PAUSED", paused.State)
	}
	if !paused.LastActiveAt.Equal(touched.LastActiveAt) {
		t.Errorf("reaper write moved last_active_at: %v vs %v", paused.LastActiveAt, touched.LastActiveAt)
	}
}

func TestAuthSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com", contracts.RoleUser)

	expires := time.Now().Add(time.Hour)
	if err := s.CreateAuthSession(ctx, u.ID, "hash-a", expires); err != nil {
		t.Fatalf("CreateAuthSession: %v", err)
	}
	got, err := s.AuthSessionUser(ctx, "hash-a", time.Now())
	if err != nil || got.ID != u.ID {
		t.Fatalf("AuthSessionUser = %v, %v", got, err)
	}
	// Expired tokens are invisible.
	if _, err := s.AuthSessionUser(ctx, "hash-a", time.Now().Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired lookup: err = %v", err)
	}
	if err := s.CreateAuthSession(ctx, u.ID, "hash-b", expires); err != nil {
		t.Fatalf("CreateAuthSession b: %v", err)
	}
	if err := s.DeleteAuthSession(ctx, "hash-a"); err != nil {
		t.Fatalf("DeleteAuthSession: %v", err)
	}
	if _, err := s.AuthSessionUser(ctx, "hash-a", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still resolves")
	}
	if n, err := s.DeleteExpiredAuthSessions(ctx, time.Now().Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredAuthSessions = %d, %v", n, err)
	}
}

func TestEnsureAdminSeed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	hasher := func(p string) (string, error) { return "hashed:" + p, nil }
	res, err := s.EnsureAdminSeed(ctx, SeedOptions{
		AdminEmail:    "admin@piforge.local",
		AdminPassword: "piforge-admin-123",
		DemoEmail:     "dev@piforge.local",
		DemoPassword:  "piforge-dev-123",
		HashPassword:  hasher,
		SharedProvider: &ProviderInput{
			Name: "shared", API: "openai-compat", Models: []contracts.ModelSpec{{ID: "gpt"}},
		},
	})
	if err != nil {
		t.Fatalf("EnsureAdminSeed: %v", err)
	}
	if !res.Created || res.Admin.Role != contracts.RoleAdmin || res.Demo.Role != contracts.RoleUser {
		t.Fatalf("seed result = %+v", res)
	}
	if res.AdminPassword != "piforge-admin-123" || res.DemoPassword != "piforge-dev-123" {
		t.Fatalf("passwords = %q / %q", res.AdminPassword, res.DemoPassword)
	}
	_, hash, err := s.UserByEmail(ctx, "admin@piforge.local")
	if err != nil || hash != "hashed:piforge-admin-123" {
		t.Fatalf("admin hash = %q, %v", hash, err)
	}
	if res.SharedProvider == nil || res.SharedProvider.Source != contracts.SourceInherited {
		t.Fatalf("shared provider = %+v", res.SharedProvider)
	}

	// Idempotent: a second call must not create anything.
	again, err := s.EnsureAdminSeed(ctx, SeedOptions{HashPassword: hasher})
	if err != nil {
		t.Fatalf("second EnsureAdminSeed: %v", err)
	}
	if again.Created {
		t.Fatal("seed must be idempotent")
	}
	if n, _ := s.CountUsers(ctx); n != 2 {
		t.Fatalf("users = %d, want 2", n)
	}

	// Without a hasher the seed refuses rather than storing a plaintext password.
	if _, err := s.EnsureAdminSeed(ctx, SeedOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing hasher: err = %v", err)
	}
}
