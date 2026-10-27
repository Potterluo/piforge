package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// stateRecord is the durable state of one sandbox. It lives outside the
// sandbox directory so it is never checked into the user's repo and never shows
// up in the file tree.
type stateRecord struct {
	ID           string                 `json:"id"`
	UserID       string                 `json:"user_id"`
	ProjectID    string                 `json:"project_id"`
	State        contracts.SandboxState `json:"state"`
	RepoURL      string                 `json:"repo_url,omitempty"`
	Branch       string                 `json:"branch,omitempty"`
	Image        string                 `json:"image,omitempty"`
	Node         string                 `json:"node,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	LastActiveAt time.Time              `json:"last_active_at"`
	ExpiresAt    *time.Time             `json:"expires_at,omitempty"`
	Message      string                 `json:"message,omitempty"`
	Snapshot     string                 `json:"snapshot,omitempty"`
}

func (r *stateRecord) toContract() *contracts.Sandbox {
	return &contracts.Sandbox{
		ID:           r.ID,
		UserID:       r.UserID,
		ProjectID:    r.ProjectID,
		State:        r.State,
		Provider:     ProviderName,
		Node:         r.Node,
		Image:        r.Image,
		CreatedAt:    r.CreatedAt,
		LastActiveAt: r.LastActiveAt,
		ExpiresAt:    r.ExpiresAt,
		Message:      r.Message,
	}
}

// statePath returns <root>/_state/<user>/<project>.json.
func (l *Local) statePath(userID, projectID string) string {
	return filepath.Join(l.cfg.Root, stateDirName, userID, projectID+".json")
}

// writeState persists rec atomically (temp file + rename inside the same dir).
func (l *Local) writeState(rec *stateRecord) error {
	if rec == nil || rec.ID == "" {
		return fmt.Errorf("sandbox: write state: empty record")
	}
	p := l.statePath(rec.UserID, rec.ProjectID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("sandbox: create state dir: %w", err)
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("sandbox: marshal state: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("sandbox: temp state file: %w", err)
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(body); werr != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sandbox: write state: %w", werr)
	}
	if serr := tmp.Sync(); serr != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sandbox: sync state: %w", serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		os.Remove(tmpName)
		return fmt.Errorf("sandbox: close state: %w", cerr)
	}
	if rerr := os.Rename(tmpName, p); rerr != nil {
		os.Remove(tmpName)
		return fmt.Errorf("sandbox: commit state: %w", rerr)
	}
	return nil
}

// readState loads <root>/_state/<user>/<project>.json.
func (l *Local) readState(userID, projectID string) (*stateRecord, error) {
	p := l.statePath(userID, projectID)
	body, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sandbox: read state: %w", err)
	}
	var rec stateRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("sandbox: parse state %s: %w", p, err)
	}
	return &rec, nil
}

// findState scans the state directory for a sandbox id.
func (l *Local) findState(id string) (*stateRecord, error) {
	stateRoot := filepath.Join(l.cfg.Root, stateDirName)
	users, err := os.ReadDir(stateRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("sandbox: read state dir: %w", err)
	}
	for _, u := range users {
		if !u.IsDir() {
			continue
		}
		entries, derr := os.ReadDir(filepath.Join(stateRoot, u.Name()))
		if derr != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			p := filepath.Join(stateRoot, u.Name(), e.Name())
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				continue
			}
			var rec stateRecord
			if jerr := json.Unmarshal(body, &rec); jerr != nil {
				continue
			}
			if rec.ID == id {
				return &rec, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// removeStateFor deletes the state file of a sandbox id (best effort on the
// parent directory).
func (l *Local) removeStateFor(id string) error {
	rec, err := l.findState(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	p := l.statePath(rec.UserID, rec.ProjectID)
	if rerr := os.Remove(p); rerr != nil && !os.IsNotExist(rerr) {
		return fmt.Errorf("sandbox: remove state: %w", rerr)
	}
	_ = os.Remove(filepath.Dir(p))
	return nil
}
