package aio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Client talks to an AIO Sandbox (agent-infra/sandbox) over its v1 HTTP API.
//
// The point of that image is that the browser, the shell and the files live in one
// container with one filesystem. This client is how the control plane uses it without
// executing anything itself: every action is an HTTP call to a process it does not share
// a kernel with.
//
// Only the endpoints PiForge needs today are wrapped. Adding one is a method, not a
// redesign — the path list is in website/docs/public/v1/openapi.json in that repository.
type Client struct {
	base string
	key  string
	http *http.Client
	// Root is the workspace directory inside the container ("/home/gem" upstream).
	Root string
}

// Config configures a client.
type Config struct {
	// BaseURL is the sandbox's HTTP endpoint, e.g. http://127.0.0.1:32768.
	BaseURL string
	// APIKey is sent as X-AIO-API-Key when the container was started with SANDBOX_API_KEY.
	APIKey string
	// Root is the workspace path used when a request does not name one.
	Root string
	// Timeout bounds one request; screenshots and shell calls are not instant.
	Timeout time.Duration
}

// New builds a client.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("aio: base url is required")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("aio: base url %q: %w", base, err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		root = "/home/gem"
	}
	return &Client{
		base: base,
		key:  strings.TrimSpace(cfg.APIKey),
		http: &http.Client{Timeout: timeout},
		Root: root,
	}, nil
}

// BaseURL is the endpoint this client talks to.
func (c *Client) BaseURL() string { return c.base }

// request performs one API call and decodes the JSON answer into out.
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("aio: encode %s: %w", path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("aio: %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("X-AIO-API-Key", c.key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("aio: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("aio: read %s: %w", path, err)
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("aio: %s %s: %d: %s", method, path, res.StatusCode, truncate(string(payload), 300))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("aio: decode %s: %w: %s", path, err, truncate(string(payload), 200))
	}
	return nil
}

// Info reports what the sandbox can do, including the CDP URL of its browser.
//
// Field names taken from the running image (1.11.0), not from the docs: the answer is
// wrapped in `data`, and the workspace is not reported at all.
type Info struct {
	CDPURL    string `json:"cdp_url"`
	VNCURL    string `json:"vnc_url"`
	CDPUIRL   string `json:"cdp_ui_url"`
	UserAgent string `json:"user_agent"`
}

// BrowserInfo asks for the browser endpoint.
//
// It is the single most useful call in the integration: the CDP URL it returns is what
// turns "the panel's browser lives on the host" into "the panel's browser lives with the
// files".
func (c *Client) BrowserInfo(ctx context.Context) (Info, error) {
	var envelope struct {
		Data Info `json:"data"`
		Info
	}
	if err := c.request(ctx, http.MethodGet, "/v1/browser/info", nil, &envelope); err != nil {
		return Info{}, err
	}
	if envelope.Data.CDPURL == "" && envelope.CDPURL != "" {
		return envelope.Info, nil
	}
	return envelope.Data, nil
}

// ShellResult is what a command produced.
type ShellResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// Exec runs a shell command inside the sandbox.
//
// The command runs in cwd when one is given. The image accepts a `cwd` field and
// ignores it — probed on 1.11.0: with cwd=/workspace/a/b, `pwd` still answered
// /home/gem — so the directory is entered by the command itself.
func (c *Client) Exec(ctx context.Context, command string, cwd string) (ShellResult, error) {
	body := map[string]any{"command": command, "async_mode": false}
	if dir := strings.TrimSpace(cwd); dir != "" {
		body["cwd"] = dir
		command = "cd -- " + Quote(dir) + " && " + command
		body["command"] = command
	}
	var out struct {
		Data struct {
			Output   string `json:"output"`
			ExitCode int    `json:"exit_code"`
		} `json:"data"`
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/shell/exec", body, &out); err != nil {
		return ShellResult{}, err
	}
	result := ShellResult{Output: out.Data.Output, ExitCode: out.Data.ExitCode}
	if result.Output == "" && out.Output != "" {
		result.Output, result.ExitCode = out.Output, out.ExitCode
	}
	return result, nil
}

// OpError is the structured failure the /v1/file/* endpoints return.
//
// Those endpoints answer HTTP 200 even when the operation failed: `success` is
// false and this object is in `data`. Ignoring `success` is how a missing file
// turns into an empty one, so every helper below checks it.
type OpError struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Message   string `json:"message"`
	Type      string `json:"error_type"`
	ErrnoName string `json:"errno_name"`
}

func (e *OpError) Error() string {
	where := e.Operation
	if where == "" {
		where = "file operation"
	}
	if e.Path != "" {
		return "aio: " + where + " " + e.Path + ": " + e.Message
	}
	return "aio: " + where + ": " + e.Message
}

// IsNotFound reports whether err came from a path that does not exist.
func IsNotFound(err error) bool {
	var op *OpError
	if errors.As(err, &op) {
		return op.Type == "not_found" || op.ErrnoName == "ENOENT"
	}
	return false
}

// fileRequest posts to one /v1/file/* endpoint and unwraps the envelope.
func (c *Client) fileRequest(ctx context.Context, path string, body any, out any) error {
	var reply struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, path, body, &reply); err != nil {
		return err
	}
	if !reply.Success {
		var op OpError
		if len(reply.Data) > 0 && string(reply.Data) != "null" {
			if err := json.Unmarshal(reply.Data, &op); err == nil && op.Message != "" {
				return &op
			}
		}
		return fmt.Errorf("aio: %s: %s", path, truncate(reply.Message, 300))
	}
	if out == nil || len(reply.Data) == 0 || string(reply.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(reply.Data, out); err != nil {
		return fmt.Errorf("aio: decode %s: %w: %s", path, err, truncate(string(reply.Data), 200))
	}
	return nil
}

// FileContent is one file as the sandbox reports it.
type FileContent struct {
	Content string `json:"content"`
	// File is the path the API echoes. Older builds called it "path".
	File string `json:"file"`
	Path string `json:"path"`
}

// FilePath is the path the sandbox read, whichever field it used.
func (f FileContent) FilePath() string {
	if f.File != "" {
		return f.File
	}
	return f.Path
}

// ReadFile reads a UTF-8 file from the sandbox.
func (c *Client) ReadFile(ctx context.Context, path string) (FileContent, error) {
	var out FileContent
	if err := c.fileRequest(ctx, "/v1/file/read", map[string]any{"file": path}, &out); err != nil {
		return FileContent{}, err
	}
	return out, nil
}

// WriteFile writes a UTF-8 file inside the sandbox. Parent directories are
// created for you: the live image does it, it is not assumed.
func (c *Client) WriteFile(ctx context.Context, path, content string) error {
	return c.fileRequest(ctx, "/v1/file/write",
		map[string]any{"file": path, "content": content, "encoding": "utf-8"}, nil)
}

// WriteBytes writes arbitrary bytes. Anything that is not valid UTF-8 travels
// base64 encoded, because a JSON string cannot carry the bytes.
func (c *Client) WriteBytes(ctx context.Context, path string, body []byte) error {
	if utf8.Valid(body) {
		return c.WriteFile(ctx, path, string(body))
	}
	return c.fileRequest(ctx, "/v1/file/write", map[string]any{
		"file":     path,
		"content":  base64.StdEncoding.EncodeToString(body),
		"encoding": "base64",
	}, nil)
}

// Node is one entry of a directory listing or one glob hit.
type Node struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
	// Modified is zero when the sandbox did not report a usable timestamp.
	Modified time.Time
}

// UnmarshalJSON accepts the spellings this API has used: the running image
// (1.11.0) reports `is_directory` and a unix-seconds `modified_time`, while the
// SDK types still say `is_dir`. Pinning either one alone mis-decodes the other,
// and a directory decoded as a file cannot be opened at all.
func (n *Node) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Name     string          `json:"name"`
		Path     string          `json:"path"`
		IsDir    bool            `json:"is_directory"`
		IsDirOld bool            `json:"is_dir"`
		Size     *int64          `json:"size"`
		Modified json.RawMessage `json:"modified_time"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	n.Name = wire.Name
	n.Path = wire.Path
	n.IsDir = wire.IsDir || wire.IsDirOld
	if wire.Size != nil {
		n.Size = *wire.Size
	}
	n.Modified = parseStamp(wire.Modified)
	return nil
}

// parseStamp reads the modified_time field, which the image sends as unix
// seconds in a string and the openapi describes as ISO 8601.
func parseStamp(raw json.RawMessage) time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return time.Time{}
		}
		if secs, err := strconv.ParseInt(text, 10, 64); err == nil {
			return time.Unix(secs, 0).UTC()
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if ts, err := time.Parse(layout, text); err == nil {
				return ts.UTC()
			}
		}
		return time.Time{}
	}
	var secs int64
	if err := json.Unmarshal(raw, &secs); err == nil {
		return time.Unix(secs, 0).UTC()
	}
	return time.Time{}
}

// List lists a directory inside the sandbox.
//
// Hidden entries are always requested: the caller decides what to show, because
// "show ignored files" is a per-request flag of the panel and a listing that
// cannot see a file can never reveal it.
func (c *Client) List(ctx context.Context, path string) ([]Node, error) {
	var out struct {
		Files []Node `json:"files"`
	}
	if err := c.fileRequest(ctx, "/v1/file/list",
		map[string]any{"path": path, "show_hidden": true}, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// GlobOptions is the per-call configuration of Glob.
type GlobOptions struct {
	// IncludeHidden also matches dot files and directories.
	IncludeHidden bool
	// FilesOnly drops directories from the answer.
	FilesOnly bool
	// MaxResults caps the answer; zero leaves the server default.
	MaxResults int
	// Exclude holds glob patterns (e.g. "node_modules", "*.min.js").
	Exclude []string
}

// Glob matches files and directories under path.
//
// It is the only endpoint that reports size and mtime for a single path, which
// is why ReadFile uses an exact-name glob instead of shelling out to stat.
func (c *Client) Glob(ctx context.Context, path, pattern string, opt GlobOptions) ([]Node, error) {
	body := map[string]any{
		"path":             path,
		"pattern":          pattern,
		"include_hidden":   opt.IncludeHidden,
		"files_only":       opt.FilesOnly,
		"include_metadata": true,
	}
	if opt.MaxResults > 0 {
		body["max_results"] = opt.MaxResults
	}
	if len(opt.Exclude) > 0 {
		body["exclude"] = opt.Exclude
	}
	var out struct {
		Files []Node `json:"files"`
	}
	if err := c.fileRequest(ctx, "/v1/file/glob", body, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// GrepMatch is one content hit.
type GrepMatch struct {
	File string
	Line int
	Text string
}

// GrepResult is a content search as the sandbox reports it.
type GrepResult struct {
	Matches   []GrepMatch
	Truncated bool
}

// Grep searches file contents under path. fixedStrings turns off regex
// interpretation, which is how a literal search still works when the query is
// not a valid pattern. The sandbox's ripgrep honours .gitignore, like the host
// implementation does.
func (c *Client) Grep(ctx context.Context, path, pattern string, fixedStrings bool, maxResults int) (GrepResult, error) {
	body := map[string]any{
		"path":          path,
		"pattern":       pattern,
		"recursive":     true,
		"fixed_strings": fixedStrings,
	}
	if maxResults > 0 {
		body["max_results"] = maxResults
	}
	var out struct {
		Matches []struct {
			File string `json:"file"`
			Line int    `json:"line_number"`
			Text string `json:"line_content"`
		} `json:"matches"`
		Truncated bool `json:"truncated"`
	}
	if err := c.fileRequest(ctx, "/v1/file/grep", body, &out); err != nil {
		return GrepResult{}, err
	}
	result := GrepResult{Truncated: out.Truncated, Matches: make([]GrepMatch, 0, len(out.Matches))}
	for _, m := range out.Matches {
		result.Matches = append(result.Matches, GrepMatch{File: m.File, Line: m.Line, Text: m.Text})
	}
	return result, nil
}

// Delete removes a path, recursively.
//
// The file API has no delete endpoint (checked against the openapi and the live
// image), so this is the one file operation that is a shell command.
func (c *Client) Delete(ctx context.Context, path, cwd string) error {
	res, err := c.Exec(ctx, deleteCommand(path), cwd)
	if err != nil {
		return err
	}
	if res.ExitCode == 3 {
		return &OpError{Path: path, Operation: "delete", Message: "no such file or directory", Type: "not_found", ErrnoName: "ENOENT"}
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("aio: delete %s: exit %d: %s", path, res.ExitCode, truncate(strings.TrimSpace(res.Output), 300))
	}
	return nil
}

// Navigate points the sandbox's browser at a URL.
func (c *Client) Navigate(ctx context.Context, target string) error {
	return c.request(ctx, http.MethodPost, "/v1/browser/page/navigate",
		map[string]any{"url": target}, nil)
}

// Screenshot returns a PNG of the sandbox's browser.
//
// GET, not POST — the running image answers 405 to a POST, and the openapi agrees. The
// body is base64 inside a JSON envelope, which is why this is not a byte stream.
func (c *Client) Screenshot(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/v1/browser/page/screenshot?format=png", nil)
	if err != nil {
		return nil, fmt.Errorf("aio: screenshot: %w", err)
	}
	if c.key != "" {
		req.Header.Set("X-AIO-API-Key", c.key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aio: screenshot: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("aio: screenshot: %w", err)
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("aio: screenshot: %d: %s", res.StatusCode, truncate(string(body), 200))
	}
	// The endpoint answers raw image bytes when the browser is up, and a JSON error
	// envelope when it is not — so a JSON-looking body is decoded for its message rather
	// than handed back as a corrupt PNG.
	if len(body) > 0 && body[0] == '{' {
		var envelope struct {
			Message string `json:"message"`
			Data    struct {
				Image string `json:"image"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if envelope.Data.Image != "" {
				return decodeBase64Image(envelope.Data.Image)
			}
			return nil, fmt.Errorf("aio: screenshot: %s", truncate(envelope.Message, 200))
		}
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("aio: screenshot returned nothing")
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Quote wraps s so a POSIX shell reads it as one literal word. Single quotes are
// the only form that does not interpret anything inside, and the one character
// they cannot contain is closed, escaped and reopened.
//
// Every command in this package is built from a shell string, so this is the
// boundary that keeps a file name from becoming syntax.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// deleteCommand removes path recursively, distinguishing "was not there" (exit
// 3) from "could not remove it". `rm -rf` alone cannot tell those apart, and a
// silent success on a missing file would make the panel lie about the delete.
func deleteCommand(path string) string {
	q := Quote(path)
	return "if [ -e " + q + " ]; then rm -rf -- " + q + "; else echo PF_MISSING; exit 3; fi"
}

// decodeBase64Image accepts both a bare base64 string and a data: URL, because the API
// has answered with each of them across versions.
func decodeBase64Image(encoded string) ([]byte, error) {
	if comma := strings.Index(encoded, ","); comma >= 0 && strings.Contains(encoded[:comma], "base64") {
		encoded = encoded[comma+1:]
	}
	return base64.StdEncoding.DecodeString(encoded)
}

// Shell sessions are the container's terminal.
//
// Unlike a PTY this is a command-oriented session: you write input, then read what the
// session printed. That is enough for the panel — the terminal sends a line and echoes
// output — and it is what the image actually offers.

// ShellSession is one long-lived shell inside the sandbox.
type ShellSession struct {
	ID      string `json:"id"`
	Cwd     string `json:"-"`
	Command string `json:"command"`
	Output  string `json:"output"`
	Status  string `json:"status"`
	Exit    *int   `json:"exit_code"`
}

// CreateShellSession opens a shell session in dir.
func (c *Client) CreateShellSession(ctx context.Context, id, dir string) (ShellSession, error) {
	body := map[string]any{"id": id}
	if strings.TrimSpace(dir) != "" {
		body["exec_dir"] = dir
	}
	var out struct {
		Data ShellSession `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/shell/sessions/create", body, &out); err != nil {
		return ShellSession{}, err
	}
	if out.Data.ID == "" {
		out.Data.ID = id
	}
	return out.Data, nil
}

// WriteShell sends input to a session. An empty input with pressEnter runs the line the
// user typed.
func (c *Client) WriteShell(ctx context.Context, id, input string, pressEnter bool) error {
	return c.request(ctx, http.MethodPost, "/v1/shell/write", map[string]any{
		"id":          id,
		"input":       input,
		"press_enter": pressEnter,
	}, nil)
}

// ViewShell reads what a session has printed so far.
func (c *Client) ViewShell(ctx context.Context, id string) (ShellSession, error) {
	var out struct {
		Data ShellSession `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/shell/view", map[string]any{"id": id}, &out); err != nil {
		return ShellSession{}, err
	}
	return out.Data, nil
}
