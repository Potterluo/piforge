package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// maxLineageDepth bounds the parent walk. A fork chain deeper than this is
// either corrupt or a bug; stopping is better than recursing on a cycle.
const maxLineageDepth = 32

// lineageBlock is one slice of a merged transcript.
//
// A branch does not copy its parent's events: it records where the parent's log
// ended (forked_at_seq) and the transcript view concatenates the ancestors'
// events up to their cuts with the branch's own. The concatenation needs a
// single monotonic seq for the SSE cursor, so every block gets a virtual base:
//
//	virtual seq = base + own seq
//
// Bases are the running sum of the previous blocks' cuts. Because each ancestor
// block keeps at most `cutoff` events (seq 1..cutoff), the virtual ranges touch
// but never overlap, and the branch's own first event follows the inherited
// transcript contiguously. The cut is frozen at fork time, so a parent event
// written later lands past the branch's window and can never leak in.
type lineageBlock struct {
	// sessionID is the log this block reads. Empty is a placeholder for a
	// parent that no longer exists: it contributes no events but keeps the
	// branch's virtual seq space stable.
	sessionID string
	// cutoff is the highest own seq included. Zero means "unbounded": only the
	// target session's own block uses that (a branch may keep writing).
	cutoff int64
	// base is added to every own seq to get the transcript seq.
	base int64
}

// sessionLineage returns the root-to-target block list for a session, or just
// the target's own block when it is not a fork.
func (s *Store) sessionLineage(ctx context.Context, userID string, sess *contracts.Session) ([]lineageBlock, error) {
	blocks, err := s.lineageFor(ctx, userID, sess, 0)
	if err != nil {
		return nil, err
	}
	// Keep the invariant that the last block belongs to the target and is
	// unbounded. lineageFor already does this; the check makes a future edit
	// that breaks it fail loudly in tests rather than silently truncating.
	last := blocks[len(blocks)-1]
	if last.sessionID != sess.ID || last.cutoff != 0 {
		return nil, fmt.Errorf("store: lineage for %q does not end in its own block", sess.ID)
	}
	return blocks, nil
}

func (s *Store) lineageFor(ctx context.Context, userID string, sess *contracts.Session, depth int) ([]lineageBlock, error) {
	own := []lineageBlock{{sessionID: sess.ID}}
	if depth > maxLineageDepth || sess.ParentID == "" || sess.ForkedAtSeq <= 0 {
		return own, nil
	}

	parent, err := s.Session(ctx, userID, sess.ParentID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// The parent was removed outside the API (the delete endpoint
			// refuses while forks depend on it, but a project cascade or a
			// manual delete can still take it). Keep the cut as an empty block
			// so the branch's own seqs do not shift under a connected client;
			// the inherited history is honestly gone, not silently replayed.
			return []lineageBlock{
				{sessionID: "", cutoff: sess.ForkedAtSeq},
				{sessionID: sess.ID, base: sess.ForkedAtSeq},
			}, nil
		}
		return nil, err
	}

	inherited, err := s.lineageFor(ctx, userID, parent, depth+1)
	if err != nil {
		return nil, err
	}
	inherited = truncateLineage(inherited, sess.ForkedAtSeq)

	var base int64
	if n := len(inherited); n > 0 {
		base = inherited[n-1].base + inherited[n-1].cutoff
	}
	blocks := make([]lineageBlock, 0, len(inherited)+1)
	blocks = append(blocks, inherited...)
	blocks = append(blocks, lineageBlock{sessionID: sess.ID, base: base})
	return blocks, nil
}

// truncateLineage keeps the prefix of `blocks` that lies at or before `at`
// (a transcript seq in the parent's merged space) and cuts the block that
// contains it. Blocks after the cut are dropped: those are the parent's later
// events, and they must not appear in the branch.
func truncateLineage(blocks []lineageBlock, at int64) []lineageBlock {
	if at <= 0 {
		return nil
	}
	out := make([]lineageBlock, 0, len(blocks))
	for _, block := range blocks {
		end := int64(math.MaxInt64)
		if block.cutoff > 0 {
			end = block.base + block.cutoff
		}
		if at >= end {
			out = append(out, block)
			if at == end {
				return out
			}
			continue
		}
		if at <= block.base {
			return out
		}
		block.cutoff = at - block.base
		out = append(out, block)
		return out
	}
	return out
}

// ListMergedEvents is the transcript read for one session: the branch's own
// events, preceded by the parent's events it inherited at fork time.
//
// It has the same "seq > afterSeq, ascending, at most limit" contract as
// ListEvents, so every paging reader (SSE replay, export, PR lookups) can use it
// unchanged; only the sequence numbers are transcript-wide instead of
// per-session. A non-fork session takes the single-log path.
func (s *Store) ListMergedEvents(ctx context.Context, userID, sessionID string, afterSeq int64, limit int) ([]*contracts.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	sess, err := s.Session(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	blocks, err := s.sessionLineage(ctx, userID, sess)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 1 {
		return s.ListEvents(ctx, userID, sessionID, afterSeq, limit)
	}

	out := make([]*contracts.Event, 0, min(limit, 256))
	for _, block := range blocks {
		if block.sessionID == "" {
			continue
		}
		if block.cutoff > 0 && afterSeq >= block.base+block.cutoff {
			continue
		}
		remaining := limit - len(out)
		if remaining <= 0 {
			break
		}
		local := afterSeq - block.base
		if local < 0 {
			local = 0
		}
		batch, err := s.ListEvents(ctx, userID, block.sessionID, local, remaining)
		if err != nil {
			return nil, err
		}
		for _, ev := range batch {
			if block.cutoff > 0 && ev.Seq > block.cutoff {
				break
			}
			// Mutating the scanned event is safe: ListEvents built it for this
			// call and nothing else holds a reference.
			ev.Seq += block.base
			out = append(out, ev)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// MergedSeqRange reports the first and last transcript seq a client can resume
// from. The SSE gap check needs it: a cursor older than the retained head must
// answer 409 rather than silently skipping history (PRD §7.4.5).
func (s *Store) MergedSeqRange(ctx context.Context, userID, sessionID string) (minSeq, maxSeq int64, err error) {
	sess, err := s.Session(ctx, userID, sessionID)
	if err != nil {
		return 0, 0, err
	}
	blocks, err := s.sessionLineage(ctx, userID, sess)
	if err != nil {
		return 0, 0, err
	}
	if len(blocks) == 1 {
		return s.EventSeqRange(ctx, userID, sessionID)
	}
	for _, block := range blocks {
		if block.sessionID == "" {
			continue
		}
		lo, hi, err := s.EventSeqRange(ctx, userID, block.sessionID)
		if err != nil {
			return 0, 0, err
		}
		if hi == 0 {
			continue
		}
		if block.cutoff > 0 {
			if lo > block.cutoff {
				continue
			}
			if hi > block.cutoff {
				hi = block.cutoff
			}
		}
		if minSeq == 0 {
			minSeq = block.base + lo
		}
		maxSeq = block.base + hi
	}
	return minSeq, maxSeq, nil
}

// lastMergedPayload returns the payload of the newest event of one type in the
// session's transcript, looking at its own log first and then at each inherited
// slice. It is what lets a fresh fork show the context usage it inherited
// instead of a zeroed meter.
func (s *Store) lastMergedPayload(ctx context.Context, userID, sessionID, evType string) (string, bool, error) {
	sess, err := s.Session(ctx, userID, sessionID)
	if err != nil {
		return "", false, err
	}
	blocks, err := s.sessionLineage(ctx, userID, sess)
	if err != nil {
		return "", false, err
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.sessionID == "" {
			continue
		}
		q := `SELECT payload FROM events WHERE session_id = ? AND type = ? AND payload IS NOT NULL`
		args := []any{block.sessionID, evType}
		if block.cutoff > 0 {
			q += ` AND seq <= ?`
			args = append(args, block.cutoff)
		}
		q += ` ORDER BY seq DESC LIMIT 1`
		var payload sql.NullString
		err := s.r.QueryRowContext(ctx, q, args...).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) || !payload.Valid {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("store: last %s event for %q: %w", evType, sessionID, err)
		}
		return payload.String, true, nil
	}
	return "", false, nil
}
