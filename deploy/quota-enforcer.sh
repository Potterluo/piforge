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
# It runs as gem because the enforcer has to signal gem's processes, and killing
# or stopping another uid needs CAP_KILL, which the sandbox drops.
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
# marker file can be written.
for _ in $(seq 1 30); do
  if su -s /bin/sh gem -c 'test -w /workspace' 2>/dev/null; then
    break
  fi
  sleep 0.2
done

# nohup + a redirect: the hook's shell exits immediately and the loop must outlive
# it. It dies with the container, which is exactly the lifecycle it should have.
nohup su -s /bin/sh gem -c "exec /opt/piforge/piforged quota-enforcer -limit $soft -root /workspace -interval 100ms -kill-after 12" \
  >>/tmp/quota-enforcer.log 2>&1 &

sleep 0.3
if pgrep -u 1000 -f 'quota-enforcer' >/dev/null 2>&1; then
  log "running as gem, pid=$(pgrep -u 1000 -f 'quota-enforcer' | head -1), log=/tmp/quota-enforcer.log"
else
  log "WARNING: the enforcer did not stay up; the platform watchdog is the remaining bound"
fi
