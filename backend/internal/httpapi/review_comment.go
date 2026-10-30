package httpapi

// FR-12.3: a comment on a diff line (or a file-preview line) becomes a steering
// message for the agent.
//
// The message text is composed here, not in the browser, for one reason: the
// line number in a comment is a claim about a file that can change while the
// comment waits in the draft store. The server is the only side that can check
// the claim against the file the agent will actually see, so it does, and the
// message says what it found. A comment that quietly points at the wrong line is
// worse than no comment.
//
// What happens when the line moved (decided here, documented in docs/STATUS.md):
//
//   - quoted code still at the same line  -> "现在仍在第 N 行", nothing else;
//   - quoted code found at another line   -> the note names the new line, so the
//     agent knows the offset without guessing;
//   - quoted code not found at all        -> the note says so: the file changed
//     in a way that cannot be mapped, and the diff is the authority;
//   - the line was a deletion in the diff -> the note says the new file does not
//     contain it;
//   - the file cannot be read             -> the note says the quote came from
//     the diff, so the agent does not treat the numbers as verified.

import (
	"context"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// ReviewComment is one anchored comment. It travels inside the ordinary message
// POST, so a comment is sent through the same path as a typed prompt (one
// message mechanism, not a second queue).
type ReviewComment struct {
	// Path is the file path relative to the worktree root, exactly as the diff
	// or the file tree shows it.
	Path string `json:"path"`
	// Line is the 1-based line number inside the file (the new side for an
	// added or context line, the old side for a removed one).
	Line int `json:"line"`
	// EndLine is the last quoted line; 0 means "same as Line".
	EndLine int `json:"end_line,omitempty"`
	// Side is "new" (added or context line) or "old" (a removed line).
	Side string `json:"side,omitempty"`
	// Hunk is the diff hunk header the line came from, if any.
	Hunk string `json:"hunk,omitempty"`
	// Quote is the code the user pointed at, without the diff's +/- marker.
	Quote []string `json:"quote,omitempty"`
	// Body is what the user wrote.
	Body string `json:"body"`
}

// Bounds that keep a comment from becoming a paste of the whole file. They are
// generous: a comment on a 12-line function is a normal comment.
const (
	maxReviewQuoteLines = 24
	maxReviewQuoteRune  = 400
	maxReviewBodyRune   = 4000
)

// quoteLocation is the result of checking the stored line against the file.
type quoteLocation int

const (
	// locationUnchecked means the file could not be read.
	locationUnchecked quoteLocation = iota
	// locationExact means the quoted code is still at the same line.
	locationExact
	// locationMoved means the code was found at a different line.
	locationMoved
	// locationMissing means the code is gone.
	locationMissing
	// locationDeleted means the comment was on a removed line.
	locationDeleted
)

// validateReviewComment rejects what cannot be composed into an honest message.
//
// The path check is the same rule the file API uses: a comment may only name a
// path inside the worktree, never an absolute path or one that walks out of it.
func validateReviewComment(in ReviewComment) error {
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return apiErr(contracts.ErrInvalidArgument, "review_comment.path is required")
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) ||
		strings.Contains(path, "..") || strings.ContainsRune(path, ':') {
		return apiErr(contracts.ErrInvalidArgument, "review_comment.path must be a relative path inside the worktree")
	}
	if in.Line <= 0 {
		return apiErr(contracts.ErrInvalidArgument, "review_comment.line must be a positive line number")
	}
	if strings.TrimSpace(in.Body) == "" {
		return apiErr(contracts.ErrInvalidArgument, "review_comment.body is required")
	}
	switch in.Side {
	case "", "new", "old":
	default:
		return apiErr(contracts.ErrInvalidArgument, `review_comment.side must be "new" or "old"`)
	}
	return nil
}

// findQuote locates the quoted lines in the current file content.
//
// The search is exact-line matching, first at the recorded line (the common
// case: nothing moved), then anywhere else. A partial match is deliberately not
// attempted: "line 42 now contains something vaguely similar" is not knowledge
// the agent can act on.
func findQuote(fileLines []string, line int, quote []string) (quoteLocation, int) {
	if len(quote) == 0 || len(fileLines) == 0 {
		return locationMissing, 0
	}
	matches := func(at int) bool {
		if at < 1 || at+len(quote)-1 > len(fileLines) {
			return false
		}
		for i, want := range quote {
			if strings.TrimRight(fileLines[at-1+i], " \t") != strings.TrimRight(want, " \t") {
				return false
			}
		}
		return true
	}
	if matches(line) {
		return locationExact, line
	}
	for at := 1; at+len(quote)-1 <= len(fileLines); at++ {
		if at == line {
			continue
		}
		if matches(at) {
			return locationMoved, at
		}
	}
	return locationMissing, 0
}

// composeReviewPrompt builds the message the agent receives.
//
// fileLines is the current content of the path (nil when it could not be read,
// which readable reports). The result is plain text on purpose: it is shown in
// the transcript as the user's own message, and a transcript that renders
// literal markdown fences around a quote reads worse than an indented one.
func composeReviewPrompt(in ReviewComment, fileLines []string, readable bool) (string, error) {
	if err := validateReviewComment(in); err != nil {
		return "", err
	}
	path := strings.TrimSpace(in.Path)
	quote := in.Quote
	if len(quote) > maxReviewQuoteLines {
		quote = append(append([]string{}, quote[:maxReviewQuoteLines]...), "…（其余行已省略）")
	}
	// The header always carries path:line: it is the anchor the agent needs even
	// if every note below is missing.
	var b strings.Builder
	end := in.EndLine
	if end < in.Line {
		end = in.Line
	}
	if end == in.Line {
		fmt.Fprintf(&b, "行内评论 · %s:%d\n", path, in.Line)
	} else {
		fmt.Fprintf(&b, "行内评论 · %s:%d-%d\n", path, in.Line, end)
	}
	if strings.TrimSpace(in.Hunk) != "" {
		fmt.Fprintf(&b, "diff 片段：%s\n", strings.TrimSpace(in.Hunk))
	}
	if len(quote) > 0 {
		b.WriteString("引用的代码：\n")
		for i, line := range quote {
			line = strings.TrimRight(line, "\r\n")
			if len([]rune(line)) > maxReviewQuoteRune {
				line = string([]rune(line)[:maxReviewQuoteRune]) + "…"
			}
			fmt.Fprintf(&b, " %4d | %s\n", in.Line+i, line)
		}
	}
	body := strings.TrimSpace(in.Body)
	if len([]rune(body)) > maxReviewBodyRune {
		body = string([]rune(body)[:maxReviewBodyRune]) + "…"
	}
	fmt.Fprintf(&b, "评论：%s\n", body)

	switch note, ok := locationNote(in, fileLines, readable); {
	case ok:
		fmt.Fprintf(&b, "位置：%s\n", note)
	}
	return b.String(), nil
}

// locationNote explains where the quoted code is now. It returns false when
// there is nothing worth saying (no quote).
func locationNote(in ReviewComment, fileLines []string, readable bool) (string, bool) {
	if len(in.Quote) == 0 {
		return "", false
	}
	if in.Side == "old" {
		return "这一行在 diff 里是删除行，当前文件里没有它。", true
	}
	if !readable {
		return "读取当前文件失败，引用来自 diff。", true
	}
	switch where, at := findQuote(fileLines, in.Line, in.Quote); where {
	case locationExact:
		return fmt.Sprintf("这段代码现在仍在第 %d 行。", at), true
	case locationMoved:
		return fmt.Sprintf("这段代码现在在第 %d 行（写评论后文件改过，行号有偏移），以当前文件为准。", at), true
	default:
		return "当前文件里找不到这段代码（可能已被修改或删除），请以 diff 和评论为准。", true
	}
}

// reviewPrompt resolves the file and composes the message for one comment.
//
// A file that cannot be read is not an error: the comment is still meaningful,
// and refusing to send it because the workspace is momentarily unavailable would
// lose what the user wrote. The message says the quote was not verified.
func (s *server) reviewPrompt(ctx context.Context, userID string, sess *contracts.Session, in ReviewComment) (string, error) {
	if err := validateReviewComment(in); err != nil {
		return "", err
	}
	var (
		lines    []string
		readable bool
	)
	if root, err := s.worktreeRoot(ctx, userID, sess); err == nil && s.deps.Workspace != nil {
		if content, cerr := s.deps.Workspace.ReadFile(ctx, root, strings.TrimSpace(in.Path)); cerr == nil &&
			content != nil && !content.Binary && !content.Truncated {
			lines = strings.Split(strings.ReplaceAll(content.Body, "\r\n", "\n"), "\n")
			readable = true
		}
	}
	return composeReviewPrompt(in, lines, readable)
}
