package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// NewSessionSkill is one skill version a session materialised. The provisioner
// owns the filesystem walk and the digest; the store only records the fact.
type NewSessionSkill struct {
	SkillID     string
	Name        string
	Version     string
	ContentHash string
	Description string
	MountedPath string
}

// RecordSessionSkills records what a session was mounted with (FR-14.5).
//
// The write is *write-once per (session, skill)*: `ON CONFLICT DO NOTHING` keeps
// the first record even if the same session is provisioned again after its skill
// was edited. That is the whole point of the feature — the session reports what
// it actually used, and a skill edit changes the `skills` row, not history. It
// returns how many new rows were written, which is 0 on a re-mount.
func (s *Store) RecordSessionSkills(
	ctx context.Context,
	userID, sessionID string,
	records []NewSessionSkill,
) (int, error) {
	// The session check is the tenant boundary: a session id from another user
	// must not gain rows, and must read as "not found" rather than "forbidden".
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}

	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: record session skills: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	written := 0
	now := formatTime(s.now())
	for _, record := range records {
		if strings.TrimSpace(record.SkillID) == "" || strings.TrimSpace(record.Name) == "" {
			continue
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO session_skills
			   (session_id, skill_id, name, version, content_hash, description, mounted_path, recorded_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (session_id, skill_id) DO NOTHING`,
			sessionID, record.SkillID, record.Name, record.Version, record.ContentHash,
			record.Description, record.MountedPath, now)
		if err != nil {
			return 0, fmt.Errorf("store: record session skill %q: %w", record.Name, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: record session skill %q: %w", record.Name, err)
		}
		written += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: record session skills: %w", err)
	}
	return written, nil
}

// SessionSkills lists the skill versions a session materialised, oldest name
// first. An empty slice is a valid answer: a session that never mounted a skill
// has no record, and the caller should say so rather than join `skills`.
func (s *Store) SessionSkills(ctx context.Context, userID, sessionID string) ([]*contracts.SessionSkill, error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT session_id, skill_id, name, version, content_hash, description, mounted_path, recorded_at
		   FROM session_skills
		  WHERE session_id = ?
		  ORDER BY name ASC, recorded_at ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: session skills: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.SessionSkill, 0, 8)
	for rows.Next() {
		var (
			recorded   contracts.SessionSkill
			recordedAt string
		)
		if err := rows.Scan(&recorded.SessionID, &recorded.SkillID, &recorded.Name,
			&recorded.Version, &recorded.ContentHash, &recorded.Description,
			&recorded.MountedPath, &recordedAt); err != nil {
			return nil, fmt.Errorf("store: session skills: %w", err)
		}
		recorded.RecordedAt = parseTime(recordedAt)
		out = append(out, &recorded)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: session skills: %w", err)
	}
	return out, nil
}

// CopySessionSkills gives a sidecar its parent's records.
//
// A sidecar deliberately shares its parent's checkout, so the skills it can see
// are the ones the parent mounted. Copying the rows (rather than reading the
// parent's at request time) keeps every session's report a fact of its own.
func (s *Store) CopySessionSkills(ctx context.Context, userID, parentID, sessionID string) (int, error) {
	if parentID == "" || sessionID == "" || parentID == sessionID {
		return 0, nil
	}
	if _, err := s.Session(ctx, userID, parentID); err != nil {
		return 0, err
	}
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return 0, err
	}
	res, err := s.w.ExecContext(ctx,
		`INSERT INTO session_skills
		   (session_id, skill_id, name, version, content_hash, description, mounted_path, recorded_at)
		 SELECT ?, skill_id, name, version, content_hash, description, mounted_path, recorded_at
		   FROM session_skills
		  WHERE session_id = ?
		 ON CONFLICT (session_id, skill_id) DO NOTHING`,
		sessionID, parentID)
	if err != nil {
		return 0, fmt.Errorf("store: copy session skills: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: copy session skills: %w", err)
	}
	return int(affected), nil
}
