# The multi-tenancy soak: four tenants, one project and one run each, at the same
# time on one host — then the two claims the acceptance pass left open are probed
# from inside the sandboxes themselves.
#
#   pwsh scripts/soak-tenancy.ps1 -Port 7723
#
# What it proves (and writes to e2e/.artifacts/tenancy/):
#
#   1. isolation — from tenant A's sandbox, tenant B's sandbox port 8080 is
#      unreachable by name *and* by address, while A's own gateway path answers
#      and A's control-plane /v1/* still answers 401;
#   2. quota — every sandbox's CPU/memory/process limits are the tenant's policy,
#      read back from Docker rather than from the configuration, and a tenant at
#      its sandbox cap is refused with a typed error naming the limit;
#   3. audit — the tool calls those four runs made are queryable by an admin
#      across tenants, and exportable as CSV.
#
# It is deliberately read-only about the product: everything goes through the
# HTTP API, plus `docker exec`/`docker inspect` for the outside view. Nothing here
# prints a provider key, a session token or a sandbox API key.
param(
    [int]$Port = 7723,
    [string]$Project = "pf-tenancy",
    [int]$Tenants = 4,
    [string]$Out = "",
    # Empty means a prompt that exercises the tool path: the audit trail is only
    # meaningful if the runs actually called a tool.
    [string]$Prompt = "",
    [int]$SettleTimeoutSeconds = 240
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($Out)) { $Out = Join-Path $root "e2e/.artifacts/tenancy" }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$log = Join-Path $Out "soak.txt"
Set-Content -Path $log -Value ""
$base = "http://127.0.0.1:$Port"
$script:failures = 0
$stamp = Get-Date -Format 'HHmmss'

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
    # `$args` is a PowerShell automatic variable and splatting an assignment to it
    # silently becomes positional arguments; the request has its own name.
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

# The endpoint answers the sandbox itself (id, state, timestamps), not an
# envelope; the caller is the owner, so the admin cookie works for any session.
function Get-Sandbox($client, [string]$sessionId) {
    # The owner's cookie: the route resolves the session per caller, so an admin
    # asking about another tenant's session correctly gets 404.
    $res = Api $client "GET" "/v1/sessions/$sessionId/sandbox" $null
    if ($res.Status -ne 200) { return $null }
    return $res.Body
}

function Invoke-InSandbox([string]$container, [string]$command) {
    $res = & docker exec -u gem $container sh -c $command 2>&1
    return [pscustomobject]@{ Code = $LASTEXITCODE; Out = ($res -join "`n").Trim() }
}

# A probe that only says whether an HTTP endpoint answered. `node` is used because
# the sandbox image ships it for the agent; curl is not guaranteed and would make
# a *failed* probe indistinguishable from a missing binary.
function Invoke-HttpProbe([string]$container, [string]$url) {
    $js = "fetch('$url',{signal:AbortSignal.timeout(4000)}).then(r=>console.log('status='+r.status)).catch(e=>console.log('error='+(e.name||'')+' '+(e.cause&&e.cause.code||'')))"
    $res = Invoke-InSandbox $container "node -e `"$js`""
    return $res.Out
}

# --- 0. the untouched neighbour ------------------------------------------------
$before = DockerCli @("inspect", "--format", "{{.Id}} {{.State.StartedAt}} {{.State.Running}}", "pf-local")
$healthyBefore = try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:7722/healthz").StatusCode } catch { 0 }
Section "0. pf-local (7722) before"
Say "pf-local: $($before.Out)  healthz=$healthyBefore"

# --- 1. admin + four tenants ---------------------------------------------------
Section "1. tenants"
$script:admin = New-Client "admin@piforge.local" "piforge-admin-123"
Say "admin login: csrf present=$(-not [string]::IsNullOrWhiteSpace($script:admin.Csrf))"

$tenants_ = @()
for ($i = 1; $i -le $Tenants; $i++) {
    $email = "soak$i@piforge.local"
    $password = "soak-pass-$i-123"
    $created = Api $script:admin "POST" "/v1/admin/users" @{
        email = $email; password = $password; display_name = "soak $i"; role = "user"
    }
    if ($created.Status -ne 201 -and $created.Status -ne 409) {
        throw "creating ${email} failed: $($created.Status) $($created.Text)"
    }
    $client = New-Client $email $password
    # A name per run: the soak may be re-run against the same stack, and a name
    # collision would otherwise read as "the product refused a project".
    $projectName = "soak-$i-$stamp"
    $made = Api $client "POST" "/v1/projects" @{ name = $projectName; repo_url = ""; default_branch = "main" }
    if ($made.Status -ge 300) { throw "project for ${email}: $($made.Status) $($made.Text)" }
    $proj = $made.Body.project
    $sess = Api $client "POST" "/v1/projects/$($proj.id)/sessions" @{ kind = "main"; title = "soak $i" }
    if ($sess.Status -ge 300) { throw "session for ${email}: $($sess.Status) $($sess.Text)" }
    $tenants_ += [pscustomobject]@{ Index = $i; Email = $email; Client = $client; Project = $proj; SessionId = $sess.Body.session.id }
    Say "tenant ${i}: ${email} project=$($proj.id) session=$($sess.Body.session.id)"
}

# --- 2. one run each, at the same time ----------------------------------------
Section "2. concurrent runs"
$sends = @()
foreach ($t in $tenants_) {
    $started = Get-Date
    $text = if ($Prompt) { $Prompt } else { "请用 bash 运行 echo SOAK-$($t.Index)，然后只回复 DONE" }
    $send = Api $t.Client "POST" "/v1/sessions/$($t.SessionId)/messages" @{
        client_msg_id = [guid]::NewGuid().ToString(); text = $text
    }
    $sends += [pscustomobject]@{ Tenant = $t; Status = $send.Status; Ms = [int]((Get-Date) - $started).TotalMilliseconds; Body = $send.Body }
    Say "tenant $($t.Index) send: $($send.Status) in $(((Get-Date) - $started).TotalMilliseconds) ms"
}
foreach ($s in $sends) {
    Check "tenant $($s.Tenant.Index) accepted its prompt" ($s.Status -eq 200) "status=$($s.Status) body=$($s.Body | ConvertTo-Json -Compress)"
}

$settled = @{}
$deadline = (Get-Date).AddSeconds($SettleTimeoutSeconds)
while ((Get-Date) -lt $deadline) {
    $pending = @()
    foreach ($t in $tenants_) {
        if ($settled.ContainsKey($t.SessionId)) { continue }
        $status = Api $t.Client "GET" "/v1/sessions/$($t.SessionId)/status" $null
        if ($status.Body.running -eq $false) {
            $settled[$t.SessionId] = $status.Body
            Say "tenant $($t.Index) settled (process_id=$($status.Body.process_id))"
        }
        else { $pending += $t.Index }
    }
    if ($settled.Count -eq $tenants_.Count) { break }
    Start-Sleep -Milliseconds 700
}
foreach ($t in $tenants_) {
    Check "tenant $($t.Index) run settled" $settled.ContainsKey($t.SessionId) "settled=$($settled.ContainsKey($t.SessionId))"
}

# --- 3. the sandboxes as Docker sees them -------------------------------------
Section "3. sandboxes, networks and limits"
$policy = Api $script:admin "GET" "/v1/admin/status" $null
Say "policy: source=$($policy.Body.tenant_limits.source) network=$($policy.Body.tenant_limits.network_mode) default_max_sandboxes=$($policy.Body.tenant_limits.max_sandboxes) enforced=$($policy.Body.tenant_limits.enforced)"
$policyRows = $policy.Body.tenant_limits.tenants
$sandboxes = @()
foreach ($t in $tenants_) {
    $sb = Get-Sandbox $t.Client $t.SessionId
    if (-not $sb) { Check "tenant $($t.Index) has a sandbox" $false "no sandbox"; continue }
    $info = DockerCli @("inspect", "--format", "{{.Name}} {{.HostConfig.NetworkMode}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{if .HostConfig.PidsLimit}}{{.HostConfig.PidsLimit}}{{else}}0{{end}}", $sb.id)
    $name, $mode, $nano, $memory, $pids = $info.Out -split "\s+"
    $ip = DockerCli @("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", $sb.id)
    $row = $policyRows | Where-Object { $_.email -eq $t.Email } | Select-Object -First 1
    $sandboxes += [pscustomobject]@{
        Tenant = $t; Id = $sb.id; Name = $name; Network = $mode
        NanoCpus = [int64]$nano; MemoryBytes = [int64]$memory; Pids = [int]$pids
        State = $sb.state; Ip = $ip.Out; Row = $row
    }
    Say ("tenant {0} sandbox: {1} network={2} cpus={3} memory={4}MiB pids={5} state={6} ip={7}" -f `
            $t.Index, $name, $mode, ([double]$nano / 1e9), ([int64]$memory / 1MB), $pids, $sb.state, $ip.Out)
    if ($row) {
        Say ("   policy row: cpus={0} memory={1} pids={2} max_sandboxes={3} sandboxes={4} override={5}" -f `
                $row.cpus, $row.memory, $row.pids, $row.max_sandboxes, $row.sandboxes, $row.override)
    }
}

foreach ($s in $sandboxes) {
    Check "tenant $($s.Tenant.Index) sandbox is on a per-tenant bridge" ($s.Network -like "piforge-t-*") "network=$($s.Network)"
    Check "tenant $($s.Tenant.Index) CPU limit applied" ($s.NanoCpus -gt 0) "cpus=$([double]$s.NanoCpus / 1e9)"
    Check "tenant $($s.Tenant.Index) memory limit applied" ($s.MemoryBytes -gt 0) "memory=$([int64]$s.MemoryBytes / 1MB)MiB"
    Check "tenant $($s.Tenant.Index) pids limit applied" ($s.Pids -gt 0) "pids=$($s.Pids)"
    if ($s.Row) {
        # The container is the authority, the policy is the intention: they have to
        # agree, or the admin page is describing a sandbox nobody is running.
        $rowCpus = if ($s.Row.cpus) { [double]$s.Row.cpus } else { 0 }
        $rowMem = 0
        if ($s.Row.memory) {
            $m = [regex]::Match($s.Row.memory, '^(\d+(?:\.\d+)?)([bkmg]?)')
            if ($m.Success) {
                $mult = switch ($m.Groups[2].Value) { "g" { 1GB } "m" { 1MB } "k" { 1KB } default { 1 } }
                $rowMem = [int64]([double]$m.Groups[1].Value * $mult)
            }
        }
        Check "tenant $($s.Tenant.Index) limits match the policy" `
        ([math]::Abs(([double]$s.NanoCpus / 1e9) - $rowCpus) -lt 0.001 -and [math]::Abs([int64]$s.MemoryBytes - $rowMem) -le 1MB -and $s.Pids -eq $s.Row.pids) `
            "docker=$([double]$s.NanoCpus / 1e9)cpu/$([int64]$s.MemoryBytes / 1MB)MiB/$($s.Pids)pids policy=$($s.Row.cpus)/$($s.Row.memory)/$($s.Row.pids)"
    }
}
$distinct = ($sandboxes | Select-Object -ExpandProperty Network -Unique)
Check "every tenant got its own bridge" ($distinct.Count -eq $sandboxes.Count) "bridges=$($distinct -join ',')"
$distinctLimits = ($sandboxes | ForEach-Object { "$($_.NanoCpus)/$($_.MemoryBytes)/$($_.Pids)" } | Select-Object -Unique)
Check "the limits are per tenant, not one deployment-wide value" ($distinctLimits.Count -gt 1) "distinct limit sets=$($distinctLimits -join ' | ')"

$bridges = DockerCli @("network", "ls", "--filter", "label=piforge.tenant", "--format", "{{.Name}}")
Say "labelled tenant bridges: $($bridges.Out -replace "`n", ' ')"
$sandboxOnShared = @()
foreach ($s in $sandboxes) {
    $on = DockerCli @("inspect", "--format", "{{range `$k, `$v := .NetworkSettings.Networks}}{{println `$k}}{{end}}", $s.Id)
    if ($on.Out -match "piforge-sandboxes") { $sandboxOnShared += $s.Name }
    Say "tenant $($s.Tenant.Index) networks: $($on.Out -replace "`n", ' ')"
}
Check "no sandbox is on the shared sandbox bridge" ($sandboxOnShared.Count -eq 0) "on shared: $($sandboxOnShared -join ',')"

# --- 4. isolation, from inside the sandboxes ----------------------------------
Section "4. isolation probes (from tenant 1's sandbox)"
$attacker = $sandboxes[0]
$victims = $sandboxes | Select-Object -Skip 1
foreach ($v in $victims) {
    $byName = Invoke-HttpProbe $attacker.Id "http://$($v.Name.TrimStart('/')):8080/"
    Check "A cannot reach B($($v.Tenant.Index)) by name" ($byName -notmatch "status=") "probe: $byName"

    $victimIp = ($v.Ip -split "\s+" | Where-Object { $_ } | Select-Object -First 1)
    if ($victimIp) {
        $byIp = Invoke-HttpProbe $attacker.Id "http://$($victimIp):8080/"
        Check "A cannot reach B($($v.Tenant.Index)) by address $victimIp" ($byIp -notmatch "status=") "probe: $byIp"
    }
    else {
        Say "tenant $($v.Tenant.Index) has no address on its bridge (unexpected)"
    }
}

# The sandbox must still reach the gateway it is supposed to reach: its own
# per-session model token, read from the agent's own models.json.
$models = Invoke-InSandbox $attacker.Id "find /workspace/.piforge/agents -name models.json -print -quit"
if ($models.Out) {
    $raw = Invoke-InSandbox $attacker.Id "cat $($models.Out)"
    $baseUrl = ([regex]::Match($raw.Out, '"baseUrl"\s*:\s*"([^"]+)"')).Groups[1].Value
    if ($baseUrl) {
        $gw = Invoke-HttpProbe $attacker.Id "$($baseUrl.TrimEnd('/'))/models"
        Check "A reaches its own gateway path" ($gw -match "status=2\d\d") "url=$baseUrl probe=$gw"
    }
    else { Check "A reaches its own gateway path" $false "no baseUrl in $($models.Out)" }
}
else { Check "A reaches its own gateway path" $false "no models.json in the sandbox" }

$cpHealth = Invoke-HttpProbe $attacker.Id "http://piforge:7700/healthz"
Check "A still reaches the control plane health endpoint" ($cpHealth -match "status=200") "probe: $cpHealth"
$cpApi = Invoke-HttpProbe $attacker.Id "http://piforge:7700/v1/projects"
Check "A's control-plane /v1/* is still 401" ($cpApi -match "status=401") "probe: $cpApi"

# --- 5. the concurrent-sandbox cap -------------------------------------------
Section "5. per-tenant sandbox cap"
$limit = $policy.Body.tenant_limits.max_sandboxes
Say "policy: default max_sandboxes=$limit source=$($policy.Body.tenant_limits.source) network=$($policy.Body.tenant_limits.network_mode)"
$first = $tenants_[0]
$refused = $null
$extra = @()
# A session gets its sandbox when it is created (provisioning runs there), so the
# cap is reached without spending a model turn per probe.
for ($i = 1; $i -le ([math]::Max($limit, 1) + 1); $i++) {
    $name = "cap-probe-$i-$stamp"
    $made = Api $first.Client "POST" "/v1/projects" @{ name = $name; repo_url = ""; default_branch = "main" }
    if ($made.Status -ge 300) { Say "cap probe ${i}: project creation refused $($made.Status)"; break }
    $sess = Api $first.Client "POST" "/v1/projects/$($made.Body.project.id)/sessions" @{ kind = "main"; title = $name }
    Say "cap probe ${i}: session -> $($sess.Status) $($sess.Body.code)"
    if ($sess.Status -ge 300) { $refused = $sess; break }
    $extra += $sess.Body.session.id
}
Say "sandboxes created by the probes: $($extra.Count); tenant 1 now holds $($extra.Count + 1)"
if ($refused) {
    $code = $refused.Body.error.code
    $message = $refused.Body.error.message
    $details = $refused.Body.error.details
    Say "refusal: status=$($refused.Status) code=$code details=$($details | ConvertTo-Json -Compress)"
    Check "a tenant over its cap is refused" ($code -eq "tenant_sandbox_limit") "code=$code status=$($refused.Status)"
    $named = $message -match "$limit"
    Check "the refusal names the limit" $named "message=$message"
    Check "the refusal reports the limit and the current count" `
    ($details.limit -eq $limit -and $details.current -eq $limit) "details=$($details | ConvertTo-Json -Compress)"
}
else {
    Check "a tenant over its cap is refused" $false "no refusal after $($extra.Count) extra sandboxes (limit=$limit)"
}

# --- 6. the audit trail -------------------------------------------------------
Section "6. audit trail"
$audit = Api $script:admin "GET" "/v1/admin/audit?limit=20" $null
if ($audit.Status -eq 200) {
    Say "audit total=$($audit.Body.total) returned=$($audit.Body.calls.Count)"
    $tools = ($audit.Body.calls | Select-Object -ExpandProperty tool -Unique) -join ","
    Say "tools: $tools"
    $owners = ($audit.Body.calls | Select-Object -ExpandProperty user_email -Unique) -join ","
    Say "owners on this page: $owners"
    Check "the audit trail has the soak runs' tool calls" ($audit.Body.total -gt 0) "total=$($audit.Body.total)"
    $soakOwners = @($audit.Body.calls | Where-Object { $_.user_email -like "soak*" })
    Check "audit rows name the tenants" ($soakOwners.Count -gt 0) "soak rows=$($soakOwners.Count)"
    $withProject = @($audit.Body.calls | Where-Object { $_.project_id })
    Check "audit rows carry the project" ($withProject.Count -eq $audit.Body.calls.Count) "rows with project=$($withProject.Count)/$($audit.Body.calls.Count)"
}
else { Check "the audit trail answers" $false "status=$($audit.Status)" }

$csvPath = Join-Path $Out "audit.csv"
$csv = Invoke-WebRequest -Method Get -Uri "$base/v1/admin/audit?format=csv&limit=50" -WebSession $script:admin.Session -Headers @{} -SkipHttpErrorCheck
$csv.Content | Set-Content -Path $csvPath -Encoding utf8
$csvLines = Get-Content $csvPath
Say "csv: $($csvLines.Count) lines, header=$($csvLines[0])"
Check "CSV export is downloadable" ($csv.StatusCode -eq 200 -and $csvLines[0] -match "started_at,ended_at") "status=$($csv.StatusCode) file=$csvPath"

# --- 7. the untouched neighbour, again ---------------------------------------
Section "7. pf-local (7722) after"
$after = DockerCli @("inspect", "--format", "{{.Id}} {{.State.StartedAt}} {{.State.Running}}", "pf-local")
$healthyAfter = try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:7722/healthz").StatusCode } catch { 0 }
Say "pf-local: $($after.Out)  healthz=$healthyAfter"
Check "pf-local is the same container" ($before.Out -eq $after.Out) "before='$($before.Out)' after='$($after.Out)'"
Check "pf-local is still healthy" ($healthyAfter -eq 200) "healthz=$healthyAfter"

Section "summary"
Say "failures: $script:failures"
Say "log: $log"
if ($script:failures -gt 0) { exit 1 }
exit 0
