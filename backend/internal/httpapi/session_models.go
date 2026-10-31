package httpapi

import (
	"context"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// Session model lifecycle.
//
// A session row outlives the providers it was created with. The acceptance pass
// deleted and re-created the deployment's provider while a session existed, and
// from then on every send to that session was a 500: the stored `model_ref`
// named a provider that no longer existed, the agent process that had been
// spawned while it was missing kept a model configuration with no key, and
// nothing between the row and the prompt ever looked again. Retrying could not
// help, because nothing in the path re-read the provider list.
//
// The fix has two halves, both here:
//
//   - resolveSessionModel re-resolves a reference whose provider is *gone* onto
//     the account's current default (or the first usable provider), persists
//     the new reference and tells the caller where it came from. A provider
//     that exists but has no key is left alone: that one has a specific,
//     actionable error, and falling back would hide it.
//   - restartForUnconfiguredAgent replaces a live agent that was spawned while
//     no model could be configured. Such a process keeps that empty
//     configuration for its whole life — models.json and the gateway token are
//     minted per spawn — so the only way a restored provider reaches it is a
//     new process.

// sessionModel is what one send should actually use.
type sessionModel struct {
	// Ref is the reference to pin on the session ("" means "follow the
	// deployment default").
	Ref string
	// FallbackFrom is the stored reference, or the provider name, that no
	// longer resolved. Non-empty only when Ref was re-resolved.
	FallbackFrom string
}

// resolveSessionModel decides which model a send runs with.
//
// It returns a typed no_model error only where the old code's explanation was
// already right: an explicit reference to a provider that exists but cannot
// answer (no key, no models). A reference whose provider is gone is re-resolved
// instead, because "deleted and re-created with the same name" and "renamed"
// are both ordinary operator actions and neither should kill a conversation.
func (s *server) resolveSessionModel(ctx context.Context, userID string, sess *contracts.Session) (sessionModel, error) {
	stored := strings.TrimSpace(sess.ModelRef)
	providerName, modelID := modelParts(stored)

	if providerName == "" {
		// No pinned model: the deployment default applies, exactly as the
		// runtime resolves it. If that default no longer exists, an old session
		// can still run on any usable provider — pinning it is the recovery,
		// and the response names the switch.
		def := strings.TrimSpace(s.deps.DefaultProvider)
		if def == "" {
			return sessionModel{Ref: stored}, nil
		}
		if provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, def); err == nil && providerUsable(provider) {
			return sessionModel{Ref: stored}, nil
		}
		if ref, ok := s.firstUsableModelRef(ctx, userID, ""); ok {
			return sessionModel{Ref: ref, FallbackFrom: def}, nil
		}
		return sessionModel{Ref: stored}, nil
	}

	provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, providerName)
	if err != nil || provider == nil {
		// The provider is gone (deleted, renamed, disabled). Prefer a working
		// model over a dead conversation.
		if ref, ok := s.firstUsableModelRef(ctx, userID, providerName); ok {
			return sessionModel{Ref: ref, FallbackFrom: stored}, nil
		}
		return sessionModel{}, apiErr(contracts.ErrNoModel,
			"the provider %q is not configured: add it in Settings → Models", providerName)
	}
	// It exists; say precisely what is wrong with it rather than silently
	// answering from another provider.
	if len(provider.Models) == 0 && modelID == "" {
		return sessionModel{}, apiErr(contracts.ErrNoModel,
			"the provider %q lists no models: add a model id in Settings → Models", providerName)
	}
	if !provider.HasKey {
		return sessionModel{}, apiErr(contracts.ErrNoModel,
			"the provider %q has no API key: add one in Settings → Models", providerName)
	}
	return sessionModel{Ref: stored}, nil
}

// resolvedModelRef is what the header may name: the pinned reference when it
// resolves, otherwise the default/fallback provider's first model. It never
// invents a model, so an unconfigured account reads 未配置 instead of `unknown`.
func (s *server) resolvedModelRef(ctx context.Context, userID string, sess *contracts.Session) string {
	if sess == nil || s.deps.Store == nil {
		return ""
	}
	stored := strings.TrimSpace(sess.ModelRef)
	if providerName, modelID := modelParts(stored); providerName != "" {
		provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, providerName)
		if err == nil && provider != nil {
			// The provider exists: the name is the user's choice even when it
			// cannot answer yet (no key). The no-model bar explains that; the
			// header must not silently rename it.
			if modelID != "" {
				return stored
			}
			for _, model := range provider.Models {
				if id := strings.TrimSpace(model.ID); id != "" {
					return contracts.ModelRef(providerName, id)
				}
			}
			return ""
		}
		// The reference is dead: name what the next send would actually use.
		if ref, ok := s.firstUsableModelRef(ctx, userID, providerName); ok {
			return ref
		}
		return ""
	}
	if def := strings.TrimSpace(s.deps.DefaultProvider); def != "" {
		if provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, def); err == nil && providerUsable(provider) {
			for _, model := range provider.Models {
				if id := strings.TrimSpace(model.ID); id != "" {
					return contracts.ModelRef(def, id)
				}
			}
		}
	}
	ref, ok := s.firstUsableModelRef(ctx, userID, "")
	if !ok {
		return ""
	}
	return ref
}

// firstUsableModelRef returns the first provider/model the account can actually
// send to, excluding the names in `exclude`.
//
// The deployment default is preferred over alphabetical order so a session
// falls back to the account's default before it picks somebody's personal
// provider. The merged list is sorted by name, so the result is deterministic
// across calls — a fallback that changed between two sends would be worse than
// the 500 it replaces.
func (s *server) firstUsableModelRef(ctx context.Context, userID string, exclude ...string) (string, bool) {
	if s.deps.Store == nil {
		return "", false
	}
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		if name = strings.TrimSpace(name); name != "" {
			skip[name] = true
		}
	}
	refFor := func(provider *contracts.Provider) (string, bool) {
		if provider == nil || skip[provider.Name] || !providerUsable(provider) {
			return "", false
		}
		for _, model := range provider.Models {
			if id := strings.TrimSpace(model.ID); id != "" {
				return contracts.ModelRef(provider.Name, id), true
			}
		}
		return "", false
	}

	if def := strings.TrimSpace(s.deps.DefaultProvider); def != "" && !skip[def] {
		if provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, def); err == nil {
			if ref, ok := refFor(provider); ok {
				return ref, true
			}
		}
	}
	merged, err := s.mergedProviders(ctx, userID)
	if err != nil {
		return "", false
	}
	for _, provider := range merged {
		if ref, ok := refFor(provider); ok {
			return ref, true
		}
	}
	return "", false
}

// sessionModelUsable reports whether a reference can answer a prompt right now.
// It is the gate for restarting an agent that was spawned without a model: a
// restart only helps once there is something to configure.
func (s *server) sessionModelUsable(ctx context.Context, userID, ref string) bool {
	if s.deps.Store == nil {
		return false
	}
	providerName, modelID := modelParts(ref)
	if providerName == "" {
		providerName = strings.TrimSpace(s.deps.DefaultProvider)
	}
	if providerName == "" {
		return false
	}
	provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, providerName)
	if err != nil || provider == nil || !provider.HasKey {
		return false
	}
	return len(provider.Models) > 0 || modelID != ""
}

// modelConfigRuntime is implemented by a runtime that knows whether the live
// process was spawned with a usable platform model configuration. It is an
// optional capability: a fake runtime does not have to answer it.
type modelConfigRuntime interface {
	StartedModelConfig(sessionID string) (configured bool, live bool)
}

// restartForUnconfiguredAgent stops a live agent that was spawned while no
// model could be configured, so the next Start mints fresh models.json and a
// gateway token that point at a provider that now exists.
//
// Only that case restarts: a process with a working configuration keeps it.
// The session's reference must resolve *now* — restarting without a usable
// model would loop, and the prompt failure already explains the configuration.
func (s *server) restartForUnconfiguredAgent(ctx context.Context, userID string, sess *contracts.Session) (bool, error) {
	if sess == nil || s.deps.Runtime == nil {
		return false, nil
	}
	aware, ok := s.deps.Runtime.(modelConfigRuntime)
	if !ok {
		return false, nil
	}
	configured, live := aware.StartedModelConfig(sess.ID)
	if !live || configured {
		return false, nil
	}
	if !s.sessionModelUsable(ctx, userID, sess.ModelRef) {
		return false, nil
	}
	// Never kill a run that is in flight; the next send after it settles
	// rebuilds the process.
	if status, err := s.deps.Runtime.Status(ctx, sess.ID); err == nil && (status.Running || status.Streaming) {
		return false, nil
	}
	if err := s.deps.Runtime.Stop(ctx, sess.ID); err != nil {
		return false, apiErr(contracts.ErrInternal, "重建 agent 时没能停掉旧进程：%v", err)
	}
	s.log.InfoContext(ctx, "restarting an agent that was spawned without a model",
		"session", sess.ID, "model_ref", sess.ModelRef)
	return true, nil
}

// pinSessionModel persists a re-resolved reference.
//
// A failure here does not fail the send: the turn can still run with the new
// reference, and the next send re-resolves again. Losing the pin would only
// cost one extra resolution.
func (s *server) pinSessionModel(ctx context.Context, userID string, sess *contracts.Session, ref string) {
	if sess == nil || ref == strings.TrimSpace(sess.ModelRef) {
		sess.ModelRef = ref
		return
	}
	updated, err := s.deps.Store.UpdateSession(ctx, userID, sess.ID, store.SessionPatch{ModelRef: &ref})
	if err != nil {
		s.log.WarnContext(ctx, "could not pin the re-resolved model",
			"session", sess.ID, "model_ref", ref, "error", err.Error())
		sess.ModelRef = ref
		return
	}
	*sess = *updated
}
