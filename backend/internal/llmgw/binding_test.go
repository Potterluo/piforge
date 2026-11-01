package llmgw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// fakeAddresses is a resolver a test can move.
type fakeAddresses struct {
	mu      sync.Mutex
	addrs   map[string][]string
	err     error
	lookups int
}

func (f *fakeAddresses) SandboxAddresses(_ context.Context, sandboxID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.err != nil {
		return nil, f.err
	}
	return f.addrs[sandboxID], nil
}

func (f *fakeAddresses) set(sandboxID string, addrs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addrs == nil {
		f.addrs = map[string][]string{}
	}
	f.addrs[sandboxID] = addrs
}

func (f *fakeAddresses) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeAddresses) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups
}

// fakeSecrets is a per-sandbox secret store.
type fakeSecrets map[string]string

func (f fakeSecrets) SandboxProofSecret(_ context.Context, sandboxID string) (string, error) {
	return f[sandboxID], nil
}

// fakeSessions maps a session to a sandbox.
type fakeSessions map[string]string

func (f fakeSessions) SandboxOfSession(_ context.Context, sessionID string) (string, error) {
	return f[sessionID], nil
}

func testBinder(addrs *fakeAddresses) *Binder {
	return &Binder{
		Addresses: addrs,
		Proofs:    fakeSecrets{"sbx_1": "secret-of-1", "sbx_2": "secret-of-2"},
		Sessions:  fakeSessions{"ses_1": "sbx_1"},
		CacheTTL:  time.Minute,
	}
}

// ---------------------------------------------------------------------------
// the address half
// ---------------------------------------------------------------------------

func TestASandboxCanSpendItsTokenFromItsOwnAddress(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)

	claims := Claims{SessionID: "ses_1", Sandbox: "sbx_1"}
	if err := b.Authorize(context.Background(), claims, "tok", "172.20.0.5:41234", Proof("secret-of-1", "tok")); err != nil {
		t.Fatalf("Authorize from the sandbox's own address = %v, want nil", err)
	}
}

func TestATokenCopiedOutOfTheSandboxIsRefusedFromTheHost(t *testing.T) {
	// The defect this closes: a token read out of models.json answered /gw/llm
	// from the operator's host (192.0.2.1 is the address httptest gives a request).
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)

	err := b.Authorize(context.Background(), Claims{SessionID: "ses_1", Sandbox: "sbx_1"}, "tok", "192.0.2.1:5000", Proof("secret-of-1", "tok"))
	var refused *Denied
	if !errors.As(err, &refused) {
		t.Fatalf("Authorize from the host = %v, want a *Denied", err)
	}
	if refused.Code != CodeAddressMismatch {
		t.Fatalf("code = %q, want %q (%s)", refused.Code, CodeAddressMismatch, refused.Message)
	}
	if !strings.Contains(refused.Message, "172.20.0.5") || !strings.Contains(refused.Message, "192.0.2.1") {
		t.Fatalf("the refusal must name both addresses: %q", refused.Message)
	}
}

func TestATokenIsRefusedFromAnotherSandboxOfTheSameTenant(t *testing.T) {
	// Same bridge, same tenant, different address: the second sandbox holds a
	// token that names the first, and it must not be spendable there.
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	addrs.set("sbx_2", "172.20.0.6")
	b := testBinder(addrs)

	if err := b.Authorize(context.Background(), Claims{SessionID: "ses_1", Sandbox: "sbx_1"}, "tok", "172.20.0.6:41234", Proof("secret-of-1", "tok")); err == nil {
		t.Fatal("the first sandbox's token was accepted from the second sandbox")
	}
}

func TestTheBindingFollowsAnAddressThatMoved(t *testing.T) {
	// A pause/resume or a container replacement can land on a new address. The
	// cached answer is stale, so a mismatch must re-read the source of truth
	// rather than refuse — otherwise the binding would break stage-3 reattach.
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)

	claims := Claims{SessionID: "ses_1", Sandbox: "sbx_1"}
	if err := b.Authorize(context.Background(), claims, "tok", "172.20.0.5:1", Proof("secret-of-1", "tok")); err != nil {
		t.Fatalf("first call = %v", err)
	}
	// The cache now holds .5; the sandbox moves to .9.
	if err := b.Authorize(context.Background(), claims, "tok", "172.20.0.5:2", Proof("secret-of-1", "tok")); err != nil {
		t.Fatalf("cached call = %v", err)
	}
	addrs.set("sbx_1", "172.20.0.9")
	if err := b.Authorize(context.Background(), claims, "tok", "172.20.0.9:3", Proof("secret-of-1", "tok")); err != nil {
		t.Fatalf("Authorize after the address moved = %v, want nil (the re-read must follow it)", err)
	}
}

func TestAMismatchIsNotResolvedByAFreshLookupThatStillDisagrees(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)

	err := b.Authorize(context.Background(), Claims{Sandbox: "sbx_1"}, "tok", "10.1.2.3:1", Proof("secret-of-1", "tok"))
	var refused *Denied
	if !errors.As(err, &refused) || refused.Code != CodeAddressMismatch {
		t.Fatalf("Authorize = %v, want an address mismatch", err)
	}
	if addrs.count() < 2 {
		t.Fatalf("lookups = %d, want at least 2 (cached + fresh)", addrs.count())
	}
}

func TestAnUnresolvableSandboxIsClosedNotOpen(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.fail(errors.New("no such host"))
	b := testBinder(addrs)

	err := b.Authorize(context.Background(), Claims{Sandbox: "sbx_1"}, "tok", "172.20.0.5:1", Proof("secret-of-1", "tok"))
	var refused *Denied
	if !errors.As(err, &refused) || refused.Code != CodeAddressUnresolved {
		t.Fatalf("Authorize = %v, want %q", err, CodeAddressUnresolved)
	}
}

func TestAResolverHiccupKeepsTheLastKnownAddress(t *testing.T) {
	// A DNS failure must not lock an agent out of its own gateway; the cached
	// address can only be used by something that already has it.
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)
	b.CacheTTL = 0 // always re-read
	claims := Claims{Sandbox: "sbx_1"}
	if err := b.Authorize(context.Background(), claims, "tok", "172.20.0.5:1", Proof("secret-of-1", "tok")); err != nil {
		t.Fatalf("first call = %v", err)
	}
	addrs.fail(errors.New("dns is down"))
	if err := b.Authorize(context.Background(), claims, "tok", "172.20.0.5:2", Proof("secret-of-1", "tok")); err != nil {
		t.Fatalf("Authorize during a resolver outage = %v, want the last known address", err)
	}
	// …and it does not turn into an open door for a different address.
	if err := b.Authorize(context.Background(), claims, "tok", "10.0.0.1:3", Proof("secret-of-1", "tok")); err == nil {
		t.Fatal("a resolver outage accepted an address that was never the sandbox's")
	}
}

func TestAnUnboundTokenIsNotAddressChecked(t *testing.T) {
	// The local placement mints tokens with no sandbox claim: the agent runs on
	// the control plane's host, so there is no second address to require.
	addrs := &fakeAddresses{}
	b := testBinder(addrs)

	if err := b.Authorize(context.Background(), Claims{SessionID: "ses_1"}, "tok", "192.0.2.1:1", ""); err != nil {
		t.Fatalf("Authorize(unbound) = %v, want nil", err)
	}
	if addrs.count() != 0 {
		t.Fatalf("lookups = %d, want 0: an unbound token must not touch the resolver", addrs.count())
	}
}

// ---------------------------------------------------------------------------
// the proof half
// ---------------------------------------------------------------------------

func TestTheProofIsDemandedAndChecked(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)
	claims := Claims{SessionID: "ses_1", Sandbox: "sbx_1"}
	from := "172.20.0.5:1"

	cases := []struct {
		name  string
		proof string
		code  string
	}{
		{"no proof at all", "", CodeProofMissing},
		{"a proof for another token", Proof("secret-of-1", "another-token"), CodeProofInvalid},
		{"another sandbox's proof", Proof("secret-of-2", "tok"), CodeProofInvalid},
		{"the right proof", Proof("secret-of-1", "tok"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := b.Authorize(context.Background(), claims, "tok", from, tc.proof)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("Authorize = %v, want nil", err)
				}
				return
			}
			var refused *Denied
			if !errors.As(err, &refused) || refused.Code != tc.code {
				t.Fatalf("Authorize = %v, want %q", err, tc.code)
			}
		})
	}
}

func TestASandboxWithNoSecretIsClosedNotOpen(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.set("sbx_9", "172.20.0.5")
	b := testBinder(addrs)

	err := b.Authorize(context.Background(), Claims{Sandbox: "sbx_9"}, "tok", "172.20.0.5:1", "anything")
	var refused *Denied
	if !errors.As(err, &refused) || refused.Code != CodeProofUnavailable {
		t.Fatalf("Authorize = %v, want %q", err, CodeProofUnavailable)
	}
}

func TestABinderWithoutSecretsEnforcesTheAddressAlone(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := &Binder{Addresses: addrs}

	if err := b.Authorize(context.Background(), Claims{Sandbox: "sbx_1"}, "tok", "172.20.0.5:1", ""); err != nil {
		t.Fatalf("Authorize without a proof half = %v, want nil", err)
	}
	if err := b.Authorize(context.Background(), Claims{Sandbox: "sbx_1"}, "tok", "192.0.2.1:1", ""); err == nil {
		t.Fatal("a binder without secrets accepted a foreign address")
	}
}

// ---------------------------------------------------------------------------
// the session half (the MCP broker)
// ---------------------------------------------------------------------------

func TestABrokerTokenIsBoundToItsSessionsSandbox(t *testing.T) {
	addrs := &fakeAddresses{}
	addrs.set("sbx_1", "172.20.0.5")
	b := testBinder(addrs)

	if err := b.AuthorizeSession(context.Background(), "ses_1", "172.20.0.5:9"); err != nil {
		t.Fatalf("AuthorizeSession from the sandbox = %v, want nil", err)
	}
	if err := b.AuthorizeSession(context.Background(), "ses_1", "192.0.2.1:9"); err == nil {
		t.Fatal("a broker token was accepted from the host")
	}
	// A session with no sandbox (the local placement) is not checked.
	if err := b.AuthorizeSession(context.Background(), "ses_unknown", "192.0.2.1:9"); err != nil {
		t.Fatalf("AuthorizeSession(no sandbox) = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// the handler, end to end
// ---------------------------------------------------------------------------

func TestTheGatewayRefusesABoundTokenFromOutsideItsSandbox(t *testing.T) {
	h := newHarness(t)
	addrs := &fakeAddresses{}
	addrs.set("piforge-abc", "172.20.0.5")
	h.gateway.cfg.Binder = &Binder{
		Addresses: addrs,
		Proofs:    fakeSecrets{"piforge-abc": "secret-of-1"},
		CacheTTL:  time.Minute,
	}

	token, err := h.signer.IssueBound("ses_1", "usr_1", "anthropic-shared", "claude", "piforge-abc", time.Now())
	if err != nil {
		t.Fatalf("IssueBound: %v", err)
	}
	proof := Proof("secret-of-1", token)

	do := func(t *testing.T, remoteAddr, proof string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/gw/llm/"+token+"/v1/messages", strings.NewReader(`{}`))
		req.RemoteAddr = remoteAddr
		if proof != "" {
			req.Header.Set(HeaderProof, proof)
		}
		rec := httptest.NewRecorder()
		h.gateway.ServeHTTP(rec, req)
		return rec
	}

	// The sandbox's own path, with the proof its environment carries.
	if rec := do(t, "172.20.0.5:41000", proof); rec.Code != http.StatusOK {
		t.Fatalf("from inside the sandbox: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The same token, from the host, with a valid proof: the address is what
	// fails, which is the whole point (a copied token does not come with the
	// container's address).
	if rec := do(t, "192.0.2.1:41000", proof); rec.Code != http.StatusForbidden {
		t.Fatalf("from the host: status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	} else if !strings.Contains(rec.Body.String(), CodeAddressMismatch) {
		t.Fatalf("the refusal must carry %q: %s", CodeAddressMismatch, rec.Body.String())
	}
	// From the right address but without the proof: still refused.
	if rec := do(t, "172.20.0.5:41000", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without the proof: status = %d, want 403", rec.Code)
	} else if !strings.Contains(rec.Body.String(), CodeProofMissing) {
		t.Fatalf("the refusal must carry %q: %s", CodeProofMissing, rec.Body.String())
	}
	// An invalid signature is still 401, not 403: nothing was authenticated.
	req := httptest.NewRequest(http.MethodPost, "/gw/llm/not-a-token/v1/messages", strings.NewReader(`{}`))
	req.RemoteAddr = "172.20.0.5:41000"
	rec := httptest.NewRecorder()
	h.gateway.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a bad token: status = %d, want 401", rec.Code)
	}
}

func TestABoundTokenIsRefusedBeforeAnyProviderLookup(t *testing.T) {
	// Cheap rejection: a foreign address must cost nothing beyond the signature
	// check, so the refusal cannot be used to make the control plane touch
	// provider rows.
	h := newHarness(t)
	addrs := &fakeAddresses{}
	addrs.set("piforge-abc", "172.20.0.5")
	h.gateway.cfg.Binder = &Binder{
		Addresses: addrs,
		Proofs:    fakeSecrets{"piforge-abc": "secret-of-1"},
		CacheTTL:  time.Minute,
	}
	token, _ := h.signer.IssueBound("ses_1", "usr_1", "anthropic-shared", "claude", "piforge-abc", time.Now())

	before := h.store.calls
	req := httptest.NewRequest(http.MethodPost, "/gw/llm/"+token+"/v1/messages", strings.NewReader(`{}`))
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set(HeaderProof, Proof("secret-of-1", token))
	h.gateway.ServeHTTP(httptest.NewRecorder(), req)
	if h.store.calls != before {
		t.Fatalf("provider lookups = %d, want %d", h.store.calls, before)
	}
}

func TestTheGatewayLeavesAnUnboundTokenAlone(t *testing.T) {
	// The local placement (and every deployment that predates the binding) mints
	// tokens with no sandbox claim; they must keep working from the host, because
	// there is no other address for that agent to use.
	h := newHarness(t)
	addrs := &fakeAddresses{}
	h.gateway.cfg.Binder = testBinder(addrs)
	token, _ := h.signer.Issue("ses_1", "usr_1", "anthropic-shared", "claude", time.Now())

	rec := h.do(t, "", http.MethodPost, "/gw/llm/"+token+"/v1/messages", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func TestRemoteIPReadsOnlyTheTransport(t *testing.T) {
	cases := map[string]string{
		"172.20.0.5:4123": "172.20.0.5",
		"172.20.0.5":      "172.20.0.5",
		"[fd00::5]:4123":  "fd00::5",
		"fd00::5":         "fd00::5",
		"":                "",
		"localhost:80":    "",
	}
	for in, want := range cases {
		if got := RemoteIP(in); got != want {
			t.Fatalf("RemoteIP(%q) = %q, want %q", in, got, want)
		}
	}
}
