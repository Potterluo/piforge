<#
.SYNOPSIS
    Measure how far a sandbox's disk can overshoot its quota in a burst.

.DESCRIPTION
    The quota (FR-5.8) has two enforcement points: a per-file RLIMIT_FSIZE equal
    to the budget, and a periodic `du` watchdog in the control plane. Neither is
    a bound on the *total*: a tenant writing many files, or one capped file plus
    many small ones, can be far over the budget before anything acts on it.

    This probe measures that overshoot instead of arguing about it. It creates a
    tenant-owned project and session on a stack the caller already started,
    starts a bursting writer inside the sandbox as `gem` (the same uid the PTY
    and the agent get), samples the workspace size from the host every 200 ms,
    and reports the largest size it saw as a multiple of the quota.

    It is the same script for "before" and "after" a fix, so the two numbers are
    comparable: same burst, same sampling, same host.

    What it prints at the end (one line, greppable):

        RESULT mode=files quota_bytes=2147483648 max_bytes=3951206581
               ratio=1.84 paused=true enforcer=absent elapsed_ms=21000

    The artifacts (a transcript of every sample, the control plane's own log
    lines about the quota, and a JSON summary) go to
    e2e/.artifacts/quota-overshoot/<stamp>/.

.PARAMETER BaseUrl
    The stack to probe. Default http://127.0.0.1:7723 (the isolated stack). It
    refuses 7722 so the standing pf-local deployment is never touched.

.PARAMETER Mode
    files    — 8 MiB files in a loop (the "many small files" case).
    onefile  — one `dd` of 6 GiB (RLIMIT_FSIZE stops it) and then the loop.
    mixed    — both at once (one big file and a flood of small ones).

.PARAMETER QuotaBytes
    The per-sandbox budget the stack was started with. It is only used for the
    ratio and the safety cap; the enforcement under test is the stack's own.

.PARAMETER CapBytes
    The burst stops when the workspace reaches this size, whatever the quota is.
    Default 6 GiB: enough to observe a ~2x overshoot of a 2 GiB budget, not
    enough to fill the host if nothing stops the writer.

.PARAMETER TimeoutSeconds
    Give up on the burst after this long. Default 120.

.PARAMETER OutDir
    Where to write the artifacts. Default e2e/.artifacts/quota-overshoot/<stamp>.
#>
param(
    [string]$BaseUrl = "http://127.0.0.1:7723",
    [ValidateSet("files", "onefile", "mixed")][string]$Mode = "files",
    [long]$QuotaBytes = 2147483648,
    [long]$CapBytes = 6442450944,
    [int]$TimeoutSeconds = 120,
    [string]$AdminEmail = "admin@piforge.local",
    [string]$AdminPassword = "piforge-admin-123",
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"

if ($BaseUrl -match ":7722\b") {
    throw "refusing to probe $BaseUrl : that port is the standing pf-local stack and must not be touched"
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $root "e2e/.artifacts/quota-overshoot/$stamp-$Mode"
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$logPath = Join-Path $OutDir "probe.txt"
$script:Lines = @()

function Say($message) {
    Write-Host $message
    $script:Lines += $message
}

function Invoke-Api {
    param(
        [ValidateSet("GET", "POST", "PATCH", "PUT", "DELETE")][string]$Method,
        [string]$Path,
        $Body = $null,
        $Session,
        [string]$CSRF = ""
    )
    $params = @{
        Method             = $Method
        Uri                = "$BaseUrl$Path"
        WebSession         = $Session
        UseBasicParsing    = $true
        SkipHttpErrorCheck = $true
    }
    if ($Method -ne "GET") { $params.Headers = @{ "X-PF-CSRF" = $CSRF } }
    if ($null -ne $Body) {
        $params.ContentType = "application/json"
        $params.Body = ($Body | ConvertTo-Json -Depth 12 -Compress)
    }
    $resp = Invoke-WebRequest @params
    $parsed = $null
    if ($resp.Content) { try { $parsed = $resp.Content | ConvertFrom-Json } catch { $parsed = $resp.Content } }
    if ([int]$resp.StatusCode -ge 400) {
        throw ("{0} {1} -> {2}: {3}" -f $Method, $Path, [int]$resp.StatusCode, $resp.Content)
    }
    return $parsed
}

# Workspace size as the platform itself measures it: apparent size, one walk,
# bytes only. `-x` keeps it on the workspace's own volume.
function Measure-Workspace([string]$container) {
    $out = & docker exec $container du -sbx /workspace 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $out) { return $null }
    $first = ("$out".Trim() -split "\s+")[0]
    $value = 0L
    if ([long]::TryParse($first, [ref]$value)) { return $value }
    return $null
}

# When the watchdog has stopped the sandbox, `docker exec` cannot answer. The
# volume can still be measured by mounting it read-only in a throwaway container,
# which is how the final number stays a measurement rather than a last sample.
function Measure-WorkspaceVolume([string]$container, [string]$image) {
    $vol = & docker inspect --format '{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Name}}{{end}}{{end}}' $container 2>$null
    if (-not $vol) { return $null }
    $out = & docker run --rm -v "${vol}:/w:ro" --entrypoint du $image -sbx /w 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $out) { return $null }
    $first = ("$out".Trim() -split "\s+")[0]
    $value = 0L
    if ([long]::TryParse($first, [ref]$value)) { return $value }
    return $null
}

function Container-Running([string]$container) {
    $state = & docker inspect --format '{{.State.Running}}' $container 2>$null
    return ("$state".Trim() -eq "true")
}

Say "# quota overshoot probe $(Get-Date -Format o)  base=$BaseUrl mode=$Mode quota=$QuotaBytes"

$health = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 5
Say "healthz: $($health.status)"
if ($health.status -ne "ok") { throw "the stack is not healthy" }

$login = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
    -Body (@{ email = $AdminEmail; password = $AdminPassword } | ConvertTo-Json -Compress) -SessionVariable session
$admin = [pscustomobject]@{ User = $login.user; CSRF = $login.csrf; Session = $session }
Say "login: $($admin.User.email) role=$($admin.User.role)"

$projectName = "quota-$Mode-$stamp"
$project = Invoke-Api -Method POST -Path "/v1/projects" -Session $admin.Session -CSRF $admin.CSRF -Body @{ name = $projectName }
$projectID = $project.project.id
$session_ = Invoke-Api -Method POST -Path "/v1/projects/$projectID/sessions" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ title = "quota $Mode" }
$sessionID = $session_.session.id
Say "project=$projectID session=$sessionID"

# Provisioning happens on the first sandbox read; wait for READY rather than
# guessing a container name.
$sandbox = $null
$deadline = (Get-Date).AddSeconds(180)
while ((Get-Date) -lt $deadline) {
    try { $sandbox = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/sandbox" -Session $admin.Session } catch { $sandbox = $null }
    if ($sandbox -and $sandbox.state -eq "READY" -and $sandbox.id) { break }
    Start-Sleep -Seconds 2
}
if (-not $sandbox -or $sandbox.state -ne "READY") { throw "the sandbox did not become READY" }
$sandboxID = $sandbox.id
$container = $sandboxID
Say "sandbox=$sandboxID state=$($sandbox.state) container=$container"
$running = & docker inspect --format '{{.State.Running}}' $container 2>$null
if ("$running".Trim() -ne "true") { throw "container $container is not running" }

# What the container is actually enforcing, read from its own HostConfig and
# from inside it: the numbers below have to be the ones the burst meets.
$limits = & docker inspect --format '{{json .HostConfig.Ulimits}}' $container 2>$null
Say "host_config_ulimits=$("$limits".Trim())"
$ulimit = & docker exec -u gem $container sh -c 'ulimit -f; ulimit -Hf' 2>$null
Say ("in_sandbox ulimit -f: " + (("$ulimit".Trim() -split "\s+") -join " "))

# Is the in-sandbox enforcer running? On the old build this prints nothing.
$enforcer = & docker exec $container sh -c 'pgrep -u 1000 -f "quota-enforcer" | head -3' 2>$null
$enforcerPresent = -not [string]::IsNullOrWhiteSpace("$enforcer")
Say ("enforcer_pids=" + [string]::Join(",", @($enforcer)))

$baseline = Measure-Workspace $container
Say "baseline_bytes=$baseline"

# The burst, as gem, detached inside the sandbox. It never exits on its own: what
# ends it is the platform (a pause) or the enforcer (a stop), which is the thing
# being measured.
switch ($Mode) {
    "files" {
        $burst = 'mkdir -p /workspace/flood; i=0; while :; do dd if=/dev/zero of=/workspace/flood/f$i bs=8M count=1 status=none || exit 1; i=$((i+1)); done'
    }
    "onefile" {
        $burst = 'mkdir -p /workspace/flood; dd if=/dev/zero of=/workspace/flood/big.bin bs=1M count=6000 status=none || true; i=0; while :; do dd if=/dev/zero of=/workspace/flood/f$i bs=8M count=1 status=none || exit 1; i=$((i+1)); done'
    }
    "mixed" {
        $burst = 'mkdir -p /workspace/flood; (dd if=/dev/zero of=/workspace/flood/big.bin bs=1M count=6000 status=none || true) & i=0; while :; do dd if=/dev/zero of=/workspace/flood/f$i bs=8M count=1 status=none || break; i=$((i+1)); done; wait'
    }
}
$startedAt = Get-Date
& docker exec -d -u gem $container sh -c $burst 2>$null
Say "burst started at $($startedAt.ToString('o'))"

$samples = New-Object System.Collections.Generic.List[object]
$maxBytes = 0L
$paused = $false
$pausedAt = $null
$stalledSince = $null
$last = 0L
$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
while ((Get-Date) -lt $deadline) {
    if (-not (Container-Running $container)) {
        $paused = $true
        $pausedAt = Get-Date
        Say ("the container stopped at {0} ms" -f [int]((Get-Date) - $startedAt).TotalMilliseconds)
        break
    }
    $bytes = Measure-Workspace $container
    $now = Get-Date
    if ($null -eq $bytes) {
        # A `du` that cannot answer while the container still runs is the
        # pids-storm failure mode, not a quota verdict.
        Say ("measurement failed at {0} ms" -f [int]($now - $startedAt).TotalMilliseconds)
    }
    else {
        if ($bytes -gt $maxBytes) { $maxBytes = $bytes }
        $samples.Add([pscustomobject]@{ t_ms = [int]($now - $startedAt).TotalMilliseconds; bytes = $bytes })
        if ($bytes -gt $last) { $last = $bytes; $stalledSince = $null }
        elseif ($null -eq $stalledSince) { $stalledSince = $now }
        if ($bytes -ge $CapBytes) {
            Say "safety cap reached: $bytes >= $CapBytes"
            break
        }
    }
    # The enforcer's effect is a freeze: the size stops moving while the sandbox
    # is still running. Four seconds of no growth is that, and continuing to
    # sample would only add noise.
    if ($null -ne $stalledSince -and ($now - $stalledSince).TotalSeconds -ge 4 -and $maxBytes -gt $QuotaBytes) {
        Say ("no growth for {0:N1}s at {1} bytes; assuming the writers were stopped" -f ($now - $stalledSince).TotalSeconds, $last)
        break
    }
    Start-Sleep -Milliseconds 200
}
$elapsedMs = [int]((Get-Date) - $startedAt).TotalMilliseconds

$sandboxAfter = $null
try { $sandboxAfter = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/sandbox" -Session $admin.Session } catch { }
$stateAfter = if ($sandboxAfter) { $sandboxAfter.state } else { "unknown" }
Say "sandbox state after the burst: $stateAfter (container_running=$(Container-Running $container))"

$finalBytes = Measure-Workspace $container
if ($null -eq $finalBytes) {
    $finalBytes = Measure-WorkspaceVolume $container "docker.io/library/alpine:3.20"
    if ($null -eq $finalBytes) {
        # The volume can also be measured with the sandbox image itself.
        $finalBytes = Measure-WorkspaceVolume $container $env:PIFORGE_SANDBOX_IMAGE
    }
}
if ($null -ne $finalBytes -and $finalBytes -gt $maxBytes) { $maxBytes = $finalBytes }
Say "final_bytes=$finalBytes max_bytes=$maxBytes"

# What the writer's processes look like now: on the after-build the frozen
# writers read `T` (SIGSTOP) or are gone.
$procs = & docker exec $container sh -c 'ps -eo pid,user,stat,args | grep -E "dd if=/dev/zero|quota-enforcer" | grep -v grep' 2>$null
if ($procs) { $procs | ForEach-Object { Say "proc: $_" } } else { Say "proc: (none: the sandbox is not answering, or nothing is left)" }

# The enforcer's own evidence, when it exists.
$enforcerLog = & docker exec $container sh -c 'cat /workspace/.piforge/log/quota-enforcer.log 2>/dev/null | tail -12' 2>$null
if ($enforcerLog) { $enforcerLog | ForEach-Object { Say "enforcer: $_" } }
$brake = & docker exec $container sh -c 'cat /workspace/.piforge/quota-brake.json 2>/dev/null' 2>$null
if ($brake) { Say "brake: $("$brake".Trim())" }

# The control plane's own account of the same seconds.
$since = [int]((Get-Date) - $startedAt).TotalSeconds + 30
$ctl = & docker logs piforge --since "$($since)s" 2>&1 | Select-String -Pattern "disk|quota|watchdog" | Select-Object -Last 12
if ($ctl) { $ctl | ForEach-Object { Say "control-plane: $($_.Line.Trim())" } }

$ratio = if ($QuotaBytes -gt 0) { [math]::Round($maxBytes / $QuotaBytes, 2) } else { 0 }
$enforcerState = if ($enforcerPresent) { "present" } else { "absent" }
$verdict = "RESULT mode=$Mode quota_bytes=$QuotaBytes max_bytes=$maxBytes ratio=$ratio paused=$($paused.ToString().ToLower()) enforcer=$enforcerState elapsed_ms=$elapsedMs state=$stateAfter"

Say ""
Say "samples=$($samples.Count)"
if ($samples.Count -gt 0) {
    $step = [math]::Max(1, [int]($samples.Count / 12))
    for ($i = 0; $i -lt $samples.Count; $i += $step) {
        Say ("sample {0,6} ms  {1,12} bytes  ({2:N2} GiB)" -f $samples[$i].t_ms, $samples[$i].bytes, ($samples[$i].bytes / 1GB))
    }
    $samples | ConvertTo-Json -Depth 4 | Set-Content -Path (Join-Path $OutDir "samples.json")
}
Say $verdict

[pscustomobject]@{
    mode           = $Mode
    quota_bytes    = $QuotaBytes
    max_bytes      = $maxBytes
    final_bytes    = $finalBytes
    ratio          = $ratio
    paused         = $paused
    enforcer       = $enforcerState
    state          = $stateAfter
    elapsed_ms     = $elapsedMs
    container      = $container
    sandbox_id     = $sandboxID
    project_id     = $projectID
    session_id     = $sessionID
    samples        = $samples.Count
    started_at     = $startedAt.ToString("o")
} | ConvertTo-Json -Depth 4 | Set-Content -Path (Join-Path $OutDir "summary.json")

$script:Lines | Set-Content -Path $logPath
Write-Host "artifacts: $OutDir"
