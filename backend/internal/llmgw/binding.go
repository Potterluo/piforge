package llmgw

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Binding a token to the sandbox it was minted for.
//
// The problem this ends: a model gateway token is a bearer credential. It used
// to answer /gw/llm from the host, from another sandbox, from another tenant —
// anywhere the control plane's port was reachable — for up to an hour. Revocation
// (§16.1) bounds its *life*; nothing bounded its *holder*.
//
// Two independent checks now do, and they are deliberately different in kind:
//
//	address  the request must arrive from an address the sandbox has on the
//	         tenant bridge. This is a property of the network position, not of
//	         anything the client knows, so a token copied out of the container is
//	         refused wherever the copy is used. It is the strong half.
//	proof    HMAC(per-sandbox secret, token) in X-PF-Sandbox-Proof, computed by
//	         the control plane when it mints the token and carried in the agent's
//	         environment (models.json holds "$PIFORGE_SANDBOX_PROOF", a
//	         reference, not the value). It is the half that still holds if the
//	         network view is wrong: two sandboxes behind one apparent address are
//	         still told apart, because their secrets differ.
//
// What neither half covers is stated plainly in docs/EXECUTION-BOUNDARY.md §16.3:
// the agent itself runs *inside* the sandbox, so it can read the proof out of its
// own environment and call the gateway from the address it is supposed to call
// from. The boundary being drawn is "this credential is worthless outside the
// sandbox it belongs to", not "the sandbox cannot use its own credential".

// HeaderProof is the header the agent's model client sends. Its value is
// HMAC-SHA256(secret, token), hex encoded.
const HeaderProof = "X-PF-Sandbox-Proof"

// ProofEnv is the environment variable models.json interpolates. The value is a
// reference: `"headers": {"X-PF-Sandbox-Proof": "$PIFORGE_SANDBOX_PROOF"}`. That
// is the whole point of the indirection — the file the agent can read (and an
// attacker can copy) does not contain the proof.
const ProofEnv = "PIFORGE_SANDBOX_PROOF"

// DefaultCacheTTL is how long a resolved address set is reused. It is short on
// purpose: the answer comes from Docker's embedded DNS, which is authoritative
// for container names, and a pause/resume or a container replacement has to be
// picked up without a control-plane restart. A mismatch bypasses the cache
// entirely, so this only decides how many lookups a *working* sandbox costs.
const DefaultCacheTTL = 2 * time.Second

// AddressResolver answers "which addresses does this sandbox have right now".
//
// The name is the sandbox id: for a container placement that is the container
// name (sandbox.Docker derives it from the (user, project) pair), and the control
// plane is joined to the tenant bridge, so Docker's embedded DNS answers with the
// sandbox's address on that bridge — the source address its requests carry.
type AddressResolver interface {
	SandboxAddresses(ctx context.Context, sandboxID string) ([]string, error)
}

// ProofSecrets answers the per-sandbox secret the proof is HMAC'd with.
//
// It is the control plane's own secret, sealed in the sandbox row, and it never
// enters the container: the only thing that crosses the boundary is
// HMAC(secret, token), which is worth exactly one token.
type ProofSecrets interface {
	SandboxProofSecret(ctx context.Context, sandboxID string) (string, error)
}

// SessionSandboxes answers which sandbox a session is running in. It exists for
// the MCP broker, whose credential is minted per (session, server) and therefore
// has a session rather than a sandbox to bind to.
type SessionSandboxes interface {
	SandboxOfSession(ctx context.Context, sessionID string) (string, error)
}

// Denied is a refusal with a reason a log line and a test can name.
//
// The message is deliberately specific: an operator debugging "my sandbox cannot
// reach the gateway" needs "address mismatch" and not "forbidden". It is also
// safe to say out loud — the caller already holds a valid token, so the reason
// tells them nothing they could not learn by trying.
type Denied struct {
	// Code is the machine-readable reason, mirrored in the JSON body.
	Code string
	// Message is the human sentence.
	Message string
}

func (d *Denied) Error() string { return d.Code + ": " + d.Message }

// The codes. They are part of the contract: the drill asserts them, and the docs
// quote them.
const (
	// CodeAddressMismatch: the request arrived from an address this sandbox does
	// not have. This is what "the token was used from the host" looks like.
	CodeAddressMismatch = "sandbox_address_mismatch"
	// CodeAddressUnresolved: the sandbox's addresses could not be read at all, so
	// the check could not be made. Closed by default: a check that cannot run is
	// not a check that passed.
	CodeAddressUnresolved = "sandbox_address_unresolved"
	// CodeProofMissing: the token is bound to a sandbox whose proof is required,
	// and the request carried no X-PF-Sandbox-Proof.
	CodeProofMissing = "sandbox_proof_missing"
	// CodeProofInvalid: a proof was presented and it does not verify.
	CodeProofInvalid = "sandbox_proof_invalid"
	// CodeProofUnavailable: the sandbox has no secret on record, so the proof
	// cannot be verified. A wiring bug, reported as one rather than waved through.
	CodeProofUnavailable = "sandbox_proof_unavailable"
)

func deny(code, format string, args ...any) *Denied {
	return &Denied{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Proof is the value the agent sends in HeaderProof.
//
// It is HMAC(secret, token) rather than the secret itself for one reason: a proof
// copied out of one sandbox is then worth nothing against another token of the
// same sandbox, and it changes on every mint.
func Proof(secret, token string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// ProofMatches compares a presented proof with the expected one in constant time.
func ProofMatches(expected, presented string) bool {
	if expected == "" || presented == "" {
		return false
	}
	return hmac.Equal([]byte(expected), []byte(presented))
}

// Binder enforces both halves. The zero value is not usable: Addresses is
// required for the address half, and a Binder built without Proofs enforces the
// address alone (which is what a deployment with no per-sandbox secret yet
// gets — named in the boot log rather than assumed).
type Binder struct {
	// Addresses resolves a sandbox's current addresses. Required.
	Addresses AddressResolver
	// Proofs supplies the per-sandbox secret. Nil disables the proof half.
	Proofs ProofSecrets
	// Sessions resolves a session to its sandbox (the MCP broker's entry point).
	Sessions SessionSandboxes
	// CacheTTL overrides DefaultCacheTTL.
	CacheTTL time.Duration
	// Now is injectable for tests.
	Now func() time.Time
	// Logger receives one line per re-resolution and per denial.
	Logger func(format string, args ...any)

	mu    sync.Mutex
	cache map[string]addressEntry
}

type addressEntry struct {
	addrs []string
	at    time.Time
}

func (b *Binder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Binder) ttl() time.Duration {
	if b.CacheTTL > 0 {
		return b.CacheTTL
	}
	return DefaultCacheTTL
}

func (b *Binder) logf(format string, args ...any) {
	if b.Logger != nil {
		b.Logger(format, args...)
	}
}

// Binds reports whether this binder enforces anything. A nil binder does not, and
// the gateway says so in its boot log rather than pretending.
func (b *Binder) Binds() bool { return b != nil && b.Addresses != nil }

// Authorize checks one /gw/llm request against the sandbox its token names.
//
// token is the exact string the client presented (the path segment or the bearer
// value): the proof is computed over it, so it has to be the one that verified.
// A token with no sandbox claim is not bound — that is the local placement, where
// the agent runs on the host and there is no address to bind it to — and the
// caller's boot log says which placement it is.
func (b *Binder) Authorize(ctx context.Context, claims Claims, token, remoteAddr, presented string) error {
	if b == nil {
		return nil
	}
	if c := claims.Sandbox; c != "" {
		err := b.authorizeAddress(ctx, c, remoteAddr)
		if err == nil {
			err = b.authorizeProof(ctx, claims.Sandbox, token, presented)
		}
		if err != nil {
			// Refusals are logged where the reason is known, at a level an
			// operator actually reads: "my sandbox cannot reach the gateway" is
			// answered by this line, and a silent 403 is the worst possible
			// answer. One line per refusal, never per request.
			b.logf("llmgw: refused a bound token: session=%s sandbox=%s from=%s reason=%s",
				claims.SessionID, c, RemoteIP(remoteAddr), err.Error())
		}
		return err
	}
	return nil
}

// AuthorizeSession applies the address half to a credential that names a session
// rather than a sandbox (the MCP broker token). There is no proof half here: the
// broker token is the agent's own credential in mcp.json, and the property worth
// having is the one that survives copying — the address.
func (b *Binder) AuthorizeSession(ctx context.Context, sessionID, remoteAddr string) error {
	if !b.Binds() || b.Sessions == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	sandboxID, err := b.Sessions.SandboxOfSession(ctx, sessionID)
	if err != nil {
		// A session with no sandbox row (the local placement, or a session that
		// was never provisioned) has no address to bind to. Not an error.
		return nil
	}
	if strings.TrimSpace(sandboxID) == "" {
		return nil
	}
	if err := b.authorizeAddress(ctx, sandboxID, remoteAddr); err != nil {
		b.logf("llmgw: refused a broker token: session=%s sandbox=%s from=%s reason=%s",
			sessionID, sandboxID, RemoteIP(remoteAddr), err.Error())
		return err
	}
	return nil
}

// authorizeAddress is the address half.
func (b *Binder) authorizeAddress(ctx context.Context, sandboxID, remoteAddr string) error {
	if !b.Binds() {
		return nil
	}
	from := RemoteIP(remoteAddr)
	if from == "" {
		return deny(CodeAddressUnresolved, "the request has no source address, so it cannot be matched to a sandbox")
	}
	addrs, err := b.addresses(ctx, sandboxID, false)
	if err != nil {
		return deny(CodeAddressUnresolved,
			"the sandbox %s has no address this control plane can resolve (%v)", sandboxID, err)
	}
	if containsIP(addrs, from) {
		return nil
	}
	// Not the cached answer: the address may have moved (a container restarted
	// onto a new address, or the bridge was rebuilt). Re-read it once from the
	// source of truth before refusing, which is what makes the binding survive a
	// restart without a re-bind hook.
	fresh, ferr := b.addresses(ctx, sandboxID, true)
	if ferr == nil && containsIP(fresh, from) {
		b.logf("llmgw: sandbox %s moved to a new address; the binding followed it", sandboxID)
		return nil
	}
	return deny(CodeAddressMismatch,
		"this token belongs to sandbox %s (addresses %s); the request arrived from %s",
		sandboxID, strings.Join(addrs, ","), from)
}

// authorizeProof is the proof half.
func (b *Binder) authorizeProof(ctx context.Context, sandboxID, token, presented string) error {
	if b.Proofs == nil {
		return nil
	}
	secret, err := b.Proofs.SandboxProofSecret(ctx, sandboxID)
	if err != nil {
		return deny(CodeProofUnavailable, "the proof secret of sandbox %s cannot be read: %v", sandboxID, err)
	}
	if strings.TrimSpace(secret) == "" {
		return deny(CodeProofUnavailable, "sandbox %s has no proof secret on record", sandboxID)
	}
	if strings.TrimSpace(presented) == "" {
		return deny(CodeProofMissing, "this token is bound to sandbox %s and needs the %s header", sandboxID, HeaderProof)
	}
	if !ProofMatches(Proof(secret, token), presented) {
		return deny(CodeProofInvalid, "the %s header does not prove possession of sandbox %s's secret", HeaderProof, sandboxID)
	}
	return nil
}

// addresses reads the resolver, through the cache unless fresh is set.
//
// A failed lookup falls back to the cached answer when there is one: a resolver
// hiccup must not lock an agent out of its own gateway, and the cached addresses
// can only be used by something that holds the address anyway.
func (b *Binder) addresses(ctx context.Context, sandboxID string, fresh bool) ([]string, error) {
	if !fresh {
		b.mu.Lock()
		entry, ok := b.cache[sandboxID]
		b.mu.Unlock()
		if ok && b.now().Sub(entry.at) < b.ttl() {
			return entry.addrs, nil
		}
	}
	addrs, err := b.Addresses.SandboxAddresses(ctx, sandboxID)
	if err != nil || len(addrs) == 0 {
		b.mu.Lock()
		cached, ok := b.cache[sandboxID]
		b.mu.Unlock()
		if ok && len(cached.addrs) > 0 {
			b.logf("llmgw: could not re-resolve sandbox %s; keeping the last known address(%s)", sandboxID, errString(err))
			return cached.addrs, nil
		}
		if err == nil {
			err = fmt.Errorf("no addresses")
		}
		return nil, err
	}
	cleaned := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if ip := normalizeIP(addr); ip != "" {
			cleaned = append(cleaned, ip)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("the resolver answered only unusable addresses: %s", strings.Join(addrs, ","))
	}
	b.mu.Lock()
	if b.cache == nil {
		b.cache = map[string]addressEntry{}
	}
	b.cache[sandboxID] = addressEntry{addrs: cleaned, at: b.now()}
	b.mu.Unlock()
	return cleaned, nil
}

// RemoteIP extracts the address from an http.Request's RemoteAddr.
//
// A bare address is accepted too (a test, or a server whose RemoteAddr is already
// just the IP). Nothing else is consulted: X-Forwarded-For is header text a client
// controls, and the whole point of this check is what the network says.
func RemoteIP(remoteAddr string) string {
	raw := strings.TrimSpace(remoteAddr)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return normalizeIP(host)
	}
	return normalizeIP(strings.Trim(raw, "[]"))
}

// normalizeIP canonicalises an address for comparison, reporting "" for anything
// that is not an IP ("localhost" is not an address here: it names a resolver).
func normalizeIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

func containsIP(addrs []string, want string) bool {
	for _, addr := range addrs {
		if normalizeIP(addr) == want {
			return true
		}
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return "no error"
	}
	return err.Error()
}

// DNSAddresses resolves a sandbox id with the process's own resolver.
//
// In the isolated deployment that is Docker's embedded DNS: the control plane is
// joined to every tenant bridge it serves, the sandbox is a container on one of
// them, and its name resolves to its address there. This is deliberately not a
// Docker API call — the control plane has no daemon socket (docs/SANDBOX-DEPLOY.md
// §1) — and it is also why the answer is live: the embedded DNS server is
// authoritative for container names, so a container that restarts onto a new
// address is described correctly on the next lookup, with no re-bind step.
type DNSAddresses struct {
	// Lookup overrides net.DefaultResolver.LookupHost (tests).
	Lookup func(ctx context.Context, host string) ([]string, error)
}

// NewDNSAddresses builds the production resolver.
func NewDNSAddresses() *DNSAddresses { return &DNSAddresses{} }

// SandboxAddresses implements AddressResolver.
func (d *DNSAddresses) SandboxAddresses(ctx context.Context, sandboxID string) ([]string, error) {
	name := strings.TrimSpace(sandboxID)
	if name == "" {
		return nil, fmt.Errorf("llmgw: empty sandbox id")
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	addrs, err := lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}
