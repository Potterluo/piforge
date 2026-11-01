package gitx

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Two properties the 变更 panel — and the e2e spec that drives it
// (48-change-list.spec.ts) — stand on. Neither was tested, and both have been
// broken by a plausible-looking change:
//
//  1. a read must not be able to make a write fail. `git status` refreshes the
//     index and writes it back under `.git/index.lock` unless
//     GIT_OPTIONAL_LOCKS=0 (hardened.EnvWith); without that variable a status
//     read that overlaps `git add` makes the add die with
//     "fatal: Unable to create '…/index.lock': File exists". Measured on one
//     Windows box: 18 of 200 adds failed while a status reader ran, 0 of 200
//     with the variable set. The panel reads status on mount, on every
//     workspace event and on window focus, so this is a user's stage button
//     failing because the panel looked — and the e2e spec polls status every
//     100 ms while it waits for the stage to commit.
//
//  2. a stage is committed when the call returns. The spec polls
//     `GET /git/status` and then asserts on what the panel draws, so "the
//     response means the index holds it" is the whole contract; a stage that
//     answered before `git add` had written the index would make that poll
//     resolve on a state that was still changing.

// TestAStatusReadCannotMakeAStageFail runs eight status readers while eighty
// stages commit. Every stage must succeed. (Eight and eighty because that is
// what turned this from "usually red" into "red ten times out of ten" when the
// GIT_OPTIONAL_LOCKS=0 line was removed: fewer readers made it a coin flip.)
func TestAStatusReadCannotMakeAStageFail(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	var stop atomic.Bool
	var reads, statusErrs atomic.Int64
	var firstStatusErr atomic.Value
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := cl.Status(ctx, dir); err != nil {
					statusErrs.Add(1)
					firstStatusErr.CompareAndSwap(nil, err.Error())
					return
				}
				reads.Add(1)
			}
		}()
	}

	const stages = 80
	var stageErrs []string
	for i := 0; i < stages; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		write(t, dir, name, "x\n")
		if err := cl.Stage(ctx, dir, []string{name}); err != nil {
			stageErrs = append(stageErrs, err.Error())
		}
	}

	stop.Store(true)
	wg.Wait()

	if len(stageErrs) > 0 {
		t.Fatalf("%d of %d stages failed while a status read was running; first: %s",
			len(stageErrs), stages, stageErrs[0])
	}
	if n := statusErrs.Load(); n > 0 {
		t.Fatalf("a status read failed while staging (%d); first: %v", n, firstStatusErr.Load())
	}
	// The readers have to have actually overlapped the writes, or this test
	// proves nothing.
	if n := reads.Load(); n < stages {
		t.Fatalf("only %d status reads ran for %d stages: the race was not exercised", n, stages)
	}
}

// TestAStageIsCommittedBeforeItReturns pins the ordering: whatever the reads
// around it saw, a status taken after Stage returns reports the file in the
// index, never "?".
func TestAStageIsCommittedBeforeItReturns(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("n%d.txt", i)
		write(t, dir, name, "x\n")

		// A read that was asked *before* the stage may legitimately answer with
		// the pre-stage index; it must not be able to change what Stage does.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cl.Status(ctx, dir)
		}()

		if err := cl.Stage(ctx, dir, []string{name}); err != nil {
			t.Fatalf("stage %d (%s): %v", i, name, err)
		}
		wg.Wait()

		st, err := cl.Status(ctx, dir)
		if err != nil {
			t.Fatalf("status after stage %d: %v", i, err)
		}
		change, ok := changeOf(st, name)
		if !ok {
			t.Fatalf("stage returned but %s is not in the change list: %+v", name, st.Changes)
		}
		if change.Index == "?" {
			t.Fatalf("stage returned but %s is still untracked (index %q, worktree %q)",
				name, change.Index, change.Worktree)
		}
	}
}

func changeOf(st *contracts.GitStatus, path string) (contracts.GitChange, bool) {
	for _, c := range st.Changes {
		if c.Path == path {
			return c, true
		}
	}
	return contracts.GitChange{}, false
}
