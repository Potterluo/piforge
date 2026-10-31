// Package agentsupervisor keeps one agent process alive inside a sandbox and
// lets a control plane that did not start it attach to its stdio.
//
// The problem it solves is ownership. Stage 1 of docs/EXECUTION-BOUNDARY.md
// launched `pi --mode rpc` with the container runtime's exec stream as its
// stdio, so the *control plane* owned the agent's file descriptors: restart the
// control plane and the stream closes, `pi` sees EOF, exits, and the session is
// dead for a reason that has nothing to do with the agent. Stage 3 interposes a
// process that lives in the sandbox and owns those descriptors instead:
//
//	control plane ──runner attach──▶ piforged agent-attach ──unix socket──▶ supervisor ──pipes──▶ agent-launcher ──▶ pi
//
// The supervisor is a *detached* child of the container (its own session, no
// controlling terminal, stdio on a log file). Its stdin is not the exec stream,
// so a closed exec stream cannot reach the agent; it notices the client is gone
// and keeps draining the agent's stdout into a bounded replay buffer instead.
// A restarted control plane dials the Unix socket in
// /workspace/.piforge/run/<session>.sock and is handed the bytes it missed
// followed by the live stream.
//
// What is promised and what is not:
//
//   - the agent process, its heartbeat marker and its conversation outlive a
//     control-plane restart;
//   - the control plane can reattach and receives the bytes produced while it
//     was gone, up to Backlog bytes (records are whole lines, so the resumed
//     stream is always valid JSONL);
//   - a control plane gone for longer than the buffer loses the *bytes*, not
//     the agent: the agent's own session file inside the sandbox is the
//     conversation, and PRD §7.4.5 forbids replaying a run on the control
//     plane's side;
//   - the first few bytes a previous client had in flight at the instant it
//     died are re-sent, because there is no way to know what it read. That is
//     bounded by one socket buffer, not by the run.
package agentsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/liveness"
)

// The three modes of the control-plane binary this package implements. They run
// in the *sandbox*, not beside it, which is why they are modes of the same
// binary the image already carries (/opt/piforge/piforged).
const (
	// SupervisorCommand runs one agent under a Unix socket. It is started
	// detached by StartCommand and normally has no direct caller.
	SupervisorCommand = "agent-supervisor"
	// StartCommand starts the supervisor if it is not already running, then
	// attaches to it. One round trip from the control plane's point of view.
	StartCommand = "agent-supervisor-start"
	// AttachCommand connects to an existing supervisor and splices its stream to
	// this process's stdio. It is what the runner's reattach verb runs.
	AttachCommand = "agent-attach"
)

const (
	// SocketDir and LogDir are relative to the sandbox workspace. The socket is
	// inside the workspace on purpose: it is the one directory the agent, the
	// sandbox user and a reattached control plane all agree on, and it is not
	// inside a checkout (a socket in .piforge/ would show up in `git status` only
	// if it were in the repository, which it is not).
	SocketDir = ".piforge/run"
	LogDir    = ".piforge/logs"
	// SocketSuffix is appended to the session id.
	SocketSuffix = ".sock"
	// DefaultBacklog bounds the bytes kept for a reattach: eight MiB of agent
	// stdout. It is a byte budget, not a time budget, because the records are
	// what somebody will read after the fact.
	DefaultBacklog = 8 << 20
	// DefaultReadyTimeout bounds how long a client waits for a supervisor's
	// handshake. A fresh supervisor has to create a socket and start a process,
	// which is why this is generous; a reattach is a local dial.
	DefaultReadyTimeout = 30 * time.Second
	// frameLimit is the largest handshake line this package will read. The frame
	// is one short JSON object; the bound exists so a broken peer cannot make the
	// control plane buffer an unbounded line.
	frameLimit = 8 << 10
	// maxLogBytes is where the stderr log is rotated. The Logs panel shows the
	// tail; a file that grows without bound is a disk quota incident.
	maxLogBytes = 1 << 20
)

// Frame is the one line a supervisor writes to a new client before any agent
// byte. It is JSON + "\n" so a reader can consume exactly one line and hand the
// rest of the stream to the RPC bridge untouched.
type Frame struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	// PID is the agent process the supervisor is hosting (the heartbeat
	// launcher's child, i.e. `pi`), or 0 when the supervisor reports a failure.
	PID int `json:"pid"`
	// Started is when the supervisor started that process, for a human reading a
	// log after an incident.
	Started string `json:"started,omitempty"`
	// ReplayFrom and ReplayBytes describe the backlog this client is about to
	// receive before the live stream.
	ReplayFrom  int64 `json:"replay_from"`
	ReplayBytes int64 `json:"replay_bytes"`
	// Gap is how many bytes of agent output were dropped because the backlog
	// overflowed while no client was connected. Zero means the transcript has no
	// hole.
	Gap int64 `json:"gap,omitempty"`
	// Message explains a Type != "ready" frame.
	Message string `json:"message,omitempty"`
}

// Config describes one supervisor.
type Config struct {
	// Root is the sandbox workspace (/workspace inside the container).
	Root string
	// SessionID names the marker, the socket and the log. Required.
	SessionID string
	// Socket overrides the derived <Root>/.piforge/run/<session>.sock.
	Socket string
	// StderrLog overrides the derived <Root>/.piforge/logs/<session>.log. The
	// agent's stderr goes here, which is what puts it in the Logs panel.
	StderrLog string
	// Backlog overrides DefaultBacklog.
	Backlog int
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// Listen and Dial exist so the protocol can be tested on a platform without
	// AF_UNIX. Production leaves them nil (Unix sockets).
	Listen func(socket string) (net.Listener, error)
	Dial   func(socket string, timeout time.Duration) (net.Conn, error)
}

func (c Config) normalise() (Config, error) {
	if err := ValidateSessionID(c.SessionID); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(c.Root) == "" {
		return Config{}, errors.New("agentsupervisor: a workspace root is required")
	}
	if c.Backlog <= 0 {
		c.Backlog = DefaultBacklog
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if strings.TrimSpace(c.Socket) == "" {
		c.Socket = SocketPath(c.Root, c.SessionID)
	}
	if strings.TrimSpace(c.StderrLog) == "" {
		c.StderrLog = LogPath(c.Root, c.SessionID)
	}
	if c.Listen == nil {
		c.Listen = ListenUnix
	}
	if c.Dial == nil {
		c.Dial = DialUnix
	}
	return c, nil
}

// SocketPath is where one session's supervisor listens, in the workspace's own
// path syntax.
func SocketPath(root, sessionID string) string {
	return joinWorkspace(root, path.Join(SocketDir, sessionID+SocketSuffix))
}

// LogPath is the agent's stderr log for one session.
func LogPath(root, sessionID string) string {
	return joinWorkspace(root, path.Join(LogDir, sessionID+".log"))
}

// SupervisorLogPath is where the detached supervisor's own stdout/stderr go. It
// is separate from the agent's log so a supervisor failure reads as one, and it
// never contains agent output.
func SupervisorLogPath(root, sessionID string) string {
	return joinWorkspace(root, path.Join(LogDir, sessionID+".supervisor.log"))
}

func joinWorkspace(root string, rel string) string {
	if strings.HasPrefix(root, "/") {
		return path.Join(root, rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

// ValidateSessionID keeps a session id one path segment: the id comes from a
// database row, and a socket path must never escape the sandbox that owns it.
func ValidateSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("agentsupervisor: a session id is required")
	}
	if sessionID == "." || sessionID == ".." || len(sessionID) > 128 {
		return fmt.Errorf("agentsupervisor: %q is not a usable session id", sessionID)
	}
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("agentsupervisor: session id %q contains %q", sessionID, string(r))
		}
	}
	return nil
}

// ListenUnix listens on a Unix socket, replacing a stale file left by a dead
// supervisor.
//
// A Unix socket file survives the process that owned it, and net.Listen refuses
// to bind over it. Distinguishing "stale" from "somebody is listening" is one
// dial: a refused connection means nobody is home and the file is litter.
func ListenUnix(socket string) (net.Listener, error) {
	ln, err := net.Listen("unix", socket)
	if err == nil {
		return ln, nil
	}
	conn, derr := net.DialTimeout("unix", socket, time.Second)
	if derr == nil {
		_ = conn.Close()
		return nil, busyError{socket: socket}
	}
	if rmErr := os.Remove(socket); rmErr != nil && !os.IsNotExist(rmErr) {
		return nil, fmt.Errorf("agentsupervisor: remove the stale socket %s: %w", socket, rmErr)
	}
	return net.Listen("unix", socket)
}

// DialUnix connects to a supervisor's socket.
func DialUnix(socket string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = DefaultReadyTimeout
	}
	return net.DialTimeout("unix", socket, timeout)
}

// Supervisor is the running state: one agent process, one listener, one replay
// buffer and at most one attached client.
type supervisor struct {
	cfg     Config
	ln      net.Listener
	started time.Time

	stdinMu sync.Mutex
	stdin   io.WriteCloser

	mu     sync.Mutex
	client *client
	sent   int64
	ending bool

	backlog *backlog

	pid int
}

// Run hosts argv under a Unix socket until the agent exits.
//
// It returns when the agent exits (with its exit code folded into the error when
// it failed). It does *not* return when a client disconnects: that is the whole
// point, so the loop below treats "no client" as a normal state.
func Run(ctx context.Context, cfg Config, argv []string) error {
	cfg, err := cfg.normalise()
	if err != nil {
		return err
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errors.New("agentsupervisor: the supervisor needs an agent command to run")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Socket), 0o755); err != nil {
		return fmt.Errorf("agentsupervisor: create the socket directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.StderrLog), 0o755); err != nil {
		return fmt.Errorf("agentsupervisor: create the log directory: %w", err)
	}
	stderrLog, err := openLog(cfg.StderrLog, maxLogBytes)
	if err != nil {
		return fmt.Errorf("agentsupervisor: open the agent log: %w", err)
	}
	defer stderrLog.Close()

	ln, err := cfg.Listen(cfg.Socket)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("agentsupervisor: agent stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("agentsupervisor: agent stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("agentsupervisor: agent stderr: %w", err)
	}
	// The child dies with this process: an unmarked, unownable agent left behind
	// by a crashed supervisor would look alive to the marker probe and dead to
	// everything else. See proc_linux.go.
	cmd.SysProcAttr = childProcAttr()
	if err := cmd.Start(); err != nil {
		_ = ln.Close()
		return fmt.Errorf("agentsupervisor: start the agent %q: %w", argv[0], err)
	}

	s := &supervisor{
		cfg:     cfg,
		ln:      ln,
		started: cfg.Now().UTC(),
		stdin:   stdin,
		backlog: newBacklog(cfg.Backlog),
		pid:     cmd.Process.Pid,
	}
	cfg.Logger.Info("agent supervisor is hosting an agent",
		"session", cfg.SessionID,
		"socket", cfg.Socket,
		"agent_pid", s.pid,
		"log", cfg.StderrLog,
		"detail", "the agent outlives the control plane that started it; a restarted control plane reattaches to this socket")

	// stderr is the one stream that must never touch the RPC channel, and it is
	// what the Logs panel reads. Copy it to the file; the file is the record.
	go func() {
		_, _ = io.Copy(stderrLog, stderr)
	}()
	// stdout is the RPC channel: drained unconditionally so the agent can never
	// block on a pipe nobody is reading, and fanned out to the current client.
	go s.pump(stdout)

	acceptDone := make(chan struct{}, 1)
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				acceptDone <- struct{}{}
				return
			}
			s.attach(conn)
		}
	}()

	waitErr := cmd.Wait()
	// The agent is gone: stop accepting, remove the socket so the next launch
	// does not have to guess whether this supervisor is alive, and let the
	// attached client see EOF.
	_ = ln.Close()
	_ = os.Remove(cfg.Socket)
	s.detach(nil)
	if ctx.Err() != nil {
		return nil
	}
	var exit *exec.ExitError
	switch {
	case waitErr == nil:
		cfg.Logger.Info("agent supervisor: the agent exited", "session", cfg.SessionID, "agent_pid", s.pid)
		return nil
	case errors.As(waitErr, &exit):
		cfg.Logger.Info("agent supervisor: the agent exited with a status",
			"session", cfg.SessionID, "agent_pid", s.pid, "code", exit.ExitCode())
		return fmt.Errorf("agentsupervisor: the agent exited with code %d", exit.ExitCode())
	default:
		return fmt.Errorf("agentsupervisor: wait for the agent: %w", waitErr)
	}
}

// pump reads the agent's stdout forever.
func (s *supervisor) pump(r io.Reader) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// The chunk is copied before it is queued: buf is reused by the next Read,
			// and a queued chunk the writer has not sent yet would otherwise be
			// overwritten in place — which silently truncated the *start* of a record
			// and left the client waiting for a response that had already been
			// delivered as garbage. (Found by
			// TestSupervisorCarriesSeveralRPCRoundTrips.)
			chunk := append([]byte(nil), buf[:n]...)
			s.deliver(chunk)
		}
		if err != nil {
			return
		}
	}
}

// deliver records a chunk for replay and hands it to the attached client, if
// any. A client that cannot keep up is dropped rather than allowed to block the
// agent: the next reattach replays the backlog from the last byte it acknowledged
// by accepting.
func (s *supervisor) deliver(chunk []byte) {
	// s.mu is held across the backlog append so that attach() can take its
	// snapshot and install the new client without a chunk slipping in between
	// (which would be delivered to the client being replaced and lost to the
	// new one). The lock order is always s.mu → backlog.mu.
	s.mu.Lock()
	end := s.backlog.append(chunk)
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return
	}
	if !c.push(out{data: chunk, end: end}) {
		s.mu.Lock()
		if s.client == c {
			s.client = nil
		}
		s.mu.Unlock()
		c.drop(errors.New("agentsupervisor: the client fell behind"))
	}
}

// attach makes conn the current client, replacing (and closing) any previous
// one, and hands it the missed bytes.
//
// Replacement rather than refusal is deliberate: after a control-plane restart
// the old connection is gone, but this process may not have noticed yet — it
// only notices when it writes. A new client is proof that the old one is dead.
func (s *supervisor) attach(conn net.Conn) {
	s.mu.Lock()
	from := s.sent
	start, data, gap := s.backlog.snapshotFrom(from)
	c := newClient(conn, s)
	old := s.client
	s.client = c
	// A gap means the requested offset was already trimmed: the next reattach
	// must resume where this client resumes, not at the offset nobody can serve.
	s.sent = start
	s.mu.Unlock()
	if old != nil {
		old.drop(errors.New("agentsupervisor: a new client attached"))
	}

	agentPID := s.agentPID()
	frame := Frame{
		Type:        "ready",
		Session:     s.cfg.SessionID,
		PID:         agentPID,
		Started:     s.started.Format(time.RFC3339Nano),
		ReplayFrom:  from,
		ReplayBytes: int64(len(data)),
		Gap:         gap,
	}
	head, err := json.Marshal(frame)
	if err != nil {
		c.drop(err)
		return
	}
	if !c.push(out{data: append(head, '\n'), end: s.currentSent()}) {
		c.drop(errors.New("agentsupervisor: the client could not be primed"))
		return
	}
	if len(data) > 0 {
		if !c.push(out{data: data, end: from + int64(len(data))}) {
			c.drop(errors.New("agentsupervisor: the client could not be primed"))
			return
		}
	}
	s.cfg.Logger.Info("control plane attached to the agent stream",
		"session", s.cfg.SessionID,
		"agent_pid", agentPID,
		"replayed_bytes", len(data),
		"gap_bytes", gap)
	go s.readClient(c)
}

// agentPID reports the *agent's* pid, which is what a human compares with `ps`
// inside the sandbox and what the liveness marker names.
//
// The supervisor's direct child is the heartbeat launcher, whose child is `pi`.
// Reading the marker is therefore the honest answer rather than a second guess:
// the launcher wrote that pid, and it is the same number the probe uses.
func (s *supervisor) agentPID() int {
	marker, err := liveness.MarkerPath(s.cfg.Root, s.cfg.SessionID)
	if err != nil {
		return s.pid
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		return s.pid
	}
	for _, field := range strings.Fields(string(raw)) {
		if value, ok := strings.CutPrefix(field, "pid="); ok {
			if pid, cerr := strconv.Atoi(value); cerr == nil && pid > 0 {
				return pid
			}
		}
	}
	return s.pid
}

// readClient feeds everything the client writes to the agent's stdin. It ends
// when the client disconnects — and only the client, never the agent.
func (s *supervisor) readClient(c *client) {
	buf := make([]byte, 32<<10)
	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			s.stdinMu.Lock()
			_, werr := s.stdin.Write(buf[:n])
			s.stdinMu.Unlock()
			if werr != nil {
				// The agent is gone; the exit path is already handling it.
				c.drop(werr)
				return
			}
		}
		if err != nil {
			s.mu.Lock()
			if s.client == c {
				s.client = nil
			}
			s.mu.Unlock()
			c.drop(err)
			return
		}
	}
}

// detach closes the current client, if any, so its reader sees EOF.
func (s *supervisor) detach(reason error) {
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.mu.Unlock()
	if c != nil {
		if reason == nil {
			reason = io.EOF
		}
		c.drop(reason)
	}
}

// currentSent is the offset the next client resumes from.
func (s *supervisor) currentSent() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

// markSent records the highest byte offset a client accepted. It is called by
// the client's writer goroutine after each successful write, which is the
// closest thing to an acknowledgement this protocol has.
func (s *supervisor) markSent(end int64) {
	s.mu.Lock()
	if end > s.sent {
		s.sent = end
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// out is one write to a client, tagged with the absolute stream offset it ends
// at, so the supervisor can record how far the peer got.
type out struct {
	data []byte
	end  int64
}

// client is one attached control plane.
//
// Writes happen on a dedicated goroutine through a bounded queue. Two reasons:
// ordering between the replay and the live stream is then the channel's FIFO
// (a live chunk can never overtake the replay), and a stalled client cannot
// stall the agent's stdout pipe.
type client struct {
	conn net.Conn

	mu     sync.Mutex
	closed bool
	out    chan out

	// quit is closed by drop; the writer goroutine exits on it. out itself is
	// never closed, so a push racing with drop either lands before the close or
	// observes closed under mu.
	quit chan struct{}
}

func newClient(conn net.Conn, s *supervisor) *client {
	c := &client{conn: conn, out: make(chan out, 256), quit: make(chan struct{})}
	go c.writer(s)
	return c
}

func (c *client) writer(s *supervisor) {
	for {
		select {
		case item := <-c.out:
			if _, err := c.conn.Write(item.data); err != nil {
				c.drop(err)
				_ = c.conn.Close()
				return
			}
			s.markSent(item.end)
		case <-c.quit:
			_ = c.conn.Close()
			return
		}
	}
}

// push queues one write. It never blocks: a full queue means the peer is not
// reading, and the honest answer is to drop the peer, not to stop the agent.
func (c *client) push(item out) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.out <- item:
		return true
	default:
		return false
	}
}

// drop closes the client. It is idempotent and safe to call from any goroutine.
func (c *client) drop(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.quit)
	c.mu.Unlock()
	_ = c.conn.Close()
	_ = reason
}

// ---------------------------------------------------------------------------
// Backlog
// ---------------------------------------------------------------------------

// backlog is the replay buffer: the agent's stdout as a sliding window with
// absolute offsets.
//
// Trimming happens on line boundaries. Splicing a JSONL stream mid-record would
// turn the resumed channel into garbage, so an over-full buffer drops whole
// records from the front and reports the byte count as a gap.
type backlog struct {
	mu   sync.Mutex
	buf  []byte
	base int64
	end  int64
	max  int
}

func newBacklog(max int) *backlog {
	if max <= 0 {
		max = DefaultBacklog
	}
	return &backlog{max: max}
}

// append adds one chunk and returns the absolute offset just past it.
func (b *backlog) append(p []byte) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	b.end += int64(len(p))
	b.trim()
	return b.end
}

// trim removes whole records from the front until the buffer fits.
func (b *backlog) trim() {
	if len(b.buf) <= b.max {
		return
	}
	drop := len(b.buf) - b.max
	idx := 0
	if drop > 0 && drop < len(b.buf) {
		idx = drop
		// Advance to just past the first newline at or after the cut, so the
		// buffer always starts on a record boundary.
		for idx < len(b.buf) && b.buf[idx-1] != '\n' {
			idx++
		}
	}
	if idx <= 0 {
		return
	}
	idx = min(idx, len(b.buf))
	b.buf = append([]byte(nil), b.buf[idx:]...)
	b.base += int64(idx)
}

// snapshotFrom returns the bytes at or after off, and how many bytes before them
// were lost. A caller that asks for an offset older than the window gets the
// window and a positive gap.
func (b *backlog) snapshotFrom(off int64) (int64, []byte, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := off
	var gap int64
	if start < b.base {
		gap = b.base - start
		start = b.base
	}
	if start > b.end {
		// The peer claims to have read more than was written: treat it as being
		// at the end rather than replaying everything twice.
		start = b.end
	}
	rel := int(start - b.base)
	out := append([]byte(nil), b.buf[rel:]...)
	return start, out, gap
}

// endOffset reports the absolute offset just past the last byte received.
func (b *backlog) endOffset() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.end
}

// ---------------------------------------------------------------------------
// Log file
// ---------------------------------------------------------------------------

// openLog opens a log file for append, rotating it once when it is already over
// the ceiling. Rotation keeps one previous generation and no more: the Logs
// panel shows a tail, not a history.
func openLog(path string, max int64) (*os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > max {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// ReadLog returns the tail of an agent's log file, newest last.
//
// It exists for the control-plane side, which reads the file through the
// sandbox's own file API; the sandbox-side helper below is used by tests and by
// anything running inside the container.
func ReadLog(path string, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	off := int64(0)
	if size > int64(maxBytes) {
		off = size - int64(maxBytes)
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}
