package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// setSandboxRoot points the project's sandbox row at a directory this test owns,
// so the quota walk measures files the test wrote instead of a real host path.
func setSandboxRoot(t *testing.T, env *testEnv, projectID, root string) {
	t.Helper()
	if err := env.store.ExecForTest(context.Background(),
		`UPDATE sandboxes SET root_dir = ? WHERE project_id = ?`, root, projectID); err != nil {
		t.Fatalf("set sandbox root: %v", err)
	}
}

func writeBigFile(t *testing.T, root, rel string, n int) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// FR-5.8: a checkout over its budget stops the run, with a message that names the
// budget, the current usage and what to delete — instead of letting an agent fill
// the volume.
func TestRunIsRefusedWhenTheSandboxIsOverItsDiskQuota(t *testing.T) {
	env := newTestEnv(t)
	env.deps.SandboxDiskQuota = 4 << 10 // 4 KiB
	env.rebuild()
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "quota")
	root := t.TempDir()
	env.withWorktree(user.ID, project.ID, sess.ID)
	setSandboxRoot(t, env, project.ID, root)
	writeBigFile(t, root, "repo/big.bin", 8<<10)

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_quota", "text": "install the world"}, c)
	env.mustErr(rec, http.StatusInsufficientStorage, contracts.ErrSandboxDiskQuota)

	var envelope struct {
		Error *contracts.APIError `json:"error"`
	}
	env.mustJSON(rec, &envelope)
	message := envelope.Error.Message
	for _, want := range []string{"8.0 KiB", "4.0 KiB", "配额", "运行已停止", "node_modules", "-sandbox-disk-quota"} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal message is missing %q:\n%s", want, message)
		}
	}
	if envelope.Error.Details["used_bytes"] != float64(8<<10) {
		t.Fatalf("details.used_bytes = %v", envelope.Error.Details["used_bytes"])
	}
	if envelope.Error.Details["limit_bytes"] != float64(4<<10) {
		t.Fatalf("details.limit_bytes = %v", envelope.Error.Details["limit_bytes"])
	}

	// The run never started: the fake runtime records every prompt it is handed.
	env.runtime.mu.Lock()
	prompts := len(env.runtime.prompts)
	env.runtime.mu.Unlock()
	if prompts != 0 {
		t.Fatalf("the runtime received %d prompt(s) for a refused run", prompts)
	}

	// The refusal is in the session's event log, which is what the transcript and
	// the notification path read.
	events, err := env.store.ListEvents(t.Context(), user.ID, sess.ID, 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type != contracts.EvPlatformErr {
			continue
		}
		if strings.Contains(string(event.Payload), contracts.ErrSandboxDiskQuota) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event recorded the refusal: %+v", contracts.EvPlatformErr, events)
	}
}

// Under the budget, the same handler runs the turn — the guard must not become
// "every message is refused".
func TestRunUnderTheDiskQuotaStarts(t *testing.T) {
	env := newTestEnv(t)
	env.deps.SandboxDiskQuota = 64 << 10
	env.rebuild()
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "under")
	root := t.TempDir()
	env.withWorktree(user.ID, project.ID, sess.ID)
	setSandboxRoot(t, env, project.ID, root)
	writeBigFile(t, root, "repo/small.bin", 1024)

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_under", "text": "hello"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("a sandbox under its quota was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// A zero quota disables enforcement, which is how a deployment opts out.
func TestDisabledDiskQuotaLetsTheRunThrough(t *testing.T) {
	env := newTestEnv(t)
	env.deps.SandboxDiskQuota = 0
	env.rebuild()
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "free")
	root := t.TempDir()
	env.withWorktree(user.ID, project.ID, sess.ID)
	setSandboxRoot(t, env, project.ID, root)
	writeBigFile(t, root, "repo/huge.bin", 1<<20)

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_free", "text": "hello"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("a disabled quota refused a run: %d %s", rec.Code, rec.Body.String())
	}
}

// The admin status has to answer "who is close", so every sandbox is reported
// next to the budget it is measured against.
func TestAdminStatusReportsEachSandboxAgainstTheQuota(t *testing.T) {
	env := newTestEnv(t)
	env.deps.SandboxDiskQuota = 1 << 20
	env.deps.DiskQuotaReportTTL = time.Millisecond
	env.rebuild()
	env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "pw")
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "reported")
	root := t.TempDir()
	env.withWorktree(user.ID, project.ID, sess.ID)
	setSandboxRoot(t, env, project.ID, root)
	writeBigFile(t, root, "repo/ok.bin", 2<<10)

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		SandboxUsage []struct {
			SandboxID  string `json:"sandbox_id"`
			UserID     string `json:"user_id"`
			ProjectID  string `json:"project_id"`
			Bytes      int64  `json:"bytes"`
			QuotaBytes int64  `json:"quota_bytes"`
			Over       bool   `json:"over"`
		} `json:"sandbox_usage"`
		SandboxDiskQuota int64 `json:"sandbox_disk_quota"`
	}
	env.mustJSON(rec, &body)
	if body.SandboxDiskQuota != 1<<20 {
		t.Fatalf("sandbox_disk_quota = %d", body.SandboxDiskQuota)
	}
	if len(body.SandboxUsage) != 1 {
		t.Fatalf("sandbox_usage = %+v, want one row", body.SandboxUsage)
	}
	row := body.SandboxUsage[0]
	if row.ProjectID != project.ID || row.UserID != user.ID {
		t.Fatalf("row does not name the owner: %+v", row)
	}
	if row.Bytes != 2<<10 || row.QuotaBytes != 1<<20 || row.Over {
		t.Fatalf("row = %+v, want 2048 under a 1 MiB quota", row)
	}

	// Exceed the budget: the same row now says so.
	writeBigFile(t, root, "repo/too-big.bin", 2<<20)
	time.Sleep(2 * time.Millisecond)
	rec = env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	env.mustJSON(rec, &body)
	if len(body.SandboxUsage) != 1 || !body.SandboxUsage[0].Over {
		t.Fatalf("an over-quota sandbox is not flagged: %+v", body.SandboxUsage)
	}
}
