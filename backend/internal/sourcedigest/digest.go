// Package sourcedigest computes the piforge.source-digest label: one sha256 over
// the working tree a container image was built from.
//
// The digest answers one question — "was this image built from these bytes?" —
// and three places have to agree on the answer: up-isolated.ps1 decides whether
// to rebuild, verify-linux.sh decides whether the image it is about to assert on
// *is* that image at all, and a human reads the label off
// `docker image inspect`. A second implementation of the same rule would make
// those three disagree, so the definition lives here and both scripts call it:
//
//	go run ./backend/cmd/sourcedigest -root . -dockerfile Dockerfile
//
// The inputs are every tracked — or untracked but not ignored — file under
// backend/, web/, deploy/, Dockerfile and docker-compose.runner.yml, the
// Dockerfile's own path (the three images share a tree but not a recipe) and the
// build arguments. The sandbox image adds the full id of the control-plane image
// it is built on top of, because a rebuilt control plane must rebuild it.
//
// The text that is hashed is exactly:
//
//	piforge-source-digest-v1\n
//	<dockerfile>\n
//	<arg 1>\n<arg 2>\n…\n
//	<path>\x00<git blob sha1>\n
//	<path>\x00<git blob sha1>\n…
//
// The blob sha1 is git's own: sha1("blob <len>\x00" + content), over the bytes on
// disk — no clean filters, so what is hashed is what a build would read. Paths
// are sorted by their bytes: the value must not depend on the filesystem's
// iteration order or on a locale's collation.
package sourcedigest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Roots are the paths whose content a build consumes. A file outside this list —
// docs, the e2e suite, a fixture — cannot change an image, so it must not change
// the digest either.
var Roots = []string{"backend", "web", "deploy", "Dockerfile", "docker-compose.runner.yml"}

// FallbackFiles is what is hashed when git cannot list the tree (no git, or a
// directory that is not a checkout): the recipes themselves. It keeps the digest
// shape-comparable, but it is narrower and cannot notice a source edit, so
// List reports it as Via "fallback" and the command refuses to print a digest
// from it unless the caller says -allow-fallback.
var FallbackFiles = []string{"Dockerfile", "deploy/Dockerfile.runner", "deploy/Dockerfile.sandbox-agent", "docker-compose.runner.yml"}

// SaltPrefix names the definition. Changing the algorithm changes this string,
// so an image built by the old rule cannot silently compare equal to a tree
// digested by the new one.
const SaltPrefix = "piforge-source-digest-v1"

// Digest is SourceFiles + DigestOf: the value a build should label its image
// with, for one recipe.
func Digest(root, dockerfile string, args []string) (string, error) {
	files, err := SourceFiles(root)
	if err != nil {
		return "", err
	}
	return DigestOf(root, files, dockerfile, args)
}

// DigestOf hashes an explicit file list. It exists so the rule can be tested on
// a fixture without git, and so a caller that already listed the tree does not
// list it twice.
func DigestOf(root string, files []string, dockerfile string, args []string) (string, error) {
	rows := make([]string, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, rel := range files {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		sha, err := blobSHA1(filepath.Join(root, rel))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// `git ls-files` lists index entries that were deleted from the
				// working tree. They are not part of what a build would copy.
				continue
			}
			return "", err
		}
		rows = append(rows, rel+"\x00"+sha)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("sourcedigest: no file under %s could be read, so there is no digest to compare", root)
	}
	// Rows start with the path, so sorting them is sorting by path — by bytes,
	// which is what git and every shell agree on.
	sort.Strings(rows)
	salt := SaltPrefix + "\n" + dockerfile + "\n" + strings.Join(args, "\n")
	text := salt + "\n" + strings.Join(rows, "\n")
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:]), nil
}

// Listing is the tree's content list plus how it was obtained.
//
// The provenance is not a detail: only a list read through git can notice an
// edit to backend/, web/ or deploy/, and the scripts decide "is this image
// current?" from the digest of that list. A caller that cannot tell a full list
// from the recipe-only fallback would answer "current" for a tree it never read,
// which is a guess, not a decision.
type Listing struct {
	Files []string
	// Via is "git" when git listed the tree, "fallback" when only
	// FallbackFiles could be read.
	Via string
	// GitErr is why git was not asked (no git, not a checkout, a git that
	// refused the directory), or nil when Via is "git".
	GitErr error
}

// List is SourceFiles plus the provenance of the list.
func List(root string) (Listing, error) {
	files, err := gitFiles(root)
	if err == nil && len(files) > 0 {
		return Listing{Files: files, Via: "git"}, nil
	}
	if err == nil {
		err = fmt.Errorf("git listed no file under %s", root)
	}
	fallback, ferr := fallbackFiles(root)
	if ferr != nil {
		return Listing{}, ferr
	}
	return Listing{Files: fallback, Via: "fallback", GitErr: err}, nil
}

// SourceFiles lists the tree's content: tracked files plus untracked files git
// does not ignore, under Roots. Missing files (deleted from the working tree) are
// dropped, so the list names bytes that exist. It drops the provenance; a
// caller that decides anything from the digest wants List instead.
func SourceFiles(root string) ([]string, error) {
	listing, err := List(root)
	if err != nil {
		return nil, err
	}
	return listing.Files, nil
}

// gitFiles lists the tree through git: tracked files plus untracked files git
// does not ignore. A tree that is not a checkout (or a host without git) falls
// back to FallbackFiles, and SourceFiles is the only place that decides.
func gitFiles(root string) ([]string, error) {
	tracked, err := runGit(root, "ls-files", "-z", "--")
	if err != nil {
		return nil, err
	}
	untracked, err := runGit(root, "ls-files", "-z", "-o", "--exclude-standard", "--")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(tracked)+len(untracked))
	var files []string
	for _, blob := range [][]string{tracked, untracked} {
		for _, rel := range blob {
			if rel == "" || seen[rel] {
				continue
			}
			if info, serr := os.Stat(filepath.Join(root, rel)); serr != nil || !info.Mode().IsRegular() {
				continue
			}
			seen[rel] = true
			files = append(files, rel)
		}
	}
	sort.Strings(files)
	return files, nil
}

func runGit(root string, verb ...string) ([]string, error) {
	args := append([]string{"-C", root}, verb...)
	args = append(args, Roots...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("sourcedigest: git %s: %w", strings.Join(verb, " "), err)
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			files = append(files, rel)
		}
	}
	return files, nil
}

func fallbackFiles(root string) ([]string, error) {
	var files []string
	for _, rel := range FallbackFiles {
		if info, err := os.Stat(filepath.Join(root, rel)); err == nil && info.Mode().IsRegular() {
			files = append(files, rel)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("sourcedigest: neither git nor the recipe files could be read under %s", root)
	}
	sort.Strings(files)
	return files, nil
}

// blobSHA1 is git's object id for a file's bytes, computed here rather than by
// `git hash-object` so that the digest does not depend on a second git
// invocation and reads exactly the bytes on disk.
func blobSHA1(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha1.New()
	h.Write([]byte("blob " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ImageID is the untruncated id of a local image. The sandbox image's digest
// folds in the id of the control-plane image it was built from (not the tag: a
// tag can be moved), so that a rebuilt control plane invalidates the sandbox
// even when the tree that produced both is unchanged.
func ImageID(ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("sourcedigest: no image reference was given")
	}
	out, err := exec.Command("docker", "image", "inspect", "-f", "{{.Id}}", ref).Output()
	if err != nil {
		return "", fmt.Errorf("sourcedigest: docker image inspect %s: %w", ref, err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("sourcedigest: %s has no id", ref)
	}
	return id, nil
}
