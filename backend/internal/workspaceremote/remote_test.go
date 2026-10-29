package workspaceremote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// ---------------------------------------------------------------------------
// A sandbox small enough to live in a test: the AIO file API over a temp dir.
// ---------------------------------------------------------------------------

// fakeSandbox serves /v1/file/* and /v1/shell/exec from a host directory that
// stands in for /workspace. It is deliberately built from the shapes the live
// image answered (probed on 1.11.0), not from the SDK types: `is_directory`,
// unix-seconds `modified_time`, and HTTP 200 with success:false for failures.
type fakeSandbox struct {
	t       *testing.T
	root    string // host directory that stands for /workspace
	tmpRoot string // host directory that stands for /tmp

	mu     sync.Mutex
	execs  []string
	gitOut string // canned `git status` stdout; empty means "nothing staged"
}

func newFakeSandbox(t *testing.T) *fakeSandbox {
	t.Helper()
	return &fakeSandbox{t: t, root: t.TempDir(), tmpRoot: t.TempDir()}
}

func (f *fakeSandbox) server() *httptest.Server { return httptest.NewServer(f.handler()) }

func (f *fakeSandbox) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/file/list", f.handleList)
	mux.HandleFunc("/v1/file/read", f.handleRead)
	mux.HandleFunc("/v1/file/download", f.handleDownload)
	mux.HandleFunc("/v1/file/write", f.handleWrite)
	mux.HandleFunc("/v1/file/glob", f.handleGlob)
	mux.HandleFunc("/v1/file/grep", f.handleGrep)
	mux.HandleFunc("/v1/shell/exec", f.handleExec)
	return mux
}

func (f *fakeSandbox) execsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

func (f *fakeSandbox) host(container string) (string, bool) {
	clean := path.Clean(container)
	switch {
	case clean == "/workspace":
		return f.root, true
	case strings.HasPrefix(clean, "/workspace/"):
		return filepath.Join(f.root, filepath.FromSlash(strings.TrimPrefix(clean, "/workspace/"))), true
	case clean == "/tmp":
		return f.tmpRoot, true
	case strings.HasPrefix(clean, "/tmp/"):
		return filepath.Join(f.tmpRoot, filepath.FromSlash(strings.TrimPrefix(clean, "/tmp/"))), true
	}
	return "", false
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func okData(w http.ResponseWriter, data any) {
	writeJSON(w, map[string]any{"success": true, "message": "ok", "data": data})
}

func opError(w http.ResponseWriter, containerPath, op string, errno int) {
	name := "ENOENT"
	if errno != 2 {
		name = "EIO"
	}
	writeJSON(w, map[string]any{
		"success": false,
		"message": "Failed to " + op,
		"data": map[string]any{
			"path": containerPath, "operation": op,
			"message": "Failed to " + op, "error_type": "not_found",
			"errno": errno, "errno_name": name,
		},
	})
}

func (f *fakeSandbox) handleList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir, ok := f.host(body.Path)
	if !ok {
		opError(w, body.Path, "list", 2)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		opError(w, body.Path, "list", 2)
		return
	}
	files := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		info, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		var size any
		if !entry.IsDir() {
			size = info.Size()
		}
		files = append(files, map[string]any{
			"name":          entry.Name(),
			"path":          path.Join(body.Path, entry.Name()),
			"is_directory":  entry.IsDir(),
			"size":          size,
			"modified_time": strconv.FormatInt(info.ModTime().Unix(), 10),
		})
	}
	okData(w, map[string]any{"path": body.Path, "files": files})
}

func (f *fakeSandbox) handleRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File string `json:"file"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	hostPath, ok := f.host(body.File)
	if !ok {
		opError(w, body.File, "read", 2)
		return
	}
	raw, err := os.ReadFile(hostPath)
	if err != nil {
		opError(w, body.File, "read", 2)
		return
	}
	okData(w, map[string]any{"content": string(raw), "file": body.File})
}

func (f *fakeSandbox) handleDownload(w http.ResponseWriter, r *http.Request) {
	containerPath := r.URL.Query().Get("path")
	hostPath, ok := f.host(containerPath)
	if !ok {
		downloadError(w, http.StatusNotFound, "File does not exist: "+containerPath)
		return
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		downloadError(w, http.StatusNotFound, "File does not exist: "+containerPath)
		return
	}
	if info.IsDir() {
		// The live 1.11.0 image answers 400, not the file API's JSON envelope.
		downloadError(w, http.StatusBadRequest, "Path is not a file: "+containerPath)
		return
	}
	raw, err := os.ReadFile(hostPath)
	if err != nil {
		downloadError(w, http.StatusInternalServerError, "Failed to read file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(raw)), 10))
	_, _ = w.Write(raw)
}

func downloadError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": message})
}

func (f *fakeSandbox) handleWrite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File     string `json:"file"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	hostPath, ok := f.host(body.File)
	if !ok {
		opError(w, body.File, "write", 2)
		return
	}
	raw := []byte(body.Content)
	if body.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			f.t.Errorf("write: bad base64: %v", err)
			opError(w, body.File, "write", 5)
			return
		}
		raw = decoded
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		opError(w, body.File, "write", 5)
		return
	}
	if err := os.WriteFile(hostPath, raw, 0o644); err != nil {
		opError(w, body.File, "write", 5)
		return
	}
	okData(w, map[string]any{"file": body.File, "bytes_written": len(raw)})
}

func (f *fakeSandbox) handleGlob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path          string `json:"path"`
		Pattern       string `json:"pattern"`
		FilesOnly     bool   `json:"files_only"`
		IncludeHidden bool   `json:"include_hidden"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir, ok := f.host(body.Path)
	if !ok {
		opError(w, body.Path, "glob", 2)
		return
	}
	files := make([]map[string]any, 0)
	for _, hostPath := range f.glob(dir, body.Pattern, body.IncludeHidden) {
		info, err := os.Stat(hostPath)
		if err != nil {
			continue
		}
		if body.FilesOnly && info.IsDir() {
			continue
		}
		containerPath := path.Join(body.Path, filepath.ToSlash(mustRel(f.t, dir, hostPath)))
		var size any
		if !info.IsDir() {
			size = info.Size()
		}
		files = append(files, map[string]any{
			"path":          containerPath,
			"name":          info.Name(),
			"is_directory":  info.IsDir(),
			"size":          size,
			"modified_time": strconv.FormatInt(info.ModTime().Unix(), 10),
		})
	}
	okData(w, map[string]any{"path": body.Path, "pattern": body.Pattern, "files": files})
}

// glob implements the two shapes the workspace uses: an exact name (direct
// child), and "**/prefix*suffix" (any depth).
func (f *fakeSandbox) glob(dir, pattern string, includeHidden bool) []string {
	var out []string
	if strings.Contains(pattern, "**") {
		rest := strings.TrimPrefix(pattern, "**/")
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel := filepath.ToSlash(mustRel(f.t, dir, p))
			if rel == "." || rel == "" {
				return nil
			}
			if !includeHidden && strings.Contains("/"+rel+"/", "/.") {
				return nil
			}
			if matchAnySuffix(rest, rel) {
				out = append(out, p)
			}
			return nil
		})
		return out
	}
	if strings.Contains(pattern, "/") {
		matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pattern)))
		return matches
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !includeHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if ok, _ := path.Match(pattern, entry.Name()); ok {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	return out
}

func matchAnySuffix(pattern, rel string) bool {
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	for i := strings.IndexByte(rel, '/'); i >= 0 && i < len(rel)-1; i = strings.IndexByte(rel, '/') {
		rel = rel[i+1:]
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
	}
	return false
}

func (f *fakeSandbox) handleGrep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
		FixedStrings bool   `json:"fixed_strings"`
		MaxResults   int    `json:"max_results"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir, ok := f.host(body.Path)
	if !ok {
		opError(w, body.Path, "grep", 2)
		return
	}
	matches := make([]map[string]any, 0)
	limit := body.MaxResults
	if limit <= 0 {
		limit = 500
	}
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil || len(raw) > 1<<20 {
			return nil
		}
		for i, line := range strings.Split(string(raw), "\n") {
			hit := false
			if body.FixedStrings {
				hit = strings.Contains(line, body.Pattern)
			} else if re, cerr := regexp.Compile(body.Pattern); cerr == nil {
				hit = re.MatchString(line)
			}
			if !hit {
				continue
			}
			containerPath := path.Join(body.Path, filepath.ToSlash(mustRel(f.t, dir, p)))
			matches = append(matches, map[string]any{
				"file": containerPath, "line_number": i + 1, "line_content": line,
			})
			if len(matches) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		opError(w, body.Path, "grep", 5)
		return
	}
	okData(w, map[string]any{"path": body.Path, "matches": matches, "truncated": false})
}

var cdPrefix = regexp.MustCompile(`^cd -- '([^']*)' && `)

func (f *fakeSandbox) handleExec(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.execs = append(f.execs, body.Command)
	gitOut := f.gitOut
	f.mu.Unlock()

	command := body.Command
	if m := cdPrefix.FindStringSubmatch(command); m != nil {
		if _, ok := f.host(m[1]); !ok {
			f.t.Errorf("exec cwd %q is not under /workspace or /tmp", m[1])
		}
		command = command[len(m[0]):]
	}
	output, code := f.runShell(command, gitOut)
	writeJSON(w, map[string]any{
		"success": true,
		"data":    map[string]any{"output": output, "exit_code": code},
	})
}

// runShell understands only the commands files.go and exec.go emit. That is the
// point: the test fails if the workspace starts shelling out for something else.
func (f *fakeSandbox) runShell(command, gitOut string) (string, int) {
	switch {
	case strings.HasPrefix(command, "if [ -e "):
		m := regexp.MustCompile(`^if \[ -e '([^']*)' \]`).FindStringSubmatch(command)
		if m == nil {
			f.t.Fatalf("unparseable delete: %s", command)
		}
		hostPath, _ := f.host(m[1])
		if _, err := os.Lstat(hostPath); err != nil {
			return "PF_MISSING", 3
		}
		if err := os.RemoveAll(hostPath); err != nil {
			return err.Error(), 1
		}
		return "", 0

	case strings.HasPrefix(command, "head -c 8192 -- "):
		m := regexp.MustCompile(`^head -c 8192 -- '([^']*)' \| base64`).FindStringSubmatch(command)
		if m == nil {
			f.t.Fatalf("unparseable sample: %s", command)
		}
		hostPath, _ := f.host(m[1])
		raw, err := os.ReadFile(hostPath)
		if err != nil {
			return "", 1
		}
		if len(raw) > 8192 {
			raw = raw[:8192]
		}
		return base64.StdEncoding.EncodeToString(raw), 0

	case strings.Contains(command, "PFHEAD:"):
		head := regexp.MustCompile(`head -c \d+ -- '([^']*)'`).FindStringSubmatch(command)
		tail := regexp.MustCompile(`tail -c \d+ -- '([^']*)'`).FindStringSubmatch(command)
		if head == nil || tail == nil {
			f.t.Fatalf("unparseable preview: %s", command)
		}
		headPath, _ := f.host(head[1])
		tailPath, _ := f.host(tail[1])
		headRaw, herr := os.ReadFile(headPath)
		tailRaw, terr := os.ReadFile(tailPath)
		if herr != nil || terr != nil {
			return "", 1
		}
		return "PFHEAD:" + base64.StdEncoding.EncodeToString(headRaw) + "\n" +
			"PFTAIL:" + base64.StdEncoding.EncodeToString(tailRaw), 0

	case strings.Contains(command, "'git'"):
		return f.runGit(command, gitOut)
	}
	f.t.Fatalf("unexpected shell command: %s", command)
	return "", 1
}

func (f *fakeSandbox) runGit(command, gitOut string) (string, int) {
	// Every git invocation is `cd -- dir && git ...`; the interesting part is
	// which subcommand, so the assertions below stay readable.
	ok := func(stdout string, code int) (string, int) {
		// The wrapper script always exits 0; the git exit code travels in
		// PFEXIT, exactly like the real bridge.
		return fmt.Sprintf("PFEXIT:%d\nPFOUT:%s\nPFERR:%s", code,
			base64.StdEncoding.EncodeToString([]byte(stdout)),
			base64.StdEncoding.EncodeToString(nil)), 0
	}
	switch {
	case strings.Contains(command, "'check-ignore'"):
		return ok("", 1) // git's "none of these paths are ignored"
	case strings.Contains(command, "'status'"):
		if gitOut == "" {
			return ok("# branch.oid abc\n# branch.head main\n", 0)
		}
		return ok(gitOut, 0)
	case strings.Contains(command, "'rev-parse'"):
		return ok("abc1234\n", 0)
	case strings.Contains(command, "'config'"):
		return ok("PiForge\n", 0)
	case strings.Contains(command, "'commit'"):
		if strings.Contains(command, "'-m'") {
			return ok("", 0)
		}
		return ok("", 0)
	}
	return ok("", 0)
}

func mustRel(t *testing.T, base, p string) string {
	t.Helper()
	rel, err := filepath.Rel(base, p)
	if err != nil {
		t.Fatalf("rel %q %q: %v", base, p, err)
	}
	return rel
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type staticBackend struct{ target Target }

func (b staticBackend) Target(context.Context, string) (Target, error) { return b.target, nil }

// newTestWorkspace wires a real aio.Client to the fake server, the way
// cmd/piforged wires it to a container.
func newTestWorkspace(t *testing.T, fake *fakeSandbox, cfg Config) (*Workspace, *httptest.Server) {
	t.Helper()
	server := fake.server()
	t.Cleanup(server.Close)
	client, err := aio.New(aio.Config{BaseURL: server.URL, Root: "/workspace"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	backend := staticBackend{target: Target{Client: client, Root: "/workspace"}}
	cfg.Backend = backend
	if cfg.Git == nil {
		cfg.Git = gitx.New(gitx.Config{Exec: ShellExecutor{Backend: backend}.Run})
	}
	ws, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ws, server
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWriteThenReadSeesTheSameBytes(t *testing.T) {
	fake := newFakeSandbox(t)
	ws, _ := newTestWorkspace(t, fake, Config{})

	ctx := context.Background()
	body := []byte("hello from the panel\n")
	if err := ws.WriteFile(ctx, `C:\host\sandboxes\repo`, "src/notes.txt", body); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// The fake is a real directory, so the bytes on "the sandbox side" are
	// directly observable.
	stored, err := os.ReadFile(filepath.Join(fake.root, "src", "notes.txt"))
	if err != nil {
		t.Fatalf("read the sandbox copy: %v", err)
	}
	if string(stored) != string(body) {
		t.Fatalf("sandbox has %q", stored)
	}

	content, err := ws.ReadFile(ctx, `C:\host\sandboxes\repo`, "src/notes.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content.Body != string(body) || content.Size != int64(len(body)) {
		t.Fatalf("content = %+v", content)
	}
	if content.Binary || content.Truncated {
		t.Fatalf("classification = %+v", content)
	}
	if content.Modified.IsZero() {
		t.Fatal("the preview lost the modification time")
	}
}

func TestReadFileClassifiesMissingDirectoryAndBinary(t *testing.T) {
	fake := newFakeSandbox(t)
	ws, _ := newTestWorkspace(t, fake, Config{})
	ctx := context.Background()
	root := `C:\host\sandboxes\repo`

	if _, err := ws.ReadFile(ctx, root, "nope.txt"); !errors.Is(err, fsx.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(fake.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile(ctx, root, "dir"); !errors.Is(err, fsx.ErrIsDir) {
		t.Fatalf("directory error = %v", err)
	}

	// A NUL byte makes it binary; the preview body must stay empty.
	if err := os.WriteFile(filepath.Join(fake.root, "blob.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := ws.ReadFile(ctx, root, "blob.bin")
	if err != nil {
		t.Fatalf("ReadFile binary: %v", err)
	}
	if !content.Binary || content.Body != "" {
		t.Fatalf("binary preview = %+v", content)
	}

	// Larger than MaxInline (8 bytes here) means head+tail, marked truncated.
	long := strings.Repeat("line of text\n", 40)
	if err := os.WriteFile(filepath.Join(fake.root, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	small := Config{MaxInlineBytes: 8, MaxPreviewBytes: 4096, HeadBytes: 32, TailBytes: 32}
	wsSmall, _ := newTestWorkspace(t, fake, small)
	content, err = wsSmall.ReadFile(ctx, root, "long.txt")
	if err != nil {
		t.Fatalf("ReadFile large: %v", err)
	}
	if !content.Truncated || !strings.Contains(content.Body, fsx.TruncationMarker) {
		t.Fatalf("large preview = %+v", content)
	}
	if !strings.HasPrefix(content.Body, "line of text") || !strings.HasSuffix(content.Body, "line of text\n") {
		t.Fatalf("preview edges = %q", content.Body)
	}

	// Above MaxPreview the body is metadata only.
	wsMeta, _ := newTestWorkspace(t, fake, Config{MaxInlineBytes: 8, MaxPreviewBytes: 16})
	content, err = wsMeta.ReadFile(ctx, root, "long.txt")
	if err != nil {
		t.Fatalf("ReadFile metadata: %v", err)
	}
	if content.Body != "" || !content.Truncated || content.Language != "" {
		t.Fatalf("metadata preview = %+v", content)
	}
}

func TestOpenFileStreamsRawBytesWithoutTextDecoding(t *testing.T) {
	fake := newFakeSandbox(t)
	ws, _ := newTestWorkspace(t, fake, Config{})
	ctx := context.Background()
	root := `C:\host\sandboxes\repo`

	// A real PNG header plus bytes that are invalid UTF-8: /v1/file/read would
	// reject this, which is exactly why OpenFile must not use it.
	binary := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0xfe, 0x01}
	if err := os.WriteFile(filepath.Join(fake.root, "icon.png"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	body, size, err := ws.OpenFile(ctx, root, "icon.png")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("stream = %x, want %x", got, binary)
	}
	if size != int64(len(binary)) {
		t.Fatalf("size = %d, want %d", size, len(binary))
	}
	if execs := fake.execsSeen(); len(execs) != 0 {
		t.Fatalf("download shelled out instead of streaming: %v", execs)
	}

	if _, _, err := ws.OpenFile(ctx, root, "nope.png"); !errors.Is(err, fsx.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(fake.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.OpenFile(ctx, root, "dir"); !errors.Is(err, fsx.ErrIsDir) {
		t.Fatalf("directory error = %v", err)
	}
}

func TestTreeFiltersIgnoredEntriesAndCarriesGitLetters(t *testing.T) {
	fake := newFakeSandbox(t)
	fake.gitOut = "# branch.oid abc\n# branch.head main\n" +
		"1 .M N... 100644 100644 100644 aaa bbb notes.txt\n" +
		"? fresh.txt\n"
	ws, _ := newTestWorkspace(t, fake, Config{})
	ctx := context.Background()
	root := `C:\host\sandboxes\repo`

	for _, dir := range []string{".git", ".hidden", "node_modules", "src"} {
		if err := os.MkdirAll(filepath.Join(fake.root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"notes.txt":          "hello",
		"fresh.txt":          "new",
		"src/main.go":        "package main",
		".git/config":        "secret",
		".hidden/inside.txt": "hidden",
		"node_modules/x.js":  "module",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(fake.root, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	nodes, err := ws.Tree(ctx, root, "", 2)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("tree = %+v", nodes)
	}
	// Directories first, then case-insensitive by name.
	if !nodes[0].IsDir || nodes[0].Name != "src" || nodes[1].Name != "fresh.txt" {
		t.Fatalf("order = %s, %s, %s", nodes[0].Name, nodes[1].Name, nodes[2].Name)
	}
	if nodes[1].GitState != "U" || nodes[2].GitState != "M" {
		t.Fatalf("letters: fresh=%q notes=%q", nodes[1].GitState, nodes[2].GitState)
	}
	if len(nodes[0].Children) != 1 || nodes[0].Children[0].Path != "src/main.go" {
		t.Fatalf("children = %+v", nodes[0].Children)
	}
	for _, n := range nodes {
		if n.Name == ".git" || n.Name == ".hidden" || n.Name == "node_modules" {
			t.Fatalf("hidden entry leaked: %s", n.Name)
		}
	}

	// "Show ignored files" has to change what the server lists.
	all, err := ws.TreeWithOptions(ctx, root, "", 1, true)
	if err != nil {
		t.Fatalf("TreeWithOptions: %v", err)
	}
	names := map[string]bool{}
	for _, n := range all {
		names[n.Name] = true
	}
	if !names[".hidden"] || !names["node_modules"] || names[".git"] {
		t.Fatalf("includeIgnored names = %v", names)
	}

	// A child directory at the depth limit says so, so the client can lazy-load.
	page, err := ws.Tree(ctx, root, "", 1)
	if err != nil {
		t.Fatalf("Tree depth 1: %v", err)
	}
	if !page[0].IsDir || !page[0].Truncated {
		t.Fatalf("depth-limit node = %+v", page[0])
	}
}

func TestSearchMapsContentAndNames(t *testing.T) {
	fake := newFakeSandbox(t)
	ws, _ := newTestWorkspace(t, fake, Config{SearchLimit: 10})
	ctx := context.Background()
	root := `C:\host\sandboxes\repo`

	for name, body := range map[string]string{
		"a/b/c.txt":    "hello world\nsecond hello\n",
		"readme.md":    "hello\n",
		"src/other.go": "package other\n",
	} {
		full := filepath.Join(fake.root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := ws.Search(ctx, root, "hello", true)
	if err != nil {
		t.Fatalf("Search content: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("content hits = %+v", hits)
	}
	for _, hit := range hits {
		if strings.HasPrefix(hit.Path, "/") || hit.Line == 0 || hit.Text == "" {
			t.Fatalf("hit is not workspace-relative: %+v", hit)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })
	if hits[0].Path != "a/b/c.txt" || hits[0].Line != 1 {
		t.Fatalf("first hit = %+v", hits[0])
	}

	// A regex that does not compile falls back to a literal search.
	if _, err := ws.Search(ctx, root, "hel(o", true); err != nil {
		t.Fatalf("Search with a broken regex: %v", err)
	}

	names, err := ws.Search(ctx, root, "readme", false)
	if err != nil {
		t.Fatalf("Search names: %v", err)
	}
	if len(names) != 1 || names[0].Path != "readme.md" {
		t.Fatalf("name hits = %+v", names)
	}
	// The subsequence matcher is shared with the host implementation.
	subseq, err := ws.Search(ctx, root, "src/go", false)
	if err != nil {
		t.Fatalf("Search subsequence: %v", err)
	}
	if len(subseq) != 1 || subseq[0].Path != "src/other.go" {
		t.Fatalf("subsequence hits = %+v", subseq)
	}
}

func TestDeleteReportsMissingAndRemovesTrees(t *testing.T) {
	fake := newFakeSandbox(t)
	ws, _ := newTestWorkspace(t, fake, Config{})
	ctx := context.Background()
	root := `C:\host\sandboxes\repo`

	if err := ws.DeleteFile(ctx, root, "nope.txt"); !errors.Is(err, fsx.ErrNotExist) {
		t.Fatalf("delete missing = %v", err)
	}
	tree := filepath.Join(fake.root, "gone", "deep")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.DeleteFile(ctx, root, "gone"); err != nil {
		t.Fatalf("delete tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fake.root, "gone")); !os.IsNotExist(err) {
		t.Fatal("the directory survived the delete")
	}
	if err := ws.DeleteFile(ctx, root, ".git"); err == nil {
		t.Fatal("deleting git metadata was allowed")
	}
	if err := ws.WriteFile(ctx, root, "../escape.txt", []byte("x")); err == nil {
		t.Fatal("a traversal path was accepted")
	}
}

// The git bridge is the risky half: it must keep gitx's exit-code and stream
// semantics, because "nothing to commit" and the hunk parser depend on them.
func TestShellExecutorKeepsGitStreamsAndExitCodes(t *testing.T) {
	fake := newFakeSandbox(t)
	server := fake.server()
	defer server.Close()
	client, err := aio.New(aio.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	backend := staticBackend{target: Target{Client: client, Root: "/workspace"}}
	runner := gitx.New(gitx.Config{Exec: ShellExecutor{Backend: backend}.Run})
	ctx := context.Background()

	fake.gitOut = "# branch.oid abc\n# branch.head main\n# branch.ab +2 -1\n" +
		"1 .M N... 100644 100644 100644 aaa bbb notes.txt\n"
	status, err := runner.Status(ctx, `C:\host\sandboxes\repo`)
	if err != nil {
		t.Fatalf("Status through the sandbox: %v", err)
	}
	if status.Branch != "main" || status.Ahead != 2 || status.Behind != 1 || len(status.Changes) != 1 {
		t.Fatalf("status = %+v", status)
	}
	if status.Changes[0].Path != "notes.txt" || status.Changes[0].Worktree != "M" {
		t.Fatalf("change = %+v", status.Changes[0])
	}

	// stdin travels as a file inside the sandbox, and a failing command comes
	// back as a *CommandError carrying stderr.
	res, err := (ShellExecutor{Backend: backend}).Run(ctx, `C:\host\sandboxes\repo`,
		[]byte("patch body\n"), []string{"-c", "color.ui=false", "apply", "--cached", "-"})
	if err != nil {
		t.Fatalf("executor with stdin: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	stdinPath := ""
	for _, cmd := range fake.execsSeen() {
		if i := strings.Index(cmd, "/tmp/pf-git-stdin-"); i >= 0 {
			rest := cmd[i+len("/tmp/"):]
			stdinPath = "/tmp/" + strings.SplitN(rest, "'", 2)[0]
		}
	}
	if stdinPath == "" {
		t.Fatalf("no stdin file was used: %v", fake.execsSeen())
	}
	stored, err := os.ReadFile(filepath.Join(fake.tmpRoot, filepath.Base(stdinPath)))
	if err != nil {
		t.Fatalf("stdin file: %v", err)
	}
	if string(stored) != "patch body\n" {
		t.Fatalf("stdin = %q", stored)
	}

	// A non-zero git exit must surface stdout, stderr and the code separately.
	fake.gitOut = ""
	badRunner := gitx.New(gitx.Config{Exec: func(ctx context.Context, dir string, stdin []byte, argv []string) (gitx.Result, error) {
		return gitx.Result{}, &gitx.CommandError{Args: argv, Code: 128, Stderr: "fatal: not a git repository", Stdout: "partial"}
	}})
	if _, err := badRunner.Status(ctx, `C:\host\sandboxes\repo`); err == nil {
		t.Fatal("a failing git call returned no error")
	} else if gitx.ExitCode(err) != 128 || !strings.Contains(gitx.StderrOf(err), "not a git repository") {
		t.Fatalf("exit/stderr lost: %v (code %d)", err, gitx.ExitCode(err))
	}
}
