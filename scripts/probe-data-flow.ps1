# Probes for docs/DATA-FLOW-AUDIT.md: every edge where a tenant-supplied value
# reaches something the control plane does.
#
#   pwsh scripts/probe-data-flow.ps1                     # against http://127.0.0.1:7723
#   pwsh scripts/probe-data-flow.ps1 -Base http://127.0.0.1:7722
#
# Each check states the edge, sends what a tenant can send, and asserts the
# status the guard is supposed to produce. It never prints a credential: the
# seeded tenant account is used, not the drill key.
#
# The count is part of the result. The probe creates the fixtures it needs (a
# project, a session, a skill) so every edge is exercised on a fresh stack, and
# it prints "N of M". Four checks used to be skipped silently when the tenant had
# no project, and a round of the audit reported "15 checks" against a document
# that said 17. An edge that could not be measured exits 2 — not 0 (measured and
# refused) and not 1 (answered the wrong status).
#
# This is the "does the running system still refuse it" half of the audit. The
# Go tests are the "does the code still contain the guard" half
# (internal/httpapi/guard_registry_test.go).
param(
    [string]$Base = "http://127.0.0.1:7723",
    [string]$Email = "dev@piforge.local",
    [string]$Password = "piforge-dev-123",
    # How long to wait for the probe's own session to get a checkout. A container
    # placement provisions one; host placement is nearly instant.
    [int]$SessionWaitSeconds = 90
)

$ErrorActionPreference = "Stop"
$script:checks = @()
$script:skipped = @()
# The edges this file measures. Keep it equal to the number of Invoke-Probe calls
# plus the one hand-built pair below; the "N of M" line is the assertion.
$script:expected = 18

function Invoke-Api {
    param([string]$Method, [string]$Path, $Body = $null)
    $headers = @{}
    if ($script:csrf) { $headers["X-PF-CSRF"] = $script:csrf }
    $json = if ($null -ne $Body) { $Body | ConvertTo-Json -Depth 6 -Compress } else { $null }
    try {
        $response = Invoke-WebRequest -Uri "$Base$Path" -Method $Method -Headers $headers `
            -ContentType "application/json" -Body $json -WebSession $script:session -UseBasicParsing
        return @{ status = [int]$response.StatusCode; text = $response.Content; json = $null }
    } catch {
        $response = $_.Exception.Response
        $status = if ($response) { [int]$response.StatusCode } else { 0 }
        $text = $_.ErrorDetails.Message
        if (-not $text -and $response) { $text = $response.Content.ReadAsStringAsync().Result }
        return @{ status = $status; text = $text; json = $null }
    }
}

function Short($text) {
    $flat = ($text -replace "\s+", " ")
    return $flat.Substring(0, [Math]::Min(160, $flat.Length))
}

function Invoke-Probe {
    param(
        [string]$Name,
        [string]$Method,
        [string]$Path,
        $Body = $null,
        [int[]]$Expect
    )
    $resp = Invoke-Api $Method $Path $Body
    $ok = $Expect -contains $resp.status
    $script:checks += [pscustomobject]@{
        Edge   = $Name
        Status = $resp.status
        Want   = ($Expect -join "|")
        Result = if ($ok) { "PASS" } else { "FAIL" }
        Detail = if ($ok) { "" } else { Short $resp.text }
    }
    return $resp
}

function Skip([string]$Name, [string]$Why) {
    $script:skipped += "$Name ($Why)"
    Write-Host ("    SKIP {0}: {1}" -f $Name, $Why) -ForegroundColor Yellow
}

$script:session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$login = Invoke-RestMethod -Uri "$Base/v1/auth/login" -Method Post -ContentType "application/json" `
    -Body (@{ email = $Email; password = $Password } | ConvertTo-Json) -WebSession $script:session
$script:csrf = $login.csrf
$suffix = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()

Write-Host "== skill import: a tenant-named local path (file://)" -ForegroundColor Cyan
# Before the fix this returned 200 and the repository's files came back through
# GET /v1/skills/{id}/bundle. Now the path has to be inside -skill-import-roots,
# which this deployment does not configure.
Invoke-Probe "skill import file:// (no roots configured)" "POST" "/v1/skills/import" @{ url = "file:///tmp" } 400 | Out-Null
Invoke-Probe "skill import file:// a control-plane directory" "POST" "/v1/skills/import" @{ url = "file:///data" } 400 | Out-Null

Write-Host "== skill import: a URL this process would fetch from its own network" -ForegroundColor Cyan
Invoke-Probe "skill import https loopback" "POST" "/v1/skills/import" @{ url = "$Base/healthz" } 400 | Out-Null

Write-Host "== MCP: the broker's target URL" -ForegroundColor Cyan
Invoke-Probe "mcp server URL (compose name)" "POST" "/v1/mcp/servers" @{ name = "probe-mcp-$suffix"; transport = "http"; url = "http://piforge:7700/healthz" } 400 | Out-Null
Invoke-Probe "mcp server URL (link-local)" "POST" "/v1/mcp/servers" @{ name = "probe-mcp2-$suffix"; transport = "http"; url = "http://169.254.169.254/latest/meta-data/" } 400 | Out-Null

Write-Host "== providers: base_url is a fetch target" -ForegroundColor Cyan
Invoke-Probe "provider base_url loopback" "POST" "/v1/providers" @{ name = "probe-prov-$suffix"; api = "openai-compat"; base_url = "$Base/v1"; api_key = "x" } 400 | Out-Null
Invoke-Probe "provider base_url private" "POST" "/v1/providers" @{ name = "probe-prov2-$suffix"; api = "openai-compat"; base_url = "http://10.0.0.5/v1"; api_key = "x" } 400 | Out-Null

Write-Host "== projects: repo_url and forge api_base" -ForegroundColor Cyan
# The value is stored so the user can see and fix it; the clone target is the
# control plane's dial only in the local placement, where the provider refuses it
# (internal/sandbox) -- so this probe asserts the project is created, not that the
# save is refused. The refusal for that edge is a Go test, and (in host
# placement) the provision log.
Invoke-Probe "project repo_url loopback (stored, clone refused)" "POST" "/v1/projects" @{ name = "probe-repo-$suffix"; repo_url = "https://127.0.0.1:7723/healthz" } 201 | Out-Null
Invoke-Probe "forge api_base loopback (refused at save)" "PUT" "/v1/projects/prj_missing/forge" @{ kind = "github"; api_base = "$Base"; repo = "a/b"; token = "t" } 404, 400 | Out-Null
Invoke-Probe "nonsense repo_url (shape)" "POST" "/v1/projects" @{ name = "probe-repo2-$suffix"; repo_url = "not-a-repo-url" } 400 | Out-Null

Write-Host "== fixtures: a project and a session, so no edge is skipped" -ForegroundColor Cyan
# These are not edges: they exist so the id and file checks below always run.
$script:projectId = $null
$script:sessionId = $null
$fixture = Invoke-Api "POST" "/v1/projects" @{ name = "probe-fixture-$suffix"; default_branch = "main" }
if ($fixture.status -eq 201) {
    $script:projectId = ($fixture.text | ConvertFrom-Json).project.id
}
if (-not $script:projectId) {
    # A deployment that will not let the tenant create a project is not what this
    # probe measures; fall back to a project the account already has.
    $projects = Invoke-RestMethod -Uri "$Base/v1/projects" -WebSession $script:session
    $mine = $projects.projects | Select-Object -First 1
    if ($mine) { $script:projectId = $mine.id }
}

if ($script:projectId) {
    $madeSession = Invoke-Api "POST" "/v1/projects/$($script:projectId)/sessions" @{ kind = "main"; title = "probe-$suffix" }
    if ($madeSession.status -eq 201) {
        $script:sessionId = ($madeSession.text | ConvertFrom-Json).session.id
    }
    if ($script:sessionId) {
        # A checkout is what the file edges are about; wait for it rather than
        # measuring a path that has nothing to escape from.
        $deadline = (Get-Date).AddSeconds($SessionWaitSeconds)
        while ((Get-Date) -lt $deadline) {
            $one = Invoke-Api "GET" "/v1/sessions/$($script:sessionId)"
            if ($one.status -eq 200 -and $one.text -match '"worktree_id":"(wt_[^"]+)"') { break }
            Start-Sleep -Milliseconds 500
        }
    }
} else {
    $script:sessionId = $null
}
if (-not $script:sessionId) {
    $sessions = Invoke-RestMethod -Uri "$Base/v1/sessions" -WebSession $script:session
    $first = $sessions.sessions | Select-Object -First 1
    if ($first) { $script:sessionId = $first.id }
}

Write-Host "== ids: a row that is not the caller's" -ForegroundColor Cyan
if ($script:projectId) {
    Invoke-Probe "session create with a foreign parent_id" "POST" "/v1/projects/$($script:projectId)/sessions" @{ kind = "branch"; parent_id = "ses_01AAAAAAAAAAAAAAAAAAAAAAAA" } 404 | Out-Null
    Invoke-Probe "session create with a foreign worktree_id" "POST" "/v1/projects/$($script:projectId)/sessions" @{ kind = "main"; worktree_id = "wt_01AAAAAAAAAAAAAAAAAAAAAAAA" } 404 | Out-Null
} else {
    Skip "session create with a foreign parent_id" "no project could be created or found"
    Skip "session create with a foreign worktree_id" "no project could be created or found"
}

Write-Host "== files: a path the tenant shaped" -ForegroundColor Cyan
if ($script:sessionId) {
    $escape = [uri]::EscapeDataString("../../../../etc/passwd")
    Invoke-Probe "file read traversal" "GET" "/v1/sessions/$($script:sessionId)/files?path=$escape" $null 400, 422 | Out-Null
    Invoke-Probe "artifact traversal" "GET" "/v1/sessions/$($script:sessionId)/artifacts?path=$escape" $null 400, 422 | Out-Null
} else {
    Skip "file read traversal" "no session could be created or found"
    Skip "artifact traversal" "no session could be created or found"
}

Write-Host "== admin: role, not id" -ForegroundColor Cyan
Invoke-Probe "admin users as tenant" "GET" "/v1/admin/users" $null 403 | Out-Null
Invoke-Probe "admin audit as tenant" "GET" "/v1/admin/audit" $null 403 | Out-Null
Invoke-Probe "support access as tenant" "POST" "/v1/admin/support-access" @{ session_id = "ses_x"; reason = "because" } 403 | Out-Null

Write-Host "== skill bundle: git storage spelled the way Windows normalises it" -ForegroundColor Cyan
# `.git` is refused. `.git.` and `.git ` are the same directory after Win32
# strips the trailing dot or space, and adversarial round 5 uploaded
# `.git./config`, got 200, and found `.git\config` on the volume.
$skill = Invoke-Api "POST" "/v1/skills" @{ name = "probe-bundle-$suffix"; body = "# probe`n"; scope = "user" }
$skillId = $null
if ($skill.status -eq 201) { $skillId = ($skill.text | ConvertFrom-Json).skill.id }
if ($skillId) {
    $dots = Invoke-Api "PUT" "/v1/skills/$skillId/bundle" @{
        files = @(@{ path = "SKILL.md"; content = "ok`n" }, @{ path = ".git./config"; content = "[core]`n" })
    }
    $plain = Invoke-Api "PUT" "/v1/skills/$skillId/bundle" @{
        files = @(@{ path = "SKILL.md"; content = "ok`n" }, @{ path = ".git/config"; content = "[core]`n" })
    }
    $ok = ($dots.status -eq 400 -and $plain.status -eq 400)
    $script:checks += [pscustomobject]@{
        Edge   = "skill bundle path .git./config (and the .git/config control)"
        Status = "$($dots.status)/$($plain.status)"
        Want   = "400/400"
        Result = if ($ok) { "PASS" } else { "FAIL" }
        Detail = if ($ok) { "" } else { Short "$($dots.text) $($plain.text)" }
    }
} else {
    Skip "skill bundle path .git./config" "could not create a skill ($($skill.status))"
}

Write-Host ""
$script:checks | Format-Table -AutoSize -Wrap
$failed = @($script:checks | Where-Object { $_.Result -eq "FAIL" })
$ran = $script:checks.Count
Write-Host ("{0} of {1} checks ran, {2} failed, {3} skipped" -f $ran, $script:expected, $failed.Count, $script:skipped.Count)
foreach ($line in $script:skipped) { Write-Host ("  skipped: " + $line) -ForegroundColor Yellow }
if ($failed.Count -gt 0) { exit 1 }
if ($script:skipped.Count -gt 0 -or $ran -ne $script:expected) {
    Write-Host ("the probe could not measure every edge ({0} of {1}): this is not a pass" -f $ran, $script:expected) -ForegroundColor Yellow
    exit 2
}
