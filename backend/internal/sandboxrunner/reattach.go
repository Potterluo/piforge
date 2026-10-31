package sandboxrunner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The eighth endpoint: connect to a process that is already inside a sandbox.
//
// It exists because of what stage 3 of docs/EXECUTION-BOUNDARY.md changed: the
// agent no longer dies with the control plane, so after a restart the control
// plane must reach the *same* process rather than start a second one. `attach`
// cannot express that — it takes an argv and always starts something. A verb
// whose body is a socket and never an argv makes "start another agent" not
// expressible, which is the failure this endpoint exists to rule out.
//
// The contract, in full:
//
//	POST /v1/sandboxes/{id}/reattach   {socket, helper, dir, env, user}
//	  → 200, no Content-Length, the helper is already running
//	    response body  = the helper's stdout (the supervisor's handshake, then
//	                     the agent's bytes)
//	    request socket = the helper's stdin (half-close = EOF)
//	    helper exit    = end of body
//	→ 400 bad body / missing socket or helper, 404 unknown sandbox,
//	  501 this provider cannot attach, 500 the helper did not start
//
// Auth is the same bearer token as every other route, and the stream is not
// bound to the request context for the same reason attach's is not: hijacking
// cancels that context, and the helper must be able to finish its handshake.
func (s *Server) handleReattach(w http.ResponseWriter, r *http.Request) {
	var spec sandbox.ReattachSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if strings.TrimSpace(spec.Socket) == "" {
		writeError(w, http.StatusBadRequest, "socket is required")
		return
	}
	if len(spec.Helper) == 0 || strings.TrimSpace(spec.Helper[0]) == "" {
		writeError(w, http.StatusBadRequest, "helper is required: the argv prefix that connects to the socket")
		return
	}
	reattacher, ok := s.cfg.Provider.(sandbox.Reattacher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "this runner cannot reattach: its provider has no exec surface")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := reattacher.Reattach(ctx, r.PathValue("id"), spec)
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
		s.cfg.Logger.Error("sandbox runner could not hijack a connection for reattach",
			"path", r.URL.Path, "error", err.Error())
		return
	}
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
	go func() {
		_, _ = io.Copy(conn, stream)
		_ = conn.Close()
		done <- struct{}{}
	}()
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
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
