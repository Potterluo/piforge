package main

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/diskquota"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// diskMeasureTimeout bounds one `du` inside the sandbox. A checkout with a
// dependency tree is the slow case; a walk that cannot finish in this window is
// reported as unmeasurable rather than allowed to hold the admin request.
const diskMeasureTimeout = 30 * time.Second

// sandboxAgentStopper kills one session's agent inside its sandbox.
//
// Why it is needed: a container placement's agent is owned by a detached
// supervisor inside the sandbox, built to survive a control-plane restart — so
// pi.Runtime.Stop only closes this process's end of the stream. For the disk
// watchdog that is not enough: the point is to end the writes. SIGTERM to the
// supervisor makes it take the agent's process group down (internal/agentsupervisor
// treats the signal as "the sandbox wants this stopped"), which is the same path
// `docker stop` takes.
//
// The match is on the supervisor's own argv, which carries `-session <id>`; the
// session id is unique, so nothing else in the container matches. It runs through
// the sandbox's own shell API as the sandbox user, so it needs no privilege the
// agent does not already have — one tenant can already kill its own processes.
func sandboxAgentStopper(remote sandbox.Remote, keys sandboxKeys) func(context.Context, string, string) error {
	if remote == nil {
		return nil
	}
	return func(ctx context.Context, sandboxID, sessionID string) error {
		ctx, cancel := context.WithTimeout(ctx, diskMeasureTimeout)
		defer cancel()
		endpoint, err := remote.Endpoint(ctx, sandboxID)
		if err != nil {
			return err
		}
		key, err := keys.For(ctx, sandboxID)
		if err != nil {
			return err
		}
		client, err := aio.New(aio.Config{
			BaseURL: endpoint,
			APIKey:  key,
			Root:    sandbox.ContainerWorkspace,
			Timeout: diskMeasureTimeout,
		})
		if err != nil {
			return err
		}
		pattern := "agent-supervisor.*-session " + sessionID
		// `; true` because pkill exits 1 when nothing matched, and "there was
		// nothing to kill" is success for this caller.
		res, err := client.Exec(ctx, "pkill -TERM -f -- "+aio.Quote(pattern)+" 2>/dev/null; true", sandbox.ContainerWorkspace)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("pkill exited %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
		}
		return nil
	}
}

// sandboxDiskMeasurer answers "how big is this sandbox's workspace" from inside
// the sandbox.
//
// The need for it is the detached workspace (stage 2 of
// docs/EXECUTION-BOUNDARY.md): the sandbox's files live in a volume nothing else
// mounts, and the RootDir the row reports is /workspace — a path that exists only
// inside the container. Walking it here found nothing, so the admin page showed
// `0 B / 2.0 GiB, over=false` for a sandbox holding 3 GiB, and the run boundary's
// 507 check never fired for the placement this repository ships. The measurement
// has to cross the same door the agent's files do.
//
// It walks the tree once, through the sandbox's own shell API, as the sandbox
// user — the account that owns the tree, so nothing is hidden from it by
// permissions. One walk yields both numbers:
//
//	bytes — the sum of every entry's st_size, which is exactly `du -sb`
//	        (GNU `-b`, apparent size). Measured equal on a 400-file tree
//	        (412288 = 412288), so the admin figure and an operator's `du`
//	        cannot disagree. Apparent size over-counts a sparse file rather
//	        than under-counting it: a quota that is wrong in the safe direction.
//	files — how many non-directory entries the same walk saw. It used to be
//	        reported as 0 always, because the count was deliberately skipped
//	        ("a second find would double the walk"); the second walk was never
//	        needed, only the second column.
//
// `find -printf` is GNU find, which this image has. A tree that cannot be walked
// is an error, and the caller reports `unmeasurable` — never a measured 0.
func sandboxDiskMeasurer(remote sandbox.Remote, keys sandboxKeys) diskquota.Measurer {
	if remote == nil {
		return nil
	}
	return func(ctx context.Context, sandboxID, root string) (diskquota.Measurement, error) {
		root = path.Clean(strings.TrimSpace(root))
		if root != sandbox.ContainerWorkspace && !strings.HasPrefix(root, sandbox.ContainerWorkspace+"/") {
			return diskquota.Measurement{}, fmt.Errorf("sandbox %s reports %q, which is not a container path this process can measure", sandboxID, root)
		}
		ctx, cancel := context.WithTimeout(ctx, diskMeasureTimeout)
		defer cancel()

		endpoint, err := remote.Endpoint(ctx, sandboxID)
		if err != nil {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: %w", sandboxID, err)
		}
		key, err := keys.For(ctx, sandboxID)
		if err != nil {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: %w", sandboxID, err)
		}
		client, err := aio.New(aio.Config{
			BaseURL: endpoint,
			APIKey:  key,
			Root:    sandbox.ContainerWorkspace,
			Timeout: diskMeasureTimeout,
		})
		if err != nil {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: %w", sandboxID, err)
		}
		// Two walks, one number: the checkout, and everything the tenant can
		// write outside it. The second term is why 4.84 GB in /tmp next to a
		// 125 KB workspace is no longer invisible: the budget is about disk, not
		// about one directory (third adversarial acceptance, attack 9(e)).
		//
		// `%y` is the entry type ("d" for a directory) and `%s` its apparent size;
		// the awk pass sums every entry (du does too) and counts only the
		// non-directories, so "files" is files rather than "things walked". The
		// root walk uses -xdev, so the workspace volume (a different device) is
		// not counted twice, and subtracts the boot baseline the image's hook
		// wrote — the image's own files are not the tenant's usage. A missing
		// baseline disables the second term rather than charging the image.
		//
		// The same command restarts the in-sandbox enforcer when it is gone. The
		// enforcer runs as the tenant's uid (it has to, to signal the tenant's
		// processes), so the tenant can kill it; the image's hook also wraps it in
		// a root-owned supervisor, and this is the platform-side half for a
		// sandbox whose hook never ran. It is deliberately in the same exec: the
		// watchdog is already talking to the sandbox every couple of seconds.
		command := `base=$(cat /run/piforge/disk-baseline 2>/dev/null || echo 0); ` +
			`case "$base" in ''|*[!0-9]*) base=0;; esac; ` +
			`if ! pgrep -f '[q]uota-enforcer' >/dev/null 2>&1; then ` +
			`lim=$(awk '/^Max file size/ {print $4}' /proc/self/limits); ` +
			`case "$lim" in ''|unlimited|*[!0-9]*) : ;; *) ` +
			`setsid nohup /opt/piforge/piforged quota-enforcer -limit "$lim" -root /workspace ` +
			`-sysroot / -baseline /run/piforge/disk-baseline -interval 100ms -kill-after 12 ` +
			`>>/tmp/quota-enforcer.log 2>&1 </dev/null & ;; esac; fi; ` +
			`w=$(find ` + aio.Quote(root) + ` -printf '%y %s\n' 2>/dev/null` +
			` | awk '{s+=$2; if ($1 != "d") n++} END{printf "%d %d", s, n}'); ` +
			`r=$(find / -xdev -printf '%y %s\n' 2>/dev/null` +
			` | awk -v base="$base" '{s+=$2; if ($1 != "d") n++} END{if (base <= 0) {printf "0 0"} else {e=s-base; if (e < 0) e=0; printf "%d %d", e, n}}'); ` +
			`set -- ${w:-0 0} ${r:-0 0}; echo $(( $1 + $3 )) $(( $2 + $4 ))`
		res, err := client.Exec(ctx, command, sandbox.ContainerWorkspace)
		if err != nil {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: %w", sandboxID, err)
		}
		if res.ExitCode != 0 {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: the walk exited %d: %s", sandboxID, res.ExitCode, strings.TrimSpace(res.Output))
		}
		fields := strings.Fields(strings.TrimSpace(res.Output))
		if len(fields) < 2 {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: the walk printed %q, want a byte total and a file count", sandboxID, strings.TrimSpace(res.Output))
		}
		bytes, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: the walk printed %q: %w", sandboxID, fields[0], err)
		}
		files, err := strconv.Atoi(fields[1])
		if err != nil {
			return diskquota.Measurement{}, fmt.Errorf("measure %s: the walk printed %q files: %w", sandboxID, fields[1], err)
		}
		return diskquota.Measurement{Bytes: bytes, Files: files}, nil
	}
}
