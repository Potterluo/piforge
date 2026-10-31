package sandboxrunner

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// echoAttacher stands in for the Docker provider: it starts an "agent" that
// echoes its stdin back upper-cased, so a test can prove the full-duplex stream
// survives the HTTP hijack.
type echoAttacher struct {
	mu   sync.Mutex
	spec sandbox.AttachSpec
}

func (e *echoAttacher) Attach(ctx context.Context, sandboxID string, spec sandbox.AttachSpec) (io.ReadWriteCloser, error) {
	e.mu.Lock()
	e.spec = spec
	e.mu.Unlock()
	in, inWriter := io.Pipe()   // test → "process stdin"
	out, outWriter := io.Pipe() // "process stdout" → test
	go func() {
		defer outWriter.Close()
		buf := make([]byte, 512)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				_, _ = outWriter.Write([]byte(strings.ToUpper(string(buf[:n]))))
			}
			if err != nil {
				return
			}
		}
	}()
	return &pipeStream{r: out, w: inWriter, closeIn: in, closeOut: out}, nil
}

func (e *echoAttacher) Spec() sandbox.AttachSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spec
}

type pipeStream struct {
	r        io.Reader
	w        io.Writer
	closeIn  io.Closer
	closeOut io.Closer
	once     sync.Once
}

func (p *pipeStream) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeStream) Write(b []byte) (int, error) { return p.w.Write(b) }

// CloseWrite is the stdin close the runner turns into EOF for the process.
func (p *pipeStream) CloseWrite() error { return p.w.(io.Closer).Close() }

func (p *pipeStream) Close() error {
	p.once.Do(func() { _ = p.closeIn.Close(); _ = p.closeOut.Close() })
	return nil
}

// attachProvider is the runner's fake provider plus the optional attach
// capability, so the same fake covers both "this provider can" and "it cannot".
type attachProvider struct {
	*fakeProvider
	attacher  sandbox.Attacher
	attachErr error
}

func (p *attachProvider) Attach(ctx context.Context, sandboxID string, spec sandbox.AttachSpec) (io.ReadWriteCloser, error) {
	if p.attachErr != nil {
		return nil, p.attachErr
	}
	if p.attacher == nil {
		return nil, sandbox.ErrNotFound
	}
	return p.attacher.Attach(ctx, sandboxID, spec)
}

// newRunnerClient points the real HTTP client at a test server.
func newRunnerClient(t *testing.T, provider Provider) *sandbox.Runner {
	t.Helper()
	srv := newTestServer(t, provider)
	runner, err := sandbox.NewRunner(sandbox.RunnerConfig{
		BaseURL:  srv.URL,
		Token:    testToken,
		DataRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new runner client: %v", err)
	}
	return runner
}

func TestAttachCarriesAFullDuplexStream(t *testing.T) {
	attacher := &echoAttacher{}
	runner := newRunnerClient(t, &attachProvider{fakeProvider: &fakeProvider{record: testRecord()}, attacher: attacher})

	stream, err := runner.Attach(context.Background(), "piforge-abc123", sandbox.AttachSpec{
		Argv: []string{"/opt/piforge/piforged", "agent-launcher", "--", "pi", "--mode", "rpc"},
		Dir:  "/workspace/repo",
		Env:  []string{"PI_CODING_AGENT_DIR=/workspace/.piforge/agents/ses_1"},
		User: "gem",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer stream.Close()

	spec := attacher.Spec()
	if got := strings.Join(spec.Argv, " "); got != "/opt/piforge/piforged agent-launcher -- pi --mode rpc" {
		t.Fatalf("argv reached the provider as %q", got)
	}
	if spec.Dir != "/workspace/repo" || spec.User != "gem" {
		t.Fatalf("dir/user reached the provider as %q/%q", spec.Dir, spec.User)
	}

	if _, err := stream.Write([]byte("{\"type\":\"prompt\"}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the stream never answered")
		}
		n, rerr := stream.Read(reply)
		if n > 0 {
			if !strings.Contains(string(reply[:n]), `{"TYPE":"PROMPT"}`) {
				t.Fatalf("stream answered %q", string(reply[:n]))
			}
			break
		}
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
	}
}

func TestAttachNeedsAToken(t *testing.T) {
	provider := &attachProvider{fakeProvider: &fakeProvider{record: testRecord()}, attacher: &echoAttacher{}}
	srv := newTestServer(t, provider)
	u, _ := url.Parse(srv.URL)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sandboxes/piforge-abc/attach", strings.NewReader(`{"argv":["pi"]}`))
	conn, err := http.DefaultTransport.(*http.Transport).DialContext(context.Background(), "tcp", u.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := req.Write(conn); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (the token is checked before the body)", resp.StatusCode)
	}
}

func TestAttachRejectsAnEmptyArgv(t *testing.T) {
	runner := newRunnerClient(t, &attachProvider{fakeProvider: &fakeProvider{record: testRecord()}, attacher: &echoAttacher{}})
	if _, err := runner.Attach(context.Background(), "piforge-abc", sandbox.AttachSpec{Argv: []string{" "}}); err == nil {
		t.Fatal("an empty argv must be refused by the runner")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v, want the runner's 400", err)
	}
}

// A provider without the capability answers 501, not 500: "this runner cannot
// do that" is different from "it tried and failed".
func TestAttachSaysSoWhenTheProviderCannot(t *testing.T) {
	runner := newRunnerClient(t, &fakeProvider{record: testRecord()})
	_, err := runner.Attach(context.Background(), "piforge-abc", sandbox.AttachSpec{Argv: []string{"pi"}})
	if err == nil || !strings.Contains(err.Error(), "501") {
		t.Fatalf("error = %v, want 501", err)
	}
}

// fakeReattacher stands in for the Docker provider's reattach: it records the
// spec and hands back a stream whose first line is a supervisor handshake,
// followed by whatever the test writes.
type fakeReattacher struct {
	mu   sync.Mutex
	spec sandbox.ReattachSpec
	body string
}

func (f *fakeReattacher) Reattach(ctx context.Context, sandboxID string, spec sandbox.ReattachSpec) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	f.spec = spec
	f.mu.Unlock()
	pr, pw := io.Pipe()
	go func() {
		if f.body != "" {
			_, _ = pw.Write([]byte(f.body))
		}
	}()
	return &pipeStream{r: pr, w: nopWriteCloser{}, closeIn: pr, closeOut: pw}, nil
}

func (f *fakeReattacher) Spec() sandbox.ReattachSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spec
}

// reattachProvider is attachProvider plus the reattach capability.
type reattachProvider struct {
	*attachProvider
	reattacher sandbox.Reattacher
}

func (p *reattachProvider) Reattach(ctx context.Context, sandboxID string, spec sandbox.ReattachSpec) (io.ReadWriteCloser, error) {
	return p.reattacher.Reattach(ctx, sandboxID, spec)
}

// A reattach carries the socket and the helper argv, and the bytes the supervisor
// writes are handed to the caller untouched: the control plane reads the
// handshake itself.
func TestReattachCarriesTheSocketAndTheStream(t *testing.T) {
	fake := &fakeReattacher{body: "{\"type\":\"ready\",\"session\":\"ses_1\",\"pid\":77}\n"}
	provider := &reattachProvider{
		attachProvider: &attachProvider{fakeProvider: &fakeProvider{record: testRecord()}},
		reattacher:     fake,
	}
	runner := newRunnerClient(t, provider)

	stream, err := runner.Reattach(context.Background(), "piforge-abc123", sandbox.ReattachSpec{
		Socket: "/workspace/.piforge/run/ses_1.sock",
		Helper: []string{"/opt/piforge/piforged", "agent-attach", "-session", "ses_1"},
		Dir:    "/workspace/repo",
		User:   "gem",
	})
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	defer stream.Close()

	spec := fake.Spec()
	if spec.Socket != "/workspace/.piforge/run/ses_1.sock" {
		t.Fatalf("socket reached the provider as %q", spec.Socket)
	}
	if got := strings.Join(spec.Helper, " "); got != "/opt/piforge/piforged agent-attach -session ses_1" {
		t.Fatalf("helper reached the provider as %q", got)
	}
	if spec.Dir != "/workspace/repo" || spec.User != "gem" {
		t.Fatalf("dir/user reached the provider as %q/%q", spec.Dir, spec.User)
	}

	line := make([]byte, 128)
	n, rerr := stream.Read(line)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if !strings.HasPrefix(string(line[:n]), `{"type":"ready"`) {
		t.Fatalf("the handshake was altered: %q", string(line[:n]))
	}
}

func TestReattachRequiresASocketAndAHelper(t *testing.T) {
	provider := &reattachProvider{
		attachProvider: &attachProvider{fakeProvider: &fakeProvider{record: testRecord()}},
		reattacher:     &fakeReattacher{},
	}
	runner := newRunnerClient(t, provider)
	if _, err := runner.Reattach(context.Background(), "piforge-abc", sandbox.ReattachSpec{Helper: []string{"piforged"}}); err == nil {
		t.Fatal("a reattach without a socket must be refused by the client")
	}
	if _, err := runner.Reattach(context.Background(), "piforge-abc", sandbox.ReattachSpec{Socket: "/s.sock"}); err == nil {
		t.Fatal("a reattach without a helper must be refused by the client")
	}
}

// A runner whose provider has no exec surface answers 501, and its message says
// which verb it cannot serve.
func TestReattachSaysSoWhenTheProviderCannot(t *testing.T) {
	runner := newRunnerClient(t, &fakeProvider{record: testRecord()})
	_, err := runner.Reattach(context.Background(), "piforge-abc", sandbox.ReattachSpec{
		Socket: "/workspace/.piforge/run/ses_1.sock",
		Helper: []string{"/opt/piforge/piforged", "agent-attach"},
	})
	if err == nil || !strings.Contains(err.Error(), "501") {
		t.Fatalf("error = %v, want 501", err)
	}
}

// nopWriteCloser is a write side that accepts and discards, so a fake stream has
// a closable stdin like a real process does.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }
