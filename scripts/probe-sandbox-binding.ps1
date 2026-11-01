<#
.SYNOPSIS
    Prove that a model-gateway token only works from its own sandbox.

.DESCRIPTION
    The defect (docs/EXECUTION-BOUNDARY.md §16.3): a token was a portable bearer
    credential. It answered /gw/llm from the operator's host, from another sandbox
    of the same tenant and from another tenant, for up to an hour, and it was
    readable in the sandbox's models.json.

    What this drill measures, on a stack the caller already started:

      1. from inside the sandbox the agent's own path still works: a real prompt
         settles, and GET <baseUrl>/models from the sandbox -> 200;
      2. from the host the same token fails (403), with the reason recorded —
         also when the attacker copies the proof along with the token;
      3. from a second sandbox of the same tenant, and from another tenant's
         sandbox, the first token fails;
      4. after pause -> resume the session's agent works with a *new* token, and
         after a control-plane restart it works with the *same* token (the binding
         follows the sandbox instead of trusting a map built at mint time);
      5. revocation still works (401 after pause) and survives a restart;
      6. the MCP broker token is control-plane-minted too, and it is bound the
         same way: it answers from its sandbox and is refused from the host.

    Secrets: this script never prints a token, a proof or a URL that carries one.
    It reports lengths and SHA-256 prefixes. Requests made *inside* a sandbox are
    fed to `sh -s` on stdin, so neither the token nor the proof appears in any
    process's argv on this host.

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
    [string]$OtherEmail = "dev@piforge.local",
    [string]$OtherPassword = "piforge-dev-123",
    [string]$Container = "piforge",
    [int]$PromptTimeoutSeconds = 300,
    # What the brokered MCP server points at. It is resolved by the *control
    # plane*, so it has to be an address that container can reach — its own
    # loopback, not this host's published port.
    [string]$McpProbeUrl = "http://127.0.0.1:7700/healthz",
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"

if ($BaseUrl -match ":7722\b") {
    throw "refusing to drill $BaseUrl : that port is the standing pf-local stack and must not be touched"
}
if ($Container -eq "pf-local") { throw "refusing to restart pf-local" }

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $root "e2e/.artifacts/sandbox-binding/$stamp"
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

function Get-TokenFingerprint([string]$value) {
    if ([string]::IsNullOrEmpty($value)) { return "none" }
    $sha = [System.Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($value))
    return (($sha[0..5] | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Read-Claims([string]$token) {
    $body = ($token -split '\.')[0]
    $pad = $body.Replace('-', '+').Replace('_', '/')
    while ($pad.Length % 4) { $pad += '=' }
    $json = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($pad))
    return $json | ConvertFrom-Json
}

# Read the agent's model configuration out of the sandbox, as the agent's own
# account can. This is the file an attacker copies, which is why the drill checks
# what it does and does not contain.
function Get-AgentConfig([string]$sandboxID, [string]$sessionID) {
    $dir = "/workspace/.piforge/agents/$sessionID"
    $models = & docker exec -u gem $sandboxID cat "$dir/models.json" 2>$null
    $mcp = & docker exec -u gem $sandboxID cat "$dir/mcp.json" 2>$null
    $out = [pscustomobject]@{ ModelsRaw = "$models"; McpRaw = "$mcp"; Dir = $dir }
    if ("$models") {
        $doc = "$models" | ConvertFrom-Json
        $out | Add-Member -NotePropertyName Token -NotePropertyValue $doc.providers.piforge.apiKey
        $out | Add-Member -NotePropertyName BaseURL -NotePropertyValue $doc.providers.piforge.baseUrl
        $out | Add-Member -NotePropertyName Headers -NotePropertyValue $doc.providers.piforge.headers
    }
    return $out
}

# The proof lives in the agent process's environment, not in any file. Reading it
# here is the *honest* attack: anything running as the sandbox account can do it.
function Get-SandboxProof([string]$sandboxID) {
    $script = 'for p in /proc/[0-9]*; do tr "\0" "\n" < "$p/environ" 2>/dev/null | sed -n "s/^PIFORGE_SANDBOX_PROOF=//p"; done | head -n 1'
    $raw = (& docker exec -u gem $sandboxID sh -c $script 2>$null) -join ""
    return "$raw".Trim()
}

# One HTTP request from *inside* a sandbox.
#
# The script is fed to `sh -s` on stdin, so the URL (which carries the token) and
# the proof never appear in a process's argv — not on the host, not in the
# container.
function Get-InSandboxFetch {
    param(
        [string]$SandboxID,
        [string]$Url,
        [string]$Proof = "",
        [switch]$ProofFromEnv
    )
    if ($ProofFromEnv) {
        $hdr = @'
P=$(for p in /proc/[0-9]*; do tr '\0' '\n' < "$p/environ" 2>/dev/null | sed -n 's/^PIFORGE_SANDBOX_PROOF=//p'; done | head -n 1)
H="X-PF-Sandbox-Proof: $P"
'@
    }
    elseif ($Proof) { $hdr = 'H="X-PF-Sandbox-Proof: ' + $Proof + '"' }
    else { $hdr = 'H=' }

    $inner = @'
HDRLINE
if [ -z "$H" ]; then
  code=$(curl -sS -m 20 -o /tmp/pf-body -w '%{http_code}' "URL" 2>/tmp/pf-err)
else
  code=$(curl -sS -m 20 -o /tmp/pf-body -w '%{http_code}' -H "$H" "URL" 2>/tmp/pf-err)
fi
printf 'CODE=%s\nBODY=' "$code"
head -c 300 /tmp/pf-body 2>/dev/null
printf '\nERR='
head -c 200 /tmp/pf-err 2>/dev/null
printf '\n'
'@
    $inner = $inner.Replace('HDRLINE', $hdr).Replace('URL', $Url)
    $raw = ($inner | & docker exec -i -u gem $SandboxID sh -s 2>&1) -join "`n"
    $status = 0
    if ($raw -match 'CODE=(\d+)') { $status = [int]$Matches[1] }
    $body = ""
    if ($raw -match 'BODY=([\s\S]*?)\nERR=') { $body = $Matches[1].Trim() }
    return [pscustomobject]@{ Status = $status; Body = $body; Raw = $raw }
}

# One HTTP request from this host, with an optional proof — the "attacker copied
# the token (and whatever else) and used it from outside" case.
function Get-HostFetch {
    param([string]$Path, [string]$Proof = "")
    $params = @{
        Uri                = "$BaseUrl$Path"
        UseBasicParsing    = $true
        SkipHttpErrorCheck = $true
        TimeoutSec         = 30
    }
    if ($Proof) { $params.Headers = @{ "X-PF-Sandbox-Proof" = $Proof } }
    $resp = Invoke-WebRequest @params
    return [pscustomobject]@{ Status = [int]$resp.StatusCode; Body = "$($resp.Content)" }
}

function Get-ContainerLog([string]$pattern, [int]$tail = 200) {
    $lines = & docker logs --tail $tail $Container 2>&1
    return @($lines | Select-String -Pattern $pattern | Select-Object -Last 3 | ForEach-Object { "$_".Trim() })
}

function Wait-PromptSettled([string]$sessionID, $Session) {
    $sawRunning = $false
    $settled = $null
    $deadline = (Get-Date).AddSeconds($PromptTimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $st = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/status" -Session $Session
        if ($st.Body.running -or $st.Body.streaming) { $sawRunning = $true }
        elseif ($sawRunning) { $settled = $st.Body; break }
        Start-Sleep -Milliseconds 700
    }
    return $settled
}

# The last assistant preview in the session tree, so "a real prompt settles" means
# "the model answered" rather than "the spinner stopped". TreeEntry carries a
# preview, not the full text.
function Get-LastAssistantText([string]$sessionID, $Session) {
    $r = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/entries" -Session $Session
    $text = ""
    foreach ($entry in @($r.Body.entries)) {
        if ($entry.role -eq "assistant" -and $entry.preview) { $text = "$($entry.preview)" }
    }
    return $text
}

# The drill creates two sandboxes for the tenant under test, and a tenant's
# sandbox quota is finite (three in the shipped isolated stack), so a second run
# against the same database would hit the ceiling. Delete this drill's own
# leftovers first — only projects named "binding-*", never anything else.
function Remove-PreviousDrillProjects($who) {
    $listed = Invoke-Api -Method GET -Path "/v1/projects" -Session $who.Session
    foreach ($p in @($listed.Body.projects)) {
        if ($p.name -like "binding-*") {
            $r = Invoke-Api -Method DELETE -Path "/v1/projects/$($p.id)" -Session $who.Session -CSRF $who.CSRF
            Say ("cleanup: removed project {0} ({1}) -> {2}" -f $p.name, $p.id, $r.Status)
        }
    }
}

function New-SandboxFor([string]$email, [string]$password, [string]$name) {
    $login = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
        -Body (@{ email = $email; password = $password } | ConvertTo-Json -Compress) -SessionVariable session
    $who = [pscustomobject]@{ User = $login.user; CSRF = $login.csrf; Session = $session }
    $project = Invoke-Api -Method POST -Path "/v1/projects" -Session $who.Session -CSRF $who.CSRF -Body @{ name = "$name-$stamp" }
    if ($project.Status -ne 201) { throw "creating project $name failed: $($project.Status) $($project.Body)" }
    $projectID = $project.Body.project.id
    $sess = Invoke-Api -Method POST -Path "/v1/projects/$projectID/sessions" -Session $who.Session -CSRF $who.CSRF -Body @{ title = $name }
    if ($sess.Status -ne 201) { throw "creating session $name failed: $($sess.Status) $($sess.Body)" }
    $sessionID = $sess.Body.session.id
    $sandbox = $null
    $deadline = (Get-Date).AddSeconds(240)
    while ((Get-Date) -lt $deadline) {
        $r = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/sandbox" -Session $who.Session
        if ($r.Status -eq 200 -and $r.Body.state -eq "READY") { $sandbox = $r.Body; break }
        Start-Sleep -Seconds 2
    }
    if (-not $sandbox) { throw "the sandbox for $name did not become READY" }
    return [pscustomobject]@{
        Who       = $who
        ProjectID = $projectID
        SessionID = $sessionID
        SandboxID = $sandbox.id
        State     = $sandbox.state
    }
}

# ---------------------------------------------------------------------------
# setup
# ---------------------------------------------------------------------------
Say "# sandbox binding drill $(Get-Date -Format o)  base=$BaseUrl container=$Container"

$health = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 5
Check "the stack answers /healthz" ($health.status -eq "ok") ("status=" + $health.status)

$binderLog = Get-ContainerLog "tokens are bound to their sandbox" 400
Say ("boot log: " + ($binderLog -join " | "))

# The MCP server is created before the first prompt, because the broker token is
# minted when an agent starts (it is written into the sandbox's mcp.json). Its URL
# is the control plane's own /healthz: reachable from the control plane, so a
# brokered call that gets through answers 200 and one that is refused does not.
$admin = $null
$adminLogin = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
    -Body (@{ email = $AdminEmail; password = $AdminPassword } | ConvertTo-Json -Compress) -SessionVariable adminSession
$admin = [pscustomobject]@{ User = $adminLogin.user; CSRF = $adminLogin.csrf; Session = $adminSession }
Say "login: $($admin.User.email) role=$($admin.User.role)"

# Leftovers from an earlier run of this drill (see Remove-PreviousDrillProjects).
Remove-PreviousDrillProjects $admin
$otherLogin = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
    -Body (@{ email = $OtherEmail; password = $OtherPassword } | ConvertTo-Json -Compress) -SessionVariable otherSession
Remove-PreviousDrillProjects ([pscustomobject]@{ User = $otherLogin.user; CSRF = $otherLogin.csrf; Session = $otherSession })

$mcpServer = Invoke-Api -Method POST -Path "/v1/mcp/servers" -Session $admin.Session -CSRF $admin.CSRF -Body @{
    name      = "binding-drill-$stamp"
    transport = "http"
    url       = $McpProbeUrl
    enabled   = $true
}
Check "an HTTP MCP server exists, so the broker mint is exercised" ($mcpServer.Status -eq 201) ("status=" + $mcpServer.Status + " upstream=" + $McpProbeUrl)

$main = New-SandboxFor $AdminEmail $AdminPassword "binding-main"
Say "sandbox A: project=$($main.ProjectID) session=$($main.SessionID) sandbox=$($main.SandboxID) state=$($main.State)"

# ---------------------------------------------------------------------------
# 1. the agent's own path: a real prompt settles, and the sandbox can reach the
#    gateway it was pointed at
# ---------------------------------------------------------------------------
$first = Invoke-Api -Method POST -Path "/v1/sessions/$($main.SessionID)/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：READY"; client_msg_id = "binding-ready-$stamp" }
Check "the first prompt is accepted" ($first.Body.accepted -eq $true) ("disposition=" + $first.Body.disposition)

$settled1 = Wait-PromptSettled $main.SessionID $admin.Session
$answer1 = Get-LastAssistantText $main.SessionID $admin.Session
Check "the first prompt settles with a model answer" (($null -ne $settled1) -and -not [string]::IsNullOrWhiteSpace($answer1)) `
    ("running=" + $settled1.running + " answer=" + ($answer1 -replace '\s+', ' ').Substring(0, [Math]::Min(40, $answer1.Length)))

$cfg = Get-AgentConfig $main.SandboxID $main.SessionID
if (-not $cfg.Token) { throw "no token was found in $($cfg.Dir)/models.json" }
$claims = Read-Claims $cfg.Token
Say ("token A: len={0} sha256={1} session={2} sandbox_claim={3} provider={4} g={5}" -f `
        $cfg.Token.Length, (Get-TokenFingerprint $cfg.Token), $claims.s, $claims.b, $claims.p, [int]$claims.g)
Check "the token carries the sandbox it may be spent from" ($claims.b -eq $main.SandboxID) ("claim=" + $claims.b)
Check "models.json names the proof as a reference and does not contain it" `
    (("$($cfg.Headers.'X-PF-Sandbox-Proof')" -eq '$PIFORGE_SANDBOX_PROOF') -and -not $cfg.ModelsRaw.Contains("X-PF-Sandbox-Proof`": `"$((Get-SandboxProof $main.SandboxID))`"")) `
    ("header=" + $cfg.Headers.'X-PF-Sandbox-Proof')

$proofA = Get-SandboxProof $main.SandboxID
Say ("proof A: len={0} sha256={1} (read from the agent's own environment, which is the honest attack)" -f $proofA.Length, (Get-TokenFingerprint $proofA))
Check "the agent's environment carries a proof" ($proofA.Length -ge 32) ("len=" + $proofA.Length)

$inSandbox = Get-InSandboxFetch -SandboxID $main.SandboxID -Url "$($cfg.BaseURL)/models" -ProofFromEnv
Check "GET <baseUrl>/models from INSIDE the sandbox -> 200" ($inSandbox.Status -eq 200) ("status=" + $inSandbox.Status)

$inSandboxNoProof = Get-InSandboxFetch -SandboxID $main.SandboxID -Url "$($cfg.BaseURL)/models"
Check "the same call without the proof is refused (the proof half is real)" ($inSandboxNoProof.Status -eq 403) `
    ("status=" + $inSandboxNoProof.Status + " body=" + $inSandboxNoProof.Body)

# ---------------------------------------------------------------------------
# 2. from the host, the same token fails
# ---------------------------------------------------------------------------
$path = "/gw/llm/$($cfg.Token)/v1/models"
$hostWithProof = Get-HostFetch -Path $path -Proof $proofA
Check "the same token from the HOST is refused -- even with the proof copied along" `
    ($hostWithProof.Status -eq 403 -and $hostWithProof.Body.Contains("sandbox_address_mismatch")) `
    ("status=" + $hostWithProof.Status + " body=" + ($hostWithProof.Body -replace '\s+', ' '))
$hostNoProof = Get-HostFetch -Path $path
Check "the token alone from the host is refused as well (the address is checked first, so the reason is the same)" `
    ($hostNoProof.Status -eq 403) ("status=" + $hostNoProof.Status + " body=" + ($hostNoProof.Body -replace '\s+', ' '))
$refusalLog = Get-ContainerLog "llmgw: refused" 300
Say ("refusal log: " + ($refusalLog -join " | "))

# ---------------------------------------------------------------------------
# 3. a second sandbox of the same tenant, and another tenant's sandbox
# ---------------------------------------------------------------------------
$sibling = New-SandboxFor $AdminEmail $AdminPassword "binding-sibling"
Say "sandbox B (same tenant): sandbox=$($sibling.SandboxID) state=$($sibling.State)"
$other = New-SandboxFor $OtherEmail $OtherPassword "binding-other"
Say "sandbox C (another tenant): sandbox=$($other.SandboxID) state=$($other.State)"
Check "the three sandboxes are different containers" (($main.SandboxID -ne $sibling.SandboxID) -and ($main.SandboxID -ne $other.SandboxID)) `
    ("A=" + $main.SandboxID + " B=" + $sibling.SandboxID + " C=" + $other.SandboxID)

# With the proof copied along: the address is the half that fails, which is the
# strongest statement (an attacker who copies everything still cannot use it).
$fromSibling = Get-InSandboxFetch -SandboxID $sibling.SandboxID -Url "$($cfg.BaseURL)/models" -Proof $proofA
Check "token A from a SIBLING sandbox of the same tenant is refused" `
    ($fromSibling.Status -eq 403 -and $fromSibling.Body.Contains("sandbox_address_mismatch")) `
    ("status=" + $fromSibling.Status + " body=" + ($fromSibling.Body -replace '\s+', ' '))
$fromOther = Get-InSandboxFetch -SandboxID $other.SandboxID -Url "$($cfg.BaseURL)/models" -Proof $proofA
Check "token A from ANOTHER TENANT's sandbox is refused" `
    ($fromOther.Status -eq 403 -and $fromOther.Body.Contains("sandbox_address_mismatch")) `
    ("status=" + $fromOther.Status + " body=" + ($fromOther.Body -replace '\s+', ' '))

# ---------------------------------------------------------------------------
# 4a. a control-plane restart: the token minted before it still works, from the
#     sandbox, because the binding is resolved per request rather than remembered
# ---------------------------------------------------------------------------
Say "restarting $Container (the agent inside the sandbox is meant to survive it) ..."
& docker restart $Container | Out-Null
$healthy = $false
$deadline = (Get-Date).AddSeconds(150)
while ((Get-Date) -lt $deadline) {
    try {
        $h = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 2
        if ($h.status -eq "ok") { $healthy = $true; break }
    }
    catch { Start-Sleep -Seconds 1 }
}
Check "the control plane is healthy again" $healthy "healthz=ok"

$adminLogin2 = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
    -Body (@{ email = $AdminEmail; password = $AdminPassword } | ConvertTo-Json -Compress) -SessionVariable adminSession2
$admin = [pscustomobject]@{ User = $adminLogin2.user; CSRF = $adminLogin2.csrf; Session = $adminSession2 }

$afterRestart = Get-InSandboxFetch -SandboxID $main.SandboxID -Url "$($cfg.BaseURL)/models" -Proof $proofA
Check "token A still works from its sandbox after the restart (same token, no re-mint)" `
    ($afterRestart.Status -eq 200) ("status=" + $afterRestart.Status)

$prompt2 = Invoke-Api -Method POST -Path "/v1/sessions/$($main.SessionID)/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：AGAIN"; client_msg_id = "binding-again-$stamp" }
$settled2 = Wait-PromptSettled $main.SessionID $admin.Session
$answer2 = Get-LastAssistantText $main.SessionID $admin.Session
$cfgAfterRestart = Get-AgentConfig $main.SandboxID $main.SessionID
Check "the session's agent still works after the restart" (($null -ne $settled2) -and -not [string]::IsNullOrWhiteSpace($answer2)) `
    ("accepted=" + $prompt2.Body.accepted + " token_unchanged=" + ($cfgAfterRestart.Token -eq $cfg.Token))

# ---------------------------------------------------------------------------
# 5. revocation: pause ends the token, and the revocation survives a restart
# ---------------------------------------------------------------------------
$pause = Invoke-Api -Method POST -Path "/v1/sessions/$($main.SessionID)/sandbox/pause" -Session $admin.Session -CSRF $admin.CSRF
Check "pause" ($pause.Body.state -eq "PAUSED") ("state=" + $pause.Body.state)
$paused = Get-InSandboxFetch -SandboxID $main.SandboxID -Url "$($cfg.BaseURL)/models" -Proof $proofA
if ($paused.Status -eq 0) {
    # A paused container cannot run the probe. Use the host then: 401 (revoked)
    # rather than 403 (wrong address) is the point, and the token is dead either way.
    $hostAfterPause = Get-HostFetch -Path $path -Proof $proofA
    Check "token A is refused after the pause" ($hostAfterPause.Status -eq 401) `
        ("sandbox paused (probe ran from the host) -> " + $hostAfterPause.Status + " body=" + ($hostAfterPause.Body -replace '\s+', ' '))
    $pauseStatus = $hostAfterPause.Status
}
else {
    Check "token A is refused after the pause" ($paused.Status -eq 401) ("status=" + $paused.Status + " body=" + $paused.Body)
    $pauseStatus = $paused.Status
}

Say "restarting $Container again, to prove the revocation is durable ..."
& docker restart $Container | Out-Null
$healthy = $false
$deadline = (Get-Date).AddSeconds(150)
while ((Get-Date) -lt $deadline) {
    try {
        $h = Invoke-RestMethod -Uri "$BaseUrl/healthz" -TimeoutSec 2
        if ($h.status -eq "ok") { $healthy = $true; break }
    }
    catch { Start-Sleep -Seconds 1 }
}
Check "the control plane is healthy again (second restart)" $healthy "healthz=ok"
$bootAfter = Get-ContainerLog "revocations are durable" 300
Say ("boot log after the restart: " + ($bootAfter -join " | "))
$onRecord = 0
# The *last* line is the one this boot wrote; an earlier line in the tail is the
# previous process's.
if ($bootAfter.Count -gt 0 -and "$($bootAfter[-1])" -match "on_record=(\d+)") { $onRecord = [int]$Matches[1] }
Check "the restarted process loaded a revocation from disk" ($onRecord -ge 1) ("on_record=" + $onRecord)

$adminLogin3 = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" -ContentType "application/json" `
    -Body (@{ email = $AdminEmail; password = $AdminPassword } | ConvertTo-Json -Compress) -SessionVariable adminSession3
$admin = [pscustomobject]@{ User = $adminLogin3.user; CSRF = $adminLogin3.csrf; Session = $adminSession3 }
$revokedAfterRestart = Get-HostFetch -Path $path -Proof $proofA
Check "token A is STILL refused after the restart" ($revokedAfterRestart.Status -eq 401) ("status=" + $revokedAfterRestart.Status)

# ---------------------------------------------------------------------------
# 4b. pause -> resume: the session's next spawn gets a new token (and a new
#     proof), and it works from the resumed sandbox
# ---------------------------------------------------------------------------
$resume = Invoke-Api -Method POST -Path "/v1/sessions/$($main.SessionID)/sandbox/resume" -Session $admin.Session -CSRF $admin.CSRF
Check "resume" ($resume.Body.state -eq "READY") ("state=" + $resume.Body.state)

$prompt3 = Invoke-Api -Method POST -Path "/v1/sessions/$($main.SessionID)/messages" -Session $admin.Session -CSRF $admin.CSRF `
    -Body @{ text = "只回复一个词：RESUMED"; client_msg_id = "binding-resumed-$stamp" }
$settled3 = Wait-PromptSettled $main.SessionID $admin.Session
$answer3 = Get-LastAssistantText $main.SessionID $admin.Session
Check "the prompt after the resume settles with a model answer" (($null -ne $settled3) -and -not [string]::IsNullOrWhiteSpace($answer3)) `
    ("accepted=" + $prompt3.Body.accepted)

$cfgB = Get-AgentConfig $main.SandboxID $main.SessionID
$claimsB = Read-Claims $cfgB.Token
Say ("token B: len={0} sha256={1} g={2} sandbox_claim={3}" -f $cfgB.Token.Length, (Get-TokenFingerprint $cfgB.Token), [int]$claimsB.g, $claimsB.b)
Check "token B is a different token, one generation later" (($cfgB.Token -ne $cfg.Token) -and ([int]$claimsB.g -eq ([int]$claims.g + 1))) `
    ("g: " + [int]$claims.g + " -> " + [int]$claimsB.g)

$inSandboxB = Get-InSandboxFetch -SandboxID $main.SandboxID -Url "$($cfgB.BaseURL)/models" -ProofFromEnv
Check "token B works from the resumed sandbox" ($inSandboxB.Status -eq 200) ("status=" + $inSandboxB.Status)
$hostB = Get-HostFetch -Path "/gw/llm/$($cfgB.Token)/v1/models" -Proof (Get-SandboxProof $main.SandboxID)
Check "token B is refused from the host like token A was" ($hostB.Status -eq 403) ("status=" + $hostB.Status)

# ---------------------------------------------------------------------------
# 6. the MCP broker token: control-plane-minted, bound the same way
# ---------------------------------------------------------------------------
$mcpInStatus = 0
$mcpHostStatus = 0
if ($cfg.McpRaw) {
    $mcpDoc = $cfg.McpRaw | ConvertFrom-Json
    # The file the agent reads: {"mcpServers":{"<name>":{"url":"http://…/gw/mcp/<token>"}}}.
    # Take the first server whatever it is called — the drill names it, but the
    # point is the shape, not the name.
    $mcpUrl = ""
    $servers = $mcpDoc.mcpServers.PSObject.Properties
    if ($servers) {
        $firstServer = $servers | Select-Object -First 1
        if ($firstServer) { $mcpUrl = "$($firstServer.Value.url)" }
    }
    $mcpPath = ""
    if ($mcpUrl -match '/gw/mcp/([^/]+)$') {
        $mcpPath = "/gw/mcp/" + $Matches[1]
    }
    Check "the sandbox's mcp.json holds a broker URL, not a credential" ([bool]$mcpPath) `
        ("servers=" + @($servers).Count + " url_shape=" + ($mcpUrl -replace '/gw/mcp/[^/]+', '/gw/mcp/<token>'))

    if ($mcpPath) {
        $mcpIn = Get-InSandboxFetch -SandboxID $main.SandboxID -Url $mcpUrl
        Check "the broker token answers from its own sandbox" ($mcpIn.Status -eq 200) ("status=" + $mcpIn.Status + " body=" + ($mcpIn.Body -replace '\s+', ' '))
        $mcpInStatus = $mcpIn.Status
        $mcpHost = Get-HostFetch -Path $mcpPath
        Check "the broker token is refused from the host" ($mcpHost.Status -eq 401) ("status=" + $mcpHost.Status + " body=" + ($mcpHost.Body -replace '\s+', ' '))
        $mcpHostStatus = $mcpHost.Status
        $mcpSibling = Get-InSandboxFetch -SandboxID $sibling.SandboxID -Url $mcpUrl
        Check "the broker token is refused from a sibling sandbox" ($mcpSibling.Status -eq 401) ("status=" + $mcpSibling.Status)
        $mcpLog = Get-ContainerLog "used from outside its sandbox" 300
        Check "the refusal is named in the control-plane log" ($mcpLog.Count -ge 1) ($mcpLog -join " | ")
    }}
else {
    Check "the sandbox's mcp.json holds a broker URL" $false "mcp.json was not staged (is the MCP server enabled for this user?)"
}

# ---------------------------------------------------------------------------
# verdict
# ---------------------------------------------------------------------------
$verdict = "RESULT in_sandbox={0} host_with_proof={1} host_without_proof={2} sibling={3} other_tenant={4} same_token_after_restart={5} revoked_after_pause={6} revoked_after_restart={7} resumed_token={8} mcp_in_sandbox={9} mcp_from_host={10} on_record={11}" -f `
    $inSandbox.Status, $hostWithProof.Status, $hostNoProof.Status, $fromSibling.Status, $fromOther.Status, $afterRestart.Status, `
    $pauseStatus, $revokedAfterRestart.Status, $inSandboxB.Status, $mcpInStatus, $mcpHostStatus, $onRecord
Say ""
Say $verdict
Say ("failures=" + $script:Failures)

$script:Lines | Set-Content -Path $logPath
[pscustomobject]@{
    session_id            = $main.SessionID
    sandbox_a             = $main.SandboxID
    sandbox_b_same_tenant = $sibling.SandboxID
    sandbox_c_other       = $other.SandboxID
    token_a_sha256        = (Get-TokenFingerprint $cfg.Token)
    token_b_sha256        = (Get-TokenFingerprint $cfgB.Token)
    in_sandbox            = $inSandbox.Status
    host_with_proof       = $hostWithProof.Status
    host_without_proof    = $hostNoProof.Status
    sibling               = $fromSibling.Status
    other_tenant          = $fromOther.Status
    same_token_after_restart = $afterRestart.Status
    revoked_after_pause   = $pauseStatus
    revoked_after_restart = $revokedAfterRestart.Status
    resumed_token         = $inSandboxB.Status
    mcp_in_sandbox        = $mcpInStatus
    mcp_from_host         = $mcpHostStatus
    on_record             = $onRecord
    failures              = $script:Failures
} | ConvertTo-Json -Depth 4 | Set-Content -Path (Join-Path $OutDir "summary.json")
Write-Host "artifacts: $OutDir"

if ($script:Failures -gt 0) { exit 1 }
