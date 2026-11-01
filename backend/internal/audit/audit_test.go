package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// The audit preview is the only copy of a tool's arguments outside the
// session, so the redaction rules are pinned here rather than trusted to a
// comment: a key-shaped field, a key-shaped value pasted into an ordinary
// field, and file content all have to come out safe.
func TestPreviewRedactsKeyShapedArguments(t *testing.T) {
	content := "package main\n\nfunc main() { println(\"hello\") }\n"
	args := json.RawMessage(`{
		"path": "src/main.go",
		"content": ` + mustJSON(t, content) + `,
		"api_key": "sk-live-abcdef0123456789",
		"command": "curl -H 'Authorization: Bearer abcdefgh12345678' https://example.test",
		"nested": {"password": "hunter2", "note": "keep me"},
		"count": 5
	}`)

	preview := BuildPreview(args)
	if preview.Bytes != len(preview.JSON) || preview.Bytes == 0 {
		t.Fatalf("bytes = %d, len = %d", preview.Bytes, len(preview.JSON))
	}
	var got map[string]any
	if err := json.Unmarshal(preview.JSON, &got); err != nil {
		t.Fatalf("preview is not JSON: %v: %s", err, preview.JSON)
	}

	// A field whose name says "credential" is replaced whole, not truncated:
	// even a prefix of a key is a leak.
	if got["api_key"] != Redacted {
		t.Fatalf("api_key = %v, want %s", got["api_key"], Redacted)
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["password"] != Redacted {
		t.Fatalf("nested.password = %v", got["nested"])
	}
	if nested["note"] != "keep me" {
		t.Fatalf("an ordinary field was dropped: %v", nested)
	}

	// File content is a digest, never the bytes. The path survives so an
	// investigator can tell which file the call wrote.
	if strings.Contains(string(preview.JSON), "func main") {
		t.Fatalf("file content was stored: %s", preview.JSON)
	}
	file, ok := got["content"].(map[string]any)
	if !ok {
		t.Fatalf("content = %v, want a digest", got["content"])
	}
	if bytes, _ := file["bytes"].(float64); int(bytes) != len(content) {
		t.Fatalf("content bytes = %v, want %d", file["bytes"], len(content))
	}
	if sum, _ := file["sha256"].(string); len(sum) != 64 {
		t.Fatalf("content sha256 = %q", file["sha256"])
	}
	if got["path"] != "src/main.go" {
		t.Fatalf("path = %v", got["path"])
	}

	// A credential pasted into a command is caught by shape, not by field name.
	command, _ := got["command"].(string)
	if strings.Contains(command, "abcdefgh12345678") {
		t.Fatalf("a bearer token survived in the command preview: %q", command)
	}
	if !strings.Contains(command, Redacted) {
		t.Fatalf("the command preview did not say what it redacted: %q", command)
	}
}

// The preview is bounded in total, not only per field: a write of a 10 MiB
// file must not become a 10 MiB audit row.
func TestPreviewIsBoundedAndNeverCarriesContent(t *testing.T) {
	huge := strings.Repeat("A", 10<<20)
	preview := BuildPreview(json.RawMessage(`{"path":"big.bin","content":` + mustJSON(t, huge) + `}`))
	if preview.Bytes > MaxPreviewBytes || len(preview.JSON) > MaxPreviewBytes {
		t.Fatalf("preview is %d bytes, cap is %d", preview.Bytes, MaxPreviewBytes)
	}
	if strings.Contains(string(preview.JSON), huge[:64]) {
		t.Fatal("the preview contains file bytes")
	}
	// Content is replaced whole, so this call is not "truncated": its digest is
	// complete. A long *instruction* is what hits the per-field cap.
	command := BuildPreview(json.RawMessage(`{"command":` + mustJSON(t, huge) + `}`))
	if !command.Truncated {
		t.Fatal("a 10 MiB command must be marked truncated")
	}
	if command.Bytes > MaxPreviewBytes {
		t.Fatalf("command preview is %d bytes", command.Bytes)
	}
}

func TestPreviewHandlesMalformedAndHugeArguments(t *testing.T) {
	if got := BuildPreview(json.RawMessage(`not json`)); string(got.JSON) != `{"_unparsed":true}` {
		t.Fatalf("malformed args = %s", got.JSON)
	}
	// Many fields, each small: the total budget still wins.
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"field_`)
		b.WriteString(strings.Repeat("x", i%5))
		b.WriteString(`_`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(`":"`)
		b.WriteString(strings.Repeat("y", 200))
		b.WriteString(`"`)
	}
	b.WriteString("}")
	preview := BuildPreview(json.RawMessage(b.String()))
	if preview.Bytes > MaxPreviewBytes {
		t.Fatalf("preview is %d bytes, cap is %d", preview.Bytes, MaxPreviewBytes)
	}
	if !preview.Truncated {
		t.Fatal("200 fields must be truncated")
	}
}

func TestSensitiveKeySpellings(t *testing.T) {
	for _, key := range []string{"api_key", "apiKey", "API-KEY", "X-Api-Key", "password", "clientSecret", "authorization", "token", "private_key"} {
		if !SensitiveKey(key) {
			t.Errorf("%q should be key-shaped", key)
		}
	}
	for _, key := range []string{"path", "command", "monkey", "count", "keyword"} {
		if SensitiveKey(key) {
			t.Errorf("%q should not be key-shaped", key)
		}
	}
}

func TestChainHashBindsEveryField(t *testing.T) {
	base := ChainFields{ID: "tc_1:start", Seq: 1, SessionID: "ses_1", ToolCallID: "tc_1", Phase: "start", Tool: "bash"}
	first := ChainHash("", base)
	if first != ChainHash("", base) {
		t.Fatal("the hash is not deterministic")
	}
	changed := base
	changed.Status = "error"
	if ChainHash("", changed) == first {
		t.Fatal("a changed field did not change the hash")
	}
	if ChainHash("prior", base) == first {
		t.Fatal("prev_hash did not change the hash")
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}
