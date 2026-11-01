package llmgw

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestTokenTTLIsOneHour pins the number the adversarial acceptance run asked to
// be defensible: a sandbox token lived for twelve hours and stayed valid outside
// the sandbox. One hour is the ceiling on a leak; revocation (below) is what
// normally ends it.
func TestTokenTTLIsOneHour(t *testing.T) {
	if DefaultTTL != time.Hour {
		t.Fatalf("DefaultTTL = %s, want 1h", DefaultTTL)
	}
	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), DefaultTTL)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	now := time.Now()
	token, err := signer.Issue("ses_ttl", "usr_1", "volc", "deepseek", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	body, _, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token is not signed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("claims are not JSON: %v", err)
	}
	if got := claims.ExpiresAt - claims.IssuedAt; got != 3600 {
		t.Fatalf("token lifetime = %ds, want 3600", got)
	}
	if signer.TTL() != time.Hour {
		t.Fatalf("Signer.TTL() = %s, want 1h", signer.TTL())
	}
	// NewSigner with a zero TTL must not mean "no expiry".
	fallback, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), 0)
	if err != nil {
		t.Fatalf("NewSigner(0): %v", err)
	}
	if fallback.TTL() != DefaultTTL {
		t.Fatalf("zero TTL fell back to %s, want %s", fallback.TTL(), DefaultTTL)
	}
}

// TestRevokeEndsTheSessionToken is the session-lifetime bound: when the agent
// stops, the token the sandbox still holds must stop working immediately, not
// when its TTL happens to run out. The gateway is the one that has to refuse it,
// because the token itself is a valid signature.
func TestRevokeEndsTheSessionToken(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	token, err := h.signer.Issue("ses_pause", "usr_1", "anthropic-shared", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// The token works before the session ends.
	if rec := h.do(t, token, http.MethodPost, "/gw/llm/"+token+"/v1/messages", `{}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("token before revocation = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// The session's agent stops (pause, archive, delete, model switch, idle
	// reaper — every path ends in pi.Runtime.Stop, which calls this).
	h.signer.Revoke("ses_pause")
	if got := h.signer.RevokedCount(); got != 1 {
		t.Fatalf("revocation set holds %d entries, want 1", got)
	}
	// Both shapes of the same token are refused: the path segment (the base URL
	// the sandbox's models.json carries) and the bearer header.
	for _, shape := range []string{"/gw/llm/" + token + "/v1/messages", "/gw/llm/v1/messages"} {
		rec := h.do(t, token, http.MethodPost, shape, `{}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s after revocation = %d, want 401: %s", shape, rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, "invalid_token") {
			t.Fatalf("%s after revocation = %s, want invalid_token", shape, body)
		}
	}
	// A different session is untouched: revocation is per session, not a switch.
	other, err := h.signer.Issue("ses_alive", "usr_1", "anthropic-shared", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if rec := h.do(t, other, http.MethodPost, "/gw/llm/"+other+"/v1/messages", `{}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("an unrelated session's token = %d, want 200", rec.Code)
	}
	if h.signer.Revoked("ses_alive") {
		t.Fatal("an unrelated session was reported revoked")
	}
}

// Revoke is idempotent and forgetting is bounded: a revocation older than the
// TTL is dropped, because any token it could describe has expired by then.
func TestRevokeIsIdempotentAndPruned(t *testing.T) {
	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	signer.Revoke("ses_one")
	signer.Revoke("ses_one")
	signer.Revoke("")
	if got := signer.RevokedCount(); got != 1 {
		t.Fatalf("revocation set holds %d entries, want 1", got)
	}

	short, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Millisecond)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	short.Revoke("ses_old")
	time.Sleep(5 * time.Millisecond)
	short.Revoke("ses_new") // the prune runs on write
	if short.Revoked("ses_old") {
		t.Fatal("a revocation older than the TTL was kept")
	}
	if !short.Revoked("ses_new") {
		t.Fatal("the fresh revocation was pruned")
	}
}
