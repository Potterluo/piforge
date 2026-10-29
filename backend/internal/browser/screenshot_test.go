package browser

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

// pngFixture builds a PNG from a drawing function, so a test can say exactly
// what colours the "page" had.
func pngFixture(t *testing.T, width, height int, draw func(x, y int) color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, draw(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

// blankPagePNG is the screenshot this defect was found with: one colour, the
// dimensions of the panel, and small enough that a `bytes > 1000` assertion
// called it a screenshot.
func blankPagePNG(t *testing.T) []byte {
	t.Helper()
	return pngFixture(t, 661, 750, func(int, int) color.RGBA {
		return color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	})
}

// paintedPagePNG has three colours, like any page with text on it.
func paintedPagePNG(t *testing.T) []byte {
	t.Helper()
	return pngFixture(t, 200, 120, func(x, y int) color.RGBA {
		switch {
		case x < 80 && y < 60:
			return color.RGBA{R: 0xcc, G: 0x22, B: 0x22, A: 0xff}
		case x >= 80 && y >= 60:
			return color.RGBA{R: 0x11, G: 0x22, B: 0xcc, A: 0xff}
		default:
			return color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
		}
	})
}

func newScreenshotSession(t *testing.T, ctx context.Context) (*fakeCDP, *Session) {
	t.Helper()
	fake := newFakeCDP(t)
	client, err := dialCDP(ctx, fake.wsURL, nil)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	t.Cleanup(client.close)
	return fake, &Session{
		client:     client,
		stopped:    make(chan struct{}),
		tabs:       map[string]*cdp{},
		defaultTab: "page-default",
	}
}

// FR-11.8: a capture of about:blank is not a screenshot. Before this check the
// endpoint answered 201 and wrote a 661×750 all-white 3008-byte PNG into the
// checkout, which the agent then "read" as the page.
func TestScreenshotRejectsAboutBlank(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake, session := newScreenshotSession(t, ctx)
	fake.mu.Lock()
	fake.pageURL = "about:blank"
	fake.capturePNG = blankPagePNG(t)
	fake.mu.Unlock()

	_, err := session.Screenshot(ctx)
	if err == nil {
		t.Fatal("an about:blank capture was accepted as a screenshot")
	}
	if !errors.Is(err, ErrBlankCapture) {
		t.Fatalf("error = %v, want the typed ErrBlankCapture", err)
	}
	var blank *BlankCaptureError
	if !errors.As(err, &blank) {
		t.Fatalf("error %v does not carry the measurements", err)
	}
	if blank.URL != "about:blank" {
		t.Fatalf("error names the page as %q", blank.URL)
	}
	if blank.Width != 0 || blank.Height != 0 {
		// The URL check answers before the image is measured; either order is
		// fine, but the message must not claim dimensions it did not use.
		t.Logf("measured %dx%d before the URL check", blank.Width, blank.Height)
	}
}

// A white image is blank even when the page claims to be a real URL: this is
// the "captured the wrong target" case, where the URL and the pixels disagree.
func TestScreenshotRejectsAFlatImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake, session := newScreenshotSession(t, ctx)
	fake.mu.Lock()
	fake.pageURL = "http://127.0.0.1:7700/login"
	fake.capturePNG = blankPagePNG(t)
	fake.mu.Unlock()

	_, err := session.Screenshot(ctx)
	if err == nil {
		t.Fatal("a one-colour image was accepted as a screenshot")
	}
	if !errors.Is(err, ErrBlankCapture) {
		t.Fatalf("error = %v, want the typed ErrBlankCapture", err)
	}
	var blank *BlankCaptureError
	if !errors.As(err, &blank) {
		t.Fatalf("error %v does not carry the measurements", err)
	}
	if blank.Width != 661 || blank.Height != 750 {
		t.Fatalf("measured %dx%d, want 661x750", blank.Width, blank.Height)
	}
	if blank.Colours != 1 {
		t.Fatalf("colours = %d, want 1", blank.Colours)
	}
	if blank.Dominant < 0.99 {
		t.Fatalf("dominant = %v, want ~1", blank.Dominant)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("blank")) {
		t.Fatalf("message does not say what happened: %v", err)
	}
}

// TestTwoCapturesInARowSucceed is the sequence the panel performs: capture,
// then capture again, while the screencast streams. The second one used to come
// back 422 "浏览器正忙" because the endpoint captured a background page — this
// fake models that Chromium: an unactivated page does not answer. The fix brings
// the page to the front first, so both captures succeed.
func TestTwoCapturesInARowSucceed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fake, session := newScreenshotSession(t, ctx)
	fake.mu.Lock()
	fake.pageURL = "http://127.0.0.1:7700/login"
	fake.capturePNG = paintedPagePNG(t)
	fake.requireActivate = true
	fake.mu.Unlock()

	// The panel is open: a screencast runs on the same connection while the
	// captures happen, which is the state the 422 appeared in.
	castCtx, castCancel := context.WithCancel(ctx)
	defer castCancel()
	go func() {
		_ = session.Screencast(castCtx, func([]byte, Meta) {})
	}()
	waitForMethod(t, fake, "Page.startScreencast")

	for i := 0; i < 3; i++ {
		png, err := session.Screenshot(ctx)
		if err != nil {
			t.Fatalf("capture %d failed: %v", i+1, err)
		}
		if len(png) < 100 {
			t.Fatalf("capture %d is %d bytes", i+1, len(png))
		}
		if !bytes.Equal(png, fake.capturePNG) {
			t.Fatalf("capture %d did not return the page's PNG", i+1)
		}
		if i == 0 && !fake.has("Target.activateTarget") {
			t.Fatal("the page was captured without being brought to the front")
		}
	}

	fake.mu.Lock()
	captures := fake.captures
	fake.mu.Unlock()
	if captures != 3 {
		t.Fatalf("the page was captured %d times, want 3", captures)
	}
}

// The URL is read from the page CDP is attached to, not assumed: a capture that
// cannot say which page it is must not silently pass the URL half of the check.
func TestScreenshotSucceedsOnARealPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake, session := newScreenshotSession(t, ctx)
	fake.mu.Lock()
	fake.pageURL = "http://127.0.0.1:7700/login"
	fake.capturePNG = paintedPagePNG(t)
	fake.mu.Unlock()

	png, err := session.Screenshot(ctx)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(png) == 0 {
		t.Fatal("no bytes")
	}
	// Activating the page keeps a background target from hanging the capture;
	// it must have happened before Page.captureScreenshot.
	if !fake.has("Target.activateTarget") {
		t.Fatal("the page was not activated before the capture")
	}
}

// A capture that is not a PNG at all is an error, not a file: writing it would
// produce something that looks like a screenshot to every later reader.
func TestScreenshotRejectsNonPNGBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake, session := newScreenshotSession(t, ctx)
	fake.mu.Lock()
	fake.pageURL = "http://127.0.0.1:7700/login"
	fake.capturePNG = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	fake.mu.Unlock()

	if _, err := session.Screenshot(ctx); err == nil {
		t.Fatal("a JPEG was accepted where a PNG is required")
	}
}
