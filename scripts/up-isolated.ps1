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
# reused only when that label matches the tree.
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

# Newest-Source is the last time anything this build compiles was touched, in UTC.
#
# It is the *fallback* signal, not the decision. Timestamps cannot tell "the same
# bytes" from "the same bytes, touched", and a file edited with its mtime set
# into the past made the old check print "is current" while the tree differed
# (adversarial round 6) — which is how a "verified green" claim can measure a
# stale image. The content digest below decides; this is consulted only when no
# digest can be computed at all.
function Newest-Source([string]$root, [string[]]$files) {
    $newest = [DateTime]::MinValue
    foreach ($rel in $files) {
        $full = Join-Path $root $rel
        if (-not (Test-Path -LiteralPath $full)) { continue }
        $stamp = (Get-Item -LiteralPath $full -ErrorAction SilentlyContinue).LastWriteTimeUtc
        if ($stamp -and $stamp -gt $newest) { $newest = $stamp }
    }
    if ($newest -eq [DateTime]::MinValue) { return $null }
    return $newest
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
# ---------------------------------------------------------------------------

# Source-Files lists what a build compiles: tracked files, plus untracked ones
# that git does not ignore, under the four source roots. Stable order, because
# the digest must not depend on the filesystem's iteration order.
function Source-Files([string]$root) {
    $files = @()
    if (Get-Command git -ErrorAction SilentlyContinue) {
        Push-Location $root
        try {
            $tracked = & git ls-files -z -- backend web deploy Dockerfile docker-compose.runner.yml 2>$null
            $untracked = & git ls-files -z -o --exclude-standard -- backend web deploy Dockerfile docker-compose.runner.yml 2>$null
            foreach ($blob in @($tracked, $untracked)) {
                if ($null -eq $blob) { continue }
                $files += ($blob -join "`n") -split "`0" | Where-Object { $_ }
            }
        }
        finally { Pop-Location }
    }
    if ($files.Count -eq 0) {
        # No git: the Dockerfiles and the compose file are the whole list that
        # can be trusted to exist, and an edit there is still a rebuild.
        $files = @('Dockerfile', 'deploy/Dockerfile.runner', 'deploy/Dockerfile.sandbox-agent', 'docker-compose.runner.yml')
    }
    return @($files | Sort-Object -Unique)
}

# Source-Rows pairs each existing file with the hash of its *content*:
# "<rel-path>`0<blob-sha>". `git hash-object` reads the working tree (not the
# index) and does all 700 files in one process (~0.1 s). The .NET fallback is
# for a checkout without git; the decision must not silently degrade to mtimes
# just because a binary is missing.
function Source-Rows([string]$root, [string[]]$files) {
    $existing = @($files | Where-Object { Test-Path -LiteralPath (Join-Path $root $_) })
    if ($existing.Count -eq 0) { return @() }
    $blobs = @()
    if (Get-Command git -ErrorAction SilentlyContinue) {
        Push-Location $root
        try { $blobs = @($existing | & git hash-object --stdin-paths 2>$null) }
        finally { Pop-Location }
    }
    if ($blobs.Count -ne $existing.Count) {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try {
            $blobs = @(foreach ($rel in $existing) {
                $stream = [System.IO.File]::OpenRead((Join-Path $root $rel))
                try { [Convert]::ToHexString($sha.ComputeHash($stream)).ToLowerInvariant() }
                finally { $stream.Dispose() }
            })
        }
        finally { $sha.Dispose() }
    }
    $rows = New-Object 'System.Collections.Generic.List[string]'
    for ($i = 0; $i -lt $existing.Count; $i++) {
        $rows.Add(("{0}`0{1}" -f $existing[$i], ([string]$blobs[$i]).Trim()))
    }
    return $rows.ToArray()
}

# Source-Digest folds the rows, the Dockerfile and the build args into one
# digest. The Dockerfile name and the args are inputs too: the three images are
# built from the same tree but not from the same recipe, and the sandbox image's
# AGENT_IMAGE names the control-plane image id it was built on top of.
function Source-Digest([string[]]$rows, [string]$dockerfile, [string[]]$buildArgs) {
    $salt = "piforge-source-digest-v1`n" + $dockerfile + "`n" + (@($buildArgs) -join "`n")
    $text = $salt + "`n" + (@($rows) -join "`n")
    $hash = [System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::UTF8.GetBytes($text))
    return [Convert]::ToHexString($hash).ToLowerInvariant()
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

# Image-FullId is the untruncated id, for a digest salt that must change when the
# image does.
function Image-FullId([string]$ref) {
    $id = & docker image inspect -f '{{.Id}}' $ref 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($id)) { return 'missing' }
    return ($id | Out-String).Trim()
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
    $sourceFiles = Source-Files $root
    $sourceRows = Source-Rows $root $sourceFiles
    $newest = Newest-Source $root $sourceFiles
    $digestable = $sourceRows.Count -gt 0
    if ($digestable) {
        Step ("source digest: {0} files, {1}" -f $sourceRows.Count, (Short-Digest (Source-Digest $sourceRows "Dockerfile" @())))
    }
    else {
        Write-Host "    no source file list could be hashed; the freshness check falls back to mtimes" -ForegroundColor Yellow
    }

    # Build-IfStale decides from content: the digest label against the digest of
    # this tree. -Rebuild forces; a missing image is built. Only when no digest
    # exists at all (no git, no readable files) is the clock consulted, and then
    # it is named as the fallback it is.
    function Build-IfStale([string]$tag, [string]$dockerfile, [string[]]$extraArgs, [string[]]$saltArgs) {
        $created = Image-Created $tag
        $want = if ($digestable) { Source-Digest $sourceRows $dockerfile $saltArgs } else { $null }
        $have = if ($created) { Image-Digest $tag } else { $null }
        $why = $null
        if ($null -eq $created) {
            $why = "the image was missing"
        }
        elseif ($Rebuild) {
            $why = "-Rebuild"
        }
        elseif ($null -eq $want) {
            if ($newest -and $newest -gt $created) {
                $why = "source is newer than the image ($($newest.ToString('yyyy-MM-dd HH:mm:ss'))Z > $($created.ToString('yyyy-MM-dd HH:mm:ss'))Z; no digest available)"
            }
        }
        elseif ($null -eq $have) {
            $why = "the image carries no piforge.source-digest label (built before this check, or by hand)"
        }
        elseif ($have -ne $want) {
            $why = "the source digest differs (image $(Short-Digest $have), tree $(Short-Digest $want))"
        }
        if (-not $why) {
            Write-Host ("    {0} is current (id {1}, built {2}, digest {3})" -f $tag, (Image-Id $tag), ($created.ToString('yyyy-MM-dd HH:mm:ss') + 'Z'), (Short-Digest $want))
            if ($newest -and $newest -gt $created) {
                Write-Host "      a file is newer than the image, but its bytes are identical: not rebuilding"
            }
            $script:ImageState[$tag] = @{ Rebuilt = $false; Why = 'current'; Digest = $want }
            return $false
        }
        Step "rebuilding ${tag}: $why"
        $labelArgs = @()
        if ($want) { $labelArgs = @("--label", "piforge.source-digest=$want") }
        Build-File $dockerfile $tag ($labelArgs + $extraArgs)
        $script:ImageState[$tag] = @{ Rebuilt = $true; Why = $why; Digest = $want }
        return $true
    }

    Build-IfStale $controlPlaneImage "Dockerfile" @() @() | Out-Null
    Build-IfStale $runnerImage "deploy/Dockerfile.runner" @() @() | Out-Null
    # The sandbox image is built *from* the control plane, so the control-plane
    # image's id is part of its digest: a rebuilt control plane changes this
    # digest and the sandbox is rebuilt by the same rule as everything else.
    $agentId = Image-FullId $controlPlaneImage
    $sandboxArgs = @("--build-arg", "AGENT_IMAGE=$controlPlaneImage")
    Build-IfStale $sandboxImage "deploy/Dockerfile.sandbox-agent" $sandboxArgs @("AGENT_IMAGE=$agentId") | Out-Null
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

