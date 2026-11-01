package llmgw

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Claims is what a sandbox token carries.
//
// It is deliberately small: the gateway validates the signature without a
// database round trip, and the only thing it needs from the claim is which
// user's provider to use. The one database read it does make is the revocation
// generation, which is the price of a revocation that survives a restart (see
// RevocationStore).
type Claims struct {
	SessionID string `json:"s"`
	UserID    string `json:"u"`
	Provider  string `json:"p"`
	// Model is advisory: it is set when the session pinned one, so a proxy log
	// can name it even though the request body carries the real model id.
	Model string `json:"m,omitempty"`
	// Sandbox is the sandbox this token may be spent from: the container's name
	// for a container placement (which is also the sandbox row's id), empty for a
	// placement with no container. It is what makes the token a *bound* credential
	// rather than a portable one — Verify proves the signature, and the gateway
	// then requires the request to arrive from that sandbox's address, with a
	// proof the sandbox holds (see binding.go).
	//
	// It is in the clear because it is not a secret: a container name is derivable
	// from the (user, project) pair by anyone who can see the deployment, and the
	// check it enables is "did this request come from there", which no amount of
	// knowing the name can fake.
	Sandbox   string `json:"b,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	// Generation is the session's revocation generation at the moment the token
	// was minted. Revoke bumps the session's generation, so every token issued
	// before the stop stops verifying — and the token minted for the *next* spawn
	// of the same session (after a pause and resume) carries the new number and
	// works immediately.
	//
	// It is a counter rather than a timestamp because the two events that matter
	// here (a stop and the resume's spawn) can happen inside one second, and a
	// timestamp compared against `iat` cannot tell them apart: either the new
	// token is rejected for a second, or a just-revoked one is accepted for a
	// second. A generation is exact.
	Generation int64 `json:"g,omitempty"`
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

// RevocationStore is the durable half of revocation (store/gateway_revocations.go).
//
// It is an interface here rather than a concrete store so this package keeps its
// single dependency (the claims) and so a signer can be built without a database
// in a unit test. A nil store means "memory only", which is what a test and a
// bare `llmgw.NewSigner` get; every deployment wires the real one.
//
// The store is authoritative: Verify reads it on every model call, and the
// in-memory map is only a fast path for the value just written or read.
type RevocationStore interface {
	// RevokeGatewayToken bumps a session's generation and returns it.
	RevokeGatewayToken(ctx context.Context, sessionID string, expiresAt time.Time) (int64, error)
	// GatewayRevocationGeneration returns the number tokens must carry (0 when
	// nothing was ever revoked).
	GatewayRevocationGeneration(ctx context.Context, sessionID string) (int64, error)
	// ExtendGatewayRevocation keeps a revocation row alive while a token minted
	// after it can still be presented.
	ExtendGatewayRevocation(ctx context.Context, sessionID string, expiresAt time.Time) error
	// SweepGatewayRevocations deletes rows past their expiry.
	SweepGatewayRevocations(ctx context.Context, now time.Time) (int64, error)
}

// Signer issues and verifies tokens with HMAC-SHA256.
//
// It also holds the revocation state: the gateway asks Verify on every model call
// and a session whose agent stopped must not be able to spend its token for
// another hour. Each session has a generation counter — Revoke increments it and
// every issue stamps the current value into the claims — so "tokens from before
// the stop" and "tokens for the next spawn" are distinguished exactly, even when
// a pause and the following resume happen inside one second.
//
// The counter is persisted (RevocationStore). It used to be memory-only, and the
// second adversarial acceptance run showed what that cost: a control-plane
// restart inside the token's lifetime forgot every revocation and re-enabled
// every token minted before it. The map below is a fast path and a fallback for
// a failed durable read; it is not the authority.
type Signer struct {
	key   []byte
	ttl   time.Duration
	store RevocationStore
	// log is where a failed durable write is reported. It never fails the call:
	// a revocation that could not be persisted is still enforced in memory, and
	// saying so is better than pretending it was durable.
	log func(format string, args ...any)

	mu         sync.RWMutex
	generation map[string]int64
	// storeErrors counts durable reads that failed, so "the fast path is being
	// used" is a number a test can assert on instead of a log line nobody reads.
	storeErrors atomic.Int64
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
	return &Signer{
		key:        key,
		ttl:        ttl,
		log:        func(string, ...any) {},
		generation: map[string]int64{},
	}, nil
}

// UseRevocations makes the durable store authoritative. It is called once at
// boot, before the gateway serves.
func (s *Signer) UseRevocations(store RevocationStore) { s.store = store }

// SetLogger routes the "the durable write failed" warnings somewhere visible.
func (s *Signer) SetLogger(log func(format string, args ...any)) {
	if log != nil {
		s.log = log
	}
}

// StoreErrors is how many durable reads fell back to the in-memory set.
func (s *Signer) StoreErrors() int64 { return s.storeErrors.Load() }

// TTL reports the lifetime this signer gives a token. The boot log prints it so
// "how long is a leaked token worth" is answerable without reading the code.
func (s *Signer) TTL() time.Duration { return s.ttl }

// revokeTimeout bounds the durable half of a revocation. The stop path is
// synchronous on purpose (a pause that returned 200 must mean the row is on
// disk), but it must not be able to hang the caller forever on a locked database.
const revokeTimeout = 5 * time.Second

// Revoke invalidates every token issued for sessionID so far.
//
// It is called when the session's agent stops: pause (manual or idle), archive,
// delete, a model switch, and the project delete that stops every agent it owns.
// The next spawn of the same session gets the next generation and works; only the
// tokens that were already in the sandbox die.
//
// The in-memory generation is bumped first and the durable row is written in the
// same call. If the write fails the revocation still holds for this process's
// lifetime (the pre-fix behaviour, never weaker) and the failure is logged with
// what it means: a restart before the row lands would re-enable the token.
func (s *Signer) Revoke(sessionID string) {
	s.RevokeContext(context.Background(), sessionID)
}

// RevokeContext is Revoke with the caller's deadline.
func (s *Signer) RevokeContext(ctx context.Context, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	if s.generation == nil {
		s.generation = map[string]int64{}
	}
	s.generation[sessionID]++
	local := s.generation[sessionID]
	s.mu.Unlock()

	if s.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	// The row must outlive every token that was minted before this stop: their
	// own `exp` is at most now+ttl, so the same instant is the sweep deadline.
	generation, err := s.store.RevokeGatewayToken(ctx, sessionID, time.Now().Add(s.ttl))
	if err != nil {
		s.log("model gateway token revocation is in memory only: the durable write failed, so a control-plane restart inside the token TTL would re-enable it",
			"session", sessionID, "error", err.Error())
		return
	}
	s.remember(sessionID, generation, local)
}

// remember keeps the in-memory fast path at least at the durable value.
func (s *Signer) remember(sessionID string, values ...int64) {
	highest := int64(0)
	for _, v := range values {
		if v > highest {
			highest = v
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation == nil {
		s.generation = map[string]int64{}
	}
	if s.generation[sessionID] < highest {
		s.generation[sessionID] = highest
	}
}

// durableGeneration asks the store for the number tokens must carry. A failure
// is reported and the in-memory value is used: a read that could not be made
// must never accept a token the fast path would have refused.
func (s *Signer) durableGeneration(ctx context.Context, sessionID string) int64 {
	if s.store == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	generation, err := s.store.GatewayRevocationGeneration(ctx, sessionID)
	if err != nil {
		s.storeErrors.Add(1)
		s.log("model gateway token check fell back to the in-memory revocation set",
			"session", sessionID, "error", err.Error())
		return 0
	}
	if generation > 0 {
		s.remember(sessionID, generation)
	}
	return generation
}

// Sweep deletes revocation rows whose last affected token has expired, and
// reports how many went. The control plane runs it on a timer; tests call it
// directly.
func (s *Signer) Sweep(ctx context.Context, now time.Time) (int64, error) {
	if s.store == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	return s.store.SweepGatewayRevocations(ctx, now)
}

// Revoked reports whether sessionID has ever been revoked. It is diagnostics,
// not the validity check: Verify compares the token's generation, so a token
// minted after the revocation is valid while this still answers true.
func (s *Signer) Revoked(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation[sessionID] > 0
}

// generationFor is the number a new token must carry.
func (s *Signer) generationFor(sessionID string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation[sessionID]
}

// RevokedCount is the number of sessions with a revocation on record. Tests and
// the admin log use it to tell "nothing was revoked" from "revocation is not
// wired".
func (s *Signer) RevokedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.generation)
}

// Issue mints a token for a session.
//
// The generation is read from the durable store when there is one: a session
// that was revoked before a control-plane restart must mint its next token with
// the *persisted* number, or the resumed session's token would be refused by the
// very rule that keeps the old one dead.
func (s *Signer) Issue(sessionID, userID, provider, model string, now time.Time) (string, error) {
	return s.IssueBound(sessionID, userID, provider, model, "", now)
}

// IssueBound is Issue for a token that may only be spent from one sandbox.
//
// sandboxID is the sandbox the agent runs in (the container's name for a
// container placement). Empty means "not bound", which is the local placement:
// the agent runs on the control plane's own host, so there is no other address it
// could be required to arrive from, and claiming one would lock it out of its own
// gateway.
//
// The claim is written into the signed payload, so a copied token cannot drop it,
// and the proof the gateway demands in addition (X-PF-Sandbox-Proof) is not in
// this file at all — it is computed here and travels in the agent's environment
// (see binding.go).
func (s *Signer) IssueBound(sessionID, userID, provider, model, sandboxID string, now time.Time) (string, error) {
	if sessionID == "" || userID == "" || provider == "" {
		return "", errors.New("llmgw: session, user and provider are required to issue a token")
	}
	generation := s.generationFor(sessionID)
	if durable := s.durableGeneration(context.Background(), sessionID); durable > generation {
		generation = durable
	}
	claims := Claims{
		SessionID:  sessionID,
		UserID:     userID,
		Provider:   provider,
		Model:      model,
		Sandbox:    strings.TrimSpace(sandboxID),
		IssuedAt:   now.Unix(),
		Generation: generation,
		ExpiresAt:  now.Add(s.ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("llmgw: encode claims: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	// Keep the revocation row alive for at least as long as this token: the
	// sweep must not delete the generation this token relies on before the token
	// itself expires.
	if s.store != nil && generation > 0 {
		s.extend(context.Background(), sessionID, time.Unix(claims.ExpiresAt, 0))
	}
	return body + "." + s.sign(body), nil
}

// extend pushes the row's expiry out to cover a token minted after the revoke.
// A failure is logged, not returned: the token is already signed and the worst
// case (the row is swept early) is a rejected token at the next check, which the
// next prompt reissues.
func (s *Signer) extend(ctx context.Context, sessionID string, expiresAt time.Time) {
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	if err := s.store.ExtendGatewayRevocation(ctx, sessionID, expiresAt); err != nil {
		s.log("model gateway: could not extend the revocation record for a resumed session",
			"session", sessionID, "error", err.Error())
	}
}

// Verify checks the signature, the expiry and the session's revocation
// generation.
func (s *Signer) Verify(ctx context.Context, token string, now time.Time) (Claims, error) {
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
	// credential behind for the rest of the TTL. The comparison is against the
	// generation, so the token minted for the next spawn of the same session (a
	// resume, a restart) verifies immediately.
	//
	// The durable number is authoritative — it is what survives the restart that
	// used to re-enable every token — and the in-memory number is the fallback
	// when the store cannot be read. Only the signature is checked before this,
	// so an unauthenticated caller cannot make the gateway hit the database.
	generation := s.generationFor(claims.SessionID)
	if durable := s.durableGeneration(ctx, claims.SessionID); durable > generation {
		generation = durable
	}
	if claims.Generation != generation {
		return Claims{}, errors.New("token revoked: the session's agent has stopped")
	}
	return claims, nil
}

func (s *Signer) sign(body string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
