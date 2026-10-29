package browser

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
)

// ErrBlankCapture means the page produced an image with nothing on it, or was
// not on a page at all. It is a typed error so the HTTP layer can explain it
// instead of reporting "the browser is busy", and so no caller can mistake a
// white rectangle for a screenshot.
//
// The defect this exists for: with the panel showing a page, the endpoint wrote
// a 661×750 all-white 3008-byte PNG of `about:blank` into the checkout and
// answered 201. The agent then "read" the screenshot and saw nothing. A blank
// capture is not a screenshot and must never reach the workspace.
var ErrBlankCapture = errors.New("browser: the captured page is blank")

// BlankCaptureError carries the measurements that made a capture blank, so the
// message can name the page and the reason rather than guessing.
type BlankCaptureError struct {
	// URL is the page's address, empty when it could not be read.
	URL string
	// Width and Height are the decoded image size; both are zero when the bytes
	// did not decode as a PNG at all.
	Width, Height int
	// Colours is how many distinct RGB values the image had (capped at the
	// counting limit), Dominant the share of the most common one.
	Colours  int
	Dominant float64
	// Reason is a short human sentence: "the page is about:blank", "the image is
	// one colour", ...
	Reason string
}

func (e *BlankCaptureError) Error() string {
	if e.Width == 0 || e.Height == 0 {
		return fmt.Sprintf("browser: refusing a blank capture: %s", e.Reason)
	}
	return fmt.Sprintf(
		"browser: refusing a blank capture: %s (%dx%d, %d colour(s), %.1f%% one colour)",
		e.Reason, e.Width, e.Height, e.Colours, e.Dominant*100,
	)
}

// Unwrap is what makes errors.Is(err, ErrBlankCapture) work for every blank
// capture, whatever produced it.
func (e *BlankCaptureError) Unwrap() error { return ErrBlankCapture }

const (
	// blankColours is the most distinct RGB values an image may have and still
	// be called blank. A white page is exactly one; a page with a single dot is
	// two. Real pages cross this immediately through antialiasing.
	blankColours = 2
	// blankDominant is the share of one colour above which the image is a flat
	// rectangle. 99.95% leaves room for a page that is almost entirely white but
	// has a line of text on it.
	blankDominant = 0.9995
	// measureLimit caps the colour table so a photograph-sized screenshot cannot
	// grow it without bound. Nothing near this number is blank.
	measureLimit = 4096
)

// inspectCapture rejects an about:blank page and an image with no content.
//
// The URL check is first because it names the cause exactly, and because Chrome
// in headless mode can hand back a perfectly uniform surface for a page that was
// never painted — the case that produced the all-white PNG.
//
// An unknown URL ("" from a browser whose history could not be read) is not
// treated as about:blank: rejecting a real page because one metadata call failed
// would be worse than trusting the pixels, which are still inspected.
func inspectCapture(raw []byte, pageURL string) error {
	if pageURL == "about:blank" {
		return &BlankCaptureError{
			URL:    pageURL,
			Reason: "the page is about:blank",
		}
	}
	facts, err := measurePNG(raw)
	if err != nil {
		// A screenshot that is not a PNG cannot be inspected, and writing it
		// would be worse than failing: the file would look like a screenshot.
		return fmt.Errorf("browser: the screenshot is not a PNG: %w", err)
	}
	if facts.Colours <= blankColours || facts.Dominant >= blankDominant {
		return &BlankCaptureError{
			URL:      pageURL,
			Width:    facts.Width,
			Height:   facts.Height,
			Colours:  facts.Colours,
			Dominant: facts.Dominant,
			Reason:   "the page has not painted anything",
		}
	}
	return nil
}

// pngFacts is what the blank check measures.
type pngFacts struct {
	Width, Height int
	Colours       int
	Dominant      float64
}

// measurePNG decodes the image and counts distinct RGB values and the share of
// the most common one. This is the "decode and inspect" rule: a byte-size
// threshold cannot tell a white rectangle from a page.
func measurePNG(raw []byte) (pngFacts, error) {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return pngFacts{}, err
	}
	bounds := img.Bounds()
	facts := pngFacts{Width: bounds.Dx(), Height: bounds.Dy()}
	if facts.Width <= 0 || facts.Height <= 0 {
		return pngFacts{}, errors.New("the image is empty")
	}

	counts := make(map[uint32]int, 64)
	top := 0
	total := 0
	// A PNG decodes to one of these concrete types; reading Pix is the
	// difference between a few milliseconds and a few hundred on a full
	// screenshot. Anything else goes through At, which is correct if slower.
	read := func(x, y int) uint32 {
		r, g, b, _ := img.At(x, y).RGBA()
		return uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(b>>8)
	}
	var pixel func(x, y int) uint32 = read
	if rgba, ok := img.(*image.RGBA); ok {
		pixel = func(x, y int) uint32 {
			i := rgba.PixOffset(x, y)
			return uint32(rgba.Pix[i])<<16 | uint32(rgba.Pix[i+1])<<8 | uint32(rgba.Pix[i+2])
		}
	} else if nrgba, ok := img.(*image.NRGBA); ok {
		pixel = func(x, y int) uint32 {
			i := nrgba.PixOffset(x, y)
			return uint32(nrgba.Pix[i])<<16 | uint32(nrgba.Pix[i+1])<<8 | uint32(nrgba.Pix[i+2])
		}
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			key := pixel(x, y)
			n := counts[key] + 1
			counts[key] = n
			if n > top {
				top = n
			}
			total++
			if len(counts) > measureLimit {
				// Far past anything blank: every further pixel only confirms it.
				facts.Colours = len(counts)
				return facts, nil
			}
		}
	}
	facts.Colours = len(counts)
	if total > 0 {
		facts.Dominant = float64(top) / float64(total)
	}
	return facts, nil
}
