package store

import (
	"context"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-15.2: a stored MCP credential must be unreadable in the database and unreadable
// through the API, and it must survive the round trip for the one caller that needs it.
func TestMCPSecretsAreEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	user := mkUser(t, st, "dev@piforge.local", contracts.RoleUser)

	const secret = "tok_live_9f2c1a"
	server, err := st.CreateMCPServer(ctx, user.ID, contracts.MCPServer{
		Name:      "files",
		Transport: contracts.MCPStdio,
		Command:   "npx",
		Args:      []string{"-y", "server-fs"},
		Enabled:   true,
		Secrets:   secret,
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}
	if !server.HasSecrets {
		t.Fatal("has_secrets is false after storing a credential")
	}
	if server.Secrets != "" {
		t.Fatal("the model came back carrying the credential")
	}

	// The ciphertext on disk must not contain the plaintext.
	var blob []byte
	if err := st.r.QueryRowContext(ctx,
		`SELECT secrets_cipher FROM mcp_servers WHERE id = ?`, server.ID).Scan(&blob); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("nothing was stored")
	}
	if strings.Contains(string(blob), secret) {
		t.Fatal("the credential is stored in the clear")
	}

	// The launcher path can still read it back.
	got, err := st.MCPServerSecrets(ctx, user.ID, server.ID)
	if err != nil {
		t.Fatalf("MCPServerSecrets: %v", err)
	}
	if got != secret {
		t.Fatalf("round trip = %q", got)
	}

	// A different user cannot read it, and a server without secrets answers empty.
	other := mkUser(t, st, "other@piforge.local", contracts.RoleUser)
	if _, err := st.MCPServerSecrets(ctx, other.ID, server.ID); err == nil {
		t.Fatal("another user read the credential")
	}
	plain, err := st.CreateMCPServer(ctx, user.ID, contracts.MCPServer{
		Name: "no-secret", Transport: contracts.MCPStdio, Command: "true", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create without secrets: %v", err)
	}
	if value, err := st.MCPServerSecrets(ctx, user.ID, plain.ID); err != nil || value != "" {
		t.Fatalf("a server without secrets returned %q, %v", value, err)
	}
}
