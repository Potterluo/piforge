#!/usr/bin/env bash
# verify-linux.sh — assert PiForge's Linux assumptions on a Linux machine.
#
# The product's isolation proofs were built on Windows plus Docker Desktop's
# Linux container VM. This script is the Linux half: it runs the same checks the
# Windows drill runs where Linux can prove them, states which ones only a native
# Linux host can prove, and refuses to pretend about the rest.
#
#   ./scripts/verify-linux.sh              everything this machine can prove
#   ./scripts/verify-linux.sh --skip-docker   structural checks only (no boot)
#   ./scripts/verify-linux.sh --keep         leave the probe container behind
#
# It prints PASS/FAIL/SKIP lines and exits non-zero if anything FAILed. A SKIP is
# never a pass: each one names the command that would prove it and where.
#
# No provider key is needed. The one check that needs a real model turn is gated:
# it runs when PIFORGE_VERIFY_KEY is set (or a key is found in
# ~/.dsh/.credentials.yaml) and PIFORGE_URL answers, and is SKIPped explicitly
# otherwise. The key is never echoed.
#
# What the ids mean: docs/LINUX-VERIFY.md carries the assumption table, and each
# check id (L1..L17, N1..N5) is a row there with its (a)/(b)/(c) classification.
#
# L17 is a precondition, not a product assertion: it refuses to run the container
# section on a sandbox image whose piforge.source-digest label is not this tree's
# digest, and prints the rebuild command. Use `PIFORGE_SANDBOX_IMAGE` to name the
# image, `PIFORGE_AGENT_IMAGE` to name the control-plane image it was built FROM.
set -u

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
SANDBOX_IMAGE=${PIFORGE_SANDBOX_IMAGE:-}
BASE_IMAGE=${PIFORGE_BASE_IMAGE:-enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox:1.11.0}
PIFORGE_URL=${PIFORGE_URL:-http://127.0.0.1:7700}
CONTAINER=${PIFORGE_VERIFY_CONTAINER:-piforge-verify-linux}
VOLUME=${PIFORGE_VERIFY_VOLUME:-piforge-verify-linux-ws}
KEEP=0
SKIP_DOCKER=0
BUDGET_BYTES=$((2 * 1024 * 1024 * 1024))

PASSED=0
FAILED=0
SKIPPED=0
FAILS=()

pass() { PASSED=$((PASSED + 1)); printf 'PASS  %s  %s\n' "$1" "$2"; }
fail() { FAILED=$((FAILED + 1)); FAILS+=("$1: $2"); printf 'FAIL  %s  %s\n' "$1" "$2"; }
skip() { SKIPPED=$((SKIPPED + 1)); printf 'SKIP  %s  %s\n' "$1" "$2"; }
info() { printf 'INFO  %s\n' "$*"; }
section() { printf '\n== %s ==\n' "$*"; }

for arg in "$@"; do
  case "$arg" in
    --skip-docker) SKIP_DOCKER=1 ;;
    --keep) KEEP=1 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$arg" >&2; exit 2 ;;
  esac
done

inside_container() {
  [ -f /.dockerenv ] || [ -f /run/.containerenv ] || grep -qa 'docker\|containerd' /proc/1/cgroup 2>/dev/null
}

cleanup() {
  if [ "$KEEP" -eq 1 ]; then
    info "keeping $CONTAINER and volume $VOLUME for inspection"
    return
  fi
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Structural checks: what the deployment files promise, on any Linux machine.
# ---------------------------------------------------------------------------
section "structural (deployment files)"

if grep -Eq '^[[:space:]]*-[[:space:]]*DAC_OVERRIDE[[:space:]]*$' "$ROOT_DIR/docker-compose.yml"; then
  pass L2 "docker-compose.yml still drops DAC_OVERRIDE, so the control plane's 0444 skill copy binds root"
else
  fail L2 "docker-compose.yml no longer drops DAC_OVERRIDE; copy-ro would be aspirational again"
fi

if grep -q 'cap_drop:' "$ROOT_DIR/docker-compose.yml"; then
  pass L2 "the control-plane container drops at least one capability (cap_drop present)"
else
  fail L2 "docker-compose.yml has no cap_drop block"
fi

# The sandbox container is the opposite decision: it *keeps* DAC_OVERRIDE for the
# image's own bootstrap, so the 0555 launcher binds the tenant, not root.
if grep -q 'var sandboxCapAdd = \[\]string{"CHOWN", "DAC_OVERRIDE"' "$ROOT_DIR/backend/internal/sandbox/docker.go"; then
  pass L7 "the sandbox provider still adds DAC_OVERRIDE (the launcher mode binds gem, not root)"
else
  fail L7 "sandboxCapAdd changed; re-check whether the 0555 launcher now binds root too"
fi

if grep -q 'DAC_OVERRIDE dropped (...00c9) could not' "$ROOT_DIR/deploy/Dockerfile.sandbox-agent"; then
  pass L7 "Dockerfile.sandbox-agent documents the measured DAC_OVERRIDE split, not the old 'root cannot write' claim"
else
  skip L7 "the corrected comment was reworded; read deploy/Dockerfile.sandbox-agent by hand"
fi

if grep -q 'if \[ -s "\$baseline_file" \]' "$ROOT_DIR/deploy/quota-enforcer.sh"; then
  pass L6 "the image hook writes the boot baseline only once per container layer"
else
  fail L6 "deploy/quota-enforcer.sh rewrites the baseline on every start; a pause/resume would fold tenant bytes into it"
fi

if grep -q "su -s /bin/sh gem -c 'du -sbx /" "$ROOT_DIR/deploy/quota-enforcer.sh"; then
  pass L5 "the image hook measures the baseline as the sandbox uid, the same uid the enforcer walks with"
else
  fail L5 "the baseline is measured as root; the enforcer subtracts a root number from a uid-1000 walk"
fi

if grep -q 'classifyDu' "$ROOT_DIR/backend/internal/quotaenforcer/measure.go"; then
  pass L4 "the enforcer treats du's exit 1 as a partial walk instead of no measurement"
else
  fail L4 "a partial walk is still fatal; the whole-container term would be dead"
fi

if grep -q 'X-PF-CSRF' "$ROOT_DIR/backend/internal/httpapi"/*.go 2>/dev/null; then
  pass L15 "the API still requires the CSRF header for mutating calls (this script's login flow)"
else
  skip L15 "CSRF header not found by grep; the agent-turn check may need adjusting"
fi

# ---------------------------------------------------------------------------
# Host checks: /proc, cgroup v2, capabilities, modes. True inside a container too.
# ---------------------------------------------------------------------------
section "kernel surface (this machine)"

if [ "$(uname -s)" = "Linux" ]; then
  pass L1 "uname -s = Linux ($(uname -r))"
else
  fail L1 "this is not Linux ($(uname -s)); the Linux assumptions cannot be checked here"
fi

if [ -r /proc/self/status ] && grep -q '^CapEff:' /proc/self/status; then
  CAP_EFF=$(awk '/^CapEff:/ {print $2}' /proc/self/status)
  pass L2 "/proc/self/status is readable (CapEff=$CAP_EFF)"
else
  fail L2 "/proc/self/status has no CapEff line; capabilities cannot be probed"
fi

# DAC_OVERRIDE is bit 1. The claim under test: 0444/0555 bind this process only
# when it cannot override them.
HAS_DAC_OVERRIDE=0
if [ -n "${CAP_EFF:-}" ]; then
  CAP_VALUE=$((16#${CAP_EFF}))
  if [ $((CAP_VALUE & 2)) -ne 0 ]; then HAS_DAC_OVERRIDE=1; fi
fi

PROBE_DIR=$(mktemp -d)
chmod 755 "$PROBE_DIR"
printf 'probe\n' >"$PROBE_DIR/SKILL.md"
chmod 444 "$PROBE_DIR/SKILL.md"
chmod 555 "$PROBE_DIR"
if ( : >"$PROBE_DIR/SKILL.md" ) 2>/dev/null; then
  MODE_BITS_BIND=0
else
  MODE_BITS_BIND=1
fi
chmod 755 "$PROBE_DIR"
chmod 644 "$PROBE_DIR/SKILL.md"
rm -rf "$PROBE_DIR"

if [ "$MODE_BITS_BIND" -eq 1 ]; then
  pass L9 "a 0444 file refuses this process's write: 0444/0555 are real here (copy-ro is honest)"
else
  if [ "$HAS_DAC_OVERRIDE" -eq 1 ]; then
    pass L9 "0444 does not bind this process, and CAP_DAC_OVERRIDE explains it: the mechanism must read 'copy', not 'copy-ro'"
  else
    fail L9 "0444 does not bind this process although CAP_DAC_OVERRIDE is clear; the read-only probe would lie"
  fi
fi

if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  pass L10 "cgroup v2 is mounted at /sys/fs/cgroup (cgroup.controllers present)"
  if [ -r /sys/fs/cgroup/pids.max ]; then
    PIDS_MAX=$(cat /sys/fs/cgroup/pids.max)
    case "$PIDS_MAX" in
      max|*[!0-9]*) skip L10 "pids.max is '$PIDS_MAX' on this host (no container limit to compare with docker --pids-limit)" ;;
      *) pass L10 "pids.max is readable and numeric ($PIDS_MAX)" ;;
    esac
  else
    skip L10 "/sys/fs/cgroup/pids.max is not readable in this namespace"
  fi
else
  skip L10 "cgroup v2 not visible from here; the container section checks it against the provider's flags"
fi

# ---------------------------------------------------------------------------
# Freshness (L17): the sandbox image must be the one *this tree* builds.
#
# A label nobody compares is decoration. The prebuilt piforge/sandbox-agent:isolated
# was built before the quota fixes, and the first run of this script discovered it
# by failing two quota assertions three hundred lines in — which reads like a
# product bug and cost a round. A stale image is not a product bug, so it is named
# here, before any assertion runs, with the command that fixes it.
#
# The digest comes from backend/cmd/sourcedigest — the same program
# scripts/up-isolated.ps1 labels the images with, so the two cannot disagree about
# what "stale" means. It needs Go; without it the check says so and the drill
# continues, because "unproven" must not look like either answer.
# ---------------------------------------------------------------------------
short_digest() { printf '%s' "$1" | cut -c1-12; }

digest_of() { # $1 dockerfile, $2 the control-plane image the sandbox was built FROM
  if [ -n "$2" ]; then
    ( cd "$ROOT_DIR/backend" && go run ./cmd/sourcedigest -root "$ROOT_DIR" \
        -dockerfile "$1" -agent-image "$2" 2>/dev/null )
  else
    ( cd "$ROOT_DIR/backend" && go run ./cmd/sourcedigest -root "$ROOT_DIR" -dockerfile "$1" 2>/dev/null )
  fi
}

rebuild_command() { # $1 image, $2 its parent ref, $3 the digest this tree expects
  printf 'docker build -f deploy/Dockerfile.sandbox-agent \\\n            --build-arg AGENT_IMAGE=%s \\\n            --build-arg PIFORGE_SOURCE_DIGEST=%s \\\n            -t %s .' \
    "${2:-piforge:local}" "${3:-<digest>}" "$1"
}

# sandbox_freshness sets SD_FRESH (fresh|stale|unknown), SD_WHY, and — when a
# digest could be computed — SD_WANT and SD_AGENT (the parent it was computed
# against), so the caller can print a rebuild command that will actually satisfy
# the check.
sandbox_freshness() {
  local image="$1" label candidate want
  SD_FRESH=unknown
  SD_WHY=""
  SD_WANT=""
  SD_AGENT=""
  label=$(docker image inspect -f '{{index .Config.Labels "piforge.source-digest"}}' "$image" 2>/dev/null | tr -d '\r\n')
  case "$label" in
    '<no value>') label="" ;;
  esac

  # The digest includes the full id of the parent image: the sandbox carries
  # piforged and pi from it, so a rebuilt control plane makes the sandbox stale
  # even when this tree did not change. Any locally present candidate can be the
  # parent — the drill runs against :isolated, :dev and :local tags — but the tree
  # half of the digest still has to match.
  for candidate in ${PIFORGE_AGENT_IMAGE:-} piforge:isolated piforge:local piforge:dev; do
    [ -n "$candidate" ] || continue
    docker image inspect "$candidate" >/dev/null 2>&1 || continue
    want=$(digest_of "deploy/Dockerfile.sandbox-agent" "$candidate")
    [ -n "$want" ] || continue
    if [ -z "$SD_WANT" ]; then
      SD_WANT=$want
      SD_AGENT=$candidate
    fi
    if [ -n "$label" ] && [ "$want" = "$label" ]; then
      SD_FRESH=fresh
      SD_WHY="$image carries this tree's digest ($(short_digest "$want")), built FROM $candidate"
      return 0
    fi
  done

  if [ -z "$label" ]; then
    SD_FRESH=stale
    SD_WHY="$image carries no piforge.source-digest label, so nothing says which tree it was built from (built by hand, or before the label existed)"
    return 1
  fi
  if [ -z "$SD_WANT" ]; then
    SD_FRESH=unknown
    SD_WHY="$image is labelled $(short_digest "$label"), but this tree's digest could not be computed here: it needs go and a local control-plane image (go run ./backend/cmd/sourcedigest -root . -dockerfile deploy/Dockerfile.sandbox-agent -agent-image piforge:isolated). Freshness is unproven, not asserted"
    return 2
  fi
  SD_FRESH=stale
  SD_WHY="$image was built from another tree: label $(short_digest "$label"), this tree $(short_digest "$SD_WANT") (parent $SD_AGENT)"
  return 1
}

# ---------------------------------------------------------------------------
# Docker checks: the sandbox image is Linux, so this is where the drill's
# assertions actually run.
# ---------------------------------------------------------------------------
section "sandbox container (the Linux half of the drill)"

if [ "$SKIP_DOCKER" -eq 1 ]; then
  skip L3 "container checks skipped (--skip-docker)"
elif ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  skip L3 "no usable docker daemon; run this section on a host with Docker"
else
  if [ -z "$SANDBOX_IMAGE" ]; then
    for candidate in piforge/sandbox-agent:isolated piforge/sandbox-agent:dev piforge/sandbox-agent:local; do
      if docker image inspect "$candidate" >/dev/null 2>&1; then SANDBOX_IMAGE=$candidate; break; fi
    done
  fi

  SD_FRESH=unknown
  SD_WHY=""
  SD_WANT=""
  SD_AGENT=""
  if [ -n "$SANDBOX_IMAGE" ]; then
    sandbox_freshness "$SANDBOX_IMAGE" || true
  fi

  if [ -z "$SANDBOX_IMAGE" ]; then
    skip L3 "no piforge sandbox image locally; build it with:
        docker build -f deploy/Dockerfile.sandbox-agent --build-arg AGENT_IMAGE=piforge:local -t piforge/sandbox-agent:dev .
      then set PIFORGE_SANDBOX_IMAGE=piforge/sandbox-agent:dev"
  elif [ "$SD_FRESH" = "stale" ]; then
    # Refuse, and say what to run. The assertions are not run at all: against a
    # stale image their failures describe an older tree, and a reader cannot tell
    # that from a regression.
    fail L17 "$SD_WHY; refusing to run the container assertions against it"
    info "rebuild it with:"
    info "$(rebuild_command "$SANDBOX_IMAGE" "$SD_AGENT" "$SD_WANT")"
    info "or, where pwsh is available: pwsh scripts/up-isolated.ps1 -Rebuild (it rebuilds all three images)"
    skip L3 "the container assertions did not run: $SANDBOX_IMAGE is not the image this tree builds"
  else
    if [ "$SD_FRESH" = "fresh" ]; then
      pass L17 "$SD_WHY"
    else
      skip L17 "$SD_WHY"
    fi
    info "sandbox image: $SANDBOX_IMAGE (base $BASE_IMAGE)"

    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker volume rm "$VOLUME" >/dev/null 2>&1 || true
    docker volume create "$VOLUME" >/dev/null

    # The flags mirror sandbox/limits.go and sandbox/docker.go: the five
    # capabilities the image needs, no-new-privileges, the pids/memory caps and
    # the RLIMIT_FSIZE budget.
    if ! docker run -d --name "$CONTAINER" \
      --security-opt no-new-privileges \
      --cap-drop ALL \
      --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER --cap-add SETUID --cap-add SETGID \
      --pids-limit 512 --memory 2g \
      --ulimit "fsize=${BUDGET_BYTES}:${BUDGET_BYTES}" \
      -e WORKSPACE=/workspace \
      -v "$VOLUME:/workspace" \
      "$SANDBOX_IMAGE" >/dev/null 2>&1; then
      fail L3 "the sandbox image did not start: docker logs $CONTAINER"
    else
      for _ in $(seq 1 60); do
        if docker exec "$CONTAINER" test -f /run/piforge/disk-baseline 2>/dev/null; then break; fi
        sleep 1
      done

      GEM_UID=$(docker exec "$CONTAINER" getent passwd gem 2>/dev/null | cut -d: -f3)
      [ -n "$GEM_UID" ] || GEM_UID=1000

      # L3: uid split. The control plane is root; the sandbox is uid 1000 and
      # holds no capability. Every chown/chmod claim downstream depends on that.
      if [ "$GEM_UID" = "1000" ]; then
        pass L3 "the sandbox user is uid 1000 (gem), the control plane runs as root: the uid split the proofs assume"
      else
        fail L3 "the sandbox user is uid $GEM_UID, not 1000; every chown/mode assumption shifts"
      fi

      GEM_CAPS=$(docker exec "$CONTAINER" su -s /bin/sh gem -c 'grep "^CapEff:" /proc/self/status' 2>/dev/null | awk '{print $2}')
      if [ "${GEM_CAPS:-x}" = "0000000000000000" ]; then
        pass L3 "gem's effective capability set is empty ($GEM_CAPS): 0444/0555 bind it and no signal reaches root"
      else
        fail L3 "gem holds capabilities (${GEM_CAPS:-unknown}); the mode-based guarantees do not hold"
      fi

      # L8: the launcher. root-owned 0555, refused to gem, and writable by root
      # exactly when the container carries DAC_OVERRIDE.
      LAUNCHER_MODE=$(docker exec "$CONTAINER" stat -c '%U:%G %a' /opt/piforge/piforged 2>/dev/null)
      case "$LAUNCHER_MODE" in
        "root:root 555") pass L8 "the agent launcher is root-owned 0555 ($LAUNCHER_MODE)" ;;
        *) fail L8 "the agent launcher is '$LAUNCHER_MODE', want root:root 555" ;;
      esac

      if docker exec "$CONTAINER" su -s /bin/sh gem -c 'printf x > /opt/piforge/piforged' >/dev/null 2>&1; then
        fail L8 "gem wrote the launcher; the tenant can repoint the supervisor it starts"
      else
        pass L8 "gem cannot write the launcher (EACCES)"
      fi

      docker exec "$CONTAINER" install -m 0555 /bin/true /opt/piforge/verify-0555 >/dev/null 2>&1
      ROOT_WROTE=0
      if docker exec "$CONTAINER" bash -c 'printf x > /opt/piforge/verify-0555' >/dev/null 2>&1; then ROOT_WROTE=1; fi
      ROOT_CAPS=$(docker exec "$CONTAINER" grep '^CapEff:' /proc/self/status 2>/dev/null | awk '{print $2}')
      ROOT_DAC=0
      if [ -n "${ROOT_CAPS:-}" ] && [ $((16#${ROOT_CAPS} & 2)) -ne 0 ]; then ROOT_DAC=1; fi
      if [ "$ROOT_WROTE" -eq "$ROOT_DAC" ]; then
        if [ "$ROOT_DAC" -eq 1 ]; then
          pass L7 "root writes a 0555 file because DAC_OVERRIDE is in the container's set (CapEff=$ROOT_CAPS): the mode binds gem, not root"
        else
          pass L7 "root cannot write a 0555 file and carries no DAC_OVERRIDE (CapEff=$ROOT_CAPS)"
        fi
      else
        fail L7 "root write=$ROOT_WROTE disagrees with CAP_DAC_OVERRIDE=$ROOT_DAC (CapEff=${ROOT_CAPS:-unknown})"
      fi
      docker exec "$CONTAINER" rm -f /opt/piforge/verify-0555 >/dev/null 2>&1

      # L10: cgroup v2 limits match what the provider asked for.
      CGROUP_PIDS=$(docker exec "$CONTAINER" cat /sys/fs/cgroup/pids.max 2>/dev/null)
      CGROUP_MEM=$(docker exec "$CONTAINER" cat /sys/fs/cgroup/memory.max 2>/dev/null)
      if [ "$CGROUP_PIDS" = "512" ] && [ "$CGROUP_MEM" = "2147483648" ]; then
        pass L10 "cgroup v2 pids.max=$CGROUP_PIDS and memory.max=$CGROUP_MEM match --pids-limit 512 --memory 2g"
      else
        fail L10 "cgroup v2 reports pids.max=${CGROUP_PIDS:-?} memory.max=${CGROUP_MEM:-?}, want 512 and 2147483648"
      fi

      # L11: RLIMIT_FSIZE. The hook reads /proc/self/limits (bytes) because
      # `ulimit -f` counts blocks and dash counts 512-byte ones.
      LIMITS_BYTES=$(docker exec "$CONTAINER" bash -c "awk '/^Max file size/ {print \$4}' /proc/self/limits")
      if [ "$LIMITS_BYTES" = "$BUDGET_BYTES" ]; then
        pass L11 "/proc/self/limits reports Max file size = $LIMITS_BYTES bytes, the budget --ulimit set"
      else
        fail L11 "/proc/self/limits reports ${LIMITS_BYTES:-?} bytes, want $BUDGET_BYTES"
      fi

      if docker exec "$CONTAINER" su -s /bin/sh gem -c 'ulimit -f unlimited' >/dev/null 2>&1; then
        fail L11 "gem raised RLIMIT_FSIZE: the per-file cap is a suggestion for the tenant"
      else
        pass L11 "gem cannot raise RLIMIT_FSIZE (soft = hard): 'ulimit -f unlimited' is refused"
      fi

      DD_OUT=$(docker exec "$CONTAINER" su -s /bin/sh gem -c \
        "dd if=/dev/zero of=/workspace/verify-big.bin bs=1M count=$((BUDGET_BYTES / 1048576 + 256)) 2>&1; stat -c %s /workspace/verify-big.bin" 2>&1)
      DD_SIZE=$(printf '%s\n' "$DD_OUT" | tail -1)
      if [ "$DD_SIZE" = "$BUDGET_BYTES" ] && printf '%s' "$DD_OUT" | grep -qi 'file size limit exceeded'; then
        pass L11 "a write past the budget is killed at exactly $DD_SIZE bytes (SIGXFSZ), not truncated silently"
      else
        fail L11 "the over-budget write ended at ${DD_SIZE:-?} bytes (want $BUDGET_BYTES and SIGXFSZ)"
      fi
      docker exec "$CONTAINER" rm -f /workspace/verify-big.bin >/dev/null 2>&1

      # L4: du's documented exit 1 with a real total, which is what the sandbox
      # uid always gets on `du -sbx /`.
      DU_OUT=$(docker exec "$CONTAINER" su -s /bin/sh gem -c 'du -sbx / 2>/dev/null; echo "rc=$?"')
      DU_RC=$(printf '%s\n' "$DU_OUT" | sed -n 's/^rc=//p')
      DU_BYTES=$(printf '%s\n' "$DU_OUT" | awk 'NR==1 {print $1}')
      if [ "${DU_RC:-0}" = "1" ] && [ -n "${DU_BYTES:-}" ]; then
        pass L4 "du -sbx / as gem exits 1 with a total ($DU_BYTES bytes): the partial walk the classifier must accept"
      elif [ "${DU_RC:-0}" = "0" ]; then
        skip L4 "du -sbx / as gem exited 0 on this image (no root-only directories); the partial path is untested here"
      else
        fail L4 "du -sbx / as gem exited ${DU_RC:-?} without a usable total"
      fi

      # L5/L6: the baseline is the same uid's walk, and the arithmetic is not
      # negative — a negative delta is clamped to 0 and silently switches the
      # whole-container term off.
      BASELINE=$(docker exec "$CONTAINER" cat /run/piforge/disk-baseline 2>/dev/null)
      BASELINE_MODE=$(docker exec "$CONTAINER" stat -c '%U:%G %a' /run/piforge/disk-baseline 2>/dev/null)
      case "$BASELINE_MODE" in
        "root:root 644") pass L5 "the boot baseline is root-owned 0644 at /run/piforge/disk-baseline (the tenant cannot rewrite it)" ;;
        *) fail L5 "the boot baseline is '$BASELINE_MODE', want root:root 644" ;;
      esac
      case "${BASELINE:-}" in
        ''|*[!0-9]*) fail L5 "the boot baseline is '${BASELINE:-missing}', want a byte count" ;;
        *)
          if [ "${DU_BYTES:-0}" -ge "$BASELINE" ] 2>/dev/null; then
            pass L5 "the uid-1000 walk ($DU_BYTES) is at least the uid-1000 baseline ($BASELINE): the second term is not clamped to 0"
          else
            fail L5 "the uid-1000 walk ($DU_BYTES) is below the baseline ($BASELINE): the whole-container term reads 0"
          fi
          ;;
      esac

      # L6: write-once. A pause/resume re-runs the hook on the same layer; a
      # re-measure would fold the tenant's earlier writes into the baseline.
      # `docker stop` + `docker start` is the product's pause/resume path, and it
      # is the one that re-runs the image hook (`docker restart` did not, on the
      # daemon this was written against).
      docker exec "$CONTAINER" bash -c 'head -c 20971520 /dev/zero > /tmp/verify-burst.bin' >/dev/null 2>&1
      docker stop -t 5 "$CONTAINER" >/dev/null 2>&1
      docker start "$CONTAINER" >/dev/null 2>&1
      for _ in $(seq 1 90); do
        if docker exec "$CONTAINER" test -f /run/piforge/disk-baseline 2>/dev/null &&
           docker exec "$CONTAINER" pgrep -f '[q]uota-enforcer' >/dev/null 2>&1; then break; fi
        sleep 1
      done
      BASELINE_AFTER=$(docker exec "$CONTAINER" cat /run/piforge/disk-baseline 2>/dev/null)
      KEPT=$(docker logs "$CONTAINER" 2>&1 | grep -c 'keeping the boot baseline already recorded')
      if [ "${BASELINE_AFTER:-x}" = "${BASELINE:-y}" ] && [ "${KEPT:-0}" -ge 1 ]; then
        pass L6 "after a stop/start with 20 MB already in /tmp the baseline is unchanged ($BASELINE_AFTER) and the hook logged that it kept it"
      elif [ "${BASELINE_AFTER:-x}" = "${BASELINE:-y}" ]; then
        skip L6 "the baseline is unchanged ($BASELINE_AFTER), but the hook's 'keeping' line was not found: the image may predate the fix"
      else
        fail L6 "the baseline moved from ${BASELINE:-?} to ${BASELINE_AFTER:-?} across a stop/start; the burst stopped counting"
      fi
      docker exec "$CONTAINER" rm -f /tmp/verify-burst.bin >/dev/null 2>&1

      # L12: git's safe.directory decision — a root-owned checkout is refused to
      # uid 1000 with "dubious ownership", and the product's `-c safe.directory`
      # is what makes it usable. A gem-owned checkout needs none.
      docker exec "$CONTAINER" bash -c '
        rm -rf /workspace/verify-rootrepo /workspace/verify-gemrepo
        mkdir -p /workspace/verify-rootrepo
        cd /workspace/verify-rootrepo
        git init -q -b main .
        git -c user.name=V -c user.email=v@localhost commit -q --allow-empty -m one
      ' >/dev/null 2>&1
      ROOT_GIT_RC=$(docker exec "$CONTAINER" su -s /bin/sh gem -c 'cd /workspace/verify-rootrepo && git status --porcelain >/dev/null 2>&1; echo $?')
      SAFE_RC=$(docker exec "$CONTAINER" su -s /bin/sh gem -c 'cd /workspace/verify-rootrepo && git -c safe.directory=/workspace/verify-rootrepo status --porcelain >/dev/null 2>&1; echo $?')
      if [ "${ROOT_GIT_RC:-0}" != "0" ] && [ "${SAFE_RC:-1}" = "0" ]; then
        pass L12 "a root-owned checkout is refused to gem (rc=$ROOT_GIT_RC) and accepted with -c safe.directory (rc=$SAFE_RC)"
      else
        fail L12 "safe.directory decision changed (plain rc=${ROOT_GIT_RC:-?}, with safe.directory rc=${SAFE_RC:-?})"
      fi

      # L13: the copy-ro residual, measured rather than assumed: 0444 refuses a
      # write, and the *owner* can chmod it back. The docs say so; this line is
      # the evidence, not a pass for the guarantee.
      COPY_PROBE=$(docker exec "$CONTAINER" bash -c '
        rm -rf /workspace/verify-copy; mkdir -p /workspace/verify-copy
        printf "body\n" > /workspace/verify-copy/SKILL.md
        chown -R gem:gem /workspace/verify-copy
        chmod 0444 /workspace/verify-copy/SKILL.md; chmod 0555 /workspace/verify-copy
        if su -s /bin/sh gem -c "printf hacked > /workspace/verify-copy/SKILL.md" 2>/dev/null; then echo write-ok; else echo write-refused; fi
        if su -s /bin/sh gem -c "chmod u+w /workspace/verify-copy/SKILL.md && printf hacked > /workspace/verify-copy/SKILL.md" 2>/dev/null; then echo chmod-ok; else echo chmod-refused; fi
      ' 2>/dev/null)
      if printf '%s' "$COPY_PROBE" | grep -q write-refused; then
        pass L13 "0444 refuses gem's write (copy-ro holds against accident)"
      else
        fail L13 "gem wrote a 0444 file: the read-only copy is not read-only"
      fi
      if printf '%s' "$COPY_PROBE" | grep -q chmod-ok; then
        info "L13 residual confirmed: the owner can 'chmod u+w' first — EXECUTION-BOUNDARY §14.1 states this; the workspace needs a root-owned writer to close it"
      fi
      docker exec "$CONTAINER" bash -c 'chmod -R u+w /workspace 2>/dev/null; rm -rf /workspace/verify-copy /workspace/verify-rootrepo /workspace/verify-gemrepo' >/dev/null 2>&1

      # L14: bind-ro, the mechanism that needs CAP_SYS_ADMIN. The probe container
      # has none, so this is SKIP with the exact command; `isolate` is the other
      # half of the same question.
      if docker exec "$CONTAINER" bash -c 'mkdir -p /tmp/bindsrc /tmp/binddst && mount --bind /tmp/bindsrc /tmp/binddst' >/dev/null 2>&1; then
        pass L14 "this sandbox container can bind mount (CAP_SYS_ADMIN present, which the shipped one lacks)"
      else
        skip L14 "bind mounts need CAP_SYS_ADMIN, which the sandbox container does not have (by design). Prove bind-ro with:
        docker run --rm --cap-add SYS_ADMIN --security-opt apparmor=unconfined -v /tmp:/host bash -c 'mkdir -p /tmp/s /tmp/d; mount --bind /tmp/s /tmp/d && mount -o remount,bind,ro /tmp/d && touch /tmp/d/x'
      Expected: mount succeeds, 'touch' fails with EROFS. Measured on this machine 2026-11-01."
      fi

      # L15: the boot log says which skill mechanism this deployment will
      # report, and it must agree with the capability it observes.
      BOOT_MECH=$(docker logs "$CONTAINER" 2>&1 | grep -o "mechanism=[a-z-]*" | head -1)
      if [ -n "${BOOT_MECH:-}" ]; then
        info "sandbox boot log: $BOOT_MECH (per-container; the control plane logs its own decision)"
      fi

      # L3b: the enforcer is running as gem under a root supervisor — the two
      # processes the quota proof depends on.
      ENFORCER_PID=$(docker exec "$CONTAINER" pgrep -u "$GEM_UID" -f '[q]uota-enforcer' 2>/dev/null | head -1)
      SUPERVISOR=$(docker exec "$CONTAINER" pgrep -u 0 -f 'quota-enforcer-supervisor|while :' 2>/dev/null | head -1)
      if [ -n "${ENFORCER_PID:-}" ]; then
        pass L3 "the quota enforcer is running as uid $GEM_UID (pid $ENFORCER_PID), so it may signal the tenant's processes"
      else
        skip L3 "the enforcer is not running in this probe container; see docker logs $CONTAINER (it needs RLIMIT_FSIZE, which this run sets)"
      fi
      [ -n "${SUPERVISOR:-}" ] && pass L3 "a root-owned supervisor loop exists (pid $SUPERVISOR): kill -9 of the enforcer is a ~0.2 s gap, not an escape"
    fi
  fi
fi

# ---------------------------------------------------------------------------
# Native-host-only checks. These are the (b) rows: run them on the host, not in
# a container, or the answer is about Docker Desktop's VM rather than Linux.
# ---------------------------------------------------------------------------
section "native host (rows only a real Linux host can prove)"

if inside_container; then
  skip N1 "run on the host shell, not in a container: file ownership of a bind-mounted path
        d=\$(mktemp -d); printf x > \$d/as-root; stat -c %u:%g \$d/as-root; su -s /bin/sh nobody -c \"printf x > \$d/as-nobody\"; stat -c %u:%g \$d/as-nobody
      Expected on a native host: 0:0 and 65534:65534. Docker Desktop's VM synthesises one owner for every file, which is why the ownership half of the sandbox proofs was never evidence."
  skip N2 "on the host: docker info --format '{{.Driver}}'  (expect overlay2) and
        findmnt -no FSTYPE,SOURCE /var/lib/docker
      Expected: overlay2 on a real filesystem; the upperdir is what 'du -sbx /' walks."
  skip N3 "on the host: for b in bwrap firejail; do command -v \$b || echo \"\$b missing\"; done
      Expected: at least one present for -isolation bubblewrap/firejail; the shipped image has neither, so PIFORGE_ISOLATION=none is the only working value there."
  skip N4 "on the host: cat /sys/fs/cgroup/cgroup.controllers (expect 'cpuset cpu io memory hugetlb pids rdma misc') and, as root,
        systemd-run --scope -p PidsMax=64 --scope sh -c 'cat /sys/fs/cgroup/pids.max'
      Expected: 64. That proves --pids-limit is the kernel's own number, not a Docker-only one."
  skip N5 "on the host: go test ./backend/internal/liveness ./backend/internal/agentsupervisor -count=1
      Expected: PASS. Those tests exercise Pdeathsig and Setsid; they are Pdeathsig's only direct evidence and they need a Linux PID namespace, not a Windows host."
else
  if command -v go >/dev/null 2>&1; then
    section "go tests (Linux-only paths)"
    if (cd "$ROOT_DIR/backend" && go test ./internal/liveness ./internal/agentsupervisor ./internal/tenantfs ./internal/materialize ./internal/skillsnap ./internal/quotaenforcer -count=1 >/tmp/piforge-verify-go.log 2>&1); then
      pass N5 "go test on the Linux-only packages passed (Pdeathsig, Setsid, EvalSymlinks, 0444/0555, the du classifier)"
    else
      fail N5 "go test failed: $(tail -3 /tmp/piforge-verify-go.log | tr '\n' ' ')"
    fi
    rm -f /tmp/piforge-verify-go.log
  else
    skip N5 "no Go toolchain: run 'go test ./backend/internal/liveness ./backend/internal/agentsupervisor -count=1' somewhere with one"
  fi

  if command -v stat >/dev/null 2>&1 && command -v su >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    HOLE=$(mktemp -d)
    chmod 777 "$HOLE"
    printf x >"$HOLE/as-root"
    su -s /bin/sh nobody -c "printf x > '$HOLE/as-nobody'" >/dev/null 2>&1
    ROOT_OWN=$(stat -c '%u:%g' "$HOLE/as-root" 2>/dev/null)
    NOBODY_OWN=$(stat -c '%u:%g' "$HOLE/as-nobody" 2>/dev/null)
    rm -rf "$HOLE"
    if [ "$ROOT_OWN" = "0:0" ] && [ "$NOBODY_OWN" != "0:0" ]; then
      pass N1 "file ownership is real on this filesystem (root wrote $ROOT_OWN, another uid wrote $NOBODY_OWN)"
    else
      fail N1 "ownership is synthesised (root $ROOT_OWN, other uid ${NOBODY_OWN:-missing}): uid-based containment cannot be measured here"
    fi
  else
    skip N1 "needs root and su to compare two owners: rerun as root, or stat a file another uid wrote and compare %u:%g"
  fi

  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    DRIVER=$(docker info --format '{{.Driver}}' 2>/dev/null)
    if [ "$DRIVER" = "overlay2" ]; then
      pass N2 "docker storage driver is overlay2, the filesystem 'du -sbx /' walks in a sandbox"
    else
      fail N2 "docker storage driver is '$DRIVER', want overlay2: the du/overlay arithmetic does not describe this host"
    fi
  else
    skip N2 "no docker daemon to ask for its storage driver"
  fi

  for bin in bwrap firejail; do
    if command -v "$bin" >/dev/null 2>&1; then
      pass N3 "$bin is installed, so -isolation $bin has something to exec"
      break
    fi
  done
  if ! command -v bwrap >/dev/null 2>&1 && ! command -v firejail >/dev/null 2>&1; then
    skip N3 "neither bwrap nor firejail is installed: -isolation bubblewrap/firejail would fail at startup (the shipped image uses none)"
  fi

  if [ -r /sys/fs/cgroup/pids.max ]; then
    pass N4 "the host's cgroup v2 root exposes pids.max ($(cat /sys/fs/cgroup/pids.max))"
  else
    skip N4 "the host's cgroup v2 pids.max is not readable from this shell"
  fi
fi

# ---------------------------------------------------------------------------
# The one check that needs a model: an agent turn inside the sandbox.
# ---------------------------------------------------------------------------
section "agent turn (needs a provider key)"

KEY=${PIFORGE_VERIFY_KEY:-}
if [ -z "$KEY" ] && [ -n "${PIFORGE_VERIFY_KEY_FILE:-}" ] && [ -r "${PIFORGE_VERIFY_KEY_FILE}" ]; then
  # Never echoed, never written to a log: read once into the variable.
  KEY=$(awk -F': *' '/^[[:space:]]*key:/ {print $2; exit}' "${PIFORGE_VERIFY_KEY_FILE}" 2>/dev/null | tr -d '"'"'"'')
fi

if [ -z "$KEY" ]; then
  skip L16 "no provider key: set PIFORGE_VERIFY_KEY (or PIFORGE_VERIFY_KEY_FILE; the value is never echoed) to run a real agent turn against $PIFORGE_URL"
else
  if ! curl -fsS --max-time 5 "$PIFORGE_URL/healthz" >/dev/null 2>&1; then
    skip L16 "no control plane at $PIFORGE_URL; start one and set PIFORGE_URL"
  else
    JAR=$(mktemp)
    LOGIN=$(curl -fsS -c "$JAR" -H 'Content-Type: application/json' \
      -d '{"email":"admin@piforge.local","password":"piforge-admin-123"}' \
      "$PIFORGE_URL/v1/auth/login" 2>/dev/null)
    CSRF=$(printf '%s' "${LOGIN:-}" | sed -n 's/.*"csrf":"\([^"]*\)".*/\1/p')
    if [ -z "${CSRF:-}" ]; then
      fail L16 "login to $PIFORGE_URL did not return a CSRF token"
    else
      PROJ=$(curl -fsS -b "$JAR" -H "X-PF-CSRF: $CSRF" -H 'Content-Type: application/json' \
        -d '{"name":"verify-linux","default_branch":"main"}' "$PIFORGE_URL/v1/projects" 2>/dev/null |
        sed -n 's/.*"project":{[^}]*"id":"\([^"]*\)".*/\1/p')
      SESS=$(curl -fsS -b "$JAR" -H "X-PF-CSRF: $CSRF" -H 'Content-Type: application/json' \
        -d '{"kind":"main","title":"verify linux"}' "$PIFORGE_URL/v1/projects/$PROJ/sessions" 2>/dev/null |
        sed -n 's/.*"session":{[^}]*"id":"\([^"]*\)".*/\1/p')
      if [ -z "${PROJ:-}" ] || [ -z "${SESS:-}" ]; then
        fail L16 "project/session creation failed against $PIFORGE_URL"
      else
        TURN_TEXT="Write the single line 'linux-verify' to the file verify-linux.txt in the repository root, then stop."
        curl -fsS -b "$JAR" -H "X-PF-CSRF: $CSRF" -H 'Content-Type: application/json' \
          -d "{\"text\":$(printf '%s' "$TURN_TEXT" | sed 's/"/\\"/g; s/^/"/; s/$/"/')}" \
          "$PIFORGE_URL/v1/sessions/$SESS/messages" >/dev/null 2>&1
        TURN_OK=0
        for _ in $(seq 1 120); do
          BODY=$(curl -fsS -b "$JAR" "$PIFORGE_URL/v1/sessions/$SESS/files?path=verify-linux.txt" 2>/dev/null || true)
          if printf '%s' "${BODY:-}" | grep -q 'linux-verify'; then TURN_OK=1; break; fi
          sleep 2
        done
        if [ "$TURN_OK" -eq 1 ]; then
          pass L16 "a real agent turn wrote verify-linux.txt inside the sandbox, and it is readable back through the API"
        else
          fail L16 "the agent turn did not produce verify-linux.txt in 240 s (provider key present); check /v1/sessions/$SESS/logs"
        fi
      fi
    fi
    rm -f "$JAR"
  fi
fi

# ---------------------------------------------------------------------------
section "summary"
printf '%d passed, %d failed, %d skipped\n' "$PASSED" "$FAILED" "$SKIPPED"
if [ "$FAILED" -gt 0 ]; then
  printf '\nfailures:\n'
  for f in "${FAILS[@]}"; do printf '  - %s\n' "$f"; done
  exit 1
fi
printf 'Every check this machine can prove passed. SKIPs are listed above with the command that would prove them.\n'
exit 0
