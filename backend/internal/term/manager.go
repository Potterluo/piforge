// Package term manages interactive PTY sessions for the terminal panel
// (PRD §5.4.2 / §7.6.1).
//
// # Shape
//
// One Manager owns every session on this node. Sessions are keyed by
// (sandboxID, tabID): the sandbox scopes the working directory, the tab is one
// PTY. Each session keeps a fixed-size ring buffer of raw bytes with absolute
// offsets, so a client that reconnects with the offset it last rendered can
// replay exactly what it missed (PRD FR-10.3, AC-10.3) without the server
// retaining an unbounded scrollback.
//
// # Backpressure
//
// The ring is the rate limit: once a session produces more than RingBytes
// without the client catching up, the oldest bytes are dropped and the stream
// is flagged lossy (Info.Lossy). A slow client therefore loses the middle of
// the stream instead of growing memory without bound (PRD §6.3-坑5).
//
// # Platform
//
// Unix uses github.com/creack/pty for a real TTY (vim, htop, top work).
// Windows falls back to stdin/stdout pipes: echo-level behaviour works, TUI
// semantics and resize do not. Both satisfy the same ptyProcess interface.
//
// # Remote sessions
//
// A Manager can instead place a tab inside an AIO Sandbox container (Config
// Remote). That shell is command-oriented rather than a PTY stream, so
// RemoteSession adapts it to the same ring buffer and byte surface; see
// remote.go. The host PTY remains the default when Remote is nil.
package term

import (
	"context"
	"errors"
	"fmt"
	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/isolate"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors returned by the manager.
var (
	// ErrNoSession is returned when a tab id is unknown.
	ErrNoSession = errors.New("term: no such session")
	// ErrClosed is returned when writing to a closed session.
	ErrClosed = errors.New("term: session closed")
	// ErrTooManySessions is returned when the per-node PTY cap is reached.
	ErrTooManySessions = errors.New("term: too many sessions")
)

// Defaults for Config.
const (
	DefaultRingBytes  = 2 << 20 // 2 MiB per PTY (PRD AC-10.3)
	MinRingBytes      = 64 << 10
	DefaultMaxSession = 64
	DefaultCols       = 120
	DefaultRows       = 32
	DefaultReadChunk  = 256 << 10
)

// Config configures a Manager. The zero value is usable.
type Config struct {
	// Shell is the preferred shell (absolute path or name). Empty picks the
	// platform default: bash, then sh on Unix; powershell.exe, then cmd.exe on
	// Windows.
	Shell string
	// RingBytes is the per-session replay buffer (default 2 MiB).
	RingBytes int
	// MaxSessions caps concurrent live PTYs (default 64).
	MaxSessions int
	// Cols/Rows are the initial window size (default 120x32).
	Cols, Rows int
	// ReadChunk caps the bytes returned by one Read (default 256 KiB).
	ReadChunk int
	// Launcher optionally wraps the shell's argv so terminals run inside the
	// same process jail as the agent. Nil means run it directly.
	Launcher isolate.Launcher
	// WorkDir resolves the working directory for a sandbox. Returning an error
	// fails Open. When nil the current process directory is used.
	WorkDir func(ctx context.Context, sandboxID string) (string, error)
	// Remote, when set, can place the terminal inside an AIO Sandbox container
	// instead of starting a host PTY. It returns the sandbox client, the shell
	// session id to create, and the directory that session should start in.
	//
	// The id is whatever Open was passed. The HTTP layer currently passes a
	// worktree id, so a resolver has to map it to its sandbox.
	//
	// Returning an error, or a nil client, falls back to the host PTY: nil is
	// the default path and stays untouched. When a client is returned but the
	// container refuses to create the session, Open fails instead of silently
	// running the terminal on the wrong machine.
	Remote func(ctx context.Context, sandboxID string) (client *aio.Client, sessionID string, cwd string, err error)
	// RemoteRetryBudget bounds how long a failing sandbox shell is retried
	// before the terminal reports failure (default RemoteRetryBudget).
	// RemoteRetryBase/RemoteRetryMax override the backoff, and
	// RemotePollInterval the poll period (tests shorten all four).
	RemoteRetryBudget  time.Duration
	RemoteRetryBase    time.Duration
	RemoteRetryMax     time.Duration
	RemotePollInterval time.Duration
}

func (c Config) normalise() Config {
	if c.RingBytes <= 0 {
		c.RingBytes = DefaultRingBytes
	}
	if c.RingBytes < MinRingBytes {
		c.RingBytes = MinRingBytes
	}
	if c.MaxSessions <= 0 {
		c.MaxSessions = DefaultMaxSession
	}
	if c.Cols <= 0 {
		c.Cols = DefaultCols
	}
	if c.Rows <= 0 {
		c.Rows = DefaultRows
	}
	if c.ReadChunk <= 0 {
		c.ReadChunk = DefaultReadChunk
	}
	return c
}

// Info is a snapshot of one session.
type Info struct {
	SandboxID string    `json:"sandbox_id"`
	TabID     string    `json:"tab_id"`
	Shell     string    `json:"shell"`
	PID       int       `json:"pid"`
	Cols      int       `json:"cols"`
	Rows      int       `json:"rows"`
	Started   time.Time `json:"started"`

	// Start/End are absolute byte offsets of the replay window. A client can
	// resume with End, or replay everything from Start.
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	// Dropped counts bytes that fell out of the ring (lossy stream).
	Dropped int64 `json:"dropped"`
	// Lossy is true once a Read asked for bytes already dropped, or when the
	// ring had to overwrite unread data.
	Lossy bool `json:"lossy"`
	// EOF is true once the shell exited and every byte was consumed.
	EOF bool `json:"eof"`
	// Closed is true after Close.
	Closed bool `json:"closed"`
	// ExitCode is the shell's exit status when it has exited (-1 otherwise).
	ExitCode int `json:"exit_code"`
	// Err is why the terminal stopped when it did not stop normally (a sandbox
	// shell that stopped answering, an API key the sandbox refused). The relay
	// forwards it to the panel instead of closing as if the shell had exited.
	Err string `json:"error,omitempty"`
	// Retrying is true while a sandbox shell is being retried after a failed
	// read, and RetrySeq changes on every transition into and out of that state
	// so the relay can report each one exactly once. RetryWhy carries the last
	// failure, for the panel's "reconnecting" notice.
	Retrying bool   `json:"retrying,omitempty"`
	RetrySeq int    `json:"retry_seq,omitempty"`
	RetryWhy string `json:"retry_error,omitempty"`
}

// session is one PTY.
type session struct {
	sandboxID string
	tabID     string
	shellPath string
	started   time.Time

	proc ptyProcess
	ring *ringBuffer

	mu       sync.Mutex
	cols     int
	rows     int
	closed   bool
	eof      bool
	exitCode int
	lossy    bool

	closeOnce sync.Once
	wakeMu    sync.Mutex
	wake      chan struct{}
}

// Manager owns every session on this node. It is safe for concurrent use.
type Manager struct {
	cfg Config

	mu       sync.RWMutex
	sessions map[string]*session
	// remotes are tabs whose shell lives in an AIO Sandbox container. They
	// keep the same key and replay semantics as host PTYs.
	remotes map[string]*RemoteSession
	closed  bool
}

// New builds a Manager.
func New(cfg Config) *Manager {
	return &Manager{
		cfg:      cfg.normalise(),
		sessions: make(map[string]*session),
		remotes:  make(map[string]*RemoteSession),
	}
}

// Config returns the effective configuration.
func (m *Manager) Config() Config { return m.cfg }

// Open starts a shell for (sandboxID, tabID) and returns its snapshot.
//
// Open is idempotent: opening an existing tab returns the live session, which
// is what a reconnecting websocket wants.
func (m *Manager) Open(ctx context.Context, sandboxID, tabID, shell string) (*Info, error) {
	if err := validateKey("sandbox id", sandboxID); err != nil {
		return nil, err
	}
	if err := validateKey("tab id", tabID); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	if s, ok := m.sessions[tabID]; ok && !s.isClosed() {
		info := s.info()
		m.mu.RUnlock()
		return &info, nil
	}
	deadRemote := (*RemoteSession)(nil)
	if r, ok := m.remotes[tabID]; ok {
		info, _ := r.Info(tabID)
		if !r.isClosed() && !info.EOF {
			m.mu.RUnlock()
			return &info, nil
		}
		// The container-side shell ended (or failed). Reopening the tab must
		// start a new shell: handing back the dead session would replay its EOF
		// to every reconnect, which is exactly the "terminal never connects"
		// shape. Drop it before anything else looks at the tab.
		deadRemote = r
	}
	m.mu.RUnlock()
	if deadRemote != nil {
		m.mu.Lock()
		if m.remotes[tabID] == deadRemote {
			delete(m.remotes, tabID)
		}
		m.mu.Unlock()
		_ = deadRemote.Close(tabID)
	}

	// A docker-backed terminal is opened in the container and never resolves a
	// host shell: the machine that matters is the one holding the files.
	if m.cfg.Remote != nil {
		client, sessionID, cwd, rerr := m.cfg.Remote(ctx, sandboxID)
		if rerr == nil && client != nil {
			return m.openRemote(ctx, sandboxID, tabID, client, sessionID, cwd)
		}
	}

	shellPath, err := resolveShell(firstNonEmpty(shell, m.cfg.Shell))
	if err != nil {
		return nil, err
	}
	dir := ""
	if m.cfg.WorkDir != nil {
		dir, err = m.cfg.WorkDir(ctx, sandboxID)
		if err != nil {
			return nil, fmt.Errorf("term: work dir for sandbox %s: %w", sandboxID, err)
		}
	}

	m.mu.Lock()
	if s, ok := m.sessions[tabID]; ok && !s.isClosed() {
		info := s.info()
		m.mu.Unlock()
		return &info, nil
	}
	if r, ok := m.remotes[tabID]; ok && !r.isClosed() {
		info, _ := r.Info(tabID)
		if !info.EOF {
			m.mu.Unlock()
			return &info, nil
		}
		delete(m.remotes, tabID)
	}
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if live := m.liveCountLocked(); live >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %d live sessions", ErrTooManySessions, live)
	}

	proc, err := startProcess(ctx, m.shellCommand(shellPath), dir, m.cfg.Cols, m.cfg.Rows)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("term: start %s: %w", shellPath, err)
	}
	s := &session{
		sandboxID: sandboxID,
		tabID:     tabID,
		shellPath: shellPath,
		started:   time.Now().UTC(),
		proc:      proc,
		ring:      newRingBuffer(m.cfg.RingBytes),
		cols:      m.cfg.Cols,
		rows:      m.cfg.Rows,
		exitCode:  -1,
	}
	m.sessions[tabID] = s
	m.mu.Unlock()

	go s.pump()
	go s.reap()

	info := s.info()
	return &info, nil
}

// openRemote creates the container-side shell and registers it under tabID.
//
// The session is inserted before Start so that a failed CreateShellSession can
// be removed by identity rather than racing another Open for the same tab.
func (m *Manager) openRemote(ctx context.Context, sandboxID, tabID string, client *aio.Client, sessionID, cwd string) (*Info, error) {
	remote, err := NewRemoteSession(RemoteConfig{
		Client:    client,
		SandboxID: sandboxID,
		TabID:     tabID,
		SessionID: sessionID,
		Cwd:       cwd,
		RingBytes: m.cfg.RingBytes,
		ReadChunk: m.cfg.ReadChunk,
		Cols:      m.cfg.Cols,
		Rows:      m.cfg.Rows,

		RetryBudget:  m.cfg.RemoteRetryBudget,
		RetryBase:    m.cfg.RemoteRetryBase,
		RetryMax:     m.cfg.RemoteRetryMax,
		PollInterval: m.cfg.RemotePollInterval,
	})
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if s, ok := m.sessions[tabID]; ok && !s.isClosed() {
		info := s.info()
		m.mu.Unlock()
		_ = remote.Close(tabID)
		return &info, nil
	}
	if r, ok := m.remotes[tabID]; ok && !r.isClosed() {
		info, _ := r.Info(tabID)
		m.mu.Unlock()
		_ = remote.Close(tabID)
		return &info, nil
	}
	if m.closed {
		m.mu.Unlock()
		_ = remote.Close(tabID)
		return nil, ErrClosed
	}
	if live := m.liveCountLocked(); live >= m.cfg.MaxSessions {
		m.mu.Unlock()
		_ = remote.Close(tabID)
		return nil, fmt.Errorf("%w: %d live sessions", ErrTooManySessions, live)
	}
	m.remotes[tabID] = remote
	m.mu.Unlock()

	info, err := remote.Start(ctx)
	if err != nil {
		m.mu.Lock()
		if m.remotes[tabID] == remote {
			delete(m.remotes, tabID)
		}
		m.mu.Unlock()
		_ = remote.Close(tabID)
		return nil, err
	}
	return info, nil
}

// Write sends bytes to the shell.
func (m *Manager) Write(tabID string, p []byte) error {
	if r := m.remote(tabID); r != nil {
		return r.Write(tabID, p)
	}
	s := m.session(tabID)
	if s == nil {
		return fmt.Errorf("%w: %s", ErrNoSession, tabID)
	}
	if s.isClosed() {
		return fmt.Errorf("%w: %s", ErrClosed, tabID)
	}
	if len(p) == 0 {
		return nil
	}
	if _, err := s.proc.Write(p); err != nil {
		return fmt.Errorf("term: write %s: %w", tabID, err)
	}
	return nil
}

// Resize sets the window size and forwards it to the PTY. It always records the
// size, even on Windows where the underlying pipe has no window size.
func (m *Manager) Resize(tabID string, cols, rows int) error {
	if r := m.remote(tabID); r != nil {
		return r.Resize(tabID, cols, rows)
	}
	s := m.session(tabID)
	if s == nil {
		return fmt.Errorf("%w: %s", ErrNoSession, tabID)
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("%w: %s", ErrClosed, tabID)
	}
	if err := s.proc.Resize(cols, rows); err != nil {
		return fmt.Errorf("term: resize %s: %w", tabID, err)
	}
	return nil
}

// Close terminates a session. Closing an unknown or already closed tab is not
// an error, so a websocket teardown can always call it.
func (m *Manager) Close(tabID string) error {
	m.mu.Lock()
	s, ok := m.sessions[tabID]
	if ok {
		delete(m.sessions, tabID)
	}
	r, remote := m.remotes[tabID]
	if remote {
		delete(m.remotes, tabID)
	}
	m.mu.Unlock()
	if ok {
		s.close()
	}
	if remote {
		_ = r.Close(tabID)
	}
	return nil
}

// Read returns the bytes for [fromOffset, ...) with an absolute next offset and
// an eof flag meaning "the shell exited and you have consumed everything".
//
// Reading an unknown tab returns (nil, fromOffset, true). If the requested
// offset is older than the ring's window the oldest available byte is returned
// instead and the session is flagged lossy.
func (m *Manager) Read(tabID string, fromOffset int64) (data []byte, next int64, eof bool) {
	if r := m.remote(tabID); r != nil {
		return r.Read(tabID, fromOffset)
	}
	s := m.session(tabID)
	if s == nil {
		return nil, fromOffset, true
	}
	if fromOffset < 0 {
		fromOffset = 0
	}
	data, next, lost := s.ring.read(fromOffset, m.cfg.ReadChunk)
	if lost > 0 {
		s.mu.Lock()
		s.lossy = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	exited := s.eof
	s.mu.Unlock()
	return data, next, exited && next >= s.ring.endOffset()
}

// Info returns a snapshot for one tab.
func (m *Manager) Info(tabID string) (Info, bool) {
	if r := m.remote(tabID); r != nil {
		return r.Info(tabID)
	}
	s := m.session(tabID)
	if s == nil {
		return Info{}, false
	}
	return s.info(), true
}

// List returns every session of a sandbox (all sessions when sandboxID is "").
func (m *Manager) List(sandboxID string) []Info {
	m.mu.RLock()
	out := make([]Info, 0, len(m.sessions)+len(m.remotes))
	for _, s := range m.sessions {
		if sandboxID != "" && s.sandboxID != sandboxID {
			continue
		}
		out = append(out, s.info())
	}
	for _, r := range m.remotes {
		info, _ := r.Info(r.tabID)
		if sandboxID != "" && info.SandboxID != sandboxID {
			continue
		}
		out = append(out, info)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Wake returns a channel that is closed when new output arrives for the tab.
// Call it again after each wakeup to get the next notification.
func (m *Manager) Wake(tabID string) <-chan struct{} {
	if r := m.remote(tabID); r != nil {
		return r.Wake(tabID)
	}
	s := m.session(tabID)
	if s == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wake == nil {
		s.wake = make(chan struct{})
	}
	return s.wake
}

// CloseSandbox closes every session of one sandbox (used when a sandbox is
// paused or destroyed).
func (m *Manager) CloseSandbox(sandboxID string) int {
	m.mu.RLock()
	ids := make([]string, 0, 4)
	for tabID, s := range m.sessions {
		if s.sandboxID == sandboxID {
			ids = append(ids, tabID)
		}
	}
	for tabID, r := range m.remotes {
		if r.sandboxID == sandboxID {
			ids = append(ids, tabID)
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		_ = m.Close(id)
	}
	return len(ids)
}

// CloseAll terminates every session and marks the manager closed.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.sessions)+len(m.remotes))
	for tabID := range m.sessions {
		ids = append(ids, tabID)
	}
	for tabID := range m.remotes {
		ids = append(ids, tabID)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(id)
	}
}

// Len reports the number of live sessions.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.liveCountLocked()
}

func (m *Manager) liveCountLocked() int {
	n := 0
	for _, s := range m.sessions {
		if !s.isClosed() {
			n++
		}
	}
	for _, r := range m.remotes {
		if !r.isClosed() {
			n++
		}
	}
	return n
}

func (m *Manager) session(tabID string) *session {
	m.mu.RLock()
	s := m.sessions[tabID]
	m.mu.RUnlock()
	return s
}

func (m *Manager) remote(tabID string) *RemoteSession {
	m.mu.RLock()
	r := m.remotes[tabID]
	m.mu.RUnlock()
	return r
}

// ---------------------------------------------------------------------------
// session internals
// ---------------------------------------------------------------------------

// pump copies PTY output into the ring until the process ends.
func (s *session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.ring.write(buf[:n])
			s.notify()
		}
		if err != nil {
			s.mu.Lock()
			s.eof = true
			s.mu.Unlock()
			s.notify()
			return
		}
	}
}

// reap records the exit status.
func (s *session) reap() {
	err := s.proc.Wait()
	code := 0
	if err != nil {
		code = 1
	}
	s.mu.Lock()
	s.eof = true
	s.exitCode = code
	s.mu.Unlock()
	s.notify()
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.proc.Close()
		s.notify()
	})
}

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *session) notify() {
	s.wakeMu.Lock()
	if s.wake != nil {
		close(s.wake)
		s.wake = nil
	}
	s.wakeMu.Unlock()
}

func (s *session) info() Info {
	s.mu.Lock()
	cols, rows := s.cols, s.rows
	closed, eof, exitCode, lossy := s.closed, s.eof, s.exitCode, s.lossy
	s.mu.Unlock()
	start, end := s.ring.window()
	return Info{
		SandboxID: s.sandboxID,
		TabID:     s.tabID,
		Shell:     s.shellPath,
		PID:       s.proc.PID(),
		Cols:      cols,
		Rows:      rows,
		Started:   s.started,
		Start:     start,
		End:       end,
		Dropped:   s.ring.droppedBytes(),
		Lossy:     lossy,
		EOF:       eof,
		Closed:    closed,
		ExitCode:  exitCode,
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func validateKey(what, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("term: empty %s", what)
	}
	if len(v) > 200 {
		return fmt.Errorf("term: %s too long", what)
	}
	if strings.ContainsRune(v, 0) {
		return fmt.Errorf("term: %s contains a NUL byte", what)
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// shellCommand builds the argv for a terminal, applying the launcher when one is
// configured. It is separate from Open so the wrapping can be asserted without
// starting a process.
func (m *Manager) shellCommand(shellPath string) []string {
	argv := shellArgv(shellPath)
	if m.cfg.Launcher != nil {
		argv = m.cfg.Launcher.Wrap(argv)
	}
	return argv
}
