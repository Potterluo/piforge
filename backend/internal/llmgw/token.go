package llmgw

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// DefaultTTL is how long a sandbox token lives.
//
// It used to be 12 hours, and an adversarial acceptance run showed what that
// meant: a token read out of `/workspace/.piforge/agents/<session>/models.json`
// stayed a working credential on the operator's provider account for half a
// day, from any machine, after the sandbox it belonged to was gone.
//
// One hour is the replacement, and the two things that make it enough are:
//
//   - the token is minted at every agent spawn (a prompt, a model switch, a
//     restart), so an hour is a *ceiling on the leak*, not a session length;
//   - Revoke ends a token the moment its session's agent stops, which is the
//     normal end of a token's life. The TTL is the backstop for the paths the
//     control plane cannot see (a crash between the stop and the revoke, or a
//     restart that lost the in-memory revocation set).
//
// The cost is honest and bounded: a single agent process that runs for more
// than an hour loses model access at the ceiling and gets a fresh token on its
// next prompt. `-gateway-token-ttl` exists for a deployment that knows its
// turns are longer; the default is the one a shared deployment should run.
const DefaultTTL = time.Hour

// Signer issues and verifies tokens with HMAC-SHA256.
//
// It also holds the revocation set: the gateway asks Verify on every model call
// and a session whose agent stopped must not be able to spend its token for
// another hour. The set is in memory on purpose — it describes live processes,
// which do not survive a restart either — and its entries are pruned once the
// token they revoke would have expired anyway.
type Signer struct {
	key []byte
	ttl time.Duration

	mu      sync.RWMutex
	revoked map[string]time.Time
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
	return &Signer{key: key, ttl: ttl, revoked: map[string]time.Time{}}, nil
}

// TTL reports the lifetime this signer gives a token. The boot log prints it so
// "how long is a leaked token worth" is answerable without reading the code.
func (s *Signer) TTL() time.Duration { return s.ttl }

// Revoke makes every token issued for sessionID invalid from now on, and prunes
// revocations that are older than the TTL (their tokens have expired).
//
// It is called when the session's agent stops: pause (manual or idle), archive,
// delete, a model switch, and the project delete that stops every agent it
// owns. Idempotent, and safe to call for a session that never had a token.
func (s *Signer) Revoke(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked == nil {
		s.revoked = map[string]time.Time{}
	}
	s.revoked[sessionID] = now
	for id, at := range s.revoked {
		if now.Sub(at) > s.ttl {
			delete(s.revoked, id)
		}
	}
}

// Revoked reports whether sessionID's tokens were revoked and are still within
// the window where one could otherwise verify.
func (s *Signer) Revoked(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	at, ok := s.revoked[sessionID]
	if !ok {
		return false
	}
	return time.Since(at) <= s.ttl
}

// RevokedCount is the size of the revocation set. Tests and the admin log use it
// to tell "nothing was revoked" from "revocation is not wired".
func (s *Signer) RevokedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.revoked)
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
	// The session-lifetime bound: a token outlives its signature check only as
	// long as the agent it was minted for runs. This is what makes "pause the
	// sandbox" also stop the spend, instead of leaving a working bearer
	// credential behind for the rest of the TTL.
	if s.Revoked(claims.SessionID) {
		return Claims{}, errors.New("token revoked: the session's agent has stopped")
	}
	return claims, nil
}

func (s *Signer) sign(body string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
