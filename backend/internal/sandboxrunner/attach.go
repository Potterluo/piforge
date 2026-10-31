package sandboxrunner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The seventh endpoint: attach a byte stream to one process inside a sandbox.
//
// It exists because the agent is not a request/response workload. `pi --mode rpc`
// speaks JSONL over stdin/stdout for the life of a session, and the control
// plane's bridge frames that stream itself (internal/pi). The sandbox's own
// /v1/shell/exec is the panel's transport — a persistent, line-oriented shell
// session shared with the user's terminal — and it cannot carry this stream.
//
// The contract, in full:
//
//	POST /v1/sandboxes/{id}/attach   {argv, dir, env, user}
//	  → 200, no Content-Length, the process is already running
//	    response body  = the process's stdout
//	    request socket = the process's stdin (half-close = EOF)
//	    process exit   = end of body
//	→ 400 bad body / empty argv, 404 unknown sandbox, 501 provider cannot attach,
//	  500 the process did not start (its stderr tail is in the message)
//
// Auth is the same bearer token as every other route. The stream is *not* bound
// to the request context: hijacking a connection cancels that context, and the
// agent must outlive the HTTP request that started it. The handler owns the
// process for exactly as long as the connection lives.
//
// What this widens: the runner can now start a process in a sandbox it manages.
// It could already create an arbitrary container, so this is not a new privilege
// over the daemon; it is a new verb on the service, and the token that opens it
// opens every sandbox. docs/EXECUTION-BOUNDARY.md §9 states that tradeoff.

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	var spec sandbox.AttachSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		writeError(w, http.StatusBadRequest, "argv is required")
		return
	}
	attacher, ok := s.cfg.Provider.(sandbox.Attacher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "this runner cannot attach: its provider has no exec surface")
		return
	}

	// Not r.Context(): see the comment above. Cancelling this context is how the
	// handler tears the process down when either side of the splice ends.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := attacher.Attach(ctx, r.PathValue("id"), spec)
	if err != nil {
		s.fail(r, w, err)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = stream.Close()
		writeError(w, http.StatusInternalServerError, "this server cannot hijack a connection")
		return
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		_ = stream.Close()
		s.cfg.Logger.Error("sandbox runner could not hijack a connection for attach",
			"path", r.URL.Path, "error", err.Error())
		return
	}
	// The status line is written by hand because the response has no length and
	// no chunking: its body is the process's stdout until the process exits.
	if _, err := buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\n\r\n"); err != nil {
		_ = conn.Close()
		_ = stream.Close()
		return
	}
	if err := buf.Flush(); err != nil {
		_ = conn.Close()
		_ = stream.Close()
		return
	}

	done := make(chan struct{}, 2)
	// stdout → client. Closing the conn when this ends tells the client the
	// process is gone, which is exactly the stream's EOF.
	go func() {
		_, _ = io.Copy(conn, stream)
		_ = conn.Close()
		done <- struct{}{}
	}()
	// client → stdin. A client that half-closes makes this return; closing the
	// stream's write side then gives the process EOF, so it can exit and the
	// goroutine above finishes.
	go func() {
		_, _ = io.Copy(stream, buf)
		if cw, ok := stream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = stream.Close()
		}
		done <- struct{}{}
	}()

	<-done
	cancel()
	_ = conn.Close()
	_ = stream.Close()
	// Drain the second goroutine so the process cannot be left half-closed while
	// this handler returns and its context is cancelled.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
