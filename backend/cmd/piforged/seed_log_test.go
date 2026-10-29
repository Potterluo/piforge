package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The container log is one of the places an initial password must never appear:
// it is copied into tickets, CI output and `docker logs` screenshots. The line
// still has to name the account, or an operator cannot tell what was created.
func TestSeedLogNamesAccountsButNeverPasswords(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logSeededAccounts(logger, &store.SeedResult{
		Created: true,
		Admin: &contracts.User{
			Email: "admin@piforge.local",
		},
		Demo: &contracts.User{
			Email: "dev@piforge.local",
		},
		AdminPassword: "piforge-admin-123",
		DemoPassword:  "piforge-dev-123",
	})

	logged := buf.String()
	for _, email := range []string{"admin@piforge.local", "dev@piforge.local"} {
		if !strings.Contains(logged, email) {
			t.Fatalf("seed log does not name %q: %s", email, logged)
		}
	}
	for _, password := range []string{"piforge-admin-123", "piforge-dev-123"} {
		if strings.Contains(logged, password) {
			t.Fatalf("seed log leaked the password %q: %s", password, logged)
		}
	}
}
