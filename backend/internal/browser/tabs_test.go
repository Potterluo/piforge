package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// PRD FR-11.7: more than one page per sandbox. What has to hold is that the tabs are
// independent — opening a second one must not disturb the first, and closing one must
// not take the browser with it.
func TestRealChromeTabsAreIndependent(t *testing.T) {
	binary := findChrome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h1>" + r.URL.Path + "</h1></body></html>"))
	}))
	defer server.Close()

	manager := NewManager(Config{Binary: binary, Headless: true, Width: 640, Height: 480})
	defer manager.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	session, err := manager.Ensure(ctx, "sb_tabs", t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	before, err := session.Tabs(ctx)
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("a fresh browser has %d tabs, want 1", len(before))
	}

	second, err := session.OpenTab(ctx, server.URL+"/second")
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	opened, err := session.Tabs(ctx)
	if err != nil {
		t.Fatalf("Tabs after open: %v", err)
	}
	if len(opened) != 2 {
		t.Fatalf("after opening one tab there are %d, want 2", len(opened))
	}

	// The new tab is a real page: navigating it changes its own URL only.
	if err := session.TabNavigate(ctx, second.ID, server.URL+"/second-page"); err != nil {
		t.Fatalf("TabNavigate: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var url string
	for time.Now().Before(deadline) {
		tabs, terr := session.Tabs(ctx)
		if terr == nil {
			for _, tab := range tabs {
				if tab.ID == second.ID {
					url = tab.URL
				}
			}
		}
		if url == server.URL+"/second-page" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if url != server.URL+"/second-page" {
		t.Fatalf("second tab url = %q, want the navigated page", url)
	}

	// A screenshot works per tab.
	png, err := session.TabScreenshot(ctx, second.ID)
	if err != nil {
		t.Fatalf("TabScreenshot: %v", err)
	}
	if len(png) < 1000 {
		t.Fatalf("screenshot is %d bytes", len(png))
	}

	// Closing it leaves the original alone.
	if err := session.CloseTab(ctx, second.ID); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	after, err := session.Tabs(ctx)
	if err != nil {
		t.Fatalf("Tabs after close: %v", err)
	}
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("after closing the second tab: %+v", after)
	}
}
