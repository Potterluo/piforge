package pi

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pitest"
)

// newFakeRuntime returns a Runtime whose sessions are served by pitest peers
// running in-process, so the runtime's own logic (process map, prompting,
// interrupts, forks) is exercised without building a binary.
func newFakeRuntime(t *testing.T, script string, delay time.Duration) *Runtime {
	t.Helper()
	return newFakeRuntimeWith(t, script, delay, nil)
}

// newFakeRuntimeWith is newFakeRuntime with a hook over the bridge's end of the
// peer pipe, so a test can model a peer (or a transport) that answers in a
// particular order.
func newFakeRuntimeWith(t *testing.T, script string, delay time.Duration, wrap func(io.ReadWriteCloser) io.ReadWriteCloser) *Runtime {
	t.Helper()
	cfg := RuntimeConfig{
		Config: Config{
			Dir:              t.TempDir(),
			CloseTimeout:     time.Second,
			SubscriberBuffer: 256,
			MaxBacklog:       4096,
		},
		Dial: func(spec contracts.SessionSpec, sessionFile string) (io.ReadWriteCloser, error) {
			ours, theirs := pitest.NewPipe()
			go func() {
				defer theirs.Close()
				_ = pitest.Serve(script, delay, sessionFile, theirs, theirs)
			}()
			if wrap != nil {
				return wrap(ours), nil
			}
			return ours, nil
		},
	}
	rt := NewRuntime(cfg)
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// sessionSpec builds a spec whose worktree is a fresh temp dir.
func sessionSpec(t *testing.T, id string, kind contracts.SessionKind) contracts.SessionSpec {
	t.Helper()
	return contracts.SessionSpec{
		SessionID: id,
		Kind:      kind,
		Worktree:  t.TempDir(),
	}
}

// ctxFor returns a context bounded by the test timeout.
func ctxFor(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), testTimeout)
}

// settledMatcher stops at the run terminal.
func settledMatcher(ev contracts.Event) bool { return ev.Type == contracts.EvRunSettled }

// TestRuntimeRunEventOrder is the contract the frontend depends on: a run
// starts, streams, runs a tool, finalises, and settles — in that order, with no
// gaps in seq.
func TestRuntimeRunEventOrder(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_main", contracts.KindMain)
	ch, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := rt.Prompt(ctx, contracts.PromptRequest{
		SessionID: "ses_main",
		Text:      "why does the cache evict early?",
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !res.Accepted || res.Disposition != "started" {
		t.Fatalf("PromptResult = %+v", res)
	}
	if res.RunID == "" {
		t.Fatal("Prompt returned no run id, so the client cannot correlate events")
	}

	events := collectUntil(t, ch, settledMatcher)
	if events[0].Type != contracts.EvRunStarted {
		t.Fatalf("first event = %s, want run.started", events[0].Type)
	}
	assertSubsequence(t, events,
		contracts.EvRunStarted,
		contracts.EvMsgDelta,
		contracts.EvToolStarted,
		contracts.EvToolProgress,
		contracts.EvToolEnded,
		contracts.EvMsgFinal,
		contracts.EvRunSettled,
	)
	assertSeqContiguous(t, events)

	for _, ev := range events {
		if ev.SessionID != "ses_main" {
			t.Fatalf("%s session_id = %q", ev.Type, ev.SessionID)
		}
		if ev.BranchID != "ses_main" {
			t.Fatalf("%s branch_id = %q", ev.Type, ev.BranchID)
		}
		if ev.RunID != res.RunID {
			t.Fatalf("%s run_id = %q, want the id Prompt returned (%q)", ev.Type, ev.RunID, res.RunID)
		}
	}

	final, ok := lastFinalOfRole(t, events, "assistant")
	if !ok || final.Text == "" {
		t.Fatalf("no assistant message.final carried text: %+v", final)
	}
	if len(final.ToolResults) != 1 {
		t.Fatalf("tool_results = %+v, want the bash result", final.ToolResults)
	}

	status, err := rt.Status(ctx, "ses_main")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running || status.Streaming {
		t.Errorf("status after settle = %+v", status)
	}
	if status.LastSeq != events[len(events)-1].Seq {
		t.Errorf("LastSeq = %d, want %d", status.LastSeq, events[len(events)-1].Seq)
	}
}

// TestRuntimeInterruptThenPromptAgain covers the Esc flow (PRD §5.1.1 FR-1.4):
// clear_queue returns the texts to restore, abort ends the run, and the session
// is immediately usable again.
func TestRuntimeInterruptThenPromptAgain(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptSlow, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_int", contracts.KindMain)
	ch, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_int", Text: "long job"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// Wait until the run is visibly streaming.
	head := collectUntil(t, ch, func(ev contracts.Event) bool { return ev.Type == contracts.EvMsgDelta })

	// A second prompt without a behaviour must be refused, not interleaved.
	_, err = rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_int", Text: "and also"})
	var apiErr *contracts.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != contracts.ErrSessionBusy {
		t.Fatalf("prompt while streaming error = %v, want session_busy", err)
	}
	queued, err := rt.Prompt(ctx, contracts.PromptRequest{
		SessionID: "ses_int", Text: "and also", StreamingBehavior: "follow_up",
	})
	if err != nil {
		t.Fatalf("queued prompt: %v", err)
	}
	if queued.Disposition != "queued" {
		t.Fatalf("queued disposition = %q", queued.Disposition)
	}

	interrupted, err := rt.Interrupt(ctx, "ses_int")
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if len(interrupted.ClearedFollowUp) != 1 || interrupted.ClearedFollowUp[0] != "and also" {
		t.Fatalf("cleared follow-up = %v, want the queued text back for the composer",
			interrupted.ClearedFollowUp)
	}

	events := collectUntil(t, ch, settledMatcher)
	firstRun := append(append([]contracts.Event{}, head...), events...)
	assertSeqContiguous(t, firstRun)

	abortIdx := indexOfType(events, contracts.EvRunAborted)
	settleIdx := indexOfType(events, contracts.EvRunSettled)
	if abortIdx < 0 || settleIdx < 0 {
		t.Fatalf("interrupt produced %v, want run.aborted then run.settled", typesOf(events))
	}
	if abortIdx > settleIdx {
		t.Fatalf("run.aborted came after run.settled")
	}
	var settled SettledPayload
	decodePayload(t, events[settleIdx], &settled)
	if !settled.Aborted {
		t.Fatalf("run.settled aborted = false after Interrupt")
	}

	// The session must still work: a later prompt starts a fresh run.
	second, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_int", Text: "try again"})
	if err != nil {
		t.Fatalf("prompt after interrupt: %v", err)
	}
	if second.RunID == "" || second.RunID == settledRunID(events) {
		t.Fatalf("prompt after interrupt reused run id %q", second.RunID)
	}
	again := collectUntil(t, ch, settledMatcher)
	assertSubsequence(t, again, contracts.EvRunStarted, contracts.EvMsgFinal, contracts.EvRunSettled)
	assertSeqContiguous(t, append(firstRun, again...))
}

// settledRunID returns the run id the settled event closed.
func settledRunID(events []contracts.Event) string {
	for _, ev := range events {
		if ev.Type == contracts.EvRunSettled {
			return ev.RunID
		}
	}
	return ""
}

// TestRuntimeForkProducesTrimmedTree is the fork contract from PRD §5.1.2: the
// branch gets its own process whose tree stops at the fork point, while the
// source session keeps its full history.
func TestRuntimeForkProducesTrimmedTree(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_root", contracts.KindMain)
	ch, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_root", Text: "first task"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	collectUntil(t, ch, settledMatcher)

	points, err := rt.ForkPoints(ctx, "ses_root")
	if err != nil {
		t.Fatalf("ForkPoints: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("fork points = %+v, want the single user message", points)
	}
	if points[0].Preview != "first task" {
		t.Errorf("fork point preview = %q", points[0].Preview)
	}

	rootTree, err := rt.Tree(ctx, "ses_root")
	if err != nil {
		t.Fatalf("Tree(root): %v", err)
	}
	rootCount := countEntries(rootTree)

	if err := rt.Fork(ctx, contracts.ForkRequest{
		SessionID:  "ses_root",
		EntryID:    points[0].EntryID,
		NewSession: "ses_branch",
		Label:      "alt-approach",
	}); err != nil {
		t.Fatalf("Fork: %v", err)
	}

	// Forking does not start the branch: the caller does, from the session row, so
	// the branch gets its own model configuration instead of the parent's.
	branchSpec := sessionSpec(t, "ses_branch", contracts.KindBranch)
	if _, err := rt.Start(ctx, branchSpec); err != nil {
		t.Fatalf("start branch: %v", err)
	}
	branchTree, err := rt.Tree(ctx, "ses_branch")
	if err != nil {
		t.Fatalf("Tree(branch): %v", err)
	}
	// Forking at the *first* user message has nothing behind it: Pi truncates at the
	// parent of the chosen message, and the first message has no parent. The branch
	// is therefore empty, and pretending otherwise made the UI show a conversation
	// the model never had.
	branchCount := countEntries(branchTree)
	if branchCount != 0 {
		t.Fatalf("branch tree has %d entries, want an empty branch for a first-message fork: %+v",
			branchCount, branchTree)
	}

	// The source session's *history* must be untouched. Its process is not: Pi
	// forks inside the running process and rebinds it to the branch, so the parent
	// is stopped and restarted on demand — that is the real contract, and asserting
	// otherwise tested a fiction.
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("restart root: %v", err)
	}
	after, err := rt.Tree(ctx, "ses_root")
	if err != nil {
		t.Fatalf("Tree(root) after fork: %v", err)
	}
	if got := countEntries(after); got != rootCount {
		t.Fatalf("root tree shrank from %d to %d entries during a fork", rootCount, got)
	}

	// The branch is a live process of its own.
	status, err := rt.Status(ctx, "ses_branch")
	if err != nil {
		t.Fatalf("Status(branch): %v", err)
	}
	if status.SessionFile == "" {
		t.Fatalf("branch status has no session file: %+v", status)
	}
	if status.SessionFile == mustStatus(t, rt, "ses_root").SessionFile {
		t.Fatalf("branch and root share a session file: %s", status.SessionFile)
	}
}

func mustStatus(t *testing.T, rt *Runtime, id string) contracts.RuntimeStatus {
	t.Helper()
	st, err := rt.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status(%s): %v", id, err)
	}
	return st
}

// countEntries flattens the tree.
func countEntries(nodes []*contracts.TreeEntry) int {
	n := 0
	for _, node := range nodes {
		n++
		n += countEntries(node.Children)
	}
	return n
}

// TestRuntimeStartRequiresSessionID keeps an empty id from creating an
// unusable session.
func TestRuntimeStartRequiresSessionID(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()
	if _, err := rt.Start(ctx, contracts.SessionSpec{}); err == nil {
		t.Fatal("Start with no session id must fail")
	}
}

// TestRuntimeStartIsIdempotent checks a second Start reuses the process instead
// of spawning a second agent for one session.
func TestRuntimeStartIsIdempotent(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()
	spec := sessionSpec(t, "ses_once", contracts.KindMain)
	ch1, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ch2, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start again: %v", err)
	}
	if ch1 != ch2 {
		t.Fatal("Start spawned a second subscription; the session must keep one process")
	}
}

// TestRuntimeStopIsIdempotent keeps archive/delete paths safe to retry.
func TestRuntimeStopIsIdempotent(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()
	spec := sessionSpec(t, "ses_stop", contracts.KindMain)
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(ctx, "ses_stop"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := rt.Stop(ctx, "ses_stop"); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if _, err := rt.Status(ctx, "ses_stop"); err == nil {
		t.Fatal("Status on a stopped session should report not found")
	}
}

// TestRuntimeUnknownSession checks every method reports not_found instead of
// nil-pointer panicking.
func TestRuntimeUnknownSession(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()
	const id = "ses_missing"

	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: id, Text: "x"}); err == nil {
		t.Error("Prompt: want not_found")
	}
	if _, err := rt.Interrupt(ctx, id); err == nil {
		t.Error("Interrupt: want not_found")
	}
	if _, err := rt.Status(ctx, id); err == nil {
		t.Error("Status: want not_found")
	}
	if _, err := rt.Tree(ctx, id); err == nil {
		t.Error("Tree: want not_found")
	}
	if _, _, err := rt.Entries(ctx, id, ""); err == nil {
		t.Error("Entries: want not_found")
	}
	if _, err := rt.ForkPoints(ctx, id); err == nil {
		t.Error("ForkPoints: want not_found")
	}
	if err := rt.Compact(ctx, id, ""); err == nil {
		t.Error("Compact: want not_found")
	}
	var apiErr *contracts.APIError
	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: id, Text: "x"}); !errors.As(err, &apiErr) ||
		apiErr.Code != contracts.ErrNotFound {
		t.Errorf("Prompt error = %v, want not_found", err)
	}
}

// TestRuntimeCompactAndEntries exercises the remaining RPC wrappers.
func TestRuntimeCompactAndEntries(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_cmp", contracts.KindMain)
	ch, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_cmp", Text: "work " + pitest.BigContextMarker}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	collectUntil(t, ch, settledMatcher)

	entries, leaf, err := rt.Entries(ctx, "ses_cmp", "")
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) == 0 || leaf == "" {
		t.Fatalf("entries = %d leaf = %q", len(entries), leaf)
	}
	more, _, err := rt.Entries(ctx, "ses_cmp", entries[0].EntryID)
	if err != nil {
		t.Fatalf("Entries(since): %v", err)
	}
	if len(more) >= len(entries) {
		t.Fatalf("since cursor returned %d entries out of %d; it must be incremental", len(more), len(entries))
	}

	if err := rt.Compact(ctx, "ses_cmp", "keep the test results"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	evs := collectUntil(t, ch, func(ev contracts.Event) bool { return ev.Type == contracts.EvCompaction })
	var payload CompactionPayload
	decodePayload(t, evs[len(evs)-1], &payload)
	if payload.Phase != "start" && payload.Phase != "end" {
		t.Fatalf("compaction payload = %+v", payload)
	}
}

// FR-4.2: a sidecar shares its parent's checkout, so its agent must not be able to
// change it. The restriction has to reach Pi's own arguments — a boolean nobody reads
// is not an enforcement.
func TestSidecarSpawnDropsWriteTools(t *testing.T) {
	base := []string{"--mode", "rpc", "--model", "m"}
	main := ArgsForKind(base, contracts.KindMain)
	if len(main) != len(base) {
		t.Fatalf("a main session got extra args: %v", main)
	}

	sidecar := ArgsForKind(base, contracts.KindSidecar)
	joined := strings.Join(sidecar, " ")
	if !strings.Contains(joined, "--exclude-tools") {
		t.Fatalf("the sidecar args do not exclude anything: %v", sidecar)
	}
	for _, tool := range []string{"bash", "edit", "write"} {
		if !strings.Contains(joined, tool) {
			t.Fatalf("the sidecar can still use %q: %v", tool, sidecar)
		}
	}
	// The base arguments survive: this appends, it does not replace.
	for _, want := range base {
		if !strings.Contains(joined, want) {
			t.Fatalf("base arg %q was dropped: %v", want, sidecar)
		}
	}
}

// FR-5.x: an agent nobody has touched is stopped, and the session is not: the next
// prompt starts a fresh process on the same transcript. That is the difference between
// reaping and losing work.
func TestReapIdleStopsUnusedAgentsOnly(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_reap", contracts.KindMain)
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := rt.ReapIdle(ctx, time.Hour); got != 0 {
		t.Fatalf("a fresh session was reaped (%d)", got)
	}

	// Nothing has happened for an hour, as far as the reaper is concerned.
	if got := rt.ReapIdle(ctx, -time.Second); got != 0 {
		t.Fatalf("a negative ttl reaped %d sessions", got)
	}
	// The agent is "running" for a moment while its process starts, and a reaper that
	// respected that is correct. Poll rather than assume the machine is instant.
	reaped := 0
	for i := 0; i < 200; i++ {
		if reaped = rt.ReapIdle(ctx, time.Nanosecond); reaped > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reaped == 0 {
		t.Fatal("idle session was never reaped")
	}

	// The session still exists as a row of history: it restarts on demand.
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("restart after reaping: %v", err)
	}
	if _, err := rt.Tree(ctx, "ses_reap"); err != nil {
		t.Fatalf("Tree after restart: %v", err)
	}
}

// A crashed agent used to brick its session: the process was gone, the runtime still
// held the entry, and every prompt answered "bridge closed" until the daemon restarted.
// The next prompt is where the crash is noticed, so that is where it has to heal.
func TestPromptRespawnsAfterTheAgentDied(t *testing.T) {
	rt := newFakeRuntime(t, pitest.ScriptOK, 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	if _, err := rt.Start(ctx, sessionSpec(t, "ses_crash", contracts.KindMain)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Simulate the kernel taking the process away.
	s, err := rt.get("ses_crash")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = s.bridge.Close()

	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_crash", Text: "after the crash"}); err != nil {
		t.Fatalf("prompt after a dead agent: %v", err)
	}
	again, err := rt.get("ses_crash")
	if err != nil {
		t.Fatalf("get after respawn: %v", err)
	}
	if !again.bridge.Alive() {
		t.Fatal("the agent was not respawned")
	}
}

// FR-1.9: "the session is too small" is Pi's answer to compacting a fresh
// session, not a crash. Recognizing it is what keeps the API from reporting a
// 500 for a no-op. The match is deliberately loose about wording and case —
// Pi's message is prose, and a 500 is what a stricter match would regress to.
func TestNothingToCompactIsRecognized(t *testing.T) {
	cases := map[string]bool{
		"Nothing to compact (session too small)": true,
		"nothing to compact":                     true,
		"NOTHING TO COMPACT (empty)":             true,
		"compact failed":                         false,
		"":                                       false,
	}
	for message, want := range cases {
		if got := isNothingToCompact(message); got != want {
			t.Errorf("isNothingToCompact(%q) = %v, want %v", message, got, want)
		}
	}
}

// TestModelRefOfNeverReportsUnknown: Pi answers get_state with provider/model
// "unknown" for a process it could not resolve a model for. That is an internal
// placeholder, not a model name — propagated, it produced the header string
// `跟随默认（unknown）` in the acceptance pass. An unresolved model must read as
// empty so the UI can say 未配置.
func TestModelRefOfNeverReportsUnknown(t *testing.T) {
	cases := []struct {
		provider, model, want string
	}{
		{"unknown", "unknown", ""},
		{"unknown", "", ""},
		{"", "unknown", ""},
		{"piforge", "deepseek-v4-flash", "piforge/deepseek-v4-flash"},
		{"", "deepseek-v4-flash", "deepseek-v4-flash"},
		{"piforge", "", "piforge"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := modelRefOf(tc.provider, tc.model); got != tc.want {
			t.Errorf("modelRefOf(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// TestStartedModelConfigTracksTheSpawnEnvironment: the control plane needs to
// tell a working agent from one spawned while no provider existed. The platform
// pointer (PI_CODING_AGENT_DIR) is exactly the difference: without it Pi uses
// its own configuration and has no gateway token, and the only cure is a new
// process.
func TestStartedModelConfigTracksTheSpawnEnvironment(t *testing.T) {
	spawn := func(t *testing.T, env []string) *Runtime {
		t.Helper()
		rt := NewRuntime(RuntimeConfig{
			Config: Config{
				Dir:          t.TempDir(),
				CloseTimeout: time.Second,
				Env:          env,
			},
			Dial: func(spec contracts.SessionSpec, sessionFile string) (io.ReadWriteCloser, error) {
				ours, theirs := pitest.NewPipe()
				go func() {
					defer theirs.Close()
					_ = pitest.Serve(pitest.ScriptOK, 0, sessionFile, theirs, theirs)
				}()
				return ours, nil
			},
		})
		t.Cleanup(func() { _ = rt.Close() })
		return rt
	}

	ctx, cancel := ctxFor(t)
	defer cancel()

	configured := spawn(t, []string{"PI_CODING_AGENT_DIR=/tmp/agent"})
	spec := sessionSpec(t, "ses_configured", contracts.KindMain)
	spec.ModelRef = "volc/deepseek-v4-flash"
	if _, err := configured.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ok, live := configured.StartedModelConfig("ses_configured"); !live || !ok {
		t.Fatalf("configured process = (%v, %v), want (true, true)", ok, live)
	}

	bare := spawn(t, nil)
	bareSpec := sessionSpec(t, "ses_bare", contracts.KindMain)
	if _, err := bare.Start(ctx, bareSpec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ok, live := bare.StartedModelConfig("ses_bare"); !live || ok {
		t.Fatalf("bare process = (%v, %v), want (false, true)", ok, live)
	}
	if _, live := bare.StartedModelConfig("ses_absent"); live {
		t.Fatal("StartedModelConfig reported a live process for an unknown session")
	}
}
