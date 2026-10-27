package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// sseBatch is how many events one replay query may return; a full batch means
// "there is probably more, do not sleep yet".
const sseBatch = 200

// handleEvents streams a session's event log as Server-Sent Events.
//
// The stream is a plain GET with no CSRF header, because EventSource cannot
// set one; authentication is the cookie alone. Cursors come from the standard
// Last-Event-ID header or ?after_seq=, and every frame carries `id: <seq>` and
// `event: <contracts event type>` so the client can switch on the event name.
//
// If the requested resume point is older than what is still retained, the
// handler answers 409 need_full_resync before any frame is written: silently
// skipping events would leave the transcript permanently wrong (PRD §7.4.5).
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.MustUser(ctx)

	sess, err := s.deps.Store.Session(ctx, user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	after := queryInt64(r, "after_seq", 0)
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		if n, err := strconv.ParseInt(header, 10, 64); err == nil && n > after {
			after = n
		}
	}

	minSeq, _, err := s.deps.Store.EventSeqRange(ctx, user.ID, sess.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if after > 0 && minSeq > 0 && after+1 < minSeq {
		s.fail(w, r, withStatus(http.StatusConflict, contracts.ErrNeedFullResync,
			"event history is no longer retained; resync with /entries"))
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	// Tell nginx and friends not to buffer: a buffered SSE stream is a dead
	// stream.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	poll := time.NewTicker(s.ssePoll)
	defer poll.Stop()
	keepalive := time.NewTicker(s.sseKeepalive)
	defer keepalive.Stop()

	last := after
	for {
		// Drain everything that is already durable, in bounded pages, before
		// waiting again.
		for {
			events, err := s.deps.Store.ListEvents(ctx, user.ID, sess.ID, last, sseBatch)
			if err != nil {
				// The client may have gone away, or the row may have been
				// deleted mid-stream. Either way the stream is over.
				return
			}
			if len(events) == 0 {
				break
			}
			for _, ev := range events {
				if err := writeSSE(w, ev); err != nil {
					return
				}
				last = ev.Seq
			}
			if err := rc.Flush(); err != nil {
				return
			}
			if len(events) < sseBatch {
				break
			}
		}

		select {
		case <-ctx.Done():
			// Client disconnected. The request goroutine is the only goroutine
			// involved, so returning here is the whole cleanup.
			return
		case <-poll.C:
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// writeSSE renders one frame. `event:` is the contracts type verbatim so the
// frontend's switch statement and the tests agree on the wire names.
func writeSSE(w io.Writer, ev *contracts.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("httpapi: encode event %d: %w", ev.Seq, err)
	}
	// JSON never contains a raw newline inside a string (it escapes it), so a
	// single data: line is always valid SSE.
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, payload)
	return err
}
