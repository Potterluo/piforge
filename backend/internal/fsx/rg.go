package fsx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// rgAvailable reports whether ripgrep is on PATH.
func rgAvailable() (string, bool) {
	p, err := exec.LookPath("rg")
	if err != nil {
		return "", false
	}
	return p, true
}

// runSearchCmd runs a search helper with a bounded environment.
func runSearchCmd(ctx context.Context, dir string, argv []string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	env := append(os.Environ(), "NO_COLOR=1", "RIPGREP_CONFIG_PATH=")
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("fsx: %s: %w (%s)", argv[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// rgJSONMatch is the subset of `rg --json` we consume.
type rgJSONMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

// decodeRGLines parses `rg --json` output into search results.
func decodeRGLines(out string, limit int) []contracts.SearchResult {
	results := make([]contracts.SearchResult, 0, limit)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(results) >= limit {
			continue
		}
		var m rgJSONMatch
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m.Type != "match" {
			continue
		}
		results = append(results, contracts.SearchResult{
			Path: filepath.ToSlash(m.Data.Path.Text),
			Line: m.Data.LineNumber,
			Text: strings.TrimRight(m.Data.Lines.Text, "\r\n"),
		})
	}
	return results
}
