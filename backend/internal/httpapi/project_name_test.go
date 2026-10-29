package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

func TestRepoBasename(t *testing.T) {
	cases := map[string]string{
		"https://github.com/octocat/Spoon-Knife.git":  "Spoon-Knife",
		"https://github.com/octocat/Spoon-Knife":      "Spoon-Knife",
		"https://github.com/octocat/Spoon-Knife.git/": "Spoon-Knife",
		"https://github.com/x/y.git?ref=main":         "y",
		"git@github.com:x/y.git":                      "y",
		"git@github.com:single.git":                   "single",
		`C:\repos\cache-service`:                      "cache-service",
		"/repos/cache-service.git":                    "cache-service",
		"":                                            "",
		"   ":                                         "",
	}
	for input, want := range cases {
		if got := repoBasename(input); got != want {
			t.Errorf("repoBasename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDeriveProjectName(t *testing.T) {
	// UTC is what the handler passes: the web dialog previews the same date in
	// UTC, so a +08:00 browser and a UTC server agree (QA-16 minor).
	now := time.Date(2026, 10, 29, 21, 0, 0, 0, time.UTC)
	if got := deriveProjectName("https://github.com/octocat/Spoon-Knife.git", now); got != "Spoon-Knife" {
		t.Errorf("derived name = %q, want Spoon-Knife", got)
	}
	if got := deriveProjectName("git@github.com:x/y.git", now); got != "y" {
		t.Errorf("derived name = %q, want y", got)
	}
	if got := deriveProjectName("", now); got != "空白工作区 · 10-29" {
		t.Errorf("blank workspace name = %q, want 空白工作区 · 10-29", got)
	}
}

// The suffix search itself: the first free name wins, and it starts at -2
// because the bare name is the first candidate.
func TestFreeProjectName(t *testing.T) {
	taken := map[string]bool{"frontend": true, "frontend-2": true}
	got, err := freeProjectName("frontend", func(name string) (bool, error) {
		return taken[name], nil
	})
	if err != nil {
		t.Fatalf("freeProjectName: %v", err)
	}
	if got != "frontend-3" {
		t.Errorf("freeProjectName = %q, want frontend-3", got)
	}

	// Free name stays untouched: the common case must not grow a suffix.
	got, err = freeProjectName("backend", func(string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("freeProjectName: %v", err)
	}
	if got != "backend" {
		t.Errorf("freeProjectName = %q, want backend", got)
	}

	// The predicate's error is the caller's error, not a silent "free".
	if _, err := freeProjectName("x", func(string) (bool, error) {
		return false, context.DeadlineExceeded
	}); err == nil {
		t.Fatal("freeProjectName swallowed the predicate error")
	}
}

// Same repository twice is the reported dead end: the second create must land
// on Spoon-Knife-2 instead of a 409 the user cannot act on.
func TestCreateProjectDerivesFreeName(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	create := func(repoURL string) *contracts.Project {
		t.Helper()
		rec := env.doAuthed(http.MethodPost, "/v1/projects", map[string]string{
			"repo_url": repoURL,
		}, c)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d body %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Project *contracts.Project `json:"project"`
		}
		env.mustJSON(rec, &body)
		return body.Project
	}

	const repo = "https://github.com/octocat/Spoon-Knife.git"
	first := create(repo)
	if first.Name != "Spoon-Knife" {
		t.Fatalf("first name = %q, want Spoon-Knife", first.Name)
	}
	second := create(repo)
	if second.Name != "Spoon-Knife-2" {
		t.Fatalf("second name = %q, want Spoon-Knife-2", second.Name)
	}
	third := create(repo)
	if third.Name != "Spoon-Knife-3" {
		t.Fatalf("third name = %q, want Spoon-Knife-3", third.Name)
	}
}

// A name the user typed is not silently suffixed: the conflict is returned by
// name, in Chinese, and case does not buy a second project.
func TestCreateProjectExplicitNameConflict(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodPost, "/v1/projects", map[string]string{"name": "Frontend"}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body %s", rec.Code, rec.Body.String())
	}

	rec = env.doAuthed(http.MethodPost, "/v1/projects", map[string]string{"name": "frontend"}, c)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	env.mustJSON(rec, &body)
	if body.Error == nil || body.Error.Code != contracts.ErrConflict {
		t.Fatalf("error = %+v", body.Error)
	}
	if !strings.Contains(body.Error.Message, "frontend") || !strings.Contains(body.Error.Message, "已存在") {
		t.Errorf("message = %q, want the conflicting name and 已存在", body.Error.Message)
	}
}

// Deleting a project with a live turn is refused instead of killing it, and the
// same delete succeeds once the turn is over.
func TestDeleteProjectRefusesWhileRunning(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")

	running := contracts.StatusRunning
	if _, err := env.store.UpdateSession(context.Background(), user.ID, sess.ID, store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	rec := env.doAuthed(http.MethodDelete, "/v1/projects/"+project.ID, nil, c)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete while running = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	env.mustJSON(rec, &body)
	if body.Error == nil || !strings.Contains(body.Error.Message, "正在运行") {
		t.Fatalf("error = %+v, want a message about a running turn", body.Error)
	}
	// The project is still there: a refusal must not half-delete anything.
	if _, err := env.store.Project(context.Background(), user.ID, project.ID); err != nil {
		t.Fatalf("project gone after refusal: %v", err)
	}

	idle := contracts.StatusIdle
	if _, err := env.store.UpdateSession(context.Background(), user.ID, sess.ID, store.SessionPatch{Status: &idle}); err != nil {
		t.Fatalf("mark idle: %v", err)
	}
	if rec := env.doAuthed(http.MethodDelete, "/v1/projects/"+project.ID, nil, c); rec.Code != http.StatusOK {
		t.Fatalf("delete idle = %d body %s", rec.Code, rec.Body.String())
	}
}

// Renaming onto an existing name has to answer with the same sentence create
// does. The store only reports "unique violation", so the patch path used to
// leak an English 409 that the UI could only render as 「操作失败，请稍后重试」 —
// the one message with no information in it (QA-16, FIX B).
func TestRenameToATakenNameNamesTheConflict(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	ctx := context.Background()

	first, err := env.store.CreateProject(ctx, user.ID, store.NewProject{Name: "frontend", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.store.CreateProject(ctx, user.ID, store.NewProject{Name: "frontend-2", DefaultBranch: "main"}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	rec := env.doAuthed(http.MethodPatch, "/v1/projects/"+first.ID, map[string]any{"name": "frontend-2"}, c)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename to a taken name = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	env.mustJSON(rec, &body)
	if body.Error == nil || !strings.Contains(body.Error.Message, "项目名称「frontend-2」已存在") {
		t.Fatalf("error = %+v, want the precise conflict sentence the dialog shows", body.Error)
	}

	// Names are unique per user; the message names the exact duplicate.
	rec = env.doAuthed(http.MethodPatch, "/v1/projects/"+first.ID, map[string]any{"name": "frontend-2"}, c)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "frontend-2") {
		t.Fatalf("duplicate rename = %d %s", rec.Code, rec.Body.String())
	}

	// The project was not renamed, and a free name still works.
	stored, err := env.store.Project(ctx, user.ID, first.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Name != "frontend" {
		t.Fatalf("the refused rename changed the name to %q", stored.Name)
	}
	if rec := env.doAuthed(http.MethodPatch, "/v1/projects/"+first.ID, map[string]any{"name": "frontend-3"}, c); rec.Code != http.StatusOK {
		t.Fatalf("free rename = %d %s", rec.Code, rec.Body.String())
	}
}
