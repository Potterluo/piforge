package term

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
)

// Remote sessions turn the AIO Sandbox's command-oriented shell into the byte
// stream the terminal panel already understands.
//
// The image has no PTY: CreateShellSession opens a long-lived bash process,
// WriteShell runs one line, and ViewShell returns everything that process has
// printed so far. There is no raw input channel and no window size. The adapter
// therefore:
//
//   - buffers keystrokes until a newline, because every WriteShell call is
//     executed immediately (press_enter=false does not defer it);
//   - echoes the buffered input into the ring itself, because the container
//     never echoes what it was typed;
//   - polls ViewShell and appends only the suffix that is new;
//   - reports a poll failure as EOF instead of retrying forever.
type RemoteSession struct {
	client    *aio.Client
	sandboxID string
	tabID     string
	sessionID string
	cwd       string
	started   time.Time

	ring      *ringBuffer
	readChunk int
	pollEvery time.Duration

	// ctx stops the poller. It is deliberately not the HTTP request context:
	// that one is cancelled as soon as Open returns.
	ctx      context.Context
	cancel   context.CancelFunc
	pollDone chan struct{}

	mu         sync.Mutex
	cols, rows int
	pending    []byte
	escape     bool
	closed     bool
	eof        bool
	exitCode   int
	lossy      bool
	emitted    int
	lastStatus string
	startedPol bool

	writeMu   sync.Mutex
	closeOnce sync.Once
	wakeMu    sync.Mutex
	wake      chan struct{}
}

const (
	// RemoteDefaultPoll is how often the container is asked for new output. It
	// is a frequency, not a latency guarantee: ViewShell is cheap and returns
	// the accumulated buffer.
	RemoteDefaultPoll = 175 * time.Millisecond
	// remoteMaxLine bounds the input buffer. Past this a line is executed in
	// pieces: the API has no way to type without running, so truncating a very
	// long paste is the lesser evil compared with unbounded memory.
	remoteMaxLine = 16 << 10
	// remoteWriteWait bounds one WriteShell call independently of Close.
	remoteWriteWait = 30 * time.Second
	// remoteCloseWait bounds how long Close waits for an in-flight poll.
	remoteCloseWait = 2 * time.Second
)

// RemoteConfig configures one sandbox-backed terminal.
type RemoteConfig struct {
	// Client is the sandbox API client. Required.
	Client *aio.Client
	// SandboxID is the id the manager keys the session by (a worktree id in the
	// HTTP layer today).
	SandboxID string
	// TabID is the manager's tab key.
	TabID string
	// SessionID is the shell session id inside the container. Empty uses TabID.
	SessionID string
	// Cwd is the directory the container shell starts in.
	Cwd string
	// RingBytes is the replay window (default DefaultRingBytes).
	RingBytes int
	// ReadChunk caps one Read (default DefaultReadChunk).
	ReadChunk int
	// Cols/Rows are recorded for Info; the container has no window size.
	Cols, Rows int
	// PollInterval overrides RemoteDefaultPoll (tests use a short one).
	PollInterval time.Duration
}

// NewRemoteSession builds a session. It does not talk to the container yet;
// call Start.
func NewRemoteSession(cfg RemoteConfig) (*RemoteSession, error) {
	if cfg.Client == nil {
		return nil, errors.New("term: remote session needs an aio client")
	}
	if strings.TrimSpace(cfg.TabID) == "" {
		return nil, errors.New("term: remote session needs a tab id")
	}
	if cfg.RingBytes <= 0 {
		cfg.RingBytes = DefaultRingBytes
	}
	if cfg.RingBytes < MinRingBytes {
		cfg.RingBytes = MinRingBytes
	}
	if cfg.ReadChunk <= 0 {
		cfg.ReadChunk = DefaultReadChunk
	}
	if cfg.Cols <= 0 {
		cfg.Cols = DefaultCols
	}
	if cfg.Rows <= 0 {
		cfg.Rows = DefaultRows
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = RemoteDefaultPoll
	}
	sessionID := strings.TrimSpace(cfg.SessionID)
	if sessionID == "" {
		sessionID = cfg.TabID
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &RemoteSession{
		client:    cfg.Client,
		sandboxID: cfg.SandboxID,
		tabID:     cfg.TabID,
		sessionID: sessionID,
		cwd:       cfg.Cwd,
		started:   time.Now().UTC(),
		ring:      newRingBuffer(cfg.RingBytes),
		readChunk: cfg.ReadChunk,
		pollEvery: cfg.PollInterval,
		ctx:       ctx,
		cancel:    cancel,
		pollDone:  make(chan struct{}),
		cols:      cfg.Cols,
		rows:      cfg.Rows,
		exitCode:  -1,
	}, nil
}

// Start opens the container-side session and starts the polling pump.
func (r *RemoteSession) Start(ctx context.Context) (*Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrClosed, r.tabID)
	}
	if r.startedPol {
		r.mu.Unlock()
		info, _ := r.Info(r.tabID)
		return &info, nil
	}
	r.mu.Unlock()

	if _, err := r.client.CreateShellSession(ctx, r.sessionID, r.cwd); err != nil {
		r.finish(1)
		return nil, fmt.Errorf("term: create sandbox shell %s: %w", r.sessionID, err)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrClosed, r.tabID)
	}
	r.startedPol = true
	r.mu.Unlock()
	go r.pump()

	info, _ := r.Info(r.tabID)
	return &info, nil
}

// Write buffers keystrokes and executes completed lines in the container.
func (r *RemoteSession) Write(tabID string, p []byte) error {
	if r.isClosed() {
		return fmt.Errorf("%w: %s", ErrClosed, tabID)
	}
	if len(p) == 0 {
		return nil
	}

	// One writer at a time: two half-lines must not interleave in the buffer,
	// and WriteShell calls have to reach the container in order.
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	var lines []string
	var echo []byte

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrClosed, tabID)
	}
	for _, b := range p {
		if r.escape {
			// Swallow an ANSI sequence (arrows, function keys) instead of
			// turning its tail into command text.
			if b >= 0x40 && b <= 0x7e {
				r.escape = false
			}
			continue
		}
		switch {
		case b == 0x1b:
			r.escape = true
		case b == '\x7f' || b == '\b':
			if len(r.pending) > 0 {
				r.pending = r.pending[:len(r.pending)-1]
				echo = append(echo, '\b', ' ', '\b')
			}
		case b == '\r':
			// CRLF arrives as CR then LF; the LF ends the line.
		case b == '\n':
			lines = append(lines, string(r.pending))
			r.pending = r.pending[:0]
			echo = append(echo, '\r', '\n')
		case b == 0x03: // Ctrl-C: abandon the line, the API cannot signal a run.
			r.pending = r.pending[:0]
			echo = append(echo, '^', 'C', '\r', '\n')
		case b == 0x15: // Ctrl-U: clear the line and erase it visually.
			for range r.pending {
				echo = append(echo, '\b', ' ', '\b')
			}
			r.pending = r.pending[:0]
		case b < 0x20:
			// Other control bytes have no equivalent here.
		default:
			r.pending = append(r.pending, b)
			echo = append(echo, b)
			if len(r.pending) >= remoteMaxLine {
				lines = append(lines, string(r.pending))
				r.pending = r.pending[:0]
			}
		}
	}
	if len(lines) > 0 {
		r.lastStatus = "running"
	}
	r.mu.Unlock()

	// Local echo: the container has no PTY, so nothing else will show the user
	// what they typed.
	if len(echo) > 0 {
		r.ring.write(echo)
		r.notify()
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		wctx, cancel := context.WithTimeout(r.ctx, remoteWriteWait)
		err := r.client.WriteShell(wctx, r.sessionID, line, true)
		cancel()
		if err != nil {
			if r.isClosed() {
				return fmt.Errorf("%w: %s", ErrClosed, tabID)
			}
			return fmt.Errorf("term: remote write %s: %w", tabID, err)
		}
	}
	return nil
}

// Read mirrors Manager.Read for one tab.
func (r *RemoteSession) Read(tabID string, from int64) (data []byte, next int64, eof bool) {
	if from < 0 {
		from = 0
	}
	data, next, lost := r.ring.read(from, r.readChunk)
	if lost > 0 {
		r.mu.Lock()
		r.lossy = true
		r.mu.Unlock()
	}
	r.mu.Lock()
	exited := r.eof
	r.mu.Unlock()
	return data, next, exited && next >= r.ring.endOffset()
}

// Resize records the requested size. The sandbox shell has no TIOCSWINSZ
// equivalent — it is a command session, not a TTY — so nothing is forwarded.
func (r *RemoteSession) Resize(tabID string, cols, rows int) error {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	r.mu.Lock()
	r.cols, r.rows = cols, rows
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return fmt.Errorf("%w: %s", ErrClosed, tabID)
	}
	return nil
}

// Close stops the poller. It does not send "exit" to the container: the image
// wedges a session id after exit (later writes are accepted but never run), and
// a fresh id is used for every new tab anyway. The container's lifecycle owns
// the shell process.
func (r *RemoteSession) Close(tabID string) error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		started := r.startedPol
		r.mu.Unlock()

		r.cancel()
		r.notify()
		if started {
			select {
			case <-r.pollDone:
			case <-time.After(remoteCloseWait):
			}
		}
	})
	return nil
}

// Info mirrors Manager.Info for one tab.
func (r *RemoteSession) Info(tabID string) (Info, bool) {
	r.mu.Lock()
	cols, rows := r.cols, r.rows
	closed, eof, exitCode, lossy := r.closed, r.eof, r.exitCode, r.lossy
	r.mu.Unlock()
	start, end := r.ring.window()
	return Info{
		SandboxID: r.sandboxID,
		TabID:     r.tabID,
		Shell:     "aio",
		PID:       0,
		Cols:      cols,
		Rows:      rows,
		Started:   r.started,
		Start:     start,
		End:       end,
		Dropped:   r.ring.droppedBytes(),
		Lossy:     lossy,
		EOF:       eof,
		Closed:    closed,
		ExitCode:  exitCode,
	}, true
}

// Wake mirrors Manager.Wake for one tab.
func (r *RemoteSession) Wake(tabID string) <-chan struct{} {
	r.wakeMu.Lock()
	defer r.wakeMu.Unlock()
	if r.wake == nil {
		r.wake = make(chan struct{})
	}
	return r.wake
}

// pump polls ViewShell and appends the new suffix of its accumulating output.
func (r *RemoteSession) pump() {
	defer close(r.pollDone)
	ticker := time.NewTicker(r.pollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		view, err := r.client.ViewShell(r.ctx, r.sessionID)
		if err != nil {
			if r.ctx.Err() == nil {
				// A broken view call will keep failing; ending the stream is
				// better than a goroutine that never stops.
				r.finish(1)
			}
			return
		}
		r.apply(view)
	}
}

// apply appends only bytes not seen yet. The API returns the whole output on
// every call, so remembering the length is what keeps the ring from repeating
// the transcript.
func (r *RemoteSession) apply(view aio.ShellSession) {
	out := view.Output

	r.mu.Lock()
	var chunk string
	switch {
	case len(out) > r.emitted:
		chunk = out[r.emitted:]
		r.emitted = len(out)
	case len(out) < r.emitted:
		// The container restarted the buffer; replay it rather than lose text.
		chunk = out
		r.emitted = len(out)
	}
	status := strings.ToLower(strings.TrimSpace(view.Status))
	completed := status == "completed"
	// The image joins command outputs without separators, so a completed
	// command whose output does not end in a newline would run into the next
	// one. Only the transition adds it; repeated polls of the same completed
	// state must not.
	addNewline := completed && r.lastStatus != "completed" && chunk != "" && !strings.HasSuffix(chunk, "\n")
	if completed {
		r.lastStatus = "completed"
	} else if status != "" {
		r.lastStatus = status
	}
	r.mu.Unlock()

	if chunk != "" {
		r.ring.write([]byte(chunk))
		r.notify()
	}
	if addNewline {
		r.ring.write([]byte("\n"))
		r.notify()
	}
}

func (r *RemoteSession) finish(code int) {
	r.mu.Lock()
	r.eof = true
	r.exitCode = code
	r.mu.Unlock()
	r.notify()
}

func (r *RemoteSession) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *RemoteSession) notify() {
	r.wakeMu.Lock()
	if r.wake != nil {
		close(r.wake)
		r.wake = nil
	}
	r.wakeMu.Unlock()
}
