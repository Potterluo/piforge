// Package auth implements PiForge authentication: bcrypt password hashes,
// opaque session tokens stored as sha256 digests, request middleware and a
// double-submit CSRF check.
//
// It deliberately owns no SQL. The two small interfaces below are satisfied by
// *store.Store in production and by in-memory fakes in tests, which keeps the
// security logic testable without a database.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Package errors. The HTTP layer maps them onto the documented API codes.
var (
	// ErrInvalidCredentials means "wrong email or password". It never says
	// which, so the API cannot be used to enumerate accounts.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrAccountDisabled means the credentials were right but the account is
	// not usable.
	ErrAccountDisabled = errors.New("auth: account disabled")
	// ErrNoSession means the request carried no usable session cookie.
	ErrNoSession = errors.New("auth: no session")
)

// UserDirectory resolves accounts during login and session resolution.
type UserDirectory interface {
	// UserByEmail returns the account and its bcrypt password hash (empty when
	// the account has no password).
	UserByEmail(ctx context.Context, email string) (*contracts.User, string, error)
	// UserByID returns one account by id.
	UserByID(ctx context.Context, userID string) (*contracts.User, error)
}

// SessionStore persists hashed session tokens.
type SessionStore interface {
	// CreateAuthSession stores tokenHash for userID until expiresAt.
	CreateAuthSession(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	// AuthSessionUser resolves tokenHash to its owner, treating sessions at or
	// before now as gone.
	AuthSessionUser(ctx context.Context, tokenHash string, now time.Time) (*contracts.User, error)
	// DeleteAuthSession revokes one token.
	DeleteAuthSession(ctx context.Context, tokenHash string) error
}

// Cookie and header names. They are constants rather than configuration
// because the frontend reads them literally.
const (
	// CookieSession is the HttpOnly session cookie.
	CookieSession = "pf_session"
	// CookieCSRF is the readable half of the double submit pair.
	CookieCSRF = "pf_csrf"
	// HeaderCSRF is the request header the client must echo.
	HeaderCSRF = "X-PF-CSRF"
	// HeaderCSRFResponse lets a client learn the token right after login even
	// if it does not read cookies.
	HeaderCSRFResponse = "X-PF-CSRF"
)

// Config is the authenticator's tunables. The zero value is usable: 24h
// sessions, non-Secure cookies (development over http), standard names.
type Config struct {
	// SessionTTL is how long a login lasts. Default 24h.
	SessionTTL time.Duration
	// Secure marks cookies Secure; set it whenever the app is served over
	// https.
	Secure bool
	// Now is injectable so tests can move the clock.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.SessionTTL <= 0 {
		c.SessionTTL = 24 * time.Hour
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Session is the material produced by a successful login. The HTTP layer is
// responsible for putting Token and CSRFToken into cookies; keeping that out
// of this package makes the login path testable without a ResponseWriter.
type Session struct {
	User      *contracts.User
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

// Authenticator performs logins and guards handlers.
type Authenticator struct {
	users    UserDirectory
	sessions SessionStore
	cfg      Config
}

// New builds an Authenticator. It fails on nil dependencies rather than
// panicking later, because a nil UserDirectory would otherwise turn every
// request into a 500.
func New(users UserDirectory, sessions SessionStore, cfg Config) (*Authenticator, error) {
	if users == nil {
		return nil, errors.New("auth: New: UserDirectory is nil")
	}
	if sessions == nil {
		return nil, errors.New("auth: New: SessionStore is nil")
	}
	return &Authenticator{users: users, sessions: sessions, cfg: cfg.withDefaults()}, nil
}

// Config returns the effective configuration (after defaults).
func (a *Authenticator) Config() Config { return a.cfg }

// Login verifies a password and starts a session. The returned token is the
// raw cookie value; only its sha256 digest is stored.
func (a *Authenticator) Login(ctx context.Context, email, password string) (*Session, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	user, hash, err := a.users.UserByEmail(ctx, email)
	if err != nil || user == nil {
		// Spend comparable time before failing so that response time does not
		// reveal whether the account exists.
		CheckPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	if !CheckPassword(hash, password) {
		return nil, ErrInvalidCredentials
	}
	if user.Status != "active" {
		return nil, ErrAccountDisabled
	}

	token, err := newOpaqueToken()
	if err != nil {
		return nil, fmt.Errorf("auth: login: session token: %w", err)
	}
	csrf, err := newOpaqueToken()
	if err != nil {
		return nil, fmt.Errorf("auth: login: csrf token: %w", err)
	}
	expires := a.cfg.Now().Add(a.cfg.SessionTTL)
	if err := a.sessions.CreateAuthSession(ctx, user.ID, HashToken(token), expires); err != nil {
		return nil, fmt.Errorf("auth: login: persist session: %w", err)
	}
	return &Session{User: user, Token: token, CSRFToken: csrf, ExpiresAt: expires}, nil
}

// Authenticate resolves the session cookie on r to a user.
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (*contracts.User, error) {
	c, err := r.Cookie(CookieSession)
	if err != nil || c.Value == "" {
		return nil, ErrNoSession
	}
	user, err := a.sessions.AuthSessionUser(ctx, HashToken(c.Value), a.cfg.Now())
	if err != nil {
		return nil, ErrNoSession
	}
	if user.Status != "active" {
		return nil, ErrAccountDisabled
	}
	return user, nil
}

// Logout revokes the session cookie on r. A missing or already-invalid cookie
// is not an error: logout is idempotent.
func (a *Authenticator) Logout(ctx context.Context, r *http.Request) error {
	c, err := r.Cookie(CookieSession)
	if err != nil || c.Value == "" {
		return nil
	}
	if err := a.sessions.DeleteAuthSession(ctx, HashToken(c.Value)); err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	return nil
}

// SetCookies writes the session pair. The session cookie is HttpOnly and
// SameSite=Lax; the CSRF cookie is intentionally readable by JavaScript
// because the client has to echo it back in a header.
func (a *Authenticator) SetCookies(w http.ResponseWriter, s *Session) {
	maxAge := int(a.cfg.SessionTTL.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     CookieSession,
		Value:    s.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  s.ExpiresAt,
		MaxAge:   maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CookieCSRF,
		Value:    s.CSRFToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   a.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  s.ExpiresAt,
		MaxAge:   maxAge,
	})
	w.Header().Set(HeaderCSRFResponse, s.CSRFToken)
}

// ClearCookies expires both cookies.
func (a *Authenticator) ClearCookies(w http.ResponseWriter) {
	for _, name := range []string{CookieSession, CookieCSRF} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: name == CookieSession,
			Secure:   a.cfg.Secure,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Unix(0, 0),
			MaxAge:   -1,
		})
	}
}

// EnsureCSRF returns the CSRF token for the caller, minting (and setting) a
// cookie when the client does not have one yet. GET /auth/me uses it so a page
// reload does not lose the ability to mutate.
func (a *Authenticator) EnsureCSRF(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(CookieCSRF); err == nil && c.Value != "" {
		return c.Value, nil
	}
	token, err := newOpaqueToken()
	if err != nil {
		return "", fmt.Errorf("auth: csrf token: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieCSRF,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   a.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  a.cfg.Now().Add(a.cfg.SessionTTL),
		MaxAge:   int(a.cfg.SessionTTL.Seconds()),
	})
	w.Header().Set(HeaderCSRFResponse, token)
	return token, nil
}

// RequireUser resolves the session cookie and stores the user in the request
// context. It answers 401 (or 403 for a disabled account) and never calls the
// next handler without a user.
func (a *Authenticator) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.Authenticate(r.Context(), r)
		if err != nil {
			if errors.Is(err, ErrAccountDisabled) {
				writeError(w, http.StatusForbidden, contracts.ErrForbidden, "account is disabled")
				return
			}
			writeError(w, http.StatusUnauthorized, contracts.ErrUnauthenticated, "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
	})
}

// RequireCSRF enforces the double-submit check on state-changing methods:
// the X-PF-CSRF header must equal the pf_csrf cookie. Safe methods pass
// through untouched, and so does an unauthenticated request — that is
// RequireUser's job, and reporting 401 before 403 keeps the error honest.
func (a *Authenticator) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(CookieCSRF)
		header := r.Header.Get(HeaderCSRF)
		if err != nil || cookie.Value == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			writeError(w, http.StatusForbidden, contracts.ErrForbidden, "missing or invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Protect is RequireUser followed by RequireCSRF, the standard wrapper for a
// mutating endpoint.
func (a *Authenticator) Protect(next http.Handler) http.Handler {
	return a.RequireUser(a.RequireCSRF(next))
}

// HashToken returns the hex sha256 of a raw session token. Only this digest is
// ever persisted or compared.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newOpaqueToken returns 32 random bytes as base64url without padding.
func newOpaqueToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// writeError emits the wrapped APIError envelope that PRD §9.1 documents
// ({"error": {"code", "message"}}) and that web/lib/types.ts parses. The auth
// package cannot import the HTTP layer (that would be a cycle), so this
// three-line writer is duplicated on purpose; the middleware tests cover it.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error *contracts.APIError `json:"error"`
	}{Error: &contracts.APIError{Code: code, Message: message}})
}

// dummyHash is a valid bcrypt hash of an unguessable value, used to keep the
// "user not found" path as expensive as the "wrong password" path.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
