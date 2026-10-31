package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// zipUpload builds the multipart body POST /v1/skills/import/zip reads.
func zipUpload(t *testing.T, name string, data []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return writer.FormDataContentType(), buf.Bytes()
}

// FR-5.8: an archive that would expand past the configured total is refused as a
// whole, with a message that names the cap, and no skill directory or row is left
// behind.
func TestZipImportRefusesAnArchiveThatWouldExpandPastTheCap(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.deps.SkillMaxBytes = 2048
	env.rebuild()
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	archive := mkzip(t, []zipEntry{
		{name: "bomb/SKILL.md", body: strings.Repeat("x", 1500)},
		{name: "bomb/part2.txt", body: strings.Repeat("y", 1500)},
	})
	contentType, body := zipUpload(t, "bomb.zip", archive)
	rec := env.doRaw(http.MethodPost, "/v1/skills/import/zip", contentType, body, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)

	var envelope struct {
		Error *contracts.APIError `json:"error"`
	}
	env.mustJSON(rec, &envelope)
	for _, want := range []string{"2.0 KiB", "上限", "没有导入任何文件", "-skill-max-bytes"} {
		if !strings.Contains(envelope.Error.Message, want) {
			t.Fatalf("refusal is missing %q:\n%s", want, envelope.Error.Message)
		}
	}

	// Nothing partial: no row, no bundle directory.
	skills, err := env.store.ListSkills(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("a refused import created %d skill row(s)", len(skills))
	}
	if entries, rerr := os.ReadDir(env.deps.SkillsDir); rerr == nil && len(entries) != 0 {
		t.Fatalf("a refused import left %d directory/ies behind", len(entries))
	}
}

// The refusal happens before a single byte is written, so there is no partial
// directory to clean up even if the caller forgot to remove its staging dir.
func TestZipExpansionRefusalWritesNothing(t *testing.T) {
	archive := mkzip(t, []zipEntry{
		{name: "bomb/SKILL.md", body: strings.Repeat("x", 1500)},
		{name: "bomb/payload.bin", body: strings.Repeat("y", 1500)},
	})
	dir := t.TempDir()
	err := extractSkillZipWithin(archive, dir, 2048)
	if err == nil {
		t.Fatal("an archive that would expand past the cap was accepted")
	}
	for _, want := range []string{"2.0 KiB", "上限", "没有导入任何文件"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal is missing %q: %v", want, err)
		}
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil && !os.IsNotExist(rerr) {
		t.Fatalf("read extraction dir: %v", rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused expansion left files behind: %v", entries)
	}
}

// The cap has a deployment default and the flag overrides it; an unset value is
// the default, never "unlimited".
func TestSkillExpandCapDefaultAndOverride(t *testing.T) {
	if got := (&server{}).skillMaxBytes(); got != DefaultSkillMaxBytes {
		t.Fatalf("default cap = %d, want %d", got, DefaultSkillMaxBytes)
	}
	if got := (&server{deps: Deps{SkillMaxBytes: 4096}}).skillMaxBytes(); got != 4096 {
		t.Fatalf("configured cap = %d, want 4096", got)
	}
}
