package pimodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The MCP configuration has to be in the shape Pi actually reads, because "we wrote a
// file" is not the same as "the agent has the tools". The shape below was taken from
// `pi mcp add` in the shipped image.
func TestWriteMCPMatchesPiShape(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteMCP(dir, []MCPEntry{
		{
			Name:    "files",
			Command: "npx",
			Args:    []string{"-y", "server-fs"},
			Env:     map[string]string{"TOKEN": "s3cret"},
		},
		{
			Name:      "remote",
			Transport: "http",
			URL:       "https://mcp.example.com/sse",
			// A command alongside an http transport would be a second, wrong way to
			// start the server, so it must not survive.
			Command: "should-be-dropped",
		},
	})
	if err != nil {
		t.Fatalf("WriteMCP: %v", err)
	}
	if path != filepath.Join(dir, "mcp.json") {
		t.Fatalf("path = %q", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	files, ok := decoded.MCPServers["files"]
	if !ok {
		t.Fatalf("the stdio server is missing: %s", raw)
	}
	if files.Command != "npx" || len(files.Args) != 2 || files.Env["TOKEN"] != "s3cret" {
		t.Fatalf("stdio server = %+v", files)
	}
	remote, ok := decoded.MCPServers["remote"]
	if !ok {
		t.Fatalf("the http server is missing: %s", raw)
	}
	if remote.Command != "" {
		t.Fatalf("an http server kept a command: %+v", remote)
	}
	if remote.URL != "https://mcp.example.com/sse" {
		t.Fatalf("url = %q", remote.URL)
	}

	// An empty list clears the file rather than leaving stale tools behind.
	if _, err := WriteMCP(dir, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	raw, _ = os.ReadFile(path)
	// Reset first: unmarshalling into a populated map merges into it, which would make
	// this assertion pass no matter what the file said.
	decoded.MCPServers = nil
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode cleared: %v", err)
	}
	if len(decoded.MCPServers) != 0 {
		t.Fatalf("cleared file still lists %d servers", len(decoded.MCPServers))
	}
}

// The settings page takes a free-text blob; the contract is KEY=value lines, which is
// what people paste.
func TestParseMCPSecrets(t *testing.T) {
	env := ParseMCPSecrets("TOKEN=abc\n\n# a comment\nURL = https://x\nnonsense\n=empty\n")
	if env["TOKEN"] != "abc" || env["URL"] != "https://x" {
		t.Fatalf("parsed = %+v", env)
	}
	if _, ok := env["nonsense"]; ok {
		t.Fatal("a line without '=' became a variable")
	}
	if _, ok := env[""]; ok {
		t.Fatal("an empty key survived")
	}
	if len(ParseMCPSecrets("")) != 0 {
		t.Fatal("an empty blob produced variables")
	}
}
