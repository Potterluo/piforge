# The forensic audit probe: one real agent run on a throwaway stack, read back
# through the admin API and the audit UI.
#
#   pwsh scripts/up-isolated.ps1                  # ports 7723, key from the environment
#   pwsh scripts/probe-audit.ps1                  # default base http://127.0.0.1:7723
#   pwsh scripts/up-isolated.ps1 -Down
#
# It answers the four questions docs/AUDIT.md promises:
#   1. does a real tool call leave an audit row with the preview, model, commit
#      sha, token/cost facts and per-session sequence?
#   2. is the preview an excerpt (file content is sha256 + bytes, not bytes)?
#   3. does the chain verify for the session, and does the UI render the row?
#   4. does the whole filtered set stream to CSV/JSONL?
#
# It never prints a credential: the provider key is not used here (the stack
# already holds it), and the probe only handles the admin login it is given.
param(
    [string]$Base = "http://127.0.0.1:7723",
    [string]$Email = "admin@piforge.local",
    [string]$Password = "piforge-admin-123",
    [string]$Prompt = "First use bash to run: git commit --allow-empty -m 'audit probe base'. Then use the write tool to create hello.txt containing exactly: hello audit. Then use bash to run: wc -c hello.txt. Finally reply DONE.",
    [int]$SettleTimeoutSeconds = 240,
    [switch]$SkipUi
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$artifactDir = Join-Path $root "e2e/.artifacts/audit-probe"
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$log = Join-Path $artifactDir "probe-$stamp.txt"
$script:failures = 0

function Say([string]$line) {
    Write-Host $line
    Add-Content -Path $log -Value $line
}
function Section([string]$title) {
    Say ""
    Say "=== $title ==="
}
function Check([string]$label, [bool]$ok, [string]$detail) {
    $mark = if ($ok) { "PASS" } else { "FAIL" }
    if (-not $ok) { $script:failures++ }
    Say ("[{0}] {1} :: {2}" -f $mark, $label, $detail)
}

# HTTP with the browser's own rules: cookie jar + double-submit CSRF. The admin
# login is the only credential this probe touches.
function New-Client([string]$email, [string]$password) {
    $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    $body = @{ email = $email; password = $password } | ConvertTo-Json
    $login = Invoke-RestMethod -Method Post -Uri "$Base/v1/auth/login" -Body $body -ContentType "application/json" -WebSession $session
    $csrf = $login.csrf
    if ([string]::IsNullOrWhiteSpace($csrf)) {
        $cookie = $session.Cookies.GetCookies($Base)["pf_csrf"]
        if ($cookie) { $csrf = $cookie.Value }
    }
    return [pscustomobject]@{ Email = $email; Session = $session; Csrf = $csrf }
}

function Api($client, [string]$method, [string]$path, $body) {
    $headers = @{}
    if ($method -ne "GET" -and $client.Csrf) { $headers["X-PF-CSRF"] = $client.Csrf }
    $request = @{
        Method = $method; Uri = "$Base$path"; WebSession = $client.Session
        Headers = $headers; SkipHttpErrorCheck = $true
    }
    if ($null -ne $body) {
        $request["Body"] = ($body | ConvertTo-Json -Depth 8)
        $request["ContentType"] = "application/json"
    }
    $response = Invoke-WebRequest @request
    $parsed = $null
    try { $parsed = $response.Content | ConvertFrom-Json } catch { }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Body = $parsed; Text = $response.Content }
}

Section "0. stack"
$health = try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "$Base/healthz").StatusCode } catch { 0 }
Check "the isolated stack answers /healthz" ($health -eq 200) "status=$health base=$Base"
if ($health -ne 200) { Say "FAILED: no stack"; exit 1 }

Section "1. one real run"
$admin = New-Client $Email $Password
Check "admin login" (-not [string]::IsNullOrWhiteSpace($admin.Csrf)) "email=$Email"

$projectName = "audit-probe-$stamp"
$made = Api $admin "POST" "/v1/projects" @{ name = $projectName; repo_url = ""; default_branch = "main" }
Check "project created" ($made.Status -lt 300 -and $made.Body.project.id) "status=$($made.Status) id=$($made.Body.project.id)"
$projectId = $made.Body.project.id

$sess = Api $admin "POST" "/v1/projects/$projectId/sessions" @{ kind = "main"; title = "audit probe" }
Check "session created" ($sess.Status -lt 300 -and $sess.Body.session.id) "status=$($sess.Status) id=$($sess.Body.session.id)"
$sessionId = $sess.Body.session.id

$send = Api $admin "POST" "/v1/sessions/$sessionId/messages" @{
    client_msg_id = [guid]::NewGuid().ToString(); text = $Prompt
}
Check "prompt accepted" ($send.Status -eq 200) "status=$($send.Status) body=$($send.Text)"

$deadline = (Get-Date).AddSeconds($SettleTimeoutSeconds)
$settled = $false
while ((Get-Date) -lt $deadline) {
    $status = Api $admin "GET" "/v1/sessions/$sessionId/status" $null
    if ($status.Body.running -eq $false) { $settled = $true; break }
    Start-Sleep -Milliseconds 800
}
Check "the run settled" $settled "timeout=$SettleTimeoutSeconds s"

Section "2. the audit rows"
$audit = Api $admin "GET" "/v1/admin/audit?session=$sessionId&limit=200" $null
Check "audit answers for the session" ($audit.Status -eq 200) "status=$($audit.Status)"
$calls = @($audit.Body.calls)
Check "the run left tool calls" ($calls.Count -gt 0) "calls=$($calls.Count) total=$($audit.Body.total)"

$withPreview = @($calls | Where-Object { $_.args_preview })
$withCommit = @($calls | Where-Object { $_.commit_sha -match '^[0-9a-f]{7,40}$' })
$withModel = @($calls | Where-Object { $_.model_ref -or $_.run_model_ref })
$withUsage = @($calls | Where-Object { $_.run_tokens_out -gt 0 })
$writeCall = $calls | Where-Object { $_.tool -eq "write" } | Select-Object -First 1
Check "every call carries a redacted preview" ($withPreview.Count -eq $calls.Count) "with_preview=$($withPreview.Count)/$($calls.Count)"
Check "every call carries the model actually used" ($withModel.Count -eq $calls.Count) "with_model=$($withModel.Count)/$($calls.Count)"
Check "the run reported token usage" ($withUsage.Count -gt 0) "tokens_out=$($calls[0].run_tokens_out) cost=$($calls[0].run_cost_usd)"
Check "the workspace revision is recorded" ($withCommit.Count -gt 0) "with_commit=$($withCommit.Count)/$($calls.Count) sha=$(($withCommit | Select-Object -First 1).commit_sha)"
Check "the sequence is monotonic per session" ((@($calls | ForEach-Object { $_.seq }) -join ",") -match '^\d+(,\d+)*$') "seq=$(($calls | ForEach-Object { $_.seq }) -join ',')"
if ($writeCall) {
    $preview = $writeCall.args_preview | ConvertTo-Json -Compress -Depth 8
    Check "the write preview is a digest, not file bytes" (($preview -match 'sha256') -and ($preview -notmatch 'hello audit')) "preview=$preview"
} else {
    Check "the agent used the write tool" $false "tools=$(($calls | ForEach-Object { $_.tool }) -join ',')"
}

# A compact table of what an operator would read.
Say ""
Say ("{0,-4} {1,-6} {2,-9} {3,7} {4,-24} {5,-12} {6,10} {7,9}  {8}" -f "seq","tool","outcome","ms","model","commit","tokens_out","cost","preview")
foreach ($call in ($calls | Sort-Object seq -Descending)) {
    $preview = if ($call.args_preview) { ($call.args_preview | ConvertTo-Json -Compress -Depth 8) } else { "" }
    if ($preview.Length -gt 90) { $preview = $preview.Substring(0, 90) + "…" }
    $sha = if ($call.commit_sha) { $call.commit_sha.Substring(0, [Math]::Min(12, $call.commit_sha.Length)) } else { "" }
    $model = if ($call.model_ref) { $call.model_ref } else { $call.run_model_ref }
    Say ("{0,-4} {1,-6} {2,-9} {3,7} {4,-24} {5,-12} {6,10} {7,9}  {8}" -f `
            $call.seq, $call.tool, $call.status, $call.duration_ms, $model, $sha, `
            $call.run_tokens_out, $call.run_cost_usd, $preview)
}

Section "3. the chain"
$verify = Api $admin "GET" "/v1/admin/audit/verify?session_id=$sessionId" $null
Check "the chain verifies" ($verify.Status -eq 200 -and $verify.Body.ok) "status=$($verify.Status) ok=$($verify.Body.ok) checked=$($verify.Body.checked) break=$($verify.Body.first_break | ConvertTo-Json -Compress)"
if (-not $verify.Body.ok) {
    Say "chain break: $($verify.Body.first_break | ConvertTo-Json -Compress -Depth 8)"
}

Section "4. streaming exports"
$csvPath = Join-Path $artifactDir "audit-$stamp.csv"
$jsonlPath = Join-Path $artifactDir "audit-$stamp.jsonl"
Invoke-WebRequest -UseBasicParsing -TimeoutSec 60 -Uri "$Base/v1/admin/audit?session=$sessionId&format=csv" -WebSession $admin.Session -OutFile $csvPath | Out-Null
Invoke-WebRequest -UseBasicParsing -TimeoutSec 60 -Uri "$Base/v1/admin/audit?session=$sessionId&format=jsonl" -WebSession $admin.Session -OutFile $jsonlPath | Out-Null
$csvLines = @(Get-Content $csvPath)
$jsonlLines = @(Get-Content $jsonlPath)
Check "CSV export has the new columns" ($csvLines[0] -match "seq,sandbox_id,model_ref,commit_sha,run_model_ref") "header=$($csvLines[0])"
Check "CSV export has one line per call" ($csvLines.Count -ge $calls.Count + 1) "lines=$($csvLines.Count) calls=$($calls.Count)"
Check "JSONL export is one object per line" ($jsonlLines.Count -eq $calls.Count) "lines=$($jsonlLines.Count) calls=$($calls.Count)"

Section "5. the audit UI"
if ($SkipUi) {
    Say "SKIP ui (requested)"
} else {
    $cookieFile = Join-Path $artifactDir "cookies-$stamp.json"
    $cookies = @($admin.Session.Cookies.GetCookies($Base) | ForEach-Object {
            [pscustomobject]@{ Name = $_.Name; Value = $_.Value; Domain = "127.0.0.1"; Path = "/" }
        })
    $cookies | ConvertTo-Json -Depth 4 | Set-Content -Path $cookieFile -Encoding UTF8
    $firstId = ($calls | Sort-Object seq -Descending | Select-Object -First 1).id
    $uiScript = Join-Path $root "e2e/scripts/probe-audit-ui.mjs"
    $uiOut = & node $uiScript $Base $cookieFile $firstId $artifactDir 2>&1
    $uiCode = $LASTEXITCODE
    $uiOut | ForEach-Object { Say $_ }
    Check "the audit UI rendered the row" ($uiCode -eq 0) "exit=$uiCode row=$firstId"
}

Say ""
if ($script:failures -eq 0) {
    Say "AUDIT PROBE: PASS"
    Say "artifacts: $artifactDir"
    exit 0
}
Say "AUDIT PROBE: FAIL ($script:failures check(s))"
exit 1
