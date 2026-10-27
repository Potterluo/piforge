package fsx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// ignoreSet answers "is this repo-relative path ignored?" for one directory
// listing. Inside a git repository it delegates to `git check-ignore` (one
// batched process per directory); outside a repository it falls back to a
// simple .gitignore glob matcher.
type ignoreSet struct {
	results map[string]bool
}

func newIgnoreSet() *ignoreSet { return &ignoreSet{results: map[string]bool{}} }

// ignored reports whether rel (slash separated, relative to the workspace
// root) is ignored.
func (s *ignoreSet) ignored(rel string) bool {
	if s == nil || len(s.results) == 0 {
		return false
	}
	return s.results[rel]
}

// checkIgnoreBatch asks git which of rels are ignored.
//
// It never fails the caller: a missing git binary, a root that is not a
// repository, or an unexpected exit falls back to the built-in matcher, so a
// listing always works. The package deliberately shells out to git instead of
// importing internal/gitx: ignore rules are git data, not workspace logic.
func (s *Service) checkIgnoreBatch(ctx context.Context, rootAbs string, rels []string) *ignoreSet {
	set := newIgnoreSet()
	if len(rels) == 0 {
		return set
	}
	// -z keeps filenames containing newlines intact; input is NUL separated.
	stdin := strings.Join(rels, "\x00") + "\x00"

	cmd := exec.CommandContext(ctx, s.cfg.GitBinary, "check-ignore", "-z", "--stdin", "--no-index")
	cmd.Dir = rootAbs
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			// Not a repository, or git is missing: use the fallback matcher.
			return s.builtinIgnore(rootAbs, rels)
		}
		// Exit 1 means "none of these paths are ignored".
	}
	for _, entry := range strings.Split(out.String(), "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		set.results[filepath.ToSlash(entry)] = true
	}
	return set
}

// builtinIgnore is the fallback when the root is not a git repository: it
// reads the root .gitignore and applies gitignore-ish glob matching.
func (s *Service) builtinIgnore(rootAbs string, rels []string) *ignoreSet {
	set := newIgnoreSet()
	rules := loadGitignore(rootAbs)
	if len(rules) == 0 {
		return set
	}
	for _, rel := range rels {
		if rules.match(rel) {
			set.results[rel] = true
		}
	}
	return set
}

type ignoreRule struct {
	negated  bool
	dirOnly  bool
	anchored bool
	pattern  string
}

type ignoreRules []ignoreRule

// loadGitignore parses <root>/.gitignore. Unreadable files yield no rules.
func loadGitignore(rootAbs string) ignoreRules {
	f, err := os.Open(filepath.Join(rootAbs, ".gitignore"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var rules ignoreRules
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 32*1024), 256*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			rule.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rule.pattern = line
		rules = append(rules, rule)
	}
	return rules
}

// match applies the rules in order; the last matching rule wins, which is the
// precedence gitignore uses.
func (r ignoreRules) match(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	base := path.Base(rel)
	ignored := false
	for _, rule := range r {
		if rule.matches(rel, base) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func (r ignoreRule) matches(rel, base string) bool {
	target := base
	if r.anchored || strings.Contains(r.pattern, "/") {
		target = rel
	}
	ok, err := path.Match(r.pattern, target)
	if err != nil {
		return false
	}
	return ok
}

// isGitRepo reports whether dir is inside a git work tree. It is a best-effort
// probe used only to decide whether a status call is worth making.
func isGitRepo(ctx context.Context, gitBinary, dir string) bool {
	cmd := exec.CommandContext(ctx, gitBinary, "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(out.String()) == "true"
}
