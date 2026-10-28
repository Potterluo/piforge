package store

import (
	"context"
	"fmt"
)

// DeploymentStats is the little that an operator needs to answer "is anything
// stuck?" (PRD FR-16.5). It is deliberately a handful of counts and not a metrics
// pipeline: this is a self-hosted tool, not a fleet.
type DeploymentStats struct {
	Users        int `json:"users"`
	UsersActive  int `json:"users_active"`
	Projects     int `json:"projects"`
	Sessions     int `json:"sessions"`
	SessionsLive int `json:"sessions_live"`
	Sandboxes    int `json:"sandboxes"`
	SandboxesUp  int `json:"sandboxes_up"`
	Providers    int `json:"providers"`
	Skills       int `json:"skills"`
	MCP          int `json:"mcp_servers"`
	Events       int `json:"events"`
}

// Stats counts the rows behind the admin status page.
//
// Every count is a single query rather than one per row: the page is opened when
// something feels wrong, which is the worst moment to add load.
func (s *Store) Stats(ctx context.Context) (*DeploymentStats, error) {
	stats := &DeploymentStats{}
	queries := []struct {
		sql  string
		into *int
	}{
		{`SELECT COUNT(*) FROM users`, &stats.Users},
		{`SELECT COUNT(*) FROM users WHERE status = 'active'`, &stats.UsersActive},
		{`SELECT COUNT(*) FROM projects`, &stats.Projects},
		{`SELECT COUNT(*) FROM sessions`, &stats.Sessions},
		{`SELECT COUNT(*) FROM sessions WHERE status IN ('active','running','idle')`, &stats.SessionsLive},
		{`SELECT COUNT(*) FROM sandboxes`, &stats.Sandboxes},
		{`SELECT COUNT(*) FROM sandboxes WHERE state IN ('READY','BUSY')`, &stats.SandboxesUp},
		{`SELECT COUNT(*) FROM providers`, &stats.Providers},
		{`SELECT COUNT(*) FROM skills`, &stats.Skills},
		{`SELECT COUNT(*) FROM mcp_servers`, &stats.MCP},
		{`SELECT COUNT(*) FROM events`, &stats.Events},
	}
	for _, query := range queries {
		if err := s.r.QueryRowContext(ctx, query.sql).Scan(query.into); err != nil {
			return nil, fmt.Errorf("store: stats: %w", err)
		}
	}
	return stats, nil
}
