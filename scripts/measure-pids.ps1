# Measure where a sandbox's pids budget breaks `docker exec`, and where the
# runtime in these images stops being able to start.
#
#   pwsh scripts/measure-pids.ps1                     # the full table
#   pwsh scripts/measure-pids.ps1 -Limits 512,256,64  # a subset
#
# Bounded on purpose: every fork bomb runs under `timeout` inside the container,
# every container is created with --cpus 1 --memory 2g, and nothing here touches
# an existing stack (the containers are named pf-pids-<limit> and removed after).
#
# Two questions the table answers:
#
#   1. At which --pids-limit can the image's own runtime (gem.sh -> supervisord ->
#      Jupyter / Chrome / node) still boot? That is the floor: a limit below it
#      would break the product for everybody.
#   2. Is "docker exec fails" a property of the *limit* or of the *saturation*?
#      If a bounded fork storm wedges exec at every limit, no default value can
#      prevent it and the product needs detection + recovery (which is what
#      internal/httpapi's sandbox watchdog adds).
param(
    [int[]]$Limits = @(1024, 512, 256, 192, 128, 96, 64),
    [string]$Image = "piforge/sandbox-agent:isolated",
    [int]$BombSeconds = 8
)

$ErrorActionPreference = "Stop"
$out = @()
function Line($m) { Write-Host $m; $script:out += $m }

function PidsCurrent($name) {
    # Inside the container: the cgroup file is readable by the sandbox user, and
    # reading it does not need a new process when it is done by an existing one.
    $raw = & docker exec $name sh -c 'cat /sys/fs/cgroup/pids.current 2>/dev/null || cat /sys/fs/cgroup/pids/pids.current 2>/dev/null' 2>$null
    if ($LASTEXITCODE -ne 0) { return "" }
    return (($raw | Select-Object -First 1) -as [string]).Trim()
}

Line "# pids measurement $(Get-Date -Format o) image=$Image bomb=${BombSeconds}s"

# --- 1. floor: at which limit does the image's runtime still come up?
Line ""
Line "## boot floor (no storm)"
foreach ($limit in @($Limits | Sort-Object)) {
    $name = "pf-pids-$limit"
    & docker rm -f $name *> $null
    & docker run -d --name $name --pids-limit $limit --cpus 1 --memory 2g $Image *> $null
    if ($LASTEXITCODE -ne 0) { Line "limit=$limit boots=no (docker run failed)"; & docker rm -f $name *> $null; continue }
    $booted = "no"
    $baseline = ""
    for ($i = 0; $i -lt 25; $i++) {
        Start-Sleep -Seconds 1
        $p = PidsCurrent $name
        if ($p -ne "") { $baseline = $p }
        $probe = & docker exec $name sh -c 'echo ok' 2>&1
        if ($LASTEXITCODE -eq 0 -and "$probe".Trim() -eq "ok") { $booted = "yes"; break }
    }
    $procs = (& docker exec $name sh -c 'ps -eo pid= --no-headers 2>/dev/null | wc -l' 2>&1)
    Line ("limit={0} boots={1} baseline_pids={2} baseline_procs={3}" -f $limit, $booted, $baseline, "$procs".Trim())
    & docker rm -f $name *> $null
}

# --- 2. does a bounded fork storm at limit N leave exec working?
Line ""
Line "## storm: bounded fork bomb (${BombSeconds}s), then one trivial exec"
foreach ($limit in @($Limits | Sort-Object)) {
    $name = "pf-pids-$limit"
    & docker rm -f $name *> $null
    & docker run -d --name $name --pids-limit $limit --cpus 1 --memory 2g $Image *> $null
    if ($LASTEXITCODE -ne 0) { Line "storm limit=$limit skipped (docker run failed)"; continue }
    Start-Sleep -Seconds 12
    $base = PidsCurrent $name

    # The classic bomb, in bash (the image's `sh` is dash, which cannot define
    # that function). `timeout` bounds the parent; the children it orphaned keep
    # forking for the rest of the test, which is the wedge being measured.
    & docker exec -d $name bash -c "timeout $BombSeconds bash -c ':(){ :|:& };:'" *> $null
    Start-Sleep -Seconds ($BombSeconds + 1)

    $peak = PidsCurrent $name
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $probe = & docker exec $name sh -c 'echo alive' 2>&1
    $code = $LASTEXITCODE
    $sw.Stop()
    $state = (& docker inspect -f '{{.State.Status}}' $name 2>&1).Trim()
    $says = ("$probe".Trim() -replace "`r?`n", " | ")
    Line ("storm limit={0} baseline={1} peak={2} exec_exit={3} exec_ms={4} exec_says={5} container={6}" -f `
            $limit, $base, $peak, $code, $sw.ElapsedMilliseconds, $says, $state)

    # Does the product's recovery (Pause = docker stop, Resume = docker start)
    # bring it back, and how long does that take?
    if ($code -ne 0) {
        $sw2 = [System.Diagnostics.Stopwatch]::StartNew()
        & docker stop -t 5 $name *> $null
        & docker start $name *> $null
        $rec = "no"
        for ($i = 0; $i -lt 60; $i++) {
            Start-Sleep -Milliseconds 500
            $r = & docker exec $name sh -c 'echo alive' 2>&1
            if ($LASTEXITCODE -eq 0 -and "$r".Trim() -eq "alive") { $rec = "yes"; break }
        }
        $sw2.Stop()
        $after = PidsCurrent $name
        Line ("recover limit={0} recovered={1} in_ms={2} pids_after={3}" -f $limit, $rec, $sw2.ElapsedMilliseconds, $after)
    }
    & docker rm -f $name *> $null
}

$dir = Join-Path (Split-Path -Parent $PSScriptRoot) "e2e/.artifacts/pids"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$path = Join-Path $dir ("measure-{0}.txt" -f (Get-Date -Format "yyyyMMdd-HHmmss"))
$out | Set-Content -Path $path -Encoding utf8
Write-Host ""
Write-Host "written: $path"
