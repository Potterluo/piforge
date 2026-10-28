package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// sessionResponse wraps a single session; the client reads `session`, so a
// bare object would break it.
type sessionResponse struct {
	Session *contracts.Session `json:"session"`
}

// sessionsResponse is the list envelope with keyset pagination.
type sessionsResponse struct {
	Sessions   []*contracts.Session `json:"sessions"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// sessionDetail is GET /v1/sessions/{id}: the persisted session, the live
// runtime status (so the header can show "running") and its project (so the
// workbench does not need a second round trip).
type sessionDetail struct {
	Session *contracts.Session      `json:"session"`
	Status  contracts.RuntimeStatus `json:"status"`
	Project *contracts.Project      `json:"project"`
}

// entriesResponse mirrors Pi's get_entries: a page of the tree plus the leaf
// the client is currently on.
type entriesResponse struct {
	Entries []*contracts.TreeEntry `json:"entries"`
	LeafID  string                 `json:"leaf_id"`
}

// treeResponse is the whole branch tree.
type treeResponse struct {
	Tree   []*contracts.TreeEntry `json:"tree"`
	LeafID string                 `json:"leaf_id"`
}

// forkPointsResponse lists the messages a branch may be forked at.
type forkPointsResponse struct {
	Points []contracts.ForkPoint `json:"points"`
	// Reason explains an empty list: the panel used to claim "no messages to fork
	// from" whenever the agent happened to be stopped.
	Reason string `json:"reason,omitempty"`
}

// handleListSessions lists the caller's sessions, newest activity first.
func (s *server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())

	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	// Ask for one extra row so "is there another page" needs no COUNT(*).
	filter := store.SessionFilter{
		ProjectID: r.URL.Query().Get("project"),
		RootID:    r.URL.Query().Get("root"),
		Status:    contracts.SessionStatus(r.URL.Query().Get("status")),
		Kind:      contracts.SessionKind(r.URL.Query().Get("kind")),
		Query:     r.URL.Query().Get("q"),
		Cursor:    r.URL.Query().Get("cursor"),
		Limit:     limit + 1,
	}
	sessions, err := s.deps.Store.ListSessions(r.Context(), user.ID, filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	out := sessionsResponse{Sessions: sessions}
	if len(sessions) > limit {
		out.Sessions = sessions[:limit]
		out.NextCursor = sessions[limit-1].ID
	}
	if out.Sessions == nil {
		out.Sessions = []*contracts.Session{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetSession returns one session with its live status and project.
func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	project, err := s.deps.Store.Project(r.Context(), user.ID, sess.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionDetail{
		Session: sess,
		Status:  s.bestEffortStatus(r.Context(), sess.ID),
		Project: project,
	})
}

// handleDeleteSession removes a session, its event log and its messages.
//
// A running agent is stopped first: killing it afterwards would leave a child
// writing into a row that no longer exists. The worktree stays on disk for the
// provisioner to reclaim — deleting a directory under a live process is how
// cleanup turns into corruption.
func (s *server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sessionID := pathID(r)
	if _, err := s.deps.Store.Session(r.Context(), user.ID, sessionID); err != nil {
		s.fail(w, r, err)
		return
	}
	if rt, err := s.requireRuntime(); err == nil {
		stopCtx, cancel := detach(r.Context(), 10*time.Second)
		_, _ = rt.Interrupt(stopCtx, sessionID)
		_ = rt.Stop(stopCtx, sessionID)
		cancel()
	}
	if err := s.deps.Store.DeleteSession(r.Context(), user.ID, sessionID); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

// handlePatchSession renames, re-models or archives a session.
func (s *server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sessionID := pathID(r)

	if _, err := s.deps.Store.Session(r.Context(), user.ID, sessionID); err != nil {
		s.fail(w, r, err)
		return
	}

	var in struct {
		Title         *string `json:"title"`
		Label         *string `json:"label"`
		ModelRef      *string `json:"model_ref"`
		ThinkingLevel *string `json:"thinking_level"`
		Status        *string `json:"status"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	patch := store.SessionPatch{
		Title:         in.Title,
		Label:         in.Label,
		ModelRef:      in.ModelRef,
		ThinkingLevel: in.ThinkingLevel,
	}
	if in.Status != nil {
		status := contracts.SessionStatus(*in.Status)
		patch.Status = &status
	}

	sess, err := s.deps.Store.UpdateSession(r.Context(), user.ID, sessionID, patch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{Session: sess})
}

// handleSessionStatus returns the live runtime status as a flat object.
func (s *server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.bestEffortStatus(r.Context(), sess.ID))
}

// handleSessionStats aggregates token, cost and context usage.
func (s *server) handleSessionStats(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sessionID := pathID(r)

	st, err := s.deps.Store.SessionStats(r.Context(), user.ID, sessionID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Context occupancy comes from the newest context.usage event rather than
	// from a counter, because the model's real window is the agent's business.
	if tokens, window, ok, err := s.deps.Store.LastContextUsage(r.Context(), user.ID, sessionID); err == nil && ok {
		st.ContextTokens = tokens
		st.ContextWindow = window
		if window > 0 {
			st.ContextPercent = float64(tokens) / float64(window) * 100
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleEntries proxies Pi's get_entries for reconnect catch-up.
func (s *server) handleEntries(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// `?since=0` is the natural way to say "everything", and Pi treats it as an
	// entry id that does not exist ("Entry not found: 0"), so it is normalised to
	// the empty cursor Pi understands.
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since == "0" {
		since = ""
	}
	entries, leaf, err := rt.Entries(r.Context(), sess.ID, since)
	if err != nil {
		if noLiveProcess(err) {
			writeJSON(w, http.StatusOK, entriesResponse{Entries: []*contracts.TreeEntry{}, LeafID: ""})
			return
		}
		s.fail(w, r, err)
		return
	}
	if entries == nil {
		entries = []*contracts.TreeEntry{}
	}
	writeJSON(w, http.StatusOK, entriesResponse{Entries: entries, LeafID: leaf})
}

// handleTree returns the append-only session tree.
func (s *server) handleTree(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tree, err := rt.Tree(r.Context(), sess.ID)
	if err != nil {
		if noLiveProcess(err) {
			writeJSON(w, http.StatusOK, treeResponse{Tree: []*contracts.TreeEntry{}, LeafID: ""})
			return
		}
		s.fail(w, r, err)
		return
	}
	if tree == nil {
		tree = []*contracts.TreeEntry{}
	}
	writeJSON(w, http.StatusOK, treeResponse{Tree: tree, LeafID: deepestLeaf(tree)})
}

// handleForkPoints lists the user messages a branch can be started from. Pi
// only forks at user messages, so offering anything else would create a
// branch that cannot exist.
func (s *server) handleForkPoints(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	points, err := rt.ForkPoints(r.Context(), sess.ID)
	if err != nil {
		if noLiveProcess(err) {
			// Forking stops the parent's process (Pi rebinds it to the branch), so a
			// second fork used to find no process and no points — one fork per
			// session. Restart the parent from its stored row and ask again; the
			// event-log fallback stays as a last resort for a session whose agent
			// cannot start.
			if started := s.restartAgent(r, user.ID, sess); started {
				if retried, rerr := rt.ForkPoints(r.Context(), sess.ID); rerr == nil {
					if retried == nil {
						retried = []contracts.ForkPoint{}
					}
					writeJSON(w, http.StatusOK, forkPointsResponse{Points: retried})
					return
				}
			}
			fallback, ferr := s.forkPointsFromEvents(r.Context(), user.ID, sess.ID)
			if ferr != nil {
				s.fail(w, r, ferr)
				return
			}
			writeJSON(w, http.StatusOK, forkPointsResponse{Points: fallback, Reason: "no_agent"})
			return
		}
		s.fail(w, r, err)
		return
	}
	if points == nil {
		points = []contracts.ForkPoint{}
	}
	// Pi reports the messages but not when they were said; borrow the timestamp
	// from the persisted event with the same entry id.
	for i := range points {
		if !points[i].TS.IsZero() {
			continue
		}
		if ts, ok := s.entryTimestamps(r.Context(), user.ID, sess.ID)[points[i].EntryID]; ok {
			points[i].TS = ts
		}
	}
	writeJSON(w, http.StatusOK, forkPointsResponse{Points: points})
}

// handleFork creates a branch session and asks the runtime to fork the agent
// session at the given user message (PRD §5.1.2).
func (s *server) handleFork(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	parent, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var in struct {
		EntryID     string `json:"entry_id"`
		Label       string `json:"label"`
		CopyChanges bool   `json:"copy_changes"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.EntryID) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "entry_id is required"))
		return
	}

	rootID := parent.RootID
	if rootID == "" {
		rootID = parent.ID
	}
	title := in.Label
	if title == "" {
		title = "branch of " + parent.Title
	}
	child, err := s.deps.Store.CreateSession(r.Context(), user.ID, store.NewSession{
		ProjectID:   parent.ProjectID,
		Kind:        contracts.KindBranch,
		RootID:      rootID,
		ParentID:    parent.ID,
		ForkEntryID: in.EntryID,
		Label:       in.Label,
		Title:       title,
		ModelRef:    parent.ModelRef,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A branch works on its own worktree, so provision it before the client can
	// open it. Best effort: a session without a checkout is still usable for
	// prompting, and the workspace endpoints report the missing sandbox.
	child = s.provisionSession(r.Context(), w, user.ID, child)

	if err := rt.Fork(r.Context(), contracts.ForkRequest{
		SessionID:   parent.ID,
		EntryID:     in.EntryID,
		NewSession:  child.ID,
		Label:       in.Label,
		CopyChanges: in.CopyChanges,
	}); err != nil {
		// The row exists so the user can see what failed and retry; mark it
		// failed rather than deleting evidence.
		failed := contracts.StatusFailed
		if _, uerr := s.deps.Store.UpdateSession(context.WithoutCancel(r.Context()), user.ID, child.ID,
			store.SessionPatch{Status: &failed}); uerr != nil {
			s.log.WarnContext(r.Context(), "could not mark forked session failed",
				"session", child.ID, "error", uerr.Error())
		}
		s.fail(w, r, err)
		return
	}

	// Start the branch from the row, not from the parent's runtime spec: the spec
	// carries the model reference after translation, and minting a gateway token
	// from it produced "provider piforge is not available" on the first call.
	startCtx, startCancel := detach(r.Context(), 30*time.Second)
	if err := s.hub.ensure(startCtx, user.ID, s.sessionSpec(user.ID, child)); err != nil {
		startCancel()
		s.log.WarnContext(r.Context(), "could not start the branch",
			"session", child.ID, "error", err.Error())
	} else {
		startCancel()
	}

	// Copy the transcript only if the branch really has history in the agent. The
	// check needs the branch process to exist, so it runs after the start.
	// Forking at the *first* user message makes Pi create an empty session (its
	// parent is null, so there is nothing to truncate to), and seeding the
	// transcript for that case is exactly the fiction a user test caught: a branch
	// that displayed the conversation while the model denied ever seeing it.
	if s.branchHasHistory(r, child.ID) {
		if err := s.seedForkedHistory(context.WithoutCancel(r.Context()), user.ID, parent.ID, child.ID, in.EntryID); err != nil {
			s.log.WarnContext(r.Context(), "could not copy history into the branch",
				"session", child.ID, "error", err.Error())
		}
	}

	writeJSON(w, http.StatusCreated, sessionResponse{Session: child})
}

// handleSidecar attaches a side conversation to a session (PRD §5.1.4). It
// shares the parent's worktree, which is what makes it cheap: no clone, no
// install, no second sandbox.
func (s *server) handleSidecar(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	parent, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var in struct {
		ModelRef      string `json:"model_ref"`
		ReadOnlyTools *bool  `json:"readonly_tools"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	rootID := parent.RootID
	if rootID == "" {
		rootID = parent.ID
	}

	// At most three sidecars per tree (FR-4.6). The cost of not enforcing this
	// is a fourth agent process per user, which is real money on a shared box.
	existing, err := s.deps.Store.ListSessions(r.Context(), user.ID, store.SessionFilter{
		RootID: rootID,
		Kind:   contracts.KindSidecar,
		Limit:  200,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(existing) >= 3 {
		s.fail(w, r, apiErr(contracts.ErrConflict, "at most 3 sidecars per session"))
		return
	}

	modelRef := in.ModelRef
	if modelRef == "" {
		modelRef = parent.ModelRef
	}
	child, err := s.deps.Store.CreateSession(r.Context(), user.ID, store.NewSession{
		ProjectID:  parent.ProjectID,
		WorktreeID: parent.WorktreeID,
		Kind:       contracts.KindSidecar,
		RootID:     rootID,
		ParentID:   parent.ID,
		Title:      "Sidecar",
		ModelRef:   modelRef,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sessionResponse{Session: child})
}

// handleCompact asks the agent to compact its context on demand.
func (s *server) handleCompact(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// web/lib/api/client.ts sends {custom_instructions}; `instructions` is
	// accepted as an alias for direct API use.
	var in struct {
		CustomInstructions string `json:"custom_instructions"`
		Instructions       string `json:"instructions"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	instructions := in.CustomInstructions
	if instructions == "" {
		instructions = in.Instructions
	}
	if err := rt.Compact(r.Context(), sess.ID, instructions); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, okBody{OK: true})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requireRuntime reports a 503 when the agent runtime was not wired in, which
// keeps partial builds (and tests of other endpoints) usable.
func (s *server) requireRuntime() (contracts.AgentRuntime, error) {
	if s.deps.Runtime == nil {
		return nil, withStatus(http.StatusServiceUnavailable, contracts.ErrInternal, "agent runtime is not configured")
	}
	return s.deps.Runtime, nil
}

// bestEffortStatus returns the runtime status, degrading to a stopped-looking
// status when the runtime is missing or unhappy. Session detail must render
// even when the agent process is down: the transcript still matters.
func (s *server) bestEffortStatus(ctx context.Context, sessionID string) contracts.RuntimeStatus {
	st := contracts.RuntimeStatus{SessionID: sessionID}
	if s.deps.Runtime == nil {
		return st
	}
	live, err := s.deps.Runtime.Status(ctx, sessionID)
	if err != nil {
		s.log.DebugContext(ctx, "runtime status unavailable",
			"session", sessionID, "error", err.Error())
		return st
	}
	if live.SessionID == "" {
		live.SessionID = sessionID
	}
	return live
}

// worktreeRoot resolves the absolute directory a session's workspace services
// should operate on.
//
// Two distinct failures, two distinct statuses: a missing workspace service is
// a deployment problem (503), a session without a worktree is a provisioning
// state the UI should show as "preparing" (422 sandbox_unavailable).
func (s *server) worktreeRoot(ctx context.Context, userID string, sess *contracts.Session) (string, error) {
	if s.deps.Workspace == nil {
		return "", withStatus(http.StatusServiceUnavailable, contracts.ErrSandboxUnavail,
			"workspace service is not available")
	}
	if sess.WorktreeID == "" {
		return "", withStatus(http.StatusUnprocessableEntity, contracts.ErrSandboxUnavail,
			"this session has no worktree yet")
	}
	wt, err := s.deps.Store.Worktree(ctx, userID, sess.WorktreeID)
	if err != nil {
		return "", err
	}
	return wt.Path, nil
}

// provisionSession attaches a working directory to a freshly created session
// and returns the session as the store now sees it.
//
// It is deliberately best-effort and detached from the request: provisioning a
// sandbox can clone a repository, which must survive the client closing the tab.
// A nil provisioner (tests, workspace-less deployments) is a no-op.
func (s *server) provisionSession(ctx context.Context, w http.ResponseWriter, userID string, sess *contracts.Session) *contracts.Session {
	if s.deps.Provisioner == nil || sess == nil {
		return sess
	}
	project, err := s.deps.Store.Project(ctx, userID, sess.ProjectID)
	if err != nil {
		s.log.WarnContext(ctx, "cannot provision without the project",
			"session", sess.ID, "error", err.Error())
		return sess
	}

	provisionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()

	if _, perr := s.deps.Provisioner.EnsureWorktree(provisionCtx, userID, project, sess); perr != nil {
		s.log.WarnContext(ctx, "session has no worktree yet",
			"session", sess.ID, "error", perr.Error())
		return sess
	}
	reloaded, rerr := s.deps.Store.Session(ctx, userID, sess.ID)
	if rerr != nil {
		return sess
	}
	return reloaded
}

// noLiveProcess reports whether err means "this session has no agent process
// yet". That is the normal state before the first prompt, so the read-only
// session endpoints answer with an empty payload instead of a 404: the session
// exists, its transcript is simply empty. A 404 here would make the workbench
// look broken on a fresh conversation.
func noLiveProcess(err error) bool {
	var apiErr *contracts.APIError
	return errors.As(err, &apiErr) && apiErr.Code == contracts.ErrNotFound
}

// deepestLeaf walks the last child of each node to find the tip of the tree.
// Pi's `get_tree` has no leaf concept of its own, and the client needs to know
// which branch is active.
func deepestLeaf(tree []*contracts.TreeEntry) string {
	leaf := ""
	nodes := tree
	for len(nodes) > 0 {
		node := nodes[len(nodes)-1]
		leaf = node.EntryID
		nodes = node.Children
	}
	return leaf
}

// forkPointsFromEvents reconstructs the forkable user messages from the persisted
// log, for sessions whose agent is not running.
func (s *server) forkPointsFromEvents(ctx context.Context, userID, sessionID string) ([]contracts.ForkPoint, error) {
	events, err := s.deps.Store.ListEvents(ctx, userID, sessionID, 0, 500)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.ForkPoint, 0, 8)
	seen := make(map[string]bool, 8)
	for _, event := range events {
		if event.Type != contracts.EvMsgFinal || event.EntryID == "" || seen[event.EntryID] {
			continue
		}
		var payload struct {
			Role  string `json:"role"`
			Text  string `json:"text"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payload.Role != "user" || strings.TrimSpace(payload.Text) == "" {
			continue
		}
		seen[event.EntryID] = true
		out = append(out, contracts.ForkPoint{
			EntryID: event.EntryID,
			Preview: previewText(payload.Text, 120),
			// Without a timestamp the UI renders Go's zero time as "1/1/1".
			TS: event.TS,
		})
	}
	return out, nil
}

// previewText is the one-line preview a fork point shows.
func previewText(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= limit {
		return flat
	}
	return string(runes[:limit]) + "…"
}

// entryTimestamps maps entry ids to when they were recorded.
func (s *server) entryTimestamps(ctx context.Context, userID, sessionID string) map[string]time.Time {
	out := make(map[string]time.Time, 64)
	events, err := s.deps.Store.ListEvents(ctx, userID, sessionID, 0, 500)
	if err != nil {
		return out
	}
	for _, event := range events {
		if event.EntryID != "" {
			out[event.EntryID] = event.TS
		}
	}
	return out
}

// sessionSpec builds the runtime spec for a session from its stored row.
func (s *server) sessionSpec(userID string, sess *contracts.Session) contracts.SessionSpec {
	spec := contracts.SessionSpec{
		SessionID:     sess.ID,
		UserID:        userID,
		Kind:          sess.Kind,
		ProjectID:     sess.ProjectID,
		ModelRef:      sess.ModelRef,
		ReadOnlyTools: sess.Kind == contracts.KindSidecar,
	}
	if root, err := s.worktreeRoot(context.Background(), userID, sess); err == nil {
		spec.Worktree = root
	}
	return spec
}

// branchHasHistory reports whether the forked agent session actually carries
// messages. A fork at the first user message produces an empty branch, and the
// transcript must agree with the model about that.
func (s *server) branchHasHistory(r *http.Request, sessionID string) bool {
	rt, err := s.requireRuntime()
	if err != nil {
		return false
	}
	entries, _, err := rt.Entries(r.Context(), sessionID, "")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Type == "message" {
			return true
		}
	}
	return false
}

// restartAgent brings a session's agent back up from its stored row.
//
// It is what makes a second fork possible: forking stops the parent's process by
// design, and the fork points live in that process.
func (s *server) restartAgent(r *http.Request, userID string, sess *contracts.Session) bool {
	ctx, cancel := detach(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.hub.ensure(ctx, userID, s.sessionSpec(userID, sess)); err != nil {
		s.log.WarnContext(r.Context(), "could not restart the agent for fork points",
			"session", sess.ID, "error", err.Error())
		return false
	}
	return true
}
