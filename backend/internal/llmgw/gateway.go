// Package llmgw is the egress point for every model call the platform makes.
//
// The promise it keeps is the one in PRD §5.7.2: the sandbox never holds a real
// provider key. An agent process is started with a short-lived token in
// ANTHROPIC_API_KEY / OPENAI_API_KEY and a base URL that points here, and this
// handler swaps the token for the user's real key on the way out.
//
// The consequences are worth stating because they are the reason for the
// indirection:
//
//   - changing a model does not restart the sandbox (only the token's claims
//     change, and tokens are reissued per session start);
//   - a leaked sandbox variable is worth minutes on one upstream host;
//   - the provider key is decrypted exactly once per request, in memory;
//   - the user-shadowing rule for providers lives in one place (the store).
package llmgw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
)

// ProviderLookup resolves a provider name for a user, with its sealed key.
type ProviderLookup interface {
	ResolveProvider(ctx context.Context, userID, name string) (*contracts.Provider, []byte, error)
}

// Config wires the gateway.
type Config struct {
	// Store resolves providers. Required.
	Store ProviderLookup
	// Box opens sealed keys. Required.
	Box *crypto.Box
	// Signer issues and verifies sandbox tokens. Required.
	Signer *Signer
	// Transport overrides the HTTP transport (tests use this).
	Transport http.RoundTripper
	// Binder enforces "this token works only from its own sandbox": the request
	// must arrive from an address the sandbox has, and (when the deployment has a
	// per-sandbox secret) carry a proof. Nil disables both halves, which is what a
	// hand-built test gateway gets; a deployment always sets it, and its boot log
	// says which halves are on.
	Binder *Binder
	// Now is injectable for tests.
	Now func() time.Time
	// Logger receives one line per forwarded request.
	Logger func(format string, args ...any)
}

// Handler forwards an agent's model traffic to the right upstream.
type Handler struct {
	cfg    Config
	client *http.Client
}

// New builds the gateway.
func New(cfg Config) (*Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("llmgw: Store is required")
	}
	if cfg.Box == nil {
		return nil, errors.New("llmgw: Box is required")
	}
	if cfg.Signer == nil {
		return nil, errors.New("llmgw: Signer is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = func(string, ...any) {}
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Handler{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			// No global timeout: model responses stream for minutes. Per-request
			// deadlines come from the caller's context.
		},
	}, nil
}

// ServeHTTP handles /gw/llm/{token}/{upstream path...}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The token normally arrives as the first path segment (a base URL the SDK
	// appends to), but a client may also point its base URL at the bare prefix
	// and send the token as a bearer header. Both shapes are supported; the
	// path segment wins only if it actually verifies, otherwise the first path
	// segment is upstream path and the bearer must carry the token.
	pathToken, rest, err := splitPath(r.URL.Path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown_gateway_path", err.Error())
		return
	}

	var claims Claims
	verified := false
	presented := ""
	if pathToken != "" {
		if cl, verr := h.cfg.Signer.Verify(r.Context(), pathToken, h.cfg.Now()); verr == nil {
			claims, verified, presented = cl, true, pathToken
		}
	}
	if !verified {
		if bearerToken := bearer(r.Header.Get("Authorization")); bearerToken != "" {
			if cl, verr := h.cfg.Signer.Verify(r.Context(), bearerToken, h.cfg.Now()); verr == nil {
				claims, verified, presented = cl, true, bearerToken
				// The segment was part of the upstream path after all.
				rest = "/" + pathToken + rest
			}
		}
	}
	if !verified {
		msg := "missing token"
		if pathToken != "" || r.Header.Get("Authorization") != "" {
			msg = "invalid or expired token"
		}
		writeErr(w, http.StatusUnauthorized, "invalid_token", msg)
		return
	}

	// The token is valid; now the question a bearer credential cannot answer: is
	// this request coming from the sandbox the token was minted for? Spelled out
	// in binding.go. A refusal is 403, not 401: the credential is real, and the
	// holder is what is wrong. The binder logs the reason at a level an operator
	// reads; this line stays at the gateway's own (debug) level because it is the
	// per-request trace.
	if err := h.cfg.Binder.Authorize(r.Context(), claims, presented, r.RemoteAddr, r.Header.Get(HeaderProof)); err != nil {
		var refused *Denied
		if errors.As(err, &refused) {
			writeErr(w, http.StatusForbidden, refused.Code, refused.Message)
			return
		}
		writeErr(w, http.StatusForbidden, "sandbox_binding_failed", err.Error())
		return
	}

	provider, sealed, err := h.cfg.Store.ResolveProvider(r.Context(), claims.UserID, claims.Provider)
	if err != nil {
		// Configuration errors are 4xx on purpose: Pi's model client retries
		// 5xx (and 429) with backoff, which is right for an upstream outage and
		// wrong for "this provider has no key". Answering 502 here made a
		// missing key cost three retries (~30s) before the user saw it.
		writeErr(w, http.StatusBadRequest, "provider_unavailable",
			fmt.Sprintf("provider %q is not available", claims.Provider))
		return
	}
	if len(sealed) == 0 {
		writeErr(w, http.StatusBadRequest, "provider_unavailable",
			fmt.Sprintf("provider %q has no key configured", claims.Provider))
		return
	}
	secret, err := h.cfg.Box.OpenString(sealed)
	if err != nil {
		h.cfg.Logger("llmgw: cannot open key for provider %s: %v", claims.Provider, err)
		writeErr(w, http.StatusBadRequest, "provider_unavailable", "stored key cannot be decrypted")
		return
	}

	target, err := h.upstreamURL(provider, rest, r.URL.RawQuery)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "provider_unavailable", err.Error())
		return
	}

	rproxy := &httputil.ReverseProxy{
		// Flush every write: the response is usually an SSE token stream, and
		// buffering it would turn a 40ms first token into a 3s one.
		FlushInterval: -1,
		Transport:     h.client.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			pr.Out.URL.RawQuery = r.URL.RawQuery

			// Drop the ephemeral credential and the ingress hop headers, then
			// attach the real one in the shape the provider expects.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("Cookie")
			switch provider.API {
			case "anthropic-messages":
				pr.Out.Header.Set("x-api-key", secret)
				pr.Out.Header.Set("anthropic-version", firstNonEmpty(
					pr.In.Header.Get("anthropic-version"), "2023-06-01"))
			default:
				pr.Out.Header.Set("Authorization", "Bearer "+secret)
			}
			pr.Out.Header.Set("X-PiForge-Session", claims.SessionID)
			pr.Out.Header.Set("X-PiForge-Provider", provider.Name)
			pr.Out.Header.Del("X-Forwarded-For")
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			h.cfg.Logger("llmgw: upstream %s failed: %v", target.Host, err)
			writeErr(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		},
	}

	h.cfg.Logger("llmgw: session=%s provider=%s %s %s", claims.SessionID, provider.Name, r.Method, rest)
	rproxy.ServeHTTP(w, r)
}

// upstreamURL joins the provider base URL with the path the client asked for.
func (h *Handler) upstreamURL(provider *contracts.Provider, path, query string) (*url.URL, error) {
	base := strings.TrimSpace(provider.BaseURL)
	if base == "" {
		switch provider.API {
		case "anthropic-messages":
			base = "https://api.anthropic.com"
		case "openai-responses", "openai-compat":
			base = "https://api.openai.com"
		default:
			return nil, fmt.Errorf("provider %q has no base URL", provider.Name)
		}
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("provider %q has an invalid base URL: %w", provider.Name, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("provider %q base URL must be http(s)", provider.Name)
	}

	// Keep any path already present on the base (many gateways document a base
	// that ends in /v1). Clients then append /v1/... themselves, so collapse the
	// duplicate: a provider URL ending in /v1 must not become /v1/v1/....
	joined := strings.TrimRight(parsed.Path, "/")
	if path != "" {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if strings.HasSuffix(joined, "/v1") && strings.HasPrefix(path, "/v1/") {
			path = strings.TrimPrefix(path, "/v1")
		}
		joined += path
	}
	parsed.Path = joined
	parsed.RawQuery = query
	return parsed, nil
}

// splitPath extracts the token and the remainder from a gateway path.
func splitPath(p string) (token, rest string, err error) {
	const prefix = "/gw/llm/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", fmt.Errorf("expected %s prefix", prefix)
	}
	remainder := strings.TrimPrefix(p, prefix)
	if remainder == "" {
		return "", "", nil
	}
	if i := strings.IndexByte(remainder, '/'); i >= 0 {
		return remainder[:i], remainder[i:], nil
	}
	return remainder, "", nil
}

func bearer(header string) string {
	const prefix = "bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The body is deliberately shaped like the platform's other errors so a
	// client that already parses `error.code` needs no special case.
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, message)
}

// DialControl is a helper for tests that need the handler's loopback address.
func (h *Handler) DialControl(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}
