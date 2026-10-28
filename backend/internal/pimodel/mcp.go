package pimodel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MCP wiring.
//
// Pi reads MCP servers from `<agent dir>/mcp.json`, which is the same directory the
// model configuration already lives in. Until this existed, the settings page stored
// MCP servers, encrypted their credentials, and nothing ever reached the agent — the
// tools simply were not there, and no error said so.

// MCPEntry is one server as Pi wants it.
type MCPEntry struct {
	Name      string
	Transport string
	Command   string
	Args      []string
	URL       string
	// Env carries credentials. The UI's free-text blob is parsed into it, so a
	// credential never appears in the command line where `ps` would show it.
	Env map[string]string
}

// mcpFile is the on-disk shape Pi reads.
type mcpFile struct {
	MCPServers map[string]mcpServerFile `json:"mcpServers"`
}

type mcpServerFile struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// WriteMCP renders the MCP configuration for one agent directory.
//
// An empty list writes an empty file rather than deleting it: a session that had
// servers and then lost them must see them gone, and a stale file would quietly keep
// tools the user removed.
func WriteMCP(dir string, entries []MCPEntry) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("pimodel: agent dir is required")
	}
	file := mcpFile{MCPServers: map[string]mcpServerFile{}}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		server := mcpServerFile{Command: entry.Command, Args: entry.Args, Env: entry.Env}
		if entry.Transport == "http" {
			server.URL = entry.URL
			// An HTTP server has no command; keeping one would be a second, wrong way
			// for Pi to start it.
			server.Command = ""
		}
		file.MCPServers[name] = server
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", fmt.Errorf("pimodel: encode mcp.json: %w", err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("pimodel: create agent dir: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("pimodel: write mcp.json: %w", err)
	}
	return path, nil
}

// ParseMCPSecrets turns the settings page's free text into environment variables.
//
// The format is one `KEY=value` per line, because that is what a person pastes from a
// secret manager and what every .env file already looks like. Lines without `=` are
// ignored rather than guessed at.
func ParseMCPSecrets(blob string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env[key] = strings.TrimSpace(value)
	}
	return env
}

// MCPServersFromEntries is a small helper for tests and callers that need the names in
// a stable order (a config file that shuffles between runs is noise in a diff).
func MCPServersFromEntries(entries []MCPEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	sort.Strings(names)
	return names
}
