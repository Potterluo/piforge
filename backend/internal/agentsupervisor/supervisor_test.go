package agentsupervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// The supervisor's protocol is tested over a TCP listener injected through
// Config.Listen, because Go's net package has no AF_UNIX on Windows and this
// suite must run there. Everything above the socket — the handshake, the replay
// window, the takeover rule — is the same code either way.

func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// dialFrame connects and reads the one handshake line, returning the frame and
// the buffered reader positioned on the agent stream.
func dialFrame(t *testing.T, addr string) (Frame, *bufio.Reader, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	frame, _, err := readFrame(reader)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read handshake: %v", err)
	}
	return frame, reader, conn
}

// TestSupervisorKeepsTheAgentAliveWhenTheClientLeaves is the stage-3 property:
// the client (the control plane's stream) goes away, the agent keeps running,
// and a second client receives the bytes produced while nobody was attached.
func TestSupervisorKeepsTheAgentAliveWhenTheClientLeaves(t *testing.T) {
	t.Setenv("PIFORGE_AGENTSUPERVISOR_CHILD", "1")
	ln := tcpListener(t)
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Root:      t.TempDir(),
			SessionID: "ses_test",
			Socket:    "test.sock",
			StderrLog: t.TempDir() + string(os.PathSeparator) + "agent.log",
			Listen:    func(string) (net.Listener, error) { return ln, nil },
			Dial:      DialUnix,
		}, []string{os.Args[0], "-test.run=TestAgentChildProcess", "--"})
	}()
	t.Cleanup(func() { cancel() })

	var first Frame
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no handshake within the deadline")
		}
		frame, reader, conn := dialFrame(t, addr)
		if frame.PID > 0 {
			first = frame
			// Consume a couple of records, then walk away as a dying control
			// plane would.
			buf := make([]byte, 256)
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _ := reader.Read(buf)
			if n == 0 {
				_ = conn.Close()
				time.Sleep(50 * time.Millisecond)
				continue
			}
			_ = conn.Close()
			break
		}
		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	if first.Session != "ses_test" {
		t.Fatalf("handshake session = %q", first.Session)
	}

	// With no client, the agent keeps writing. Give it time to produce records
	// nobody is reading.
	time.Sleep(400 * time.Millisecond)

	second, reader, conn := dialFrame(t, addr)
	defer conn.Close()
	if second.ReplayBytes <= 0 {
		t.Fatalf("the reattaching client was given no backlog: %+v", second)
	}
	if second.Gap != 0 {
		t.Fatalf("a 400ms detach should not overflow the backlog: gap=%d", second.Gap)
	}
	if second.PID != first.PID {
		t.Fatalf("reattach reports agent pid %d, first client saw %d: the agent was not the same process", second.PID, first.PID)
	}

	// The replayed bytes must be whole JSONL records, and must include the
	// counter values produced while detached.
	replayed := make([]byte, second.ReplayBytes)
	if _, err := ioReadFull(reader, replayed); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if !bytes.HasSuffix(replayed, []byte("\n")) {
		t.Fatalf("replay does not end on a record boundary: %q", string(replayed[max(0, len(replayed)-40):]))
	}
	var last int
	for _, line := range strings.Split(strings.TrimRight(string(replayed), "\n"), "\n") {
		var rec struct {
			Type string `json:"type"`
			N    int    `json:"n"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("replayed line is not JSON: %q: %v", line, err)
		}
		if rec.Type != "tick" {
			t.Fatalf("replayed record type = %q", rec.Type)
		}
		last = rec.N
	}
	if last < 3 {
		t.Fatalf("replay held only %d records; the agent did not keep running while detached", last)
	}
}

// TestSupervisorSendsAHandshakeBeforeAnyAgentByte pins the shape the control
// plane's bridge depends on: exactly one line, then raw agent bytes.
func TestSupervisorSendsAHandshakeBeforeAnyAgentByte(t *testing.T) {
	t.Setenv("PIFORGE_AGENTSUPERVISOR_CHILD", "1")
	ln := tcpListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{
			Root:      t.TempDir(),
			SessionID: "ses_frame",
			Socket:    "test.sock",
			StderrLog: t.TempDir() + string(os.PathSeparator) + "agent.log",
			Listen:    func(string) (net.Listener, error) { return ln, nil },
			Dial:      DialUnix,
		}, []string{os.Args[0], "-test.run=TestAgentChildProcess", "--"})
	}()

	addr := ln.Addr().String()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no handshake")
		}
		frame, reader, conn := dialFrame(t, addr)
		if frame.PID == 0 {
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		defer conn.Close()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("first agent byte: %v", err)
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("the byte after the handshake is not a record: %q", line)
		}
		if rec["type"] != "tick" {
			t.Fatalf("record type = %v", rec["type"])
		}
		return
	}
}

// TestBacklogDropsWholeRecords. The resumed channel must always be valid JSONL;
// an over-full buffer therefore drops records, never half of one.
func TestBacklogDropsWholeRecords(t *testing.T) {
	b := newBacklog(200)
	for i := 0; i < 100; i++ {
		b.append([]byte(fmt.Sprintf("{\"type\":\"tick\",\"n\":%d}\n", i)))
	}
	start, data, gap := b.snapshotFrom(0)
	if gap <= 0 {
		t.Fatalf("expected a gap, got %d (buffer %d bytes)", gap, len(data))
	}
	if start <= 0 {
		t.Fatalf("resume offset = %d", start)
	}
	if len(data) > 200 {
		t.Fatalf("backlog kept %d bytes for a 200-byte ceiling", len(data))
	}
	if !bytes.HasPrefix(data, []byte("{")) || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("the window does not start and end on record boundaries: %q", string(data[:min(20, len(data))]))
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("trimmed buffer has a partial record: %q", line)
		}
	}
}

// TestBacklogSnapshotAtEnd. A peer that claims to have read everything gets
// nothing replayed.
func TestBacklogSnapshotAtEnd(t *testing.T) {
	b := newBacklog(1024)
	end := b.append([]byte("hello\n"))
	start, data, gap := b.snapshotFrom(end)
	if len(data) != 0 || gap != 0 || start != end {
		t.Fatalf("snapshot at end = (%d, %q, %d)", start, data, gap)
	}
	start, data, gap = b.snapshotFrom(end + 100)
	if len(data) != 0 || gap != 0 || start != end {
		t.Fatalf("snapshot past end = (%d, %q, %d)", start, data, gap)
	}
}

// TestValidateSessionIDKeepsThePathOneSegment.
func TestValidateSessionIDKeepsThePathOneSegment(t *testing.T) {
	good := []string{"ses_01J8", "abc-123", "a.b"}
	for _, id := range good {
		if err := ValidateSessionID(id); err != nil {
			t.Fatalf("%q rejected: %v", id, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", "a\\b", "a b", strings.Repeat("x", 129)}
	for _, id := range bad {
		if err := ValidateSessionID(id); err == nil {
			t.Fatalf("%q accepted", id)
		}
	}
}

// TestAgentChildProcess is the agent the tests above host. It speaks the only
// thing the supervisor cares about: JSONL on stdout, and it keeps ticking until
// its stdin closes.
func TestAgentChildProcess(t *testing.T) {
	if os.Getenv("PIFORGE_AGENTSUPERVISOR_CHILD") != "1" {
		t.Skip("helper process")
	}
	stdout := os.Stdout
	go func() {
		_, _ = fmt.Fprintln(os.Stderr, "child: started")
	}()
	for i := 0; ; i++ {
		if _, err := fmt.Fprintf(stdout, "{\"type\":\"tick\",\"n\":%d}\n", i); err != nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
		// stdin is deliberately not read: the supervisor must be able to host a
		// child whose input nobody is feeding, exactly as a detached agent is.
	}
}

// ioReadFull is io.ReadFull without importing io in every test.
func ioReadFull(r *bufio.Reader, p []byte) (int, error) {
	n := 0
	for n < len(p) {
		m, err := r.Read(p[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// TestSupervisorCarriesSeveralRPCRoundTrips is the shape the control plane
// actually uses: a command with an id, a response with the same id, several
// times, on one attached stream. A supervisor that only survived the first
// round trip would look fine in a one-shot test and break every second prompt.
func TestSupervisorCarriesSeveralRPCRoundTrips(t *testing.T) {
	t.Setenv("PIFORGE_AGENTSUPERVISOR_RPC_CHILD", "1")
	ln := tcpListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{
			Root:      t.TempDir(),
			SessionID: "ses_rpc",
			Socket:    "test.sock",
			StderrLog: t.TempDir() + string(os.PathSeparator) + "agent.log",
			Listen:    func(string) (net.Listener, error) { return ln, nil },
			Dial:      DialUnix,
		}, []string{os.Args[0], "-test.run=TestRPCChildProcess", "--"})
	}()

	addr := ln.Addr().String()
	var reader *bufio.Reader
	var conn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no handshake")
		}
		frame, r, c := dialFrame(t, addr)
		if frame.PID == 0 {
			_ = c.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		reader, conn = r, c
		break
	}
	defer conn.Close()

	lr := newLineReader(reader)
	for round := 1; round <= 3; round++ {
		id := fmt.Sprintf("pf-test-%d", round)
		line := fmt.Sprintf("{\"type\":\"prompt\",\"id\":%q,\"message\":\"round %d\"}\n", id, round)
		if _, err := conn.Write([]byte(line)); err != nil {
			t.Fatalf("round %d write: %v", round, err)
		}
		got := ""
		for got != id {
			line, err := lr.next(5 * time.Second)
			if err != nil {
				t.Fatalf("round %d: %v (got id %q)", round, err, got)
			}
			var rec struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatalf("round %d: not JSON: %q", round, line)
			}
			if rec.Type == "response" {
				got = rec.ID
			}
		}
	}
}

// TestRPCChildProcess answers each prompt with a response carrying its id, the
// way `pi --mode rpc` does (measured inside the sandbox: one response record,
// then the turn's events).
func TestRPCChildProcess(t *testing.T) {
	if os.Getenv("PIFORGE_AGENTSUPERVISOR_RPC_CHILD") != "1" {
		t.Skip("helper process")
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var rec struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Type == "prompt" {
				_, _ = fmt.Fprintf(os.Stdout, "{\"id\":%q,\"type\":\"response\",\"command\":\"prompt\",\"success\":true,\"data\":{\"disposition\":\"started\"}}\n", rec.ID)
				_, _ = fmt.Fprintf(os.Stdout, "{\"type\":\"agent_start\"}\n")
				_, _ = fmt.Fprintf(os.Stdout, "{\"type\":\"turn_end\"}\n")
			}
		}
		if err != nil {
			return
		}
	}
}

// lineReader reads whole lines from a stream on one goroutine, so a slow round
// trip cannot leave a stale reader behind (two concurrent readers of the same
// bufio.Reader interleave partial lines).
type lineReader struct {
	lines chan []byte
	errs  chan error
}

func newLineReader(r *bufio.Reader) *lineReader {
	lr := &lineReader{lines: make(chan []byte, 64), errs: make(chan error, 1)}
	go func() {
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				lr.lines <- line
			}
			if err != nil {
				lr.errs <- err
				return
			}
		}
	}()
	return lr
}

func (l *lineReader) next(timeout time.Duration) ([]byte, error) {
	select {
	case line := <-l.lines:
		return line, nil
	case err := <-l.errs:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for a record")
	}
}

// TestSupervisorWritesTheAgentStderrToItsLog is stage 3 item 3: the agent's
// stderr must land in a file the control plane can read through the sandbox API,
// not only in the runner's log. This is the write half of that path (the read
// half is sandboxAgent.Logs and the /v1/sessions/{id}/logs endpoint).
func TestSupervisorWritesTheAgentStderrToItsLog(t *testing.T) {
	t.Setenv("PIFORGE_AGENTSUPERVISOR_CHILD", "1")
	ln := tcpListener(t)
	dir := t.TempDir()
	logPath := dir + string(os.PathSeparator) + "agent.log"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{
			Root:      dir,
			SessionID: "ses_log",
			Socket:    "test.sock",
			StderrLog: logPath,
			Listen:    func(string) (net.Listener, error) { return ln, nil },
			Dial:      DialUnix,
		}, []string{os.Args[0], "-test.run=TestAgentChildProcess", "--"})
	}()

	// The child prints "child: started" on stderr before it ticks.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the child's stderr never reached the log file")
		}
		raw, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(raw), "child: started") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
