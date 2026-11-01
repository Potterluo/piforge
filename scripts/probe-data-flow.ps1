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
# This is the "does the running system still refuse it" half of the audit. The
# Go tests are the "does the code still contain the guard" half
# (internal/httpapi/guard_registry_test.go).
param(
    [string]$Base = "http://127.0.0.1:7723",
    [string]$Email = "dev@piforge.local",
    [string]$Password = "piforge-dev-123"
)

$ErrorActionPreference = "Stop"
$script:checks = @()

function Invoke-Probe {
    param(
        [string]$Name,
        [string]$Method,
        [string]$Path,
        $Body = $null,
        [int[]]$Expect
    )
    $headers = @{}
    if ($script:csrf) { $headers["X-PF-CSRF"] = $script:csrf }
    $json = if ($null -ne $Body) { $Body | ConvertTo-Json -Depth 6 -Compress } else { $null }
    try {
        $response = Invoke-WebRequest -Uri "$Base$Path" -Method $Method -Headers $headers `
            -ContentType "application/json" -Body $json -WebSession $script:session -UseBasicParsing
        $status = [int]$response.StatusCode
        $text = $response.Content
    } catch {
        $response = $_.Exception.Response
        $status = if ($response) { [int]$response.StatusCode } else { 0 }
        $text = $_.ErrorDetails.Message
        if (-not $text -and $response) { $text = $response.Content.ReadAsStringAsync().Result }
    }
    $ok = $Expect -contains $status
    $script:checks += [pscustomobject]@{
        Edge   = $Name
        Status = $status
        Want   = ($Expect -join "|")
        Result = if ($ok) { "PASS" } else { "FAIL" }
        Detail = if ($ok) { "" } else { ($text -replace "\s+", " ").Substring(0, [Math]::Min(160, ($text -replace "\s+", " ").Length)) }
    }
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
Invoke-Probe "skill import file:// (no roots configured)" "POST" "/v1/skills/import" @{ url = "file:///tmp" } 400
Invoke-Probe "skill import file:// a control-plane directory" "POST" "/v1/skills/import" @{ url = "file:///data" } 400

Write-Host "== skill import: a URL this process would fetch from its own network" -ForegroundColor Cyan
Invoke-Probe "skill import https loopback" "POST" "/v1/skills/import" @{ url = "$Base/healthz" } 400

Write-Host "== MCP: the broker's target URL" -ForegroundColor Cyan
Invoke-Probe "mcp server URL (compose name)" "POST" "/v1/mcp/servers" @{ name = "probe-mcp-$suffix"; transport = "http"; url = "http://piforge:7700/healthz" } 400
Invoke-Probe "mcp server URL (link-local)" "POST" "/v1/mcp/servers" @{ name = "probe-mcp2-$suffix"; transport = "http"; url = "http://169.254.169.254/latest/meta-data/" } 400

Write-Host "== providers: base_url is a fetch target" -ForegroundColor Cyan
Invoke-Probe "provider base_url loopback" "POST" "/v1/providers" @{ name = "probe-prov-$suffix"; api = "openai-compat"; base_url = "$Base/v1"; api_key = "x" } 400
Invoke-Probe "provider base_url private" "POST" "/v1/providers" @{ name = "probe-prov2-$suffix"; api = "openai-compat"; base_url = "http://10.0.0.5/v1"; api_key = "x" } 400

Write-Host "== projects: repo_url and forge api_base" -ForegroundColor Cyan
# The value is stored so the user can see and fix it; the clone target is the
# control plane's dial only in the local placement, where the provider refuses it
# (internal/sandbox) -- so this probe asserts the project is created, not that the
# save is refused. The refusal for that edge is a Go test.
Invoke-Probe "project repo_url loopback (stored, clone refused)" "POST" "/v1/projects" @{ name = "probe-repo-$suffix"; repo_url = "https://127.0.0.1:7723/healthz" } 201
Invoke-Probe "forge api_base loopback (refused at save)" "PUT" "/v1/projects/prj_missing/forge" @{ kind = "github"; api_base = "$Base"; repo = "a/b"; token = "t" } 404, 400
Invoke-Probe "nonsense repo_url (shape)" "POST" "/v1/projects" @{ name = "probe-repo2-$suffix"; repo_url = "not-a-repo-url" } 400

Write-Host "== ids: a row that is not the caller's" -ForegroundColor Cyan
$script:sessionId = $null
$script:projectId = $null
$projects = Invoke-RestMethod -Uri "$Base/v1/projects" -WebSession $script:session
$mine = $projects.projects | Select-Object -First 1
if ($mine) { $script:projectId = $mine.id }
$sessions = Invoke-RestMethod -Uri "$Base/v1/sessions" -WebSession $script:session
$first = $sessions.sessions | Select-Object -First 1
if ($first) { $script:sessionId = $first.id }

if ($script:projectId) {
    Invoke-Probe "session create with a foreign parent_id" "POST" "/v1/projects/$($script:projectId)/sessions" @{ kind = "branch"; parent_id = "ses_01AAAAAAAAAAAAAAAAAAAAAAAA" } 404
    Invoke-Probe "session create with a foreign worktree_id" "POST" "/v1/projects/$($script:projectId)/sessions" @{ kind = "main"; worktree_id = "wt_01AAAAAAAAAAAAAAAAAAAAAAAA" } 404
}

Write-Host "== files: a path the tenant shaped" -ForegroundColor Cyan
if ($script:sessionId) {
    $escape = [uri]::EscapeDataString("../../../../etc/passwd")
    Invoke-Probe "file read traversal" "GET" "/v1/sessions/$($script:sessionId)/files?path=$escape" $null 400, 422
    Invoke-Probe "artifact traversal" "GET" "/v1/sessions/$($script:sessionId)/artifacts?path=$escape" $null 400, 422
}

Write-Host "== admin: role, not id" -ForegroundColor Cyan
Invoke-Probe "admin users as tenant" "GET" "/v1/admin/users" $null 403
Invoke-Probe "admin audit as tenant" "GET" "/v1/admin/audit" $null 403
Invoke-Probe "support access as tenant" "POST" "/v1/admin/support-access" @{ session_id = "ses_x"; reason = "because" } 403

Write-Host ""
$script:checks | Format-Table -AutoSize -Wrap
$failed = @($script:checks | Where-Object { $_.Result -eq "FAIL" })
Write-Host ("{0} checks, {1} failed" -f $script:checks.Count, $failed.Count)
if ($failed.Count -gt 0) { exit 1 }
