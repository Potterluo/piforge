package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// TestComposeReviewPromptNamesFileLineAndQuote pins the shape of the message the
// agent receives for a line comment (FR-12.3): the file and the line, the code
// the user pointed at, the comment itself, and an honest note about where that
// code is now.
func TestComposeReviewPromptNamesFileLineAndQuote(t *testing.T) {
	file := strings.Split("package cache\n\nfunc Get(k string) any {\n\treturn cache[k]\n}\n", "\n")
	got, _, err := composeReviewPrompt(ReviewComment{
		Path:  "internal/cache/cache.go",
		Line:  4,
		Side:  "new",
		Hunk:  "@@ -1,4 +1,5 @@",
		Quote: []string{"\treturn cache[k]"},
		Body:  "这里没有处理未命中，应该返回 nil, false。",
	}, file, true)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	for _, want := range []string{
		"internal/cache/cache.go:4",
		"@@ -1,4 +1,5 @@",
		"    4 | \treturn cache[k]",
		"评论：这里没有处理未命中，应该返回 nil, false。",
		"这段代码现在仍在第 4 行。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("message is missing %q:\n%s", want, got)
		}
	}
	// The header must come first: the anchor is the part an agent reads before
	// anything else.
	if !strings.HasPrefix(got, "行内评论 · internal/cache/cache.go:4") {
		t.Fatalf("message does not start with the anchor:\n%s", got)
	}
}

// TestComposeReviewPromptWhenTheLineMoved is the "what happens when the file
// changes" decision: the quote is kept as quoted, and the note says where it is
// now. The agent gets both the anchor the user clicked and the current truth.
func TestComposeReviewPromptWhenTheLineMoved(t *testing.T) {
	file := strings.Split(`package x

// a comment that was added after the fact
// and one more line
func f() {
	return 1
}
`, "\n")
	got, _, err := composeReviewPrompt(ReviewComment{
		Path:  "x.go",
		Line:  2,
		Quote: []string{"func f() {"},
		Body:  "这个函数太长了。",
	}, file, true)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !strings.Contains(got, "x.go:2") {
		t.Fatalf("the anchor must stay at the clicked line:\n%s", got)
	}
	if !strings.Contains(got, "现在在第 5 行") {
		t.Fatalf("the note must name the new line:\n%s", got)
	}
	// And it must not claim the quote is still where it was.
	if strings.Contains(got, "仍在第 2 行") {
		t.Fatalf("the note claims the old line is still correct:\n%s", got)
	}
}

// TestComposeReviewPromptWhenTheCodeIsGone: the quote cannot be located, so the
// message says so instead of inventing a line.
func TestComposeReviewPromptWhenTheCodeIsGone(t *testing.T) {
	file := strings.Split("package x\n\nfunc g() {}\n", "\n")
	got, _, err := composeReviewPrompt(ReviewComment{
		Path:  "x.go",
		Line:  3,
		Quote: []string{"func f() {}"},
		Body:  "删掉这个。",
	}, file, true)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !strings.Contains(got, "找不到这段代码") {
		t.Fatalf("the message must say the quote is gone:\n%s", got)
	}
}

// TestComposeReviewPromptOnARemovedLine: a comment on a deletion is about code
// the new file does not contain, so the moved-line search must not run.
func TestComposeReviewPromptOnARemovedLine(t *testing.T) {
	file := strings.Split("package x\n\nfunc g() {}\n", "\n")
	got, _, err := composeReviewPrompt(ReviewComment{
		Path:  "x.go",
		Line:  2,
		Side:  "old",
		Quote: []string{"func f() {}"},
		Body:  "为什么删掉？",
	}, file, true)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !strings.Contains(got, "删除行") {
		t.Fatalf("a comment on a deleted line must say so:\n%s", got)
	}
}

// TestComposeReviewPromptWithoutTheFile: the workspace may be unavailable. The
// comment still goes out, and the message says the quote was not verified.
func TestComposeReviewPromptWithoutTheFile(t *testing.T) {
	got, _, err := composeReviewPrompt(ReviewComment{
		Path:  "x.go",
		Line:  1,
		Quote: []string{"package x"},
		Body:  "包的注释呢？",
	}, nil, false)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !strings.Contains(got, "引用来自 diff") {
		t.Fatalf("an unverified quote must be labelled:\n%s", got)
	}
}

// TestReviewCommentValidation: what the endpoint refuses. A path that walks out
// of the worktree is the one that matters — this text ends up in a prompt.
func TestReviewCommentValidation(t *testing.T) {
	cases := []struct {
		name string
		in   ReviewComment
	}{
		{"no path", ReviewComment{Line: 1, Body: "x"}},
		{"absolute path", ReviewComment{Path: "/etc/passwd", Line: 1, Body: "x"}},
		{"parent traversal", ReviewComment{Path: "a/../../b", Line: 1, Body: "x"}},
		{"empty body", ReviewComment{Path: "a.ts", Line: 1, Body: "   "}},
		{"zero line", ReviewComment{Path: "a.ts", Line: 0, Body: "x"}},
		{"bad side", ReviewComment{Path: "a.ts", Line: 1, Side: "middle", Body: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := composeReviewPrompt(tc.in, nil, false); err == nil {
				t.Fatalf("%+v was accepted", tc.in)
			}
		})
	}
}

// TestPostMessageWithReviewComment is the wiring test: the endpoint composes the
// comment into the prompt the runtime receives, and it does so through the same
// POST as a typed message.
func TestPostMessageWithReviewComment(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("reviewer@piforge.local", "reviewer-pass-123", "user")
	_, sess := env.mkSessionFor(user.ID, "review")
	c := env.login("reviewer@piforge.local", "reviewer-pass-123")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "review-1",
		"review_comment": map[string]any{
			"path":  "src/cache.ts",
			"line":  42,
			"side":  "new",
			"hunk":  "@@ -40,3 +40,4 @@",
			"quote": []string{"  return cache.get(key);"},
			"body":  "这里没有处理未命中。",
		},
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST messages: %d %s", rec.Code, rec.Body.String())
	}

	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	if len(env.runtime.prompts) != 1 {
		t.Fatalf("runtime received %d prompts, want 1", len(env.runtime.prompts))
	}
	prompt := env.runtime.prompts[0].Text
	for _, want := range []string{
		"src/cache.ts:42",
		"  42 |   return cache.get(key);",
		"评论：这里没有处理未命中。",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// TestComposeReviewPromptCapsTruncateInsteadOfRefusing pins the shape of the
// caps: they are budgets for the composed message, not validation. A comment
// with too many quoted lines, a huge line or a huge body is still sent — it is
// cut, visibly, and never answered with a 400. (docs/HANDOFF.md's API shapes
// says the same thing; without this test the next reader may reintroduce a
// refusal "for safety".)
func TestComposeReviewPromptCapsTruncateInsteadOfRefusing(t *testing.T) {
	longLine := strings.Repeat("y", 500)
	// The long line is inside the 24-line window on purpose: a line past it is
	// dropped before the per-line cap is ever applied.
	quote := []string{longLine}
	for i := 2; i <= 30; i++ {
		quote = append(quote, fmt.Sprintf("line %d", i))
	}
	body := strings.Repeat("z", 5000)

	got, cut, err := composeReviewPrompt(ReviewComment{
		Path:    "notes.txt",
		Line:    3,
		EndLine: 99, // past the end of the file: accepted, it is only a header
		Quote:   quote,
		Body:    body,
	}, nil, false)
	if err != nil {
		t.Fatalf("the caps must not refuse the comment: %v", err)
	}
	// The report is the point of this test: the caller is told, with exact
	// counts, what the caps took. A cut that only shows up as a marker inside
	// the text is the defect this replaced.
	if cut == nil {
		t.Fatalf("the caps cut the comment without reporting it:\n%s", got)
	}
	want := contracts.ReviewTruncation{
		QuoteLinesKept:      24,
		QuoteLinesOmitted:   6,
		QuoteLinesShortened: 1,
		QuoteLineRunesCut:   100,
		BodyRunesCut:        1000,
	}
	if *cut != want {
		t.Fatalf("truncation = %+v, want %+v", *cut, want)
	}
	// The same counts are in the message, because that string is the transcript
	// entry: the record has to show what actually reached the agent.
	for _, want := range []string{
		"注：原始评论引用了 30 行，仅前 24 行送达；",
		"第 3 行引用超过 400 字，已截断 100 字；",
		"正文超出 4000 字，已截断 1000 字。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the composed block is missing %q:\n%s", want, got)
		}
	}

	// The header keeps the anchor the caller sent, even past EOF.
	if !strings.Contains(got, "行内评论 · notes.txt:3-99") {
		t.Fatalf("the end_line was not used in the header:\n%s", got)
	}
	// 24 lines make it; the rest are replaced by one visible marker.
	if !strings.Contains(got, "…（其余行已省略）") {
		t.Fatalf("the truncated quote is not marked:\n%s", got)
	}
	if strings.Contains(got, "line 25") || strings.Contains(got, "line 30") {
		t.Fatalf("a quote line past the cap was kept:\n%s", got)
	}
	if !strings.Contains(got, "line 24") {
		t.Fatalf("the 24th quote line was dropped:\n%s", got)
	}
	// Each line stops at 400 runes plus an ellipsis; the body at 4000.
	if !strings.Contains(got, strings.Repeat("y", 400)+"…") {
		t.Fatalf("the long quote line was not cut at 400 runes")
	}
	if strings.Contains(got, strings.Repeat("y", 401)) {
		t.Fatalf("the long quote line kept more than 400 runes")
	}
	if !strings.Contains(got, strings.Repeat("z", 4000)+"…") {
		t.Fatalf("the body was not cut at 4000 runes")
	}
	if strings.Contains(got, strings.Repeat("z", 4001)) {
		t.Fatalf("the body kept more than 4000 runes")
	}
}

// TestComposeReviewPromptSaysWhatItCut: the caps are budgets, so the composed
// message — which is also the transcript entry and the text the agent receives —
// must end by saying what did not fit. A reviewer who selected 30 lines has to
// be able to read, in the record, that only 24 arrived.
func TestComposeReviewPromptSaysWhatItCut(t *testing.T) {
	quote := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		quote = append(quote, fmt.Sprintf("line %d", i))
	}
	got, cut, err := composeReviewPrompt(ReviewComment{
		Path:  "notes.txt",
		Line:  3,
		Quote: quote,
		Body:  strings.Repeat("z", 5000),
	}, nil, false)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if cut == nil {
		t.Fatalf("compose did not report the truncation:\n%s", got)
	}
	want := "注：原始评论引用了 30 行，仅前 24 行送达；正文超出 4000 字，已截断 1000 字。"
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), want) {
		t.Fatalf("the composed block does not end with %q:\n%s", want, got)
	}
}

// TestComposeReviewPromptWithinLimitsSaysNothing: a comment that fits must not
// grow a note. An always-on warning is noise, and noise is how a real one gets
// ignored.
func TestComposeReviewPromptWithinLimitsSaysNothing(t *testing.T) {
	got, cut, err := composeReviewPrompt(ReviewComment{
		Path:  "notes.txt",
		Line:  2,
		Quote: []string{"line 2", "line 3"},
		Body:  "这一行不要动。",
	}, nil, false)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if cut != nil {
		t.Fatalf("a comment within the caps reported %+v", *cut)
	}
	if strings.Contains(got, "注：") {
		t.Fatalf("a comment within the caps must not carry a truncation note:\n%s", got)
	}
}

// TestPostMessageWithAReviewCommentReportsTruncation is the API half: the
// caller is told what was cut, with exact counts, in the response — not left to
// discover it by reading the composed text. The prompt the runtime received is
// asserted too, because that string is the transcript entry.
func TestPostMessageWithAReviewCommentReportsTruncation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("reviewer3@piforge.local", "reviewer-pass-123", "user")
	_, sess := env.mkSessionFor(user.ID, "review3")
	c := env.login("reviewer3@piforge.local", "reviewer-pass-123")

	quote := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		quote = append(quote, fmt.Sprintf("line %d", i))
	}
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "review-truncated",
		"review_comment": map[string]any{
			"path":  "notes.txt",
			"line":  3,
			"quote": quote,
			"body":  strings.Repeat("z", 5000),
		},
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST messages: %d %s", rec.Code, rec.Body.String())
	}

	type report struct {
		QuoteLinesKept      int `json:"quote_lines_kept"`
		QuoteLinesOmitted   int `json:"quote_lines_omitted"`
		QuoteLinesShortened int `json:"quote_lines_shortened"`
		QuoteLineRunesCut   int `json:"quote_line_runes_cut"`
		BodyRunesCut        int `json:"body_runes_cut"`
	}
	var got struct {
		ReviewTruncation *report `json:"review_truncation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ReviewTruncation == nil {
		t.Fatalf("the response did not report the truncation: %s", rec.Body.String())
	}
	if want := (report{24, 6, 0, 0, 1000}); *got.ReviewTruncation != want {
		t.Fatalf("review_truncation = %+v, want %+v", *got.ReviewTruncation, want)
	}

	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	if len(env.runtime.prompts) != 1 {
		t.Fatalf("runtime received %d prompts, want 1", len(env.runtime.prompts))
	}
	prompt := env.runtime.prompts[0].Text
	if !strings.Contains(prompt, "仅前 24 行送达") || !strings.Contains(prompt, "已截断 1000 字") {
		t.Fatalf("the transcript text does not say what was cut:\n%s", prompt)
	}
}

// TestPostMessageWithAReviewCommentWithinLimitsReportsNothing: the shape is
// absent, not zeroed, when nothing was cut — a client that renders on presence
// must not show "0 lines omitted".
func TestPostMessageWithAReviewCommentWithinLimitsReportsNothing(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("reviewer4@piforge.local", "reviewer-pass-123", "user")
	_, sess := env.mkSessionFor(user.ID, "review4")
	c := env.login("reviewer4@piforge.local", "reviewer-pass-123")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "review-small",
		"review_comment": map[string]any{
			"path":  "notes.txt",
			"line":  2,
			"quote": []string{"line 2"},
			"body":  "这一行不要动。",
		},
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST messages: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "review_truncation") {
		t.Fatalf("a comment within the caps reported a truncation: %s", rec.Body.String())
	}
}

// TestPostMessageWithAReviewCommentRejectsABadAnchor: the same validation runs
// through the endpoint, so a malformed comment is a 400 and never reaches the
// agent.
func TestPostMessageWithAReviewCommentRejectsABadAnchor(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("reviewer2@piforge.local", "reviewer-pass-123", "user")
	_, sess := env.mkSessionFor(user.ID, "review2")
	c := env.login("reviewer2@piforge.local", "reviewer-pass-123")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id":  "review-2",
		"review_comment": map[string]any{"path": "/etc/passwd", "line": 1, "body": "x"},
	}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	if len(env.runtime.prompts) != 0 {
		t.Fatalf("a refused comment still reached the agent: %+v", env.runtime.prompts)
	}
}
