package llmgw

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims is what a sandbox token carries.
//
// It is deliberately small and stateless: the gateway must be able to validate
// a request without a database round trip on every model call, and the only
// thing it actually needs is which user's provider to use.
type Claims struct {
	SessionID string `json:"s"`
	UserID    string `json:"u"`
	Provider  string `json:"p"`
	// Model is advisory: it is set when the session pinned one, so a proxy log
	// can name it even though the request body carries the real model id.
	Model     string `json:"m,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// DefaultTTL is how long a sandbox token lives. It only has to outlive one
// agent process; reissuing is free because the token is generated at spawn.
const DefaultTTL = 12 * time.Hour

// Signer issues and verifies tokens with HMAC-SHA256.
type Signer struct {
	key []byte
	ttl time.Duration
}

// NewSigner builds a signer. The key should be random and process-local unless
// several replicas must verify each other's tokens.
func NewSigner(key []byte, ttl time.Duration) (*Signer, error) {
	if len(key) < 16 {
		return nil, errors.New("llmgw: signing key must be at least 16 bytes")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Signer{key: key, ttl: ttl}, nil
}

// Issue mints a token for a session.
func (s *Signer) Issue(sessionID, userID, provider, model string, now time.Time) (string, error) {
	if sessionID == "" || userID == "" || provider == "" {
		return "", errors.New("llmgw: session, user and provider are required to issue a token")
	}
	claims := Claims{
		SessionID: sessionID,
		UserID:    userID,
		Provider:  provider,
		Model:     model,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(s.ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("llmgw: encode claims: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body), nil
}

// Verify checks the signature and the expiry.
func (s *Signer) Verify(token string, now time.Time) (Claims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Claims{}, errors.New("missing token")
	}
	body, signature, ok := strings.Cut(token, ".")
	if !ok || body == "" || signature == "" {
		return Claims{}, errors.New("malformed token")
	}
	if !hmac.Equal([]byte(signature), []byte(s.sign(body))) {
		return Claims{}, errors.New("bad signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, errors.New("malformed token payload")
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, errors.New("malformed token claims")
	}
	if claims.ExpiresAt > 0 && now.Unix() >= claims.ExpiresAt {
		return Claims{}, errors.New("token expired")
	}
	if claims.SessionID == "" || claims.UserID == "" || claims.Provider == "" {
		return Claims{}, errors.New("token is missing claims")
	}
	return claims, nil
}

func (s *Signer) sign(body string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
