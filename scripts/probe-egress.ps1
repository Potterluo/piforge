# The tenant-egress probe: on a throwaway isolated stack, prove what each policy
# mode actually does — from inside the sandboxes, not from the configuration.
#
#   pwsh scripts/probe-egress.ps1                 # down + up + probe + down
#   pwsh scripts/probe-egress.ps1 -Keep           # leave the stack running
#   pwsh scripts/probe-egress.ps1 -NoStart        # probe a stack that is already up
#
# What it proves (artifacts in e2e/.artifacts/egress/):
#
#   1. `none` — the tenant bridge was created `--internal`; from inside that
#      sandbox a public fetch fails the way an agent would see it (DNS does not
#      resolve, a literal IP has no route / times out), while the control plane's
#      /healthz answers 200, its /v1/* still answers 401, and the sandbox's own
#      gateway URL answers 200;
#   2. `open` — the same sandbox reaches the same public address, and its bridge
#      is an ordinary one;
#   3. `none` is still a *working* tenant: a real prompt settles (the agent needs
#      the gateway, not the internet);
#   4. the operator can see it: GET /v1/admin/status reports each tenant's
#      effective egress mode.
#
# It is deliberately read-only about the product: everything goes through the HTTP
# API plus `docker exec`/`docker inspect` for the outside view. Nothing here
# prints a provider key, a session token or a sandbox API key.
param(
    [int]$Port = 7723,
    [string]$Project = "pf-egress",
    [string]$PolicyDir = "",
    # Empty means "start the stack": a cold down+up on the egress policy below.
    [switch]$NoStart,
    # Rebuild the three images before starting (after a source change).
    [switch]$Rebuild,
    [switch]$Keep,
    [int]$SettleTimeoutSeconds = 300,
    [string]$Prompt = "请用 bash 运行 echo EGRESS-PROBE，然后只回复 DONE"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($PolicyDir)) { $PolicyDir = Join-Path $root "e2e/.artifacts/egress" }
New-Item -ItemType Directory -Force -Path $PolicyDir | Out-Null
$log = Join-Path $PolicyDir "probe.txt"
Set-Content -Path $log -Value ""
$base = "http://127.0.0.1:$Port"
$script:failures = 0
$stamp = Get-Date -Format 'HHmmss'
$policyPath = Join-Path $PolicyDir "tenant-egress.json"

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

# --- the policy this probe needs ----------------------------------------------
#
# The default is closed, which is what a deployment gets; one account is opened
# explicitly so both modes are measured on the same stack, on the same host, at
# the same time. The admin account is the *default* tenant — its sandbox must be
# closed — and the seeded dev account is the exception.
$policy = @{
    comment = "egress probe: default closed; dev is opened so both modes are measured on one stack"
    default = @{ mode = "none" }
    tenants = @{ "dev@piforge.local" = @{ mode = "open" } }
} | ConvertTo-Json -Depth 6
Set-Content -Path $policyPath -Value $policy -Encoding utf8
Say "policy: $policyPath"
Say ($policy | Out-String).Trim()

if (-not $NoStart) {
    Section "0a. a fresh stack on that policy"
    $up = Join-Path $PSScriptRoot "up-isolated.ps1"
    & $up -Port $Port -Project $Project -EgressFile $policyPath -Down | Out-Null
    if ($Rebuild) {
        & $up -Port $Port -Project $Project -EgressFile $policyPath -Rebuild | Out-Null
    } else {
        & $up -Port $Port -Project $Project -EgressFile $policyPath | Out-Null
    }
    if ($LASTEXITCODE -ne 0) { throw "up-isolated.ps1 failed with exit $LASTEXITCODE" }
}

# --- HTTP with the browser's own rules: cookie jar + double-submit CSRF --------
function New-Client([string]$email, [string]$password) {
    $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    $body = @{ email = $email; password = $password } | ConvertTo-Json
    $login = Invoke-RestMethod -Method Post -Uri "$base/v1/auth/login" -Body $body -ContentType "application/json" -WebSession $session
    $csrf = $login.csrf
    if ([string]::IsNullOrWhiteSpace($csrf)) {
        $cookie = $session.Cookies.GetCookies($base)["pf_csrf"]
        if ($cookie) { $csrf = $cookie.Value }
    }
    return [pscustomobject]@{ Email = $email; Session = $session; Csrf = $csrf }
}

function Api($client, [string]$method, [string]$path, $body) {
    $headers = @{}
    if ($method -ne "GET" -and $client.Csrf) { $headers["X-PF-CSRF"] = $client.Csrf }
    $request = @{
        Method = $method
        Uri = "$base$path"
        WebSession = $client.Session
        Headers = $headers
        SkipHttpErrorCheck = $true
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

function DockerCli([string[]]$arguments) {
    $output = & docker @arguments 2>&1
    return [pscustomobject]@{ Code = $LASTEXITCODE; Out = ($output -join "`n").Trim() }
}

function Invoke-InSandbox([string]$container, [string]$command) {
    $res = & docker exec -u gem $container sh -c $command 2>&1
    return [pscustomobject]@{ Code = $LASTEXITCODE; Out = ($res -join "`n").Trim() }
}

# What the agent sees, in the agent's own words. curl is used when the image ships
# it (the failure message is the one the docs quote); node's fetch is the fallback,
# so a missing binary can never be mistaken for a closed network.
$hasCurl = $false
function HttpProbe([string]$container, [string]$url, [int]$timeoutSeconds = 5) {
    if ($script:hasCurl) {
        $res = Invoke-InSandbox $container "curl -m $timeoutSeconds -sS -o /dev/null -w '%{http_code}' '$url'"
        return [pscustomobject]@{ Tool = "curl"; Code = $res.Code; Out = $res.Out }
    }
    $js = "fetch('$url',{signal:AbortSignal.timeout($($timeoutSeconds*1000))}).then(r=>console.log('status='+r.status)).catch(e=>console.log('error='+(e.name||'')+' '+(e.cause&&e.cause.code||'')))"
    $res = Invoke-InSandbox $container "node -e `"$js`""
    return [pscustomobject]@{ Tool = "node"; Code = $res.Code; Out = $res.Out }
}

# A probe that must *fail*: no HTTP status, and the output says why.
function Blocked([string]$container, [string]$url) {
    $probe = HttpProbe $container $url
    $status = ""
    if ($probe.Out -match "status=(\d+)") { $status = $Matches[1] }
    elseif ($probe.Out -match "(?:^|\n)\s*(\d{3})\s*(?:\n|$)") { $status = $Matches[1] }
    # curl prints 000 when it never got a response; node prints an error with the
    # error's cause code (ENOTFOUND, ECONNREFUSED, ...).
    $ok = ($status -eq "000" -or $status -eq "") -and ($probe.Out -notmatch "status=[23]\d\d")
    return [pscustomobject]@{ Ok = $ok; Out = ($probe.Out -replace "`n", " | "); Code = $probe.Code }
}

# A probe that must *succeed*, with the status code the caller expects.
function Reachable([string]$container, [string]$url, [string]$want = "200") {
    $probe = HttpProbe $container $url
    $status = ""
    if ($probe.Out -match "status=(\d+)") { $status = $Matches[1] }
    elseif ($probe.Out -match "(?:^|\n)\s*(\d{3})\s*(?:\n|$)") { $status = $Matches[1] }
    return [pscustomobject]@{ Ok = ($status -eq $want); Status = $status; Out = ($probe.Out -replace "`n", " | ") }
}

# A public destination that must answer *something* (2xx or 3xx). The exact code is
# the site's business — a 301 from a literal IP is as good a proof of "there is a
# route" as a 200, and pinning 200 would make this probe about the internet's
# opinions rather than about the policy.
function PublicReachable([string]$container, [string]$url) {
    $probe = HttpProbe $container $url
    $status = ""
    if ($probe.Out -match "status=(\d+)") { $status = $Matches[1] }
    elseif ($probe.Out -match "(?:^|\n)\s*(\d{3})\s*(?:\n|$)") { $status = $Matches[1] }
    return [pscustomobject]@{ Ok = ($status -match "^[23]\d\d$"); Status = $status; Out = ($probe.Out -replace "`n", " | ") }
}

# --- 0. the untouched neighbour ------------------------------------------------
$before = DockerCli @("inspect", "--format", "{{.Id}} {{.State.StartedAt}} {{.State.Running}}", "pf-local")
$healthyBefore = try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:7722/healthz").StatusCode } catch { 0 }
Section "0. pf-local (7722) before"
Say "pf-local: $($before.Out)  healthz=$healthyBefore"

# --- 1. two tenants, two policies ---------------------------------------------
Section "1. tenants"
$admin = New-Client "admin@piforge.local" "piforge-admin-123"
$dev = New-Client "dev@piforge.local" "piforge-dev-123"
Say "admin login: csrf present=$(-not [string]::IsNullOrWhiteSpace($admin.Csrf))"
Say "dev login:   csrf present=$(-not [string]::IsNullOrWhiteSpace($dev.Csrf))"

$tenants = @()
foreach ($t in @(
    @{ Name = "closed"; Client = $admin; Mode = "none" },
    @{ Name = "opened"; Client = $dev; Mode = "open" }
)) {
    $projectName = "egress-$($t.Name)-$stamp"
    $made = Api $t.Client "POST" "/v1/projects" @{ name = $projectName; repo_url = ""; default_branch = "main" }
    if ($made.Status -ge 300) { throw "project for $($t.Name): $($made.Status) $($made.Text)" }
    $proj = $made.Body.project
    $sess = Api $t.Client "POST" "/v1/projects/$($proj.id)/sessions" @{ kind = "main"; title = $projectName }
    if ($sess.Status -ge 300) { throw "session for $($t.Name): $($sess.Status) $($sess.Text)" }
    $sb = Api $t.Client "GET" "/v1/sessions/$($sess.Body.session.id)/sandbox" $null
    if ($sb.Status -ne 200) { throw "sandbox for $($t.Name): $($sb.Status) $($sb.Text)" }
    $tenants += [pscustomobject]@{
        Name = $t.Name; Mode = $t.Mode; Client = $t.Client
        ProjectID = $proj.id; SessionID = $sess.Body.session.id; SandboxID = $sb.Body.id
    }
    Say ("{0}: mode={1} project={2} session={3} sandbox={4}" -f $t.Name, $t.Mode, $proj.id, $sess.Body.session.id, $sb.Body.id)
}

Start-Sleep -Seconds 2
$hasCurl = ((Invoke-InSandbox $tenants[0].SandboxID "command -v curl >/dev/null && echo yes").Out -eq "yes")
Say "curl in the sandbox image: $hasCurl"

# --- 2. what Docker was actually told -----------------------------------------
Section "2. the bridges, as Docker sees them"
$bridges = @{}
foreach ($t in $tenants) {
    $net = DockerCli @("inspect", "--format", "{{range `$k, `$v := .NetworkSettings.Networks}}{{println `$k}}{{end}}", $t.SandboxID)
    $name = ($net.Out -split "`n" | Where-Object { $_ } | Select-Object -First 1)
    $internal = DockerCli @("network", "inspect", "--format", "{{.Internal}}", $name)
    $labels = DockerCli @("network", "inspect", "--format", "{{index .Labels `"piforge.egress`"}}", $name)
    $bridges[$t.Name] = [pscustomobject]@{ Name = $name; Internal = $internal.Out; Label = $labels.Out }
    Say ("{0}: network={1} Internal={2} label piforge.egress={3}" -f $t.Name, $name, $internal.Out, $labels.Out)
    Check "tenant $($t.Name) is on a bridge of its own" ($name -like "piforge-t-*") "network=$name"
    if ($t.Mode -eq "none") {
        Check "closed tenant's bridge is internal" ($internal.Out -eq "true") "Internal=$($internal.Out)"
    } else {
        Check "open tenant's bridge is not internal" ($internal.Out -eq "false") "Internal=$($internal.Out)"
    }
    Check "the bridge carries the policy it was created with" ($labels.Out -eq $t.Mode) "label=$($labels.Out) policy=$($t.Mode)"
}
$distinct = ($bridges.Values | Select-Object -ExpandProperty Name -Unique)
Check "the two tenants do not share a bridge" ($distinct.Count -eq 2) "bridges=$($distinct -join ',')"

# --- 3. egress, from inside the sandboxes -------------------------------------
Section "3. egress probes (from inside each sandbox)"
# Plain HTTP and a literal IP on purpose: this host's TLS to some public sites is
# intercepted (curl: (35) unexpected eof), which is a property of the network, not
# of the policy. A 2xx/3xx from either target is proof of a route; the absence of
# any response is proof of none.
$publicUrls = @(
    @{ Label = "http://example.com (DNS + HTTP)"; Url = "http://example.com" },
    @{ Label = "https://1.1.1.1 (literal IP, no DNS)"; Url = "https://1.1.1.1" }
)
foreach ($t in $tenants) {
    $dns = Invoke-InSandbox $t.SandboxID "getent hosts example.com || echo DNS-FAILED"
    Say ("{0}: getent hosts example.com -> {1}" -f $t.Name, ($dns.Out -replace "`n", " / "))
    if ($t.Mode -eq "none") {
        Check "closed tenant cannot resolve a public name" ($dns.Out -match "DNS-FAILED") "getent: $($dns.Out)"
    } else {
        Check "open tenant resolves a public name" ($dns.Out -notmatch "DNS-FAILED") "getent: $($dns.Out)"
    }
    foreach ($p in $publicUrls) {
        if ($t.Mode -eq "none") {
            $got = Blocked $t.SandboxID $p.Url
            Check "closed tenant cannot reach $($p.Label)" $got.Ok "probe: $($got.Out)"
        } else {
            $got = PublicReachable $t.SandboxID $p.Url
            Check "open tenant reaches $($p.Label)" $got.Ok "probe: $($got.Out)"
        }
    }
}

# --- 4. the product still works from inside a closed sandbox -------------------
Section "4. a closed sandbox still reaches the control plane"
foreach ($t in $tenants) {
    $health = Reachable $t.SandboxID "http://piforge:7700/healthz"
    Check "$($t.Name) tenant reaches the control plane /healthz" $health.Ok "probe: $($health.Out)"
    $api = Reachable $t.SandboxID "http://piforge:7700/v1/projects" "401"
    Check "$($t.Name) tenant's control-plane /v1/* is still 401" $api.Ok "probe: $($api.Out)"
}
Say "the gateway path is probed in section 6: an agent writes its models.json when it starts"

# --- 5. the operator can see it ------------------------------------------------
Section "5. GET /v1/admin/status reports the modes"
$status = Api $admin "GET" "/v1/admin/status" $null
$statusJson = Join-Path $PolicyDir "admin-status-egress.json"
$status.Text | Set-Content -Path $statusJson -Encoding utf8
$limits = $status.Body.tenant_limits
Say ("policy block: egress_mode={0} source={1} configured={2} applied={3}" -f `
        $limits.egress_mode, $limits.egress_source, $limits.egress_configured, $limits.egress_applied)
Check "the status reports a configured egress policy" ([bool]$limits.egress_configured) "configured=$($limits.egress_configured)"
Check "the status's default mode is the policy's default" ($limits.egress_mode -eq "none") "egress_mode=$($limits.egress_mode)"
foreach ($t in $tenants) {
    $row = $limits.tenants | Where-Object { $_.email -eq $t.Client.Email } | Select-Object -First 1
    if (-not $row) { Check "the status lists tenant $($t.Name)" $false "no row for $($t.Client.Email)"; continue }
    Say ("tenant row {0}: egress={1} override={2} cpus={3}" -f $row.email, $row.egress, $row.egress_override, $row.cpus)
    Check "the status reports the $($t.Name) tenant's effective mode" ($row.egress -eq $t.Mode) "reported=$($row.egress) want=$($t.Mode)"
}

# --- 6. a real prompt still settles in `none` ----------------------------------
Section "6. a real prompt in the closed tenant"
$closed = $tenants | Where-Object { $_.Mode -eq "none" } | Select-Object -First 1
$started = Get-Date
$send = Api $closed.Client "POST" "/v1/sessions/$($closed.SessionID)/messages" @{
    client_msg_id = [guid]::NewGuid().ToString(); text = $Prompt
}
Say "send: $($send.Status) in $([int]((Get-Date) - $started).TotalMilliseconds) ms"
Check "the closed tenant accepted its prompt" ($send.Status -eq 200) "status=$($send.Status) body=$($send.Body | ConvertTo-Json -Compress)"

$settled = $null
$deadline = (Get-Date).AddSeconds($SettleTimeoutSeconds)
while ((Get-Date) -lt $deadline) {
    $s = Api $closed.Client "GET" "/v1/sessions/$($closed.SessionID)/status" $null
    if ($s.Body.running -eq $false) { $settled = $s.Body; break }
    Start-Sleep -Milliseconds 700
}
Check "the closed tenant's run settled (the gateway was enough)" ($null -ne $settled) `
    "settled=$($null -ne $settled) process_id=$($settled.process_id)"

# The transcript is the other half: a settled run with no message would be a
# hung agent that stopped, not an agent that worked. The export is read from the
# store, so it does not depend on the agent process still being warm. The check
# is for an *assistant* section ("## 助手"), not for the word DONE: the prompt
# itself contains DONE, so matching the whole export would pass on the user's own
# text and prove nothing.
$export = Api $closed.Client "GET" "/v1/sessions/$($closed.SessionID)/export?format=md" $null
if ($export.Status -eq 200) {
    $text = $export.Text
    Say "transcript: $($text.Length) chars, first line: $(($text -split "`n")[0])"
    $assistant = [regex]::Matches($text, "(?s)## 助手\s*\n+(.+?)(?=\n## |\z)")
    $answered = @($assistant | Where-Object { $_.Groups[1].Value.Trim().Length -gt 0 })
    $answerText = if ($answered.Count -gt 0) { $answered[0].Groups[1].Value.Trim() } else { "" }
    Say "assistant answer: $(if ($answerText.Length -gt 200) { $answerText.Substring(0, 200) + '…' } else { $answerText })"
    Check "the closed tenant's agent answered" ($answered.Count -gt 0) "assistant sections=$($answered.Count)"
} else {
    Check "the closed tenant's transcript is readable" $false "status=$($export.Status)"
}

# The gateway path, from the agent's own models.json: this is what a prompt takes,
# and the reason "none" is not "offline". The file exists because the agent ran.
# The check is a *real completion* through it, from inside the sandbox — the
# literal "its own /gw/llm/... returns 200", not merely "the socket opened".
$models = Invoke-InSandbox $closed.SandboxID "find /workspace/.piforge/agents -name models.json -print -quit"
if ($models.Out) {
    $raw = Invoke-InSandbox $closed.SandboxID "cat $($models.Out)"
    $baseUrl = ([regex]::Match($raw.Out, '"baseUrl"\s*:\s*"([^"]+)"')).Groups[1].Value
    $model = ([regex]::Match($raw.Out, '"id"\s*:\s*"([^"]+)"')).Groups[1].Value
    $api = ([regex]::Match($raw.Out, '"api"\s*:\s*"([^"]+)"')).Groups[1].Value
    # The token is per session and lives in the sandbox; it is still a credential,
    # so the log names the path and not the value.
    $shownUrl = $baseUrl -replace "/gw/llm/[^/]+", "/gw/llm/<token>"
    if ($baseUrl -and $model) {
        $gwPath = if ($api -like "anthropic*") { "/messages" } else { "/chat/completions" }
        $gwScript = @"
#!/bin/sh
curl -m 30 -sS -w '\nstatus=%{http_code}\n' \
  -H 'content-type: application/json' \
  -d '{"model":"$model","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}' \
  "`$GW$gwPath"
"@
        $gwFile = Join-Path $env:TEMP "piforge-egress-gw.sh"
        [System.IO.File]::WriteAllText($gwFile, $gwScript.Replace("`r`n", "`n"))
        & docker cp $gwFile "$($closed.SandboxID):/tmp/egress-gw.sh" *> $null
        $res = & docker exec -u gem -e "GW=$baseUrl" $closed.SandboxID sh /tmp/egress-gw.sh 2>&1
        $out = ($res -join "`n")
        $status = ([regex]::Match($out, "status=(\d+)")).Groups[1].Value
        Say "gateway completion: $shownUrl$gwPath -> status=$status"
        if ($status -ne "200") {
            # The body is the gateway's own words. A 400 with
            # "provider ... has no key configured" means the stack was seeded
            # without one, which is the probe's configuration, not the policy.
            $body = ($out -replace 'status=\d+', '').Trim()
            Say ("gateway body: " + ($body -replace "`n", ' | '))
        }
        Check "the closed tenant's own /gw/llm path returns 200" ($status -eq "200") "url=$shownUrl$gwPath status=$status"
    } else {
        Check "the closed tenant's own /gw/llm path returns 200" $false "no baseUrl/model in models.json"
    }
} else {
    Check "the closed tenant's own /gw/llm path returns 200" $false "no models.json in the sandbox"
}

# --- 7. the untouched neighbour, again ----------------------------------------
Section "7. pf-local (7722) after"
$after = DockerCli @("inspect", "--format", "{{.Id}} {{.State.StartedAt}} {{.State.Running}}", "pf-local")
$healthyAfter = try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:7722/healthz").StatusCode } catch { 0 }
Say "pf-local: $($after.Out)  healthz=$healthyAfter"
Check "pf-local is the same container" ($before.Out -eq $after.Out) "before='$($before.Out)' after='$($after.Out)'"
Check "pf-local is still healthy" ($healthyAfter -eq 200) "healthz=$healthyAfter"

Section "summary"
Say "failures: $script:failures"
Say "log: $log"
if (-not $Keep -and -not $NoStart) {
    Say ""
    Say "tearing the stack down (pass -Keep to leave it running)"
    & (Join-Path $PSScriptRoot "up-isolated.ps1") -Port $Port -Project $Project -EgressFile $policyPath -Down | Out-Null
}
if ($script:failures -gt 0) { exit 1 }
exit 0
