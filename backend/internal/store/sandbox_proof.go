package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Per-sandbox proof secrets: the second half of "this token only works from its
// own sandbox" (docs/EXECUTION-BOUNDARY.md §16.3).
//
// The address half needs nothing stored: a container's address is a fact the
// network already knows, and the control plane reads it from Docker's DNS. The
// proof half needs one durable per-sandbox value, because the gateway has to
// recognise a proof an agent computed at spawn time — possibly before a
// control-plane restart, or across a pause and resume.
//
// Two decisions are worth stating:
//
//   - The secret is *sealed* in the sandboxes row, like the per-sandbox API key,
//     and it never enters the container. The only thing that crosses into the
//     sandbox is HMAC(secret, token), which is worth exactly one token — and even
//     that lives in the agent's environment, not in models.json (pimodel writes a
//     "$PIFORGE_SANDBOX_PROOF" reference).
//   - Minting is lazy and idempotent (EnsureSandboxProofSecret): the row is
//     created before any agent exists, and a sandbox adopted from an older build
//     has no secret at all. Minting on first use means the first prompt after an
//     upgrade gets one, and a second call gets the same one.

// SandboxProofSecret returns the sandbox's proof secret, decrypted, or "" when the
// row predates per-sandbox proofs. It is deliberately not part of
// contracts.Sandbox: the only callers are the gateway (to verify a proof) and the
// launcher (to compute one).
func (s *Store) SandboxProofSecret(ctx context.Context, sandboxID string) (string, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return "", fmt.Errorf("store: sandbox proof secret: an id is required: %w", ErrInvalid)
	}
	var blob []byte
	err := s.r.QueryRowContext(ctx,
		`SELECT proof_cipher FROM sandboxes WHERE id = ?`, sandboxID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: sandbox %q: %w", sandboxID, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: sandbox %q: read proof secret: %w", sandboxID, err)
	}
	return s.openSandboxProof(blob)
}

// EnsureSandboxProofSecret returns the sandbox's proof secret, minting one if the
// row has none yet.
//
// It is what the agent launcher calls when it mints a model gateway token, so
// "minted at container creation" and "minted at the first agent spawn in it" are
// the same moment in practice: a sandbox with no agent has no token to prove
// anything about.
func (s *Store) EnsureSandboxProofSecret(ctx context.Context, sandboxID string) (string, error) {
	existing, err := s.SandboxProofSecret(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(existing) != "" {
		return existing, nil
	}
	fresh, err := newProofSecret()
	if err != nil {
		return "", err
	}
	blob, err := s.sealSandboxProof(fresh)
	if err != nil {
		return "", err
	}
	// Only fill an empty column: if a concurrent call won the race, its value is
	// the one the row keeps and the re-read below returns it, so two callers can
	// never disagree about the sandbox's secret.
	if _, err := s.w.ExecContext(ctx,
		`UPDATE sandboxes SET proof_cipher = ?
		  WHERE id = ? AND (proof_cipher IS NULL OR length(proof_cipher) = 0)`,
		blob, sandboxID); err != nil {
		return "", fmt.Errorf("store: sandbox %q: save proof secret: %w", sandboxID, err)
	}
	stored, err := s.SandboxProofSecret(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(stored) == "" {
		return "", fmt.Errorf("store: sandbox %q: the proof secret was written but reads back empty", sandboxID)
	}
	return stored, nil
}

// newProofSecret returns 32 bytes from crypto/rand, hex encoded. crypto/rand
// because the value is a secret the gateway trusts.
func newProofSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: generate a sandbox proof secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) sealSandboxProof(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, nil
	}
	box, err := secretBoxFor(filepath.Dir(s.path))
	if err != nil {
		return nil, err
	}
	blob, err := box.seal(secret)
	if err != nil {
		return nil, fmt.Errorf("store: seal sandbox proof secret: %w", err)
	}
	return blob, nil
}

func (s *Store) openSandboxProof(blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	box, err := secretBoxFor(filepath.Dir(s.path))
	if err != nil {
		return "", err
	}
	secret, err := box.open(blob)
	if err != nil {
		return "", fmt.Errorf("store: open sandbox proof secret: %w", err)
	}
	return secret, nil
}
