package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/store"
)

// defaultProviderWarning describes a deployment whose default provider does not
// exist, or returns "" when it does.
//
// "-default-provider" (or PIFORGE_DEFAULT_PROVIDER) is what a session with no
// model_ref uses. A name nobody created is accepted at boot today, and the first
// signal is a failed send much later: the acceptance pass started with
// PIFORGE_DEFAULT_PROVIDER=volc and no volc provider, which surfaced as 400
// no_model on the first message (and, with an older agent process, as a 500).
//
// This is deliberately a warning, not a startup error: an operator may add the
// provider from the UI after the boot, and refusing to start would turn a
// recoverable misconfiguration into an outage. The consequence is named here
// because a bare "not found" does not tell anyone what will break.
func defaultProviderWarning(ctx context.Context, st *store.Store, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || st == nil {
		return "", nil
	}
	exists, err := st.ProviderNamed(ctx, name)
	if err != nil {
		return "", err
	}
	if exists {
		return "", nil
	}
	return fmt.Sprintf(
		"default provider %q does not exist: sessions that follow the default will answer 400 no_model "+
			"until a provider with this name is added (Settings → 模型与凭据) or -default-provider is "+
			"changed to a provider that exists", name), nil
}
