package httpapi

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// newWebProxy forwards everything that is not the API to the web app.
//
// The deployment shape this exists for: one port, one origin. The browser talks
// to the control plane for /v1, /gw and /healthz (including the WebSocket
// upgrades for the terminal and browser panels) and gets the Next.js pages from
// here. Doing it in-process removes a reverse proxy from the deployment, which
// also removes a class of "works locally, breaks behind the proxy" bugs.
//
// The proxy speaks plain HTTP only: every WebSocket endpoint lives under /v1, so
// no upgrade ever reaches this handler.
func (s *server) newWebProxy(upstream string) (http.Handler, error) {
	parsed, err := url.Parse(strings.TrimRight(upstream, "/"))
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(parsed)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		// Keep the Host the browser used: Next uses it to build absolute URLs,
		// and a mismatch shows up as broken asset links behind a tunnel.
		req.Host = req.URL.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		s.log.ErrorContext(r.Context(), "web upstream failed",
			"path", r.URL.Path, "error", proxyErr.Error())
		http.Error(w, "web app is not reachable", http.StatusBadGateway)
	}
	return proxy, nil
}

// handleWebOrNotFound serves the web app, and answers the API's own paths with a
// JSON 404 so a typo in an endpoint never returns the HTML shell.
func (s *server) handleWebOrNotFound(web http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/gw/") || p == "/v1" || p == "/gw" {
			s.handleNotFound(w, r)
			return
		}
		web.ServeHTTP(w, r)
	}
}

// ensure contracts stays imported if this file is trimmed.
var _ = contracts.ErrNotFound
