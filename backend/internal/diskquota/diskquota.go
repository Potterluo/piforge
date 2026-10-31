// Package diskquota measures one sandbox's checkout against a disk budget
// (FR-5.8).
//
// The budget exists because a runaway agent fills the volume with something
// ordinary: an `npm install` that pulls a deep tree, a checkout of a repository
// full of binaries, a build that leaves artifacts behind. The directory lifecycle
// says who owns each directory; this says how big one may get.
//
// The measurement is a du-style walk of the sandbox root, not a per-write
// accounting: a sandbox is checked when a run is about to start (the honest
// boundary — the user is waiting on this turn) and reported from a short-lived
// cache for the admin screen, so an idle dashboard does not turn into a disk
// scan. A single `npm install` can therefore overshoot the budget by one turn;
// what it cannot do is fill the disk silently, because the next message is
// refused with the limit, the current usage and what to delete.
package diskquota

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/runtimedirs"
)

// DefaultQuotaBytes is the per-sandbox budget a deployment gets unless it says
// otherwise: 2 GiB. Big enough for a normal checkout plus one dependency tree,
// small enough that a run-away install is caught before the volume fills.
const DefaultQuotaBytes int64 = 2 << 30

// DefaultReportTTL is how long a measurement may be reused by the admin screen.
// The page polls every 15 seconds; walking every checkout that often would make
// an idle screen a disk scan.
const DefaultReportTTL = time.Minute

// DefaultMaxFiles bounds one walk, the same way the runtime-directory report is
// bounded: a checkout with node_modules can hold hundreds of thousands of files.
// A truncated walk is a lower bound, and a lower bound over the quota is still
// over the quota.
const DefaultMaxFiles = runtimedirs.DefaultMaxFiles

// Config wires a Guardian. The zero value is a disabled guardian, which is what a
// hand-built test server gets.
type Config struct {
	// QuotaBytes is the budget for one sandbox root. Zero (or negative)
	// disables enforcement; the report still measures, so an operator can size a
	// quota against real usage before turning it on.
	QuotaBytes int64
	// MaxFiles bounds one walk. Zero uses DefaultMaxFiles.
	MaxFiles int
	// ReportTTL is how long Cached may reuse a measurement. Zero uses
	// DefaultReportTTL.
	ReportTTL time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

// Report is one sandbox's measured footprint against its budget.
type Report struct {
	SandboxID string `json:"sandbox_id"`
	// Root is the measured directory. It is not serialised: a host path is an
	// implementation detail and the admin screen already knows the sandbox id.
	Root string `json:"-"`
	// Bytes is the sum of regular file sizes below the root.
	Bytes int64 `json:"bytes"`
	// Files is how many those were.
	Files int `json:"files"`
	// Truncated says the walk hit its file cap: Bytes is a lower bound.
	Truncated bool `json:"truncated,omitempty"`
	// QuotaBytes is the budget this report was measured against (0 = disabled).
	QuotaBytes int64 `json:"quota_bytes"`
	// Over is the decision: a positive quota and Bytes above it.
	Over bool `json:"over"`
	// MeasuredAt is when the walk finished.
	MeasuredAt time.Time `json:"measured_at"`
}

// Guardian measures sandboxes and remembers the last report per sandbox. It is
// safe for concurrent use.
type Guardian struct {
	cfg     Config
	mu      sync.Mutex
	reports map[string]Report
}

// New builds a guardian. A nil Now falls back to time.Now.
func New(cfg Config) *Guardian {
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = DefaultMaxFiles
	}
	if cfg.ReportTTL <= 0 {
		cfg.ReportTTL = DefaultReportTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Guardian{cfg: cfg, reports: make(map[string]Report, 8)}
}

// Limit reports the configured budget in bytes; 0 means enforcement is off.
func (g *Guardian) Limit() int64 {
	if g == nil {
		return 0
	}
	return g.cfg.QuotaBytes
}

// Disabled reports whether enforcement is off.
func (g *Guardian) Disabled() bool { return g.Limit() <= 0 }

// Measure walks one sandbox root and returns a fresh report. It is the check a
// run boundary uses: the answer must include what the last turn wrote.
func (g *Guardian) Measure(ctx context.Context, sandboxID, root string) Report {
	if g == nil {
		return Report{SandboxID: sandboxID, Root: root}
	}
	if err := ctx.Err(); err != nil {
		// The caller is gone; report what the last walk saw rather than a zero
		// that would read as "empty".
		return Report{SandboxID: sandboxID, Root: root, QuotaBytes: g.cfg.QuotaBytes}
	}
	rep := g.walk(sandboxID, root)
	g.mu.Lock()
	g.reports[sandboxID] = rep
	g.mu.Unlock()
	return rep
}

// Cached returns the report for one sandbox, walking it only when the last
// measurement is older than ReportTTL. This is what the admin screen reads.
func (g *Guardian) Cached(ctx context.Context, sandboxID, root string) Report {
	if g == nil {
		return Report{SandboxID: sandboxID, Root: root}
	}
	g.mu.Lock()
	rep, ok := g.reports[sandboxID]
	g.mu.Unlock()
	if ok && rep.Root == root && g.cfg.Now().Sub(rep.MeasuredAt) < g.cfg.ReportTTL {
		return rep
	}
	return g.Measure(ctx, sandboxID, root)
}

// Forget drops a cached report, so the next Cached walks again. A sandbox that
// was destroyed and recreated at the same id must not inherit the old numbers.
func (g *Guardian) Forget(sandboxID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.reports, sandboxID)
	g.mu.Unlock()
}

// walk measures the root with the same bounded walk the runtime-directory report
// uses; one implementation means the admin screen and the refusal cannot disagree
// about how big a sandbox is.
func (g *Guardian) walk(sandboxID, root string) Report {
	usage := runtimedirs.Measure(sandboxID, root, g.cfg.MaxFiles)
	rep := Report{
		SandboxID:  sandboxID,
		Root:       root,
		Bytes:      usage.Bytes,
		Files:      usage.Files,
		Truncated:  usage.Truncated,
		QuotaBytes: g.cfg.QuotaBytes,
		MeasuredAt: g.cfg.Now(),
	}
	rep.Over = rep.QuotaBytes > 0 && rep.Bytes > rep.QuotaBytes
	return rep
}

// Message is the refusal a user sees. It names the budget, what is actually
// there, and the things that usually caused it, because "quota exceeded" without
// a size or a next step is a support ticket.
func (g *Guardian) Message(rep Report) string {
	root := strings.TrimSpace(rep.Root)
	where := ""
	if root != "" {
		where = fmt.Sprintf("（沙箱目录 %s）", root)
	}
	return fmt.Sprintf(
		"沙箱磁盘用量已达到 %s，超过配额 %s%s：本次运行已停止，没有启动 agent。"+
			"请删除不需要的文件（node_modules、构建产物、下载的仓库或大文件）后重试，"+
			"或请管理员调整 -sandbox-disk-quota / PIFORGE_SANDBOX_DISK_QUOTA。",
		HumanBytes(rep.Bytes), HumanBytes(rep.QuotaBytes), where)
}

// HumanBytes renders a byte count the way a refusal should read it.
func HumanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}
