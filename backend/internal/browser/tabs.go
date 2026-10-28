package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Tabs are separate pages inside one sandbox's Chrome (PRD FR-11.7).
//
// One browser per sandbox already existed; what a person needs on top of that is
// more than one page — a docs page next to the app, a second account's session,
// two pages to compare. Each tab is a Chrome target with its own devtools
// connection, so a screencast on one does not disturb the other.

// TabInfo describes one page.
type TabInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// tabsLocked guards the per-tab clients. The session already has a mutex for
// stop state, but tab clients are created on demand from request goroutines.
func (s *Session) tabClient(ctx context.Context, tabID string) (*cdp, error) {
	if tabID == "" || tabID == s.DefaultTabID() {
		return s.client, nil
	}
	s.tabMu.Lock()
	if client, ok := s.tabs[tabID]; ok {
		s.tabMu.Unlock()
		return client, nil
	}
	s.tabMu.Unlock()

	wsURL, err := s.targetWSURL(ctx, tabID)
	if err != nil {
		return nil, err
	}
	client, err := dialCDP(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	// A direct page connection speaks Page/Input without a flatten session.
	client.sessionID = ""

	s.tabMu.Lock()
	if existing, ok := s.tabs[tabID]; ok {
		s.tabMu.Unlock()
		client.close()
		return existing, nil
	}
	s.tabs[tabID] = client
	s.tabMu.Unlock()
	return client, nil
}

// DefaultTabID is the tab the session started with.
func (s *Session) DefaultTabID() string {
	s.tabMu.Lock()
	defer s.tabMu.Unlock()
	return s.defaultTab
}

// targetWSURL asks Chrome where a target's own devtools socket is.
func (s *Session) targetWSURL(ctx context.Context, tabID string) (string, error) {
	if s.debugHTTP == "" {
		return "", errors.New("browser: this session has no devtools http endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.debugHTTP+"/json/list", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("browser: list targets: %w", err)
	}
	defer resp.Body.Close()
	var targets []struct {
		ID                   string `json:"id"`
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		Title                string `json:"title"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return "", fmt.Errorf("browser: decode targets: %w", err)
	}
	for _, target := range targets {
		if target.ID == tabID && target.WebSocketDebuggerURL != "" {
			return target.WebSocketDebuggerURL, nil
		}
	}
	return "", fmt.Errorf("browser: no devtools socket for tab %q", tabID)
}

// Tabs lists the pages of this sandbox's browser.
func (s *Session) Tabs(ctx context.Context) ([]TabInfo, error) {
	var out struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
			Title    string `json:"title"`
		} `json:"targetInfos"`
	}
	if err := s.client.call(ctx, "Target.getTargets", nil, &out); err != nil {
		return nil, fmt.Errorf("browser: list tabs: %w", err)
	}
	tabs := make([]TabInfo, 0, len(out.TargetInfos))
	for _, target := range out.TargetInfos {
		if target.Type != "page" {
			continue
		}
		tabs = append(tabs, TabInfo{ID: target.TargetID, URL: target.URL, Title: target.Title})
	}
	return tabs, nil
}

// OpenTab creates a page and returns it.
func (s *Session) OpenTab(ctx context.Context, url string) (TabInfo, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		url = "about:blank"
	}
	// Same rule as Navigate: no file:// or javascript: from the API.
	lower := strings.ToLower(url)
	if url != "about:blank" && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return TabInfo{}, fmt.Errorf("browser: %q is not an http(s) url", url)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := s.client.call(ctx, "Target.createTarget", map[string]any{"url": url}, &created); err != nil {
		return TabInfo{}, fmt.Errorf("browser: open tab: %w", err)
	}
	if created.TargetID == "" {
		return TabInfo{}, errors.New("browser: chrome did not return a target id")
	}
	return TabInfo{ID: created.TargetID, URL: url}, nil
}

// CloseTab closes one page. Closing the last page is refused: a browser with no
// page cannot be navigated back to.
func (s *Session) CloseTab(ctx context.Context, tabID string) error {
	tabs, err := s.Tabs(ctx)
	if err != nil {
		return err
	}
	if len(tabs) <= 1 {
		return errors.New("browser: refusing to close the last tab")
	}
	var ignored struct{}
	if err := s.client.call(ctx, "Target.closeTarget", map[string]any{"targetId": tabID}, &ignored); err != nil {
		return fmt.Errorf("browser: close tab: %w", err)
	}
	s.tabMu.Lock()
	if client, ok := s.tabs[tabID]; ok {
		delete(s.tabs, tabID)
		client.close()
	}
	s.tabMu.Unlock()
	return nil
}

// tabScoped runs `fn` against one tab's connection.
func (s *Session) tabScoped(ctx context.Context, tabID string, fn func(*cdp) error) error {
	client, err := s.tabClient(ctx, tabID)
	if err != nil {
		return err
	}
	return fn(client)
}

// closeTabs drops every per-tab connection. Called from Stop.
func (s *Session) closeTabs() {
	s.tabMu.Lock()
	defer s.tabMu.Unlock()
	for id, client := range s.tabs {
		client.close()
		delete(s.tabs, id)
	}
}

// TabScreencast streams one tab's frames.
func (s *Session) TabScreencast(ctx context.Context, tabID string, onFrame func(jpeg []byte, meta Meta)) error {
	var err error
	_ = s.tabScoped(ctx, tabID, func(client *cdp) error {
		err = s.screencastOn(ctx, client, onFrame)
		return err
	})
	return err
}

// tabDispatch sends one input event to a tab.
func (s *Session) TabDispatch(ctx context.Context, tabID string, in Input) error {
	return s.tabScoped(ctx, tabID, func(client *cdp) error { return s.dispatchOn(ctx, client, in) })
}

// TabNavigate points one tab at a URL.
func (s *Session) TabNavigate(ctx context.Context, tabID, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return errors.New("browser: url is required")
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("browser: %q is not an http(s) url", url)
	}
	return s.tabScoped(ctx, tabID, func(client *cdp) error {
		return client.call(ctx, "Page.navigate", map[string]any{"url": url}, nil)
	})
}

// TabResize sets one tab's viewport.
func (s *Session) TabResize(ctx context.Context, tabID string, width, height int) error {
	if width < 120 || height < 120 {
		return fmt.Errorf("browser: refusing to resize to %dx%d", width, height)
	}
	if width > 2560 {
		width = 2560
	}
	if height > 1600 {
		height = 1600
	}
	return s.tabScoped(ctx, tabID, func(client *cdp) error {
		return client.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
			"width":             width,
			"height":            height,
			"deviceScaleFactor": 1,
			"mobile":            false,
		}, nil)
	})
}

// TabScreenshot captures one tab.
func (s *Session) TabScreenshot(ctx context.Context, tabID string) ([]byte, error) {
	var png []byte
	err := s.tabScoped(ctx, tabID, func(client *cdp) error {
		out, err := captureOn(ctx, client)
		if err != nil {
			return err
		}
		png = out
		return nil
	})
	return png, err
}
