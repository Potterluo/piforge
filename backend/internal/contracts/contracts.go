// Package contracts holds the types shared by every PiForge subsystem:
// the HTTP API, the Pi runtime bridge, the workspace services and the tests.
//
// Nothing in this package may import another internal package. It is the
// bottom of the dependency graph, so that each subsystem can be developed and
// tested in isolation against these interfaces.
package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

// Role is the only permission axis the product has (PRD §3).
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// ---------------------------------------------------------------------------
// Session kinds (PRD §5.1)
// ---------------------------------------------------------------------------

// SessionKind distinguishes the three conversation shapes.
type SessionKind string

const (
	// KindMain is a normal conversation started by the user.
	KindMain SessionKind = "main"
	// KindBranch is forked from a main session or another branch. It owns its
	// own git worktree and its own Pi process.
	KindBranch SessionKind = "branch"
	// KindSidecar is a side conversation attached to a main session. It shares
	// the main session's worktree but has an independent context and, by
	// default, a read-only tool set.
	KindSidecar SessionKind = "sidecar"
)

// SessionStatus is the lifecycle of a conversation.
type SessionStatus string

const (
	StatusActive   SessionStatus = "active"
	StatusRunning  SessionStatus = "running"
	StatusIdle     SessionStatus = "idle"
	StatusPaused   SessionStatus = "paused"
	StatusSettled  SessionStatus = "settled"
	StatusFailed   SessionStatus = "failed"
	StatusArchived SessionStatus = "archived"
)

// SandboxState mirrors PRD §5.2 FR-5.1.
type SandboxState string

const (
	SandboxCreating   SandboxState = "CREATING"
	SandboxStarting   SandboxState = "STARTING"
	SandboxReady      SandboxState = "READY"
	SandboxBusy       SandboxState = "BUSY"
	SandboxIdle       SandboxState = "IDLE"
	SandboxPaused     SandboxState = "PAUSED"
	SandboxTerminated SandboxState = "TERMINATED"
	SandboxError      SandboxState = "ERROR"
	SandboxMissing    SandboxState = "MISSING"
)

// ---------------------------------------------------------------------------
// Domain objects
// ---------------------------------------------------------------------------

// User is an account. Admin users may manage users and shared providers but
// must not be able to read other users' conversations through a normal path.
type User struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        Role       `json:"role"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// Project is one git repository owned by one user (PRD §8.2).
type Project struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
	// Forge is where pull requests for this project are opened (FR-12.6). Nil
	// means "not configured yet"; the token is never part of this value.
	Forge *ForgeConfig `json:"forge,omitempty"`
	// Runtime state, not persisted directly:
	SandboxState SandboxState `json:"sandbox_state"`
	SessionCount int          `json:"session_count"`
	LastActive   *time.Time   `json:"last_active_at,omitempty"`
}

// ForgeConfig says where to open a pull request for one project (FR-12.6).
//
// This is deliberately per project rather than one deployment-wide token: a
// token identifies both a person and the repositories they may write to, and an
// operator who configures one global token would silently turn every project's
// PR into "opened as that account", including projects the token's owner never
// intended to touch. The cost is one more field to fill in per project; the
// alternative is a credential whose blast radius is the whole deployment.
//
// Kind selects the REST dialect. "github" also covers Gitea/Forgejo and GitHub
// Enterprise, which speak the same create-pull API; "gitlab" is the GitLab
// merge-request dialect. Any other value is refused when the config is saved.
type ForgeConfig struct {
	Kind string `json:"kind"`
	// APIBase is the API root, not the web page: https://api.github.com,
	// https://gitea.example.com/api/v1, https://gitlab.example.com/api/v4.
	APIBase string `json:"api_base"`
	// Repo is "owner/name" for GitHub-compatible forges and "group/project" (or
	// a numeric project id) for GitLab.
	Repo string `json:"repo"`
	// BaseBranch is the target branch. Empty means the project's default branch.
	BaseBranch string `json:"base_branch,omitempty"`
	// HasToken reports whether a token is sealed at rest. The token itself is
	// write-only: nothing reads it back over HTTP.
	HasToken bool `json:"has_token"`
}

// PullRequest is one pull request a session created (FR-12.6).
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	// Existing is true when the forge already had an open PR for this branch;
	// the response then reports it instead of creating a second one.
	Existing bool `json:"existing,omitempty"`
}

// Worktree is a git worktree inside a sandbox. One worktree backs one branch
// session (PRD §6.4-B).
type Worktree struct {
	ID         string    `json:"id"`
	SandboxID  string    `json:"sandbox_id"`
	UserID     string    `json:"user_id"`
	Path       string    `json:"path"`
	Branch     string    `json:"branch"`
	BaseCommit string    `json:"base_commit"`
	CreatedAt  time.Time `json:"created_at"`
}

// Session is a conversation. Main sessions and branches own a worktree;
// sidecars reference the same worktree as their main session.
type Session struct {
	ID            string        `json:"id"`
	UserID        string        `json:"user_id"`
	ProjectID     string        `json:"project_id"`
	WorktreeID    string        `json:"worktree_id,omitempty"`
	Kind          SessionKind   `json:"kind"`
	RootID        string        `json:"root_id,omitempty"`
	ParentID      string        `json:"parent_id,omitempty"`
	ForkEntryID   string        `json:"fork_entry_id,omitempty"`
	Label         string        `json:"label,omitempty"`
	Title         string        `json:"title"`
	Status        SessionStatus `json:"status"`
	ModelRef      string        `json:"model_ref,omitempty"`
	ThinkingLevel string        `json:"thinking_level,omitempty"`
	// Ephemeral sessions are scratch space (PRD FR-1.10): hidden from the list and
	// deleted after a few hours, so a quick question does not become an artifact.
	Ephemeral    bool      `json:"ephemeral,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// Run is one prompt → agent_settled cycle.
type Run struct {
	ID             string     `json:"id"`
	SessionID      string     `json:"session_id"`
	Trigger        string     `json:"trigger"`
	Status         string     `json:"status"`
	TokensIn       int64      `json:"tokens_in"`
	TokensOut      int64      `json:"tokens_out"`
	TokensCacheR   int64      `json:"tokens_cache_read"`
	TokensCacheW   int64      `json:"tokens_cache_write"`
	CostUSD        float64    `json:"cost_usd"`
	StartedAt      time.Time  `json:"started_at"`
	SettledAt      *time.Time `json:"settled_at,omitempty"`
	InterruptedBy  string     `json:"interrupted_by,omitempty"`
	CompletionNote string     `json:"completion_note,omitempty"`
}

// SessionStats is what the context meter renders (PRD §5.1.1 FR-1.8).
type SessionStats struct {
	SessionID      string  `json:"session_id"`
	UserMessages   int     `json:"user_messages"`
	AssistantMsgs  int     `json:"assistant_messages"`
	ToolCalls      int     `json:"tool_calls"`
	TokensIn       int64   `json:"tokens_in"`
	TokensOut      int64   `json:"tokens_out"`
	TokensCacheR   int64   `json:"tokens_cache_read"`
	CostUSD        float64 `json:"cost_usd"`
	ContextTokens  int64   `json:"context_tokens"`
	ContextWindow  int64   `json:"context_window"`
	ContextPercent float64 `json:"context_percent"`
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// Event is the normalized envelope for everything the UI renders. `Seq` is
// monotonic per session and doubles as the SSE event id, so a reconnecting
// client can replay from where it stopped (PRD §7.4.4).
type Event struct {
	Seq       int64           `json:"seq"`
	SessionID string          `json:"session_id"`
	BranchID  string          `json:"branch_id"`
	Kind      SessionKind     `json:"kind"`
	Type      string          `json:"type"`
	TS        time.Time       `json:"ts"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	// RunID is empty for events that are not part of a run.
	RunID string `json:"run_id,omitempty"`
	// EntryID/ParentID mirror Pi's session-tree ids when the event creates an
	// entry, which lets the UI build the branch tree without a second call.
	EntryID  string `json:"entry_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}

// Normalized event types. These are deliberately stable strings: the frontend
// switches on them and the tests assert on them.
const (
	EvRunStarted   = "run.started"
	EvRunSettled   = "run.settled"
	EvRunRetry     = "run.retry"
	EvRunAborted   = "run.aborted"
	EvTurnStarted  = "turn.started"
	EvTurnEnded    = "turn.ended"
	EvMsgStarted   = "message.started"
	EvMsgDelta     = "message.delta"
	EvMsgFinal     = "message.final"
	EvToolStarted  = "tool.started"
	EvToolProgress = "tool.progress"
	EvToolEnded    = "tool.ended"
	EvQueueChanged = "queue.updated"
	EvCompaction   = "context.compaction"
	EvContextUsage = "context.usage"
	EvPtyStream    = "pty.stream"
	EvPlatformErr  = "platform.error"
)

// ---------------------------------------------------------------------------
// Agent runtime (implemented by internal/pi, faked by internal/pitest)
// ---------------------------------------------------------------------------

// PromptRequest is what the HTTP layer hands to the runtime.
type PromptRequest struct {
	SessionID string
	Text      string
	Images    []Image
	// StreamingBehavior is "" (idle), "steer" or "follow_up". The UI sets it
	// when a run is already in flight (PRD §10.5.1).
	StreamingBehavior string
	ClientMsgID       string
	// ModelRef, when set, switches the model before the prompt is sent.
	ModelRef string
}

// Image is an inline attachment.
type Image struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

// PromptResult reports what happened to a submitted prompt.
type PromptResult struct {
	Accepted      bool   `json:"accepted"`
	Disposition   string `json:"disposition"` // started|queued|handled
	QueuePosition int    `json:"queue_position"`
	RunID         string `json:"run_id"`
	// ModelRef is the model this turn actually used when the control plane had
	// to re-resolve the session's reference (its provider was deleted). It is
	// empty when the stored reference was used as-is, so a replay of the same
	// message still compares equal to the first answer.
	ModelRef string `json:"model_ref,omitempty"`
	// ModelFallbackFrom is the reference (or provider name) that no longer
	// resolved. The UI turns the pair into one sentence so a run never silently
	// answers from a different model than the one on screen.
	ModelFallbackFrom string `json:"model_fallback_from,omitempty"`
}

// InterruptResult carries back the text the user had queued, so the composer
// can restore it (PRD §10.5.1).
type InterruptResult struct {
	ClearedSteering []string `json:"cleared_steering"`
	ClearedFollowUp []string `json:"cleared_follow_up"`
}

// ForkRequest asks the runtime to branch a session at a user message.
type ForkRequest struct {
	SessionID   string
	EntryID     string
	NewSession  string
	Label       string
	CopyChanges bool
}

// TreeEntry is one node of Pi's append-only session tree, projected for the UI.
type TreeEntry struct {
	EntryID  string          `json:"entry_id"`
	ParentID string          `json:"parent_id,omitempty"`
	Type     string          `json:"type"`
	Label    string          `json:"label,omitempty"`
	Role     string          `json:"role,omitempty"`
	Preview  string          `json:"preview,omitempty"`
	TS       time.Time       `json:"ts"`
	Children []*TreeEntry    `json:"children,omitempty"`
	Extra    json.RawMessage `json:"extra,omitempty"`
}

// ForkPoint is a user message that can serve as a fork origin. Pi's `fork`
// only accepts user messages, so the UI must not offer anything else.
type ForkPoint struct {
	EntryID string    `json:"entry_id"`
	Preview string    `json:"preview"`
	TS      time.Time `json:"ts"`
}

// RuntimeStatus is the runtime's view of one session process.
type RuntimeStatus struct {
	SessionID   string `json:"session_id"`
	Running     bool   `json:"running"`
	Streaming   bool   `json:"streaming"`
	Compacting  bool   `json:"compacting"`
	QueueDepth  int    `json:"queue_depth"`
	ModelRef    string `json:"model_ref,omitempty"`
	LastSeq     int64  `json:"last_seq"`
	ProcessID   int    `json:"process_id,omitempty"`
	SessionFile string `json:"session_file,omitempty"`
	// ResolvedModelRef is the model the *control plane* would use for this
	// session right now: the pinned reference when it resolves, otherwise the
	// deployment default (or the fallback the next send would pick). It exists
	// so the header can name a real model before an agent process exists; an
	// empty value is honestly "未配置", never the word "unknown".
	ResolvedModelRef string `json:"resolved_model_ref,omitempty"`
	// ModelUnavailable says there is nothing to send to at all: the pinned
	// provider is gone and no fallback exists, or the account has no usable
	// provider. The UI renders 未配置 from this fact rather than from a stale
	// provider list it may still be caching.
	ModelUnavailable bool `json:"model_unavailable,omitempty"`
}

// AgentRuntime is the only way the HTTP layer talks to an agent.
//
// Implementations must be safe for concurrent use per session, and must fan
// normalized events out through Subscribe before returning from Start.
type AgentRuntime interface {
	// Start ensures a live agent process exists for the session and returns a
	// subscription of normalized events.
	Start(ctx context.Context, spec SessionSpec) (<-chan Event, error)
	// Stop tears the process down (used when a session is archived or the
	// sandbox goes away).
	Stop(ctx context.Context, sessionID string) error
	// Prompt submits a user message.
	Prompt(ctx context.Context, req PromptRequest) (PromptResult, error)
	// Interrupt aborts the current run and clears the queues.
	Interrupt(ctx context.Context, sessionID string) (InterruptResult, error)
	// Status reports live process state.
	Status(ctx context.Context, sessionID string) (RuntimeStatus, error)
	// Fork creates a new session file branching at a user message.
	Fork(ctx context.Context, req ForkRequest) error
	// Tree returns the append-only session tree.
	Tree(ctx context.Context, sessionID string) ([]*TreeEntry, error)
	// Entries returns entries appended after `since` plus the current leaf.
	Entries(ctx context.Context, sessionID, since string) ([]*TreeEntry, string, error)
	// ForkPoints lists fusable user messages.
	ForkPoints(ctx context.Context, sessionID string) ([]ForkPoint, error)
	// Compact asks the agent to compact its context.
	Compact(ctx context.Context, sessionID, instructions string) error
}

// SessionSpec is everything the runtime needs to spawn an agent process.
type SessionSpec struct {
	SessionID string
	// UserID is who owns the session. The runtime needs it to mint a model
	// gateway token, and the gateway needs it to pick that user's provider.
	UserID     string
	Kind       SessionKind
	ProjectID  string
	Worktree   string // absolute path inside the sandbox
	ModelRef   string
	SkillsDir  string
	SystemHint string
	// ReadOnlyTools restricts the tool set; sidecars default to true.
	ReadOnlyTools bool
}

// ---------------------------------------------------------------------------
// Workspace (implemented by internal/gitx + internal/fsx)
// ---------------------------------------------------------------------------

// FileNode is one row of the file tree.
type FileNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"is_dir"`
	Size     int64       `json:"size,omitempty"`
	Modified time.Time   `json:"modified,omitempty"`
	GitState string      `json:"git_state,omitempty"` // M A D U ! or ""
	Children []*FileNode `json:"children,omitempty"`
	// Truncated marks a directory whose children were not expanded.
	Truncated bool `json:"truncated,omitempty"`
}

// FileContent is a preview payload. Binary starts true and Body is then empty;
// the UI shows a download card instead (PRD §5.3.2).
type FileContent struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Binary    bool      `json:"binary"`
	Truncated bool      `json:"truncated"`
	Body      string    `json:"body"`
	Language  string    `json:"language,omitempty"`
	MimeType  string    `json:"mime_type,omitempty"`
	Modified  time.Time `json:"modified"`
}

// Risk markers attached to a git change (PRD FR-12.4). A lockfile or a CI
// definition changes more than the file it names — it changes what everyone
// else builds and runs — so the review list must make it evident.
const (
	RiskLockfile = "lockfile"
	RiskCI       = "ci"
)

// GitStatus is the workspace's git state.
type GitStatus struct {
	Branch    string      `json:"branch"`
	Ahead     int         `json:"ahead"`
	Behind    int         `json:"behind"`
	Clean     bool        `json:"clean"`
	Changes   []GitChange `json:"changes"`
	Conflicts []string    `json:"conflicts,omitempty"`
	// Additions/Deletions are the totals over the whole change list, measured
	// against HEAD (staged and unstaged together). They are what the 变更 header
	// shows: "how big is this diff" should not need a second request.
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// GitChange is one path in the change list.
type GitChange struct {
	Path     string `json:"path"`
	Index    string `json:"index"`    // staged status letter
	Worktree string `json:"worktree"` // unstaged status letter
	// Line counts for this path against HEAD. An untracked file counts as pure
	// additions (or zero for a binary one, which has no lines to count).
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	// Risk marks a change the review should not skim: "lockfile" or "ci".
	// Computed here, not in the browser, so the API and every client agree on
	// what counts as risky.
	Risk string `json:"risk,omitempty"`
}

// GitCommit is a node of the commit DAG. Lane is assigned by the frontend; the
// backend only reports enough structure for it to do so (PRD §10.6).
type GitCommit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Parents []string  `json:"parents"`
	Author  string    `json:"author"`
	Email   string    `json:"email,omitempty"`
	TS      time.Time `json:"ts"`
	Subject string    `json:"subject"`
	Refs    []GitRef  `json:"refs,omitempty"`
}

// GitRef is a branch/tag/HEAD label on a commit.
type GitRef struct {
	Type string `json:"type"` // branch | tag | head | remote
	Name string `json:"name"`
}

// GitGraph is a page of the DAG.
type GitGraph struct {
	Commits    []GitCommit `json:"commits"`
	Head       string      `json:"head"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// FileDiff is one file's patch in a diff.
type FileDiff struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added|modified|deleted|renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Patch     string `json:"patch"`
}

// DiffScope selects what a diff compares (PRD §5.6 FR-12.1).
type DiffScope string

const (
	ScopeUnstaged DiffScope = "unstaged"
	ScopeStaged   DiffScope = "staged"
	ScopeBranch   DiffScope = "branch"
	ScopeCommit   DiffScope = "commit"
	// ScopeRange compares two explicit revisions: everything in To that is not
	// in From (`git diff From..To`). It is what a review means by "what this
	// commit or branch added" — HEAD~1..HEAD, main..HEAD — and it is the scope
	// the changes panel uses for a commit range (FR-12.1).
	ScopeRange DiffScope = "range"
)

// DiffRequest is a diff query.
type DiffRequest struct {
	Scope  DiffScope
	Path   string // optional single-file filter
	Commit string // required for ScopeCommit
	Base   string // optional base ref for ScopeBranch
	From   string // ScopeRange: the left side (exclusive), e.g. HEAD~1 or main
	To     string // ScopeRange: the right side; defaults to HEAD
}

// CommitRequest creates a commit from staged paths.
type CommitRequest struct {
	Message string
	Paths   []string
	Amend   bool
}

// SearchResult is one hit from a content or filename search.
type SearchResult struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}

// PRFile is one file a pull request would change, with its own line counts.
type PRFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // added | modified | deleted | renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// PRRange is what a pull request from base to HEAD would carry (FR-12.6).
//
// It is computed with `git log base..HEAD` and a three-dot diff, so a branch
// that is behind its base does not "un-change" the commits the base already
// has: the preview has to describe the review, not the raw delta of two tips.
type PRRange struct {
	Base    string `json:"base"`
	Head    string `json:"head"`
	HeadSHA string `json:"head_sha"`
	// CommitCount is exact; Commits is a capped list for display.
	CommitCount int         `json:"commit_count"`
	Commits     []GitCommit `json:"commits"`
	Files       []PRFile    `json:"files"`
	Additions   int         `json:"additions"`
	Deletions   int         `json:"deletions"`
}

// Workspace exposes the file and git operations the UI needs. Every method is
// scoped to one worktree directory; path traversal protection is the
// implementation's responsibility (PRD §6.3-坑6).
type Workspace interface {
	Tree(ctx context.Context, root, relPath string, depth int) ([]*FileNode, error)
	// TreeWithOptions is Tree with per-request flags. It exists because "show
	// ignored files" has to change what the server lists, not what the client
	// hides: the filtering happens here.
	TreeWithOptions(ctx context.Context, root, relPath string, depth int, includeIgnored bool) ([]*FileNode, error)
	ReadFile(ctx context.Context, root, relPath string) (*FileContent, error)
	// OpenFile opens relPath for a raw byte stream. Unlike ReadFile it does not
	// decode text, so it is the only correct way to serve a downloaded binary
	// file. The caller closes the reader. size is the number of bytes the
	// stream will produce, or -1 when the implementation cannot know it.
	OpenFile(ctx context.Context, root, relPath string) (io.ReadCloser, int64, error)
	WriteFile(ctx context.Context, root, relPath string, body []byte) error
	DeleteFile(ctx context.Context, root, relPath string) error
	Search(ctx context.Context, root, query string, byContent bool) ([]SearchResult, error)

	Status(ctx context.Context, root string) (*GitStatus, error)
	Diff(ctx context.Context, root string, req DiffRequest) ([]FileDiff, error)
	Log(ctx context.Context, root string, limit int, cursor string) (*GitGraph, error)
	Stage(ctx context.Context, root string, paths []string) error
	Unstage(ctx context.Context, root string, paths []string) error
	Commit(ctx context.Context, root string, req CommitRequest) (string, error)
	CreateBranch(ctx context.Context, root, name string) error
	Checkout(ctx context.Context, root, name string) error
	// Adopt carries one checkout's work into another (PRD FR-2.6).
	Adopt(ctx context.Context, sourceRoot, targetRoot, message string) (AdoptResult, error)

	// The remote workflow (PRD FR-8.7). Fast-forward only: a pull that needs a merge
	// is refused with ErrDiverged rather than half-done.
	// Hunk-level work (PRD FR-12.2). Index is a position in the diff the server
	// produced, never a patch from the client.
	StageHunk(ctx context.Context, root, path string, index int) error
	UnstageHunk(ctx context.Context, root, path string, index int) error
	DiscardHunk(ctx context.Context, root, path string, index int) error
	DiscardFile(ctx context.Context, root, path string) error

	Push(ctx context.Context, root string) (RemoteResult, error)
	Pull(ctx context.Context, root string) (RemoteResult, error)
	Fetch(ctx context.Context, root string) error
	AheadBehind(ctx context.Context, root string) (ahead, behind int, err error)
	// PRRange describes what a PR from base to HEAD would carry (FR-12.6).
	PRRange(ctx context.Context, root, base string) (PRRange, error)
}

// RemoteResult is what a push or a pull reports.
type RemoteResult struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	// Changed is false when a pull found nothing to bring down.
	Changed bool `json:"changed"`
}

// AdoptResult says what a branch hand-off carried (PRD FR-2.6).
//
// A branch can hand over two different things — commits and uncommitted work —
// and the result distinguishes them instead of collapsing both into one patch:
//
//   - Mode is merge | fast-forward | working-tree, or a "+working-tree" suffix
//     when commits were merged and loose edits were applied on top.
//   - Commits lists the branch commits the target now has (oldest first).
//   - MergeCommit is the commit created on the target for a non-fast-forward
//     merge; empty for a fast-forward or a working-tree-only hand-off.
//   - BranchCommit is the commit the branch-side uncommitted work was captured
//     in, when there was any.
//   - Commit is the id to look at: the target's new HEAD when commits travelled,
//     and the branch-side commit for a working-tree-only adopt.
type AdoptResult struct {
	Commit       string   `json:"commit"`
	Files        []string `json:"files"`
	Mode         string   `json:"mode"`
	Commits      []string `json:"commits"`
	MergeCommit  string   `json:"merge_commit,omitempty"`
	BranchCommit string   `json:"branch_commit,omitempty"`
}

// ---------------------------------------------------------------------------
// Sandbox (implemented by internal/sandbox)
// ---------------------------------------------------------------------------

// Sandbox is one isolated execution environment, keyed by (project, user).
type Sandbox struct {
	ID           string       `json:"id"`
	UserID       string       `json:"user_id"`
	ProjectID    string       `json:"project_id"`
	State        SandboxState `json:"state"`
	Provider     string       `json:"provider"`
	Node         string       `json:"node,omitempty"`
	Image        string       `json:"image"`
	RootDir      string       `json:"-"`
	LastActiveAt time.Time    `json:"last_active_at"`
	CreatedAt    time.Time    `json:"created_at"`
	ExpiresAt    *time.Time   `json:"expires_at,omitempty"`
	Message      string       `json:"message,omitempty"`

	// SandboxAPIKey is the key the sandbox's own HTTP API requires, as reported
	// by the provider that created the container. It is a secret: json:"-" keeps
	// it out of every API response and every log field that marshals a sandbox,
	// and the store persists it sealed (store.CreateSandbox / SandboxAPIKey).
	// It is only populated on the provider's create answer, which is the one
	// moment the control plane does not already know it.
	SandboxAPIKey string `json:"-"`
}

// SandboxProvider creates and destroys sandboxes.
//
// The local implementation runs commands directly on the host in a scratch
// directory, which is what development and the e2e suite use. A production
// deployment swaps in the Firecracker/gVisor implementation; nothing above
// this interface changes.
type SandboxProvider interface {
	Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*Sandbox, error)
	Get(ctx context.Context, sandboxID string) (*Sandbox, error)
	Pause(ctx context.Context, sandboxID string) error
	Resume(ctx context.Context, sandboxID string) (*Sandbox, error)
	Destroy(ctx context.Context, sandboxID string) error
	// Exec runs a command inside the sandbox and returns combined output.
	Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error)
}

// WorktreeSandbox is the optional half of SandboxProvider that creates a
// session's linked worktree inside the sandbox instead of on the control plane's
// own filesystem.
//
// It exists because a linked worktree's administrative files belong to whoever
// ran `git worktree add`: `<main>/.git/worktrees/<name>` and the `.git` file in
// the worktree itself. With a container provider the shell — and the panel's git
// — runs as the sandbox user, so a worktree created by the host is owned by
// another account (stage then dies on `<wt>/index.lock`), and its `.git` file
// records a host path the container cannot resolve. A provider that can run
// commands inside its sandbox implements this; providers without that capability
// keep using the control plane's git (provision.Config.Git) and the host layout,
// which is what -sandbox local needs.
type WorktreeSandbox interface {
	// AddWorktree creates a linked worktree of repoDir at path on branch,
	// creating branch from the repository's HEAD. All three paths are host-side
	// paths in the sandbox's workspace; translating them is the provider's job.
	AddWorktree(ctx context.Context, sandboxID, repoDir, path, branch string) error
}

// ---------------------------------------------------------------------------
// Providers (BYOK, PRD §5.7)
// ---------------------------------------------------------------------------

// ProviderSource tells the UI whether a model came from the admin or the user.
type ProviderSource string

const (
	SourceInherited ProviderSource = "inherited"
	SourceCustom    ProviderSource = "custom"
)

// Provider is a model endpoint. Secrets are never part of this struct.
type Provider struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	API       string         `json:"api"` // anthropic-messages | openai-responses | openai-compat
	BaseURL   string         `json:"base_url,omitempty"`
	Models    []ModelSpec    `json:"models"`
	Source    ProviderSource `json:"source"`
	Enabled   bool           `json:"enabled"`
	HasKey    bool           `json:"has_key"`
	CreatedAt time.Time      `json:"created_at"`
}

// ModelSpec is one model offered by a provider.
type ModelSpec struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	ContextWindow int64   `json:"context_window"`
	MaxTokens     int64   `json:"max_tokens"`
	Reasoning     bool    `json:"reasoning"`
	InputCost     float64 `json:"input_cost"`  // USD per 1M tokens
	OutputCost    float64 `json:"output_cost"` // USD per 1M tokens
}

// ModelRef joins a provider name and a model id: "openai/gpt-4o".
func ModelRef(provider, model string) string { return provider + "/" + model }

// ---------------------------------------------------------------------------
// Skills and MCP
// ---------------------------------------------------------------------------

// SkillScope mirrors the two-tier model of PRD §5.8.
type SkillScope string

const (
	SkillAdmin SkillScope = "admin"
	SkillUser  SkillScope = "user"
)

// Skill is one SKILL.md bundle.
type Skill struct {
	ID          string     `json:"id"`
	Scope       SkillScope `json:"scope"`
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	ContentHash string     `json:"content_hash"`
	// BundleKey names the directory under the host's skills root that holds
	// this skill's SKILL.md and assets. Empty means the content was never
	// uploaded, which is why the platform skips it instead of mounting a stub.
	BundleKey string `json:"bundle_key,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	Enabled   bool   `json:"enabled"`
	// Effective is computed per caller, never stored: true when this row is the
	// one the caller's new sessions mount under its name. A private row shadows
	// an enabled shared row with the same name; disabling the private row makes
	// the shared row effective again. See backend/internal/skills.
	Effective bool      `json:"effective"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionSkill is the immutable record of one skill version a session actually
// materialised into its checkout (PRD FR-14.5).
//
// It is deliberately a copy, not a join: `skills` describes what is configured
// *now*, while a running session must be able to say what it was given when it
// started. Editing a skill later changes the row in `skills`; this record keeps
// the version and the content hash it was mounted with, so a session stays
// reproducible after its skill has moved on.
type SessionSkill struct {
	SessionID string `json:"session_id"`
	SkillID   string `json:"skill_id"`
	Name      string `json:"name"`
	// Version is the skill's declared version at mount time ("0.0.0" when the
	// author never set one).
	Version string `json:"version"`
	// ContentHash is derived from the mounted bundle's files and content, so it
	// changes exactly when what the agent can read changes — which is what makes
	// it useful for two undeclared versions of the same skill.
	ContentHash string `json:"content_hash"`
	// Description is the one-line summary the palette shows; copied so the
	// record survives the skill being deleted.
	Description string    `json:"description,omitempty"`
	MountedPath string    `json:"mounted_path"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// MCPTransport is stdio or streamable HTTP. stdio is unrestricted by design
// (product decision): the user already has a shell.
type MCPTransport string

const (
	MCPStdio MCPTransport = "stdio"
	MCPHTTP  MCPTransport = "http"
)

// MCPServer is a user-configured MCP server. Secret values live in the vault,
// never in this struct.
type MCPServer struct {
	ID         string       `json:"id"`
	Scope      SkillScope   `json:"scope"`
	Name       string       `json:"name"`
	Transport  MCPTransport `json:"transport"`
	Command    string       `json:"command,omitempty"`
	Args       []string     `json:"args,omitempty"`
	Cwd        string       `json:"cwd,omitempty"`
	URL        string       `json:"url,omitempty"`
	Exposure   string       `json:"exposure"` // codemode|deferred|direct|hidden
	Enabled    bool         `json:"enabled"`
	HasSecrets bool         `json:"has_secrets"`
	// Secrets is write-only: it goes in on create and comes back as `has_secrets`.
	// Nothing in the API can read a stored credential (PRD FR-15.2).
	Secrets   string    `json:"secrets,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// APIError is the wire shape of every failure (PRD §9.7).
type APIError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Error codes used across the API.
const (
	ErrInvalidArgument = "invalid_argument"
	// ErrNoModel means the session has no usable model: no provider resolved, or
	// the provider lists no models. It is its own code because the remedy lives in
	// a specific place in the UI, and "internal error" sent users there blind.
	ErrNoModel         = "no_model"
	ErrUnauthenticated = "unauthenticated"
	ErrForbidden       = "forbidden"
	ErrNotFound        = "not_found"
	ErrSessionBusy     = "session_busy"
	// ErrBrowserBusy means another principal (user + session) holds the
	// sandbox browser's takeover lease (FR-11.3). The details name the holder
	// and the remaining time, because "busy" without a who or how long is not
	// something a user can act on.
	ErrBrowserBusy    = "browser_busy"
	ErrNeedFullResync = "need_full_resync"
	ErrBranchConflict = "branch_conflict"
	ErrFileTooLarge   = "file_too_large"
	ErrSandboxUnavail = "sandbox_unavailable"
	ErrSandboxPaused  = "sandbox_paused"
	// ErrSandboxDiskQuota means the session's sandbox checkout is over its disk
	// budget (FR-5.8). It is its own code because the remedy is "delete something
	// in the workspace", not "retry" and not "the server is broken"; the message
	// names the limit and the current usage.
	ErrSandboxDiskQuota = "sandbox_disk_quota"
	ErrRateLimited      = "rate_limited"
	ErrUpstreamOverload = "upstream_overloaded"
	ErrConflict         = "conflict"
	// ErrSharedProvider means the caller addressed a deployment-shared provider
	// through the per-user provider routes. The provider exists; the endpoint is
	// the wrong one. It is not `not_found`: a 404 there reads as "gone" and
	// sends an operator looking for the row that is right there in the list.
	ErrSharedProvider = "shared_provider"
	ErrInternal       = "internal"
	// ErrForge is a failure in the push → pull-request flow whose cause is the
	// remote or the code-hosting platform rather than the local checkout
	// (FR-12.6). The message names the step and keeps the forge's own text.
	ErrForge = "forge_error"
	// ErrClientClosed means the caller disconnected before the work finished.
	// It is never a server fault and must not be logged as one.
	ErrClientClosed = "client_closed"
)

// NewError builds a typed API error.
func NewError(code, msg string) *APIError { return &APIError{Code: code, Message: msg} }

// ErrNothingToCompact reports that the agent had no context worth compacting.
//
// It is not a failure. Pi answers the compact command with "Nothing to compact
// (session too small)" on a fresh session; forwarding that as a 500 told the
// user the server was broken when the honest answer is "there is nothing to
// compact yet". Runtimes return this sentinel and the handler turns it into a
// 200 no-op with a reason (FR-1.9).
var ErrNothingToCompact = errors.New("nothing to compact")

// SessionLogs is the tail of one session's agent stderr, kept so a crash can be
// explained without entering the host (PRD FR-5.10).
type SessionLogs struct {
	SessionID string    `json:"session_id"`
	Lines     []string  `json:"lines"`
	Truncated bool      `json:"truncated"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LogSource is implemented by runtimes that keep such a tail. It is a separate
// interface on purpose: a runtime that keeps no logs should say so, not pretend
// with an empty buffer.
type LogSource interface {
	Logs(ctx context.Context, sessionID string) (*SessionLogs, error)
}

// APIKey is a bearer token a user creates for scripts (PRD FR-13.9). The
// plaintext exists only in the creation response; everything here describes it.
type APIKey struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	// Prefix is the readable head of the token, shown in the list so a person can
	// tell two keys apart without ever seeing the secret again.
	Prefix     string    `json:"prefix"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
	Revoked    bool      `json:"revoked"`
	RevokedAt  time.Time `json:"revoked_at,omitzero"`
}

// SessionCloner copies one session's agent transcript to another. It is optional for
// the same reason LogSource is: a runtime that cannot do it should say so by not
// implementing the interface, and the caller keeps the display-only copy.
type SessionCloner interface {
	CloneSession(ctx context.Context, fromSession, toSession string) error
}
