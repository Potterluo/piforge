package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// secretBox encrypts the small secrets this deployment stores (PRD FR-15.2).
//
// The key comes from PIFORGE_SECRET_KEY when the operator set one, and otherwise from
// a 0600 file next to the database, created on first use. The file exists so a
// self-hosted install works without ceremony; the env var exists so a deployment that
// already has a secret manager does not need one more file to back up. Losing the key
// means losing the stored secrets — which is the honest consequence of encrypting
// them, and the reason `has_secrets` says whether anything is at stake.
type secretBox struct {
	aead cipher.AEAD
}

var (
	boxOnce sync.Once
	boxVal  *secretBox
	boxErr  error
)

// secretBoxFor lazily derives the deployment's box. It is process-wide because the key
// is: a per-call derivation would mean the salt lives in somebody's memory.
func secretBoxFor(dataDir string) (*secretBox, error) {
	boxOnce.Do(func() {
		boxVal, boxErr = newSecretBox(dataDir)
	})
	return boxVal, boxErr
}

func newSecretBox(dataDir string) (*secretBox, error) {
	raw := strings.TrimSpace(os.Getenv("PIFORGE_SECRET_KEY"))
	var key []byte
	if raw != "" {
		// Accept hex or base64, because an operator pasting a key should not have to
		// care which one their tool of choice produced.
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
			key = decoded
		} else if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
			key = decoded
		} else {
			return nil, errors.New("store: PIFORGE_SECRET_KEY must be 32 bytes as hex or base64")
		}
	} else {
		path := filepath.Join(dataDir, "secret.key")
		if data, err := os.ReadFile(path); err == nil {
			key = data
		} else {
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return nil, fmt.Errorf("store: generate a secret key: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return nil, fmt.Errorf("store: prepare the key dir: %w", err)
			}
			if err := os.WriteFile(path, key, 0o600); err != nil {
				return nil, fmt.Errorf("store: write %s: %w", path, err)
			}
		}
	}
	// Hash to a fixed size, so a key of the wrong length cannot silently weaken AES.
	sum := sha256.Sum256(key)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("store: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("store: gcm: %w", err)
	}
	return &secretBox{aead: aead}, nil
}

// seal encrypts one secret blob. The output is nonce||ciphertext.
func (b *secretBox) seal(plain string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, b.aead.Seal(nil, nonce, []byte(plain), nil)...), nil
}

// open decrypts what seal produced.
func (b *secretBox) open(blob []byte) (string, error) {
	if len(blob) < b.aead.NonceSize() {
		return "", errors.New("store: secret blob is too short")
	}
	nonce, ciphertext := blob[:b.aead.NonceSize()], blob[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// setMCPServerSecrets stores an encrypted blob for one server.
func (s *Store) setMCPServerSecrets(ctx context.Context, userID, serverID, plain string) error {
	if strings.TrimSpace(plain) == "" {
		return nil
	}
	box, err := secretBoxFor(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	blob, err := box.seal(plain)
	if err != nil {
		return fmt.Errorf("store: encrypt mcp secrets: %w", err)
	}
	_, err = s.w.ExecContext(ctx,
		`UPDATE mcp_servers SET secrets_cipher = ? WHERE id = ? AND owner_id = ?`,
		blob, serverID, userID)
	if err != nil {
		return fmt.Errorf("store: save mcp secrets: %w", err)
	}
	return nil
}

// MCPServerSecrets decrypts one server's blob for the launcher that has to hand it to
// the process. It is deliberately not part of the API surface: nothing HTTP-facing can
// read a credential back (FR-15.2).
func (s *Store) MCPServerSecrets(ctx context.Context, userID, serverID string) (string, error) {
	var blob []byte
	err := s.r.QueryRowContext(ctx,
		`SELECT secrets_cipher FROM mcp_servers WHERE id = ? AND owner_id = ?`, serverID, userID).Scan(&blob)
	if err != nil {
		return "", err
	}
	if len(blob) == 0 {
		return "", nil
	}
	box, err := secretBoxFor(filepath.Dir(s.path))
	if err != nil {
		return "", err
	}
	return box.open(blob)
}
