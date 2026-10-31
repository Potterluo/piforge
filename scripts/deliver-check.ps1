<#
.SYNOPSIS
    From-zero delivery drill for PiForge.

.DESCRIPTION
    Starts a throw-away PiForge container on a brand-new empty volume, with only
    the port mapping, the seed accounts and a bootstrap model provider. It then
    proves, through the public API and the container itself, that a brand-new
    user can log in, create a project and a session, and get one real agent turn
    that runs code and commits it.

    Two later main-flow steps are covered as well: opening a pull request
    (FR-12.6) against a fake forge that runs inside the drill container, and a
    comment on one line of a file turning into the next message the agent
    receives (FR-12.3). A final guard asserts the drill did not dirty the source
    checkout and left no container or volume behind.

    The provider key is read from PIFORGE_DRILL_KEY and is never printed, never
    written to an artifact and never committed. The script refuses to operate on
    a container named pf-local and only removes the container and volume it
    created itself.

    Evidence (logs, JSON, screenshots) is written under
    e2e/.artifacts/delivery-drill/<run-id>/.

.PARAMETER Image
    The image to drill. Default: piforge:local.

.PARAMETER Port
    Host port to publish the container's 7700 on. Default: 7731.

.PARAMETER ProviderName
    PIFORGE_BOOTSTRAP_PROVIDER name and PIFORGE_DEFAULT_PROVIDER. Default: volc.

.PARAMETER ProviderBaseURL
    OpenAI-compatible base URL of the provider.

.PARAMETER ProviderModel
    Model id the provider serves.

.PARAMETER ContainerName
    Name of the throw-away container. Must not be pf-local. Default: pf-drill.

.PARAMETER VolumeName
    Name of the throw-away volume. Must not exist before the run.
    Default: pf-drill-data.

.PARAMETER ExistingContainer
    The deployment this drill must not disturb. Default: pf-local.

.PARAMETER ExistingPort
    Host port of ExistingContainer, used for its /healthz check. Default: 7722.

.PARAMETER BrowserIdleWaitSeconds
    How long to wait for the sandbox browser to be collected after the last
    viewer detaches. The product's default idle timeout is 60s. Default: 75.

.PARAMETER FakeForgePort
    Port the fake GitHub-dialect forge listens on inside the drill container.
    It is never published to the host. Default: 7799.

.PARAMETER SkipUi
    Skip the Playwright check that the file tree refreshes without a reload.

.EXAMPLE
    $env:PIFORGE_DRILL_KEY = '<key>'
    ./scripts/deliver-check.ps1

.NOTES
    Requires: docker, node (and e2e/node_modules for the UI check), curl.exe.
#>
[CmdletBinding()]
param(
    [string]$Image = "piforge:local",
    [int]$Port = 7731,
    [string]$ProviderName = "volc",
    [string]$ProviderBaseURL = "https://api.example.com/v1",
    [string]$ProviderModel = "deepseek-v4-flash",
    [string]$ContainerName = "pf-drill",
    [string]$VolumeName = "pf-drill-data",
    [string]$ExistingContainer = "pf-local",
    [int]$ExistingPort = 7722,
    [int]$StartupTimeoutSeconds = 120,
    [int]$SettleTimeoutSeconds = 600,
    [int]$BrowserIdleWaitSeconds = 75,
    [int]$FakeForgePort = 7799,
    [switch]$SkipUi
)

$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------------------
# Constants and state
# ---------------------------------------------------------------------------

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$ArtifactRoot = Join-Path $RepoRoot "e2e\.artifacts\delivery-drill"
$RunID = Get-Date -Format "yyyyMMdd-HHmmss"
$ArtifactDir = Join-Path $ArtifactRoot $RunID
if (Test-Path $ArtifactDir) {
    $ArtifactDir = Join-Path $ArtifactRoot ("$RunID-" + [guid]::NewGuid().ToString("N").Substring(0, 4))
}
New-Item -ItemType Directory -Force -Path $ArtifactDir | Out-Null

$BaseUrl = "http://127.0.0.1:$Port"
$Key = $env:PIFORGE_DRILL_KEY
$script:Checks = New-Object 'System.Collections.Generic.List[object]'
$script:Metrics = [ordered]@{}
$script:CreatedContainer = $false
$script:CreatedVolume = $false
$script:Failure = $null
$script:StartedAt = [DateTime]::UtcNow

# Secrets that must never appear in an artifact. The provider key is the only
# one the drill is required to protect; the seed and drill-user passwords are
# redacted too because the product prints seeds once at first boot.
$SeedAdminEmail = "admin@piforge.local"
$SeedUserEmail = "dev@piforge.local"
$SeedAdminPassword = "Seed-" + [guid]::NewGuid().ToString("N").Substring(0, 12) + "!9"
$SeedUserPassword = "Seed-" + [guid]::NewGuid().ToString("N").Substring(0, 12) + "!9"
$NewUserEmail = "drill-$RunID@piforge.local"
$NewUserPassword = "Drill-" + [guid]::NewGuid().ToString("N").Substring(0, 12) + "!9"
$ProjectName = "drill-$RunID"
# The fake forge token is generated per run and belongs to the same protected
# set as the provider key: it goes into a header, is sealed in the database and
# must never reach the console, an artifact or the container log.
$ForgeToken = "ghp_drill_" + [guid]::NewGuid().ToString("N")
$ForgeFailMarker = "drill forge refused: base branch is protected (drill-422)"
$PRBranch = "drill/pull-request"
$PRRejectBranch = "drill/rejected-base"
$ReviewPath = "notes.txt"
$ReviewLine = 2
$ReviewQuote = "theme: dark"
$ReviewBody = "这一行是演练用的行内评论：请只回复你看到了它，不要修改任何文件。"
$PromptText = @'
Create a small Node script named primes.js that prints the first 10 primes, one per line. Run it with node and show the output. Add a simple test: a package.json whose test script is "node --test" and a node:test file that checks the primes, then run `npm test`. Finally commit everything with git.
'@

function Write-Step([string]$Message) {
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Record-Check {
    param([string]$Name, [bool]$OK, [string]$Detail = "")
    $result = if ($OK) { "PASS" } else { "FAIL" }
    $script:Checks.Add([pscustomobject]@{ Check = $Name; Result = $result; Detail = $Detail })
    $color = if ($OK) { "Green" } else { "Red" }
    Write-Host ("  [{0}] {1}{2}" -f $result, $Name, $(if ($Detail) { " — $Detail" } else { "" })) -ForegroundColor $color
}

function Assert-Check {
    param([string]$Name, [bool]$OK, [string]$Detail = "")
    Record-Check -Name $Name -OK $OK -Detail $Detail
    if (-not $OK) {
        throw "ASSERTION FAILED: $Name — $Detail"
    }
}

function Save-Json {
    param([string]$Path, $Value)
    $Value | ConvertTo-Json -Depth 25 | Set-Content -Path $Path -Encoding UTF8
}

function Invoke-Api {
    param(
        [ValidateSet("GET", "POST", "PATCH", "PUT", "DELETE")]
        [string]$Method,
        [string]$Path,
        $Body = $null,
        $Session,
        [string]$CSRF = "",
        [switch]$Raw
    )
    $params = @{
        Method = $Method
        Uri    = "$BaseUrl$Path"
        WebSession = $Session
        UseBasicParsing = $true
    }
    if ($Method -ne "GET") {
        $params.Headers = @{ "X-PF-CSRF" = $CSRF }
    }
    if ($null -ne $Body) {
        $params.ContentType = "application/json"
        $params.Body = ($Body | ConvertTo-Json -Depth 12 -Compress)
    }
    if ($Raw) {
        # -SkipHttpErrorCheck lets a 4xx/5xx be an assertion input instead of an
        # exception: the PR drill has to read the forge's own refusal verbatim.
        $resp = Invoke-WebRequest @params -SkipHttpErrorCheck
        $parsed = $null
        if ($resp.Content) {
            try { $parsed = $resp.Content | ConvertFrom-Json } catch { $parsed = $resp.Content }
        }
        return [pscustomobject]@{ Status = [int]$resp.StatusCode; Body = $parsed }
    }
    return Invoke-RestMethod @params
}

function Login-Drill {
    param([string]$Email, [string]$Password)
    $body = @{ email = $Email; password = $Password } | ConvertTo-Json -Compress
    $resp = Invoke-RestMethod -Method Post -Uri "$BaseUrl/v1/auth/login" `
        -ContentType "application/json" -Body $body -SessionVariable session
    return [pscustomobject]@{ User = $resp.user; CSRF = $resp.csrf; Session = $session }
}

function Redact-Secrets {
    param([string]$Text)
    foreach ($secret in @($Key, $SeedAdminPassword, $SeedUserPassword, $NewUserPassword, $ForgeToken)) {
        if ($secret) { $Text = $Text.Replace($secret, "<redacted>") }
    }
    return $Text
}

function Test-PortFree {
    param([int]$P)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $iar = $client.BeginConnect("127.0.0.1", $P, $null, $null)
        if (-not $iar.AsyncWaitHandle.WaitOne(400)) { return $true }
        $client.EndConnect($iar)
        return $false
    }
    catch {
        return $true
    }
    finally {
        $client.Close()
    }
}

function Get-ContainerState {
    param([string]$Name, [int]$HealthPort)
    $names = @(& docker ps -a --format "{{.Names}}" 2>$null)
    if (-not ($names -contains $Name)) {
        return [pscustomobject]@{ Exists = $false; ID = ""; Running = $false; Healthz = $false }
    }
    $info = (& docker inspect $Name --format "{{.Id}}|{{.State.Running}}" 2>$null | Out-String).Trim()
    $parts = $info -split "\|"
    $healthy = $false
    try {
        $r = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$HealthPort/healthz" -TimeoutSec 3
        $healthy = ($r.StatusCode -eq 200)
    }
    catch { }
    return [pscustomobject]@{
        Exists  = $true
        ID      = $parts[0]
        Running = ($parts.Count -gt 1 -and $parts[1] -eq "true")
        Healthz = $healthy
    }
}

function Invoke-Docker {
    param([string[]]$Arguments)
    $output = & docker @Arguments 2>&1
    $code = $LASTEXITCODE
    return [pscustomobject]@{ Code = $code; Output = (($output | Out-String).Trim()) }
}

function Wait-Until {
    param([scriptblock]$Condition, [int]$TimeoutSeconds, [string]$What)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (& $Condition) { return $true }
        Start-Sleep -Milliseconds 400
    }
    throw "timed out after ${TimeoutSeconds}s waiting for $What"
}

# Write-SessionFile is the raw-body PUT the editor uses. The body goes out as
# UTF-8 bytes so a non-ASCII line is the same bytes the API reads back.
function Write-SessionFile {
    param([string]$SessionID, $Session, [string]$CSRF, [string]$Path, [string]$Text)
    $uri = "$BaseUrl/v1/sessions/$SessionID/files?path=$([uri]::EscapeDataString($Path))"
    $resp = Invoke-WebRequest -Method Put -Uri $uri -WebSession $Session `
        -Headers @{ "X-PF-CSRF" = $CSRF } -ContentType "text/plain; charset=utf-8" `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($Text)) -UseBasicParsing
    return [int]$resp.StatusCode
}

# Wait-SessionSettled polls the same status route a client uses until a run has
# been seen running and then stays idle with a stable sequence number. It
# returns the observation instead of throwing so the caller decides what to
# assert.
function Wait-SessionSettled {
    param([string]$SessionID, $Session, [int]$TimeoutSeconds)
    $sawRunning = $false
    $firstIdleAt = $null
    $lastSeq = -1
    $stableTicks = 0
    $lastStatus = $null
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $st = Invoke-Api -Method GET -Path "/v1/sessions/$SessionID/status" -Session $Session
        $lastStatus = $st
        if ($st.running) {
            $sawRunning = $true
            $firstIdleAt = $null
            $stableTicks = 0
        }
        elseif ($sawRunning) {
            if ($null -eq $firstIdleAt) {
                $firstIdleAt = [DateTime]::UtcNow
                $lastSeq = $st.last_seq
                $stableTicks = 0
            }
            elseif ($st.last_seq -eq $lastSeq) {
                $stableTicks++
                if ($stableTicks -ge 4) { break }
            }
            else {
                $lastSeq = $st.last_seq
                $stableTicks = 0
            }
        }
        Start-Sleep -Milliseconds 500
    }
    return [pscustomobject]@{
        SawRunning = $sawRunning
        Settled    = ($sawRunning -and $null -ne $firstIdleAt)
        Status     = $lastStatus
    }
}

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

$ContainerArgs = $null
try {
    Write-Step "Preflight"

    if (-not $Key) {
        throw "PIFORGE_DRILL_KEY is not set. Put the provider key in that environment variable; the script will not read or print it anywhere else."
    }
    if ($Key.Length -lt 8) {
        throw "PIFORGE_DRILL_KEY looks too short to be a provider key."
    }
    if ($ContainerName -eq $ExistingContainer) {
        throw "refusing to run: ContainerName equals '$ExistingContainer'; this drill never touches that deployment."
    }
    if ($ContainerName -eq "pf-local") {
        throw "refusing to run: this drill never creates, reuses or removes a container named pf-local."
    }
    if ($VolumeName -eq "pf-local-data") {
        throw "refusing to run: this drill never creates, reuses or removes the pf-local volume."
    }
    if ($ProviderName -match "\|" -or $ProviderBaseURL -match "\|" -or $ProviderModel -match "\|") {
        throw "provider name, base URL and model must not contain '|' (the bootstrap format uses it as a separator)."
    }
    foreach ($tool in @("docker", "curl.exe")) {
        if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
            throw "$tool is required but was not found on PATH."
        }
    }
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        throw "node is required for the UI check; install Node 22+ or pass -SkipUi."
    }
    if (-not $SkipUi -and -not (Test-Path (Join-Path $RepoRoot "e2e\node_modules\@playwright\test"))) {
        throw "e2e/node_modules/@playwright/test is missing; run 'pnpm --dir e2e install' or pass -SkipUi."
    }

    $imageInspect = Invoke-Docker @("image", "inspect", $Image, "--format", "{{.Id}}")
    Assert-Check "image exists: $Image" ($imageInspect.Code -eq 0) ($imageInspect.Output -replace "`n", " ")

    $existingBefore = Get-ContainerState -Name $ExistingContainer -HealthPort $ExistingPort
    if ($existingBefore.Exists) {
        Assert-Check "$ExistingContainer is running before the drill" $existingBefore.Running "id=$($existingBefore.ID)"
    }
    else {
        Write-Host "  [INFO] $ExistingContainer does not exist on this host; the drill will not create it." -ForegroundColor Yellow
    }

    Assert-Check "port $Port is free" (Test-PortFree -P $Port) "127.0.0.1:$Port"

    $names = @(& docker ps -a --format "{{.Names}}")
    Assert-Check "drill container name '$ContainerName' is unused" (-not ($names -contains $ContainerName)) "a pre-existing container is never deleted by this script"

    $volumes = @(& docker volume ls --format "{{.Name}}")
    Assert-Check "drill volume name '$VolumeName' is unused" (-not ($volumes -contains $VolumeName)) "a fresh empty volume is required"

    $script:Metrics["run_id"] = $RunID
    $script:Metrics["image"] = $Image
    $script:Metrics["image_id"] = $imageInspect.Output
    $script:Metrics["port"] = $Port
    $script:Metrics["container"] = $ContainerName
    $script:Metrics["volume"] = $VolumeName
    $script:Metrics["provider"] = "$ProviderName|$ProviderBaseURL|$ProviderModel"
    $script:Metrics["new_user"] = $NewUserEmail

    # -----------------------------------------------------------------------
    # 1. Fresh volume + container with only the documented environment
    # -----------------------------------------------------------------------

    Write-Step "Create a brand-new volume and start the container"

    $volumeCreate = Invoke-Docker @("volume", "create", $VolumeName)
    Assert-Check "docker volume create $VolumeName" ($volumeCreate.Code -eq 0) $volumeCreate.Output
    $script:CreatedVolume = $true

    $ContainerArgs = @(
        "run", "-d",
        "--name", $ContainerName,
        "-p", "${Port}:7700",
        # The same capability decision as the default deployment (FR-14.2): no
        # SYS_ADMIN (a bind mount is an upgrade, not a requirement), and
        # DAC_OVERRIDE dropped so a read-only skill copy is enforced even though
        # the control plane runs as root. None of the drill's checks mount a
        # skill, so this is about proving the shipped shape starts and works —
        # a drill that needed a capability the product no longer asks for would
        # be testing a deployment nobody runs.
        "--cap-drop", "DAC_OVERRIDE",
        "-v", "${VolumeName}:/data",
        "-e", "PIFORGE_SEED_EMAIL=$SeedAdminEmail",
        "-e", "PIFORGE_SEED_PASSWORD=$SeedAdminPassword",
        "-e", "PIFORGE_SEED_USER_EMAIL=$SeedUserEmail",
        "-e", "PIFORGE_SEED_USER_PASSWORD=$SeedUserPassword",
        "-e", "PIFORGE_DEFAULT_PROVIDER=$ProviderName",
        "-e", "PIFORGE_BOOTSTRAP_PROVIDER=$ProviderName|$ProviderBaseURL|$ProviderModel",
        "-e", "PIFORGE_BOOTSTRAP_PROVIDER_KEY=$Key",
        $Image
    )

    $runStart = [DateTime]::UtcNow
    $run = Invoke-Docker $ContainerArgs
    if ($run.Code -ne 0) {
        throw "docker run failed: $($run.Output)"
    }
    $script:CreatedContainer = $true
    $containerID = ($run.Output -split "`n" | Select-Object -Last 1).Trim()
    Assert-Check "container started" ([bool]$containerID) "id=$containerID"
    $script:Metrics["container_id"] = $containerID

    $healthSeconds = $null
    $healthy = $false
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/healthz" -TimeoutSec 2
            if ($resp.StatusCode -eq 200) {
                $healthy = $true
                $healthSeconds = ([DateTime]::UtcNow - $runStart).TotalSeconds
                break
            }
        }
        catch { }
        Start-Sleep -Milliseconds 200
    }
    Assert-Check "GET /healthz answered 200" $healthy ("{0:N2}s after docker run" -f $healthSeconds)
    $script:Metrics["healthz_seconds"] = [Math]::Round($healthSeconds, 2)

    # -----------------------------------------------------------------------
    # 2. Bootstrap evidence: setup endpoint + log, and the key is absent
    # -----------------------------------------------------------------------

    Write-Step "Prove the bootstrap provider exists without hand configuration"

    $rawLog = (& docker logs $ContainerName 2>&1 | Out-String)
    $keyHits = ([regex]::Matches($rawLog, [regex]::Escape($Key))).Count
    $logPath = Join-Path $ArtifactDir "container.log"
    (Redact-Secrets $rawLog) | Set-Content -Path $logPath -Encoding UTF8

    Assert-Check "container log says the shared provider was created with has_key=true" `
        ($rawLog -match "bootstrap: created the shared model provider" -and $rawLog -match "has_key=true") `
        "provider=$ProviderName model=$ProviderModel"
    Assert-Check "provider key appears zero times in the container log" ($keyHits -eq 0) "occurrences=$keyHits"

    $admin = Login-Drill -Email $SeedAdminEmail -Password $SeedAdminPassword
    $script:AdminSession = $admin.Session
    $script:AdminCSRF = $admin.CSRF
    Assert-Check "seed admin can log in" ($admin.User.email -eq $SeedAdminEmail) "role=$($admin.User.role)"

    $setupAdmin = Invoke-Api -Method GET -Path "/v1/setup" -Session $admin.Session
    Save-Json (Join-Path $ArtifactDir "setup-admin.json") $setupAdmin
    Assert-Check "GET /v1/setup (admin): needs_provider=false" (-not $setupAdmin.needs_provider) "needs_provider=$($setupAdmin.needs_provider)"
    Assert-Check "GET /v1/setup (admin): shared_provider_available=true" ([bool]$setupAdmin.shared_provider_available) "shared=$($setupAdmin.shared_provider_available)"
    Assert-Check "GET /v1/setup (admin): bootstrap prefill matches the deployment env" `
        ($setupAdmin.bootstrap -and $setupAdmin.bootstrap.name -eq $ProviderName -and
         $setupAdmin.bootstrap.base_url -eq $ProviderBaseURL -and $setupAdmin.bootstrap.model -eq $ProviderModel) `
        "bootstrap=$($setupAdmin.bootstrap.name)|$($setupAdmin.bootstrap.base_url)|$($setupAdmin.bootstrap.model)"

    # -----------------------------------------------------------------------
    # 3. New user through the API, then project and session
    # -----------------------------------------------------------------------

    Write-Step "Create a brand-new user through the API"

    $created = Invoke-Api -Method POST -Path "/v1/admin/users" -Session $admin.Session -CSRF $admin.CSRF `
        -Body @{ email = $NewUserEmail; display_name = "Delivery Drill"; password = $NewUserPassword; role = "user" }
    Assert-Check "POST /v1/admin/users created $NewUserEmail" `
        ([bool]$created.users -and $created.users[0].email -eq $NewUserEmail) "id=$($created.users[0].id)"

    $user = Login-Drill -Email $NewUserEmail -Password $NewUserPassword
    Assert-Check "new user can log in" ($user.User.email -eq $NewUserEmail) "role=$($user.User.role)"

    $setupUser = Invoke-Api -Method GET -Path "/v1/setup" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "setup-user.json") $setupUser
    Assert-Check "new user inherits the shared provider (needs_provider=false)" (-not $setupUser.needs_provider) "needs_provider=$($setupUser.needs_provider)"
    Assert-Check "new user sees shared_provider_available=true" ([bool]$setupUser.shared_provider_available) "shared=$($setupUser.shared_provider_available)"
    Assert-Check "new user is not an admin" (-not $setupUser.is_admin) "is_admin=$($setupUser.is_admin)"

    $project = Invoke-Api -Method POST -Path "/v1/projects" -Session $user.Session -CSRF $user.CSRF `
        -Body @{ name = $ProjectName; repo_url = ""; default_branch = "main" }
    $projectID = $project.project.id
    Assert-Check "POST /v1/projects created a project without a repo" ([bool]$projectID) "project=$projectID"

    $session = Invoke-Api -Method POST -Path "/v1/projects/$projectID/sessions" -Session $user.Session -CSRF $user.CSRF `
        -Body @{ title = "delivery drill" }
    $sessionID = $session.session.id
    Assert-Check "POST /v1/projects/{id}/sessions created a session" ([bool]$sessionID) "session=$sessionID"
    Assert-Check "session was provisioned with a worktree before prompting" ([bool]$session.session.worktree_id) "worktree=$($session.session.worktree_id)"

    $script:Metrics["project_id"] = $projectID
    $script:Metrics["session_id"] = $sessionID
    $script:Metrics["worktree_id"] = $session.session.worktree_id

    Save-Json (Join-Path $ArtifactDir "context.json") ([pscustomobject]@{
        run_id = $RunID; user = $NewUserEmail; project = $projectID; session = $sessionID
        worktree = $session.session.worktree_id; image = $Image; port = $Port
    })

    # -----------------------------------------------------------------------
    # 4. One real agent turn
    # -----------------------------------------------------------------------

    Write-Step "Send the real task and wait for settle"

    $promptSentAt = [DateTime]::UtcNow
    $promptResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/messages" -Session $user.Session -CSRF $user.CSRF `
        -Body @{ client_msg_id = "drill-$RunID"; text = $PromptText }
    Assert-Check "POST /v1/sessions/{id}/messages accepted the prompt" ([bool]$promptResp.accepted) "disposition=$($promptResp.disposition) run=$($promptResp.run_id)"
    $script:Metrics["prompt_run_id"] = $promptResp.run_id

    $sawRunning = $false
    $firstIdleAt = $null
    $lastSeq = -1
    $stableTicks = 0
    $lastStatus = $null
    $deadline = [DateTime]::UtcNow.AddSeconds($SettleTimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $st = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/status" -Session $user.Session
        $lastStatus = $st
        if ($st.running) {
            $sawRunning = $true
            $firstIdleAt = $null
            $stableTicks = 0
        }
        elseif ($sawRunning) {
            if ($null -eq $firstIdleAt) {
                $firstIdleAt = [DateTime]::UtcNow
                $lastSeq = $st.last_seq
                $stableTicks = 0
            }
            elseif ($st.last_seq -eq $lastSeq) {
                $stableTicks++
                if ($stableTicks -ge 4) { break }
            }
            else {
                $lastSeq = $st.last_seq
                $stableTicks = 0
            }
        }
        Start-Sleep -Milliseconds 500
    }
    Assert-Check "the run was observed running and then idle" ($sawRunning -and $null -ne $firstIdleAt) `
        "running=$($lastStatus.running) last_seq=$($lastStatus.last_seq) model=$($lastStatus.model_ref)"
    Assert-Check "the agent process is live (session process id)" ($lastStatus.process_id -gt 0) "pid=$($lastStatus.process_id)"

    $statusIdleSeconds = ($firstIdleAt - $promptSentAt).TotalSeconds
    $script:Metrics["prompt_to_first_idle_seconds"] = [Math]::Round($statusIdleSeconds, 2)

    # Replay the event log after the fact: SSE serves history from after_seq=0,
    # so a short bounded curl is enough to read the run.settled frame that the
    # product (and its own e2e suite) treats as the only completion signal.
    $cookie = $user.Session.Cookies.GetCookies([uri]$BaseUrl) | Where-Object { $_.Name -eq "pf_session" }
    Assert-Check "the session cookie is available for the SSE replay" ($null -ne $cookie) "pf_session"
    $eventsPath = Join-Path $ArtifactDir "events.sse"
    & curl.exe -sN --max-time 10 -b "pf_session=$($cookie.Value)" "$BaseUrl/v1/sessions/$sessionID/events?after_seq=0" -o $eventsPath
    $sse = Get-Content -Path $eventsPath -Raw
    Assert-Check "the event log replayed" ($sse.Length -gt 0) ("{0:N0} bytes" -f $sse.Length)

    $settledMatches = [regex]::Matches($sse, "(?m)^event: run\.settled\r?\ndata: (.+)$")
    Assert-Check "the transcript has a run.settled frame" ($settledMatches.Count -ge 1) "frames=$($settledMatches.Count)"
    $startedMatches = [regex]::Matches($sse, "(?m)^event: run\.started\r?\ndata: (.+)$")
    Assert-Check "the transcript has a run.started frame" ($startedMatches.Count -ge 1) "frames=$($startedMatches.Count)"

    $settleEvent = $settledMatches[$settledMatches.Count - 1].Groups[1].Value | ConvertFrom-Json
    $settleAt = ([DateTimeOffset]::Parse($settleEvent.ts)).UtcDateTime
    $settleSeconds = ($settleAt - $promptSentAt).TotalSeconds
    Assert-Check "run.settled carries a timestamp after the prompt" ($settleSeconds -gt 0) ("{0:N2}s" -f $settleSeconds)
    $script:Metrics["prompt_to_settle_seconds"] = [Math]::Round($settleSeconds, 2)
    $script:Metrics["settle_event_seq"] = $settleEvent.seq
    $script:Metrics["settle_tokens_in"] = $settleEvent.payload.tokens_in
    $script:Metrics["settle_tokens_out"] = $settleEvent.payload.tokens_out

    # -----------------------------------------------------------------------
    # 5. Transcript: a reply and tool calls that settled
    # -----------------------------------------------------------------------

    Write-Step "Verify the transcript and the workspace"

    $entries = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/entries" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "entries.json") $entries
    $list = @($entries.entries)
    Assert-Check "the transcript is non-empty" ($list.Count -gt 0) "entries=$($list.Count)"

    $userEntry = @($list | Where-Object { $_.role -eq "user" -and $_.preview -like "*first 10 primes*" })
    Assert-Check "the prompt is in the transcript as a user message" ($userEntry.Count -ge 1) "entries=$($userEntry.Count)"

    $assistantEntries = @($list | Where-Object { $_.role -eq "assistant" -and $_.preview })
    Assert-Check "the agent produced assistant replies" ($assistantEntries.Count -ge 1) "replies=$($assistantEntries.Count)"

    $toolResultEntries = @($list | Where-Object { $_.role -eq "toolResult" })
    Assert-Check "the agent produced tool results" ($toolResultEntries.Count -ge 3) "toolResults=$($toolResultEntries.Count)"

    $toolCalls = 0
    foreach ($entry in $list) {
        if ($entry.extra -and $entry.extra.content) {
            foreach ($part in @($entry.extra.content)) {
                if ($part.type -eq "toolCall") { $toolCalls++ }
            }
        }
    }
    Assert-Check "the assistant issued tool calls" ($toolCalls -ge 3) "toolCalls=$toolCalls"

    $toolText = ($toolResultEntries | ForEach-Object {
        if ($_.extra -and $_.extra.content) {
            foreach ($part in @($_.extra.content)) { if ($part.text) { $part.text } }
        }
    }) -join "`n"
    $normalizedOutput = $toolText -replace "[,;，、]", " "
    $primesPattern = "2\s+3\s+5\s+7\s+11\s+13\s+17\s+19\s+23\s+29"
    Assert-Check "the run printed the first 10 primes" ($normalizedOutput -match $primesPattern) "2 3 5 7 11 13 17 19 23 29"

    $tree = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/files/tree?path=&depth=3" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "files-tree.json") $tree
    $allNodes = New-Object 'System.Collections.Generic.List[object]'
    function Add-Nodes($nodes) {
        foreach ($n in @($nodes)) {
            $script:allNodes.Add($n)
            if ($n.children) { Add-Nodes $n.children }
        }
    }
    # The helper above uses $script:allNodes; keep the local alias in sync.
    $script:allNodes = $allNodes
    Add-Nodes $tree.nodes
    $paths = @($allNodes | ForEach-Object { $_.path })
    Assert-Check "the file tree lists primes.js" ($paths -contains "primes.js") ($paths -join ", ")
    Assert-Check "the file tree lists package.json" ($paths -contains "package.json") ($paths -join ", ")
    Assert-Check "the file tree lists a test file" (@($paths | Where-Object { $_ -match "\.test\.js$" }).Count -ge 1) ($paths -join ", ")

    # Locate the checkout inside the container and compare bytes with the API.
    $findCmd = "find /data/sandboxes -type f -name primes.js -path '*$projectID*' -print -quit"
    $sandboxFile = (& docker exec $ContainerName sh -c $findCmd 2>$null | Out-String).Trim()
    if (-not $sandboxFile) {
        $sandboxFile = (& docker exec $ContainerName sh -c "find /data/sandboxes -type f -name primes.js -print -quit" 2>$null | Out-String).Trim()
    }
    Assert-Check "primes.js exists inside the sandbox filesystem" ([bool]$sandboxFile) $sandboxFile
    $worktree = $sandboxFile -replace "/primes\.js$", ""
    Assert-Check "the sandbox checkout path was resolved" ($worktree -like "/data/sandboxes/*") $worktree
    $script:Metrics["sandbox_worktree"] = $worktree

    $workspaceCopy = Join-Path $ArtifactDir "primes.js"
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/v1/sessions/$sessionID/files?path=primes.js&download=1" `
        -WebSession $user.Session -OutFile $workspaceCopy
    $workspaceHash = (Get-FileHash -Algorithm SHA256 -Path $workspaceCopy).Hash.ToLowerInvariant()
    $sandboxHashOut = (& docker exec $ContainerName sh -c "sha256sum '$worktree/primes.js'" | Out-String).Trim()
    $sandboxHash = ($sandboxHashOut -split "\s+")[0].ToLowerInvariant()
    Assert-Check "the workspace copy and the sandbox file are the same bytes" ($workspaceHash -eq $sandboxHash) "api=$workspaceHash sandbox=$sandboxHash"

    $testRel = (& docker exec $ContainerName sh -c "cd '$worktree' && find . -type f -name '*.test.js' -print -quit" | Out-String).Trim()
    Assert-Check "the agent wrote a test file in the sandbox" ([bool]$testRel) $testRel

    # -----------------------------------------------------------------------
    # 6. Commit made with the container's identity
    # -----------------------------------------------------------------------

    Write-Step "Verify the commit made inside the container"

    $commitLine = (& docker exec $ContainerName sh -c "cd '$worktree' && git log -1 --format='%an|%ae|%s'" | Out-String).Trim()
    $commitParts = $commitLine -split "\|", 3
    $commitHash = (& docker exec $ContainerName sh -c "cd '$worktree' && git rev-parse HEAD" | Out-String).Trim()
    $statusOut = (& docker exec $ContainerName sh -c "cd '$worktree' && git status --porcelain" | Out-String).Trim()
    $changed = (& docker exec $ContainerName sh -c "cd '$worktree' && git show --name-only --format= HEAD" | Out-String).Trim()
    @(
        "git log -1 --format=%an|%ae|%s -> $commitLine",
        "HEAD -> $commitHash",
        "git status --porcelain -> $(if ($statusOut) { $statusOut -replace "`r?`n", "; " } else { "<clean>" })",
        "git show --name-only --format= HEAD ->",
        $changed
    ) | Set-Content -Path (Join-Path $ArtifactDir "commit.txt") -Encoding UTF8

    Assert-Check "git log -1 has the three identity fields" ($commitParts.Count -eq 3) $commitLine
    Assert-Check "the commit author is non-empty" ([bool]$commitParts[0]) "author=$($commitParts[0])"
    Assert-Check "the commit email is a real address" ($commitParts[1] -match "@") "email=$($commitParts[1])"
    Assert-Check "the commit subject is non-empty" ([bool]$commitParts[2]) "subject=$($commitParts[2])"
    Assert-Check "HEAD resolves to a commit" ($commitHash -match "^[0-9a-f]{40}$") "HEAD=$commitHash"
    Assert-Check "the checkout is clean after the commit" (-not $statusOut) $(if ($statusOut) { $statusOut -replace "`r?`n", "; " } else { "<clean>" })
    $changedLines = @($changed -split "\r?\n" | ForEach-Object { $_.Trim() } | Where-Object { $_ })
    Assert-Check "the commit contains primes.js" ($changedLines -contains "primes.js") ($changedLines -join ", ")
    $script:Metrics["commit"] = $commitHash
    $script:Metrics["commit_author"] = $commitParts[0]
    $script:Metrics["commit_email"] = $commitParts[1]
    $script:Metrics["commit_subject"] = $commitParts[2]

    # -----------------------------------------------------------------------
    # 7. Test panel
    # -----------------------------------------------------------------------

    Write-Step "Verify the test panel recorded a run"

    $testRuns = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/test-runs?limit=20" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "test-runs.json") $testRuns
    $runs = @($testRuns.runs)
    Assert-Check "GET /v1/sessions/{id}/test-runs has at least one run" ($runs.Count -ge 1) "count=$($runs.Count)"
    $testRun = @($runs | Where-Object { $_.command -match "npm test|node --test" })
    Assert-Check "the recorded run is the npm test command the agent was asked to run" ($testRun.Count -ge 1) (($runs | ForEach-Object { $_.command }) -join ", ")
    $firstRun = $runs[0]
    Assert-Check "the test run is no longer running" ($firstRun.status -ne "running") "status=$($firstRun.status) runner=$($firstRun.runner)"
    $script:Metrics["test_runs"] = $runs.Count
    $script:Metrics["test_run_status"] = $firstRun.status
    $script:Metrics["test_run_command"] = $firstRun.command

    # -----------------------------------------------------------------------
    # 8. Open a pull request through the API against a fake forge (FR-12.6)
    #
    # The forge is a tiny Node HTTP server started inside the drill container:
    # 127.0.0.1 there is the control plane's own loopback, so no host
    # networking, no firewall rule and no second host process are involved.
    # It records every request, so the drill can assert what actually crossed
    # the wire instead of trusting the product's own success response.
    # -----------------------------------------------------------------------

    Write-Step "Open a pull request through the API against a fake forge"

    $ForgeRepo = "acme/widgets"
    $ForgeBaseBranch = "main"
    $ForgeTitle = "Drill: open a pull request"
    $ForgeBody = "Opened by the from-zero delivery drill."
    $ForgeRoot = "/tmp/drill-forge"
    $ForgeLogPath = "$ForgeRoot/requests.jsonl"
    $ForgeControlPath = "$ForgeRoot/control.json"
    $ForgeReadyPath = "$ForgeRoot/ready"

    $fakeForgeSource = Join-Path $ArtifactDir "fake-forge.js"
    Set-Content -Path $fakeForgeSource -Encoding UTF8 -Value @'
// The fake forge of the delivery drill (FR-12.6). It speaks only the two
// GitHub-dialect calls the product makes and appends every request to a JSONL
// log. A control file lets the drill force a refusal (422) and see how the
// product reports it.
const http = require("node:http");
const fs = require("node:fs");

const port = Number(process.env.DRILL_FORGE_PORT || 7799);
const logPath = process.env.DRILL_FORGE_LOG || "/tmp/drill-forge/requests.jsonl";
const controlPath = process.env.DRILL_FORGE_CONTROL || "/tmp/drill-forge/control.json";
const readyPath = process.env.DRILL_FORGE_READY || "/tmp/drill-forge/ready";
const repo = process.env.DRILL_FORGE_REPO || "acme/widgets";
const pullsPath = "/repos/" + repo + "/pulls";

const open = [];
fs.writeFileSync(logPath, "");

function control() {
  try {
    return JSON.parse(fs.readFileSync(controlPath, "utf8"));
  } catch {
    return {};
  }
}

function record(entry) {
  fs.appendFileSync(logPath, JSON.stringify(entry) + "\n");
}

const server = http.createServer((req, res) => {
  let raw = "";
  req.on("data", (chunk) => {
    raw += chunk;
  });
  req.on("end", () => {
    const url = new URL(req.url, "http://127.0.0.1");
    const entry = {
      at: new Date().toISOString(),
      method: req.method,
      path: url.pathname,
      query: url.search,
      authorization: req.headers.authorization || "",
      api_version: req.headers["x-github-api-version"] || "",
      body: null,
    };
    if (raw) {
      try {
        entry.body = JSON.parse(raw);
      } catch {
        entry.body = raw;
      }
    }
    const send = (status, payload) => {
      entry.status = status;
      record(entry);
      res.writeHead(status, { "content-type": "application/json" });
      res.end(payload === undefined ? "" : JSON.stringify(payload));
    };
    if (url.pathname !== pullsPath) {
      return send(404, { message: "drill forge: no route for " + url.pathname });
    }
    if (req.method === "GET") {
      const head = url.searchParams.get("head") || "";
      const base = url.searchParams.get("base") || "";
      const owner = repo.split("/")[0];
      const list = open.filter((pr) => {
        const headOk = !head || head === pr.head.ref || head === owner + ":" + pr.head.ref;
        const baseOk = !base || base === pr.base.ref;
        return headOk && baseOk;
      });
      return send(200, list);
    }
    if (req.method === "POST") {
      const ctl = control();
      if (ctl.failStatus) {
        return send(Number(ctl.failStatus), ctl.failBody || { message: "Validation Failed" });
      }
      const body = entry.body || {};
      const number = open.length + 7;
      const pr = {
        number: number,
        html_url: "http://127.0.0.1:" + port + "/" + repo + "/pull/" + number,
        title: body.title || "",
        head: { ref: String(body.head || "").replace(/^[^:]*:/, "") },
        base: { ref: body.base || "" },
      };
      open.push(pr);
      return send(201, pr);
    }
    return send(405, { message: "method not allowed" });
  });
});

server.on("error", (err) => {
  fs.writeFileSync(readyPath + ".error", String(err));
  process.exit(1);
});
server.listen(port, "127.0.0.1", () => {
  fs.writeFileSync(readyPath, "ready");
});
'@

    $forgeSetup = Invoke-Docker @("exec", $ContainerName, "mkdir", "-p", $ForgeRoot)
    $controlFile = Join-Path $ArtifactDir "forge-control-initial.json"
    Save-Json $controlFile ([ordered]@{})
    $copyServer = Invoke-Docker @("cp", $fakeForgeSource, "${ContainerName}:$ForgeRoot/server.js")
    $copyControl = Invoke-Docker @("cp", $controlFile, "${ContainerName}:$ForgeRoot/control.json")
    Assert-Check "the fake forge is installed inside the drill container" `
        ($forgeSetup.Code -eq 0 -and $copyServer.Code -eq 0 -and $copyControl.Code -eq 0) `
        "mkdir=$($forgeSetup.Code) server=$($copyServer.Code) control=$($copyControl.Code)"

    $forgeStart = Invoke-Docker @(
        "exec", "-d",
        "-e", "DRILL_FORGE_PORT=$FakeForgePort",
        "-e", "DRILL_FORGE_LOG=$ForgeLogPath",
        "-e", "DRILL_FORGE_CONTROL=$ForgeControlPath",
        "-e", "DRILL_FORGE_READY=$ForgeReadyPath",
        "-e", "DRILL_FORGE_REPO=$ForgeRepo",
        $ContainerName, "node", "$ForgeRoot/server.js"
    )
    $forgeReady = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    while ([DateTime]::UtcNow -lt $deadline) {
        & docker exec $ContainerName test -f $ForgeReadyPath 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $forgeReady = $true; break }
        Start-Sleep -Milliseconds 200
    }
    Assert-Check "the fake forge is listening inside the drill container" $forgeReady `
        "http://127.0.0.1:$FakeForgePort/repos/$ForgeRepo/pulls (docker exec -d exit=$($forgeStart.Code))"

    # A local bare repository is the drill's "remote": the product pushes the
    # branch there before it creates a PR, exactly as it would to a real origin.
    $originPath = "/data/drill-forge-origin.git"
    $remoteOut = (& docker exec $ContainerName sh -c "rm -rf '$originPath' && git init -q --bare -b '$ForgeBaseBranch' '$originPath' && cd '$worktree' && (git remote remove origin >/dev/null 2>&1 || true) && git remote add origin '$originPath' && git remote get-url origin" 2>&1 | Out-String).Trim()
    Assert-Check "the drill checkout has a git remote that can receive the push" ($remoteOut -eq $originPath) $remoteOut

    $branchResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/branch" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{ name = $PRBranch }
    Assert-Check "POST /git/branch created $PRBranch" ($branchResp.Status -eq 200) "status=$($branchResp.Status)"
    $checkoutResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/checkout" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{ name = $PRBranch }
    Assert-Check "POST /git/checkout switched the session to $PRBranch" ($checkoutResp.Status -eq 200) "status=$($checkoutResp.Status)"

    $prFileStatus = Write-SessionFile -SessionID $sessionID -Session $user.Session -CSRF $user.CSRF `
        -Path "pr-drill.txt" -Text "added for the delivery drill`n"
    Assert-Check "PUT /files wrote pr-drill.txt on the PR branch" ($prFileStatus -eq 200) "status=$prFileStatus"
    $prCommitResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/commit" -Session $user.Session -CSRF $user.CSRF `
        -Body @{ message = "drill: a file for the pull request"; paths = @("pr-drill.txt") }
    $prCommit = $prCommitResp.commit
    Assert-Check "POST /git/commit made a commit on $PRBranch" ($prCommit -match "^[0-9a-f]{40}$") "commit=$prCommit"
    $prStatus = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/git/status" -Session $user.Session
    Assert-Check "the PR branch is checked out and clean" ($prStatus.branch -eq $PRBranch -and $prStatus.clean) `
        "branch=$($prStatus.branch) clean=$($prStatus.clean)"
    $script:Metrics["pr_branch"] = $PRBranch
    $script:Metrics["pr_commit"] = $prCommit

    # Point the project at the fake forge through the product's own API.
    $forgeConfig = Invoke-Api -Method PUT -Path "/v1/projects/$projectID/forge" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{
        kind = "github"; api_base = "http://127.0.0.1:$FakeForgePort"; repo = $ForgeRepo
        base_branch = $ForgeBaseBranch; token = $ForgeToken
    }
    Save-Json (Join-Path $ArtifactDir "forge-config.json") $forgeConfig.Body
    $forgeConfigJSON = $forgeConfig.Body | ConvertTo-Json -Depth 25
    Assert-Check "PUT /projects/{id}/forge saved the forge configuration" `
        ($forgeConfig.Status -eq 200 -and $forgeConfig.Body.project.forge.kind -eq "github" -and
         $forgeConfig.Body.project.forge.repo -eq $ForgeRepo) `
        "status=$($forgeConfig.Status) kind=$($forgeConfig.Body.project.forge.kind) repo=$($forgeConfig.Body.project.forge.repo)"
    Assert-Check "the saved forge reports has_token but never echoes the token" `
        ($forgeConfig.Body.project.forge.has_token -and -not $forgeConfigJSON.Contains($ForgeToken)) `
        "has_token=$($forgeConfig.Body.project.forge.has_token)"

    $prPreview = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/git/pr" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "pr-preview.json") $prPreview
    Assert-Check "GET /git/pr previews $PRBranch -> $ForgeBaseBranch with a remote" `
        ($prPreview.branch -eq $PRBranch -and $prPreview.base_branch -eq $ForgeBaseBranch -and [bool]$prPreview.remote) `
        "branch=$($prPreview.branch) base=$($prPreview.base_branch) remote=$($prPreview.remote)"
    $previewFiles = @($prPreview.files | ForEach-Object { $_.path })
    Assert-Check "the preview carries the new commit and its file" `
        ($prPreview.commit_count -ge 1 -and ($previewFiles -contains "pr-drill.txt")) `
        "commits=$($prPreview.commit_count) files=$($previewFiles -join ', ')"
    Assert-Check "the preview reports a saved forge token without showing it" `
        ([bool]$prPreview.forge.has_token) "has_token=$($prPreview.forge.has_token)"

    $prCreateBody = @{ title = $ForgeTitle; body = $ForgeBody }
    $prCreated = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/pr" -Session $user.Session -CSRF $user.CSRF -Raw -Body $prCreateBody
    Save-Json (Join-Path $ArtifactDir "pr-create.json") $prCreated.Body
    Assert-Check "POST /git/pr created the pull request (201, created=true)" `
        ($prCreated.Status -eq 201 -and [bool]$prCreated.Body.created) `
        "status=$($prCreated.Status) created=$($prCreated.Body.created)"
    $prURL = $prCreated.Body.pull_request.url
    Assert-Check "the response carries a PR URL" `
        ($prURL -match "^http://127\.0\.0\.1:$FakeForgePort/$([regex]::Escape($ForgeRepo))/pull/\d+$") $prURL
    Assert-Check "the pushed branch is the session's branch" `
        ($prCreated.Body.pushed.branch -eq $PRBranch -and $prCreated.Body.pushed.commit -match "^[0-9a-f]{40}$") `
        "branch=$($prCreated.Body.pushed.branch) commit=$($prCreated.Body.pushed.commit)"

    $prAgain = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/pr" -Session $user.Session -CSRF $user.CSRF -Raw -Body $prCreateBody
    Save-Json (Join-Path $ArtifactDir "pr-create-again.json") $prAgain.Body
    Assert-Check "opening the same PR again returns created=false and the same URL" `
        ($prAgain.Status -eq 200 -and -not $prAgain.Body.created -and $prAgain.Body.pull_request.url -eq $prURL) `
        "status=$($prAgain.Status) created=$($prAgain.Body.created) url=$($prAgain.Body.pull_request.url)"
    Assert-Check "the second response marks the pull request as existing" `
        ([bool]$prAgain.Body.pull_request.existing) "existing=$($prAgain.Body.pull_request.existing)"
    $script:Metrics["pr_url"] = $prURL

    # What the forge actually saw. The log is copied redacted; the token is
    # asserted from the in-memory copy and never written anywhere.
    $forgeRaw = (& docker exec $ContainerName cat $ForgeLogPath 2>$null | Out-String)
    $forgeLines = @($forgeRaw -split "\r?\n" | Where-Object { $_.Trim() })
    $forgeRequests = @($forgeLines | ForEach-Object { $_ | ConvertFrom-Json })
    ($forgeLines | ForEach-Object { $_.Replace($ForgeToken, "<redacted>") }) |
        Set-Content -Path (Join-Path $ArtifactDir "forge-requests.jsonl") -Encoding UTF8
    $createPosts = @($forgeRequests | Where-Object { $_.method -eq "POST" -and $_.path -eq "/repos/$ForgeRepo/pulls" })
    Assert-Check "the forge received exactly one create call" ($createPosts.Count -eq 1) "creates=$($createPosts.Count)"
    $wire = $createPosts[0]
    Assert-Check "the create call carried 'Bearer <token>' in the Authorization header" `
        ($wire.authorization -eq "Bearer $ForgeToken") "authorization=Bearer <redacted>"
    Assert-Check "the create call carried the exact title, head and base" `
        ($wire.body.title -eq $ForgeTitle -and $wire.body.head -eq "acme:$PRBranch" -and $wire.body.base -eq $ForgeBaseBranch) `
        "title=$($wire.body.title) head=$($wire.body.head) base=$($wire.body.base)"
    $tokenInQuery = @($forgeRequests | Where-Object { $_.query -and $_.query.Contains($ForgeToken) }).Count
    Assert-Check "the token never travels in a URL or query string" ($tokenInQuery -eq 0) "occurrences=$tokenInQuery"
    $script:Metrics["forge_requests"] = $forgeRequests.Count

    # A forge refusal has to surface as itself: the create step and the forge's
    # own words, not a generic "failed".
    $rejectBranchResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/branch" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{ name = $PRRejectBranch }
    $rejectCheckoutResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/checkout" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{ name = $PRRejectBranch }
    $rejectFileStatus = Write-SessionFile -SessionID $sessionID -Session $user.Session -CSRF $user.CSRF `
        -Path "reject.txt" -Text "a branch the forge will refuse`n"
    $rejectCommitResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/commit" -Session $user.Session -CSRF $user.CSRF `
        -Body @{ message = "drill: a branch for the forced refusal"; paths = @("reject.txt") }
    Assert-Check "a second branch was prepared for the forced 422" `
        ($rejectBranchResp.Status -eq 200 -and $rejectCheckoutResp.Status -eq 200 -and
         $rejectFileStatus -eq 200 -and $rejectCommitResp.commit -match "^[0-9a-f]{40}$") `
        "branch=$($rejectBranchResp.Status) checkout=$($rejectCheckoutResp.Status) file=$rejectFileStatus commit=$($rejectCommitResp.commit)"

    $rejectControl = Join-Path $ArtifactDir "forge-control-422.json"
    Save-Json $rejectControl ([ordered]@{
        failStatus = 422
        failBody   = [ordered]@{ message = $ForgeFailMarker }
    })
    $armCopy = Invoke-Docker @("cp", $rejectControl, "${ContainerName}:$ForgeRoot/control.json.new")
    # Rename inside the container so the server never reads a half-written file.
    $armMove = Invoke-Docker @("exec", $ContainerName, "sh", "-c", "mv '$ForgeRoot/control.json.new' '$ForgeControlPath'")
    Assert-Check "the fake forge is armed to refuse the next create with 422" ($armCopy.Code -eq 0 -and $armMove.Code -eq 0) $armMove.Output

    $prRejected = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/pr" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{
        title = "Drill: rejected by the forge"; body = "This one must not be reported as created."
    }
    Save-Json (Join-Path $ArtifactDir "pr-rejected.json") $prRejected.Body
    Assert-Check "a forge 422 is mirrored as 422 instead of a success" ($prRejected.Status -eq 422) "status=$($prRejected.Status)"
    $rejectedErr = $prRejected.Body.error
    Assert-Check "the refusal is typed as a forge error and names the create step" `
        ($rejectedErr.code -eq "forge_error" -and $rejectedErr.details.step -eq "create" -and
         $rejectedErr.details.forge_status -eq 422) `
        "code=$($rejectedErr.code) step=$($rejectedErr.details.step) forge_status=$($rejectedErr.details.forge_status)"
    Assert-Check "the forge's own refusal text is preserved verbatim" `
        ($rejectedErr.message.Contains($ForgeFailMarker)) "marker_present=$($rejectedErr.message.Contains($ForgeFailMarker))"
    Assert-Check "the refusal is labelled as step 3 (create)" `
        ($rejectedErr.message.Contains("第 3 步（创建 PR）")) $rejectedErr.message

    $forgeRaw2 = (& docker exec $ContainerName cat $ForgeLogPath 2>$null | Out-String)
    $forgeLines2 = @($forgeRaw2 -split "\r?\n" | Where-Object { $_.Trim() })
    $forgeRequests2 = @($forgeLines2 | ForEach-Object { $_ | ConvertFrom-Json })
    ($forgeLines2 | ForEach-Object { $_.Replace($ForgeToken, "<redacted>") }) |
        Set-Content -Path (Join-Path $ArtifactDir "forge-requests.jsonl") -Encoding UTF8
    $failedCreates = @($forgeRequests2 | Where-Object { $_.method -eq "POST" -and $_.status -eq 422 })
    Assert-Check "the forge received exactly one refused create, for the rejected branch" `
        ($failedCreates.Count -eq 1 -and $failedCreates[0].body.head -eq "acme:$PRRejectBranch") `
        "refused=$($failedCreates.Count) head=$(if ($failedCreates.Count -eq 1) { $failedCreates[0].body.head } else { '' })"
    $successfulCreates = @($forgeRequests2 | Where-Object { $_.method -eq "POST" -and $_.status -eq 201 })
    Assert-Check "the idempotent second run did not create a second pull request" `
        ($successfulCreates.Count -eq 1) "successfulCreates=$($successfulCreates.Count)"
    $script:Metrics["forge_requests"] = $forgeRequests2.Count

    # The token crossed the network in a header; it must not have reached the
    # product's log, which is the artifact an operator pastes into a ticket.
    $rawLogAfterForge = (& docker logs $ContainerName 2>&1 | Out-String)
    $forgeTokenHits = ([regex]::Matches($rawLogAfterForge, [regex]::Escape($ForgeToken))).Count
    (Redact-Secrets $rawLogAfterForge) | Set-Content -Path (Join-Path $ArtifactDir "container-after-forge.log") -Encoding UTF8
    Assert-Check "the forge token appears zero times in the container log" ($forgeTokenHits -eq 0) "occurrences=$forgeTokenHits"

    # -----------------------------------------------------------------------
    # 9. Browser panel screenshot (a real page, decoded non-blank)
    # -----------------------------------------------------------------------

    Write-Step "Navigate the sandbox browser to a real page and capture it"

    $browserURL = "http://127.0.0.1:3100/login"
    $tabResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/browser/tabs" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{ url = $browserURL }
    Assert-Check "POST /v1/sessions/{id}/browser/tabs opened a tab" ($tabResp.Status -eq 201) "status=$($tabResp.Status) url=$browserURL"
    Save-Json (Join-Path $ArtifactDir "browser-tab.json") $tabResp.Body
    Start-Sleep -Seconds 3

    $shotResp = $null
    $lastShotError = ""
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        try {
            $shotResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/browser/screenshot" -Session $user.Session -CSRF $user.CSRF -Raw
            if ($shotResp.Status -eq 201) { break }
        }
        catch {
            $lastShotError = $_.Exception.Message
            if ($_.ErrorDetails.Message) { $lastShotError = $_.ErrorDetails.Message }
        }
        Start-Sleep -Seconds 2
    }
    Assert-Check "POST /v1/sessions/{id}/browser/screenshot captured the page" ($null -ne $shotResp -and $shotResp.Status -eq 201) "last error: $lastShotError"
    $shotPath = $shotResp.Body.path
    $shotBytes = [int]$shotResp.Body.bytes
    Assert-Check "the screenshot is a PNG in the session's artifacts (not the checkout)" `
        ($shotPath -match "^artifacts/.+/screenshots/.*\.png$") $shotPath
    Assert-Check "the screenshot has more than 1000 bytes" ($shotBytes -gt 1000) "bytes=$shotBytes"

    $localShot = Join-Path $ArtifactDir "browser-panel.png"
    # The client path is virtual: artifacts/<session>/<relative>. The route takes
    # the session-relative part, so a screenshot can be previewed without a
    # screenshots/ directory ever appearing in the user's repository.
    $shotRel = $shotPath -replace "^artifacts/$([regex]::Escape($sessionID))/", ""
    $encodedShot = [uri]::EscapeDataString($shotRel)
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/v1/sessions/$sessionID/artifacts?path=$encodedShot&download=1" `
        -WebSession $user.Session -OutFile $localShot
    $downloaded = (Get-Item $localShot).Length
    Assert-Check "the PNG downloads from the artifacts route byte-for-byte" ($downloaded -eq $shotBytes) "api=$shotBytes downloaded=$downloaded"

    # The capture is a file the user can preview, not untracked work in their
    # repository: `git status` must still be clean after it.
    $shotStatus = Invoke-Docker -Arguments @("exec", $ContainerName, "git", "-C", $worktree, "status", "--porcelain")
    Assert-Check "the checkout is still clean after the capture" (-not $shotStatus.Output) `
        $(if ($shotStatus.Output) { $shotStatus.Output -replace "`r?`n", "; " } else { "<clean>" })

    $imageMeta = [ordered]@{ path = $shotPath; bytes = $shotBytes; width = 0; height = 0; colours = 0 }
    try {
        Add-Type -AssemblyName System.Drawing
        $bmp = [System.Drawing.Bitmap]::new($localShot)
        try {
            $stepX = [Math]::Max(1, [int]($bmp.Width / 160))
            $stepY = [Math]::Max(1, [int]($bmp.Height / 160))
            $colours = New-Object 'System.Collections.Generic.HashSet[int]'
            for ($y = 0; $y -lt $bmp.Height; $y += $stepY) {
                for ($x = 0; $x -lt $bmp.Width; $x += $stepX) {
                    [void]$colours.Add($bmp.GetPixel($x, $y).ToArgb())
                }
            }
            $imageMeta.width = $bmp.Width
            $imageMeta.height = $bmp.Height
            $imageMeta.colours = $colours.Count
        }
        finally {
            $bmp.Dispose()
        }
    }
    catch {
        throw "could not decode the screenshot PNG: $($_.Exception.Message)"
    }
    Save-Json (Join-Path $ArtifactDir "browser-panel.json") $imageMeta
    Assert-Check "the screenshot is not a blank/flat image" ($imageMeta.colours -gt 8) "$($imageMeta.width)x$($imageMeta.height), $($imageMeta.colours) sampled colours"
    $script:Metrics["screenshot_path"] = $shotPath
    $script:Metrics["screenshot_bytes"] = $shotBytes
    $script:Metrics["screenshot_colours"] = $imageMeta.colours

    # -----------------------------------------------------------------------
    # 9. File tree refreshes without a reload (real browser against the
    #    drill container; no model call, so it does not spend the provider)
    # -----------------------------------------------------------------------

    if (-not $SkipUi) {
        Write-Step "Prove the file tree shows new files without a manual refresh"

        $uiScript = Join-Path $ArtifactDir "ui-check.mjs"
        Set-Content -Path $uiScript -Encoding UTF8 -Value @'
import { chromium } from "@playwright/test";
import path from "node:path";

const base = process.env.DRILL_BASE;
const email = process.env.DRILL_EMAIL;
const password = process.env.DRILL_PASSWORD;
const project = process.env.DRILL_PROJECT;
const session = process.env.DRILL_SESSION;
const out = process.env.DRILL_OUT;

let browser;
try {
  browser = await chromium.launch({ channel: "chrome", headless: true });
} catch {
  browser = await chromium.launch({ headless: true });
}
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
try {
  await page.goto(`${base}/login`);
  await page.getByTestId("login-email").fill(email);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

  await page.goto(`${base}/p/${project}/s/${session}`);
  await page.locator(`[data-session-id="${session}"]`).waitFor({ timeout: 60000 });
  await page.evaluate(() => { window.__drillNoReload = true; });

  await page.getByTestId("panel-tab-files").click();
  await page.getByTestId("file-tree").waitFor({ timeout: 30000 });
  const primes = page.locator('[data-testid="file-node"][data-path="primes.js"]');
  await primes.waitFor({ timeout: 30000 });
  const primesVisible = await primes.isVisible();

  await page.getByTestId("panel-tab-terminal").click();
  const pane = page.getByTestId("terminal-pane");
  await pane.waitFor({ timeout: 60000 });
  for (let i = 0; i < 60; i += 1) {
    if ((await pane.getAttribute("data-pty-status")) === "open") break;
    await page.waitForTimeout(500);
  }
  const input = page.locator(".xterm-helper-textarea").first();
  await input.focus();
  await page.keyboard.type("echo ui-live > ui-live.txt");
  await page.keyboard.press("Enter");

  await page.getByTestId("panel-tab-files").click();
  const live = page.locator('[data-testid="file-node"][data-path="ui-live.txt"]');
  await live.waitFor({ timeout: 30000 });
  const appeared = await live.isVisible();
  const noReload = await page.evaluate(() => window.__drillNoReload === true);
  await page.screenshot({ path: path.join(out, "ui-file-tree.png") });

  // Remove the probe file through the same shell so the checkout is not left
  // with a file a later status assertion would see.
  await page.getByTestId("panel-tab-terminal").click();
  await input.focus();
  await page.keyboard.type("rm -f ui-live.txt");
  await page.keyboard.press("Enter");
  await page.waitForTimeout(1000);

  console.log(JSON.stringify({ ok: true, appeared, noReload, primesVisible }));
} catch (err) {
  console.error(String(err));
  process.exit(1);
} finally {
  await browser.close();
}
'@

        $savedEnv = @{}
        foreach ($name in @("DRILL_BASE", "DRILL_EMAIL", "DRILL_PASSWORD", "DRILL_PROJECT", "DRILL_SESSION", "DRILL_OUT")) {
            $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
        }
        try {
            [Environment]::SetEnvironmentVariable("DRILL_BASE", $BaseUrl, "Process")
            [Environment]::SetEnvironmentVariable("DRILL_EMAIL", $NewUserEmail, "Process")
            [Environment]::SetEnvironmentVariable("DRILL_PASSWORD", $NewUserPassword, "Process")
            [Environment]::SetEnvironmentVariable("DRILL_PROJECT", $projectID, "Process")
            [Environment]::SetEnvironmentVariable("DRILL_SESSION", $sessionID, "Process")
            [Environment]::SetEnvironmentVariable("DRILL_OUT", $ArtifactDir, "Process")

            $uiOutput = & node $uiScript 2>&1 | Out-String
            $uiCode = $LASTEXITCODE
            $uiOutput | Set-Content -Path (Join-Path $ArtifactDir "ui-check.log") -Encoding UTF8
            Assert-Check "the Playwright UI check exited 0" ($uiCode -eq 0) "exit=$uiCode"
            $jsonLine = @($uiOutput -split "`r?`n" | Where-Object { $_.Trim().StartsWith("{") }) | Select-Object -Last 1
            Assert-Check "the Playwright UI check produced a result" ([bool]$jsonLine) $uiOutput.Trim()
            $uiResult = $jsonLine | ConvertFrom-Json
            Save-Json (Join-Path $ArtifactDir "ui-check.json") $uiResult
            Assert-Check "the sheet shows primes.js from the agent's run" ([bool]$uiResult.primesVisible) "primesVisible=$($uiResult.primesVisible)"
            Assert-Check "a file created during the session appears without a page reload" ([bool]$uiResult.appeared -and [bool]$uiResult.noReload) "appeared=$($uiResult.appeared) noReload=$($uiResult.noReload)"
        }
        finally {
            foreach ($name in $savedEnv.Keys) {
                [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], "Process")
            }
        }
    }
    else {
        Write-Host "  [SKIP] UI check disabled by -SkipUi" -ForegroundColor Yellow
        $script:Metrics["ui_check"] = "skipped"
    }

    # -----------------------------------------------------------------------
    # 10. Browser idle collection + container memory
    # -----------------------------------------------------------------------

    Write-Step "Wait out the browser idle timeout and measure the container"

    function Get-ChromiumCount {
        $raw = (& docker exec $ContainerName sh -c "pgrep -c chromium || true" | Out-String).Trim()
        if ($raw -match "^\d+$") { return [int]$raw }
        return 0
    }

    $chromiumActive = Get-ChromiumCount
    $script:Metrics["chromium_processes_active"] = $chromiumActive
    Assert-Check "the screenshot had started the sandbox browser" ($chromiumActive -gt 0) "processes=$chromiumActive"

    $idleStart = [DateTime]::UtcNow
    $chromiumIdle = $chromiumActive
    $idleDeadline = [DateTime]::UtcNow.AddSeconds($BrowserIdleWaitSeconds + 60)
    while ([DateTime]::UtcNow -lt $idleDeadline) {
        $chromiumIdle = Get-ChromiumCount
        if ($chromiumIdle -eq 0) { break }
        Start-Sleep -Seconds 5
    }
    $idleSeconds = ([DateTime]::UtcNow - $idleStart).TotalSeconds
    Assert-Check "Chromium processes after the idle timeout" ($chromiumIdle -eq 0) "processes=$chromiumIdle after $([Math]::Round($idleSeconds,1))s"
    $script:Metrics["chromium_processes_after_idle"] = $chromiumIdle
    $script:Metrics["chromium_idle_seconds"] = [Math]::Round($idleSeconds, 1)

    $statsOut = (& docker stats --no-stream --format "{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}|{{.PIDs}}" $ContainerName | Out-String).Trim()
    $statsOut | Set-Content -Path (Join-Path $ArtifactDir "docker-stats.txt") -Encoding UTF8
    $statsParts = $statsOut -split "\|"
    Assert-Check "docker stats returned a memory reading" ($statsParts.Count -ge 4 -and $statsParts[1] -match "MiB|GiB") $statsOut
    $script:Metrics["memory_usage"] = $statsParts[1]
    $script:Metrics["cpu_percent"] = $statsParts[2]
    $script:Metrics["pids"] = $statsParts[3]

    # -----------------------------------------------------------------------
    # 11. Inline comment -> the agent's next message (FR-12.3)
    #
    # The comment goes through the same POST /messages the composer uses, with
    # the anchor the diff stores (path, line, side, hunk, quote, body). The
    # product composes the prompt server-side; the transcript entry fetched
    # here is the agent's own record of what it was asked, so it proves the
    # quote reached the agent rather than the browser.
    # -----------------------------------------------------------------------

    Write-Step "Comment on a line and prove the agent receives the quote"

    $commentFileStatus = Write-SessionFile -SessionID $sessionID -Session $user.Session -CSRF $user.CSRF `
        -Path $ReviewPath -Text "# delivery drill notes`n$ReviewQuote`nkeep: untouched`n"
    Assert-Check "PUT /files created $ReviewPath to comment on" ($commentFileStatus -eq 200) "status=$commentFileStatus"

    $commentSentAt = [DateTime]::UtcNow
    $commentResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/messages" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{
        client_msg_id  = "drill-comment-$RunID"
        review_comment = @{
            path  = $ReviewPath
            line  = $ReviewLine
            side  = "new"
            hunk  = "@@ -1,3 +1,3 @@"
            quote = @($ReviewQuote)
            body  = $ReviewBody
        }
    }
    Assert-Check "POST /messages accepted the inline comment as the next turn" `
        ($commentResp.Status -eq 200 -and [bool]$commentResp.Body.accepted) `
        "status=$($commentResp.Status) disposition=$($commentResp.Body.disposition)"

    $commentRun = Wait-SessionSettled -SessionID $sessionID -Session $user.Session -TimeoutSeconds $SettleTimeoutSeconds
    $commentTurnSeconds = ([DateTime]::UtcNow - $commentSentAt).TotalSeconds
    Assert-Check "the inline-comment turn started and settled" ([bool]$commentRun.Settled) `
        "settled=$([Math]::Round($commentTurnSeconds,2))s last_seq=$($commentRun.Status.last_seq)"
    $script:Metrics["review_comment_turn_seconds"] = [Math]::Round($commentTurnSeconds, 2)

    $commentEntries = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/entries" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "entries-comment.json") $commentEntries
    $commentEntryList = @($commentEntries.entries)
    $anchor = "行内评论 · $ReviewPath`:$ReviewLine"
    $commentTurn = @($commentEntryList | Where-Object {
        $_.role -eq "user" -and $_.preview -like "*行内评论*" -and $_.preview -like "*$ReviewPath`:$ReviewLine*"
    })
    Assert-Check "the transcript has one user turn anchored at $ReviewPath`:$ReviewLine" `
        ($commentTurn.Count -ge 1) "turns=$($commentTurn.Count) entries=$($commentEntryList.Count)"
    $commentPreview = $commentTurn[0].preview
    Assert-Check "the agent's message starts with the inline-comment anchor" `
        ($commentPreview.StartsWith($anchor)) $commentPreview
    Assert-Check "the agent's message quotes the commented line" `
        ($commentPreview.Contains("$ReviewLine | $ReviewQuote")) $commentPreview
    Assert-Check "the agent's message carries the comment body" `
        ($commentPreview.Contains($ReviewBody)) $commentPreview
    Assert-Check "the message says where the quoted code is now" `
        ($commentPreview.Contains("这段代码现在仍在第 $ReviewLine 行")) $commentPreview
    $commentPreview | Set-Content -Path (Join-Path $ArtifactDir "review-comment-message.txt") -Encoding UTF8
    $script:Metrics["review_comment_anchor"] = $anchor

    # The turn was asked not to touch files; if it did, restore the drill
    # checkout through the product's own discard so the sandbox is not left
    # dirty (the container is thrown away either way, but a clean end state is
    # the honest one and proves the guard below is about the *source* tree).
    $postCommentStatus = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/git/status" -Session $user.Session
    $discarded = 0
    foreach ($change in @($postCommentStatus.changes)) {
        $discardResp = Invoke-Api -Method POST -Path "/v1/sessions/$sessionID/git/discard" -Session $user.Session -CSRF $user.CSRF -Raw -Body @{ path = $change.path }
        if ($discardResp.Status -eq 200) { $discarded++ }
    }
    $postCommentStatus = Invoke-Api -Method GET -Path "/v1/sessions/$sessionID/git/status" -Session $user.Session
    Save-Json (Join-Path $ArtifactDir "git-status-after-comment.json") $postCommentStatus
    Record-Check "the inline-comment turn left the drill checkout clean" ([bool]$postCommentStatus.clean) `
        "discarded=$discarded clean=$($postCommentStatus.clean)"

    # -----------------------------------------------------------------------
    # 12. Teardown happens in finally; the pf-local assertion is there too.
    # -----------------------------------------------------------------------
}
catch {
    $script:Failure = $_.Exception.Message
}
finally {
    Write-Step "Teardown and non-interference checks"

    if ($script:CreatedContainer) {
        $rm = Invoke-Docker @("rm", "-f", $ContainerName)
        Record-Check "docker rm -f $ContainerName" ($rm.Code -eq 0) $rm.Output
    }
    if ($script:CreatedVolume) {
        $rmv = Invoke-Docker @("volume", "rm", $VolumeName)
        Record-Check "docker volume rm $VolumeName" ($rmv.Code -eq 0) $rmv.Output
    }

    $leftover = @(& docker ps -a --format "{{.Names}}")
    Record-Check "the drill container is gone" (-not ($leftover -contains $ContainerName)) $ContainerName
    $leftoverVolumes = @(& docker volume ls --format "{{.Name}}")
    Record-Check "the drill volume is gone" (-not ($leftoverVolumes -contains $VolumeName)) $VolumeName

    $existingAfter = Get-ContainerState -Name $ExistingContainer -HealthPort $ExistingPort
    if ($existingBefore -and $existingBefore.Exists) {
        Record-Check "$ExistingContainer still has the same container id" ($existingAfter.Exists -and $existingAfter.ID -eq $existingBefore.ID) "before=$($existingBefore.ID) after=$($existingAfter.ID)"
        Record-Check "$ExistingContainer is still running" ([bool]$existingAfter.Running) "running=$($existingAfter.Running)"
        Record-Check "$ExistingContainer /healthz is still 200" ([bool]$existingAfter.Healthz) "http://127.0.0.1:$ExistingPort/healthz"
    }

    # -----------------------------------------------------------------------
    # Regression guard (the last thing the drill does): the run must not dirty
    # the checkout it was started from, and it must not leave a container or a
    # volume in the Docker data behind. Artifacts live under e2e/.artifacts,
    # which is ignored; screenshots live in the data volume, which is deleted.
    # -----------------------------------------------------------------------
    $sourceStatus = (& git -C $RepoRoot status --porcelain 2>&1 | Out-String).Trim()
    $sourceStatus | Set-Content -Path (Join-Path $ArtifactDir "source-git-status.txt") -Encoding UTF8
    Record-Check "the source checkout is clean after the drill" (-not $sourceStatus) `
        $(if ($sourceStatus) { $sourceStatus -replace "`r?`n", "; " } else { "<clean>" })

    $remainingContainers = @(& docker ps -a --format "{{.Names}}" | Where-Object { $_ -like "$ContainerName*" })
    $remainingContainers | Set-Content -Path (Join-Path $ArtifactDir "leftover-containers.txt") -Encoding UTF8
    Record-Check "no '$ContainerName*' container is left behind" ($remainingContainers.Count -eq 0) `
        $(if ($remainingContainers.Count) { $remainingContainers -join ", " } else { "<none>" })

    $remainingVolumes = @(& docker volume ls --format "{{.Name}}" | Where-Object { $_ -like "$VolumeName*" -or $_ -like "$ContainerName*" })
    $remainingVolumes | Set-Content -Path (Join-Path $ArtifactDir "leftover-volumes.txt") -Encoding UTF8
    Record-Check "no drill volume is left behind" ($remainingVolumes.Count -eq 0) `
        $(if ($remainingVolumes.Count) { $remainingVolumes -join ", " } else { "<none>" })

    # Never leave a secret in an artifact. Scan text artifacts only; the PNGs
    # are binary and are checked by their own assertions above.
    $leaks = 0
    $tokenLeaks = 0
    foreach ($file in Get-ChildItem -Path $ArtifactDir -File) {
        if ($file.Extension -in @(".png", ".jpg", ".jpeg", ".webp")) { continue }
        $text = Get-Content -Path $file.FullName -Raw -ErrorAction SilentlyContinue
        if ($text -and $text.Contains($Key)) { $leaks++ }
        if ($text -and $text.Contains($ForgeToken)) { $tokenLeaks++ }
    }
    Record-Check "the provider key appears in zero text artifacts" ($leaks -eq 0) "files=$leaks"
    Record-Check "the forge token appears in zero text artifacts" ($tokenLeaks -eq 0) "files=$tokenLeaks"

    $script:FinishedAt = [DateTime]::UtcNow
    $script:Metrics["total_seconds"] = [Math]::Round(($script:FinishedAt - $script:StartedAt).TotalSeconds, 1)
    $summary = [pscustomobject]@{
        run_id      = $RunID
        started_at  = $script:StartedAt.ToString("o")
        finished_at = $script:FinishedAt.ToString("o")
        image       = $Image
        port        = $Port
        provider    = "$ProviderName|$ProviderBaseURL|$ProviderModel"
        new_user    = $NewUserEmail
        artifacts   = $ArtifactDir
        failure     = $script:Failure
        checks      = $script:Checks
        metrics     = $script:Metrics
    }
    Save-Json (Join-Path $ArtifactDir "summary.json") $summary

    Write-Host ""
    Write-Host "Summary" -ForegroundColor Cyan
    $script:Checks | Format-Table -AutoSize -Property Check, Result, Detail | Out-String -Width 240 | Write-Host

    Write-Host "Measured" -ForegroundColor Cyan
    $script:Metrics.GetEnumerator() | ForEach-Object {
        [pscustomobject]@{ Metric = $_.Key; Value = $_.Value }
    } | Format-Table -AutoSize | Out-String -Width 200 | Write-Host

    Write-Host "Artifacts: $ArtifactDir" -ForegroundColor Cyan

    $failed = @($script:Checks | Where-Object { $_.Result -eq "FAIL" })
    if ($script:Failure) {
        Write-Host ""
        Write-Host "DRILL FAILED: $($script:Failure)" -ForegroundColor Red
    }
    elseif ($failed.Count -gt 0) {
        Write-Host ""
        Write-Host "DRILL FAILED: $($failed.Count) assertion(s)" -ForegroundColor Red
        $script:Failure = "assertions failed"
    }
}

if ($script:Failure) { exit 1 }
exit 0
