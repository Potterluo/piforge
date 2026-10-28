package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// providerCols is: id, scope, owner_id, name, api, base_url, has_key, models,
// enabled, created_at. has_key is derived from the ciphertext length: the
// secret itself never leaves the row.
const providerCols = `id, scope, COALESCE(owner_id, ''), name, api, base_url,
	COALESCE(LENGTH(key_cipher), 0), models, enabled, created_at`

// ProviderInput is the input for CreateProvider and UpsertAdminProvider.
type ProviderInput struct {
	// Name shadows an inherited provider of the same name (PRD §5.7.1).
	Name    string
	API     string
	BaseURL string
	// Models is stored as JSON and returned inside contracts.Provider.
	Models []contracts.ModelSpec
	// KeyCipher is the *ciphertext* of the API key. The store never
	// interprets it; encryption is owned by the secret layer. An empty value
	// means "no key stored" and surfaces as HasKey=false.
	KeyCipher []byte
	// Enabled defaults to true.
	Enabled *bool
}

func (in ProviderInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("store: provider: name is required: %w", ErrInvalid)
	}
	if strings.TrimSpace(in.API) == "" {
		return fmt.Errorf("store: provider %q: api is required: %w", in.Name, ErrInvalid)
	}
	return nil
}

// CreateProvider creates a provider owned by userID (scope='user').
//
// A duplicate name for the same owner is ErrConflict rather than an upsert:
// silently replacing an existing endpoint+key would be surprising. Callers
// that mean "update" should delete or patch instead.
func (s *Store) CreateProvider(ctx context.Context, userID string, in ProviderInput) (*contracts.Provider, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, fmt.Errorf("store: create provider: empty user: %w", ErrInvalid)
	}
	models, err := json.Marshal(modelsOrEmpty(in.Models))
	if err != nil {
		return nil, fmt.Errorf("store: create provider %q: encode models: %w", in.Name, err)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	id := NewID("prv")
	_, err = s.w.ExecContext(ctx,
		`INSERT INTO providers (id, scope, owner_id, name, api, base_url, key_cipher, models, extra, enabled, created_at)
		 VALUES (?, 'user', ?, ?, ?, ?, ?, ?, '{}', ?, ?)`,
		id, userID, in.Name, in.API, in.BaseURL, nullBytes(in.KeyCipher), string(models),
		boolInt(enabled), formatTime(s.now()))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create provider %q: %w", in.Name, ErrConflict)
		}
		return nil, fmt.Errorf("store: create provider %q: %w", in.Name, err)
	}
	return s.Provider(ctx, userID, id)
}

// Provider loads one provider the caller may see: either their own (scope
// 'user') or a shared one (scope 'admin').
func (s *Store) Provider(ctx context.Context, userID, providerID string) (*contracts.Provider, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+providerCols+` FROM providers
		  WHERE id = ? AND ((scope = 'user' AND owner_id = ?) OR scope = 'admin')`,
		providerID, userID)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: provider %q: %w", providerID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: provider %q: %w", providerID, err)
	}
	return p, nil
}

// ListProviders lists the caller's own providers (scope='user') only. The UI
// merges these over the inherited ones.
func (s *Store) ListProviders(ctx context.Context, userID string) ([]*contracts.Provider, error) {
	return s.queryProviders(ctx,
		`SELECT `+providerCols+` FROM providers
		  WHERE scope = 'user' AND owner_id = ?
		  ORDER BY name ASC`, userID)
}

// ProviderSecret returns a provider's sealed key by id.
//
// It exists for the "test this provider" button, which knows the id rather than
// the name. Like ResolveProvider it returns ciphertext: the store never sees a
// plaintext credential.
func (s *Store) ProviderSecret(ctx context.Context, userID, providerID string) ([]byte, error) {
	var sealed []byte
	err := s.r.QueryRowContext(ctx,
		`SELECT key_cipher FROM providers
		  WHERE id = ? AND (scope = 'admin' OR (scope = 'user' AND owner_id = ?))`,
		providerID, userID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: provider %q: %w", providerID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: provider secret %q: %w", providerID, err)
	}
	return sealed, nil
}

// ResolveProvider returns the provider a name resolves to for this user, plus
// its sealed key.
//
// Resolution order is the product's inheritance rule (PRD §5.7.1): a user-scope
// provider shadows an admin-scope provider with the same name. This is the only
// lookup the egress gateway uses, so the shadowing rule lives in exactly one
// place instead of being re-implemented by every caller.
func (s *Store) ResolveProvider(ctx context.Context, userID, name string) (*contracts.Provider, []byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, fmt.Errorf("store: provider name is required: %w", ErrInvalid)
	}

	var (
		id        string
		api       string
		baseURL   string
		modelsRaw string
		scope     string
		keyCipher []byte
	)
	// created_at is deliberately not selected: the gateway never shows it, and
	// the column stores a formatted string that would need parsing for no gain.
	err := s.r.QueryRowContext(ctx,
		`SELECT id, scope, api, base_url, models, key_cipher
		   FROM providers
		  WHERE name = ?
		    AND enabled = 1
		    AND (scope = 'admin' OR (scope = 'user' AND owner_id = ?))
		  ORDER BY CASE scope WHEN 'user' THEN 0 ELSE 1 END
		  LIMIT 1`, name, userID).
		Scan(&id, &scope, &api, &baseURL, &modelsRaw, &keyCipher)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("store: provider %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: resolve provider %q: %w", name, err)
	}

	provider := &contracts.Provider{
		ID:      id,
		Name:    name,
		API:     api,
		BaseURL: baseURL,
		Enabled: true,
		HasKey:  len(keyCipher) > 0,
	}
	if err := json.Unmarshal([]byte(modelsRaw), &provider.Models); err != nil {
		return nil, nil, fmt.Errorf("store: provider %q models: %w", name, err)
	}
	if scope == "admin" {
		provider.Source = contracts.SourceInherited
	} else {
		provider.Source = contracts.SourceCustom
	}
	return provider, keyCipher, nil
}

// InheritedProviders lists the admin-scope providers every user can see.
//
// This is the one read that is not scoped to rows the caller owns, and that is
// the product design rather than an oversight: shared providers are read-only
// configuration ("Inherited" in the UI, PRD §5.7.1). The caller still has to
// be a real account, which the EXISTS check enforces.
func (s *Store) InheritedProviders(ctx context.Context, userID string) ([]*contracts.Provider, error) {
	var one int
	err := s.r.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE id = ? AND status = 'active' AND deleted_at IS NULL`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: inherited providers: caller %q: %w", userID, ErrForbidden)
	}
	if err != nil {
		return nil, fmt.Errorf("store: inherited providers: %w", err)
	}
	return s.queryProviders(ctx,
		`SELECT `+providerCols+` FROM providers WHERE scope = 'admin' ORDER BY name ASC`)
}

// DeleteProvider deletes one of the caller's own providers. A shared provider
// is not deletable through this path, and an id the caller does not own is
// reported as ErrNotFound so existence cannot be probed.
func (s *Store) DeleteProvider(ctx context.Context, userID, providerID string) error {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM providers WHERE id = ? AND scope = 'user' AND owner_id = ?`,
		providerID, userID)
	if err != nil {
		return fmt.Errorf("store: delete provider %q: %w", providerID, err)
	}
	return requireOneRow(res, "store: delete provider "+providerID)
}

// UpsertAdminProvider creates or replaces the shared provider of a given name.
//
// AdminXxx: it requires an active admin caller. It is an upsert because the
// shared provider is a singleton per name — rotating its key must not create a
// second row that users would then see twice.
func (s *Store) UpsertAdminProvider(ctx context.Context, callerID string, in ProviderInput) (*contracts.Provider, error) {
	if err := s.requireAdmin(ctx, callerID); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	models, err := json.Marshal(modelsOrEmpty(in.Models))
	if err != nil {
		return nil, fmt.Errorf("store: upsert admin provider %q: encode models: %w", in.Name, err)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	var existing string
	err = s.r.QueryRowContext(ctx,
		`SELECT id FROM providers WHERE scope = 'admin' AND owner_id IS NULL AND name = ?`,
		in.Name).Scan(&existing)
	switch {
	case err == nil:
		// Keep the old ciphertext when the caller passes no key, so an
		// "enabled toggling" update cannot silently wipe the credential.
		if len(in.KeyCipher) > 0 {
			_, err = s.w.ExecContext(ctx,
				`UPDATE providers SET api = ?, base_url = ?, key_cipher = ?, models = ?, enabled = ?
				  WHERE id = ? AND scope = 'admin'`,
				in.API, in.BaseURL, in.KeyCipher, string(models), boolInt(enabled), existing)
		} else {
			_, err = s.w.ExecContext(ctx,
				`UPDATE providers SET api = ?, base_url = ?, models = ?, enabled = ?
				  WHERE id = ? AND scope = 'admin'`,
				in.API, in.BaseURL, string(models), boolInt(enabled), existing)
		}
		if err != nil {
			return nil, fmt.Errorf("store: upsert admin provider %q: %w", in.Name, err)
		}
		return s.Provider(ctx, callerID, existing)
	case errors.Is(err, sql.ErrNoRows):
		id := NewID("prv")
		if _, err := s.w.ExecContext(ctx,
			`INSERT INTO providers (id, scope, owner_id, name, api, base_url, key_cipher, models, extra, enabled, created_at)
			 VALUES (?, 'admin', NULL, ?, ?, ?, ?, ?, '{}', ?, ?)`,
			id, in.Name, in.API, in.BaseURL, nullBytes(in.KeyCipher), string(models),
			boolInt(enabled), formatTime(s.now())); err != nil {
			return nil, fmt.Errorf("store: upsert admin provider %q: %w", in.Name, err)
		}
		return s.Provider(ctx, callerID, id)
	default:
		return nil, fmt.Errorf("store: upsert admin provider %q: %w", in.Name, err)
	}
}

// AdminListProviders lists the shared provider set.
//
// AdminXxx: admin callers only.
func (s *Store) AdminListProviders(ctx context.Context, callerID string) ([]*contracts.Provider, error) {
	if err := s.requireAdmin(ctx, callerID); err != nil {
		return nil, err
	}
	return s.queryProviders(ctx,
		`SELECT `+providerCols+` FROM providers WHERE scope = 'admin' ORDER BY name ASC`)
}

func (s *Store) queryProviders(ctx context.Context, q string, args ...any) ([]*contracts.Provider, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list providers: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.Provider, 0, 8)
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list providers: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list providers: %w", err)
	}
	return out, nil
}

func scanProvider(sc interface{ Scan(dest ...any) error }) (*contracts.Provider, error) {
	var (
		p       contracts.Provider
		scope   string
		ownerID string
		models  string
		enabled int
		created string
		hasKey  int64
	)
	if err := sc.Scan(&p.ID, &scope, &ownerID, &p.Name, &p.API, &p.BaseURL, &hasKey,
		&models, &enabled, &created); err != nil {
		return nil, err
	}
	p.Source = contracts.SourceCustom
	if scope == "admin" {
		p.Source = contracts.SourceInherited
	}
	p.HasKey = hasKey > 0
	p.Enabled = enabled == 1
	p.CreatedAt = parseTime(created)
	if models != "" {
		if err := json.Unmarshal([]byte(models), &p.Models); err != nil {
			return nil, fmt.Errorf("store: decode provider models: %w", err)
		}
	}
	if p.Models == nil {
		p.Models = []contracts.ModelSpec{}
	}
	return &p, nil
}

func modelsOrEmpty(m []contracts.ModelSpec) []contracts.ModelSpec {
	if m == nil {
		return []contracts.ModelSpec{}
	}
	return m
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// UpdateProvider patches a provider the caller owns.
//
// Patching rather than delete-and-recreate matters for the key: rotating a URL or
// a model list must not throw away a key the user pasted once. An empty KeyCipher
// means "leave the stored key alone"; callers that mean "remove the key" pass
// ClearKey.
func (s *Store) UpdateProvider(ctx context.Context, userID, providerID string, in ProviderPatch) (*contracts.Provider, error) {
	current, err := s.Provider(ctx, userID, providerID)
	if err != nil {
		return nil, err
	}
	if current.Source != contracts.SourceCustom {
		// An admin's shared provider is not a user's to edit, even when it is
		// visible to them; editing it would silently change every other tenant.
		return nil, fmt.Errorf("store: update provider %q: %w", providerID, ErrForbidden)
	}

	sets := []string{}
	args := []any{}
	if in.API != nil {
		if strings.TrimSpace(*in.API) == "" {
			return nil, fmt.Errorf("store: update provider: api is required: %w", ErrInvalid)
		}
		sets = append(sets, "api = ?")
		args = append(args, *in.API)
	}
	if in.BaseURL != nil {
		if strings.TrimSpace(*in.BaseURL) == "" {
			return nil, fmt.Errorf("store: update provider: base url is required: %w", ErrInvalid)
		}
		sets = append(sets, "base_url = ?")
		args = append(args, *in.BaseURL)
	}
	if in.Models != nil {
		models, merr := json.Marshal(modelsOrEmpty(in.Models))
		if merr != nil {
			return nil, fmt.Errorf("store: update provider %q: encode models: %w", providerID, merr)
		}
		sets = append(sets, "models = ?")
		args = append(args, string(models))
	}
	if len(in.KeyCipher) > 0 {
		sets = append(sets, "key_cipher = ?")
		args = append(args, in.KeyCipher)
	} else if in.ClearKey {
		sets = append(sets, "key_cipher = NULL")
	}
	if in.Enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolInt(*in.Enabled))
	}
	if len(sets) == 0 {
		return current, nil
	}
	// The providers table has no updated_at column: created_at is the only
	// timestamp it keeps, and adding one means a migration for a field nothing
	// reads. The edit is still observable through the row it changes.
	args = append(args, providerID)

	res, err := s.w.ExecContext(ctx,
		`UPDATE providers SET `+strings.Join(sets, ", ")+` WHERE id = ? AND scope = 'user'`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: update provider %q: %w", providerID, err)
	}
	if err := requireOneRow(res, "store: update provider "+providerID); err != nil {
		return nil, err
	}
	return s.Provider(ctx, userID, providerID)
}

// ProviderPatch is a partial provider update; nil means "leave as is".
type ProviderPatch struct {
	API       *string
	BaseURL   *string
	Models    []contracts.ModelSpec
	KeyCipher []byte
	ClearKey  bool
	Enabled   *bool
}
