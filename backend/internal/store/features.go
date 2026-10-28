package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// FeatureFlag is one per-user switch (PRD FR-16.7).
//
// The point is not novelty: it is that a deployment sometimes needs to say "this
// tenant may not download transcripts" or "the sandbox browser is off for this
// team", and the server has to enforce it rather than hide a button.
type FeatureFlag string

// The flags this build knows about. A flag that only exists in the database is a
// typo waiting to happen, so the set is closed and validated.
const (
	FeatureExport  FeatureFlag = "export"
	FeatureBrowser FeatureFlag = "browser"
	FeatureSidecar FeatureFlag = "sidecar"
	// FeatureParallel lets branches of one session run at the same time (FR-3.2).
	// Off by default: two agents in two checkouts is a thing to choose, not inherit.
	FeatureParallel FeatureFlag = "parallel"
)

// FeatureFlags is the closed set with its defaults. Everything is on unless an
// admin turns it off: a self-hosted tool that ships with features disabled is a
// tool whose documentation is wrong.
var featureDefaults = map[FeatureFlag]bool{
	FeatureExport:  true,
	FeatureBrowser: true,
	FeatureSidecar: true,
	// Parallel branch execution is opt-in (FR-3.2/3.3): the safe default is one run
	// at a time, and an admin turning it on is a deliberate act.
	FeatureParallel: false,
}

// KnownFeature reports whether a flag exists in this build.
func KnownFeature(flag string) bool {
	_, ok := featureDefaults[FeatureFlag(flag)]
	return ok
}

// Features returns the effective flags for one user: defaults, with any row the
// admin has written on top.
func (s *Store) Features(ctx context.Context, userID string) (map[string]bool, error) {
	out := make(map[string]bool, len(featureDefaults))
	for flag, on := range featureDefaults {
		out[string(flag)] = on
	}

	rows, err := s.r.QueryContext(ctx,
		`SELECT flag, enabled FROM feature_flags WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: features: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var flag string
		var enabled bool
		if err := rows.Scan(&flag, &enabled); err != nil {
			return nil, fmt.Errorf("store: scan feature: %w", err)
		}
		if KnownFeature(flag) {
			out[flag] = enabled
		}
	}
	return out, rows.Err()
}

// FeatureEnabled answers one flag. Callers that gate a request should use this
// rather than reading the map, so the default lives in one place.
func (s *Store) FeatureEnabled(ctx context.Context, userID, flag string) (bool, error) {
	if !KnownFeature(flag) {
		return false, fmt.Errorf("store: unknown feature %q: %w", flag, ErrInvalid)
	}
	var enabled bool
	err := s.r.QueryRowContext(ctx,
		`SELECT enabled FROM feature_flags WHERE user_id = ? AND flag = ?`, userID, flag).Scan(&enabled)
	if err == nil {
		return enabled, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return featureDefaults[FeatureFlag(flag)], nil
	}
	return false, fmt.Errorf("store: feature %q: %w", flag, err)
}

// SetFeature writes one flag for one user.
func (s *Store) SetFeature(ctx context.Context, callerID, userID, flag string, enabled bool) error {
	if err := s.requireAdmin(ctx, callerID); err != nil {
		return err
	}
	if !KnownFeature(flag) {
		return fmt.Errorf("store: unknown feature %q: %w", flag, ErrInvalid)
	}
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO feature_flags (user_id, flag, enabled, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, flag) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		userID, flag, enabled, formatTime(s.now())); err != nil {
		return fmt.Errorf("store: set feature %q: %w", flag, err)
	}
	return nil
}
