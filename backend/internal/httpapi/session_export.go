package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pi"
)

// handleExportSession renders a conversation as a file the user can keep.
//
// It reads the persisted event log rather than asking the agent, so an archived
// or long-finished session exports exactly as it was seen — and it works when no
// process is running, which is the common case for a session worth exporting.
func (s *server) handleExportSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	events, err := s.exportEvents(r, user.ID, sess.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "md"
	}

	var body, contentType string
	switch format {
	case "md", "markdown":
		body, contentType = renderMarkdown(sess, events), "text/markdown; charset=utf-8"
	case "html":
		body, contentType = renderHTML(sess, events), "text/html; charset=utf-8"
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "format must be md or html"))
		return
	}

	filename := exportFilename(sess) + "." + format
	w.Header().Set("Content-Type", contentType)
	// Two forms: an ASCII fallback for old clients and RFC 5987 for the real name.
	// Chinese titles have no ASCII form worth keeping.
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		"attachment; filename=%q; filename*=UTF-8''%s", asciiFilename(filename), url.PathEscape(filename)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// exportEvents pages through the whole log; a conversation can be longer than
// one ListEvents call, and a silently truncated export is worse than none.
func (s *server) exportEvents(r *http.Request, userID, sessionID string) ([]*contracts.Event, error) {
	const page = 500
	all := make([]*contracts.Event, 0, page)
	after := int64(0)
	for {
		batch, err := s.deps.Store.ListEvents(r.Context(), userID, sessionID, after, page)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return all, nil
		}
		all = append(all, batch...)
		after = batch[len(batch)-1].Seq
		if len(batch) < page {
			return all, nil
		}
	}
}

// exportLine is one rendered message.
type exportLine struct {
	role string
	text string
}

// messagesFromEvents keeps the final form of each message and drops the stream.
func messagesFromEvents(events []*contracts.Event) []exportLine {
	lines := make([]exportLine, 0, len(events))
	seen := map[string]int{}
	for _, event := range events {
		if event.Type != contracts.EvMsgFinal {
			continue
		}
		var payload pi.MessageFinalPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		text := renderBlocks(payload)
		if text == "" {
			continue
		}
		if index, ok := seen[event.RunID+":"+payload.Role]; ok && event.Type == contracts.EvMsgFinal {
			// Repeated finals for the same turn replace the earlier text: that is
			// how a streamed answer settles into its authoritative form.
			lines[index] = exportLine{role: payload.Role, text: text}
			continue
		}
		if event.RunID != "" {
			seen[event.RunID+":"+payload.Role] = len(lines)
		}
		lines = append(lines, exportLine{role: payload.Role, text: text})
	}
	return lines
}

// renderBlocks flattens one message into prose plus a mention of each tool call.
//
// Payload.Text and the text blocks carry the same prose: the normalizer fills both
// so a client can render without walking blocks. Using both duplicated every
// message in the exported file, which is how this was found.
func renderBlocks(payload pi.MessageFinalPayload) string {
	parts := make([]string, 0, len(payload.Blocks)+2)
	textSeen := false
	for _, block := range payload.Blocks {
		switch block.Kind {
		case pi.BlockText:
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
				textSeen = true
			}
		case pi.BlockToolCall:
			if block.Tool != nil {
				parts = append(parts, "`"+block.Tool.Name+"`")
			}
		}
	}
	if !textSeen {
		if text := strings.TrimSpace(payload.Text); text != "" {
			parts = append([]string{text}, parts...)
		}
	}
	if payload.Error != "" {
		parts = append(parts, "**错误：** "+strings.TrimSpace(payload.Error))
	}
	return strings.Join(parts, "\n\n")
}

func exportFilename(sess *contracts.Session) string {
	name := strings.TrimSpace(sess.Title)
	if name == "" {
		name = sess.ID
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", `"`, "-", "\n", " ", "\t", " ")
	name = strings.TrimSpace(replacer.Replace(name))
	runes := []rune(name)
	if len(runes) > 60 {
		name = string(runes[:60])
	}
	return name + "-" + sess.ID
}

// asciiFilename is the fallback half of Content-Disposition: conservative ASCII,
// because a header with raw UTF-8 in it is mangled by some clients.
func asciiFilename(name string) string {
	var out strings.Builder
	for _, r := range name {
		if r < 32 || r > 126 || r == '"' || r == ';' {
			out.WriteByte('_')
			continue
		}
		out.WriteRune(r)
	}
	value := out.String()
	if strings.Trim(value, "_ ") == "" {
		return "session-export"
	}
	return value
}

func exportHead(sess *contracts.Session) string {
	title := sess.Title
	if strings.TrimSpace(title) == "" {
		title = "未命名会话"
	}
	return title
}

func renderMarkdown(sess *contracts.Session, events []*contracts.Event) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", exportHead(sess))
	fmt.Fprintf(&out, "- 会话：`%s`\n- 导出时间：%s\n\n", sess.ID, time.Now().Format(time.RFC3339))
	lines := messagesFromEvents(events)
	for _, line := range lines {
		who := "助手"
		if line.role == "user" {
			who = "我"
		}
		fmt.Fprintf(&out, "## %s\n\n%s\n\n", who, line.text)
	}
	if len(lines) == 0 {
		out.WriteString("_这个会话还没有可导出的消息：要么尚未开始，要么正在运行中（未完成的一轮不会出现在这里）。_\n")
	}
	return out.String()
}

func renderHTML(sess *contracts.Session, events []*contracts.Event) string {
	var out strings.Builder
	out.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	fmt.Fprintf(&out, "<title>%s</title>", escapeHTML(exportHead(sess)))
	out.WriteString("<style>body{max-width:46rem;margin:3rem auto;padding:0 1rem;font:16px/1.6 system-ui,sans-serif}")
	out.WriteString("h1{font-size:1.5rem}h2{font-size:.8rem;text-transform:uppercase;letter-spacing:.08em;color:#666;margin-top:2rem}")
	out.WriteString("pre,code{background:#f4f4f5;border-radius:4px}pre{padding:.75rem;overflow:auto}")
	out.WriteString("p{white-space:pre-wrap}</style></head><body>")
	fmt.Fprintf(&out, "<h1>%s</h1>", escapeHTML(exportHead(sess)))
	for _, line := range messagesFromEvents(events) {
		who := "助手"
		if line.role == "user" {
			who = "我"
		}
		fmt.Fprintf(&out, "<h2>%s</h2><p>%s</p>", who, escapeHTML(line.text))
	}
	out.WriteString("</body></html>")
	return out.String()
}

func escapeHTML(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(value)
}
