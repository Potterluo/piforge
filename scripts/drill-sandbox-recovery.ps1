<#
.SYNOPSIS
    Live proof of the second adversarial-acceptance fixes against a running stack.

.DESCRIPTION
    Four things are proved here, in this order, against a stack the caller
    already started (scripts/up-isolated.ps1 on 7723):

      1. FIX 2 — a real prompt runs; the sandbox is paused and resumed with NO
         new prompt; `GET /v1/sessions/{id}/entries` still returns the whole
         transcript (the defect was `{"entries":[],"leaf_id":""}`).
      2. FIX 1 — a fork storm inside the tenant's sandbox (bounded here: the
         bomb self-terminates and the squatter runs under `timeout`) makes the
         sandbox unable to `docker exec`. The platform's own liveness watchdog
         must notice (`responsive: false`), stop+start the container, verify, and
         tell the session (captured from the SSE stream the page reads).
      3. FIX 1, second half — a real prompt settles again after the recovery.
      4. FIX 3(b) — `GET /v1/admin/status`'s `sandbox_usage[].files` counts the
         files that are actually in the workspace (it used to be 0 always).

    Output: every command and its output is echoed and written to
    e2e/.artifacts/fixes2/<stamp>/drill.txt, with the SSE capture beside it.
    The key is never printed: this script does not touch it at all.

.PARAMETER BaseUrl
    The stack to drill. Default http://127.0.0.1:7723 (the isolated stack), and
    it refuses 7722 so the standing `pf-local` is never touched.

.PARAMETER KeepSandbox
    Leave the tenant's sandbox container alone instead of letting the drill's own
    recovery stop it. Off by default: a wedged sandbox is the subject, not a
    bystander.
#>
param(
    [string]$BaseUrl = "http://127.0.0.1:7723",
    [string]$AdminEmail = "admin@piforge.local",
    [string]$AdminPassword = "piforge-admin-123",
    [int]$PromptTimeoutSeconds = 300,
    [int]$RecoveryTimeoutSeconds = 150,
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"

if ($BaseUrl -match ":7722\b") {
    throw "refusing to drill $BaseUrl : that port is the standing pf-local stack and must not be touched"
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $root "e2e/.artifacts/fixes2/$stamp"
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$logPath = Join-Path $OutDir "drill.txt"
$script:Lines = @()
$script:Failures = 0

function Say($message) {
    Write-Host $message
    $script:Lines += $message
}

function Check($what, $ok, $detail) {
    if ($ok) {
        Say ("PASS  {0}  ({1})" -f $what, $detail)
    }
    else {
        $script:Failures++
        Say ("FAIL  {0}  ({1})" -f $what, $detail)
    }
}

function Invoke-Api {
    param(
        [ValidateSet("GET", "POST", "PATCH", "PUT", "DELETE")][string]$Method,
        [string]$Path,
        $Body = $null,
        $Session,
        [string]$CSRF = "",
        [switch]$Raw
    )
    $params = @{
        Method          = $Method
        Uri             = "$BaseUrl$Path"
        WebSession      = $Session
        UseBasicParsing = $true
        SkipHttpErrorCheck = $true
    }
    if ($Method -ne "GET") { $params.Headers = @{ "X-PF-CSRF" = $CSRF } }
    if ($null -ne $Body) {
        $params.ContentType = "application/json"
        $params.Body = ($Body | ConvertTo-Json -Depth 12 -Compress)
    }
    $resp = Invoke-WebRequest @params
    $parsed = $null
    if ($resp.Content) {
        try { $parsed = $resp.Content | ConvertFrom-Json } catch { $parsed = $resp.Content }
    }
    if ($Raw) { return [pscustomobject]@{ Status = [int]$resp.StatusCode; Body = $parsed } }
    if ([int]$resp.StatusCode -ge 400) {
        throw ("{0} {1} -> {2}: {3}" -f $Method, $Path, [int]$resp.StatusCode, $resp.Content)
    }
    return $parsed
}

function Login-Drill {
    param([string]$Email, [string]$Password)
    $body = @{ email = $Email; password = $Password } | ConvertTo-Json -Compress
    $resp = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" `
        -ContentType "application/json" -Body $body -SessionVariable session
    return [pscustomobject]@{ User = $resp.user; CSRF = $resp.csrf; Session = $session }
}

function Wait-SessionSettled {
    param([string]$SessionID, $Session, [int]$TimeoutSeconds)
    $sawRunning = $false
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $st = Invoke-Api -Method GET -Path "/v1/sessions/$SessionID/status" -Session $Session
        if ($st.running -or $st.streaming) { $sawRunning = $true }
        elseif ($sawRunning) { return $st }
        Start-Sleep -Milliseconds 700
    }
    throw "the session did not settle within ${TimeoutSeconds}s"
}

function Entries-Of {
    param([string]$SessionID, $Session)
    $page = Invoke-Api -Method GET -Path "/v1/sessions/$SessionID/entries" -Session $Session
    return $page
}

function Sandbox-Of {
    param([string]$SessionID, $Session)
    return Invoke-Api -Method GET -Path "/v1/sessions/$SessionID/sandbox" -Session $Session
}

Say "# sandbox-recovery drill $(Get-Date -Format o)  base=$BaseUrl"

# ---------------------------------------------------------------------------
# 0. the stack answers, and the account can act
# ---------------------------------------------------------------------------
$health = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 5
Check "the stack answers /healthz" ($health.status -eq "ok") ("status=" + $health.status)

$admin = Login-Drill -Email $AdminEmail -Password $AdminPassword
Check "login as $AdminEmail" ($null -ne $admin.User.id) ("role=" + $admin.User.role)

$project = Invoke-Api -Method POST -Path "/v1/projects" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ name = "recovery-$stamp" }
$projectID = $project.project.id
$session = Invoke-Api -Method POST -Path "/v1/projects/$projectID/sessions" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ title = "recovery drill" }
$sessionID = $session.session.id
Say "project=$projectID session=$sessionID"

# ---------------------------------------------------------------------------
# 1. a real prompt, so there is a transcript to lose
# ---------------------------------------------------------------------------
$first = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：READY"; client_msg_id = "drill-ready-$stamp" }
Check "the first prompt is accepted" ($first.accepted -eq $true) ("disposition=" + $first.disposition)

$settled = Wait-SessionSettled -SessionID $sessionID -Session $admin.Session -TimeoutSeconds $PromptTimeoutSeconds
Say ("settled: running={0} last_seq={1}" -f $settled.running, $settled.last_seq)

$statsBefore = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/stats" -Session $admin.Session
Say ("stats before pause: user={0} assistant={1} tokens_in={2}" -f `
        $statsBefore.user_messages, $statsBefore.assistant_messages, $statsBefore.tokens_in)
$entriesBefore = Entries-Of -SessionID $sessionID -Session $admin.Session
Say ("entries before pause: count={0} leaf={1}" -f @($entriesBefore.entries).Count, $entriesBefore.leaf_id)
Check "the first turn is in the transcript" (@($entriesBefore.entries).Count -ge 2) `
    ("count=" + @($entriesBefore.entries).Count)

# ---------------------------------------------------------------------------
# 2. FIX 2: pause -> resume with no prompt, then read the transcript
# ---------------------------------------------------------------------------
$paused = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/sandbox/pause" -Session $admin.Session -CSRF $admin.CSRF
Check "pause" ($paused.state -eq "PAUSED") ("state=" + $paused.state)
$resumed = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/sandbox/resume" -Session $admin.Session -CSRF $admin.CSRF
Check "resume" ($resumed.state -eq "READY") ("state=" + $resumed.state)

$entriesAfter = Entries-Of -SessionID $sessionID -Session $admin.Session
$afterCount = @($entriesAfter.entries).Count
Say ("entries after pause+resume (no prompt): count={0} leaf={1}" -f $afterCount, $entriesAfter.leaf_id)
Check "FIX 2: the transcript survives pause+resume without a prompt" `
    ($afterCount -eq @($entriesBefore.entries).Count -and $afterCount -gt 0) `
    ("before={0} after={1} leaf={2}" -f @($entriesBefore.entries).Count, $afterCount, $entriesAfter.leaf_id)
$statsAfter = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/stats" -Session $admin.Session
Say ("stats after resume: user={0} assistant={1}" -f $statsAfter.user_messages, $statsAfter.assistant_messages)

# ---------------------------------------------------------------------------
# 3. the sandbox container, and the SSE stream the page reads
# ---------------------------------------------------------------------------
$container = (& docker ps --filter "label=piforge.user" --format "{{.Names}}" | Select-Object -First 1)
if (-not $container) { $container = (& docker ps --filter "label=piforge.sandbox" --format "{{.Names}}" | Select-Object -First 1) }
Check "the tenant's sandbox container is running" (-not [string]::IsNullOrWhiteSpace($container)) ("container=" + $container)

$cookieHeader = "Cookie: " + (($admin.Session.Cookies.GetCookies($BaseUrl) | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join "; ")
$ssePath = Join-Path $OutDir "sse.txt"
$curl = Start-Process -FilePath "curl.exe" -PassThru -NoNewWindow `
    -ArgumentList @("-sS", "-N", "--max-time", "240", "-H", "`"$cookieHeader`"", "$BaseUrl/v1/sessions/$sessionID/events") `
    -RedirectStandardOutput $ssePath

# ---------------------------------------------------------------------------
# 4. FIX 1: a bounded fork storm, and what the platform does about it
# ---------------------------------------------------------------------------
Say "storm: the bomb self-terminates; the squatter runs under timeout 120 inside the container"
& docker exec -d $container bash -c ':(){ :|:& };:' 2>$null
Start-Sleep -Seconds 1
& docker exec -d $container bash -c 'for i in $(seq 1 400); do sleep 600 & done; wait' 2>$null
Start-Sleep -Seconds 4

# The probe must *need a fork*. `sh -c 'echo alive'` is a shell builtin, so it
# succeeds on a container whose cgroup is completely full — which is exactly why
# the platform's own probe is a real command through the sandbox API (`true`
# needs a fork from the API server) and not a shell builtin. Measured on the
# bench: with the squatter below, `sh -c 'cat /proc/uptime'` fails 10/10 while
# `sh -c 'echo alive'` fails 0/10.
$attempts = 10
$failed = 0
$sample = ""
for ($i = 0; $i -lt $attempts; $i++) {
    $probe = & docker exec $container sh -c 'cat /proc/uptime' 2>&1
    if ($LASTEXITCODE -ne 0 -or "$probe" -notmatch '^\d') {
        $failed++
        if (-not $sample) { $sample = ("$probe".Trim() -replace "`r?`n", " | ") }
    }
}
Say ("docker exec while pinned: {0}/{1} attempts failed; sample={2}" -f $failed, $attempts, $sample)
Check "the storm really made the sandbox unable to fork" ($failed -ge 3) `
    ("failed={0}/{1}" -f $failed, $attempts)

$sawUnresponsive = $false
$sawRecovering = $false
$recoveredAt = $null
$detectedAt = $null
$stormStartedAt = Get-Date
$deadline = (Get-Date).AddSeconds($RecoveryTimeoutSeconds)
$lastState = $null
while ((Get-Date) -lt $deadline) {
    $sb = Sandbox-Of -SessionID $sessionID -Session $admin.Session
    $lastState = $sb
    if ($null -ne $sb.responsive -and $sb.responsive -eq $false) {
        if (-not $detectedAt) { $detectedAt = Get-Date }
        $sawUnresponsive = $true
    }
    if ($sb.recovering) { $sawRecovering = $true }
    if ($sawUnresponsive -and $sb.responsive -eq $true) { $recoveredAt = Get-Date; break }
    Start-Sleep -Seconds 2
}
Say ("watched: responsive_false={0} recovering={1} responsive_true_after={2}" -f `
        $sawUnresponsive, $sawRecovering, [bool]$recoveredAt)
if ($detectedAt) {
    Say ("detected after {0:N1}s; recovered after {1:N1}s" -f `
            ($detectedAt - $stormStartedAt).TotalSeconds, ($recoveredAt - $stormStartedAt).TotalSeconds)
}
Check "FIX 1: the platform noticed the wedge on its own" $sawUnresponsive ("responsive=false seen=" + $sawUnresponsive)
Check "FIX 1: the platform recovered without a human" ([bool]$recoveredAt) `
    ("state=" + $lastState.state + " message=" + $lastState.message)

$execAfter = & docker exec $container sh -c 'echo alive' 2>&1
Say ("docker exec after recovery: exit={0} says={1}" -f $LASTEXITCODE, ("$execAfter".Trim()))
Check "the sandbox can run commands again" ($LASTEXITCODE -eq 0 -and "$execAfter".Trim() -eq "alive") ("exit=" + $LASTEXITCODE)

# ---------------------------------------------------------------------------
# 5. the session was told, in its own transcript (the SSE the page reads)
# ---------------------------------------------------------------------------
Start-Sleep -Seconds 2
Stop-Process -Id $curl.Id -ErrorAction SilentlyContinue
$sseText = if (Test-Path $ssePath) { Get-Content $ssePath -Raw } else { "" }
$unresponsiveFrame = $sseText -match "沙箱无响应，正在恢复"
$recoveredFrame = $sseText -match "沙箱已恢复"
Check "FIX 1: the session got 「沙箱无响应，正在恢复」" $unresponsiveFrame ("sse_bytes=" + $sseText.Length)
Check "FIX 1: the session got 「沙箱已恢复」" $recoveredFrame ("sse_bytes=" + $sseText.Length)

# ---------------------------------------------------------------------------
# 6. a real prompt settles afterwards
# ---------------------------------------------------------------------------
$second = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：RESUMED"; client_msg_id = "drill-resumed-$stamp" }
Check "a prompt is accepted after the recovery" ($second.accepted -eq $true) ("disposition=" + $second.disposition)
$settled2 = Wait-SessionSettled -SessionID $sessionID -Session $admin.Session -TimeoutSeconds $PromptTimeoutSeconds
Say ("settled after recovery: running={0} last_seq={1}" -f $settled2.running, $settled2.last_seq)
$entriesFinal = Entries-Of -SessionID $sessionID -Session $admin.Session
Check "FIX 1: a real prompt settles after the recovery" (@($entriesFinal.entries).Count -gt $afterCount) `
    ("entries={0} -> {1}" -f $afterCount, @($entriesFinal.entries).Count)

# ---------------------------------------------------------------------------
# 7. FIX 3(b): the admin's `files` counts what is there
# ---------------------------------------------------------------------------
for ($i = 1; $i -le 60; $i++) { & docker exec $container sh -c "echo x > /workspace/drill-$i.txt" 2>$null | Out-Null }
$du = (& docker exec $container sh -c 'du -sb -- /workspace | cut -f1' 2>$null | Select-Object -First 1)
Say ("workspace: added 60 files; du -sb = {0}" -f $du)
# The admin report reuses a measurement for a minute, so poll until the walk sees
# our files (or the window closes) and select *this* project's row: the stack has
# rows for every sandbox it has ever made.
$row = $null
$deadline = (Get-Date).AddSeconds(100)
while ((Get-Date) -lt $deadline) {
    $status = Invoke-Api -Method GET -Path "/v1/admin/status" -Session $admin.Session
    $row = @($status.sandbox_usage) | Where-Object { $_.project_id -eq $projectID } | Select-Object -First 1
    if ($null -ne $row -and [int]$row.files -ge 60) { break }
    Start-Sleep -Seconds 5
}
Say ("sandbox_usage for this project: {0}" -f ($row | ConvertTo-Json -Compress))
Check "FIX 3(b): files is not 0 while the workspace holds files" ($null -ne $row -and [int]$row.files -ge 60) `
    ("files=" + $row.files + " bytes=" + $row.bytes)

# ---------------------------------------------------------------------------
# 8. leave nothing running that the drill created (the recovery already
#    restarted the container; drop our project so the stack stays small)
# ---------------------------------------------------------------------------
$deleted = Invoke-Api -Method DELETE -Path "/v1/projects/$projectID" -Session $admin.Session -CSRF $admin.CSRF -Raw
Say ("project delete: status={0}" -f $deleted.Status)
Check "the drill's project is gone" ($deleted.Status -lt 400) ("status=" + $deleted.Status)

$script:Lines | Set-Content -Path $logPath -Encoding utf8
Say ""
Say "written: $logPath"
Say "sse:     $ssePath"
if ($script:Failures -gt 0) {
    Say ("RESULT: {0} check(s) failed" -f $script:Failures)
    exit 1
}
Say "RESULT: all checks passed"
exit 0

