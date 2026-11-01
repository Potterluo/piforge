# Proves that `up-isolated.ps1` decides image staleness from *content*, not from
# timestamps.
#
#   pwsh scripts/verify-image-freshness.ps1            # -BuildOnly runs, no stack started
#   pwsh scripts/verify-image-freshness.ps1 -Keep      # leave the edited file (do not restore)
#
# The four cases the sixth adversarial round asked for, plus the one the fourth
# cannot help creating:
#
#   (a) a backdated edit to a real source file  -> all three images rebuild
#   (b) the timestamp restored, content unchanged -> no rebuild
#   (c) a file touched, content unchanged        -> no rebuild (digest equal)
#   (d) -Rebuild                                 -> all three rebuild
#   (e) the content restored                     -> rebuilds once back to the
#       committed tree, then reports current
#
# (e) is not a failure of the rule, it is the rule: the image built by (a) holds
# the edited bytes, so restoring the file changes the digest again. What (b) and
# (c) prove is the other direction — a clock difference alone never rebuilds.
#
# Every run is `-BuildOnly`: the decision is exercised, the stack is not started
# and `pf-local` (7722) is not touched. The evidence is the output text.
param(
    [string]$Project = "pf-isolated",
    [switch]$Keep
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$up = Join-Path $PSScriptRoot "up-isolated.ps1"
$edited = "backend/internal/fsx/fsx.go"
$touched = "web/package.json"

$controlPlane = "piforge:isolated"
$runner = "piforge/sandbox-runner:isolated"
$sandbox = "piforge/sandbox-agent:isolated"
$all = @($controlPlane, $runner, $sandbox)

$script:Failures = 0
function Say($message) { Write-Host $message }

function Assert-Case([string]$Name, [bool]$OK, [string]$Detail) {
    $tag = if ($OK) { "PASS" } else { "FAIL" }
    $color = if ($OK) { "Green" } else { "Red" }
    Write-Host ("  [{0}] {1} — {2}" -f $tag, $Name, $Detail) -ForegroundColor $color
    if (-not $OK) { $script:Failures++ }
}

# Invoke-Build runs the build phase only and returns its output lines.
function Invoke-Build([string[]]$extra) {
    $lines = & pwsh -NoProfile -File $up -Project $Project -BuildOnly @extra 2>&1 | ForEach-Object { "$_" }
    return @($lines)
}

# Decisions maps each image tag to "current" or "rebuild:<why>" as printed.
function Decisions([string[]]$lines) {
    $map = @{}
    foreach ($ref in $all) { $map[$ref] = "absent" }
    foreach ($line in $lines) {
        if ($line -match '^\s*(\S+) is current') { $map[$matches[1]] = "current" }
        elseif ($line -match '^==> rebuilding (\S+): (.*)$') { $map[$matches[1]] = "rebuild:" + $matches[2] }
    }
    return $map
}

function Show-Lines([string[]]$lines) {
    foreach ($line in $lines) {
        if ($line -match 'is current|^==> rebuilding|^==> source digest|a file is newer') {
            Say ("    | " + $line.Trim())
        }
    }
}

function Expect([string]$case, [hashtable]$map, [string[]]$want) {
    $ok = $true
    $detail = @()
    foreach ($ref in $all) {
        $got = $map[$ref]
        $index = [array]::IndexOf($all, $ref)
        $expected = $want[$index]
        $detail += ("{0}={1}" -f $ref, $got)
        if ($expected -eq 'current') {
            if ($got -ne 'current') { $ok = $false }
        }
        else {
            if (-not $got.StartsWith('rebuild:')) { $ok = $false }
        }
    }
    Assert-Case $case $ok ($detail -join "; ")
    return $ok
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: docker is not on PATH" -ForegroundColor Red
    exit 1
}
$dirty = (& git -C $root status --porcelain -- $edited 2>$null | Out-String).Trim()
if ($dirty) {
    Write-Host "ERROR: $edited has uncommitted changes; this script edits and restores it." -ForegroundColor Red
    Write-Host "       commit or stash them first (they are not mine to throw away)."
    exit 1
}
$originalStamp = (Get-Item (Join-Path $root $edited)).LastWriteTimeUtc
$restored = $false
function Restore-Edit {
    if ($script:restored) { return }
    & git -C $root checkout -- $edited 2>$null | Out-Null
    (Get-Item (Join-Path $root $edited)).LastWriteTimeUtc = $originalStamp
    $script:restored = $true
}
try {
    Write-Host ""
    Write-Host "== case 0: the baseline is current" -ForegroundColor Cyan
    $lines = Invoke-Build @()
    Show-Lines $lines
    $map = Decisions $lines
    # The first run after this check was introduced rebuilds images that carry
    # no digest label yet (and a missing image is built). After at most two such
    # runs every image must report current: a rule that keeps rebuilding its own
    # output would never settle.
    for ($i = 0; $i -lt 2; $i++) {
        if (@($map.Values | Where-Object { $_ -ne 'current' }).Count -eq 0) { break }
        $lines = Invoke-Build @()
        Show-Lines $lines
        $map = Decisions $lines
    }
    Expect "baseline: all three images are current" $map @("current", "current", "current") | Out-Null

    Write-Host ""
    Write-Host "== case (a): a backdated edit rebuilds" -ForegroundColor Cyan
    $path = Join-Path $root $edited
    Add-Content -LiteralPath $path -Value "// verify-image-freshness: a deliberately backdated edit."
    (Get-Item $path).LastWriteTimeUtc = [datetime]::UtcNow.AddYears(-3)
    $stampText = (Get-Item $path).LastWriteTimeUtc.ToString("yyyy-MM-dd HH:mm:ss") + "Z"
    Say "    edited $edited and set its mtime to $stampText (older than every image)"
    $lines = Invoke-Build @()
    Show-Lines $lines
    $map = Decisions $lines
    Expect "backdated edit -> all three rebuild" $map @("rebuild", "rebuild", "rebuild") | Out-Null
    Assert-Case "the rebuild reason is the digest, not the clock" `
        (($map[$controlPlane] -like "*source digest differs*") -and ($map[$runner] -like "*source digest differs*")) `
        $map[$controlPlane]

    Write-Host ""
    Write-Host "== case (b): restoring the timestamp does not rebuild" -ForegroundColor Cyan
    (Get-Item $path).LastWriteTimeUtc = [datetime]::UtcNow
    Say "    restored the mtime to now; the bytes are the ones the image was just built from"
    $lines = Invoke-Build @()
    Show-Lines $lines
    $map = Decisions $lines
    Expect "mtime restored -> no rebuild" $map @("current", "current", "current") | Out-Null

    Write-Host ""
    Write-Host "== case (c): a touch without a content change does not rebuild" -ForegroundColor Cyan
    (Get-Item (Join-Path $root $touched)).LastWriteTimeUtc = [datetime]::UtcNow.AddMinutes(5)
    Say "    touched $touched (mtime now + 5 min, bytes unchanged)"
    $lines = Invoke-Build @()
    Show-Lines $lines
    $map = Decisions $lines
    Expect "touch only -> no rebuild" $map @("current", "current", "current") | Out-Null

    Write-Host ""
    Write-Host "== case (d): -Rebuild always rebuilds" -ForegroundColor Cyan
    $lines = Invoke-Build @("-Rebuild")
    Show-Lines $lines
    $map = Decisions $lines
    Expect "-Rebuild -> all three rebuild" $map @("rebuild", "rebuild", "rebuild") | Out-Null
    Assert-Case "-Rebuild says so" ($map[$sandbox] -like "*Rebuild*") $map[$sandbox]

    Write-Host ""
    Write-Host "== case (e): the committed content comes back, and settles" -ForegroundColor Cyan
    Restore-Edit
    Say "    restored $edited to HEAD"
    $lines = Invoke-Build @()
    Show-Lines $lines
    $map = Decisions $lines
    Expect "restore -> rebuilds the image back to the committed tree" $map @("rebuild", "rebuild", "rebuild") | Out-Null
    $lines = Invoke-Build @()
    Show-Lines $lines
    $map = Decisions $lines
    Expect "the run after that is current (no rebuild loop)" $map @("current", "current", "current") | Out-Null

    # A last look at the labels the images now carry: equal to the tree digest.
    Say ""
    Say "  image labels:"
    foreach ($ref in $all) {
        $label = (& docker image inspect -f '{{index .Config.Labels "piforge.source-digest"}}' $ref 2>$null | Out-String).Trim()
        Say ("    {0,-34} piforge.source-digest={1}" -f $ref, $label)
        Assert-Case "$ref carries a 64-hex source digest" ($label -match '^[0-9a-f]{64}$') $label
    }
}
finally {
    if (-not $Keep) { Restore-Edit }
    $status = (& git -C $root status --porcelain -- $edited 2>$null | Out-String).Trim()
    if ($status) {
        Write-Host "WARNING: $edited is still modified: $status" -ForegroundColor Yellow
    }
}

Write-Host ""
if ($script:Failures -gt 0) {
    Write-Host ("FAILED: {0} assertion(s)" -f $script:Failures) -ForegroundColor Red
    exit 1
}
Write-Host "PASS: the digest decides; timestamps never do." -ForegroundColor Green
exit 0
