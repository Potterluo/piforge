// Command fakepi runs a Pi-compatible RPC peer as a subprocess. It is the
// throwaway agent used by the local development sandbox and by the end-to-end
// tests: it speaks the same JSONL protocol as `pi --mode rpc`, so the control
// plane cannot tell the difference, but it never calls a model.
//
// All of the behaviour lives in internal/pitest so the same peer can also be
// driven in-process over an io.Pipe. This command is only a flag parser.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/pitest"
)

func main() {
	var sessionFile string
	fs := flag.NewFlagSet("fakepi", flag.ContinueOnError)
	fs.StringVar(&sessionFile, "session-file", "", "path of the JSONL session file")
	// Pi's own flag is -session; accept it so the bridge does not need to know
	// which peer it is talking to.
	fs.StringVar(&sessionFile, "session", "", "alias of -session-file")
	script := fs.String("script", pitest.EnvOr("FAKEPI_SCRIPT", pitest.ScriptOK),
		"scripted output: ok|error|slow")
	delayMs := fs.Int("delay", pitest.EnvInt("FAKEPI_DELAY_MS", 5), "per-chunk delay in ms")
	emitHeader := fs.Bool("json-header", false, "emit the session header first (JSON mode)")

	// The real `pi` accepts a much larger surface — --model, --provider, and
	// whatever else the control plane learns to pass. A stand-in that dies on an
	// unrecognised flag turns "the runtime started sending one more argument" into
	// every end-to-end test failing at once, with the agent gone before it reads a
	// byte. Ignore what we do not model, the way the real CLI tolerates it.
	args := withoutUnknown(fs, os.Args[1:])
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

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

// withoutUnknown drops flags this stand-in does not implement, along with their
// value when they take one. Everything from the first positional argument or a
// bare "--" onward is left alone.
func withoutUnknown(fs *flag.FlagSet, args []string) []string {
	known := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { known[f.Name] = true })

	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			out = append(out, args[i:]...)
			break
		}

		name := strings.TrimLeft(arg, "-")
		// `--flag=value` carries its own value; the name is what decides.
		hasInlineValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, hasInlineValue = name[:eq], true
		}

		if !known[name] {
			// Skip the value too: `--model gpt-4o` must not leave `gpt-4o` behind
			// to be mistaken for the session file or the prompt.
			if !hasInlineValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}

		out = append(out, arg)
		// A kept flag's value is not a positional argument, so it must not end
		// the scan: `--session /path --model x` has to reach `--model`.
		if !hasInlineValue && !isBoolFlag(fs, name) && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out
}

// isBoolFlag reports whether a flag can be written without a value.
func isBoolFlag(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	type boolFlag interface{ IsBoolFlag() bool }
	if bf, ok := f.Value.(boolFlag); ok {
		return bf.IsBoolFlag()
	}
	return false
}
