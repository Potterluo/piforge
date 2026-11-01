<#
.SYNOPSIS
    Prove that a revoked model-gateway token stays revoked across a control-plane
    restart, and that a resumed session's new token still works.

.DESCRIPTION
    The defect (adversarial acceptance #2, risk 3): the gateway's revocation set
    was process-local, so a control-plane restart inside a token's one-hour life
    re-enabled every token minted before it.

    This drill runs against a stack the caller already started, and it never
    prints the token: it reads the sandbox's models.json, keeps the value in a
    variable, and reports only its length, a SHA-256 prefix and the claims
    (session, user, provider, iat/exp, generation) — the claims are not secret,
    the signature is.

    Calls that need the token to *work* are made from inside the sandbox, because
    after docs/EXECUTION-BOUNDARY.md §16.3 a token is bound to its sandbox: the
    same token from the host is 403 by design, and this drill would otherwise be
    measuring the binding instead of the revocation. Calls that ask "is it dead"
    are made from the host while the container is paused (it cannot run anything
    then), where a dead token answers 401.

    Steps (each one is a PASS/FAIL line in the artifact):
      1. a real prompt, so an agent starts and a token is minted;
      2. the token works from inside its sandbox (GET /gw/llm/<token>/v1/models
         -> 200) and is refused from the host (-> 403);
      3. pause the sandbox: the same token -> 401;
      4. restart the control plane (docker restart piforge);
      5. the same token -> 401 again  <-- the proof, and the boot log's
         `gateway_revocations on_record=` says the row was loaded from disk;
      6. resume, send a second prompt: the new token (generation N+1) -> 200 from
         the resumed sandbox, and the old token is still 401.

    Each real model call is a provider request: two here (the READY prompt and
    the RESUMED prompt). Nothing else in this script talks to the provider.

.PARAMETER BaseUrl
    The stack to drill. Default http://127.0.0.1:7723 (the isolated stack). It
    refuses 7722 so the standing pf-local deployment is never touched.

.PARAMETER Container
    The control-plane container to restart. Default piforge.
#>
param(
    [string]$BaseUrl = "http://127.0.0.1:7723",
    [string]$AdminEmail = "admin@piforge.local",
    [string]$AdminPassword = "piforge-admin-123",
    [string]$Container = "piforge",
    [int]$PromptTimeoutSeconds = 300,
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"

if ($BaseUrl -match ":7722\b") {
    throw "refusing to drill $BaseUrl : that port is the standing pf-local stack and must not be touched"
}
if ($Container -eq "pf-local") {
    throw "refusing to restart pf-local"
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $root "e2e/.artifacts/gateway-revocation/$stamp"
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
    if ($ok) { Say ("PASS  {0}  ({1})" -f $what, $detail) }
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
    return [pscustomobject]@{ Status = [int]$resp.StatusCode; Body = $parsed }
}

# One host-side call with a sandbox token. The URL is the same shape the
# sandbox's models.json carries, with the host's address instead of piforge:7700.
# Since §16.3 this is the *refused* path (403) for a live token, and the path that
# answers 401 for a dead one (revocation is checked before the binding).
function Get-ModelsStatusFromHost([string]$token) {
    $url = "$BaseUrl/gw/llm/$token/v1/models"
    try {
        $resp = Invoke-WebRequest -UseBasicParsing -Uri $url -SkipHttpErrorCheck -TimeoutSec 30
        return [int]$resp.StatusCode
    }
    catch {
        return 0
    }
}

# One call from inside the sandbox, the way the agent itself makes it: the token
# in the path, and the proof the agent's environment carries. The script goes in
# on stdin so neither value appears in an argv.
function Get-ModelsStatusInSandbox([string]$sandboxID, [string]$url) {
    $inner = @'
P=$(for p in /proc/[0-9]*; do tr '\0' '\n' < "$p/environ" 2>/dev/null | sed -n 's/^PIFORGE_SANDBOX_PROOF=//p'; done | head -n 1)
code=$(curl -sS -m 20 -o /tmp/pf-body -w '%{http_code}' -H "X-PF-Sandbox-Proof: $P" "URL" 2>/dev/null)
printf 'CODE=%s\n' "$code"
'@
    $inner = $inner.Replace('URL', $url)
    $raw = ($inner | & docker exec -i -u gem $sandboxID sh -s 2>&1) -join "`n"
    if ($raw -match 'CODE=(\d+)') { return [int]$Matches[1] }
    return 0
}

function Get-TokenFromSandbox([string]$sandboxID, [string]$sessionID) {
    $path = "/workspace/.piforge/agents/$sessionID/models.json"
    $raw = & docker exec -u gem $sandboxID cat $path 2>$null
    if (-not $raw) { return $null }
    $doc = "$raw" | ConvertFrom-Json
    return [pscustomobject]@{
        Token   = $doc.providers.piforge.apiKey
        BaseURL = $doc.providers.piforge.baseUrl
        Path    = $path
    }
}

# The claims are base64url JSON; decoding them names the session, the provider and
# the generation without revealing the signature.
function Read-Claims([string]$token) {
    $body = ($token -split '\.')[0]
    $pad = $body.Replace('-', '+').Replace('_', '/')
    while ($pad.Length % 4) { $pad += '=' }
    $json = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($pad))
    return $json | ConvertFrom-Json
}

function Get-TokenFingerprint([string]$token) {
    $sha = [System.Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($token))
    return (($sha[0..5] | ForEach-Object { $_.ToString("x2") }) -join "")
}

Say "# gateway revocation drill $(Get-Date -Format o)  base=$BaseUrl container=$Container"

$health = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 5
Check "the stack answers /healthz" ($health.status -eq "ok") ("status=" + $health.status)

$login = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
    -Body (@{ email = $AdminEmail; password = $AdminPassword } | ConvertTo-Json -Compress) -SessionVariable session
$admin = [pscustomobject]@{ User = $login.user; CSRF = $login.csrf; Session = $session }
Say "login: $($admin.User.email) role=$($admin.User.role)"

# Leftovers from an earlier run of this drill would otherwise take the tenant past
# its sandbox quota. Only this drill's own projects ("revocation-*") are removed.
$listed = Invoke-Api -Method GET -Path "/v1/projects" -Session $admin.Session
foreach ($p in @($listed.Body.projects)) {
    if ($p.name -like "revocation-*") {
        $r = Invoke-Api -Method DELETE -Path "/v1/projects/$($p.id)" -Session $admin.Session -CSRF $admin.CSRF
        Say ("cleanup: removed project {0} ({1}) -> {2}" -f $p.name, $p.id, $r.Status)
    }
}

$projectName = "revocation-$stamp"
$project = Invoke-Api -Method POST -Path "/v1/projects" -Session $admin.Session -CSRF $admin.CSRF -Body @{ name = $projectName }
$projectID = $project.Body.project.id
$session_ = Invoke-Api -Method POST -Path "/v1/projects/$projectID/sessions" -Session $admin.Session -CSRF $admin.CSRF -Body @{ title = "revocation" }
$sessionID = $session_.Body.session.id
Say "project=$projectID session=$sessionID"

$sandbox = $null
$deadline = (Get-Date).AddSeconds(180)
while ((Get-Date) -lt $deadline) {
    $r = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/sandbox" -Session $admin.Session
    if ($r.Status -eq 200 -and $r.Body.state -eq "READY") { $sandbox = $r.Body; break }
    Start-Sleep -Seconds 2
}
if (-not $sandbox) { throw "the sandbox did not become READY" }
$sandboxID = $sandbox.id
Say "sandbox=$sandboxID state=$($sandbox.state)"

# ---------------------------------------------------------------------------
# 1. a real prompt, so an agent starts and a token exists
# ---------------------------------------------------------------------------
$first = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：READY"; client_msg_id = "revoke-ready-$stamp" }
Check "the first prompt is accepted" ($first.Body.accepted -eq $true) ("disposition=" + $first.Body.disposition)

$sawRunning = $false
$settled = $null
$deadline = (Get-Date).AddSeconds($PromptTimeoutSeconds)
while ((Get-Date) -lt $deadline) {
    $st = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/status" -Session $admin.Session
    if ($st.Body.running -or $st.Body.streaming) { $sawRunning = $true }
    elseif ($sawRunning) { $settled = $st.Body; break }
    Start-Sleep -Milliseconds 700
}
Check "the first run settled" ($null -ne $settled) ("running=" + $settled.running + " last_seq=" + $settled.last_seq)

$before = Get-TokenFromSandbox $sandboxID $sessionID
if (-not $before -or -not $before.Token) { throw "no token was found in $($before.Path) — did the agent start?" }
$claims = Read-Claims $before.Token
# Never print the token: the base URL is the one place it would appear, so it is
# masked before anything reaches the console or the artifact.
$maskedBase = "$($before.BaseURL)" -replace [regex]::Escape($before.Token), "<token>"
Say ("token A: len={0} sha256={1} session={2} provider={3} g={4} ttl_s={5} baseUrl={6}" -f `
        $before.Token.Length, (Get-TokenFingerprint $before.Token), $claims.s, $claims.p, [int]$claims.g, ($claims.exp - $claims.iat), $maskedBase)
Check "token A is the session's own token" ($claims.s -eq $sessionID) ("claims.s=" + $claims.s)

$statusInSandbox = Get-ModelsStatusInSandbox $sandboxID "$($before.BaseURL)/models"
Check "token A works from inside its sandbox before the pause" ($statusInSandbox -eq 200) ("GET inside -> " + $statusInSandbox)
$statusFromHost = Get-ModelsStatusFromHost $before.Token
Check "the same token is refused from the host (§16.3 binds it to the sandbox)" ($statusFromHost -eq 403) ("GET from the host -> " + $statusFromHost)

# ---------------------------------------------------------------------------
# 2. pause revokes it (the existing behaviour, re-proved here because the
#    restart proof depends on the pause having happened). A paused container runs
#    nothing, so this one call is made from the host: 401 means revoked, which is
#    checked before the address is.
# ---------------------------------------------------------------------------
$pause = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/sandbox/pause" -Session $admin.Session -CSRF $admin.CSRF
Check "pause" ($pause.Body.state -eq "PAUSED") ("state=" + $pause.Body.state)
$statusAfterPause = Get-ModelsStatusFromHost $before.Token
Check "token A is refused after the pause" ($statusAfterPause -eq 401) ("GET -> " + $statusAfterPause)

# ---------------------------------------------------------------------------
# 3. restart the control plane. Nothing in memory survives; only the database is
#    left to say the token is dead.
# ---------------------------------------------------------------------------
$bootBefore = & docker logs $Container 2>&1 | Select-String -Pattern "revocations are durable" | Select-Object -Last 1
Say ("boot log before the restart: " + ("$bootBefore".Trim()))

Say "restarting $Container ..."
& docker restart $Container | Out-Null
$healthy = $false
$deadline = (Get-Date).AddSeconds(120)
while ((Get-Date) -lt $deadline) {
    try {
        $h = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 2
        if ($h.status -eq "ok") { $healthy = $true; break }
    }
    catch { Start-Sleep -Seconds 1 }
}
Check "the control plane is healthy again" $healthy "healthz=ok"

$bootAfter = & docker logs $Container 2>&1 | Select-String -Pattern "revocations are durable" | Select-Object -Last 1
Say ("boot log after the restart: " + ("$bootAfter".Trim()))
$onRecord = 0
if ("$bootAfter" -match "on_record=(\d+)") { $onRecord = [int]$Matches[1] }
Check "the restarted process loaded a revocation from disk" ($onRecord -ge 1) ("on_record=" + $onRecord)

$statusAfterRestart = Get-ModelsStatusFromHost $before.Token
Check "token A is STILL refused after the restart" ($statusAfterRestart -eq 401) ("GET -> " + $statusAfterRestart)

# ---------------------------------------------------------------------------
# 4. resume: the session's next spawn must work, which is what the generation
#    counter is for. A token minted at generation N+1 has to verify against the
#    persisted N.
# ---------------------------------------------------------------------------
$resume = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/sandbox/resume" -Session $admin.Session -CSRF $admin.CSRF
Check "resume" ($resume.Body.state -eq "READY") ("state=" + $resume.Body.state)

$second = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：RESUMED"; client_msg_id = "revoke-resumed-$stamp" }
Check "the prompt after the resume is accepted" ($second.Body.accepted -eq $true) ("disposition=" + $second.Body.disposition)

$sawRunning = $false
$settled = $null
$deadline = (Get-Date).AddSeconds($PromptTimeoutSeconds)
while ((Get-Date) -lt $deadline) {
    $st = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/status" -Session $admin.Session
    if ($st.Body.running -or $st.Body.streaming) { $sawRunning = $true }
    elseif ($sawRunning) { $settled = $st.Body; break }
    Start-Sleep -Milliseconds 700
}
Check "the resumed run settled" ($null -ne $settled) ("running=" + $settled.running + " last_seq=" + $settled.last_seq)

$after = Get-TokenFromSandbox $sandboxID $sessionID
if (-not $after -or -not $after.Token) { throw "no token was found after the resume" }
$claimsAfter = Read-Claims $after.Token
Say ("token B: len={0} sha256={1} session={2} g={3} ttl_s={4}" -f `
        $after.Token.Length, (Get-TokenFingerprint $after.Token), $claimsAfter.s, [int]$claimsAfter.g, ($claimsAfter.exp - $claimsAfter.iat))
Check "token B is a different token, one generation later" (($after.Token -ne $before.Token) -and ([int]$claimsAfter.g -eq ([int]$claims.g + 1))) ("g: " + [int]$claims.g + " -> " + [int]$claimsAfter.g)

$statusNew = Get-ModelsStatusInSandbox $sandboxID "$($after.BaseURL)/models"
Check "token B works from the resumed sandbox (the resumed generation is valid)" ($statusNew -eq 200) ("GET inside -> " + $statusNew)
$statusNewHost = Get-ModelsStatusFromHost $after.Token
Check "token B is refused from the host, like token A was" ($statusNewHost -eq 403) ("GET from the host -> " + $statusNewHost)
# Revocation is checked before the binding, so a dead token answers 401 from
# anywhere — including from its own sandbox, where the address is right.
$statusOldInSandbox = Get-ModelsStatusInSandbox $sandboxID "$($before.BaseURL)/models"
$statusOldAgain = Get-ModelsStatusFromHost $before.Token
Check "token A is still refused while token B works" (($statusOldAgain -eq 401) -and ($statusOldInSandbox -eq 401)) `
    ("from the host -> " + $statusOldAgain + ", from inside the sandbox -> " + $statusOldInSandbox)

$verdict = "RESULT durable_revocation={0} old_token_after_restart={1} resumed_token={2} host_before_pause={3} generations={4}->{5} on_record={6}" -f `
    ($(if ($statusAfterRestart -eq 401) { "401" } else { "BROKEN" })), $statusAfterRestart, $statusNew, $statusFromHost, [int]$claims.g, [int]$claimsAfter.g, $onRecord
Say ""
Say $verdict
Say ("failures=" + $script:Failures)

$script:Lines | Set-Content -Path $logPath
[pscustomobject]@{
    project_id           = $projectID
    session_id           = $sessionID
    sandbox_id           = $sandboxID
    token_a_sha256       = (Get-TokenFingerprint $before.Token)
    token_a_generation   = $claims.g
    token_b_sha256       = (Get-TokenFingerprint $after.Token)
    token_b_generation   = $claimsAfter.g
    status_in_sandbox    = $statusInSandbox
    status_from_host     = $statusFromHost
    status_after_pause   = $statusAfterPause
    status_after_restart = $statusAfterRestart
    status_resumed       = $statusNew
    status_resumed_host  = $statusNewHost
    on_record            = $onRecord
    failures             = $script:Failures
} | ConvertTo-Json -Depth 4 | Set-Content -Path (Join-Path $OutDir "summary.json")
Write-Host "artifacts: $OutDir"

if ($script:Failures -gt 0) { exit 1 }
