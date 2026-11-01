# One command from a clean checkout to a working isolated PiForge.
#
#   pwsh scripts/up-isolated.ps1                 # build what is stale, preflight, start
#   pwsh scripts/up-isolated.ps1 -Check          # preflight only: no build, no start
#   pwsh scripts/up-isolated.ps1 -BuildOnly      # build what is stale, print, stop
#   pwsh scripts/up-isolated.ps1 -Rebuild        # rebuild all three images, then start
#   pwsh scripts/up-isolated.ps1 -Down           # remove the stack, its sandboxes and volumes
#
# "Stale" is decided from file *content*, not timestamps: each image carries a
# piforge.source-digest label (see the Freshness section below) and an image is
# reused only when that label matches the tree. The rule is
# backend/cmd/sourcedigest — shared with scripts/verify-linux.sh, which refuses to
# assert on a sandbox image carrying another tree's digest. Refreshing every
# image is `pwsh scripts/up-isolated.ps1 -Rebuild`; that is also the command
# verify-linux.sh names when it refuses one, and `-NoBuild` now refuses a stale
# image instead of starting it.
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
    # Build what is stale and stop: no preflight, no start. It exists so the
    # freshness decision can be exercised (scripts/verify-image-freshness.ps1)
    # without bringing a stack up.
    [switch]$BuildOnly,
    [switch]$Rebuild,
    # Move a stack that is already up on another port. Without it, a different
    # -Port is refused instead of silently re-publishing the live stack.
    [switch]$Force,
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
    [string]$EgressFile = "",
    # The outbound URL policy's allow-list (docs/SANDBOX-DEPLOY.md 1.9).
    # deploy/url-policy.json is empty by default: public http(s) only. A drill
    # that needs an internal MCP server points this at a file that names it.
    [string]$UrlAllowFile = ""
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
if ([string]::IsNullOrWhiteSpace($UrlAllowFile)) { $UrlAllowFile = Join-Path $root "deploy/url-policy.json" }
if (-not (Test-Path $UrlAllowFile)) { Write-Host "ERROR: URL allow-list file not found: $UrlAllowFile" -ForegroundColor Red; exit 1 }
$UrlAllowFile = (Resolve-Path $UrlAllowFile).Path

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

# Image-Created is when the image was built, in UTC, or $null when it is missing.
# It is the other half of the staleness check: a tag that exists is not a tag
# that matches the tree.
function Image-Created([string]$ref) {
    $text = & docker image inspect -f '{{.Created}}' $ref 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($text)) { return $null }
    try { return ([DateTimeOffset]::Parse($text.Trim())).UtcDateTime } catch { return $null }
}

# Image-Id reports the short image id, for the log line an operator reads to know
# which build actually ran.
function Image-Id([string]$ref) {
    $id = & docker image inspect -f '{{.Id}}' $ref 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($id)) { return 'missing' }
    $id = $id.Trim()
    if ($id.Length -gt 19) { return $id.Substring(0, 19) }
    return $id
}

# ---------------------------------------------------------------------------
# Freshness: the content digest.
#
# Every image is built with a label naming the digest of the inputs it was built
# from:
#
#   --label piforge.source-digest=<sha256 over the source content, the
#                                   Dockerfile and the build args>
#
# The next run recomputes the same digest from the tree and compares. Equal
# content means "reuse the image", whatever the clock says; different content
# means "rebuild", even when the edit looks older than the image.
#
# The rule itself lives in backend/cmd/sourcedigest and nowhere else. This script
# is no longer the only consumer: scripts/verify-linux.sh refuses to assert on a
# sandbox image whose label is not this tree's digest, and it runs on a Linux
# host without PowerShell. Two implementations of one rule would eventually
# disagree — and the disagreement would look like a stale image, not like a bug.
# ---------------------------------------------------------------------------

# Source-Digest asks backend/cmd/sourcedigest for one recipe's digest:
#
#   -dockerfile   the recipe (its path is part of the digest)
#   -agent-image  the control-plane image a sandbox image is built FROM; the
#                 program folds in its full id as AGENT_IMAGE=<id>, so a rebuilt
#                 control plane rebuilds the sandbox by the same rule
#
# It returns the digest and how many files it covers (the log line names the
# count, so an empty list cannot pass as a full one).
function Source-Digest([string]$dockerfile, [string]$agentImage) {
    $goArgs = @("run", "./cmd/sourcedigest", "-root", $root, "-dockerfile", $dockerfile, "-json")
    if (-not [string]::IsNullOrWhiteSpace($agentImage)) { $goArgs += @("-agent-image", $agentImage) }
    Push-Location $backend
    try {
        $output = & go @goArgs 2>&1
        $code = $LASTEXITCODE
    }
    finally { Pop-Location }
    $text = ($output | Out-String).Trim()
    if ($code -ne 0) {
        Fail ("the source digest for {0} could not be computed ({1}): the freshness decision has no input. Run 'go run ./cmd/sourcedigest -root .. -dockerfile {0}' in backend/ to see it" -f $dockerfile, $text)
    }
    try { $parsed = $text | ConvertFrom-Json }
    catch { Fail ("the source digest tool printed no JSON for {0}: {1}" -f $dockerfile, $text) }
    if ([string]::IsNullOrWhiteSpace([string]$parsed.digest)) {
        Fail ("the source digest tool printed no digest for {0}: {1}" -f $dockerfile, $text)
    }
    # A list read from the recipe files alone does not change when backend/,
    # web/ or deploy/ changes, so it must never answer "current".
    # backend/cmd/sourcedigest refuses to print one now; this is the script's own
    # refusal as well, so a caller that relaxes the tool cannot quietly bring the
    # guess back.
    if ([string]$parsed.source -ne 'git') {
        Fail ("the source digest for {0} came from '{1}', not from git: it covers only the recipe files, so a change under backend/, web/ or deploy/ would not be noticed. Refusing to decide freshness from it (install git, or run from a checkout)" -f $dockerfile, [string]$parsed.source)
    }
    return [pscustomobject]@{ Digest = [string]$parsed.digest; Files = [int]$parsed.files }
}

# Short-Digest is the 12-hex prefix the log lines print.
function Short-Digest([string]$digest) {
    if ([string]::IsNullOrWhiteSpace($digest)) { return 'none' }
    if ($digest.Length -le 12) { return $digest }
    return $digest.Substring(0, 12)
}

# Image-Digest reads piforge.source-digest off an image, or $null when it has
# none (built before this check, or by hand).
function Image-Digest([string]$ref) {
    $value = & docker image inspect -f '{{index .Config.Labels "piforge.source-digest"}}' $ref 2>$null
    if ($LASTEXITCODE -ne 0) { return $null }
    $value = ($value | Out-String).Trim()
    if ($value -eq '<no value>' -or [string]::IsNullOrWhiteSpace($value)) { return $null }
    return $value
}


# Assert-ImageFresh refuses to start a stack from an image this tree did not
# build.
#
# The build phase above makes this a no-op when it ran. The path it exists for is
# -NoBuild, where the freshness decision is deliberately skipped: without this,
# a stale image starts silently and the first symptom is an audit probe measuring
# a binary from three commits ago. That is not hypothetical — a prebuilt
# piforge/sandbox-agent:isolated masked two Linux quota fixes until
# scripts/verify-linux.sh happened to fail on them (docs/LINUX-VERIFY.md).
function Assert-ImageFresh([string]$tag, [string]$dockerfile, [string]$agentImage) {
    $want = (Source-Digest $dockerfile $agentImage).Digest
    $have = Image-Digest $tag
    if ($have -eq $want) {
        Write-Host ("    {0} is current (digest {1})" -f $tag, (Short-Digest $want))
        return
    }
    $why = if ($null -eq $have) {
        "carries no piforge.source-digest label, so nothing shows which tree it came from"
    }
    else {
        "was built from another tree (image $(Short-Digest $have), this tree $(Short-Digest $want))"
    }
    Fail ("{0} {1}; refusing to start it. Rebuild with: pwsh scripts/up-isolated.ps1 -Project {2} -Rebuild (add -BuildOnly to rebuild without starting)" -f $tag, $why, $Project)
}

function Build-File([string]$dockerfile, [string]$tag, [string[]]$extraArgs) {
    Step "building $tag ($dockerfile)"
    $args = @("build", "-f", $dockerfile, "-t", $tag) + $extraArgs + @(".")
    Invoke-Docker -Arguments $args | Out-Null
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Fail "docker is not on PATH; the isolated deployment runs three containers and needs Docker Engine 25+"
}

# The effective port is printed before anything is removed, built or started:
# it is the one value an operator has to know to find the stack again.
Step "isolated stack: project $Project, effective port $Port (http://127.0.0.1:$Port)"

# Down first: it is the only operation that must work even when the images are
# stale or the compose file has grown a required variable.
if ($Down) {
    Step "removing the compose stack, its sandboxes and its volumes"
    # The compose file sets the provider too. Stating it here is the script's own
    # answer to "which shape?": host placement is no longer a default (adversarial
    # round 6), and this script only ever starts the isolated one.
    $env:PIFORGE_SANDBOX = "runner"
    $env:PIFORGE_IMAGE = $controlPlaneImage
    $env:PIFORGE_RUNNER_IMAGE = $runnerImage
    $env:PIFORGE_SANDBOX_IMAGE = $sandboxImage
    $env:PIFORGE_PUBLISH = "$Port"
    $env:PIFORGE_DATA_VOLUME = $DataVolume
    $env:PIFORGE_TENANT_LIMITS_MOUNT = $LimitsFile
    $env:PIFORGE_TENANT_EGRESS_MOUNT = $EgressFile
    $env:PIFORGE_URL_ALLOW_MOUNT = $UrlAllowFile
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

# A compose project publishes exactly one port. Re-running the same project with
# another -Port re-creates its containers on the new port and the old one simply
# stops answering — a live stack re-pointed without a word (adversarial round 6).
# So: the port a project is already on is read from Docker, and a different
# -Port is refused unless it is said out loud with -Force. `-Down` runs before
# this and therefore works whatever port was passed.

# Get-PublishedPort is the host port the project's control plane is published
# on, or $null when the project is not up (or publishes none).
function Get-PublishedPort([string]$project) {
    $ids = @(& docker ps -q --filter "label=com.docker.compose.project=$project" 2>$null)
    foreach ($id in $ids) {
        if (-not $id) { continue }
        $service = (& docker inspect -f '{{index .Config.Labels "com.docker.compose.service"}}' $id 2>$null | Out-String).Trim()
        if ($service -ne 'piforge') { continue }
        $raw = (& docker inspect -f '{{json .NetworkSettings.Ports}}' $id 2>$null | Out-String).Trim()
        if ([string]::IsNullOrWhiteSpace($raw) -or $raw -eq 'null') { continue }
        try { $published = $raw | ConvertFrom-Json } catch { continue }
        foreach ($entry in @($published.'7700/tcp')) {
            if ($entry -and $entry.HostPort) { return [int]$entry.HostPort }
        }
    }
    return $null
}

$livePort = Get-PublishedPort $Project
if ($null -ne $livePort -and $livePort -ne $Port) {
    if (-not $Force) {
        $refusal = (
            "project {0} is already up on port {1}; refusing to re-point it to {2} " +
            "(the old port would stop answering). Pass -Force to move it, or -Down first."
        ) -f $Project, $livePort, $Port
        Fail $refusal
    }
    Write-Host ("    -Force: moving project {0} from port {1} to {2}; {1} will stop answering" -f $Project, $livePort, $Port) -ForegroundColor Yellow
}
elseif ($null -ne $livePort) {
    Write-Host ("    project {0} is already up on this port; the stack will be re-created in place" -f $Project)
}

# ---------------------------------------------------------------------------
# Build. Order matters: the sandbox image is built *from* the control plane, so
# `pi` and /opt/piforge/piforged are exactly the version the control plane runs.
# ---------------------------------------------------------------------------
$script:ImageState = @{}

# Write-ImageSummary prints, for each image, what actually ran and why: id,
# build time, the decision (rebuilt for this reason / current) and the digest.
# "The tag exists" is not the same claim as "the tag is this tree", and the log
# line an operator reads is the only place that difference is visible.
function Write-ImageSummary {
    Write-Host "  images   : $controlPlaneImage, $runnerImage, $sandboxImage"
    foreach ($ref in @($controlPlaneImage, $runnerImage, $sandboxImage)) {
        $created = Image-Created $ref
        $stamp = if ($created) { $created.ToString('yyyy-MM-dd HH:mm:ss') + 'Z' } else { 'missing' }
        $state = $script:ImageState[$ref]
        $decision = 'not checked (the build phase was skipped)'
        if ($state -and $state.Rebuilt) { $decision = 'rebuilt (' + $state.Why + ')' }
        elseif ($state -and $state.Why) { $decision = $state.Why }
        $digest = 'none'
        if ($state -and $state.Digest) { $digest = Short-Digest $state.Digest }
        Write-Host ("             {0,-34} id {1}  built {2}  digest {3}  {4}" -f $ref, (Image-Id $ref), $stamp, $digest, $decision)
    }
}

if (-not $NoBuild -and -not $Check) {
    # Go is needed before anything is built now, not only for the preflight: the
    # freshness rule itself is backend/cmd/sourcedigest. A missing toolchain has
    # to fail here rather than degrade into "everything is current".
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Fail "go is not on PATH; the source digest (backend/cmd/sourcedigest) and the preflight are Go programs and need Go 1.25+"
    }
    $source = Source-Digest "Dockerfile" ""
    Step ("source digest: {0} files, {1}" -f $source.Files, (Short-Digest $source.Digest))

    # Build-IfStale decides from content: the digest label against the digest of
    # this tree. -Rebuild forces; a missing image is built; an image with no
    # label, or a label from another tree, is rebuilt.
    function Build-IfStale([string]$tag, [string]$dockerfile, [string[]]$extraArgs, [string]$agentImage, [switch]$StampDockerfile) {
        $created = Image-Created $tag
        $want = (Source-Digest $dockerfile $agentImage).Digest
        $have = if ($created) { Image-Digest $tag } else { $null }
        $why = $null
        if ($null -eq $created) {
            $why = "the image was missing"
        }
        elseif ($Rebuild) {
            $why = "-Rebuild"
        }
        elseif ($null -eq $have) {
            $why = "the image carries no piforge.source-digest label (built before this check, or by hand)"
        }
        elseif ($have -ne $want) {
            $why = "the source digest differs (image $(Short-Digest $have), tree $(Short-Digest $want))"
        }
        if (-not $why) {
            Write-Host ("    {0} is current (id {1}, built {2}, digest {3})" -f $tag, (Image-Id $tag), ($created.ToString('yyyy-MM-dd HH:mm:ss') + 'Z'), (Short-Digest $want))
            $script:ImageState[$tag] = @{ Rebuilt = $false; Why = 'current'; Digest = $want }
            return $false
        }
        Step "rebuilding ${tag}: $why"
        $labelArgs = @("--label", "piforge.source-digest=$want")
        if ($StampDockerfile) {
            # This recipe's Dockerfile stamps the label itself, so a build that
            # does not go through this script — the `docker build` line in the
            # header of deploy/Dockerfile.sandbox-agent — still carries it. Both
            # paths set the same value.
            $labelArgs += @("--build-arg", "PIFORGE_SOURCE_DIGEST=$want")
        }
        Build-File $dockerfile $tag ($labelArgs + $extraArgs)
        $script:ImageState[$tag] = @{ Rebuilt = $true; Why = $why; Digest = $want }
        return $true
    }

    Build-IfStale $controlPlaneImage "Dockerfile" @() "" | Out-Null
    Build-IfStale $runnerImage "deploy/Dockerfile.runner" @() "" | Out-Null
    # The sandbox image is built *from* the control plane, so the control-plane
    # image's id is part of its digest: a rebuilt control plane changes this
    # digest and the sandbox is rebuilt by the same rule as everything else.
    $sandboxArgs = @("--build-arg", "AGENT_IMAGE=$controlPlaneImage")
    Build-IfStale $sandboxImage "deploy/Dockerfile.sandbox-agent" $sandboxArgs $controlPlaneImage -StampDockerfile | Out-Null
}

if ($BuildOnly) {
    Write-Host ""
    Write-ImageSummary
    Write-Host "build only (-BuildOnly): nothing was started." -ForegroundColor Green
    exit 0
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

# -NoBuild skipped the freshness phase, so it is the one path that could start a
# stack from images this tree did not build. Compare the labels anyway and refuse
# with the command that fixes it, rather than starting something stale and
# leaving the mismatch for an assertion in the middle of a run.
if ($NoBuild) {
    Step "freshness (-NoBuild: the build phase was skipped, so the images are checked here)"
    Assert-ImageFresh $controlPlaneImage "Dockerfile" ""
    Assert-ImageFresh $runnerImage "deploy/Dockerfile.runner" ""
    Assert-ImageFresh $sandboxImage "deploy/Dockerfile.sandbox-agent" $controlPlaneImage
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
$env:PIFORGE_SANDBOX = "runner"
$env:PIFORGE_IMAGE = $controlPlaneImage
$env:PIFORGE_RUNNER_IMAGE = $runnerImage
$env:PIFORGE_SANDBOX_IMAGE = $sandboxImage
$env:PIFORGE_PUBLISH = "$Port"
$env:PIFORGE_DATA_VOLUME = $DataVolume
$env:PIFORGE_TENANT_LIMITS_MOUNT = $LimitsFile
    $env:PIFORGE_TENANT_EGRESS_MOUNT = $EgressFile
$env:PIFORGE_URL_ALLOW_MOUNT = $UrlAllowFile
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
Write-Host "  port     : $Port (project $Project; http://127.0.0.1:$Port)"
Write-Host "  accounts : admin@piforge.local / piforge-admin-123 (change before exposing)"
Write-Host "              dev@piforge.local   / piforge-dev-123"
# Each image's id, build time, digest and the reason it was or was not rebuilt:
# "the tag exists" is not the same claim as "the tag is this tree", and an audit
# measured the wrong binary once because of the difference (adversarial rounds 5-6).
Write-ImageSummary
Write-Host "  data     : volume $DataVolume"
Write-Host "  provider : $DefaultProvider ($Provider)"
Write-Host "  egress   : $EgressFile (default none: no route to the internet; the gateway on the tenant bridge still answers)"
Write-Host "  url policy: $UrlAllowFile (default empty: the control plane fetches public http(s) only; an internal destination must be written down)"
Write-Host "  stop     : pwsh scripts/up-isolated.ps1 -Project $Project -Down"
if ([string]::IsNullOrWhiteSpace($ProviderKey)) {
    Write-Host "  note     : no provider key was supplied; set -ProviderKey (or PIFORGE_DRILL_KEY) for a real prompt," -ForegroundColor Yellow
    Write-Host "             or open Settings -> Providers once the stack is up."
}

