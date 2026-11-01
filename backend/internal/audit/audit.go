// Package audit builds the forensic half of PiForge's tool-call trail.
//
// It owns three rules that must not drift apart between the writer (the store)
// and the reader (the admin endpoint):
//
//   - What a stored argument preview may contain. A preview is bounded (per
//     field and in total), it never carries a credential, and a field that
//     holds file content is replaced by its size and SHA-256. The full
//     arguments stay in the session; this package only decides the excerpt.
//   - The redaction of key-shaped values. The marker is "<redacted>", the same
//     one the sandbox provider already uses for SANDBOX_API_KEY in logs
//     (internal/sandbox/docker.go), so an operator reads one convention across
//     the product.
//   - The chained hash over audit rows. The chain proves that the rows read
//     back are the rows a process wrote, in the order it wrote them, and that
//     none was removed from the middle. It does not prove them true, and it is
//     not a signature (see docs/AUDIT.md).
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The caps. They are deliberately small: the audit trail is an index into the
// session, not a copy of it.
const (
	// MaxPreviewBytes bounds the whole serialised preview of one tool call.
	MaxPreviewBytes = 2048
	// MaxFieldRunes bounds one string field before the total cap is applied.
	MaxFieldRunes = 256
	// MaxFields bounds one object (top level or nested).
	MaxFields = 16
	// MaxArrayItems bounds one array.
	MaxArrayItems = 8
	// MaxDepth is how deep a nested argument object is described.
	MaxDepth = 3
)

// Redacted is the marker a sensitive value is replaced with. It is the marker
// the sandbox provider already uses for SANDBOX_API_KEY in its log lines.
const Redacted = "<redacted>"

// Preview is the bounded, redacted excerpt of one tool call's arguments.
type Preview struct {
	// JSON is a JSON object. It is never the raw argument text.
	JSON json.RawMessage
	// Bytes is len(JSON), stored so a reader can tell a small call from one
	// that hit the cap without re-serialising it.
	Bytes int
	// Truncated says a field, an array, an object or the total budget was cut.
	Truncated bool
}

// BuildPreview renders one tool call's arguments for storage.
//
// It is best-effort by design: a tool the runtime calls with a non-object or
// malformed payload still produces a row, with `{"_unparsed":true}`. The
// arguments hash is computed from the raw bytes by the caller, so an
// unparseable payload is still correlated with the event that produced it.
func BuildPreview(args json.RawMessage) Preview {
	if len(args) == 0 {
		return Preview{JSON: json.RawMessage(`{}`)}
	}
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return Preview{JSON: json.RawMessage(`{"_unparsed":true}`)}
	}
	// A tool's arguments are an object in every protocol Pi speaks. An array or
	// a scalar is kept under one key rather than dropped: the shape itself is a
	// fact an investigator may need.
	if _, ok := raw.(map[string]any); !ok {
		raw = map[string]any{"args": raw}
	}

	truncated := false
	for _, limit := range []int{MaxFieldRunes, MaxFieldRunes / 2, MaxFieldRunes / 4} {
		value := sanitizeObject(raw.(map[string]any), limit, 0, &truncated)
		encoded, err := marshalPreviewJSON(value)
		if err != nil {
			return Preview{JSON: json.RawMessage(`{"_unparsed":true}`)}
		}
		if len(encoded) <= MaxPreviewBytes {
			return Preview{JSON: encoded, Bytes: len(encoded), Truncated: truncated}
		}
	}
	// Still too large after shrinking every field: keep the first few keys in
	// sorted order. The row says it was cut; it never stores the rest.
	object := sanitizeObject(raw.(map[string]any), MaxFieldRunes/4, 0, &truncated)
	keys := sortedKeys(object)
	kept := map[string]any{}
	for _, key := range keys {
		if len(kept) >= 4 {
			truncated = true
			break
		}
		kept[key] = object[key]
	}
	encoded, err := marshalPreviewJSON(kept)
	if err != nil {
		return Preview{JSON: json.RawMessage(`{"_unparsed":true}`)}
	}
	if len(encoded) > MaxPreviewBytes {
		return Preview{
			JSON:      json.RawMessage(fmt.Sprintf(`{"_truncated":true,"_bytes":%d}`, len(encoded))),
			Bytes:     len(encoded),
			Truncated: true,
		}
	}
	return Preview{JSON: encoded, Bytes: len(encoded), Truncated: true}
}

func sanitizeObject(object map[string]any, fieldLimit int, depth int, truncated *bool) map[string]any {
	keys := sortedKeys(object)
	out := make(map[string]any, len(keys))
	for i, key := range keys {
		if i >= MaxFields {
			*truncated = true
			break
		}
		out[key] = sanitizeField(key, object[key], fieldLimit, depth, truncated)
	}
	return out
}

func sanitizeField(key string, value any, fieldLimit int, depth int, truncated *bool) any {
	if SensitiveKey(key) {
		return Redacted
	}
	if ContentKey(key) {
		if text, ok := value.(string); ok {
			sum := sha256.Sum256([]byte(text))
			return map[string]any{
				"sha256": hex.EncodeToString(sum[:]),
				"bytes":  len(text),
			}
		}
	}
	switch v := value.(type) {
	case string:
		return truncateRedact(v, fieldLimit, truncated)
	case json.Number:
		return v
	case bool, nil:
		return v
	case []any:
		items := v
		if len(items) > MaxArrayItems {
			items = items[:MaxArrayItems]
			*truncated = true
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, sanitizeField(key, item, fieldLimit, depth, truncated))
		}
		return out
	case map[string]any:
		if depth+1 >= MaxDepth {
			*truncated = true
			return "…"
		}
		return sanitizeObject(v, fieldLimit, depth+1, truncated)
	default:
		return truncateRedact(fmt.Sprint(v), fieldLimit, truncated)
	}
}

func truncateRedact(text string, limit int, truncated *bool) string {
	text = RedactText(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	*truncated = true
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// marshalPreviewJSON encodes without HTML escaping: a preview is read by a
// person in a table and exported to CSV, so "<redacted>" must not arrive as
// "\u003credacted\u003e". Encoding is otherwise the standard library's, so map
// keys stay sorted and the output is deterministic.
func marshalPreviewJSON(value any) ([]byte, error) {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(buf.String(), "\n")), nil
}

// sensitiveTokens are the words that make a field's *name* key-shaped. The
// match is on word tokens, so "monkey" is not a key and "api_key" is.
var sensitiveTokens = map[string]bool{
	"password": true, "passwd": true, "passphrase": true,
	"secret": true, "secrets": true,
	"token": true, "tokens": true,
	"apikey": true, "accesskey": true, "secretkey": true, "privatekey": true,
	"publickey": true, "signingkey": true, "encryptionkey": true, "masterkey": true,
	"credential": true, "credentials": true,
	"authorization": true, "auth": true, "cookie": true, "cookies": true,
	"bearer": true, "jwt": true,
	"clientsecret": true, "subscriptionkey": true, "connectionstring": true,
	"dsn": true, "cvv": true, "ssn": true, "pin": true, "key": true,
}

// SensitiveKey reports whether an argument field's name says its value is a
// credential. Splitting on non-alphanumerics and camelCase is what makes
// `apiKey`, `api_key`, `X-Api-Key` and `api-key` one rule.
func SensitiveKey(key string) bool {
	for _, token := range keyTokens(key) {
		if sensitiveTokens[token] {
			return true
		}
	}
	return false
}

// contentTokens are the fields that carry a file's bytes. They are replaced by
// a digest, never stored: an audit trail that keeps a copy of every write is a
// second copy of the tenant's data.
var contentTokens = map[string]bool{
	"content": true, "contents": true, "filecontent": true,
	"newstring": true, "oldstring": true, "newtext": true, "oldtext": true,
	"patch": true, "diff": true, "blob": true, "base64": true,
	"image": true, "body": true, "text": true, "data": true,
}

// ContentKey reports whether a field holds file content rather than an
// instruction. The caller stores path + size + sha256 in its place.
func ContentKey(key string) bool {
	// The whole name matters for the compound spellings: `new_string` is two
	// tokens, and neither "new" nor "string" alone holds file content.
	joined := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key))
	if contentTokens[joined] {
		return true
	}
	for _, token := range keyTokens(key) {
		if contentTokens[token] {
			return true
		}
	}
	return false
}

// keyTokens splits an argument name into lowercase words: `api_key`,
// `api-key`, `apiKey` and `API.Key` all become ["api", "key"].
func keyTokens(key string) []string {
	words := make([]string, 0, 4)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, strings.ToLower(current.String()))
		current.Reset()
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case r >= 'A' && r <= 'Z':
			// A capital starts a word unless it continues an acronym
			// ("APIKey" -> API, Key).
			if i > 0 && (runes[i-1] < 'A' || runes[i-1] > 'Z') {
				flush()
			}
			current.WriteRune(r)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return words
}

// valuePatterns are credentials recognised by shape, not by field name. A key
// pasted into a `command` or `url` field is still a key.
var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{12,}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{12,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)(?:password|passwd|passphrase|secret|token|api[_-]?key)\s*[:=]\s*["']?[^\s"',;]{6,}`),
}

// RedactText replaces every value-shaped credential in text with the marker.
// It is exported because the sandbox provider's log redaction and this one
// must agree on the marker, and because the preview builder is not the only
// place a command string is logged.
func RedactText(text string) string {
	// The sandbox key keeps its name: the log line is the one place an operator
	// needs to see *what* was redacted, which is the rule
	// internal/sandbox/docker.go already applies.
	text = sandboxKeyPattern.ReplaceAllString(text, "${1}"+Redacted)
	for _, pattern := range valuePatterns {
		text = pattern.ReplaceAllString(text, Redacted)
	}
	return text
}

// sandboxKeyPattern matches one SANDBOX_API_KEY=... assignment.
var sandboxKeyPattern = regexp.MustCompile(`(SANDBOX_API_KEY=)\S+`)

// chainVersion is part of every hash. Changing the field list or the encoding
// is a new version: old rows keep verifying under the version they were
// written with (the reader does not pretend their chain is current).
const chainVersion = 1

// ChainFields is exactly the content a chained hash covers.
//
// The declaration order is the encoding order (encoding/json marshals struct
// fields in order), so appending a field is a chain version bump. Fields that
// are not here are not protected by the chain; there are none today, and the
// test in store/audit_chain_test.go fails if a new column is added without a
// field.
type ChainFields struct {
	V             int    `json:"v"`
	ID            string `json:"id"`
	Seq           int64  `json:"seq"`
	SessionID     string `json:"session_id"`
	ToolCallID    string `json:"tool_call_id"`
	Phase         string `json:"phase"`
	RecordedAt    string `json:"recorded_at"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at"`
	UserID        string `json:"user_id"`
	ProjectID     string `json:"project_id"`
	SandboxID     string `json:"sandbox_id"`
	RunID         string `json:"run_id"`
	UserEmail     string `json:"user_email"`
	ProjectName   string `json:"project_name"`
	SessionTitle  string `json:"session_title"`
	Tool          string `json:"tool"`
	Namespace     string `json:"namespace"`
	ArgsHash      string `json:"args_hash"`
	ArgsPreview   string `json:"args_preview"`
	PreviewBytes  int    `json:"preview_bytes"`
	ArgsTruncated bool   `json:"args_truncated"`
	Status        string `json:"status"`
	IsError       int    `json:"is_error"`
	HasIsError    bool   `json:"has_is_error"`
	ExitCode      int    `json:"exit_code"`
	HasExitCode   bool   `json:"has_exit_code"`
	DurationMS    int    `json:"duration_ms"`
	HasDuration   bool   `json:"has_duration"`
	ResultBytes   int    `json:"result_bytes"`
	HasResult     bool   `json:"has_result"`
	ModelRef      string `json:"model_ref"`
	CommitSHA     string `json:"commit_sha"`
}

// ChainHash is the hash of one row, given the previous row's hash in the same
// session. prev is empty for the first row of a session.
//
// It is SHA-256 over the previous hash and the canonical JSON of the fields,
// with a version prefix. A row can be checked without a secret, which is also
// the chain's limit: an attacker with write access can recompute every hash
// after an edit. What they cannot do quietly is edit one row or delete one from
// the middle — that is the case this detects (docs/AUDIT.md).
func ChainHash(prev string, fields ChainFields) string {
	fields.V = chainVersion
	encoded, err := json.Marshal(fields)
	if err != nil {
		// The struct is entirely strings and numbers; a marshal failure is a
		// programming error, and a hash over the error would hide it.
		panic("audit: marshal chain fields: " + err.Error())
	}
	sum := sha256.Sum256(append([]byte(prev+"\n"), encoded...))
	return hex.EncodeToString(sum[:])
}
