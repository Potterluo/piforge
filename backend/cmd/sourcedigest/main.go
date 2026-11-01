// Command sourcedigest prints the piforge.source-digest value for one image
// recipe. It is the single implementation of that rule: scripts/up-isolated.ps1
// labels every image with its answer and rebuilds when the label differs, and
// scripts/verify-linux.sh refuses to assert on a sandbox image whose label
// differs — so a stale image is named at the point of use instead of failing an
// assertion three hundred lines later.
//
// Digest only:
//
//	go run ./backend/cmd/sourcedigest -root . -dockerfile Dockerfile
//
// The sandbox image, which is built on top of another image:
//
//	go run ./backend/cmd/sourcedigest -root . \
//	  -dockerfile deploy/Dockerfile.sandbox-agent -agent-image piforge:isolated
//
// The same value plus how many files it covers and where they came from (what
// up-isolated.ps1 logs):
//
//	go run ./backend/cmd/sourcedigest -root . -dockerfile Dockerfile -json
//
// Exit status 0 means a digest was printed; 1 means it could not be computed and
// the reason is on stderr.
//
// "Could not be computed" includes one case that used to be silent: a tree git
// cannot list (no git, not a checkout, a directory git refuses). The digest then
// covers the recipe files alone and does not change when backend/, web/ or
// deploy/ changes — so it would answer "current" for a tree it never read. That
// is a guess, and this program refuses to print it. -allow-fallback exists for a
// build that really is made of those files alone; it prints the digest, says so
// on stderr, and marks the answer "source":"fallback" in -json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/piforge/piforge/backend/internal/sourcedigest"
)

// stringList collects a flag that may be given more than once, in order: the
// build arguments are part of the digest, so their order must be the caller's.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	root := flag.String("root", ".", "the checkout the image was built from")
	dockerfile := flag.String("dockerfile", "Dockerfile",
		"the recipe's path, relative to root; its name is part of the digest")
	agentImage := flag.String("agent-image", "",
		"the control-plane image a sandbox image was built FROM; its full id is folded in as AGENT_IMAGE=<id>")
	asJSON := flag.Bool("json", false, `print {"digest":…,"files":…,"source":…,"dockerfile":…,"args":[…]} instead of the bare digest`)
	allowFallback := flag.Bool("allow-fallback", false,
		"print a digest from the recipe files alone when git cannot list the tree (it then cannot notice a source edit; the scripts refuse such a digest)")
	var args stringList
	flag.Var(&args, "arg", "a build argument that changes what the image contains (repeatable, KEY=VALUE)")
	flag.Parse()

	if strings.TrimSpace(*root) == "" {
		fail(fmt.Errorf("-root is required"))
	}
	if strings.TrimSpace(*dockerfile) == "" {
		fail(fmt.Errorf("-dockerfile is required"))
	}
	buildArgs := []string(args)
	if *agentImage != "" {
		id, err := sourcedigest.ImageID(*agentImage)
		if err != nil {
			fail(fmt.Errorf("the sandbox image's parent could not be resolved, so its digest cannot be computed: %w", err))
		}
		// The image, not the tag: a tag can be moved to another image, and then
		// the sandbox would still be claiming to be current.
		buildArgs = append(buildArgs, "AGENT_IMAGE="+id)
	}

	listing, err := sourcedigest.List(*root)
	if err != nil {
		fail(err)
	}
	if listing.Via != "git" {
		if !*allowFallback {
			fail(fmt.Errorf("the tree could not be listed through git (%v), so the digest would cover only these %d file(s) — %s — and would not change when a source file under backend/, web/ or deploy/ changed; refusing to guess. Run from a checkout with git installed, or pass -allow-fallback if the image really is built from these files alone",
				listing.GitErr, len(listing.Files), strings.Join(listing.Files, ", ")))
		}
		fmt.Fprintf(os.Stderr, "sourcedigest: WARNING: %v; the digest covers %d recipe file(s) only and cannot notice a source edit (source \"fallback\")\n",
			listing.GitErr, len(listing.Files))
	}
	files := listing.Files
	digest, err := sourcedigest.DigestOf(*root, files, *dockerfile, buildArgs)
	if err != nil {
		fail(err)
	}

	if !*asJSON {
		fmt.Println(digest)
		return
	}
	payload := struct {
		Digest     string   `json:"digest"`
		Files      int      `json:"files"`
		Source     string   `json:"source"`
		Dockerfile string   `json:"dockerfile"`
		Args       []string `json:"args"`
	}{Digest: digest, Files: len(files), Source: listing.Via, Dockerfile: *dockerfile, Args: buildArgs}
	if payload.Args == nil {
		payload.Args = []string{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "sourcedigest: %v\n", err)
	os.Exit(1)
}
