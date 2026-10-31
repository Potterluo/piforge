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
	"sync"
	"sync/atomic"
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
	// stream is the client for endpoints that answer with a file body. The
	// request client's Timeout covers the whole transfer, which would cut a
	// large download off mid-stream; here only the request context bounds it.
	stream *http.Client
	// Root is the workspace directory inside the container ("/home/gem" upstream).
	Root string

	// Exec runs commands in a small pool of *pinned* sessions. See Exec: the
	// image creates a shell session for every exec that does not name one, and
	// it evicts the oldest idle session once ten exist — which is how the user's
	// terminal was being killed mid-session (measured, docs/HANDOFF.md).
	execSessions []string
	execMu       sync.Mutex
	execNext     int
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
	// ExecSessions is how many pinned shell sessions Exec uses (default 2).
	// Each one serializes the commands it runs — the image holds a per-session
	// exec lock — so more sessions mean more parallel file/git work. Every
	// session counts against the image's ten-session cap, which is shared with
	// the user's terminals.
	ExecSessions int
	// ExecSessionPrefix names the pinned sessions (default "pf-exec").
	ExecSessionPrefix string
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
	count := cfg.ExecSessions
	if count <= 0 {
		count = 2
	}
	if count > 4 {
		count = 4
	}
	prefix := strings.TrimSpace(cfg.ExecSessionPrefix)
	if prefix == "" {
		prefix = "pf-exec"
	}
	sessions := make([]string, 0, count)
	for i := 0; i < count; i++ {
		sessions = append(sessions, fmt.Sprintf("%s-%d", prefix, i))
	}
	// Start each client at a different slot: the components build their own
	// clients for the same sandbox, and if all of them started at slot 0 every
	// file operation would queue behind every other one (the image serializes
	// the commands of one session).
	slot := int(clientSeq.Add(1)-1) % len(sessions)
	return &Client{
		base:         base,
		key:          strings.TrimSpace(cfg.APIKey),
		http:         &http.Client{Timeout: timeout},
		stream:       &http.Client{},
		Root:         root,
		execSessions: sessions,
		execNext:     slot,
	}, nil
}

// clientSeq spreads clients across the pinned exec sessions.
var clientSeq atomic.Uint64

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
		// Typed, not a formatted string: the callers that have to tell "the
		// sandbox refused this" from "the sandbox is not there yet" need the
		// status, and a string cannot carry one.
		return &HTTPError{Status: res.StatusCode, Path: method + " " + path, Message: truncate(string(payload), 300)}
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
//
// The request names a session. That is not an optimisation: without an `id` the
// image creates a *new* shell session for every exec, keeps at most ten, and
// evicts the oldest idle one to make room. A provisioning pass or an agent
// startup runs dozens of execs, so the picture was a storm of sessions that
// evicted whatever was oldest and idle — which is exactly what an open terminal
// is (measured on the isolated deployment: the user's shell was created at
// 04:35:57 and evicted at 04:36:01 while `Exec` calls were still arriving, and
// the panel went dead). Pinned sessions bound the count to the pool size.
func (c *Client) Exec(ctx context.Context, command string, cwd string) (ShellResult, error) {
	body := map[string]any{"command": command, "async_mode": false}
	if dir := strings.TrimSpace(cwd); dir != "" {
		body["cwd"] = dir
		command = "cd -- " + Quote(dir) + " && " + command
		body["command"] = command
	}
	id := c.nextExecSession()
	if !c.sessionEnsured(id) {
		if err := c.createSession(ctx, id, ""); err != nil {
			return ShellResult{}, err
		}
		markSessionEnsured(c.base, id)
	}
	result, err := c.execIn(ctx, id, body)
	if err != nil && isMissingSession(err) {
		// The session was evicted or the container restarted under it. One
		// re-create, one retry: the command itself is stateless here, so this is
		// safe, and it keeps a long-lived client working across a sandbox restart.
		forgetSessionEnsured(c.base, id)
		if cerr := c.createSession(ctx, id, ""); cerr != nil {
			return ShellResult{}, cerr
		}
		markSessionEnsured(c.base, id)
		result, err = c.execIn(ctx, id, body)
	}
	return result, err
}

// execIn runs one command in an existing pinned session.
func (c *Client) execIn(ctx context.Context, id string, body map[string]any) (ShellResult, error) {
	body["id"] = id
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

// nextExecSession round-robins over the pinned pool.
func (c *Client) nextExecSession() string {
	if len(c.execSessions) == 0 {
		return ""
	}
	c.execMu.Lock()
	defer c.execMu.Unlock()
	id := c.execSessions[c.execNext%len(c.execSessions)]
	c.execNext++
	return id
}

// createSession makes sure a pinned session exists. The endpoint is idempotent:
// an existing id answers "Session had created successfully".
func (c *Client) createSession(ctx context.Context, id, dir string) error {
	_, err := c.CreateShellSession(ctx, id, dir)
	return err
}

// ensuredShells remembers which (sandbox, session) pairs this process has already
// created. It is process-wide because every component builds its own client for
// the same container — the file panel, the liveness probe, the terminal — and a
// per-client cache would make each of them pay a create per call.
var ensuredShells sync.Map // "base|id" -> struct{}

func shellKey(base, id string) string { return base + "|" + id }

func (c *Client) sessionEnsured(id string) bool {
	_, ok := ensuredShells.Load(shellKey(c.base, id))
	return ok
}

func markSessionEnsured(base, id string) { ensuredShells.Store(shellKey(base, id), struct{}{}) }

func forgetSessionEnsured(base, id string) { ensuredShells.Delete(shellKey(base, id)) }

// isMissingSession reports the 404 the image answers for an id it does not have.
func isMissingSession(err error) bool {
	return StatusOf(err) == http.StatusNotFound
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
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound
}

// HTTPError is a non-2xx answer from an endpoint that returns a raw body
// instead of the file API's usual 200-with-success:false envelope. The live
// download endpoint answers 404 for a missing path and 400 for a directory.
type HTTPError struct {
	Status  int
	Path    string
	Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("aio: %s: %d: %s", e.Path, e.Status, e.Message)
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

// OpenFile opens a file for a raw byte stream through the download endpoint.
//
// This is a GET of /v1/file/download?path=..., which the 1.11.0 image answers
// with the bytes themselves, a Content-Length and range support — not with the
// JSON envelope /v1/file/read uses. ReadFile cannot be used for a binary file:
// it hands the bytes through a UTF-8 decoder and fails on the first invalid
// sequence, which is exactly the bug this call exists to avoid.
//
// size is the response's Content-Length, or -1 when the server omitted it. The
// caller must close the reader.
func (c *Client) OpenFile(ctx context.Context, path string) (io.ReadCloser, int64, error) {
	endpoint := c.base + "/v1/file/download?path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, -1, fmt.Errorf("aio: download %s: %w", path, err)
	}
	if c.key != "" {
		req.Header.Set("X-AIO-API-Key", c.key)
	}
	// The bytes are the payload: do not let the transport negotiate gzip and
	// transparently decode it, which would make Content-Length a lie.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.stream.Do(req)
	if err != nil {
		return nil, -1, fmt.Errorf("aio: download %s: %w", path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, -1, downloadFailure(res, path)
	}
	return res.Body, res.ContentLength, nil
}

// ReadPrefix returns up to n leading bytes of path without decoding them as
// text. It is the probe ReadFile cannot be: /v1/file/read runs the bytes
// through a UTF-8 codec, which rejects a PNG at its first byte. The download
// endpoint carries raw bytes, and the 1.11.0 image honours a Range header with
// 206 + content-range, so the preview can classify a file by its first bytes
// instead of asking for a decode that is guaranteed to fail.
//
// A server that ignores Range is still safe: the read is bounded by n.
func (c *Client) ReadPrefix(ctx context.Context, path string, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	endpoint := c.base + "/v1/file/download?path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("aio: download %s: %w", path, err)
	}
	if c.key != "" {
		req.Header.Set("X-AIO-API-Key", c.key)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", "bytes=0-"+strconv.FormatInt(n-1, 10))
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aio: download %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, downloadFailure(res, path)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, n))
	if err != nil {
		return nil, fmt.Errorf("aio: read %s: %w", path, err)
	}
	return raw, nil
}

// downloadFailure turns a non-2xx download answer into HTTPError, preferring
// the JSON envelope's message when the server sent one.
func downloadFailure(res *http.Response, path string) *HTTPError {
	payload, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	message := strings.TrimSpace(string(payload))
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &envelope) == nil && envelope.Message != "" {
		message = envelope.Message
	}
	return &HTTPError{Status: res.StatusCode, Path: path, Message: message}
}

// IsDecodeError reports whether err is the file API refusing to read a binary
// file as UTF-8. The live image types that answer as error_type "decode_error"
// (exception_type UnicodeDecodeError); matching the type rather than the text
// keeps a genuine I/O or permission failure an error.
func IsDecodeError(err error) bool {
	var op *OpError
	return errors.As(err, &op) && op.Type == "decode_error"
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
//
// The running image answers `session_id`, not `id` (probed on 1.11.0). Both are
// kept: a create whose answer carries a generated id must be able to tell the
// caller which session now exists, or every follow-up call addresses a session
// the container never registered.
type ShellSession struct {
	ID        string `json:"-"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"-"`
	Command   string `json:"command"`
	Output    string `json:"output"`
	Status    string `json:"status"`
	Exit      *int   `json:"exit_code"`
}

// ShellID is the id of the session this answer describes, whichever field the
// image used.
func (s ShellSession) ShellID() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	return s.ID
}

// CreateShellSession opens a shell session in dir.
//
// The returned session id is the one the container actually registered: the
// image may answer with a different id than the one requested (it reuses a
// pre-warmed session and stamps the requested id onto it, and future versions
// may generate their own). Callers must address the session with this id.
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
	out.Data.ID = out.Data.ShellID()
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
	out.Data.ID = out.Data.ShellID()
	if out.Data.ID == "" {
		out.Data.ID = id
	}
	return out.Data, nil
}

// StatusOf reports the HTTP status behind err, or 0 when err did not come from
// an HTTP answer (a DNS failure, a refused connection, a cancelled context).
func StatusOf(err error) int {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status
	}
	return 0
}
