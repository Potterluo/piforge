# Build the iteration image: this repository's artifacts on top of the published
# stack (node, pi, chromium, git).
#
# The full Dockerfile rebuilds everything from source and takes ~20 minutes on a
# good link. This path exists because during development only the two artifacts
# below change, and rebuilding an npm tree and a Chromium to see a Go change is a
# waste of the one resource that is actually scarce.
#
#   pwsh scripts/build-app-image.ps1              # rebuild artifacts + image
#   pwsh scripts/build-app-image.ps1 -NoBuild     # reuse existing artifacts
#   pwsh scripts/build-app-image.ps1 -Tag mine    # different tag
param(
    [string]$Tag = "piforge:local",
    # The stack this image layers on: the local full build (`piforge:dev`,
    # deploy/README.md "Build"). Pass -Base to layer on a published tag of your
    # own instead; none is assumed here, because that tag is not this project's
    # to name.
    [string]$Base = "piforge:dev",
    [switch]$NoBuild
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$stage = Join-Path $root "dist/app-image"

if (-not $NoBuild) {
    Write-Host "building the control plane for linux/amd64"
    Push-Location (Join-Path $root "backend")
    $env:CGO_ENABLED = "0"; $env:GOOS = "linux"; $env:GOARCH = "amd64"
    go build -trimpath -ldflags="-s -w" -o (Join-Path $root "dist/piforged-linux") ./cmd/piforged
    if ($LASTEXITCODE -ne 0) { throw "control plane build failed" }
    go build -trimpath -ldflags="-s -w" -o (Join-Path $root "dist/fakepi-linux") ./cmd/fakepi
    if ($LASTEXITCODE -ne 0) { throw "fakepi build failed" }
    $env:CGO_ENABLED = ""; $env:GOOS = ""; $env:GOARCH = ""
    Pop-Location

    Write-Host "building the web app"
    # `.env.local` points the bundle at the development API port. Next inlines
    # NEXT_PUBLIC_* at build time, so a bundle built with it present would send the
    # terminal and browser sockets to a port the container never listens on — the
    # exact bug this script would otherwise reintroduce on every rebuild. Move it
    # aside for the build that ships inside the image.
    $envLocal = Join-Path $root "web/.env.local"
    $stashed = "$envLocal.image-build"
    $hadEnv = Test-Path $envLocal
    if ($hadEnv) { Move-Item $envLocal $stashed -Force }
    Push-Location (Join-Path $root "web")
    try {
        pnpm exec next build
        if ($LASTEXITCODE -ne 0) { throw "web build failed" }
    }
    finally {
        Pop-Location
        if ($hadEnv) { Move-Item $stashed $envLocal -Force }
    }
}

Write-Host "staging $stage"
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path "$stage/bin", "$stage/web" | Out-Null
Copy-Item "$root/dist/piforged-linux" "$stage/bin/piforged"
Copy-Item "$root/dist/fakepi-linux" "$stage/bin/fakepi"
Copy-Item "$root/deploy/entrypoint.sh" "$stage/entrypoint.sh"
Copy-Item "$root/web/.next" "$stage/web/.next" -Recurse
Copy-Item "$root/web/public" "$stage/web/public" -Recurse

Write-Host "building the image"
Push-Location $stage
docker build -f "$root/deploy/Dockerfile.app" -t $Tag --build-arg "BASE=$Base" .
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "image build failed" }
Pop-Location

Write-Host "done: $Tag"
