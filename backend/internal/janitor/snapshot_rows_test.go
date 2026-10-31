package janitor

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/store"
)

// fakeSnapshotRows is the snapshots table as the janitor sees it: a list of rows
// and a delete that records what it was asked to remove.
type fakeSnapshotRows struct {
	rows    []store.SnapshotRef
	deleted []string
	err     error
}

func (f *fakeSnapshotRows) SnapshotIndex(context.Context) ([]store.SnapshotRef, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func (f *fakeSnapshotRows) DeleteSnapshots(_ context.Context, ids []string) (int, error) {
	f.deleted = append(f.deleted, ids...)
	return len(ids), nil
}

func (f *fakeSnapshotRows) remaining() map[string]bool {
	out := make(map[string]bool, len(f.rows))
	gone := make(map[string]bool, len(f.deleted))
	for _, id := range f.deleted {
		gone[id] = true
	}
	for _, row := range f.rows {
		if !gone[row.ID] {
			out[row.ID] = true
		}
	}
	return out
}

// manifestRow builds the row the reaper writes: <sandbox>/<stamp>/manifest.json
// relative to the snapshot root.
func manifestRow(id, sandbox, stamp string) store.SnapshotRef {
	return store.SnapshotRef{
		ID:        id,
		SandboxID: sandbox,
		BlobKey:   filepath.ToSlash(filepath.Join(sandbox, stamp, "manifest.json")),
	}
}

func snapshotRowJanitor(t *testing.T, root string, live []string, rows *fakeSnapshotRows) *Janitor {
	t.Helper()
	return New(Config{
		SnapshotsRoot: root,
		Sandboxes:     fakeSandboxes(live),
		SnapshotRows:  rows,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// The rows are the half of the snapshot lifecycle the directories used to have on
// their own: a row whose manifest directory is gone, or whose sandbox row is gone,
// is garbage. A row whose directory exists *and* whose sandbox is alive must never
// be touched, whatever else the pass is doing.
func TestSweepRemovesSnapshotRowsWithoutADirectoryOrASandbox(t *testing.T) {
	root := t.TempDir()
	// The live sandbox: directory and row are both there, so the row survives.
	mkdir(t, root, "sbx_01MLIVE00000000000000000A/20260101T000000.000000000Z")
	// The dead sandbox: its directory exists, but the sandbox row does not. The
	// directory sweep removes the directory in this pass and the row sweep must
	// remove the row, not leave it pointing at nothing.
	mkdir(t, root, "sbx_01MGONE000000000000000000/20260101T000000.000000000Z")

	rows := &fakeSnapshotRows{rows: []store.SnapshotRef{
		manifestRow("snp_keep_live", "sbx_01MLIVE00000000000000000A", "20260101T000000.000000000Z"),
		// The sandbox is alive but its manifest directory was deleted by hand (or
		// by a failed earlier sweep): the row no longer points at anything.
		manifestRow("snp_live_without_dir", "sbx_01MLIVE00000000000000000A", "20260102T000000.000000000Z"),
		// The sandbox is gone: the row has no owner left.
		manifestRow("snp_dead_sandbox", "sbx_01MGONE000000000000000000", "20260101T000000.000000000Z"),
	}}

	j := snapshotRowJanitor(t, root, []string{"sbx_01MLIVE00000000000000000A"}, rows)
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshotRows != 2 {
		t.Fatalf("removed rows = %d, want 2: %+v", res.RemovedSnapshotRows, res)
	}
	left := rows.remaining()
	if !left["snp_keep_live"] {
		t.Fatalf("a row whose directory and sandbox both exist was deleted: %+v", rows.deleted)
	}
	if left["snp_live_without_dir"] || left["snp_dead_sandbox"] {
		t.Fatalf("an orphaned row survived: deleted=%v remaining=%v", rows.deleted, left)
	}
}

// A row with no blob key cannot prove its directory is gone. The sweep keeps it
// unless its sandbox is gone: "unknown" must never become "delete".
func TestSweepKeepsARowItCannotProveIsOrphaned(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "sbx_01MLIVE00000000000000000B/20260101T000000.000000000Z")
	rows := &fakeSnapshotRows{rows: []store.SnapshotRef{
		{ID: "snp_no_blob_key", SandboxID: "sbx_01MLIVE00000000000000000B"},
	}}
	j := snapshotRowJanitor(t, root, []string{"sbx_01MLIVE00000000000000000B"}, rows)
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshotRows != 0 || len(rows.deleted) != 0 {
		t.Fatalf("a row with an unknown directory was deleted: %+v", rows.deleted)
	}
}

// A store that cannot answer must not become "every row is garbage": the sweep
// aborts and deletes nothing.
func TestSweepRefusesToGuessWhenTheSnapshotRowIndexFails(t *testing.T) {
	root := t.TempDir()
	rows := &fakeSnapshotRows{err: errors.New("database is away")}
	j := snapshotRowJanitor(t, root, nil, rows)
	if _, err := j.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce reported success without a snapshot-row index")
	}
	if len(rows.deleted) != 0 {
		t.Fatalf("rows were deleted against an unreadable index: %v", rows.deleted)
	}
}

// Dry run reports the number it would remove without deleting anything.
func TestSweepDryRunReportsSnapshotRowsWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	rows := &fakeSnapshotRows{rows: []store.SnapshotRef{
		manifestRow("snp_dead", "sbx_01MGONE000000000000000000", "20260101T000000.000000000Z"),
	}}
	j := snapshotRowJanitor(t, root, nil, rows)
	j.cfg.DryRun = true
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshotRows != 1 {
		t.Fatalf("dry run reported %d, want 1", res.RemovedSnapshotRows)
	}
	if len(rows.deleted) != 0 {
		t.Fatalf("dry run deleted rows: %v", rows.deleted)
	}
}

// The row sweep must not run without a live-sandbox index: that is the one
// reading that decides whether a row still has an owner.
func TestSnapshotRowSweepNeedsTheSandboxIndex(t *testing.T) {
	rows := &fakeSnapshotRows{rows: []store.SnapshotRef{
		manifestRow("snp_x", "sbx_01MGONE000000000000000000", "20260101T000000.000000000Z"),
	}}
	j := New(Config{
		SnapshotsRoot: t.TempDir(),
		SnapshotRows:  rows,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(rows.deleted) != 0 {
		t.Fatalf("rows were swept without a live-sandbox index: %v", rows.deleted)
	}
}
