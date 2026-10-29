package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/testrun"
)

// defaultTestRunTail is how much raw output an unrecognised run keeps. Enough
// to read the compiler error that made the output unreadable, small enough that
// a 100k-line log does not become part of every list response.
const defaultTestRunTail = 4000

// testRunOutputTail is a variable so tests can shrink it.
var testRunOutputTail = defaultTestRunTail

// startTestRun records the intent to run a recognised test command.
//
// It runs on the tool.started event, before the command has printed anything,
// which is what makes 运行中 visible while the tests are still going.
func (h *hub) startTestRun(ctx context.Context, userID string, ev contracts.Event, toolCallID, command string) {
	runner, ok := testrun.Detect(command)
	if !ok {
		return
	}
	if _, err := h.store.CreateTestRun(ctx, userID, store.NewTestRun{
		SessionID:  ev.SessionID,
		RunID:      ev.RunID,
		ToolCallID: toolCallID,
		Source:     "agent",
		Runner:     string(runner),
		Command:    command,
		StartedAt:  ev.TS,
	}); err != nil {
		h.log.Debug("test run not recorded", slog.String("error", err.Error()))
	}
}

// finishTestRun settles the run a tool call started, parsing the output the
// tool returned. A tool call that was not a recognised test command has no row
// and this is a no-op.
func (h *hub) finishTestRun(ctx context.Context, userID string, ev contracts.Event, toolCallID, text string, durationMS int64) {
	run, err := h.store.TestRunByToolCall(ctx, userID, ev.SessionID, toolCallID)
	if err != nil {
		return
	}
	h.settleTestRun(ctx, userID, run.SessionID, run.ID, testrun.Parse(run.Runner, text), text, durationMS)
}

// settleTestRun is the one place a parsed result becomes a stored row, shared
// by the tool-event path and the terminal tap so pass/fail cannot disagree.
func (h *hub) settleTestRun(ctx context.Context, userID, sessionID, runID string, res testrun.Result, raw string, fallbackMS int64) {
	// A parsed failure is a failed run even when the runner's final package line
	// never arrived (a truncated tool result, for instance). The counts and the
	// failure list are the evidence; FailedRun covers the "package failed but no
	// per-test marker" case (a panic).
	status := store.TestRunStatusFor(res.Recognized, res.FailedRun, res.Failed > 0)
	duration := res.DurationMS
	if duration == 0 {
		duration = fallbackMS
	}
	tail := ""
	if !res.Recognized {
		tail = testrun.Tail(raw, testRunOutputTail)
	}
	if err := h.store.FinishTestRun(ctx, userID, runID, store.TestRunFinish{
		Status:     status,
		Total:      res.Total,
		Passed:     res.Passed,
		Failed:     res.Failed,
		Skipped:    res.Skipped,
		DurationMS: duration,
		Failures:   cleanFailures(h.worktreeRoot(ctx, userID, sessionID), res.Failures),
		OutputTail: tail,
		EndedAt:    time.Now(),
	}); err != nil {
		h.log.Debug("test run not settled", slog.String("error", err.Error()))
	}
}

// worktreeRoot resolves the checkout a session's commands ran in, so a failure
// that names an absolute path can be turned into a path the file API accepts.
// An unresolvable root is not fatal: the failure simply keeps the path the
// runner printed.
func (h *hub) worktreeRoot(ctx context.Context, userID, sessionID string) string {
	sess, err := h.store.Session(ctx, userID, sessionID)
	if err != nil || sess.WorktreeID == "" {
		return ""
	}
	root, err := h.store.WorktreePathByID(ctx, sess.WorktreeID)
	if err != nil {
		return ""
	}
	return root
}

// cleanFailures converts parser failures into stored ones, normalising paths
// against the checkout root.
func cleanFailures(root string, in []testrun.Failure) []store.TestFailure {
	out := make([]store.TestFailure, 0, len(in))
	for _, f := range in {
		out = append(out, store.TestFailure{
			Name:    f.Name,
			File:    cleanFailureFile(root, f.File),
			Line:    f.Line,
			Message: f.Message,
		})
	}
	return out
}

// cleanFailureFile turns the path a runner printed into a workspace-relative
// path when it is inside the checkout. Anything else is left alone rather than
// guessed at: a wrong path would open the wrong file, which is worse than a
// click that reports "not found".
func cleanFailureFile(root, file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	if root != "" {
		rootFwd := strings.ReplaceAll(root, "\\", "/")
		fileFwd := strings.ReplaceAll(file, "\\", "/")
		if strings.HasPrefix(fileFwd, rootFwd+"/") {
			file = fileFwd[len(rootFwd)+1:]
		}
	}
	file = strings.TrimPrefix(file, "./")
	file = strings.ReplaceAll(file, "\\", "/")
	return file
}
