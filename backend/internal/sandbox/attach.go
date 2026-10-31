package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Attaching to a process inside a sandbox: the byte channel the agent needs.
//
// The panels do not need this. They ask the sandbox's own HTTP API for files,
// git and shell output, which is why they already work for every remote
// placement. The agent is different: `pi --mode rpc` is a long-lived process
// that speaks JSONL over stdin/stdout, and the control plane's bridge
// (internal/pi) frames that stream itself. There is no HTTP request to make per
// record and no request/response shape that fits.
//
// So the provider exposes the container runtime's own stdio primitive — the same
// one `docker exec -i` gives a human — and nothing else:
//
//	POST /v1/sandboxes/{id}/attach  {argv, dir, env, user}
//
// The response is a hijacked, unbuffered, full-duplex byte stream; stdout is the
// response body and stdin is the request's write side. Auth is the runner's
// existing bearer token. docs/EXECUTION-BOUNDARY.md §2 and §9 explain why this
// is the stage-1 choice, what it costs, and why the sandbox's line-oriented
// /v1/shell/exec is not usable for it.
//
// What this is NOT: a second shell for users, a way to read files, or an
// endpoint that survives the caller. Close it and the process it started gets
// EOF on stdin and exits; that is the whole lifecycle contract.

// AttachSpec is one process to start inside an existing sandbox.
//
// Argv is executed directly (never through a shell), so a record's text can
// never become syntax. Dir and User are the sandbox-side working directory and
// account; Env is a list of KEY=VALUE entries added to the container's own
// environment.
type AttachSpec struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir,omitempty"`
	Env  []string `json:"env,omitempty"`
	User string   `json:"user,omitempty"`
}

// Attacher is the optional provider capability behind AttachSpec.
//
// It is separate from contracts.SandboxProvider and from Remote on purpose: a
// provider that cannot attach (the local one) is still a complete provider, and
// a caller that needs to attach says so with a type assertion instead of
// pretending every provider has a process namespace.
type Attacher interface {
	Attach(ctx context.Context, sandboxID string, spec AttachSpec) (io.ReadWriteCloser, error)
}

// ReattachSpec is one *existing* process to connect to inside a sandbox.
//
// Stage 3 of docs/EXECUTION-BOUNDARY.md puts a supervisor inside the sandbox
// that owns the agent's stdio (internal/agentsupervisor). The agent therefore
// outlives the control plane, and the control plane needs a verb that means "get
// me the stdio of the process that is already there" rather than "start one".
// The two are deliberately separate verbs even though the transport is the same
// docker exec primitive: a reattach that starts a second agent would be the one
// failure this stage exists to prevent, so it must not be expressible as an
// attach with a different argv.
//
// Socket is the Unix socket inside the sandbox. Helper is the argv prefix that
// connects to it (the sandbox's copy of piforged, `agent-attach`); the provider
// appends `-socket <socket>`. Both are explicit because the runner has no way to
// know either: the socket is named by the control plane's session, and the helper
// lives in the sandbox image.
type ReattachSpec struct {
	Socket string   `json:"socket"`
	Helper []string `json:"helper,omitempty"`
	Dir    string   `json:"dir,omitempty"`
	Env    []string `json:"env,omitempty"`
	User   string   `json:"user,omitempty"`
}

// Reattacher is the optional provider capability behind ReattachSpec.
type Reattacher interface {
	Reattach(ctx context.Context, sandboxID string, spec ReattachSpec) (io.ReadWriteCloser, error)
}

var (
	_ Attacher   = (*Docker)(nil)
	_ Attacher   = (*Runner)(nil)
	_ Reattacher = (*Docker)(nil)
	_ Reattacher = (*Runner)(nil)
)

// reattachArgv is the one place that turns a ReattachSpec into an argv. It is
// shared (as a shape) by both providers so a spec that means one thing here
// cannot mean another over HTTP.
func reattachArgv(spec ReattachSpec) ([]string, error) {
	socket := strings.TrimSpace(spec.Socket)
	if socket == "" {
		return nil, fmt.Errorf("reattach needs a socket")
	}
	if len(spec.Helper) == 0 || strings.TrimSpace(spec.Helper[0]) == "" {
		return nil, fmt.Errorf("reattach needs a helper argv")
	}
	argv := append([]string{}, spec.Helper...)
	return append(argv, "-socket", socket), nil
}

// Reattach connects to the supervisor already listening on spec.Socket.
func (d *Docker) Reattach(ctx context.Context, sandboxID string, spec ReattachSpec) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sandbox: docker: reattach: %w", err)
	}
	argv, err := reattachArgv(spec)
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker: %w", err)
	}
	args := []string{"exec", "-i"}
	if user := strings.TrimSpace(spec.User); user != "" {
		args = append(args, "-u", user)
	}
	if dir := strings.TrimSpace(spec.Dir); dir != "" {
		args = append(args, "-w", dir)
	}
	for _, entry := range spec.Env {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		args = append(args, "-e", entry)
	}
	args = append(args, d.resolve(sandboxID))
	args = append(args, argv...)
	return startDockerExec(args)
}

// Attach starts argv inside the sandbox and returns its stdio.
//
// ctx bounds the *start*, not the stream: the agent outlives the HTTP request
// that asked for it, so cancelling ctx after Attach returns leaves the process
// running. Close ends it.
func (d *Docker) Attach(ctx context.Context, sandboxID string, spec AttachSpec) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sandbox: docker: attach: %w", err)
	}
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("sandbox: docker: attach needs an argv")
	}
	args := []string{"exec", "-i"}
	if user := strings.TrimSpace(spec.User); user != "" {
		args = append(args, "-u", user)
	}
	if dir := strings.TrimSpace(spec.Dir); dir != "" {
		args = append(args, "-w", dir)
	}
	for _, entry := range spec.Env {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		args = append(args, "-e", entry)
	}
	args = append(args, d.resolve(sandboxID))
	args = append(args, spec.Argv...)
	return startDockerExec(args)
}

// Attach asks the runner to start argv inside the sandbox and hands back the
// stream it hijacked.
//
// The HTTP exchange is written by hand because net/http's client cannot hand
// back a live connection: the response body IS the channel, and the request
// body must be finished before the stream starts. Hijacking is the server's half
// of the same decision (internal/sandboxrunner).
func (r *Runner) Attach(ctx context.Context, sandboxID string, spec AttachSpec) (io.ReadWriteCloser, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("sandbox: runner: attach needs an argv")
	}
	return r.stream(ctx, sandboxID, "attach", spec)
}

// Reattach asks the runner to connect to the supervisor already inside the
// sandbox. The stream it returns is the agent's stdio, exactly like Attach's.
func (r *Runner) Reattach(ctx context.Context, sandboxID string, spec ReattachSpec) (io.ReadWriteCloser, error) {
	if _, err := reattachArgv(spec); err != nil {
		return nil, fmt.Errorf("sandbox: runner: %w", err)
	}
	return r.stream(ctx, sandboxID, "reattach", spec)
}

// stream performs one hijacking request against a runner verb.
//
// The HTTP exchange is written by hand because net/http's client cannot hand
// back a live connection: the response body IS the channel, and the request
// body must be finished before the stream starts. Hijacking is the server's half
// of the same decision (internal/sandboxrunner).
func (r *Runner) stream(ctx context.Context, sandboxID, verb string, spec any) (io.ReadWriteCloser, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("sandbox: runner: encode %s: %w", verb, err)
	}
	base, err := url.Parse(r.baseURL)
	if err != nil {
		return nil, fmt.Errorf("sandbox: runner: %s: base url: %w", verb, err)
	}
	addr := base.Host
	if base.Port() == "" {
		if base.Scheme == "https" {
			addr = net.JoinHostPort(addr, "443")
		} else {
			addr = net.JoinHostPort(addr, "80")
		}
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	if base.Scheme == "https" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: base.Hostname(), MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("sandbox: runner: %s %s: %w", verb, sandboxID, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	req := &http.Request{
		Method: http.MethodPost,
		URL:    &url.URL{Scheme: base.Scheme, Host: base.Host, Path: sandboxResource(sandboxID, verb)},
		Host:   base.Host,
		Header: http.Header{
			"Authorization": {"Bearer " + r.cfg.Token},
			"Content-Type":  {"application/json"},
			"Accept":        {"application/octet-stream"},
		},
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: runner: %s %s: write request: %w", verb, sandboxID, err)
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: runner: %s %s: read response: %w", verb, sandboxID, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: runner: %s %s: HTTP %d: %s", verb, sandboxID, resp.StatusCode, truncateText(string(body), 300))
	}
	// The handshake is done: the stream is the agent, with no request deadline.
	_ = conn.SetDeadline(time.Time{})
	return &attachConn{conn: conn, out: resp.Body}, nil
}

// attachConn turns one hijacked TCP connection into an io.ReadWriteCloser:
// reads are the response body, writes go the other way on the same socket.
type attachConn struct {
	conn net.Conn
	out  io.ReadCloser
	once sync.Once
}

func (a *attachConn) Read(b []byte) (int, error)  { return a.out.Read(b) }
func (a *attachConn) Write(b []byte) (int, error) { return a.conn.Write(b) }

// CloseWrite half-closes the socket so the sandbox-side process sees EOF on
// stdin while its stdout stays readable — the same sequence a local child gets
// when its parent closes the pipe.
func (a *attachConn) CloseWrite() error {
	if tcp, ok := a.conn.(*net.TCPConn); ok {
		return tcp.CloseWrite()
	}
	return nil
}

func (a *attachConn) Close() error {
	var err error
	a.once.Do(func() {
		err = a.out.Close()
		if cerr := a.conn.Close(); err == nil {
			err = cerr
		}
	})
	return err
}

// startDockerExec runs `docker exec -i …` with its stdio exposed.
func startDockerExec(args []string) (io.ReadWriteCloser, error) {
	cmd := exec.Command("docker", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker: attach stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("sandbox: docker: attach stdout: %w", err)
	}
	// stderr is not part of the RPC stream (it would corrupt it), so it is kept
	// in a small tail for the error message when the process dies early.
	tail := newTail(8 << 10)
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("sandbox: docker: attach: %w", err)
	}
	return &execConn{cmd: cmd, in: stdin, out: stdout, err: tail}, nil
}

// execConn is a child process's stdio as a ReadWriteCloser.
type execConn struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	out       io.ReadCloser
	err       *tailWriter
	writeOnce sync.Once
	closeOnce sync.Once
	waitErr   error
}

func (e *execConn) Read(b []byte) (int, error)  { return e.out.Read(b) }
func (e *execConn) Write(b []byte) (int, error) { return e.in.Write(b) }

func (e *execConn) CloseWrite() error {
	e.writeOnce.Do(func() { _ = e.in.Close() })
	return nil
}

func (e *execConn) Close() error {
	e.closeOnce.Do(func() {
		_ = e.CloseWrite()
		waited := make(chan struct{})
		go func() {
			e.waitErr = e.cmd.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			_ = e.cmd.Process.Kill()
			<-waited
		}
		_ = e.out.Close()
	})
	return e.waitErr
}

// Error reports the process's stderr tail, for a caller that wants to explain a
// stream that ended immediately.
func (e *execConn) Error() string { return strings.TrimSpace(e.err.String()) }

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTail(max int) *tailWriter { return &tailWriter{max: max} }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
