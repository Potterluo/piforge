package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

func TestBundleFilesValidatesPaths(t *testing.T) {
	cases := []struct {
		name    string
		files   []skillFile
		wantErr string
	}{
		{
			name:  "plain skill",
			files: []skillFile{{Path: "SKILL.md", Content: "# hi"}},
		},
		{
			name: "nested assets",
			files: []skillFile{
				{Path: "SKILL.md", Content: "# hi"},
				{Path: "scripts/run.sh", Content: "echo"},
				{Path: `docs\\guide.md`, Content: "x"},
			},
		},
		{name: "empty", files: nil, wantErr: "no files"},
		{name: "no SKILL.md", files: []skillFile{{Path: "README.md", Content: "x"}}, wantErr: "must contain SKILL.md"},
		{name: "absolute", files: []skillFile{{Path: "/etc/passwd", Content: "x"}}, wantErr: "not allowed"},
		{name: "traversal", files: []skillFile{{Path: "../../etc/passwd", Content: "x"}}, wantErr: "not allowed"},
		{name: "empty path", files: []skillFile{{Path: "  ", Content: "x"}}, wantErr: "empty path"},
		{
			name:    "too large",
			files:   []skillFile{{Path: "SKILL.md", Content: strings.Repeat("x", maxSkillFileBytes+1)}},
			wantErr: "larger than",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bundleFiles(tc.files)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("bundleFiles: %v", err)
				}
				if len(got) != len(tc.files) {
					t.Fatalf("files = %d, want %d", len(got), len(tc.files))
				}
				// Windows-style separators must be normalized, not written as
				// a filename with a backslash in it.
				for name := range got {
					if strings.Contains(name, "\\") {
						t.Fatalf("path %q was not normalized", name)
					}
				}
				return
			}
			if err == nil {
				t.Fatalf("bundleFiles accepted %+v", tc.files)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestSkillUploadWritesAndReadsTheBundle(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
		"name": "Code Review", "version": "1.0.0", "description": "review diffs",
		"body": "---\nname: review\n---\nRead the diff before commenting.\n",
	}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create skill = %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(rec, &created)
	if created.Skill.BundleKey == "" {
		t.Fatal("a skill created with a body has no bundle")
	}
	if created.Skill.ContentHash == "" {
		t.Fatal("no content hash was derived")
	}

	// The content is on disk where provisioning will look for it.
	onDisk := filepath.Join(env.skillsDir, created.Skill.BundleKey, "SKILL.md")
	body, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("bundle file: %v", err)
	}
	if !strings.Contains(string(body), "Read the diff") {
		t.Fatalf("SKILL.md = %q", body)
	}

	// And it reads back, which is what the editor shows.
	read := env.doAuthed(http.MethodGet, "/v1/skills/"+created.Skill.ID+"/bundle", nil, c)
	if read.Code != http.StatusOK {
		t.Fatalf("read bundle = %d body %s", read.Code, read.Body.String())
	}
	var payload struct {
		Files []skillFile `json:"files"`
	}
	env.mustJSON(read, &payload)
	if len(payload.Files) != 1 || payload.Files[0].Path != "SKILL.md" {
		t.Fatalf("files = %+v", payload.Files)
	}
	if !strings.Contains(payload.Files[0].Content, "Read the diff") {
		t.Fatalf("content = %q", payload.Files[0].Content)
	}
}

func TestSkillUploadReplacesContentAndRejectsTraversal(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
		"name": "Deploy", "version": "1", "body": "v1\n",
	}, c)
	var payload struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &payload)
	skillID := payload.Skill.ID

	// A second upload replaces the bundle entirely: an old file left behind
	// would keep being available to the agent after the user removed it.
	replace := env.doAuthed(http.MethodPut, "/v1/skills/"+skillID+"/bundle", map[string]any{
		"files": []map[string]any{
			{"path": "SKILL.md", "content": "v2\n"},
			{"path": "scripts/deploy.sh", "content": "echo deploy\n"},
		},
	}, c)
	if replace.Code != http.StatusOK {
		t.Fatalf("replace = %d body %s", replace.Code, replace.Body.String())
	}
	read := env.doAuthed(http.MethodGet, "/v1/skills/"+skillID+"/bundle", nil, c)
	var after struct {
		Files []skillFile `json:"files"`
	}
	env.mustJSON(read, &after)
	if len(after.Files) != 2 {
		t.Fatalf("files after replace = %+v", after.Files)
	}
	if after.Files[0].Content != "v2\n" || after.Files[0].Path != "SKILL.md" {
		t.Fatalf("files = %+v", after.Files)
	}

	// Traversal is refused, and the previous bundle survives the refusal.
	bad := env.doAuthed(http.MethodPut, "/v1/skills/"+skillID+"/bundle", map[string]any{
		"files": []map[string]any{{"path": "../../escape.md", "content": "x"}},
	}, c)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("traversal = %d body %s, want 400", bad.Code, bad.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.skillsDir, skillID, "SKILL.md")); err != nil {
		t.Fatalf("the previous bundle was destroyed by a rejected upload: %v", err)
	}
}

func TestSkillUploadRequiresSkillMD(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
		"name": "Empty", "version": "1",
	}, c)
	var payload struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &payload)
	if payload.Skill.BundleKey != "" {
		t.Fatal("a skill created with no body got a bundle")
	}

	rec := env.doAuthed(http.MethodPut, "/v1/skills/"+payload.Skill.ID+"/bundle", map[string]any{
		"files": []map[string]any{{"path": "notes.md", "content": "x"}},
	}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("upload without SKILL.md = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SKILL.md") {
		t.Fatalf("error does not say what is missing: %s", rec.Body.String())
	}
}
