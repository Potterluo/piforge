// Package quotaenforcer is the in-sandbox half of the disk budget (FR-5.8).
//
// # Why it exists
//
// The two mechanisms the product already had do not bound the *total*:
//
//   - RLIMIT_FSIZE caps one file at the budget, so `fallocate -l 3G` and a 3 GiB
//     `dd` both die at exactly 2 GiB. A tree of files each just under the cap is
//     not affected by it at all.
//   - the control plane's watchdog measures the workspace from inside the
//     sandbox every few seconds. Between two measurements a writer at 700 MB/s
//     (measured on this host) puts several GiB on the volume.
//
// Measured before this package existed, on the isolated stack: a tenant writing
// 8 MiB files reached 7.03 GB into a 2 GiB budget — 3.27x — in ten seconds, with
// the sandbox still READY and the writer still running. The watchdog had only
// logged its first warning.
//
// # What this does
//
// A small loop *inside* the sandbox, started by the sandbox image at boot
// (deploy/quota-enforcer.sh). It walks the workspace with `du` on a short
// interval and, the moment the tree is over the budget, SIGSTOPs the processes
// that have a writable file open below the workspace root. They cannot write
// again until someone continues them; the platform's watchdog then pauses the
// sandbox through its normal path. If a frozen process is replaced by a new
// writer (the tenant starts another shell), later readings escalate to SIGKILL.
//
// # What it is not
//
// It is not a filesystem quota: the enforcement is still "measure, then react",
// so the overshoot is bounded by the write rate and the interval, not by zero.
// Measured after the change: see docs/SANDBOX-DEPLOY.md §6.
//
// It is also not, by itself, a guarantee against a tenant that kills it. The
// process runs as the same uid as the tenant (it must, to be allowed to signal
// the tenant's processes), so `pkill -f quota-enforcer` ends it. Since the third
// adversarial acceptance there are two answers to that, in this order:
//
//   - the image's hook starts a *root-owned* supervisor loop around it
//     (deploy/quota-enforcer.sh). The tenant cannot signal another uid — the
//     sandbox has CapEff 0 — so the loop restarts the enforcer within ~0.2 s of
//     it being killed, indefinitely;
//   - the platform's disk watchdog also restarts it when a measurement finds it
//     missing (cmd/piforged, one `pgrep` in the measurement command), so a
//     sandbox whose hook never ran is repaired without a container restart.
//
// # What it measures
//
// The budget is about disk, not about one directory. The workspace is measured
// on the volume it lives on, and the container's writable layer outside it
// (/tmp, /var/tmp, /home/gem — where the third acceptance put 4.84 GB while the
// workspace stayed at 125 KB) is measured as the root filesystem minus the size
// it had at boot, so the image's own files are not charged to the tenant.
package quotaenforcer

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Mode is the argv[1] that makes piforged run the enforcer instead of the server.
//
// It follows the same pattern as the agent launcher and the supervisor
// (internal/liveness, internal/agentsupervisor): the binary is already inside the
// sandbox image, root-owned and executable, so a small mode of it is one file
// instead of a second binary in the image.
const Mode = "quota-enforcer"

// Defaults. The interval is short because the walk it bounds is cheap: `du -sbx`
// over a 2 GiB / 256-file tree measured under 1 ms on the bench host, i.e. the
// cost is the wake-up, not the walk. On a burst the overshoot is then
// (interval + reaction) x write rate rather than the seconds a platform-side
// watchdog needs; the measured number is in docs/SANDBOX-DEPLOY.md §6.
const (
	DefaultInterval = 100 * time.Millisecond
	// MaxInterval caps the back-off applied when the walk itself is slow (a
	// checkout with hundreds of thousands of files). Without it the loop would
	// spend all its time walking; with it, a huge tree is checked every couple of
	// seconds and the platform watchdog covers the rest.
	MaxInterval = 5 * time.Second
	// SlowWalk is when NextInterval starts backing off: a walk that takes longer
	// than this is no longer "free" and repeating it every 250 ms would spend the
	// tenant's CPU on measuring.
	SlowWalk = 50 * time.Millisecond
	// DefaultKillAfter is how many consecutive over-budget readings a frozen
	// writer gets before it is killed instead. A frozen writer cannot grow the
	// tree, so reaching this means new writers keep appearing.
	DefaultKillAfter = 12
)

// growthFloor is how much the tree must grow between two readings before it
// counts as "still growing". A frozen writer cannot add a mebibyte, so anything
// smaller is du noise (a directory entry, an inode change) rather than a writer —
// and treating that noise as growth escalated frozen writers to SIGKILL after a
// few seconds, which is the wrong outcome for a tenant whose only sin was a
// burst.
const growthFloor = 1 << 20

// Action is what one reading says to do.
type Action int

const (
	// ActionNone: under budget (or enforcement disabled).
	ActionNone Action = iota
	// ActionFreeze: SIGSTOP the writers. Reversible, and it stops the bytes
	// immediately.
	ActionFreeze
	// ActionKill: SIGKILL the writers. For a tenant that keeps starting new ones
	// after being frozen.
	ActionKill
)

// String makes the decision readable in the log and in tests.
func (a Action) String() string {
	switch a {
	case ActionFreeze:
		return "freeze"
	case ActionKill:
		return "kill"
	default:
		return "none"
	}
}

// Policy is everything the loop needs. It is a value so tests can pin the
// decision without a sandbox.
type Policy struct {
	// LimitBytes is the workspace budget. Zero disables the enforcer.
	LimitBytes int64
	// Root is the workspace root as this process sees it ("/workspace").
	Root string
	// Interval is the base poll interval (DefaultInterval when zero).
	Interval time.Duration
	// KillAfter escalates a freeze to a kill after this many consecutive
	// over-budget readings (DefaultKillAfter when zero).
	KillAfter int
	// SysRoot is the container's root filesystem, measured so /tmp and /home/gem
	// are inside the budget too. Empty disables the second term (a host build or
	// a test that only wants the workspace walk).
	SysRoot string
	// BaselineFile holds the size of the container's writable layers at boot. The
	// image's hook measures it before any tenant process runs
	// (deploy/quota-enforcer.sh); subtracting it is what makes "root filesystem
	// minus baseline" the tenant's own bytes rather than the image's.
	//
	// A missing or unreadable baseline disables the second term instead of
	// counting the image as usage: over-counting would pause every sandbox at
	// boot, which is worse than the gap it would close.
	BaselineFile string
	// SysInterval is how often the second term is re-measured. It walks the whole
	// container, so it is much slower than the workspace walk; the loop adds the
	// cached value on every 100 ms reading.
	SysInterval time.Duration
}

// DefaultSysInterval is how often the non-workspace term is measured. One second
// is the compromise between "a /tmp burst is inside the bound" and "measuring the
// container's whole root four times a second".
const DefaultSysInterval = time.Second

// DefaultSysRoot and DefaultBaselineFile are where the image puts the container's
// root filesystem and the boot-time size of its writable layers.
const (
	DefaultSysRoot      = "/"
	DefaultBaselineFile = "/run/piforge/disk-baseline"
)

// sysInterval resolves the second-term interval.
func (p Policy) sysInterval() time.Duration {
	if p.SysInterval > 0 {
		return p.SysInterval
	}
	return DefaultSysInterval
}

// killAfter resolves the policy's escalation threshold.
func (p Policy) killAfter() int {
	if p.KillAfter > 0 {
		return p.KillAfter
	}
	return DefaultKillAfter
}

// Decide is the whole enforcement rule, as a pure function.
//
// overReadings is the number of consecutive readings at or above the budget,
// including this one; grew says the tree is larger than the previous reading.
//
//   - under the budget: nothing, ever;
//   - over, and this is not the first reading over: freeze. Freezing first is
//     deliberate — SIGSTOP is reversible, and the process that is holding the
//     bytes is stopped before anyone decides the tenant should lose it;
//   - still over after the escalation threshold *and* still growing: kill. A
//     frozen process cannot grow anything, so growth at that point means new
//     writers were started.
func Decide(p Policy, used int64, overReadings int, grew bool) Action {
	if p.LimitBytes <= 0 || used <= p.LimitBytes {
		return ActionNone
	}
	if overReadings < 1 {
		overReadings = 1
	}
	if overReadings >= p.killAfter() && grew {
		return ActionKill
	}
	return ActionFreeze
}

// NextInterval is how long to wait before the next walk: the base interval while
// the walk is cheap, three times the walk when it is not, never above MaxInterval.
func NextInterval(base, walk time.Duration) time.Duration {
	if base <= 0 {
		base = DefaultInterval
	}
	if walk <= SlowWalk {
		return base
	}
	next := 3 * walk
	if next < base {
		next = base
	}
	if next > MaxInterval {
		next = MaxInterval
	}
	return next
}

// Marker is what the enforcer last did, written into the workspace so the
// platform, the operator and a later acceptance run can see it. It is a file
// rather than a log line because the control plane does not read this process's
// stdout.
type Marker struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Used   int64     `json:"used_bytes"`
	// Workspace and System split the total, so an operator can see which layer
	// grew: "the checkout is 120 KB and /tmp is 4.8 GB" was the finding.
	Workspace int64 `json:"workspace_bytes"`
	System    int64 `json:"system_bytes"`
	Limit     int64 `json:"limit_bytes"`
	Writers   []int `json:"writers"`
	// Readings counts how many times the enforcer has acted in this container's
	// life, so "it fired once" and "it is firing constantly" are distinguishable.
	Readings int `json:"readings"`
}

// Run is the loop. It blocks until ctx is cancelled (the container stopping is
// the ordinary case). A failed walk is logged and retried; it is never fatal,
// because a workspace that cannot be measured must not end enforcement for good.
func Run(ctx context.Context, p Policy, logger func(format string, args ...any)) error {
	if logger == nil {
		logger = func(string, ...any) {}
	}
	if p.LimitBytes <= 0 {
		return errors.New("quota-enforcer: a positive -limit is required")
	}
	if p.Root == "" {
		p.Root = "/workspace"
	}
	if p.Interval <= 0 {
		p.Interval = DefaultInterval
	}
	// Own session, so "kill the process group" from the tenant's side and
	// "SIGSTOP the writers" from ours can never meet at this process.
	isolate()

	// The container's writable layers outside the workspace. A missing baseline
	// turns the second term off instead of charging the image's own files to the
	// tenant: over-counting would pause every sandbox at boot, which is a worse
	// failure than the gap it would close.
	var baseline int64
	sysRoot := p.SysRoot
	if sysRoot != "" {
		baseline = ReadBaseline(p.BaselineFile)
		if baseline <= 0 {
			logger("quota-enforcer: no boot baseline for the container's writable layers; measuring the workspace only",
				"baseline_file", p.BaselineFile)
			sysRoot = ""
		}
	}

	logger("quota-enforcer: watching", "root", p.Root, "sys_root", sysRoot,
		"baseline_bytes", baseline, "limit_bytes", p.LimitBytes,
		"interval", p.Interval.String(), "sys_interval", p.sysInterval().String(),
		"escalate_after", p.killAfter())

	var (
		strikes         int
		lastUsed        int64
		readings        int
		sysUsed         int64
		sysMeasuredAt   time.Time
		lastAct         = time.Time{}
		lastNoWriterLog time.Time
		lastNegativeLog time.Time
		// halted remembers what the last action stopped. A burst is usually a
		// loop that opens a new writer every few milliseconds, so the reading that
		// happens to land between two of them finds nothing to signal; remembering
		// the last set (and re-signalling it, which is idempotent) closes that gap
		// without guessing which process to freeze.
		halted = newHalted()
	)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		start := time.Now()
		workspace, err := Measure(ctx, p.Root)
		walk := time.Since(start)

		// The second term: everything the tenant can write outside the workspace
		// (/tmp, /var/tmp, /home/gem), re-measured on its own slower cadence and
		// added to every reading. Without it the budget was a per-directory number
		// and 4.84 GB in /tmp left the workspace at 125 KB with no event.
		if sysRoot != "" && (sysMeasuredAt.IsZero() || time.Since(sysMeasuredAt) >= p.sysInterval()) {
			total, serr := MeasureRoot(ctx, sysRoot)
			var partial *WalkError
			usable := serr == nil || errors.As(serr, &partial)
			var warned int64
			if usable {
				extra := total - baseline
				if extra < 0 {
					// A walk smaller than the boot baseline means the two numbers
					// were not measured the same way — the classic cause is the
					// baseline written by root and this walk run as the sandbox
					// uid, which cannot read root-only directories. Clamping to 0
					// is right (the negative is not the tenant's), but saying it
					// once a minute is what turns "the second term never fires"
					// into a line an operator can act on.
					warned = extra
					extra = 0
				}
				sysUsed = extra
				sysMeasuredAt = time.Now()
				if partial != nil {
					logger("quota-enforcer: the container walk could not read part of the tree; the budget counts what it could",
						"sys_root", sysRoot, "unreadable_entries", partial.Unreadable, "measured_bytes", partial.Bytes)
				}
				if warned < 0 && time.Since(lastNegativeLog) > time.Minute {
					logger("quota-enforcer: the container walk is smaller than the boot baseline; check that the baseline was measured as the same uid as this process",
						"sys_root", sysRoot, "walk_bytes", total, "baseline_bytes", baseline, "difference", warned)
					lastNegativeLog = time.Now()
				}
			} else {
				logger("quota-enforcer: could not measure the container's writable layers",
					"sys_root", sysRoot, "error", serr.Error())
			}
		}
		used := workspace + sysUsed
		if err != nil {
			// A transient failure must not silently end enforcement; it also must
			// not count as "under budget".
			logger("quota-enforcer: could not measure the workspace", "root", p.Root, "error", err.Error())
			strikes = 0
		} else {
			// Growth has a floor. `du` moves by a few bytes between readings on a
			// tree nobody is writing (a directory entry appears or goes), and with a
			// zero floor those blips counted as "still growing" and escalated a
			// frozen writer to SIGKILL after a few seconds. A frozen writer cannot
			// add a mebibyte, so a mebibyte is what "growing" means here.
			grew := used > lastUsed+growthFloor
			if used <= p.LimitBytes {
				strikes = 0
				halted.clear()
			} else {
				strikes++
			}
			action := Decide(p, used, strikes, grew)
			if action != ActionNone {
				readings++
				signalled := apply(p, used, action, halted, &lastNoWriterLog, logger)
				if len(signalled) > 0 || time.Since(lastAct) > time.Minute {
					if len(signalled) > 32 {
						signalled = signalled[:32]
					}
					writeMarker(p.Root, Marker{
						At: time.Now().UTC(), Action: action.String(), Used: used,
						Workspace: workspace, System: sysUsed,
						Limit: p.LimitBytes, Writers: signalled, Readings: readings,
					})
					lastAct = time.Now()
				}
			}
			lastUsed = used
		}
		if !sleep(ctx, NextInterval(p.Interval, walk)) {
			return nil
		}
	}
}

// halted remembers the process tree the last action stopped, and for how long it
// is worth re-signalling.
//
// It exists because a burst is usually a loop: `while :; do dd ...; done` opens a
// new writer every few milliseconds, and a reading that lands between two of them
// finds no writable fd anywhere. Without memory that reading is a no-op and the
// tree keeps growing until the next one. Re-signalling a process that is already
// stopped costs one syscall and cannot hurt it, so the fix is to keep telling the
// same tree to stop for as long as the sandbox is over budget.
type halted struct {
	pids map[int]time.Time
	// keep is how long a pid stays worth re-signalling after the last time it
	// held the workspace open.
	keep time.Duration
}

func newHalted() *halted {
	return &halted{pids: map[int]time.Time{}, keep: 30 * time.Second}
}

// clear forgets everything: the sandbox is under budget again, so there is
// nothing to keep telling to stop.
func (h *halted) clear() { h.pids = map[int]time.Time{} }

// merge returns the pids to signal now: the ones holding the workspace open plus
// the ones this loop stopped recently and that still exist.
func (h *halted) merge(now time.Time, current []int) []int {
	out := make([]int, 0, len(current)+len(h.pids))
	seen := make(map[int]bool, len(current)+len(h.pids))
	for _, pid := range current {
		seen[pid] = true
		h.pids[pid] = now
		out = append(out, pid)
	}
	for pid, at := range h.pids {
		if now.Sub(at) > h.keep {
			delete(h.pids, pid)
			continue
		}
		if seen[pid] || !processAlive(pid) {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
	}
	return out
}

// apply freezes or kills the writers (and the tree they run in), and returns the
// pids it signalled for the marker.
func apply(p Policy, used int64, action Action, h *halted, lastNoWriterLog *time.Time, logger func(string, ...any)) []int {
	now := time.Now()
	// Both roots: a burst into /tmp is a writer too, and leaving it out is how
	// the enforcer would log "over budget with no writer" while /tmp grew.
	roots := []string{p.Root}
	if p.SysRoot != "" {
		roots = append(roots, p.SysRoot)
	}
	current, err := Writers(roots...)
	if err != nil {
		logger("quota-enforcer: could not list the writers", "error", err.Error())
		return nil
	}
	pids := h.merge(now, current)
	if len(pids) == 0 {
		// Once per ten seconds, not once per reading: a sandbox that is over
		// budget with nothing writing is the platform's problem to pause, and
		// repeating the line four times a second buries the interesting one.
		if time.Since(*lastNoWriterLog) > 10*time.Second {
			logger("quota-enforcer: over budget with no writer holding the workspace open",
				"used_bytes", used, "limit_bytes", p.LimitBytes, "action", action.String())
			*lastNoWriterLog = time.Now()
		}
		return nil
	}
	signalled := make([]int, 0, len(pids))
	failed := 0
	for _, pid := range pids {
		if serr := signalProcess(pid, action); serr != nil {
			failed++
			continue
		}
		signalled = append(signalled, pid)
	}
	logger("quota-enforcer: acted on the writers",
		"action", action.String(), "used_bytes", used, "limit_bytes", p.LimitBytes,
		"writers", len(pids), "fresh", len(current), "signalled", len(signalled), "failed", failed)
	return signalled
}

// sleep waits for d, reporting false when the context was cancelled. It exists so
// the loop has one place where cancellation is handled.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = DefaultInterval
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// writeMarker records the last action inside the workspace. Failures are
// ignored on purpose: the enforcement already happened, and a marker that cannot
// be written must not turn into a crash loop.
func writeMarker(root string, m Marker) {
	dir := filepath.Join(root, ".piforge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	body, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "quota-brake.json.tmp")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, "quota-brake.json"))
}

// MaybeRun is the argv entry point, in the same shape as the agent launcher and
// the supervisor: `piforged quota-enforcer -limit <bytes> -root /workspace`.
func MaybeRun(ctx context.Context, argv []string) (int, bool) {
	if len(argv) < 2 || argv[1] != Mode {
		return 0, false
	}
	fs := flag.NewFlagSet(Mode, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	limit := fs.Int64("limit", 0, "workspace budget in bytes (0 refuses to start)")
	root := fs.String("root", "/workspace", "workspace root as this process sees it")
	interval := fs.Duration("interval", DefaultInterval, "how often to walk the workspace")
	killAfter := fs.Int("kill-after", DefaultKillAfter, "readings over budget before a frozen writer is killed")
	sysRoot := fs.String("sysroot", DefaultSysRoot, "the container's root filesystem, so writable layers outside the workspace are in the budget too (empty disables it)")
	baselineFile := fs.String("baseline", DefaultBaselineFile, "boot-time size of the container's writable layers, written by the image's hook")
	sysInterval := fs.Duration("sys-interval", DefaultSysInterval, "how often the writable layers outside the workspace are measured")
	if err := fs.Parse(argv[2:]); err != nil {
		return 2, true
	}
	logger := func(format string, args ...any) {
		// The loop logs key=value pairs rather than a formatted sentence, so the
		// line stays greppable: `quota-enforcer: acted on the writers action=freeze ...`
		line := format
		for i := 0; i+1 < len(args); i += 2 {
			line += " " + fmt.Sprintf("%v=%v", args[i], args[i+1])
		}
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
	}
	if err := Run(ctx, Policy{
		LimitBytes: *limit, Root: *root, Interval: *interval, KillAfter: *killAfter,
		SysRoot: *sysRoot, BaselineFile: *baselineFile, SysInterval: *sysInterval,
	}, logger); err != nil {
		fmt.Fprintln(os.Stderr, "quota-enforcer:", err)
		return 1, true
	}
	return 0, true
}

// HumanBytes renders a byte count for a log line. It mirrors diskquota.HumanBytes
// without importing it: this package runs inside the sandbox and must stay small.
func HumanBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	i := -1
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + units[i]
}
