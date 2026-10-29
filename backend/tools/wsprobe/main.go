// Command wsprobe is a development tool: it opens the terminal WebSocket the
// way the browser does and reports what came back. Keeping it in the repo means
// "the terminal is broken" can be triaged without a browser.
//
// Usage:
//
//	go run ./tools/wsprobe -session ses_xxx -cookie <pf_session value> [-cmd "echo hi"]
//
// -cmd is typed once the PTY reports ready, and the tool then keeps reading for
// -grace: a terminal echoes what you type, so "stop when the command appears in
// the output" would stop on the echo and never show the answer.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	base := flag.String("base", "ws://127.0.0.1:8080", "control plane base")
	session := flag.String("session", "", "session id")
	cookie := flag.String("cookie", "", "pf_session cookie value")
	tab := flag.String("tab", "probe", "terminal tab id")
	command := flag.String("cmd", "", "command to type once the PTY is ready")
	origin := flag.String("origin", "", "Origin header to send")
	grace := flag.Duration("grace", 3*time.Second, "how long to keep reading after the command is typed")
	wait := flag.Duration("timeout", 20*time.Second, "overall timeout")
	flag.Parse()

	if *session == "" {
		fmt.Fprintln(os.Stderr, "-session is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()

	url := fmt.Sprintf("%s/v1/sessions/%s/pty?tab=%s", *base, *session, *tab)
	header := http.Header{}
	header.Set("Cookie", "pf_session="+*cookie)
	if *origin != "" {
		header.Set("Origin", *origin)
	}

	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		fmt.Printf("DIAL_ERROR: %v\n", err)
		os.Exit(1)
	}
	defer conn.CloseNow()

	var output strings.Builder
	sent := false
	var readBy time.Time
	for {
		// Before the command is typed there is no deadline of our own; after it,
		// the read is bounded by the grace window so the output is printed.
		readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
		if sent {
			readCtx, readCancel = context.WithDeadline(ctx, readBy)
		}
		kind, data, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			break
		}
		if kind == websocket.MessageText {
			fmt.Printf("CTRL: %s\n", string(data))
			if strings.Contains(string(data), `"ready"`) && !sent && *command != "" {
				_ = conn.Write(ctx, websocket.MessageBinary, []byte(*command+"\n"))
				sent = true
				readBy = time.Now().Add(*grace)
			}
			continue
		}
		output.Write(data)
		if sent && !time.Now().Before(readBy) {
			break
		}
	}

	out := output.String()
	fmt.Printf("BYTES: %d\n", len(out))
	if len(out) > 600 {
		out = out[:600]
	}
	fmt.Printf("OUTPUT: %q\n", out)
}
