// Command fakepi runs a Pi-compatible RPC peer as a subprocess. It is the
// throwaway agent used by the local development sandbox and by the end-to-end
// tests: it speaks the same JSONL protocol as `pi --mode rpc`, so the control
// plane cannot tell the difference, but it never calls a model.
//
// All of the behaviour lives in internal/pitest so the same peer can also be
// driven in-process over an io.Pipe. This command is only a flag parser.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/piforge/piforge/backend/internal/pitest"
)

func main() {
	var sessionFile string
	flag.StringVar(&sessionFile, "session-file", "", "path of the JSONL session file")
	// Pi's own flag is -session; accept it so the bridge does not need to know
	// which peer it is talking to.
	flag.StringVar(&sessionFile, "session", "", "alias of -session-file")
	script := flag.String("script", pitest.EnvOr("FAKEPI_SCRIPT", pitest.ScriptOK),
		"scripted output: ok|error|slow")
	delayMs := flag.Int("delay", pitest.EnvInt("FAKEPI_DELAY_MS", 5), "per-chunk delay in ms")
	emitHeader := flag.Bool("json-header", false, "emit the session header first (JSON mode)")
	flag.Parse()

	if *emitHeader {
		if err := pitest.EmitHeader(os.Stdout, ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	delay := time.Duration(*delayMs) * time.Millisecond
	if err := pitest.Serve(*script, delay, sessionFile, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
