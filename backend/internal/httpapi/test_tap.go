package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/testrun"
)

// Terminal test detection.
//
// The agent's runs come from tool events, which carry the command and its
// output as data. A command the *user* types in the terminal has no such event:
// the PTY is a byte relay (see pty_handlers.go), and this tap reads the same
// bytes without changing what reaches xterm.
//
// Detection is at the input line, so "运行中" starts when Enter is pressed, and
// settlement is at the runner's own final summary line. When the output never
// produces one, an idle timer settles it as 未识别格式 with the tail: a row that
// stays "running" forever is worse than a row that says it could not read the
// output.
//
// Known limits, deliberately not guessed at:
//   - multi-line shell input (heredocs, backslash continuations) is not joined;
//   - a command recalled from shell history with the up-arrow produces one or
//     more cursor-movement sequences, which this drops, so the recalled line is
//     empty;
//   - a test that legitimately prints nothing for idleSettle is settled early.
const (
	terminalIdleSettle = 20 * time.Second
	terminalMaxBuffer  = 256 << 10
	terminalMaxLine    = 4 << 10
)

// pendingTerminalRun is one typed test command whose output is still arriving.
type pendingTerminalRun struct {
	id       string
	runner   string
	command  string
	started  time.Time
	lastSeen time.Time
	buf      []byte
}

// testTap watches one terminal tab. It is created lazily per tab and survives a
// socket reconnect, because the PTY does.
type testTap struct {
	mu        sync.Mutex
	log       *slog.Logger
	userID    string
	sessionID string
	tabID     string

	// create records the run and returns its id. A false return means the row
	// could not be written and the command is ignored rather than half-tracked.
	create func(command string, runner testrun.Runner) (string, bool)
	// settle stores the parsed outcome. Shared with the tool-event path.
	settle func(runID string, res testrun.Result, raw string, started time.Time)

	high    int64
	line    []byte
	esc     bool
	pending *pendingTerminalRun
	timer   *time.Timer
}

// input consumes keystrokes. Enter completes a line; Ctrl-C drops it.
func (t *testTap) input(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, b := range p {
		if t.esc {
			// CSI/OSC sequence terminator. Dropping the whole sequence means a
			// history recall leaves no fabricated command behind.
			if b >= 0x40 && b <= 0x7e {
				t.esc = false
			}
			continue
		}
		switch {
		case b == 0x1b:
			t.esc = true
		case b == '\r' || b == '\n':
			line := strings.TrimSpace(string(t.line))
			t.line = t.line[:0]
			t.handleLineLocked(line)
		case b == 0x7f || b == 0x08:
			if len(t.line) > 0 {
				t.line = t.line[:len(t.line)-1]
			}
		case b == 0x03: // Ctrl-C: the command was abandoned, not completed.
			t.line = t.line[:0]
			t.settleLocked()
		case b == '\t':
			// Completion inserts text this tap cannot model, so the line is
			// discarded rather than misread as a command the user did not type.
			t.line = t.line[:0]
		default:
			if b >= 0x20 {
				t.line = append(t.line, b)
			}
		}
		if len(t.line) > terminalMaxLine {
			t.line = t.line[:0]
		}
	}
}

// handleLineLocked reacts to one completed input line.
//
// Any new command ends the previous run: the shell only reads a new line after
// the previous command exited, so this is the honest "the test is over" signal
// for a runner whose output we could not read.
func (t *testTap) handleLineLocked(line string) {
	if t.pending != nil {
		t.settleLocked()
	}
	if line == "" {
		return
	}
	runner, ok := testrun.Detect(line)
	if !ok {
		return
	}
	id, ok := t.create(line, runner)
	if !ok {
		return
	}
	now := time.Now()
	t.pending = &pendingTerminalRun{
		id: id, runner: string(runner), command: line,
		started: now, lastSeen: now,
	}
	t.timer = time.AfterFunc(terminalIdleSettle, t.expire)
}

// output consumes PTY bytes. offset is the stream offset of p[0]; bytes the tap
// has already seen are skipped so a reconnect replay cannot duplicate output.
func (t *testTap) output(offset int64, p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	end := offset + int64(len(p))
	if end <= t.high {
		return
	}
	if offset < t.high {
		p = p[t.high-offset:]
	}
	t.high = end

	if t.pending == nil {
		return
	}
	t.pending.buf = append(t.pending.buf, p...)
	if len(t.pending.buf) > terminalMaxBuffer {
		t.pending.buf = append([]byte(nil), t.pending.buf[len(t.pending.buf)-terminalMaxBuffer:]...)
	}
	t.pending.lastSeen = time.Now()
	if t.timer != nil {
		t.timer.Reset(terminalIdleSettle)
	}
	out := string(t.pending.buf)
	if testrun.Complete(t.pending.runner, out) || testrun.Aborted(out) {
		t.settleLocked()
	}
}

// expire settles a run that has printed nothing for the idle window.
func (t *testTap) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		return
	}
	if time.Since(t.pending.lastSeen) < terminalIdleSettle {
		if t.timer != nil {
			t.timer.Reset(terminalIdleSettle)
		}
		return
	}
	t.settleLocked()
}

// eof settles the open run when the shell exits: there will be no summary.
func (t *testTap) eof() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.settleLocked()
}

// settleLocked stores whatever the pending run printed. It parses even when no
// completion marker was seen, which is what turns a missing summary into an
// honest 未识别格式 row instead of a permanently running one.
func (t *testTap) settleLocked() {
	p := t.pending
	if p == nil {
		return
	}
	t.pending = nil
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	raw := string(p.buf)
	res := testrun.Parse(p.runner, raw)
	t.settle(p.id, res, raw, p.started)
}

// newTestTap builds a tap whose row writes go through the real store.
func (s *server) newTestTap(userID, sessionID, tabID string) *testTap {
	return &testTap{
		log:       s.log,
		userID:    userID,
		sessionID: sessionID,
		tabID:     tabID,
		create: func(command string, runner testrun.Runner) (string, bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			run, err := s.deps.Store.CreateTestRun(ctx, userID, store.NewTestRun{
				SessionID: sessionID,
				Source:    "terminal",
				Runner:    string(runner),
				Command:   command,
				StartedAt: time.Now(),
			})
			if err != nil {
				s.log.Debug("terminal test run not recorded", slog.String("error", err.Error()))
				return "", false
			}
			return run.ID, true
		},
		settle: func(runID string, res testrun.Result, raw string, started time.Time) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// The checkout root is resolved by settleTestRun from the session,
			// so a file:line in a terminal run is just as clickable as an
			// agent's.
			s.hub.settleTestRun(ctx, userID, sessionID, runID, res, raw, time.Since(started).Milliseconds())
		},
	}
}

// testTapFor returns the tab's tap, creating it on first use. A tab id already
// contains its session id (see handleSessionPTY), so one map keyed by tab is
// unambiguous.
func (s *server) testTapFor(userID, sessionID, tabID string) *testTap {
	s.tapsMu.Lock()
	defer s.tapsMu.Unlock()
	if tap, ok := s.taps[tabID]; ok {
		return tap
	}
	if s.taps == nil {
		s.taps = map[string]*testTap{}
	}
	tap := s.newTestTap(userID, sessionID, tabID)
	s.taps[tabID] = tap
	return tap
}

// dropTestTap forgets a tab whose shell exited. Any open run was settled first
// by eof(); keeping the map entry would only leak one small struct per closed
// terminal.
func (s *server) dropTestTap(tabID string) {
	s.tapsMu.Lock()
	defer s.tapsMu.Unlock()
	delete(s.taps, tabID)
}
