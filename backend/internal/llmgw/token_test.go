package llmgw

import (
	"encoding/base64"
	"context"
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

// Revoke is idempotent, and the token minted *after* it for the same session is
// valid: that is the pause → resume path, where the stop revokes and the next
// spawn mints. A set keyed only by session id (the first version of this fix)
// rejected the new token too, and a session that was ever paused could not use
// the gateway again until the TTL expired.
func TestRevokeIsIdempotentAndTheNextTokenWorks(t *testing.T) {
	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	now := time.Now()
	before, err := signer.Issue("ses_one", "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	signer.Revoke("ses_one")
	signer.Revoke("ses_one") // idempotent: same generation, same effect
	signer.Revoke("")
	if got := signer.RevokedCount(); got != 1 {
		t.Fatalf("revocation state holds %d sessions, want 1", got)
	}
	if _, err := signer.Verify(context.Background(), before, now); err == nil {
		t.Fatal("a token issued before the revoke still verifies")
	}
	after, err := signer.Issue("ses_one", "usr_1", "volc", "", now)
	if err != nil {
		t.Fatalf("Issue after revoke: %v", err)
	}
	if _, err := signer.Verify(context.Background(), after, now); err != nil {
		t.Fatalf("the token minted after the revoke does not verify: %v", err)
	}
	// A second stop kills the second token too, and the first stays dead.
	signer.Revoke("ses_one")
	if _, err := signer.Verify(context.Background(), after, now); err == nil {
		t.Fatal("the second revoke left the current token valid")
	}
}
