# Verify the sandbox-runner deployment end to end: piforged creates containers
# without ever running the Docker CLI itself.
#
#   pwsh scripts/verify-runner-sandbox.ps1
#   pwsh scripts/verify-runner-sandbox.ps1 -Image <all-in-one-sandbox:tag>
#
# What it proves, in order:
#
#   1. sandbox-runner builds, binds 127.0.0.1:8111, rejects a request without the
#      token (401) and answers 404 for a sandbox it does not have;
#   2. piforged starts with `-sandbox runner` on a PATH that has **no docker CLI on
#      it** except a tripwire docker.exe that records any invocation and exits 1.
#      The tripwire log staying absent is the proof that the control plane never
#      forked docker — not an argument from the design, a file that does not exist;
#   3. through the API: project → main session → sandbox → PUT file → git
#      status/stage/commit → branch session → branch commit, all 200;
#   4. `docker ps` shows the piforge-* container the runner created, its /workspace
#      bind mount is the shared data path, and `.git/worktrees/<slug>` is owned by
#      `gem` (the worktree was created inside the container, through the runner);
#   5. cleanup: both processes stopped, `docker rm -f` only the containers this
#      script recorded (never `pf-local`/`piforge`), temp dirs removed, and
#      `docker ps -a` has the same number of rows it had before the run.
#
# Requirements: Docker (daemon reachable), the AIO sandbox image already pulled,
# Go, PowerShell 7. Expected runtime: 2-4 minutes on a warm machine.

param(
    [string]$Image = "",
    [int]$RunnerPort = 8111,
    [int]$ServerPort = 8112,
    [string]$RunnerToken = "",
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
if ([string]::IsNullOrWhiteSpace($RunnerToken)) {
    $RunnerToken = "verify-" + [guid]::NewGuid().ToString("N")
}

$script:results = [System.Collections.Generic.List[object]]::new()
$script:failed = $false
$script:containers = @{}
$script:projectIDs = [System.Collections.Generic.List[string]]::new()
$script:csrf = ""
$script:base = ""
$script:web = $null
$script:proc = $null
$script:runnerProc = $null
$script:tmp = ""
$script:data = ""
$script:outLog = ""
$script:errLog = ""
$script:runnerLog = ""
$script:runnerErrLog = ""
$script:tripwire = ""
$script:tripwireLog = ""
$script:psBefore = 0

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

function Register-Container {
    param([string]$Name)
    if ([string]::IsNullOrWhiteSpace($Name)) { throw "an empty container name was resolved" }
    if ($Name -in @("pf-local", "piforge", "piforge-sandbox-runner")) {
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

function Show-Logs {
    param([int]$Tail = 100)
    foreach ($pair in @(
            @("piforged", $script:outLog),
            @("piforged(stderr)", $script:errLog),
            @("sandbox-runner", $script:runnerLog),
            @("sandbox-runner(stderr)", $script:runnerErrLog))) {
        $label, $file = $pair
        if ($file -and (Test-Path -LiteralPath $file)) {
            Write-Host ""
            Write-Host "---- $label (last $Tail lines) ----" -ForegroundColor Yellow
            Get-Content -LiteralPath $file -Tail $Tail -ErrorAction SilentlyContinue
        }
    }
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
    $body = @{ kind = $Kind; title = "verify runner $Kind" }
    if (-not [string]::IsNullOrWhiteSpace($Label)) { $body.label = $Label }
    $resp = Invoke-Api -Method Post -Path "/v1/projects/$ProjectID/sessions" -Json $body -TimeoutSec 300
    Expect-Status $resp 201 "POST session ($Kind)"
    $sess = (Get-Json $resp).session
    if ($null -eq $sess -or [string]::IsNullOrWhiteSpace($sess.id)) {
        throw "session creation returned no session"
    }
    return $sess
}

function Get-PfSandbox {
    param([string]$SessionID)
    $resp = Invoke-Api -Method Get -Path "/v1/sessions/$SessionID/sandbox" -TimeoutSec 60
    Expect-Status $resp 200 "GET /v1/sessions/$SessionID/sandbox"
    $body = Get-Json $resp
    Register-Container $body.id
    return $body
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
            # Listener not up yet; the next iteration retries.
        }
        Start-Sleep -Milliseconds 300
    }
    throw "piforged did not answer /healthz within ${TimeoutSec}s"
}

function Wait-Runner {
    param([int]$TimeoutSec)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $script:runnerProc -and $script:runnerProc.HasExited) {
            $log = if (Test-Path -LiteralPath $script:runnerErrLog) { Get-Content -LiteralPath $script:runnerErrLog -Raw } else { "" }
            throw "sandbox-runner exited with code $($script:runnerProc.ExitCode): $log"
        }
        try {
            $r = Invoke-WebRequest -Uri "http://127.0.0.1:$RunnerPort/v1/sandboxes/piforge-doesnotexist" `
                -Headers @{ Authorization = "Bearer $RunnerToken" } -TimeoutSec 2 -SkipHttpErrorCheck -NoProxy
            if ([int]$r.StatusCode -eq 404) { return }
            $last = "status $($r.StatusCode)"
        }
        catch {
            $last = $_.Exception.Message
        }
        Start-Sleep -Milliseconds 200
    }
    throw "sandbox-runner did not answer on 127.0.0.1:$RunnerPort within ${TimeoutSec}s ($last)"
}

function Test-PortFree {
    param([int]$Port)
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $Port)
    try {
        $listener.Start()
        return $true
    }
    catch {
        return $false
    }
    finally {
        try { $listener.Stop() } catch { }
    }
}

# The tripwire is a real docker.exe built from five lines of Go: it appends its
# argv to a file and fails. piforged gets a PATH whose only `docker` is this
# binary, so any call it makes is both visible (the file) and fatal (the exit
# code) rather than silently going to the daemon.
function New-DockerTripwire {
    param([string]$Directory, [string]$LogPath)
    New-Item -ItemType Directory -Force -Path $Directory | Out-Null
    $source = Join-Path $Directory "tripwire.go"
    @'
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if path := os.Getenv("PIFORGE_DOCKER_TRIPWIRE"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%s docker %v\n", time.Now().Format(time.RFC3339Nano), os.Args[1:])
			_ = f.Close()
		}
	}
	fmt.Fprintln(os.Stderr, "docker is not available to the control plane in this deployment")
	os.Exit(1)
}
'@ | Set-Content -LiteralPath $source -Encoding utf8
    Push-Location $Directory
    try {
        & go build -o (Join-Path $Directory "docker.exe") $source 2>&1 | ForEach-Object { Write-Host "$_" }
        if ($LASTEXITCODE -ne 0) { throw "go build of the docker tripwire failed" }
    }
    finally {
        Pop-Location
    }
    Remove-Item -LiteralPath $source -Force
    if (-not (Test-Path -LiteralPath (Join-Path $Directory "docker.exe"))) {
        throw "the docker tripwire was not built"
    }
    $env:PIFORGE_DOCKER_TRIPWIRE = $LogPath
}

# Every PATH entry that carries a docker.exe is dropped, and the tripwire is
# prepended. Everything else (git, powershell, Windows itself) stays, because
# piforged legitimately needs those.
function Get-NoDockerPath {
    param([string]$GuardDirectory)
    $kept = @()
    foreach ($entry in ($env:PATH -split ';')) {
        if ([string]::IsNullOrWhiteSpace($entry)) { continue }
        if (Test-Path -LiteralPath (Join-Path $entry "docker.exe")) {
            Write-Host "  dropping from PATH: $entry" -ForegroundColor DarkGray
            continue
        }
        $kept += $entry
    }
    return ((@($GuardDirectory) + $kept) -join ';')
}

function Remove-OurContainers {
    foreach ($projectID in $script:projectIDs) {
        $r = Invoke-Docker @("ps", "-aq", "--filter", "label=piforge.project=$projectID")
        if ($r.Code -eq 0 -and -not [string]::IsNullOrWhiteSpace($r.Output)) {
            foreach ($id in @($r.Output -split "`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
                $nameResult = Invoke-Docker @("inspect", "--format", "{{.Name}}", $id.Trim())
                if ($nameResult.Code -ne 0) { continue }
                $name = $nameResult.Output.Trim().TrimStart("/")
                if ($name -in @("pf-local", "piforge", "piforge-sandbox-runner")) { continue }
                if ($name -match '^piforge-[0-9a-f]+$') { $script:containers[$name] = $true }
            }
        }
    }
    foreach ($name in @($script:containers.Keys)) {
        if ($name -in @("pf-local", "piforge", "piforge-sandbox-runner")) { continue }
        if ($name -notmatch '^piforge-[0-9a-f]+$') { continue }
        $r = Invoke-Docker @("rm", "-f", $name)
        if ($r.Code -ne 0) {
            Write-Host "warning: docker rm -f $name failed: $($r.Output)" -ForegroundColor Yellow
        }
    }
}

# ---------------------------------------------------------------------------
# Preconditions
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
foreach ($port in @($RunnerPort, $ServerPort)) {
    if (-not (Test-PortFree $port)) {
        Write-Host "BUILD/PRECONDITION FAILED: port $port is already in use" -ForegroundColor Red
        exit 1
    }
}

$script:psBefore = @((Invoke-Docker @("ps", "-aq")).Output -split "`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }).Count

$script:tmp = Join-Path ([IO.Path]::GetTempPath()) ("piforge-runner-verify-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
$bin = Join-Path $script:tmp "bin"
$script:data = Join-Path $script:tmp "data"
$guard = Join-Path $script:tmp "guard"
$script:tripwireLog = Join-Path $script:tmp "docker-invocations.log"
$script:outLog = Join-Path $script:tmp "piforged.out.log"
$script:errLog = Join-Path $script:tmp "piforged.err.log"
$script:runnerLog = Join-Path $script:tmp "runner.out.log"
$script:runnerErrLog = Join-Path $script:tmp "runner.err.log"
New-Item -ItemType Directory -Force -Path $bin, $script:data | Out-Null
$sandboxRoot = Join-Path $script:data "sandboxes"

try {
    # -----------------------------------------------------------------------
    # Build
    # -----------------------------------------------------------------------
    Write-Host "building piforged, fakepi and sandbox-runner into $bin"
    Push-Location $backend
    try {
        foreach ($target in @(
                @("piforged", "./cmd/piforged"),
                @("fakepi", "./cmd/fakepi"),
                @("sandbox-runner", "./cmd/sandbox-runner"))) {
            $name, $package = $target
            $out = & go build -trimpath -o (Join-Path $bin "$name.exe") $package 2>&1
            if ($LASTEXITCODE -ne 0) {
                $out | ForEach-Object { Write-Host "$_" }
                throw "go build $package failed"
            }
        }
    }
    finally {
        Pop-Location
    }
    Add-Result "build: piforged + fakepi + sandbox-runner" $true $bin

    # -----------------------------------------------------------------------
    # sandbox-runner on 127.0.0.1:8111
    # -----------------------------------------------------------------------
    $runnerArgs = @(
        "-addr", "127.0.0.1:$RunnerPort",
        "-token", $RunnerToken,
        "-data-root", (Quote-Arg $sandboxRoot),
        "-image", (Quote-Arg $Image),
        "-api-port", "8080",
        "-api-key", "sandbox-key-" + [guid]::NewGuid().ToString("N")
    )
    $sandboxAPIKey = $runnerArgs[-1]
    Write-Host "starting sandbox-runner on 127.0.0.1:$RunnerPort (data root $sandboxRoot)"
    Write-Host ("  sandbox-runner -addr 127.0.0.1:$RunnerPort -token <redacted> -data-root `"$sandboxRoot`" -image `"$Image`" -api-port 8080 -api-key <redacted>") -ForegroundColor DarkGray
    $script:runnerProc = Start-Process -FilePath (Join-Path $bin "sandbox-runner.exe") `
        -ArgumentList $runnerArgs -WorkingDirectory $root `
        -RedirectStandardOutput $script:runnerLog -RedirectStandardError $script:runnerErrLog `
        -PassThru -NoNewWindow

    Wait-Runner -TimeoutSec 30
    Add-Result "runner: listening on 127.0.0.1:$RunnerPort" $true "GET /v1/sandboxes/piforge-doesnotexist -> 404"

    Invoke-Check "runner: a request without the bearer token is 401" {
        $r = Invoke-WebRequest -Uri "http://127.0.0.1:$RunnerPort/v1/sandboxes" -Method Post `
            -Body ([Text.Encoding]::UTF8.GetBytes('{"user_id":"u","project_id":"p"}')) `
            -ContentType "application/json" -SkipHttpErrorCheck -NoProxy
        if ([int]$r.StatusCode -ne 401) { throw "POST /v1/sandboxes without a token -> $($r.StatusCode), want 401" }
        "POST /v1/sandboxes without Authorization -> 401 $($r.Content)"
    }

    Invoke-Check "runner: a wrong token is 401" {
        $r = Invoke-WebRequest -Uri "http://127.0.0.1:$RunnerPort/v1/sandboxes/piforge-x" `
            -Headers @{ Authorization = "Bearer wrong-token" } -SkipHttpErrorCheck -NoProxy
        if ([int]$r.StatusCode -ne 401) { throw "GET with a wrong token -> $($r.StatusCode), want 401" }
        "GET /v1/sandboxes/piforge-x with a wrong token -> 401"
    }

    # -----------------------------------------------------------------------
    # piforged with a PATH that has no docker CLI
    # -----------------------------------------------------------------------
    New-DockerTripwire -Directory $guard -LogPath $script:tripwireLog
    $restrictedPath = Get-NoDockerPath -GuardDirectory $guard
    Write-Host "piforged PATH docker resolves to:"
    $savedPath = $env:PATH
    try {
        $env:PATH = $restrictedPath
        # where.exe re-reads PATH; Get-Command would answer from PowerShell's
        # discovery cache, which is exactly the wrong answer here.
        $resolved = (& where.exe docker 2>$null | Select-Object -First 1)
        Write-Host ("  " + $(if (-not [string]::IsNullOrWhiteSpace($resolved)) { $resolved } else { "<none>" }))
        if ([string]::IsNullOrWhiteSpace($resolved) -or $resolved -notlike "$guard*") {
            throw "the restricted PATH still resolves docker outside the tripwire: $resolved"
        }
        Add-Result "piforged PATH: the only docker is a tripwire" $true $resolved
    }
    finally {
        $env:PATH = $savedPath
    }

    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $ServerPort)
    $listener.Start()
    $listener.Stop()
    $script:base = "http://127.0.0.1:$ServerPort"

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

    $serverArgs = @(
        "-sandbox", "runner",
        "-sandbox-runner-url", "http://127.0.0.1:$RunnerPort",
        "-sandbox-runner-token", $RunnerToken,
        "-sandbox-image", (Quote-Arg $Image),
        "-sandbox-api-key", $sandboxAPIKey,
        "-fake-pi",
        "-dev",
        "-addr", "127.0.0.1:$ServerPort",
        "-data", (Quote-Arg $script:data)
    )
    try {
        Write-Host "starting piforged on $($script:base) with -sandbox runner"
        Write-Host ("  piforged -sandbox runner -sandbox-runner-url http://127.0.0.1:$RunnerPort -sandbox-runner-token <redacted> -sandbox-image `"$Image`" -sandbox-api-key <redacted> -fake-pi -dev -addr 127.0.0.1:$ServerPort -data `"$($script:data)`"") -ForegroundColor DarkGray
        $env:PATH = $restrictedPath
        $script:proc = Start-Process -FilePath (Join-Path $bin "piforged.exe") `
            -ArgumentList $serverArgs -WorkingDirectory $root `
            -RedirectStandardOutput $script:outLog -RedirectStandardError $script:errLog `
            -PassThru -NoNewWindow
    }
    finally {
        $env:PATH = $savedPath
        foreach ($name in $envNames) {
            [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], "Process")
        }
    }

    Wait-Health $StartTimeoutSec
    Add-Result "piforged: healthy on $($script:base)" $true "GET /healthz -> 200"

    Invoke-Check "piforged: placement is runner / all remote, and it never looked up docker" {
        $log = Get-ServerLog
        $resolved = $log -match 'msg="sandbox placement resolved"[^\r\n]*sandbox=runner[^\r\n]*browser=remote[^\r\n]*terminal=remote[^\r\n]*files=remote'
        if (-not $resolved) { throw "no 'sandbox placement resolved' line with sandbox=runner browser=remote terminal=remote files=remote" }
        if ($log -notmatch 'msg="sandbox lifecycle runs through the runner"') {
            throw "the log does not say the lifecycle goes through the runner"
        }
        if ($log -match 'docker CLI is not on PATH') { throw "piforged tried to look up the docker CLI" }
        if ($log -match 'msg="sandbox placement is mixed') { throw "the log contains a mixed-placement WARN" }
        "sandbox=runner browser=remote terminal=remote files=remote; lifecycle log present; no docker lookup"
    }

    # -----------------------------------------------------------------------
    # Login
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
    Add-Result "login: admin cookie + CSRF" $true "user $userID"

    # -----------------------------------------------------------------------
    # Project A: main session -> sandbox -> files -> git
    # -----------------------------------------------------------------------
    $projA = New-PfProject "verify-runner-a"
    Add-Result "project: POST /v1/projects -> 201" $true $projA

    $sessA = New-PfSession $projA "main"
    if ([string]::IsNullOrWhiteSpace($sessA.worktree_id)) {
        throw "the main session has no worktree; provisioning failed (see the piforged log)"
    }
    Add-Result "session: POST /v1/projects/{id}/sessions -> 201" $true "session $($sessA.id) worktree $($sessA.worktree_id)"

    $sandbox = Get-PfSandbox $sessA.id
    $cA = $sandbox.id
    if ($cA -notmatch '^piforge-[0-9a-f]+$') { throw "sandbox id '$cA' is not a container name derived from the pair" }
    if ($sandbox.state -ne "READY") { throw "sandbox state is '$($sandbox.state)', want READY" }
    Add-Result "sandbox: GET /v1/sessions/{id}/sandbox -> READY" $true ("state=$($sandbox.state) id=$cA")

    $repoA = "/workspace/repo"

    Invoke-Check "docker: the runner created the container (docker ps)" {
        $ps = Invoke-Docker @("ps", "--filter", "name=^$cA$", "--format", "{{.Names}}  {{.Image}}  {{.Ports}}  {{.Status}}")
        if ($ps.Code -ne 0) { throw "docker ps failed: $($ps.Output)" }
        $line = ($ps.Output -split "`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) -join " | "
        if ([string]::IsNullOrWhiteSpace($line)) { throw "docker ps has no running container named $cA" }
        "docker ps: $line"
    }

    Invoke-Check "docker: /workspace is the shared data path for this project" {
        $r = Invoke-Docker @(
            "inspect", "--format",
            '{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Source}}{{end}}{{end}}',
            $cA
        )
        if ($r.Code -ne 0) { throw "docker inspect mounts failed: $($r.Output)" }
        $mount = $r.Output.Trim()
        $want = Join-Path (Join-Path $sandboxRoot $userID) $projA
        if ($mount -ne $want) { throw "mount source is '$mount', want '$want'" }
        "mount source: $mount"
    }

    Invoke-Check "files: PUT /files writes into the container" {
        $payload = "runner-verify " + [guid]::NewGuid().ToString("N") + "`n"
        $bytes = [Text.Encoding]::UTF8.GetBytes($payload)
        $resp = Invoke-Api -Method Put -Path "/v1/sessions/$($sessA.id)/files?path=panel.txt" `
            -Bytes $bytes -ContentType "text/plain; charset=utf-8" -TimeoutSec 60
        Expect-Status $resp 200 "PUT /files?path=panel.txt"
        # od instead of cat: byte-for-byte, no console decoding in the middle.
        $r = Invoke-Docker @("exec", $cA, "od", "-An", "-tx1", "-v", "$repoA/panel.txt")
        if ($r.Code -ne 0) { throw "docker exec od failed: $($r.Output)" }
        $containerHex = ($r.Output -replace '\s+', '').ToLowerInvariant()
        $panelHex = ([Convert]::ToHexString($bytes)).ToLowerInvariant()
        if ($containerHex -ne $panelHex) {
            throw "container bytes ($containerHex) differ from the panel write ($panelHex)"
        }
        "PUT /files?path=panel.txt -> 200; od bytes identical ($($bytes.Length) bytes)"
    }

    $script:commitA = ""
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
            -Json @{ message = "verify: runner main commit" } -TimeoutSec 120
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
        if ($author -ne "sandbox@piforge.local") { throw "commit author is '$author', want sandbox@piforge.local" }
        "docker exec -u gem git log -1 --format=%ae -> $author"
    }

    # -----------------------------------------------------------------------
    # Branch session: the worktree must be created inside the container
    # -----------------------------------------------------------------------
    $branchSlug = "runner-branch"
    Invoke-Check "branch session: PUT/status/stage/commit are 200" {
        $branch = New-PfSession $projA "branch" $branchSlug
        if ([string]::IsNullOrWhiteSpace($branch.worktree_id)) { throw "the branch session has no worktree" }

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
            -Json @{ message = "verify: runner branch commit" } -TimeoutSec 120
        Expect-Status $commit 200 "POST branch /git/commit"
        "session $($branch.id); status/stage/commit all 200 ($((Get-Json $commit).commit.Substring(0, 7)))"
    }

    Invoke-Check "branch session: .git/worktrees/<slug> is owned by gem" {
        $wtMeta = "$repoA/.git/worktrees/$branchSlug"
        $r = Invoke-Docker @("exec", "-u", "gem", $cA, "stat", "-c", "%U", $wtMeta)
        if ($r.Code -ne 0) { throw "stat $wtMeta failed: $($r.Output)" }
        $owner = $r.Output.Trim()
        if ($owner -ne "gem") { throw "$wtMeta is owned by '$owner', want gem (created inside the container)" }
        $list = Invoke-Docker @("exec", "-u", "gem", $cA, "git", "-c", "safe.directory=$repoA", "-C", $repoA, "worktree", "list", "--porcelain")
        "stat %U -> $owner; worktree list:`n$($list.Output.Trim())"
    }

    # -----------------------------------------------------------------------
    # The proof that matters: the control plane never ran docker
    # -----------------------------------------------------------------------
    Invoke-Check "control plane: docker was never invoked by piforged" {
        $containerNames = @($script:containers.Keys) -join ", "
        if (Test-Path -LiteralPath $script:tripwireLog) {
            $called = Get-Content -LiteralPath $script:tripwireLog -Raw
            throw "piforged invoked the tripwire docker: $called"
        }
        "the tripwire log does not exist; containers were created by the runner ($containerNames)"
    }

    Invoke-Check "runner: it, not piforged, ran the docker calls" {
        $log = if (Test-Path -LiteralPath $script:runnerErrLog) {
            Get-Content -LiteralPath $script:runnerErrLog -Raw
        }
        else { "" }
        $requests = ([regex]::Matches($log, 'msg="sandbox runner request"')).Count
        if ($requests -lt 1) { throw "the runner log has no 'sandbox runner request' lines" }
        $posts = ([regex]::Matches($log, 'method=POST path=/v1/sandboxes ')).Count
        if ($posts -lt 1) { throw "the runner log has no POST /v1/sandboxes" }
        "runner logged $requests requests, including $posts sandbox creates"
    }
}
catch {
    Add-Result "harness" $false ($_.Exception.Message -replace "`r?`n", " ")
    Show-Logs
}
finally {
    foreach ($proc in @($script:proc, $script:runnerProc)) {
        if ($null -ne $proc -and -not $proc.HasExited) {
            try { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue } catch { }
            try { [void]$proc.WaitForExit(5000) } catch { }
        }
    }
    Remove-OurContainers
    $psAfter = @((Invoke-Docker @("ps", "-aq")).Output -split "`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }).Count
    if ($psAfter -ne $script:psBefore) {
        Add-Result "cleanup: docker ps -a rows unchanged" $false "before $($script:psBefore), after $psAfter"
    }
    else {
        Add-Result "cleanup: docker ps -a rows unchanged" $true "$psAfter rows before and after"
    }
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
    Write-Host "verify-runner-sandbox: FAILED" -ForegroundColor Red
    exit 1
}
Write-Host "verify-runner-sandbox: all checks passed" -ForegroundColor Green
exit 0
