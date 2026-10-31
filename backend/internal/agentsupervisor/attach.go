package agentsupervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// Attaching to a supervisor: the client half of the protocol.
//
// The wire shape is one JSON line followed by the agent's bytes:
//
//	{"type":"ready","session":"…","pid":812,"replay_from":0,"replay_bytes":0}\n
//	…agent stdout, exactly as the agent wrote it…
//
// The frame is read and written out untouched by Attach, so the process on the
// other end of the runner's exec stream (the control plane's bridge) can consume
// exactly one line and hand the remainder to its JSONL framing. Everything after
// that line is the agent: no framing, no escaping, no second protocol.
//
// Who runs this:
//
//   - `piforged agent-attach -socket …` inside the sandbox, started by the
//     runner's reattach verb. Its stdio is the hijacked exec stream; its Unix
//     socket is the supervisor.
//   - `piforged agent-supervisor-start`, which starts a supervisor when there is
//     none and then attaches to it, so a first launch and a reattach are the same
//     code path from the control plane's point of view.
//
// A failed attach must fail *fast and quietly*: the control plane treats "the
// stream ended before the handshake" as "no supervisor here, start one", so an
// error message on stderr is for the operator, and no bytes may reach stdout.

// Attach connects to socket, copies the ready frame to stdout, then splices:
// stdin → socket and socket → stdout until one side ends.
func Attach(socket string, sessionID string, stdin io.Reader, stdout io.Writer) error {
	conn, err := DialUnix(socket, DefaultReadyTimeout)
	if err != nil {
		return fmt.Errorf("agentsupervisor: connect to %s: %w", socket, err)
	}
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, 64<<10)
	frame, line, err := readFrame(reader)
	if err != nil {
		return fmt.Errorf("agentsupervisor: %s did not answer with a handshake: %w", socket, err)
	}
	if frame.Type != "ready" {
		msg := frame.Message
		if msg == "" {
			msg = frame.Type
		}
		return fmt.Errorf("agentsupervisor: %s refused this client: %s", socket, msg)
	}
	if sessionID != "" && frame.Session != sessionID {
		return fmt.Errorf("agentsupervisor: %s hosts session %q, not %q", socket, frame.Session, sessionID)
	}
	if frame.PID <= 0 {
		return fmt.Errorf("agentsupervisor: %s reported no agent process", socket)
	}
	// The frame is the control plane's proof that an agent is there. It must be
	// forwarded byte for byte; the bridge reads it as one line.
	if _, err := stdout.Write(line); err != nil {
		return fmt.Errorf("agentsupervisor: forward the handshake: %w", err)
	}

	// stdin → socket. When stdin ends, half-close so the supervisor's client
	// loop ends too — and the agent keeps running, which is the difference
	// between this and a pipe owned by the control plane.
	go func() {
		_, _ = io.Copy(conn, stdin)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	// socket → stdout, starting with whatever the bufio.Reader buffered past the
	// handshake line.
	_, copyErr := io.Copy(stdout, reader)
	if copyErr != nil && !isClosed(copyErr) {
		return fmt.Errorf("agentsupervisor: relay the agent stream: %w", copyErr)
	}
	return nil
}

// readFrame reads exactly one handshake line and returns it verbatim (with its
// newline) alongside the decoded frame.
func readFrame(r *bufio.Reader) (Frame, []byte, error) {
	var frame Frame
	line, err := r.ReadBytes('\n')
	if err != nil {
		if len(line) == 0 {
			return frame, nil, err
		}
		return frame, nil, fmt.Errorf("truncated handshake: %w", err)
	}
	if len(line) > frameLimit {
		return frame, nil, errors.New("handshake line is too long")
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return frame, nil, fmt.Errorf("malformed handshake %q: %w", strings.TrimSpace(string(line)), err)
	}
	return frame, line, nil
}

// ReadFrame reads one handshake line from an arbitrary stream. The control
// plane uses it on the stream the runner's attach/reattach verb handed back.
func ReadFrame(r io.Reader) (Frame, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64<<10)
	}
	frame, _, err := readFrame(br)
	return frame, err
}

func isClosed(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "use of closed network connection")
}

// socketReachable reports whether somebody is listening, without consuming a
// handshake. Used to decide between "attach" and "start".
func socketReachable(socket string) bool {
	if strings.TrimSpace(socket) == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
