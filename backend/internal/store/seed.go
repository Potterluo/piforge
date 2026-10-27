package store

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Seed environment variables. cmd/piforged passes them through so the e2e
// suite gets deterministic credentials; when a variable is absent the
// corresponding password is generated and returned instead.
const (
	// EnvSeedAdminEmail is the admin account email.
	EnvSeedAdminEmail = "PIFORGE_SEED_EMAIL"
	// EnvSeedAdminPassword is the admin account password.
	EnvSeedAdminPassword = "PIFORGE_SEED_PASSWORD"
	// EnvSeedUserEmail is the demo (role=user) account email.
	EnvSeedUserEmail = "PIFORGE_SEED_USER_EMAIL"
	// EnvSeedUserPassword is the demo account password.
	EnvSeedUserPassword = "PIFORGE_SEED_USER_PASSWORD"
)

// SeedOptionsFromEnv builds SeedOptions from the PIFORGE_SEED_* variables.
// The caller still supplies HashPassword (the store does not import bcrypt), so
// the usual wiring is:
//
//	opts := store.SeedOptionsFromEnv()
//	opts.HashPassword = auth.HashPassword
//	res, err := st.EnsureAdminSeed(ctx, opts)
func SeedOptionsFromEnv() SeedOptions {
	return SeedOptions{
		AdminEmail:    strings.TrimSpace(os.Getenv(EnvSeedAdminEmail)),
		AdminPassword: os.Getenv(EnvSeedAdminPassword),
		DemoEmail:     strings.TrimSpace(os.Getenv(EnvSeedUserEmail)),
		DemoPassword:  os.Getenv(EnvSeedUserPassword),
	}
}

// SeedOptions configures EnsureAdminSeed.
type SeedOptions struct {
	// AdminEmail / DemoEmail default to admin@piforge.local and
	// dev@piforge.local.
	AdminEmail string
	AdminName  string
	DemoEmail  string
	DemoName   string
	// AdminPassword / DemoPassword are used verbatim when set (the e2e suite
	// pins them). When empty each account gets its own generated password,
	// returned in SeedResult so the binary can print it once.
	AdminPassword string
	DemoPassword  string
	// HashPassword is required: the store does not import bcrypt, the auth
	// package owns hashing. cmd/piforged passes auth.HashPassword.
	HashPassword func(plain string) (string, error)
	// SharedProvider, when non-nil, becomes the admin-scope provider every user
	// inherits (PRD §5.7.1).
	SharedProvider *ProviderInput
}

// SeedResult reports what EnsureAdminSeed did. Passwords are returned in
// clear so the caller can print them exactly once; they are never stored in
// clear anywhere.
type SeedResult struct {
	Created        bool
	Admin          *contracts.User
	Demo           *contracts.User
	AdminPassword  string
	DemoPassword   string
	SharedProvider *contracts.Provider
}

// EnsureAdminSeed creates the first admin and one demo account when the users
// table is empty, and optionally the shared provider. It is a no-op on a
// database that already has accounts, so it is safe to call on every boot.
//
// Printing the generated passwords is the caller's job: this package has no
// logger and a store should not write to stdout.
func (s *Store) EnsureAdminSeed(ctx context.Context, opts SeedOptions) (*SeedResult, error) {
	if opts.HashPassword == nil {
		return nil, fmt.Errorf("store: seed: HashPassword is required: %w", ErrInvalid)
	}
	n, err := s.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return &SeedResult{Created: false}, nil
	}

	adminEmail := strings.TrimSpace(opts.AdminEmail)
	if adminEmail == "" {
		adminEmail = "admin@piforge.local"
	}
	adminName := opts.AdminName
	if adminName == "" {
		adminName = "Admin"
	}
	demoEmail := strings.TrimSpace(opts.DemoEmail)
	if demoEmail == "" {
		demoEmail = "dev@piforge.local"
	}
	demoName := opts.DemoName
	if demoName == "" {
		demoName = "Dev User"
	}

	adminPw := opts.AdminPassword
	if adminPw == "" {
		adminPw, err = generatedPassword()
		if err != nil {
			return nil, fmt.Errorf("store: seed: generate admin password: %w", err)
		}
	}
	demoPw := opts.DemoPassword
	if demoPw == "" {
		demoPw, err = generatedPassword()
		if err != nil {
			return nil, fmt.Errorf("store: seed: generate demo password: %w", err)
		}
	}

	adminHash, err := opts.HashPassword(adminPw)
	if err != nil {
		return nil, fmt.Errorf("store: seed: hash admin password: %w", err)
	}
	demoHash, err := opts.HashPassword(demoPw)
	if err != nil {
		return nil, fmt.Errorf("store: seed: hash demo password: %w", err)
	}

	admin, err := s.CreateUser(ctx, NewUser{
		Email:        adminEmail,
		DisplayName:  adminName,
		PasswordHash: adminHash,
		Role:         contracts.RoleAdmin,
		Status:       "active",
	})
	if err != nil {
		return nil, fmt.Errorf("store: seed: create admin: %w", err)
	}
	demo, err := s.CreateUser(ctx, NewUser{
		Email:        demoEmail,
		DisplayName:  demoName,
		PasswordHash: demoHash,
		Role:         contracts.RoleUser,
		Status:       "active",
	})
	if err != nil {
		return nil, fmt.Errorf("store: seed: create demo user: %w", err)
	}

	res := &SeedResult{
		Created:       true,
		Admin:         admin,
		Demo:          demo,
		AdminPassword: adminPw,
		DemoPassword:  demoPw,
	}
	if opts.SharedProvider != nil {
		sp, err := s.UpsertAdminProvider(ctx, admin.ID, *opts.SharedProvider)
		if err != nil {
			return nil, fmt.Errorf("store: seed: shared provider: %w", err)
		}
		res.SharedProvider = sp
	}
	return res, nil
}

// generatedPassword returns a 15-character Crockford base32 password. It is
// long enough to survive bcrypt and short enough to copy from a terminal, and
// it avoids characters that are easy to misread.
func generatedPassword() (string, error) {
	tok, err := randomToken(9)
	if err != nil {
		return "", err
	}
	return "pf-" + tok, nil
}
