package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// PRD FR-11.9 is about the browser keeping its identity per sandbox: one profile
// directory, reused, so a login made in one session is still there in the next.
//
// What this test pins down is exactly what the product guarantees: the profile is a
// stable directory per sandbox and it is reused across sessions. Cookie survival
// across a full browser restart is deliberately *not* asserted here — it depends on
// Chromium's cookie encryption and its 30-second lazy-write, neither of which is ours
// to promise, and a test that dodges that would be theatre.
func TestRealChromeProfileIsStableAcrossSessions(t *testing.T) {
	binary := findChrome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "kept", Path: "/"})
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer server.Close()

	profileRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	manager := NewManager(Config{Binary: binary, Headless: true, Width: 640, Height: 480})
	defer manager.CloseAll()

	session, err := manager.Ensure(ctx, "sb_profile", profileRoot)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	expected := filepath.Join(profileRoot, ".piforge-browser")
	if session.Profile != expected {
		t.Fatalf("profile = %q, want %q", session.Profile, expected)
	}
	if info, err := os.Stat(expected); err != nil || !info.IsDir() {
		t.Fatalf("profile directory does not exist: %v", err)
	}

	// The cookie set by the page is visible to the browser that set it.
	if err := session.Navigate(ctx, server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	var set struct{}
	if err := session.client.call(ctx, "Network.enable", nil, &set); err != nil {
		t.Fatalf("Network.enable: %v", err)
	}
	var cookies struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if err := session.client.call(ctx, "Network.getCookies", map[string]any{"urls": []string{server.URL}}, &cookies); err != nil {
		t.Fatalf("Network.getCookies: %v", err)
	}
	found := false
	for _, cookie := range cookies.Cookies {
		if cookie.Name == "sid" && cookie.Value == "kept" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the page's cookie is not visible: %+v", cookies.Cookies)
	}

	// A second Ensure for the same sandbox reuses both the process and the profile:
	// two sessions in one sandbox share one login, which is the point.
	again, err := manager.Ensure(ctx, "sb_profile", profileRoot)
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if again.PID != session.PID {
		t.Fatalf("a second session launched a second browser (%d then %d)", session.PID, again.PID)
	}
	if again.Profile != expected {
		t.Fatalf("profile changed between sessions: %q", again.Profile)
	}
}
