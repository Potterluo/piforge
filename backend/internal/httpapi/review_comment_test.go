package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestComposeReviewPromptNamesFileLineAndQuote pins the shape of the message the
// agent receives for a line comment (FR-12.3): the file and the line, the code
// the user pointed at, the comment itself, and an honest note about where that
// code is now.
func TestComposeReviewPromptNamesFileLineAndQuote(t *testing.T) {
	file := strings.Split("package cache\n\nfunc Get(k string) any {\n\treturn cache[k]\n}\n", "\n")
	got, err := composeReviewPrompt(ReviewComment{
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
	got, err := composeReviewPrompt(ReviewComment{
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
	got, err := composeReviewPrompt(ReviewComment{
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
	got, err := composeReviewPrompt(ReviewComment{
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
	got, err := composeReviewPrompt(ReviewComment{
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
			if _, err := composeReviewPrompt(tc.in, nil, false); err == nil {
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
