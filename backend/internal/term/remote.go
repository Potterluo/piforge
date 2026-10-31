package term

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
//   - retries a failed poll with a bounded backoff instead of ending the stream
//     on the first error, because a view can miss while the container is still
//     bringing a session up (measured: the first panel of a freshly created
//     sandbox answers `ready` and then fails its first view);
//   - ends with a typed failure when the retries are exhausted, so the relay can
//     tell the panel what went wrong instead of closing as if the shell exited.
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

	// retryBase/retryMax/retryBudget decide how long a failing view is retried.
	// They are fields rather than constants so a test can run the real pump in
	// milliseconds.
	retryBase   time.Duration
	retryMax    time.Duration
	retryBudget time.Duration

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
	// lastViewErr is the most recent ViewShell failure, kept so a failed Start
	// can name it.
	lastViewErr string
	// skipLF swallows the LF of a CRLF pair after a CR ended the line.
	skipLF bool
	// retrying/retrySeq/retryReason describe a shell that stopped answering and
	// is being retried. The relay watches retrySeq so the panel can say
	// "reconnecting" instead of showing a terminal that silently froze.
	retrying    bool
	retrySeq    int
	retryReason string
	// failure is why this terminal stopped, when it did not stop because the
	// user closed it. It is what the relay reports to the panel.
	failure string

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
	// RemoteRetryBase is the first wait after a failed view; each further
	// failure doubles it up to RemoteRetryMax.
	RemoteRetryBase = 100 * time.Millisecond
	// RemoteRetryMax caps one retry wait.
	RemoteRetryMax = 2 * time.Second
	// RemoteRetryBudget is how long a failing view is retried before the
	// terminal reports failure. A container that is merely slow to register a
	// session recovers inside it; a sandbox that is gone does not, and the user
	// gets an error instead of a terminal that never connects.
	RemoteRetryBudget = 20 * time.Second
)

// RemoteFailure is the typed reason a remote terminal stopped. The relay turns
// it into an `error` frame; nothing else may be inferred from a closed socket.
type RemoteFailure struct {
	TabID    string
	Op       string
	Attempts int
	Err      error
}

func (f *RemoteFailure) Error() string {
	return fmt.Sprintf("term: sandbox %s for tab %s failed after %d attempt(s): %v", f.Op, f.TabID, f.Attempts, f.Err)
}

func (f *RemoteFailure) Unwrap() error { return f.Err }

// isPermanentViewError reports whether retrying a failed view cannot help.
//
// 401/403 means the sandbox rejected the key this control plane holds: that does
// not heal, and a terminal that spends 20 s retrying it hides a deployment
// mistake. Everything else — a 404 while a session is still being registered, a
// 5xx from a container that is still starting, a refused or timed-out
// connection — is retried.
func isPermanentViewError(err error) bool {
	switch aio.StatusOf(err) {
	case http.StatusUnauthorized, http.StatusForbidden:
		return true
	}
	return false
}

// nextRetry is the pump's whole reconnect policy, kept pure so it can be tested
// without a clock: given the number of consecutive failures and the time since
// the first one, should it retry, and after how long?
func nextRetry(failures int, elapsed, base, max, budget time.Duration) (bool, time.Duration) {
	if failures <= 0 {
		return false, 0
	}
	wait := base << (failures - 1)
	if wait > max || wait <= 0 {
		wait = max
	}
	if elapsed+wait > budget {
		return false, 0
	}
	return true, wait
}

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
	// RetryBudget overrides RemoteRetryBudget; RetryBase/RetryMax override the
	// backoff. Zero means the package default.
	RetryBudget time.Duration
	RetryBase   time.Duration
	RetryMax    time.Duration
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
	if cfg.RetryBudget <= 0 {
		cfg.RetryBudget = RemoteRetryBudget
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = RemoteRetryBase
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = RemoteRetryMax
	}
	sessionID := strings.TrimSpace(cfg.SessionID)
	if sessionID == "" {
		sessionID = cfg.TabID
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &RemoteSession{
		client:      cfg.Client,
		sandboxID:   cfg.SandboxID,
		tabID:       cfg.TabID,
		sessionID:   sessionID,
		cwd:         cfg.Cwd,
		started:     time.Now().UTC(),
		ring:        newRingBuffer(cfg.RingBytes),
		readChunk:   cfg.ReadChunk,
		pollEvery:   cfg.PollInterval,
		retryBase:   cfg.RetryBase,
		retryMax:    cfg.RetryMax,
		retryBudget: cfg.RetryBudget,
		ctx:         ctx,
		cancel:      cancel,
		pollDone:    make(chan struct{}),
		cols:        cfg.Cols,
		rows:        cfg.Rows,
		exitCode:    -1,
	}, nil
}

// Start opens the container-side session and starts the polling pump.
//
// It does not report success until the container has answered one ViewShell for
// the session it just created. Without that handshake the relay says "ready"
// (which the panel renders as a connected terminal) and the first poll fails a
// few hundred milliseconds later, which the user sees as a terminal that never
// connects. If the shell cannot be viewed, Start fails with a *RemoteFailure and
// the relay reports that instead.
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

	created, err := r.client.CreateShellSession(ctx, r.sessionID, r.cwd)
	if err != nil {
		r.finish(1)
		return nil, fmt.Errorf("term: create sandbox shell %s: %w", r.sessionID, err)
	}
	// Address the session the container registered, not necessarily the id that
	// was asked for. The image answers `session_id`; the two are usually equal,
	// and when they are not, polling the asked-for id is guaranteed to 404.
	if id := strings.TrimSpace(created.ShellID()); id != "" && id != r.sessionID {
		slog.Warn("term: the sandbox registered the shell under another id",
			"tab", r.tabID, "requested", r.sessionID, "registered", id)
		r.sessionID = id
	}

	if ok, attempts := r.awaitViewable(ctx); !ok {
		failure := &RemoteFailure{TabID: r.tabID, Op: "view", Attempts: attempts, Err: r.lastFailureErr()}
		r.fail(failure)
		return nil, failure
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

// awaitViewable polls the shell until one view succeeds. It is the same retry
// policy the pump uses, applied to the first look. It reports how many attempts
// it made so a failure can say so.
func (r *RemoteSession) awaitViewable(ctx context.Context) (bool, int) {
	started := time.Now()
	failures := 0
	for {
		view, err := r.client.ViewShell(ctx, r.sessionID)
		if err == nil {
			r.rememberViewError(nil)
			r.apply(view)
			return true, failures
		}
		if ctx.Err() != nil || r.ctx.Err() != nil {
			return false, failures
		}
		failures++
		r.rememberViewError(err)
		if isPermanentViewError(err) {
			return false, failures
		}
		retry, wait := nextRetry(failures, time.Since(started), r.retryBase, r.retryMax, r.retryBudget)
		if !retry {
			return false, failures
		}
		select {
		case <-ctx.Done():
			return false, failures
		case <-r.ctx.Done():
			return false, failures
		case <-time.After(wait):
		}
	}
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
		// The CRLF swallow lasts exactly one byte: anything other than the LF of
		// the pair means a later LF is a real Enter.
		if r.skipLF && b != '\n' {
			r.skipLF = false
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
			// A terminal sends CR for Enter (xterm does), a paste over a socket
			// may send CRLF. CR ends the line, and the LF of a CRLF pair is
			// swallowed so it cannot run the same line twice.
			lines = append(lines, string(r.pending))
			r.pending = r.pending[:0]
			echo = append(echo, '\r', '\n')
			r.skipLF = true
		case b == '\n':
			if r.skipLF {
				r.skipLF = false
				continue
			}
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
	failure := r.failure
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
		Err:       failure,
		Retrying:  r.retrying,
		RetrySeq:  r.retrySeq,
		RetryWhy:  r.retryReason,
	}, true
}

// setRetrying records the transition into "the shell stopped answering". It is
// idempotent: only the first failure of a run of failures bumps retrySeq.
func (r *RemoteSession) setRetrying(reason string) {
	r.mu.Lock()
	if !r.retrying {
		r.retrying = true
		r.retrySeq++
	}
	r.retryReason = reason
	r.mu.Unlock()
	r.notify()
}

// clearRetrying records that the shell answered again.
func (r *RemoteSession) clearRetrying() {
	r.mu.Lock()
	changed := r.retrying
	if changed {
		r.retrying = false
		r.retrySeq++
	}
	r.retryReason = ""
	r.mu.Unlock()
	if changed {
		r.notify()
	}
}

// Failure reports why this terminal stopped, or nil when it has not failed.
func (r *RemoteSession) Failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure == "" {
		return nil
	}
	return &RemoteFailure{TabID: r.tabID, Op: "view", Err: errors.New(r.failure)}
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
//
// A failed view is retried with backoff inside one retry budget. Measured on the
// isolated deployment: the first view of a session created moments earlier can
// fail while the container finishes registering it, and the next one succeeds —
// ending the stream there is what made the panel dead. When the budget runs out
// the session ends with a *RemoteFailure, which the relay reports as an error
// frame; it never ends as a clean "shell exited" that hides the cause.
func (r *RemoteSession) pump() {
	defer close(r.pollDone)
	ticker := time.NewTicker(r.pollEvery)
	defer ticker.Stop()

	// started is reset on every transition from healthy to failing: the budget
	// is how long a *broken* shell is retried, not how old the terminal is.
	// Measuring from the pump's start made a terminal that had worked for a
	// while give up on its first failed view (measured: attempts=1 after 25 s).
	started := time.Now()
	failures := 0
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		view, err := r.client.ViewShell(r.ctx, r.sessionID)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			if failures == 0 {
				started = time.Now()
			}
			failures++
			r.rememberViewError(err)
			retry, wait := nextRetry(failures, time.Since(started), r.retryBase, r.retryMax, r.retryBudget)
			if isPermanentViewError(err) {
				retry = false
			}
			if !retry {
				r.clearRetrying()
				failure := &RemoteFailure{TabID: r.tabID, Op: "view", Attempts: failures, Err: err}
				slog.Warn("term: the sandbox shell stopped answering; ending the terminal with an error",
					"tab", r.tabID, "sandbox", r.sandboxID, "session", r.sessionID,
					"attempts", failures, "error", err.Error())
				r.fail(failure)
				return
			}
			// Tell the relay the terminal is reconnecting rather than leaving a
			// silent gap: a frozen pane and a working one look identical.
			r.setRetrying(err.Error())
			// The first failure of a run is the one an operator needs: it is the
			// difference between "the container was still coming up" and "this
			// sandbox's shell is gone", and the acceptance pass had to read the
			// source to tell them apart.
			if failures == 1 {
				slog.Warn("term: the sandbox shell did not answer a view; retrying",
					"tab", r.tabID, "sandbox", r.sandboxID, "session", r.sessionID,
					"wait", wait.String(), "error", err.Error())
			} else {
				slog.Debug("term: a sandbox shell view failed; retrying",
					"tab", r.tabID, "session", r.sessionID, "attempt", failures,
					"wait", wait.String(), "error", err.Error())
			}
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		if failures > 0 {
			slog.Info("term: the sandbox shell answered again", "tab", r.tabID, "after_failures", failures)
		}
		failures = 0
		r.clearRetrying()
		r.rememberViewError(nil)
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

// fail ends the session with a reason the relay can show. It is deliberately
// different from finish(): a failure is not a shell that exited.
func (r *RemoteSession) fail(failure *RemoteFailure) {
	r.mu.Lock()
	r.eof = true
	r.exitCode = 1
	if failure != nil && failure.Err != nil {
		r.failure = failure.Error()
	}
	r.mu.Unlock()
	r.notify()
}

// rememberViewError keeps the most recent view error so a failed open can report
// the reason even when the last attempt only says "timed out".
func (r *RemoteSession) rememberViewError(err error) {
	r.mu.Lock()
	if err != nil {
		r.lastViewErr = err.Error()
	} else {
		r.lastViewErr = ""
	}
	r.mu.Unlock()
}

func (r *RemoteSession) lastFailureErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastViewErr == "" {
		return errors.New("the sandbox never answered a view of the new shell")
	}
	return errors.New(r.lastViewErr)
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
