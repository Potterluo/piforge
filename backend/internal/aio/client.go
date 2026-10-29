package aio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
type Info struct {
	CDPURL    string `json:"cdp_url"`
	VNCURL    string `json:"vnc_url"`
	WorkSpace string `json:"workspace"`
	Version   string `json:"version"`
}

// BrowserInfo asks for the browser endpoint.
//
// It is the single most useful call in the integration: the CDP URL it returns is what
// turns "the panel's browser lives on the host" into "the panel's browser lives with the
// files".
func (c *Client) BrowserInfo(ctx context.Context) (Info, error) {
	var out Info
	err := c.request(ctx, http.MethodGet, "/v1/browser/info", nil, &out)
	return out, err
}

// ShellResult is what a command produced.
type ShellResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// Exec runs a shell command inside the sandbox.
func (c *Client) Exec(ctx context.Context, command string, cwd string) (ShellResult, error) {
	body := map[string]any{"command": command, "async_mode": false}
	if strings.TrimSpace(cwd) != "" {
		body["cwd"] = cwd
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

// FileContent is one file as the sandbox reports it.
type FileContent struct {
	Content string `json:"content"`
	Path    string `json:"path"`
}

// ReadFile reads a UTF-8 file from the sandbox.
func (c *Client) ReadFile(ctx context.Context, path string) (FileContent, error) {
	var out struct {
		Data FileContent `json:"data"`
		FileContent
	}
	err := c.request(ctx, http.MethodPost, "/v1/file/read", map[string]any{"file": path}, &out)
	if err != nil {
		return FileContent{}, err
	}
	if out.Data.Content != "" || out.Data.Path != "" {
		return out.Data, nil
	}
	return out.FileContent, nil
}

// WriteFile writes a file inside the sandbox.
func (c *Client) WriteFile(ctx context.Context, path, content string) error {
	return c.request(ctx, http.MethodPost, "/v1/file/write",
		map[string]any{"file": path, "content": content}, nil)
}

// Node is one entry of a directory listing.
type Node struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// List lists a directory inside the sandbox.
func (c *Client) List(ctx context.Context, path string) ([]Node, error) {
	var out struct {
		Data struct {
			Files []Node `json:"files"`
		} `json:"data"`
		Files []Node `json:"files"`
	}
	err := c.request(ctx, http.MethodPost, "/v1/file/list", map[string]any{"path": path}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Data.Files) > 0 {
		return out.Data.Files, nil
	}
	return out.Files, nil
}

// Navigate points the sandbox's browser at a URL.
func (c *Client) Navigate(ctx context.Context, target string) error {
	return c.request(ctx, http.MethodPost, "/v1/browser/page/navigate",
		map[string]any{"url": target}, nil)
}

// Screenshot returns a PNG/JPEG of the sandbox's browser.
//
// The API answers with base64 in a JSON envelope, which is why this is not a byte stream.
func (c *Client) Screenshot(ctx context.Context) ([]byte, error) {
	var out struct {
		Data struct {
			Image string `json:"image"`
		} `json:"data"`
		Image string `json:"image"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/browser/page/screenshot", map[string]any{}, &out); err != nil {
		return nil, err
	}
	encoded := out.Data.Image
	if encoded == "" {
		encoded = out.Image
	}
	if encoded == "" {
		return nil, fmt.Errorf("aio: screenshot returned nothing")
	}
	return decodeBase64Image(encoded)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// decodeBase64Image accepts both a bare base64 string and a data: URL, because the API
// has answered with each of them across versions.
func decodeBase64Image(encoded string) ([]byte, error) {
	if comma := strings.Index(encoded, ","); comma >= 0 && strings.Contains(encoded[:comma], "base64") {
		encoded = encoded[comma+1:]
	}
	return base64.StdEncoding.DecodeString(encoded)
}
