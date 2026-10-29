# Verify the docker sandbox end to end, the way a human did it by hand.
#
# The checks below are the defects that were found by manually starting the
# product with `-sandbox docker`, driving it over HTTP and inspecting the
# containers. Each one is written as an assertion, and several of them only pass
# when a *negative* result holds (a second project cannot see the first one's
# file, the panel cannot read the host after the container stops). Those are the
# ones that regress silently, so they are the reason this script exists.
#
#   pwsh scripts/verify-docker-sandbox.ps1
#   pwsh scripts/verify-docker-sandbox.ps1 -Image <all-in-one-sandbox:tag>
#   $env:PIFORGE_SANDBOX_IMAGE = "<image>"; pwsh scripts/verify-docker-sandbox.ps1
#
# Requirements: Docker (daemon reachable), the AIO sandbox image already pulled,
# Go, and PowerShell 7. The script builds its own binaries into a temp directory,
# starts its own piforged on a free port, and removes everything it created in a
# finally block. It never touches a container named `pf-local` or `piforge`.
#
# Expected runtime: 5-8 minutes on a warm machine (the image starts twice, and
# Chromium has to come up inside one of them for the screenshot check).
param(
    [string]$Image = "",
    [int]$StartTimeoutSec = 120
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $root "backend"
$defaultImage = "enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox:1.11.0"
if ([string]::IsNullOrWhiteSpace($Image)) {
    $Image = [Environment]::GetEnvironmentVariable("PIFORGE_SANDBOX_IMAGE", "Process")
}
if ([string]::IsNullOrWhiteSpace($Image)) {
    $Image = $defaultImage
}

$script:results = [System.Collections.Generic.List[object]]::new()
$script:failed = $false
$script:containers = @{}          # names we created, recorded as soon as we see them
$script:projectIDs = [System.Collections.Generic.List[string]]::new()
$script:csrf = ""
$script:base = ""
$script:web = $null
$script:proc = $null
$script:tmp = ""
$script:data = ""
$script:outLog = ""
$script:errLog = ""

# Start-Process joins this list with spaces, so a value with a space has to be
# quoted for CreateProcess; every other argument is passed verbatim.
function Quote-Arg {
    param([string]$Value)
    if ($Value -match '\s') { return '"' + $Value + '"' }
    return $Value
}

function Add-Result {
    param([string]$Name, [bool]$Passed, [string]$Detail)
    $status = if ($Passed) { "PASS" } else { "FAIL" }
    $color = if ($Passed) { "Green" } else { "Red" }
    if ([string]::IsNullOrWhiteSpace($Detail)) { $Detail = "" }
    Write-Host ("[{0}] {1} - {2}" -f $status, $Name, $Detail) -ForegroundColor $color
    $script:results.Add([pscustomobject]@{
            Status = $status
            Check  = $Name
            Detail = $Detail
        })
    if (-not $Passed) { $script:failed = $true }
}

function Invoke-Check {
    param([string]$Name, [scriptblock]$Body)
    try {
        $detail = & $Body
        Add-Result $Name $true ([string]$detail)
    }
    catch {
        Add-Result $Name $false ($_.Exception.Message -replace "`r?`n", " ")
    }
}

function Invoke-Docker {
    param([string[]]$Arguments)
    $out = & docker @Arguments 2>&1
    $code = $LASTEXITCODE
    return [pscustomobject]@{
        Code   = $code
        Output = (($out | ForEach-Object { "$_" }) -join "`n")
    }
}

function Wait-ContainerRunning {
    param([string]$Name, [int]$TimeoutSec = 90)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        $r = Invoke-Docker @("inspect", "--format", "{{.State.Running}}", $Name)
        if ($r.Code -eq 0) {
            $last = $r.Output.Trim()
            if ($last -eq "true") { return }
        }
        else {
            $last = $r.Output
        }
        Start-Sleep -Milliseconds 400
    }
    throw "container $Name did not reach running within ${TimeoutSec}s: $last"
}

function Register-Container {
    param([string]$Name)
    if ([string]::IsNullOrWhiteSpace($Name)) { throw "an empty container name was resolved" }
    if ($Name -in @("pf-local", "piforge")) {
        throw "refusing to treat $Name as a container this script created"
    }
    if ($Name -notmatch '^piforge-[0-9a-f]+$') {
        throw "refusing to record unexpected container name '$Name'"
    }
    $script:containers[$Name] = $true
}

function Get-ServerLog {
    $parts = @()
    foreach ($f in @($script:outLog, $script:errLog)) {
        if ($f -and (Test-Path -LiteralPath $f)) {
            $parts += (Get-Content -LiteralPath $f -Raw -ErrorAction SilentlyContinue)
        }
    }
    return (($parts | Where-Object { $null -ne $_ }) -join "`n")
}

function Show-ServerLog {
    param([int]$Tail = 200)
    $present = @(@($script:outLog, $script:errLog) | Where-Object { $_ -and (Test-Path -LiteralPath $_) })
    if ($present.Count -eq 0) { return }
    Write-Host ""
    Write-Host "---- piforged output (last $Tail lines) ----" -ForegroundColor Yellow
    foreach ($f in $present) {
        Write-Host "== $f =="
        Get-Content -LiteralPath $f -Tail $Tail -ErrorAction SilentlyContinue
    }
    Write-Host "---- end piforged output ----" -ForegroundColor Yellow
}

function Get-Json {
    param($Response)
    if ($null -eq $Response) { return $null }
    $text = [string]$Response.Content
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    return ($text | ConvertFrom-Json)
}

function Expect-Status {
    param($Response, [int]$Expected, [string]$What)
    if ($null -eq $Response) { throw "$What returned no response" }
    if ([int]$Response.StatusCode -ne $Expected) {
        $body = [string]$Response.Content
        if ($body.Length -gt 600) { $body = $body.Substring(0, 600) + "..." }
        throw "$What returned $($Response.StatusCode), expected ${Expected}: $body"
    }
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Json = $null,
        [byte[]]$Bytes = $null,
        [string]$ContentType = "application/json",
        [int]$TimeoutSec = 180
    )
    $headers = @{}
    if (-not [string]::IsNullOrEmpty($script:csrf)) { $headers["X-PF-CSRF"] = $script:csrf }
    $params = @{
        Uri                = "$($script:base)$Path"
        Method             = $Method
        WebSession         = $script:web
        Headers            = $headers
        SkipHttpErrorCheck = $true
        NoProxy            = $true
        TimeoutSec         = $TimeoutSec
    }
    if ($null -ne $Json) {
        $params.Body = [Text.Encoding]::UTF8.GetBytes(($Json | ConvertTo-Json -Depth 12 -Compress))
        $params.ContentType = "application/json"
    }
    elseif ($null -ne $Bytes) {
        $params.Body = $Bytes
        $params.ContentType = $ContentType
    }
    return (Invoke-WebRequest @params)
}

function New-PfProject {
    param([string]$Name)
    $resp = Invoke-Api -Method Post -Path "/v1/projects" -Json @{
        name           = $Name
        default_branch = "main"
    } -TimeoutSec 60
    Expect-Status $resp 201 "POST /v1/projects ($Name)"
    $id = (Get-Json $resp).project.id
    if ([string]::IsNullOrWhiteSpace($id)) { throw "project $Name has no id in the response" }
    $script:projectIDs.Add($id)
    return $id
}

function New-PfSession {
    param([string]$ProjectID, [string]$Kind, [string]$Label = "")
    $body = @{ kind = $Kind; title = "verify $Kind" }
    if (-not [string]::IsNullOrWhiteSpace($Label)) { $body.label = $Label }
    $resp = Invoke-Api -Method Post -Path "/v1/projects/$ProjectID/sessions" -Json $body -TimeoutSec 300
    Expect-Status $resp 201 "POST session ($Kind)"
    $sess = (Get-Json $resp).session
    if ($null -eq $sess -or [string]::IsNullOrWhiteSpace($sess.id)) {
        throw "session creation returned no session"
    }
    return $sess
}

function Get-PfContainer {
    param([string]$SessionID)
    $resp = Invoke-Api -Method Get -Path "/v1/sessions/$SessionID/sandbox" -TimeoutSec 60
    Expect-Status $resp 200 "GET /v1/sessions/$SessionID/sandbox"
    $id = (Get-Json $resp).id
    Register-Container $id
    return $id
}

function Get-ContainerFileHex {
    param([string]$Container, [string]$Path)
    $r = Invoke-Docker @("exec", $Container, "od", "-An", "-tx1", "-v", $Path)
    if ($r.Code -ne 0) {
        throw "docker exec od $Path failed (exit $($r.Code)): $($r.Output)"
    }
    return (($r.Output -replace '\s+', '').ToLowerInvariant())
}

function Get-BytesHex {
    param([byte[]]$Bytes)
    return ([Convert]::ToHexString($Bytes)).ToLowerInvariant()
}

function Wait-Health {
    param([int]$TimeoutSec)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        if ($script:proc.HasExited) {
            throw "piforged exited during startup with code $($script:proc.ExitCode): $((Get-ServerLog).Trim())"
        }
        try {
            $r = Invoke-WebRequest -Uri "$($script:base)/healthz" -TimeoutSec 2 -SkipHttpErrorCheck -NoProxy
            if ([int]$r.StatusCode -eq 200) { return }
        }
        catch {
            # The listener is not up yet, or the process just died; both are
            # handled by the next iteration and the HasExited check above.
        }
        Start-Sleep -Milliseconds 300
    }
    throw "piforged did not answer /healthz within ${TimeoutSec}s"
}

function Remove-OurContainers {
    # Two proofs that a container is ours: its name was recorded from a session
    # we created, or it carries the project label of a project we created. The
    # second one catches a container whose sandbox row was never written (for
    # example, Ensure failed after docker run). Both exclude pf-local/piforge.
    $byLabel = @()
    foreach ($projectID in $script:projectIDs) {
        $r = Invoke-Docker @("ps", "-aq", "--filter", "label=piforge.project=$projectID")
        if ($r.Code -eq 0 -and -not [string]::IsNullOrWhiteSpace($r.Output)) {
            $byLabel += @($r.Output -split "`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        }
    }
    foreach ($id in $byLabel) {
        $nameResult = Invoke-Docker @("inspect", "--format", "{{.Name}}", $id.Trim())
        if ($nameResult.Code -ne 0) { continue }
        $name = $nameResult.Output.Trim().TrimStart("/")
        if ($name -in @("pf-local", "piforge")) { continue }
        if ($name -match '^piforge-[0-9a-f]+$') { $script:containers[$name] = $true }
    }
    foreach ($name in @($script:containers.Keys)) {
        if ($name -in @("pf-local", "piforge")) { continue }
        if ($name -notmatch '^piforge-[0-9a-f]+$') { continue }
        $r = Invoke-Docker @("rm", "-f", $name)
        if ($r.Code -ne 0) {
            Write-Host "warning: docker rm -f $name failed: $($r.Output)" -ForegroundColor Yellow
        }
    }
}

# ---------------------------------------------------------------------------
# Preconditions and build
# ---------------------------------------------------------------------------

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "BUILD/PRECONDITION FAILED: docker is not on PATH" -ForegroundColor Red
    exit 1
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "BUILD/PRECONDITION FAILED: go is not on PATH" -ForegroundColor Red
    exit 1
}

$daemon = Invoke-Docker @("version", "--format", "{{.Server.Version}}")
if ($daemon.Code -ne 0) {
    Write-Host "BUILD/PRECONDITION FAILED: docker daemon is not reachable: $($daemon.Output)" -ForegroundColor Red
    exit 1
}
$imageCheck = Invoke-Docker @("image", "inspect", $Image)
if ($imageCheck.Code -ne 0) {
    Write-Host "BUILD/PRECONDITION FAILED: sandbox image '$Image' is not present locally." -ForegroundColor Red
    Write-Host "Pull it first: docker pull $Image"
    exit 1
}

$script:tmp = Join-Path ([IO.Path]::GetTempPath()) ("piforge-verify-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
$bin = Join-Path $script:tmp "bin"
$script:data = Join-Path $script:tmp "data"
$script:outLog = Join-Path $script:tmp "piforged.out.log"
$script:errLog = Join-Path $script:tmp "piforged.err.log"
New-Item -ItemType Directory -Force -Path $bin, $script:data | Out-Null

try {
    Write-Host "building piforged and fakepi into $bin"
    Push-Location $backend
    try {
        $buildOut = & go build -trimpath -o (Join-Path $bin "piforged.exe") ./cmd/piforged 2>&1
        if ($LASTEXITCODE -ne 0) {
            $buildOut | ForEach-Object { Write-Host "$_" }
            Write-Host "BUILD FAILED: go build ./cmd/piforged" -ForegroundColor Red
            throw "go build ./cmd/piforged failed"
        }
        $buildOut = & go build -trimpath -o (Join-Path $bin "fakepi.exe") ./cmd/fakepi 2>&1
        if ($LASTEXITCODE -ne 0) {
            $buildOut | ForEach-Object { Write-Host "$_" }
            Write-Host "BUILD FAILED: go build ./cmd/fakepi" -ForegroundColor Red
            throw "go build ./cmd/fakepi failed"
        }
    }
    finally {
        Pop-Location
    }
    Add-Result "build: piforged + fakepi" $true $bin

    # -----------------------------------------------------------------------
    # Start the server on a free loopback port
    # -----------------------------------------------------------------------

    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $srvPort = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    $script:base = "http://127.0.0.1:$srvPort"

    $envNames = @(
        "PIFORGE_SEED_EMAIL", "PIFORGE_SEED_PASSWORD",
        "PIFORGE_SEED_USER_EMAIL", "PIFORGE_SEED_USER_PASSWORD"
    )
    $savedEnv = @{}
    foreach ($name in $envNames) {
        $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    }
    $env:PIFORGE_SEED_EMAIL = "admin@piforge.local"
    $env:PIFORGE_SEED_PASSWORD = "piforge-admin-123"
    $env:PIFORGE_SEED_USER_EMAIL = "dev@piforge.local"
    $env:PIFORGE_SEED_USER_PASSWORD = "piforge-dev-123"

    $apiKey = "verify-" + [guid]::NewGuid().ToString("N")
    $argList = @(
        "-addr", "127.0.0.1:$srvPort",
        "-data", (Quote-Arg $script:data),
        "-dev",
        "-fake-pi",
        "-sandbox", "docker",
        "-sandbox-image", (Quote-Arg $Image),
        "-sandbox-api-key", $apiKey
    )
    try {
        Write-Host "starting piforged on $($script:base) (image $Image)"
        $script:proc = Start-Process -FilePath (Join-Path $bin "piforged.exe") `
            -ArgumentList $argList -WorkingDirectory $root `
            -RedirectStandardOutput $script:outLog -RedirectStandardError $script:errLog `
            -PassThru -NoNewWindow
    }
    finally {
        foreach ($name in $envNames) {
            [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], "Process")
        }
    }

    Wait-Health $StartTimeoutSec
    Start-Sleep -Milliseconds 300

    # -----------------------------------------------------------------------
    # Login (cookie session + X-PF-CSRF)
    # -----------------------------------------------------------------------

    $script:web = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    $login = Invoke-Api -Method Post -Path "/v1/auth/login" -Json @{
        email    = "admin@piforge.local"
        password = "piforge-admin-123"
    } -TimeoutSec 30
    Expect-Status $login 200 "POST /v1/auth/login"
    $loginBody = Get-Json $login
    $script:csrf = $loginBody.csrf
    if ([string]::IsNullOrWhiteSpace($script:csrf)) {
        $cookie = $script:web.Cookies.GetCookies([uri]$script:base) | Where-Object { $_.Name -eq "pf_csrf" }
        if ($null -ne $cookie) { $script:csrf = $cookie.Value }
    }
    if ([string]::IsNullOrWhiteSpace($script:csrf)) { throw "login returned no CSRF token" }
    $userID = $loginBody.user.id
    if ([string]::IsNullOrWhiteSpace($userID)) { throw "login returned no user id" }

    # -----------------------------------------------------------------------
    # Project A: a main session gets a sandbox and a checkout
    # -----------------------------------------------------------------------

    $projA = New-PfProject "verify-a"
    $sessA = New-PfSession $projA "main"
    if ([string]::IsNullOrWhiteSpace($sessA.worktree_id)) {
        throw "the main session has no worktree; provisioning failed (see the server log)"
    }
    $cA = Get-PfContainer $sessA.id
    Wait-ContainerRunning $cA
    $repoA = "/workspace/repo"

    # -----------------------------------------------------------------------
    # Checks
    # -----------------------------------------------------------------------

    Invoke-Check "placement: docker resolves to all-remote" {
        $log = Get-ServerLog
        $resolved = $log -match 'msg="sandbox placement resolved"[^\r\n]*sandbox=docker[^\r\n]*browser=remote[^\r\n]*terminal=remote[^\r\n]*files=remote'
        if (-not $resolved) {
            throw "no 'sandbox placement resolved' line with sandbox=docker browser=remote terminal=remote files=remote"
        }
        if ($log -match 'msg="sandbox placement is mixed') {
            throw "the log contains a mixed-placement WARN"
        }
        "sandbox=docker browser=remote terminal=remote files=remote, no mixed WARN"
    }

    Invoke-Check "container: piforge-* exists with 8080 published" {
        $inspect = Invoke-Docker @("inspect", "--format", "{{.State.Running}}", $cA)
        if ($inspect.Code -ne 0) { throw "docker inspect $cA failed: $($inspect.Output)" }
        if ($inspect.Output.Trim() -ne "true") { throw "$cA is not running" }
        $port = Invoke-Docker @("port", $cA, "8080/tcp")
        if ($port.Code -ne 0 -or $port.Output -notmatch ':\d+\s*$') {
            throw "$cA does not publish 8080: $($port.Output)"
        }
        "$cA running, port $($port.Output.Trim())"
    }

    Invoke-Check "files: panel write is visible in the container with the same bytes" {
        $payload = "panel-write: " + [guid]::NewGuid().ToString("N") + " héllo`n"
        $bytes = [Text.Encoding]::UTF8.GetBytes($payload)
        $resp = Invoke-Api -Method Put -Path "/v1/sessions/$($sessA.id)/files?path=panel.txt" `
            -Bytes $bytes -ContentType "text/plain; charset=utf-8" -TimeoutSec 60
        Expect-Status $resp 200 "PUT /files?path=panel.txt"
        $containerHex = Get-ContainerFileHex $cA "$repoA/panel.txt"
        $localHex = Get-BytesHex $bytes
        if ($containerHex -ne $localHex) {
            throw "container bytes differ from the panel write (container $($containerHex.Length / 2) bytes, panel $($bytes.Length))"
        }
        "$($bytes.Length) bytes, identical UTF-8 content in $repoA/panel.txt"
    }

    Invoke-Check "git: status/stage/commit are 200" {
        $write = Invoke-Api -Method Put -Path "/v1/sessions/$($sessA.id)/files?path=a.txt" `
            -Bytes ([Text.Encoding]::UTF8.GetBytes("project A only`n")) `
            -ContentType "text/plain; charset=utf-8" -TimeoutSec 60
        Expect-Status $write 200 "PUT /files?path=a.txt"

        $status = Invoke-Api -Method Get -Path "/v1/sessions/$($sessA.id)/git/status" -TimeoutSec 120
        Expect-Status $status 200 "GET /git/status"
        $statusBody = Get-Json $status
        $paths = @($statusBody.changes | ForEach-Object { $_.path })
        if ($paths -notcontains "a.txt") { throw "git status does not list a.txt: $($paths -join ', ')" }

        $stage = Invoke-Api -Method Post -Path "/v1/sessions/$($sessA.id)/git/stage" `
            -Json @{ paths = @("a.txt") } -TimeoutSec 120
        Expect-Status $stage 200 "POST /git/stage"

        $commit = Invoke-Api -Method Post -Path "/v1/sessions/$($sessA.id)/git/commit" `
            -Json @{ message = "verify: project A only" } -TimeoutSec 120
        Expect-Status $commit 200 "POST /git/commit"
        $sha = (Get-Json $commit).commit
        if ([string]::IsNullOrWhiteSpace($sha)) { throw "commit returned no hash" }
        $script:commitA = $sha
        "status 200 (a.txt listed), stage 200, commit 200 ($($sha.Substring(0, 7)))"
    }

    Invoke-Check "git: the commit author is the container identity" {
        $r = Invoke-Docker @(
            "exec", "-u", "gem", $cA,
            "git", "-c", "safe.directory=$repoA", "-C", $repoA,
            "log", "-1", "--format=%ae"
        )
        if ($r.Code -ne 0) { throw "git log in the container failed: $($r.Output)" }
        $author = $r.Output.Trim()
        if ($author -ne "sandbox@piforge.local") {
            throw "commit author is '$author', want sandbox@piforge.local (the container's git identity)"
        }
        "docker exec -u gem git log -1 --format=%ae -> $author"
    }

    $script:projB = $null
    $script:containerB = $null
    Invoke-Check "isolation: project B does not see project A's a.txt" {
        $script:projB = New-PfProject "verify-b"
        $sessB = New-PfSession $script:projB "main"
        if ([string]::IsNullOrWhiteSpace($sessB.worktree_id)) { throw "project B's session has no worktree" }
        $script:containerB = Get-PfContainer $sessB.id
        Wait-ContainerRunning $script:containerB

        $r = Invoke-Docker @("exec", $script:containerB, "test", "-e", "/workspace/repo/a.txt")
        if ($r.Code -ne 1) {
            throw "docker exec test -e a.txt in project B exited $($r.Code), expected 1 (it can see project A's file): $($r.Output)"
        }
        "project B ($($script:containerB)): test -e /workspace/repo/a.txt -> exit 1"
    }

    Invoke-Check "isolation: the two projects have different /workspace mount sources" {
        if ([string]::IsNullOrWhiteSpace($script:containerB)) {
            throw "project B's container was not created, so the mount sources cannot be compared"
        }
        $mountOf = {
            param([string]$Name)
            $r = Invoke-Docker @(
                "inspect", "--format",
                '{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Source}}{{end}}{{end}}',
                $Name
            )
            if ($r.Code -ne 0) { throw "docker inspect mounts $Name failed: $($r.Output)" }
            return $r.Output.Trim()
        }
        $mountA = & $mountOf $cA
        $mountB = & $mountOf $script:containerB
        if ([string]::IsNullOrWhiteSpace($mountA) -or [string]::IsNullOrWhiteSpace($mountB)) {
            throw "a container has no /workspace mount (A='$mountA', B='$mountB')"
        }
        if ($mountA -eq $mountB) {
            throw "both projects mount the same host directory: $mountA"
        }
        "A=$mountA B=$mountB"
    }

    $script:branchSlug = "verify-branch"
    Invoke-Check "branch session: write/stage/commit are 200" {
        $branch = New-PfSession $projA "branch" $script:branchSlug
        if ([string]::IsNullOrWhiteSpace($branch.worktree_id)) {
            throw "the branch session has no worktree"
        }
        $script:branchID = $branch.id

        $write = Invoke-Api -Method Put -Path "/v1/sessions/$($branch.id)/files?path=branch.txt" `
            -Bytes ([Text.Encoding]::UTF8.GetBytes("branch only`n")) `
            -ContentType "text/plain; charset=utf-8" -TimeoutSec 60
        Expect-Status $write 200 "PUT branch /files?path=branch.txt"

        $status = Invoke-Api -Method Get -Path "/v1/sessions/$($branch.id)/git/status" -TimeoutSec 120
        Expect-Status $status 200 "GET branch /git/status"
        $stage = Invoke-Api -Method Post -Path "/v1/sessions/$($branch.id)/git/stage" `
            -Json @{ paths = @("branch.txt") } -TimeoutSec 120
        Expect-Status $stage 200 "POST branch /git/stage"
        $commit = Invoke-Api -Method Post -Path "/v1/sessions/$($branch.id)/git/commit" `
            -Json @{ message = "verify: branch session commit" } -TimeoutSec 120
        Expect-Status $commit 200 "POST branch /git/commit"
        "status 200, stage 200, commit 200 ($((Get-Json $commit).commit.Substring(0, 7)))"
    }

    Invoke-Check "branch session: .git/worktrees/<slug> is owned by gem" {
        $wtMeta = "$repoA/.git/worktrees/$($script:branchSlug)"
        $r = Invoke-Docker @("exec", "-u", "gem", $cA, "stat", "-c", "%U", $wtMeta)
        if ($r.Code -ne 0) { throw "stat $wtMeta failed: $($r.Output)" }
        $owner = $r.Output.Trim()
        if ($owner -ne "gem") {
            throw "$wtMeta is owned by '$owner', want gem (the worktree was created on the host)"
        }
        "docker exec -u gem stat -c %U $wtMeta -> $owner"
    }

    Invoke-Check "terminal: WS /pty hostname equals the container hostname" {
        $r = Invoke-Docker @("exec", $cA, "hostname")
        if ($r.Code -ne 0) { throw "docker exec hostname failed: $($r.Output)" }
        $containerHost = $r.Output.Trim()
        if ([string]::IsNullOrWhiteSpace($containerHost)) { throw "the container reported an empty hostname" }

        $cookieHeader = ((@($script:web.Cookies.GetCookies([uri]$script:base)) |
                    ForEach-Object { "$($_.Name)=$($_.Value)" }) -join "; ")
        $ws = [System.Net.WebSockets.ClientWebSocket]::new()
        $cts = [Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds(60))
        try {
            $ws.Options.SetRequestHeader("Cookie", $cookieHeader)
            $wsUrl = "$($script:base -replace '^http', 'ws')/v1/sessions/$($sessA.id)/pty"
            $ws.ConnectAsync([uri]$wsUrl, $cts.Token).GetAwaiter().GetResult() | Out-Null

            $input = (@{ type = "input"; data = "hostname`n" } | ConvertTo-Json -Compress)
            $inputBytes = [Text.Encoding]::UTF8.GetBytes($input)
            $ws.SendAsync(
                [ArraySegment[byte]]::new($inputBytes),
                [System.Net.WebSockets.WebSocketMessageType]::Text,
                $true, $cts.Token).GetAwaiter().GetResult() | Out-Null

            $buf = New-Object byte[] 16384
            $transcript = [Text.StringBuilder]::new()
            $found = $false
            while ($true) {
                $result = $ws.ReceiveAsync(
                    [ArraySegment[byte]]::new($buf), $cts.Token).GetAwaiter().GetResult()
                if ($result.MessageType -eq [System.Net.WebSockets.WebSocketMessageType]::Close) { break }
                $chunk = [Text.Encoding]::UTF8.GetString($buf, 0, $result.Count)
                if ($result.MessageType -eq [System.Net.WebSockets.WebSocketMessageType]::Text) {
                    if ($chunk -match '"type"\s*:\s*"error"') { throw "pty socket error frame: $chunk" }
                    continue
                }
                [void]$transcript.Append($chunk)
                $lines = $transcript.ToString() -split '\r?\n'
                foreach ($line in $lines) {
                    if ($line.Trim() -eq $containerHost) { $found = $true; break }
                }
                if ($found) { break }
            }
            if (-not $found) {
                $text = $transcript.ToString()
                if ($text.Length -gt 500) { $text = $text.Substring($text.Length - 500) }
                throw "pty never printed '$containerHost'; tail: $($text -replace '\r?\n', ' | ')"
            }
        }
        catch [System.OperationCanceledException] {
            throw "pty did not print '$containerHost' within 60s"
        }
        finally {
            try { $ws.Dispose() } catch { }
            $cts.Dispose()
        }
        "WS /pty printed '$containerHost', the same hostname docker exec reports"
    }

    Invoke-Check "browser: screenshot is a PNG and the same file is inside the container" {
        $shotResp = Invoke-Api -Method Post -Path "/v1/sessions/$($sessA.id)/browser/screenshot" -TimeoutSec 120
        Expect-Status $shotResp 201 "POST /browser/screenshot"
        $shot = Get-Json $shotResp
        if ([string]::IsNullOrWhiteSpace($shot.path)) { throw "screenshot returned no path" }
        $declared = [int]$shot.bytes
        if ($declared -le 0) { throw "screenshot returned $declared bytes" }

        # The download endpoint runs the workspace ReadFile first, and the
        # container's file API refuses a PNG as "not UTF-8". That is a known
        # limitation, not what this check is about: the screenshot has to be a
        # PNG inside the container, and the bind-mounted host tree has to contain
        # the same bytes.
        $containerHex = Get-ContainerFileHex $cA "$repoA/$($shot.path)"
        if (-not $containerHex.StartsWith("89504e470d0a1a0a")) {
            throw "the screenshot inside the container does not start with the PNG magic bytes: $($containerHex.Substring(0, [Math]::Min(16, $containerHex.Length)))"
        }
        if ($containerHex.Length / 2 -ne $declared) {
            throw "screenshot reported $declared bytes but the container file has $($containerHex.Length / 2)"
        }

        $hostFile = Join-Path $script:data ("sandboxes\$userID\$projA\repo\" + ($shot.path -replace '/', '\'))
        if (-not (Test-Path -LiteralPath $hostFile)) {
            throw "the screenshot is not visible in the bind-mounted host tree at $hostFile"
        }
        $hostHex = Get-BytesHex ([IO.File]::ReadAllBytes($hostFile))
        if ($hostHex -ne $containerHex) {
            throw "the host copy of the screenshot differs from the container copy"
        }
        "$declared bytes, PNG 89504e47, identical bytes at $repoA/$($shot.path) and on the host"
    }

    Invoke-Check "negative control: a stopped container makes the panel file read fail" {
        $running = Invoke-Docker @("inspect", "--format", "{{.State.Running}}", $cA)
        if ($running.Code -ne 0 -or $running.Output.Trim() -ne "true") {
            throw "$cA was not running before the negative control"
        }
        $stop = Invoke-Docker @("stop", "--time", "10", $cA)
        if ($stop.Code -ne 0) { throw "docker stop $cA failed: $($stop.Output)" }

        $read = Invoke-Api -Method Get -Path "/v1/sessions/$($sessA.id)/files?path=panel.txt" -TimeoutSec 60
        if ([int]$read.StatusCode -lt 400) {
            throw "GET /files?path=panel.txt returned $($read.StatusCode) after the container stopped; the panel is reading the host"
        }
        "docker stop $cA; GET /files?path=panel.txt -> $($read.StatusCode)"
    }
}
catch {
    Add-Result "harness" $false ($_.Exception.Message -replace "`r?`n", " ")
    Show-ServerLog
}
finally {
    # Stop the control plane first: it may still be talking to the containers.
    if ($null -ne $script:proc -and -not $script:proc.HasExited) {
        try { Stop-Process -Id $script:proc.Id -Force -ErrorAction SilentlyContinue } catch { }
        try { [void]$script:proc.WaitForExit(5000) } catch { }
    }
    Remove-OurContainers
    if (-not [string]::IsNullOrWhiteSpace($script:tmp) -and (Test-Path -LiteralPath $script:tmp)) {
        for ($attempt = 0; $attempt -lt 3; $attempt++) {
            try {
                Remove-Item -LiteralPath $script:tmp -Recurse -Force -ErrorAction Stop
                break
            }
            catch {
                if ($attempt -eq 2) {
                    Write-Host "warning: could not remove $($script:tmp): $($_.Exception.Message)" -ForegroundColor Yellow
                }
                else {
                    Start-Sleep -Milliseconds 500
                }
            }
        }
    }
}

Write-Host ""
Write-Host "summary" -ForegroundColor Cyan
$script:results | Format-Table -AutoSize -Wrap Status, Check, Detail |
    Out-String -Width 240 | Write-Host
$passed = @($script:results | Where-Object { $_.Status -eq "PASS" }).Count
$failed = @($script:results | Where-Object { $_.Status -eq "FAIL" }).Count
Write-Host ("{0} passed, {1} failed" -f $passed, $failed)

if ($script:failed) {
    Write-Host "verify-docker-sandbox: FAILED" -ForegroundColor Red
    exit 1
}
Write-Host "verify-docker-sandbox: all checks passed" -ForegroundColor Green
exit 0
