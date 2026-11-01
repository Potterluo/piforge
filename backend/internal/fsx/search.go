package fsx

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Search finds files by name (byContent=false) or by content
// (byContent=true).
//
// ripgrep is used when available (PRD FR-6.4/FR-6.6); otherwise a bounded
// filepath.Walk keeps the feature working on a box without rg. Both paths are
// capped so a search in a huge repository cannot hang the request.
func (s *Service) Search(ctx context.Context, root, query string, byContent bool) ([]contracts.SearchResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("fsx: search: empty query")
	}
	rootAbs, err := absRoot(root)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fsx: search: %w", err)
	}
	if fi, serr := os.Stat(rootAbs); serr != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: %q", ErrNotExist, root)
	}
	if byContent {
		return s.searchContent(ctx, rootAbs, q)
	}
	return s.searchNames(ctx, rootAbs, q)
}

// searchContent greps file contents.
func (s *Service) searchContent(ctx context.Context, rootAbs, q string) ([]contracts.SearchResult, error) {
	if rg, ok := rgAvailable(); ok {
		base := []string{
			rg, "--json", "--no-config", "--color=never", "--smart-case",
			"--max-filesize=" + strconv.FormatInt(s.cfg.MaxInlineBytes, 10),
			"--max-count=20", "--glob=!.git", "--glob=!node_modules",
		}
		// Regex first (what users expect), literal fallback when the pattern
		// does not compile.
		for _, mode := range [][]string{
			append(append([]string{}, base...), "--regexp="+q),
			append(append([]string{}, base...), "--fixed-strings", "--regexp="+q),
		} {
			out, err := runSearchCmd(ctx, rootAbs, mode)
			if err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("fsx: search: %w", ctx.Err())
				}
				continue
			}
			results := decodeRGLines(out, s.cfg.SearchLimit)
			return results, nil
		}
	}
	return s.walkSearch(ctx, rootAbs, q, true)
}

// searchNames lists candidate files and filters them by a case-insensitive
// substring or subsequence match (the ⌘P behaviour).
func (s *Service) searchNames(ctx context.Context, rootAbs, q string) ([]contracts.SearchResult, error) {
	if rg, ok := rgAvailable(); ok {
		out, err := runSearchCmd(ctx, rootAbs, []string{
			rg, "--files", "--no-config", "--color=never",
			"--glob=!.git", "--glob=!node_modules", "--hidden",
		})
		if err == nil {
			results := make([]contracts.SearchResult, 0, s.cfg.SearchLimit)
			for _, line := range strings.Split(out, "\n") {
				p := strings.TrimSpace(line)
				if p == "" {
					continue
				}
				p = filepath.ToSlash(p)
				if strings.HasPrefix(p, ".git/") {
					continue
				}
				if !MatchName(p, q) {
					continue
				}
				results = append(results, contracts.SearchResult{Path: p})
				if len(results) >= s.cfg.SearchLimit {
					break
				}
			}
			return results, nil
		}
	}
	return s.walkSearch(ctx, rootAbs, q, false)
}

// walkSearch is the dependency-free fallback.
func (s *Service) walkSearch(ctx context.Context, rootAbs, q string, byContent bool) ([]contracts.SearchResult, error) {
	results := make([]contracts.SearchResult, 0, s.cfg.SearchLimit)
	visited := 0
	needle := strings.ToLower(q)

	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil // unreadable entry: keep walking
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		name := d.Name()
		if d.IsDir() {
			if p == rootAbs {
				return nil
			}
			if name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			if !s.cfg.ShowIgnored && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > s.cfg.MaxWalkFiles {
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// A workspace belongs to the tenant, and a search reads what it
			// finds: a link in it can name any file the control plane can read
			// (`/data/master.key`). The walk never follows one, so a search
			// cannot be turned into that read. ripgrep's path already behaves
			// this way — it does not follow links unless asked to.
			return nil
		}
		rel, rerr := filepath.Rel(rootAbs, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if !byContent {
			if MatchName(rel, q) {
				results = append(results, contracts.SearchResult{Path: rel})
			}
			if len(results) >= s.cfg.SearchLimit {
				return fs.SkipAll
			}
			return nil
		}

		fi, ierr := d.Info()
		if ierr != nil || fi.Size() > s.cfg.MaxScanBytes || fi.Size() == 0 {
			return nil
		}
		f, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			text := sc.Text()
			if strings.Contains(strings.ToLower(text), needle) {
				results = append(results, contracts.SearchResult{Path: rel, Line: lineNo, Text: text})
				if len(results) >= s.cfg.SearchLimit {
					break
				}
			}
		}
		f.Close()
		if len(results) >= s.cfg.SearchLimit {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("fsx: search: %w", ctxErr)
		}
		return nil, fmt.Errorf("fsx: search %q: %w", rootAbs, walkErr)
	}
	return results, nil
}

// MatchName reports whether the path matches the query as a case-insensitive
// substring or as an ordered subsequence ("srcrd" matches "src/readme.md").
func MatchName(p, q string) bool {
	lp := strings.ToLower(filepath.ToSlash(p))
	lq := strings.ToLower(q)
	if strings.Contains(lp, lq) {
		return true
	}
	i := 0
	for j := 0; j < len(lp) && i < len(lq); j++ {
		if lp[j] == lq[i] {
			i++
		}
	}
	return i == len(lq)
}
