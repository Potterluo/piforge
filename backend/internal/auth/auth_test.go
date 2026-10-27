package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// ---------------------------------------------------------------------------
// Fakes (this package must not depend on internal/store)
// ---------------------------------------------------------------------------

type fakeUsers struct {
	byEmail map[string]*contracts.User
	byID    map[string]*contracts.User
	hashes  map[string]string // user id -> bcrypt hash
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{
		byEmail: map[string]*contracts.User{},
		byID:    map[string]*contracts.User{},
		hashes:  map[string]string{},
	}
}

func (f *fakeUsers) add(u *contracts.User, password string) {
	f.byEmail[strings.ToLower(u.Email)] = u
	f.byID[u.ID] = u
	hash, err := HashPassword(password)
	if err != nil {
		panic(err)
	}
	f.hashes[u.ID] = hash
}

func (f *fakeUsers) UserByEmail(_ context.Context, email string) (*contracts.User, string, error) {
	u, ok := f.byEmail[strings.ToLower(email)]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return u, f.hashes[u.ID], nil
}

func (f *fakeUsers) UserByID(_ context.Context, userID string) (*contracts.User, error) {
	u, ok := f.byID[userID]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

type fakeSessionRow struct {
	userID  string
	expires time.Time
}

type fakeSessions struct {
	mu    sync.Mutex
	rows  map[string]fakeSessionRow
	users *fakeUsers
}

func newFakeSessions(users *fakeUsers) *fakeSessions {
	return &fakeSessions{rows: map[string]fakeSessionRow{}, users: users}
}

func (f *fakeSessions) CreateAuthSession(_ context.Context, userID, tokenHash string, expiresAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[tokenHash] = fakeSessionRow{userID: userID, expires: expiresAt}
	return nil
}

func (f *fakeSessions) AuthSessionUser(_ context.Context, tokenHash string, now time.Time) (*contracts.User, error) {
	f.mu.Lock()
	row, ok := f.rows[tokenHash]
	f.mu.Unlock()
	if !ok || !row.expires.After(now) {
		return nil, errors.New("not found")
	}
	// Return the live user object so tests can flip Status and observe the
	// effect, exactly like the real store (which re-reads the row).
	if u, ok := f.users.byID[row.userID]; ok {
		return u, nil
	}
	return &contracts.User{ID: row.userID, Email: row.userID + "@test", Status: "active"}, nil
}

func (f *fakeSessions) DeleteAuthSession(_ context.Context, tokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, tokenHash)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testAuthenticator(t *testing.T, cfg Config) (*Authenticator, *fakeUsers, *fakeSessions) {
	t.Helper()
	users := newFakeUsers()
	sessions := newFakeSessions(users)
	a, err := New(users, sessions, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, users, sessions
}

func activeUser(id, email string) *contracts.User {
	return &contracts.User{ID: id, Email: email, Role: contracts.RoleUser, Status: "active"}
}

// ---------------------------------------------------------------------------
// Password hashing
// ---------------------------------------------------------------------------

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("piforge-admin-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "piforge-admin-123" {
		t.Fatal("password was stored in clear")
	}
	if !CheckPassword(hash, "piforge-admin-123") {
		t.Fatal("correct password was rejected")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("wrong password was accepted")
	}
	if CheckPassword("", "anything") {
		t.Fatal("empty hash must never match")
	}
	if CheckPassword(hash, "") {
		t.Fatal("empty password must never match")
	}
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestLoginOK(t *testing.T) {
	a, users, sessions := testAuthenticator(t, Config{})
	users.add(activeUser("usr_1", "admin@piforge.local"), "piforge-admin-123")

	sess, err := a.Login(context.Background(), "Admin@PiForge.local", "piforge-admin-123")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.User.ID != "usr_1" {
		t.Fatalf("user = %q, want usr_1", sess.User.ID)
	}
	if sess.Token == "" || sess.CSRFToken == "" {
		t.Fatal("login must mint both a session token and a csrf token")
	}
	if sess.Token == sess.CSRFToken {
		t.Fatal("session and csrf tokens must differ")
	}
	if !sess.ExpiresAt.After(time.Now()) {
		t.Fatal("session must expire in the future")
	}

	sessions.mu.Lock()
	_, storedInClear := sessions.rows[sess.Token]
	_, storedHashed := sessions.rows[HashToken(sess.Token)]
	sessions.mu.Unlock()
	if storedInClear {
		t.Fatal("raw session token was persisted")
	}
	if !storedHashed {
		t.Fatal("hashed session token was not persisted")
	}
}

func TestLoginFailures(t *testing.T) {
	a, users, _ := testAuthenticator(t, Config{})
	users.add(activeUser("usr_1", "dev@piforge.local"), "right-password")

	disabled := activeUser("usr_2", "off@piforge.local")
	disabled.Status = "disabled"
	users.add(disabled, "right-password")

	cases := []struct {
		name     string
		email    string
		password string
		want     error
	}{
		{"wrong password", "dev@piforge.local", "nope", ErrInvalidCredentials},
		{"unknown user", "ghost@piforge.local", "right-password", ErrInvalidCredentials},
		{"empty email", "", "right-password", ErrInvalidCredentials},
		{"empty password", "dev@piforge.local", "", ErrInvalidCredentials},
		{"disabled account", "off@piforge.local", "right-password", ErrAccountDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.Login(context.Background(), tc.email, tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Session resolution and expiry
// ---------------------------------------------------------------------------

func TestAuthenticateExpiredToken(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	cfg := Config{SessionTTL: time.Hour, Now: func() time.Time { return now }}
	a, users, _ := testAuthenticator(t, cfg)
	users.add(activeUser("usr_1", "dev@piforge.local"), "pw")

	sess, err := a.Login(context.Background(), "dev@piforge.local", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: sess.Token})

	u, err := a.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate before expiry: %v", err)
	}
	if u.ID != "usr_1" {
		t.Fatalf("user = %q", u.ID)
	}

	now = now.Add(2 * time.Hour)
	if _, err := a.Authenticate(context.Background(), req); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired token: err = %v, want ErrNoSession", err)
	}
}

func TestAuthenticateRejectsDisabledAccount(t *testing.T) {
	a, users, _ := testAuthenticator(t, Config{})
	u := activeUser("usr_1", "dev@piforge.local")
	users.add(u, "pw")

	sess, err := a.Login(context.Background(), "dev@piforge.local", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	u.Status = "disabled"

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: sess.Token})
	if _, err := a.Authenticate(context.Background(), req); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("err = %v, want ErrAccountDisabled", err)
	}
}

func TestLogoutRevokes(t *testing.T) {
	a, users, sessions := testAuthenticator(t, Config{})
	users.add(activeUser("usr_1", "dev@piforge.local"), "pw")
	sess, err := a.Login(context.Background(), "dev@piforge.local", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: sess.Token})
	if err := a.Logout(context.Background(), req); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	sessions.mu.Lock()
	n := len(sessions.rows)
	sessions.mu.Unlock()
	if n != 0 {
		t.Fatalf("session rows = %d, want 0", n)
	}
	// Logging out twice must not fail.
	if err := a.Logout(context.Background(), req); err != nil {
		t.Fatalf("second Logout: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

func TestRequireUser(t *testing.T) {
	a, users, _ := testAuthenticator(t, Config{})
	users.add(activeUser("usr_1", "dev@piforge.local"), "pw")
	sess, err := a.Login(context.Background(), "dev@piforge.local", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	var seen string
	guarded := a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = MustUserID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("no cookie is 401 unauthenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var body struct {
			Error *contracts.APIError `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Error == nil || body.Error.Code != contracts.ErrUnauthenticated {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})

	t.Run("valid cookie passes the user through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: sess.Token})
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if seen != "usr_1" {
			t.Fatalf("handler saw %q", seen)
		}
	})
}

func TestRequireCSRF(t *testing.T) {
	a, _, _ := testAuthenticator(t, Config{})
	ok := a.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name   string
		method string
		cookie string
		header string
		want   int
	}{
		{"GET is safe", http.MethodGet, "", "", http.StatusNoContent},
		{"HEAD is safe", http.MethodHead, "", "", http.StatusNoContent},
		{"POST without csrf cookie", http.MethodPost, "", "tok", http.StatusForbidden},
		{"POST without header", http.MethodPost, "tok", "", http.StatusForbidden},
		{"POST mismatched", http.MethodPost, "tok", "other", http.StatusForbidden},
		{"POST matched", http.MethodPost, "tok", "tok", http.StatusNoContent},
		{"DELETE matched", http.MethodDelete, "tok", "tok", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/v1/projects", nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: tc.cookie})
			}
			if tc.header != "" {
				req.Header.Set(HeaderCSRF, tc.header)
			}
			rec := httptest.NewRecorder()
			ok.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestMustUserPanicsWithoutUser(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustUser must panic on an unauthenticated context")
		}
	}()
	MustUser(context.Background())
}

func TestMustUserReturnsUser(t *testing.T) {
	u := &contracts.User{ID: "usr_9"}
	got := MustUser(WithUser(context.Background(), u))
	if got.ID != "usr_9" {
		t.Fatalf("user = %q", got.ID)
	}
	if MustUserID(WithUser(context.Background(), u)) != "usr_9" {
		t.Fatal("MustUserID mismatch")
	}
}

func TestSetCookiesFlags(t *testing.T) {
	a, _, _ := testAuthenticator(t, Config{Secure: true})
	rec := httptest.NewRecorder()
	a.SetCookies(rec, &Session{
		Token:     "raw-token",
		CSRFToken: "csrf-token",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies = %d, want 2", len(cookies))
	}
	byName := map[string]*http.Cookie{}
	for _, c := range cookies {
		byName[c.Name] = c
	}
	sessCookie, ok := byName[CookieSession]
	if !ok {
		t.Fatal("missing session cookie")
	}
	if !sessCookie.HttpOnly || !sessCookie.Secure || sessCookie.Path != "/" {
		t.Fatalf("session cookie flags: %+v", sessCookie)
	}
	if sessCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie SameSite = %v", sessCookie.SameSite)
	}
	csrfCookie, ok := byName[CookieCSRF]
	if !ok {
		t.Fatal("missing csrf cookie")
	}
	if csrfCookie.HttpOnly {
		t.Fatal("csrf cookie must be readable by the client")
	}
	if got := rec.Header().Get(HeaderCSRFResponse); got != "csrf-token" {
		t.Fatalf("X-PF-CSRF header = %q", got)
	}
}
