package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// ErrClosed is returned when a call is attempted on a bridge whose process has
// gone away. Callers treat it as "the agent is gone, the run was interrupted":
// PRD §7.4.5 forbids replaying it automatically.
var ErrClosed = errors.New("pi: bridge closed")

// CommandError reports a command that Pi answered with success:false. The
// message is Pi's own text, kept verbatim so the HTTP layer can surface it.
type CommandError struct {
	Command string
	Message string
}

// Error implements error.
func (e *CommandError) Error() string {
	return "pi: command " + e.Command + " failed: " + e.Message
}

// Defaults applied by Config.withDefaults.
const (
	// DefaultSubscriberBuffer is the channel capacity for one event consumer.
	DefaultSubscriberBuffer = 256
	// DefaultMaxBacklog bounds the events a subscriber may fall behind on
	// before cosmetic events start being dropped.
	DefaultMaxBacklog = 4096
	// DefaultCloseTimeout is how long Close waits for the peer to exit after
	// stdin is closed.
	DefaultCloseTimeout = 5 * time.Second
)

// Config describes how to launch, and how to serve, one Pi process.
type Config struct {
	// BinaryPath is the executable to run, e.g. "pi" or a fake peer's path.
	BinaryPath string
	// Args is the complete argv tail; the bridge does not add protocol flags.
	Args []string
	// Env holds extra KEY=VALUE entries appended to the inherited environment.
	Env []string
	// Dir is the working directory (the session's worktree).
	Dir string
	// SessionFile is the JSONL session file this process owns. It is reported
	// through Status and handed to the subprocess as --session=<file>.
	SessionFile string
	// Translator, when set, is installed before the read loop starts, so the
	// very first record is already normalized.
	Translator Translator
	// Stderr receives the child's stderr. Nil discards it, but the bridge keeps
	// the last few KiB either way so a crash can be explained.
	Stderr io.Writer
	// SubscriberBuffer overrides DefaultSubscriberBuffer.
	SubscriberBuffer int
	// MaxBacklog overrides DefaultMaxBacklog.
	MaxBacklog int
	// CloseTimeout overrides DefaultCloseTimeout.
	CloseTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.SubscriberBuffer <= 0 {
		c.SubscriberBuffer = DefaultSubscriberBuffer
	}
	if c.MaxBacklog <= 0 {
		c.MaxBacklog = DefaultMaxBacklog
	}
	if c.CloseTimeout <= 0 {
		c.CloseTimeout = DefaultCloseTimeout
	}
	return c
}

// Translator turns one raw Pi record into the normalized events subscribers
// receive. It runs on the read loop, so it must be fast and must never block;
// returning an error only drops that record.
type Translator func(raw []byte) ([]contracts.Event, error)

// EchoTranslator wraps every record in a single event so tests (and debugging
// sessions) can observe the raw stream. It is not used in production: the
// runtime installs a Normalizer instead.
func EchoTranslator(raw []byte) ([]contracts.Event, error) {
	payload := append(json.RawMessage(nil), raw...)
	return []contracts.Event{{
		Type:    "pi.record",
		TS:      time.Now().UTC(),
		Payload: payload,
	}}, nil
}

// closeWriter is implemented by transports that can half-close the write side
// so the peer sees EOF on stdin while stdout stays readable.
type closeWriter interface {
	CloseWrite() error
}

// Bridge owns one Pi child process (or an equivalent connected transport) and
// is the only place that touches its stdin and stdout.
//
// Responsibilities: strict LF framing, id-keyed command/response correlation,
// and a fan-out that never blocks the read loop. It knows nothing about
// contracts.Event semantics beyond the drop policy; normalization is injected
// through SetTranslator.
type Bridge struct {
	cfg    Config
	stream io.ReadWriteCloser
	cmd    *exec.Cmd

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan Response
	nextID    atomic.Uint64

	translatorMu sync.Mutex
	translator   Translator

	subsMu sync.Mutex
	subs   map[*subscriber]struct{}

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error

	errMu   sync.Mutex
	readErr error
	stderr  *tailBuffer

	malformed atomic.Int64
	started   atomic.Bool
}

// NewBridge prepares a bridge for cfg. It does not touch the filesystem or
// spawn anything; call Start to launch the child. Subscribe before Start so no
// output record can be missed.
func NewBridge(cfg Config) *Bridge {
	cfg = cfg.withDefaults()
	return &Bridge{
		cfg:        cfg,
		pending:    map[string]chan Response{},
		subs:       map[*subscriber]struct{}{},
		done:       make(chan struct{}),
		stderr:     newTailBuffer(8 << 10),
		translator: cfg.Translator,
	}
}

// NewStream wires a bridge to an already-connected transport — the sandbox
// shim's stdio channel, or a test peer over io.Pipe — and starts reading
// immediately. Start must not be called afterwards.
func NewStream(t io.ReadWriteCloser, cfg Config) *Bridge {
	b := NewBridge(cfg)
	if t == nil {
		b.setReadErr(errors.New("pi: nil transport"))
		close(b.done)
		return b
	}
	b.stream = t
	b.started.Store(true)
	go b.readLoop(bufio.NewReaderSize(t, 64<<10))
	return b
}

// Start spawns cfg.BinaryPath and starts the read loop. The process outlives
// ctx: it is a long-lived per-session agent, and Close is what ends it, so a
// cancelled request context cannot orphan or kill a session mid-run.
func (b *Bridge) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pi: start: %w", err)
	}
	if b.started.Swap(true) {
		return errors.New("pi: start: bridge already started")
	}
	if b.cfg.BinaryPath == "" {
		return errors.New("pi: start: BinaryPath is empty")
	}
	args := b.cfg.Args
	if b.cfg.SessionFile != "" {
		args = append(append([]string(nil), args...), "--session="+b.cfg.SessionFile)
	}
	cmd := exec.Command(b.cfg.BinaryPath, args...)
	cmd.Dir = b.cfg.Dir
	cmd.Env = append(os.Environ(), b.cfg.Env...)
	if b.cfg.Stderr != nil {
		cmd.Stderr = io.MultiWriter(b.cfg.Stderr, b.stderr)
	} else {
		cmd.Stderr = b.stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("pi: start: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("pi: start: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("pi: start %s: %w", b.cfg.BinaryPath, err)
	}
	b.cmd = cmd
	b.stream = &procStream{in: stdin, out: stdout, cmd: cmd}
	go b.readLoop(bufio.NewReaderSize(stdout, 64<<10))
	return nil
}

// PID returns the child's process id, or 0 for a stream-backed bridge.
func (b *Bridge) PID() int {
	if b.cmd == nil || b.cmd.Process == nil {
		return 0
	}
	return b.cmd.Process.Pid
}

// SessionFile returns the JSONL file this process owns.
func (b *Bridge) SessionFile() string { return b.cfg.SessionFile }

// Done is closed when the read loop ends, i.e. when the peer is gone.
func (b *Bridge) Done() <-chan struct{} { return b.done }

// Alive reports whether the peer is still connected.
func (b *Bridge) Alive() bool {
	select {
	case <-b.done:
		return false
	default:
		return true
	}
}

// MalformedRecords counts stdout lines that were not valid JSON envelopes. A
// non-zero value with a working agent means Pi is writing something the bridge
// does not understand; it is a metric, not an error.
func (b *Bridge) MalformedRecords() int64 { return b.malformed.Load() }

// Err returns the error that ended the read loop, if any.
func (b *Bridge) Err() error {
	b.errMu.Lock()
	defer b.errMu.Unlock()
	return b.readErr
}

// StderrTail returns the tail of the child's stderr, used to explain a crash.
func (b *Bridge) StderrTail() string { return b.stderr.String() }

// SetTranslator installs the record translator. It must be called before Start
// to guarantee no record is missed.
func (b *Bridge) SetTranslator(t Translator) {
	b.translatorMu.Lock()
	b.translator = t
	b.translatorMu.Unlock()
}

func (b *Bridge) translate(raw []byte) ([]contracts.Event, error) {
	b.translatorMu.Lock()
	t := b.translator
	b.translatorMu.Unlock()
	if t == nil {
		t = EchoTranslator
	}
	return t(raw)
}

// ---------------------------------------------------------------------------
// Read loop and dispatch
// ---------------------------------------------------------------------------

// readLoop splits stdout on '\n' only.
//
// bufio.Reader.ReadBytes is deliberate: bufio.Scanner with ScanLines is safe
// too, but any reader that also splits on U+2028/U+2029 would corrupt payloads,
// because those characters are legal inside JSON strings. Trailing "\r" is
// trimmed for peers that emit CRLF, empty lines are skipped, and a malformed
// record increments a counter instead of killing the session.
func (b *Bridge) readLoop(r *bufio.Reader) {
	defer close(b.done)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			b.dispatchLine(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
				b.setReadErr(fmt.Errorf("pi: read loop: %w", err))
			}
			return
		}
	}
}

func (b *Bridge) dispatchLine(line []byte) {
	line = trimRecord(line)
	if len(line) == 0 {
		return
	}
	var env record
	if err := json.Unmarshal(line, &env); err != nil {
		b.malformed.Add(1)
		return
	}
	if env.Type == "" {
		b.malformed.Add(1)
		return
	}
	if env.Type == "response" {
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			b.malformed.Add(1)
			return
		}
		b.deliver(resp)
		return
	}
	events, err := b.translate(append(json.RawMessage(nil), line...))
	if err != nil {
		b.malformed.Add(1)
		return
	}
	b.fanout(events)
}

// deliver hands a response to the goroutine waiting on its id. The channel is
// buffered, so a late reply to a cancelled call is discarded rather than
// blocking the read loop.
func (b *Bridge) deliver(resp Response) {
	if resp.ID == "" {
		return
	}
	b.pendingMu.Lock()
	ch := b.pending[resp.ID]
	b.pendingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// trimRecord removes the framing terminator. Only "\n" and one preceding "\r"
// are stripped: whitespace inside a JSON string is content, not framing.
func trimRecord(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line
}

func (b *Bridge) setReadErr(err error) {
	b.errMu.Lock()
	if b.readErr == nil {
		b.readErr = err
	}
	b.errMu.Unlock()
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// Call sends one command record and waits for the reply carrying the same id.
//
// Pi handles commands asynchronously, so replies may arrive in any order and
// are matched by id only. fields is merged into the record; its keys may not
// include "type" or "id", which the bridge sets.
func (b *Bridge) Call(ctx context.Context, typeName string, fields map[string]any) (Response, error) {
	if typeName == "" {
		return Response{}, errors.New("pi: call: empty command type")
	}
	select {
	case <-b.done:
		return Response{}, fmt.Errorf("pi: call %s: %w", typeName, ErrClosed)
	default:
	}
	id := fmt.Sprintf("pf-%d-%d", os.Getpid(), b.nextID.Add(1))
	rec := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		if k == "type" || k == "id" {
			continue
		}
		rec[k] = v
	}
	rec["type"] = typeName
	rec["id"] = id

	ch := make(chan Response, 1)
	b.pendingMu.Lock()
	b.pending[id] = ch
	b.pendingMu.Unlock()
	defer func() {
		b.pendingMu.Lock()
		delete(b.pending, id)
		b.pendingMu.Unlock()
	}()

	if err := b.writeRecord(rec); err != nil {
		if !b.Alive() {
			return Response{}, fmt.Errorf("pi: call %s: %w", typeName, ErrClosed)
		}
		return Response{}, fmt.Errorf("pi: call %s: %w", typeName, err)
	}
	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, &CommandError{Command: typeName, Message: resp.Error}
		}
		return resp, nil
	case <-ctx.Done():
		return Response{}, fmt.Errorf("pi: call %s: %w", typeName, ctx.Err())
	case <-b.done:
		if err := b.Err(); err != nil {
			return Response{}, fmt.Errorf("pi: call %s: %w", typeName, err)
		}
		return Response{}, fmt.Errorf("pi: call %s: %w", typeName, ErrClosed)
	}
}

// CallInto is Call with the response payload decoded into out. A nil out only
// checks that the command succeeded.
func (b *Bridge) CallInto(ctx context.Context, typeName string, fields map[string]any, out any) error {
	resp, err := b.Call(ctx, typeName, fields)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(resp.Data) == 0 {
		return fmt.Errorf("pi: command %s: empty response data", typeName)
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("pi: decode %s response: %w", typeName, err)
	}
	return nil
}

func (b *Bridge) writeRecord(rec any) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal command: %w", err)
	}
	payload = append(payload, '\n')
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if b.stream == nil {
		return ErrClosed
	}
	if _, err := b.stream.Write(payload); err != nil {
		return fmt.Errorf("write command: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Close stops the peer: it closes stdin (asking Pi to unwind and exit), waits
// up to CloseTimeout for the read loop to drain, kills the process if it is
// still alive, then closes every subscription. It is idempotent.
func (b *Bridge) Close() error {
	b.closeOnce.Do(func() {
		if b.stream == nil {
			b.closeSubs()
			return
		}
		if cw, ok := b.stream.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		select {
		case <-b.done:
		case <-time.After(b.cfg.CloseTimeout):
			// stdin EOF did not make the peer exit: kill it. Closing the
			// transport below unblocks the read loop either way.
			if b.cmd != nil && b.cmd.Process != nil {
				_ = b.cmd.Process.Kill()
			}
		}
		b.closeErr = b.stream.Close()
		select {
		case <-b.done:
		case <-time.After(2 * time.Second):
		}
		b.closeSubs()
	})
	return b.closeErr
}

// procStream adapts a child process's stdio to io.ReadWriteCloser. Close waits
// for the process, then kills it, so a bridge never leaks a child.
type procStream struct {
	in        io.WriteCloser
	out       io.ReadCloser
	cmd       *exec.Cmd
	writeOnce sync.Once
	closeOnce sync.Once
	err       error
}

func (p *procStream) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *procStream) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *procStream) CloseWrite() error {
	p.writeOnce.Do(func() { _ = p.in.Close() })
	return nil
}

func (p *procStream) Close() error {
	_ = p.CloseWrite()
	p.closeOnce.Do(func() {
		_ = p.in.Close()
		waited := make(chan struct{})
		go func() {
			p.err = p.cmd.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			_ = p.cmd.Process.Kill()
			<-waited
		}
		_ = p.out.Close()
	})
	return p.err
}

// ---------------------------------------------------------------------------
// tailBuffer
// ---------------------------------------------------------------------------

// tailBuffer keeps the last max bytes written to it, so a crashed peer can be
// explained without buffering an unbounded stderr.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

// Write implements io.Writer, always reporting success: the child's stderr must
// never block on our bookkeeping.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
