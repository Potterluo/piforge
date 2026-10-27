// Command wsprobe is a development tool: it opens the terminal WebSocket the
// way the browser does and reports what came back. Keeping it in the repo means
// "the terminal is broken" can be triaged without a browser.
//
// Usage:
//
//	go run ./tools/wsprobe -session ses_xxx -cookie <pf_session value> [-cmd "echo hi"]
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
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			fmt.Printf("READ_ERROR: %v\n", err)
			break
		}
		if kind == websocket.MessageText {
			fmt.Printf("CTRL: %s\n", string(data))
			if strings.Contains(string(data), `"ready"`) && !sent && *command != "" {
				_ = conn.Write(ctx, websocket.MessageBinary, []byte(*command+"\r"))
				sent = true
			}
			continue
		}
		output.Write(data)
		if *command != "" && strings.Contains(output.String(), strings.Fields(*command)[len(strings.Fields(*command))-1]) {
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
