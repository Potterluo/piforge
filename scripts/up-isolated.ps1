# One command from a clean checkout to a working isolated PiForge.
#
#   pwsh scripts/up-isolated.ps1                 # build what is missing, preflight, start
#   pwsh scripts/up-isolated.ps1 -Check          # preflight only: no build, no start
#   pwsh scripts/up-isolated.ps1 -Rebuild        # rebuild all three images, then start
#   pwsh scripts/up-isolated.ps1 -Down           # remove the stack, its sandboxes and volumes
#
# Why this exists: the isolated shape needs three images that agree with each
# other (the control plane, the runner, and a sandbox image built *from* the
# control plane so `pi` and /opt/piforge/piforged are inside it). Naming the
# wrong sandbox image fails at the first session with "the agent supervisor did
# not come up", twenty minutes after the mistake. This script builds them in the
# only order that works, then runs `cmd/preflight`, which fails at minute one
# naming the missing piece.
#
# It never prints a secret: the runner token and the provider key are passed to
# docker compose through the environment and are not echoed.
param(
    [int]$Port = 7723,
    [string]$Project = "pf-isolated",
    [string]$Tag = "isolated",
    [string]$DataVolume = "",
    [switch]$Check,
    [switch]$NoBuild,
    [switch]$Rebuild,
    [switch]$Down,
    [string]$Provider = "volc|https://api.example.com/v1|deepseek-v4-flash",
    [string]$ProviderKey = $env:PIFORGE_DRILL_KEY,
    [string]$DefaultProvider = "volc",
    [string]$RunnerToken = "",
    # The tenant quota policy: the default every account gets is the
    # deploy/tenant-limits.json file, and this points the stack at another one
    # (the soak uses per-tenant overrides).
    [string]$LimitsFile = "",
    # The tenant egress policy: what a sandbox may reach beyond the control plane.
    # deploy/tenant-egress.json is closed by default (every tenant bridge is
    # created --internal). scripts/probe-egress.ps1 points this at a file that
    # opens one tenant, so both modes are measured on one stack.
    [string]$EgressFile = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$compose = Join-Path $root "docker-compose.runner.yml"
$backend = Join-Path $root "backend"

$controlPlaneImage = "piforge:$Tag"
$runnerImage = "piforge/sandbox-runner:$Tag"
$sandboxImage = "piforge/sandbox-agent:$Tag"
if ([string]::IsNullOrWhiteSpace($DataVolume)) { $DataVolume = "$Project-data" }
if ([string]::IsNullOrWhiteSpace($LimitsFile)) { $LimitsFile = Join-Path $root "deploy/tenant-limits.json" }
if (-not (Test-Path $LimitsFile)) { Write-Host "ERROR: tenant limits file not found: $LimitsFile" -ForegroundColor Red; exit 1 }
$LimitsFile = (Resolve-Path $LimitsFile).Path
if ([string]::IsNullOrWhiteSpace($EgressFile)) { $EgressFile = Join-Path $root "deploy/tenant-egress.json" }
if (-not (Test-Path $EgressFile)) { Write-Host "ERROR: tenant egress file not found: $EgressFile" -ForegroundColor Red; exit 1 }
$EgressFile = (Resolve-Path $EgressFile).Path

function Step($message) { Write-Host "==> $message" -ForegroundColor Cyan }
function Fail($message) { Write-Host "ERROR: $message" -ForegroundColor Red; exit 1 }

function Invoke-Docker {
    param([string[]]$Arguments, [switch]$AllowFailure)
    & docker @Arguments
    $code = $LASTEXITCODE
    if ($code -ne 0 -and -not $AllowFailure) {
        Fail "docker $($Arguments -join ' ') failed with exit $code"
    }
    return $code
}

function Image-Exists([string]$ref) {
    & docker image inspect $ref *> $null
    return $LASTEXITCODE -eq 0
}

function Build-File([string]$dockerfile, [string]$tag, [string[]]$extraArgs) {
    Step "building $tag ($dockerfile)"
    $args = @("build", "-f", $dockerfile, "-t", $tag) + $extraArgs + @(".")
    Invoke-Docker -Arguments $args | Out-Null
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Fail "docker is not on PATH; the isolated deployment runs three containers and needs Docker Engine 25+"
}

# Down first: it is the only operation that must work even when the images are
# stale or the compose file has grown a required variable.
if ($Down) {
    Step "removing the compose stack, its sandboxes and its volumes"
    $env:PIFORGE_IMAGE = $controlPlaneImage
    $env:PIFORGE_RUNNER_IMAGE = $runnerImage
    $env:PIFORGE_SANDBOX_IMAGE = $sandboxImage
    $env:PIFORGE_PUBLISH = "$Port"
    $env:PIFORGE_DATA_VOLUME = $DataVolume
    $env:PIFORGE_TENANT_LIMITS_MOUNT = $LimitsFile
    $env:PIFORGE_TENANT_EGRESS_MOUNT = $EgressFile
    if ([string]::IsNullOrWhiteSpace($env:SANDBOX_RUNNER_TOKEN)) { $env:SANDBOX_RUNNER_TOKEN = "teardown-only" }
    Invoke-Docker -Arguments @("compose", "-p", $Project, "-f", $compose, "down", "-v", "--remove-orphans") -AllowFailure | Out-Null

    # Sandboxes are created at runtime by the runner, outside the compose
    # project, so `compose down` cannot know about them. They carry the
    # product's own labels, which is what this removes.
    $leftovers = & docker ps -aq --filter "label=piforge.user"
    foreach ($id in @($leftovers)) {
        if ($id) { & docker rm -f $id *> $null }
    }
    $volumes = & docker volume ls -q --filter "name=piforge-ws-"
    foreach ($volume in @($volumes)) {
        if ($volume) { & docker volume rm -f $volume *> $null }
    }
    # Per-tenant bridges are labelled at creation (the runner makes one per
    # tenant, outside any compose project), so this is the only thing that can
    # find them after the containers are gone.
    $tenantNetworks = & docker network ls -q --filter "label=piforge.tenant"
    foreach ($network in @($tenantNetworks)) {
        if ($network) { & docker network rm $network *> $null }
    }
    # `compose down` cannot remove it while the sandboxes are still attached, and
    # they are removed above; this is the second half of that sequence.
    & docker network rm piforge-sandboxes *> $null
    Write-Host "removed: containers labelled piforge.user, volumes named piforge-ws-*, networks labelled piforge.tenant, network piforge-sandboxes"
    Write-Host "kept: the image tags (pass -Rebuild after a source change)"
    exit 0
}

# ---------------------------------------------------------------------------
# Build. Order matters: the sandbox image is built *from* the control plane, so
# `pi` and /opt/piforge/piforged are exactly the version the control plane runs.
# ---------------------------------------------------------------------------
if (-not $NoBuild -and -not $Check) {
    $buildControlPlane = $Rebuild -or -not (Image-Exists $controlPlaneImage)
    if ($buildControlPlane) {
        Build-File "Dockerfile" $controlPlaneImage @()
    }
    if ($Rebuild -or -not (Image-Exists $runnerImage)) {
        Build-File "deploy/Dockerfile.runner" $runnerImage @()
    }
    if ($Rebuild -or $buildControlPlane -or -not (Image-Exists $sandboxImage)) {
        Build-File "deploy/Dockerfile.sandbox-agent" $sandboxImage @("--build-arg", "AGENT_IMAGE=$controlPlaneImage")
    }
}

# ---------------------------------------------------------------------------
# Preflight. Go is required for this one program; the rest of the script is
# Docker. Its checks are `backend/internal/preflight` and are unit tested there.
# ---------------------------------------------------------------------------
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Fail "go is not on PATH; the preflight is Go (backend/cmd/preflight) and needs Go 1.25+"
}
Step "preflight"
Push-Location $backend
try {
    $lines = & go run ./cmd/preflight `
        -control-plane-image $controlPlaneImage `
        -runner-image $runnerImage `
        -sandbox-image $sandboxImage 2>&1
    $code = $LASTEXITCODE
}
finally {
    Pop-Location
}
$lines | ForEach-Object { Write-Host "$_" }
if ($code -ne 0) {
    Fail "preflight failed; the isolated stack was not started (see the FAIL lines above)"
}
if ($Check) {
    Write-Host ""
    Write-Host "preflight only (-Check): nothing was built or started." -ForegroundColor Green
    exit 0
}

# ---------------------------------------------------------------------------
# Start. Values from the environment win over the .env file, so a stale .env
# cannot decide which images run.
# ---------------------------------------------------------------------------
if ([string]::IsNullOrWhiteSpace($RunnerToken)) {
    if ([string]::IsNullOrWhiteSpace($env:SANDBOX_RUNNER_TOKEN)) {
        $RunnerToken = [guid]::NewGuid().ToString("N") + [guid]::NewGuid().ToString("N")
    }
    else {
        $RunnerToken = $env:SANDBOX_RUNNER_TOKEN
    }
}
$env:PIFORGE_IMAGE = $controlPlaneImage
$env:PIFORGE_RUNNER_IMAGE = $runnerImage
$env:PIFORGE_SANDBOX_IMAGE = $sandboxImage
$env:PIFORGE_PUBLISH = "$Port"
$env:PIFORGE_DATA_VOLUME = $DataVolume
$env:PIFORGE_TENANT_LIMITS_MOUNT = $LimitsFile
    $env:PIFORGE_TENANT_EGRESS_MOUNT = $EgressFile
$env:SANDBOX_RUNNER_TOKEN = $RunnerToken
$env:PIFORGE_DEFAULT_PROVIDER = $DefaultProvider
if (-not [string]::IsNullOrWhiteSpace($Provider)) { $env:PIFORGE_BOOTSTRAP_PROVIDER = $Provider }
if (-not [string]::IsNullOrWhiteSpace($ProviderKey)) { $env:PIFORGE_BOOTSTRAP_PROVIDER_KEY = $ProviderKey }

Step "starting the stack (project $Project, port $Port)"
Invoke-Docker -Arguments @("compose", "-p", $Project, "-f", $compose, "up", "-d", "--no-build") | Out-Null

$health = "http://127.0.0.1:$Port/healthz"
$ok = $false
for ($i = 0; $i -lt 60; $i++) {
    try {
        $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -Uri $health
        if ($response.StatusCode -eq 200) { $ok = $true; break }
    }
    catch { Start-Sleep -Seconds 1 }
}
if (-not $ok) {
    Write-Host "the stack did not answer $health within 60 s. Its logs:" -ForegroundColor Red
    & docker logs piforge --tail 40 2>&1 | ForEach-Object { Write-Host "$_" }
    Fail "isolated stack did not become healthy"
}

Write-Host ""
Write-Host "PiForge (isolated) is up: http://127.0.0.1:$Port/login" -ForegroundColor Green
Write-Host "  accounts : admin@piforge.local / piforge-admin-123 (change before exposing)"
Write-Host "              dev@piforge.local   / piforge-dev-123"
Write-Host "  images   : $controlPlaneImage, $runnerImage, $sandboxImage"
Write-Host "  data     : volume $DataVolume"
Write-Host "  provider : $DefaultProvider ($Provider)"
Write-Host "  egress   : $EgressFile (default none: no route to the internet; the gateway on the tenant bridge still answers)"
Write-Host "  stop     : pwsh scripts/up-isolated.ps1 -Project $Project -Down"
if ([string]::IsNullOrWhiteSpace($ProviderKey)) {
    Write-Host "  note     : no provider key was supplied; set -ProviderKey (or PIFORGE_DRILL_KEY) for a real prompt," -ForegroundColor Yellow
    Write-Host "             or open Settings -> Providers once the stack is up."
}

