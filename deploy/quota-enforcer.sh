#!/bin/bash
# The sandbox image's disk-quota enforcer, started by the image's own bootstrap.
#
# Why it lives here and not in the control plane: the enforcement has to run
# *inside* the sandbox, where the workspace is, and it has to be there before an
# agent or a terminal writes anything. The control plane cannot start it, because
# the shipped isolated deployment uses the runner provider, which deliberately has
# no exec endpoint (docs/SANDBOX-DEPLOY.md §6). The image can: the base image runs
# RUN_HOOK_PRE_SERVICES after the sandbox user exists and before the services
# start, and this repository's Dockerfile.sandbox-agent sets that hook to this
# script.
#
# The budget comes from the kernel, not from a second configuration source: both
# sandbox providers start the container with `--ulimit fsize=<quota>`, and
# RLIMIT_FSIZE is inherited by every process in it. It is read from
# /proc/self/limits in bytes: `ulimit -f` is not portable here (bash counts 1024
# byte blocks, dash 512, and a quota that is off by 2x is worse than none).
#
# Two things changed after the third adversarial acceptance (2026-11-01):
#
#   1. The measurement is no longer "the workspace only". 4.84 GB in /tmp with the
#      workspace at 125 KB and no event was the finding: the budget is about
#      *disk*. The writable layers outside the workspace are measured as the
#      container's root filesystem minus this script's boot baseline, so the
#      image's own files are not charged to the tenant and a /tmp burst is.
#   2. The loop is wrapped in a root-owned supervisor. The enforcer itself must
#      run as gem (it has to signal gem's processes, and the sandbox has CapEff 0,
#      so a root process could not), which made it killable by the tenant:
#      `kill -9 $(pgrep -f quota-enforcer)` took the bound from 1.04x to 1.56x.
#      A root loop cannot be signalled by gem and restarts the child within
#      ~0.2 s, so killing it is a hiccup rather than an escape.
#
# Nothing here is secret and nothing is configurable by the tenant: the script and
# the binary are root-owned 0555 in the image, and the only input is the rlimit.
set -eu

log() {
  echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') quota-enforcer.sh: $*" >&2
}

soft=$(awk '/^Max file size/ {print $4}' /proc/self/limits)
case "$soft" in
  ''|unlimited)
    # No per-file cap means the deployment decided not to enforce a disk budget;
    # saying so once is better than starting a loop that would never fire.
    log "no RLIMIT_FSIZE on this container; not enforcing a workspace budget"
    exit 0
    ;;
esac
case "$soft" in
  *[!0-9]*)
    log "could not read RLIMIT_FSIZE from /proc/self/limits ($soft); not enforcing a workspace budget"
    exit 0
    ;;
esac

log "starting: limit=${soft} bytes (RLIMIT_FSIZE)"

# The user exists by now (the base image creates it before this hook), but the
# workspace may still be root-owned for a moment; wait for it briefly so the
# baseline below and the enforcer see the tree they are meant to.
for _ in $(seq 1 30); do
  if su -s /bin/sh gem -c 'test -w /workspace' 2>/dev/null; then
    break
  fi
  sleep 0.2
done

# The boot baseline: how large the container's writable layers already are
# (the image's own files), measured before any tenant process runs. Kept in /run,
# which is root-owned, so the tenant cannot rewrite it — a baseline the tenant
# controls would be a budget the tenant controls.
#
# Two things about this number are Linux-specific and were measured, not assumed
# (docs/LINUX-VERIFY.md, 2026-11-01):
#
#   * It is measured as gem, the uid the enforcer runs as. `du -sbx /` as root
#     sees ~470-570 MB more than as gem, because 17 directories in the image are
#     root-only; subtracting a root number from a gem walk makes the second term
#     negative (clamped to 0, so /tmp had no budget) or, worse, silently smaller
#     than the tenant's real usage. Same uid, same tree, same answer.
#   * It is written only once per container filesystem. /run is on the
#     container's writable layer, which survives `docker stop` / `docker start`
#     (the pause/resume path), so the hook runs again on a layer that already
#     holds the tenant's files. Measured: a second boot after a 200 MB /tmp
#     burst moved the baseline from 9,044,482,902 to 10,014,794,913 bytes, and
#     every byte of that burst stopped counting. Keeping the first number keeps
#     the budget. A fresh container (a new layer) has no file and measures.
baseline_dir=/run/piforge
baseline_file="$baseline_dir/disk-baseline"
mkdir -p "$baseline_dir"
chmod 755 "$baseline_dir"
if [ -s "$baseline_file" ]; then
  log "keeping the boot baseline already recorded: $(cat "$baseline_file") bytes (${baseline_file}); this container layer is reused, so re-measuring would fold the tenant's earlier writes into it"
else
  boot_bytes=$(su -s /bin/sh gem -c 'du -sbx / 2>/dev/null' | awk '{print $1}')
  root_bytes=$(du -sbx / 2>/dev/null | awk '{print $1}')
  case "$boot_bytes" in
    ''|*[!0-9]*) log "could not measure the boot baseline; the enforcer will measure the workspace only" ;;
    *)
      printf '%s\n' "$boot_bytes" > "$baseline_file"
      chmod 644 "$baseline_file"
      log "writable-layer baseline: ${boot_bytes} bytes as gem (${baseline_file}); root sees ${root_bytes:-unknown} bytes, and the difference is the root-only part of the image the enforcer cannot read"
      ;;
  esac
fi

# The supervisor. Root-owned and detached, it starts the enforcer as gem and
# restarts it whenever it exits — a tenant `kill -9` of the gem process is
# therefore a ~0.2 s gap in enforcement, not the end of it. It dies with the
# container, which is the lifecycle it should have.
nohup setsid /bin/bash -c "
  while :; do
    su -s /bin/sh gem -c 'exec /opt/piforge/piforged quota-enforcer -limit $soft -root /workspace -sysroot / -baseline $baseline_file -interval 100ms -kill-after 12' >>/tmp/quota-enforcer.log 2>&1
    echo \$(date -u +%Y-%m-%dT%H:%M:%SZ) quota-enforcer-supervisor: the enforcer exited, restarting >>/tmp/quota-enforcer.log
    sleep 0.2
  done
" >>/tmp/quota-enforcer-supervisor.log 2>&1 &

sleep 0.5
if pgrep -u 1000 -f 'quota-enforcer' >/dev/null 2>&1; then
  log "running as gem, pid=$(pgrep -u 1000 -f 'quota-enforcer' | head -1), supervisor=root, log=/tmp/quota-enforcer.log"
else
  log "WARNING: the enforcer did not stay up; the platform watchdog is the remaining bound"
fi
