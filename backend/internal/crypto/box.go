// Package crypto holds the one piece of symmetric cryptography PiForge needs:
// sealing provider API keys at rest and opening them at the egress point.
//
// The threat model is narrow and explicit. Keys must not be readable from a
// database dump, a backup, or a log line, and destroying the master key must
// make every stored key unrecoverable (that is the crypto-shred guarantee).
// They are *not* protected against an attacker who already has code execution
// in the control plane — at that point the gateway can decrypt anything it can
// serve, and no amount of local encryption changes that.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeySize is the AES-256 key length.
const KeySize = 32

// ErrNoKey means the box was built without usable key material.
var ErrNoKey = errors.New("crypto: no master key configured")

// Box seals and opens small secrets with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// New builds a box from raw key bytes.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: master key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// NewFromEnv loads PIFORGE_MASTER_KEY (base64, hex, or raw 32 bytes).
//
// When the variable is absent it falls back to a key file inside dataDir,
// generating one on first boot. That keeps a developer's local database
// encrypted without asking them to manage a secret, while a real deployment
// sets the variable and never relies on the file.
func NewFromEnv(dataDir string) (*Box, error) {
	if raw := strings.TrimSpace(os.Getenv("PIFORGE_MASTER_KEY")); raw != "" {
		key, err := decodeKey(raw)
		if err != nil {
			return nil, err
		}
		return New(key)
	}
	if dataDir == "" {
		return nil, ErrNoKey
	}

	path := filepath.Join(dataDir, "master.key")
	if data, err := os.ReadFile(path); err == nil {
		return New(data)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("crypto: read %s: %w", path, err)
	}

	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("crypto: generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("crypto: create key dir: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("crypto: write %s: %w", path, err)
	}
	return New(key)
}

func decodeKey(raw string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == KeySize {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(raw); err == nil && len(decoded) == KeySize {
		return decoded, nil
	}
	if len(raw) == KeySize {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("crypto: PIFORGE_MASTER_KEY must decode to %d bytes", KeySize)
}

// Seal encrypts plaintext and returns nonce||ciphertext.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open decrypts what Seal produced.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, ErrNoKey
	}
	size := b.aead.NonceSize()
	if len(sealed) <= size {
		return nil, errors.New("crypto: ciphertext is too short")
	}
	plain, err := b.aead.Open(nil, sealed[:size], sealed[size:], nil)
	if err != nil {
		// A wrong master key and a tampered ciphertext are indistinguishable,
		// and saying which would leak information.
		return nil, errors.New("crypto: cannot decrypt (wrong master key or corrupted data)")
	}
	return plain, nil
}

// SealString is Seal for text secrets.
func (b *Box) SealString(plain string) ([]byte, error) { return b.Seal([]byte(plain)) }

// OpenString is Open for text secrets.
func (b *Box) OpenString(sealed []byte) (string, error) {
	plain, err := b.Open(sealed)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Mask renders a secret for display: the last four characters, which is enough
// for a human to recognise a key and useless to anyone else.
func Mask(secret string) string {
	if len(secret) <= 4 {
		return "••••"
	}
	return "••••" + secret[len(secret)-4:]
}
