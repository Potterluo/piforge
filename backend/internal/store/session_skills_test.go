package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mustUser and mustSession are local aliases for this file: the store's other
// tests build contracts.Project/Session values, while these tests only need ids.

func mustUser(t *testing.T, st *Store, email string) string {
	t.Helper()
	user, err := st.CreateUser(context.Background(), NewUser{
		Email: email, DisplayName: email, PasswordHash: "x", Role: "user", Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user.ID
}

func mustSession(t *testing.T, st *Store, userID, name string) string {
	t.Helper()
	project, err := st.CreateProject(context.Background(), userID, NewProject{Name: name, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sess, err := st.CreateSession(context.Background(), userID, NewSession{
		ProjectID: project.ID, Title: name,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess.ID
}

// TestSessionSkillRecordIsWriteOnce is the FR-14.5 invariant: the record of what
// a session used is a fact, not a join. Recording the same skill again after it
// was edited must keep the first version and hash.
func TestSessionSkillRecordIsWriteOnce(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	userID := mustUser(t, st, "owner@example.test")
	sessionID := mustSession(t, st, userID, "versioned")

	written, err := st.RecordSessionSkills(ctx, userID, sessionID, []NewSessionSkill{{
		SkillID: "skl_1", Name: "code-review", Version: "1.0.0",
		ContentHash: "hash-old", Description: "review", MountedPath: "/wt/.pi/skills/code-review",
	}})
	if err != nil {
		t.Fatalf("RecordSessionSkills: %v", err)
	}
	if written != 1 {
		t.Fatalf("first record wrote %d rows, want 1", written)
	}

	// A later mount (worktree rebuilt, skill edited) must not rewrite the record.
	written, err = st.RecordSessionSkills(ctx, userID, sessionID, []NewSessionSkill{{
		SkillID: "skl_1", Name: "code-review", Version: "2.0.0",
		ContentHash: "hash-new", Description: "review v2", MountedPath: "/wt2/.pi/skills/code-review",
	}})
	if err != nil {
		t.Fatalf("RecordSessionSkills again: %v", err)
	}
	if written != 0 {
		t.Fatalf("re-record wrote %d rows, want 0 (write-once)", written)
	}

	list, err := st.SessionSkills(ctx, userID, sessionID)
	if err != nil {
		t.Fatalf("SessionSkills: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("SessionSkills returned %d rows, want 1", len(list))
	}
	got := list[0]
	if got.Version != "1.0.0" || got.ContentHash != "hash-old" {
		t.Fatalf("record changed after an edit: %+v", got)
	}
	if got.MountedPath != "/wt/.pi/skills/code-review" {
		t.Fatalf("mounted path = %q", got.MountedPath)
	}
	if got.RecordedAt.IsZero() || got.RecordedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("recorded_at is not a real timestamp: %v", got.RecordedAt)
	}

	// A second session is free to record the new version; that is how a new
	// session picks up an edit.
	secondSession := mustSession(t, st, userID, "versioned-2")
	if _, err := st.RecordSessionSkills(ctx, userID, secondSession, []NewSessionSkill{{
		SkillID: "skl_1", Name: "code-review", Version: "2.0.0",
		ContentHash: "hash-new", MountedPath: "/wt2/.pi/skills/code-review",
	}}); err != nil {
		t.Fatalf("RecordSessionSkills second session: %v", err)
	}
	newer, err := st.SessionSkills(ctx, userID, secondSession)
	if err != nil {
		t.Fatalf("SessionSkills: %v", err)
	}
	if len(newer) != 1 || newer[0].ContentHash != "hash-new" {
		t.Fatalf("new session did not record the new version: %+v", newer)
	}
	// The old one is unchanged by the new session's mount.
	old, _ := st.SessionSkills(ctx, userID, sessionID)
	if old[0].ContentHash != "hash-old" {
		t.Fatalf("first session's record drifted: %+v", old[0])
	}
}

// TestSessionSkillsAreTenantScoped: a foreign session id must read as not found,
// exactly like every other session route.
func TestSessionSkillsAreTenantScoped(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ownerID := mustUser(t, st, "owner@example.test")
	otherID := mustUser(t, st, "other@example.test")
	sessionID := mustSession(t, st, ownerID, "private")
	if _, err := st.RecordSessionSkills(ctx, ownerID, sessionID, []NewSessionSkill{{
		SkillID: "skl_1", Name: "private-skill", Version: "1", ContentHash: "h",
	}}); err != nil {
		t.Fatalf("RecordSessionSkills: %v", err)
	}

	if _, err := st.SessionSkills(ctx, otherID, sessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read error = %v, want ErrNotFound", err)
	}
	_, err := st.RecordSessionSkills(ctx, otherID, sessionID, []NewSessionSkill{{
		SkillID: "skl_2", Name: "injected", Version: "1", ContentHash: "h",
	}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign write error = %v, want ErrNotFound", err)
	}
}

// TestCopySessionSkills covers the sidecar: it shares the parent's checkout, so
// it must report the parent's versions as its own immutable record.
func TestCopySessionSkills(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	userID := mustUser(t, st, "owner@example.test")
	parent := mustSession(t, st, userID, "parent")
	child := mustSession(t, st, userID, "child")

	if _, err := st.RecordSessionSkills(ctx, userID, parent, []NewSessionSkill{{
		SkillID: "skl_1", Name: "review", Version: "1", ContentHash: "hash-parent",
		MountedPath: "/wt/.pi/skills/review",
	}}); err != nil {
		t.Fatalf("RecordSessionSkills: %v", err)
	}
	written, err := st.CopySessionSkills(ctx, userID, parent, child)
	if err != nil {
		t.Fatalf("CopySessionSkills: %v", err)
	}
	if written != 1 {
		t.Fatalf("copied %d rows, want 1", written)
	}
	copied, err := st.SessionSkills(ctx, userID, child)
	if err != nil {
		t.Fatalf("SessionSkills: %v", err)
	}
	if len(copied) != 1 || copied[0].ContentHash != "hash-parent" {
		t.Fatalf("sidecar record = %+v", copied)
	}

	// Copying again is a no-op rather than a duplicate or an update.
	if written, err = st.CopySessionSkills(ctx, userID, parent, child); err != nil || written != 0 {
		t.Fatalf("second copy = %d, %v; want 0, nil", written, err)
	}
}
