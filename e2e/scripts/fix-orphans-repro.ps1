# FIX 1 reproduction / verification: deleting a project must not orphan its sandbox.
#
# On Windows `os.RemoveAll` of `sandboxes/<user>/<project>` fails while anything
# still holds a file inside it (the agent process, a terminal, the sandbox
# browser, or — as this script simulates — an unrelated process). The old
# handler logged the failure and deleted the project row anyway, so the
# directory stayed behind with nothing pointing at it.
#
# The script starts the real control plane from `e2e/.bin/piforged.exe` on a
# throwaway data dir, creates a project + a session (which materialises the
# sandbox), opens a file handle inside the checkout, and deletes the project.
#
#   pwsh e2e/scripts/fix-orphans-repro.ps1
#
# It prints the before/after facts and exits non-zero when the promise is not
# kept. The exact evidence this produced is quoted in docs/STATUS.md.
param(
    [int]$Port = 8099,
    [string]$DataDir = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$exe = Join-Path $repoRoot "e2e\.bin\piforged.exe"
if (-not (Test-Path $exe)) {
    throw "missing $exe — run 'node e2e/scripts/prepare.mjs' first"
}
if ([string]::IsNullOrWhiteSpace($DataDir)) {
    $DataDir = Join-Path $repoRoot "e2e\.artifacts\fix-orphans\win-data-$(Get-Date -Format 'HHmmss')"
}
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
$log = Join-Path $DataDir "server.log"

# The dev account is seeded on first boot; PIFORGE_SEED_USER_* is the same pair
# the browser suite uses.
$env:PIFORGE_SEED_EMAIL = "admin@piforge.local"
$env:PIFORGE_SEED_PASSWORD = "piforge-admin-123"
$env:PIFORGE_SEED_USER_EMAIL = "dev@piforge.local"
$env:PIFORGE_SEED_USER_PASSWORD = "piforge-dev-123"
$env:PIFORGE_FAKE_PI_DELAY_MS = "10"
$env:PIFORGE_AGENT_IDLE_TTL = "45s"

$base = "http://127.0.0.1:$Port"
$srv = Start-Process -FilePath $exe -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError "$log.err" -ArgumentList @(
    "-addr", "127.0.0.1:$Port",
    "-data", $DataDir,
    "-fake-pi",
    "-dev"
)

function Wait-Health {
    for ($i = 0; $i -lt 100; $i++) {
        try {
            $r = Invoke-WebRequest -Uri "$base/healthz" -UseBasicParsing -TimeoutSec 2
            if ($r.StatusCode -eq 200) { return }
        } catch { Start-Sleep -Milliseconds 150 }
    }
    throw "piforged did not become healthy; see $log"
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        [Microsoft.PowerShell.Commands.WebRequestSession]$Session,
        [string]$Csrf = ""
    )
    $headers = @{}
    if ($Csrf) { $headers["X-PF-CSRF"] = $Csrf }
    $req = @{
        Uri                = "$base$Path"
        Method             = $Method
        WebSession         = $Session
        Headers            = $headers
        UseBasicParsing    = $true
        SkipHttpErrorCheck = $true
        TimeoutSec         = 60
    }
    if ($null -ne $Body) {
        $req["ContentType"] = "application/json"
        $req["Body"] = ($Body | ConvertTo-Json -Depth 6 -Compress)
    }
    $res = Invoke-WebRequest @req
    $parsed = $null
    if ($res.Content) { try { $parsed = $res.Content | ConvertFrom-Json } catch { } }
    [pscustomobject]@{ Status = [int]$res.StatusCode; Body = $parsed }
}

try {
    Wait-Health
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $login = Invoke-Api -Method POST -Path "/v1/auth/login" -Session $session -Body @{
        email = "dev@piforge.local"; password = "piforge-dev-123"
    }
    if ($login.Status -ne 200) { throw "login failed: $($login.Status)" }
    $csrf = ($session.Cookies.GetCookies($base) | Where-Object { $_.Name -eq "pf_csrf" }).Value
    $userID = $login.Body.user.id

    $stamp = Get-Date -Format "HHmmss"
    $project = Invoke-Api -Method POST -Path "/v1/projects" -Session $session -Csrf $csrf -Body @{
        name = "orphan-repro-$stamp"
    }
    if ($project.Status -ne 201) { throw "create project: $($project.Status) $($project.Body)" }
    $projectID = $project.Body.project.id

    $created = Invoke-Api -Method POST -Path "/v1/projects/$projectID/sessions" -Session $session -Csrf $csrf -Body @{}
    if ($created.Status -ne 201) { throw "create session: $($created.Status) $($created.Body)" }

    $sandboxRoot = Join-Path $DataDir "sandboxes"
    $projectDir = Join-Path $sandboxRoot "$userID\$projectID"
    $repoDir = Join-Path $projectDir "repo"
    for ($i = 0; $i -lt 100 -and -not (Test-Path $repoDir); $i++) { Start-Sleep -Milliseconds 100 }

    Write-Host "[setup] project  = $projectID"
    Write-Host "[setup] sandbox  = $projectDir"
    Write-Host "[setup] repo dir exists = $(Test-Path $repoDir)"

    # The held handle. FileShare.None is what a running agent/terminal/chrome
    # effectively does to a file under the checkout as far as Windows is
    # concerned: RemoveAll cannot delete it.
    $heldPath = Join-Path $repoDir "held-by-a-process.txt"
    Set-Content -Path $heldPath -Value "held" -NoNewline
    $handle = [System.IO.File]::Open($heldPath, "Open", "Read", "None")

    try {
        $first = Invoke-Api -Method DELETE -Path "/v1/projects/$projectID" -Session $session -Csrf $csrf
        Write-Host "[delete #1, handle held] status=$($first.Status) body=$($first.Body | ConvertTo-Json -Compress)"

        $listAfterFirst = Invoke-Api -Method GET -Path "/v1/projects" -Session $session
        $rowGone = -not ($listAfterFirst.Body.projects | Where-Object { $_.id -eq $projectID })
        $dirStillThere = Test-Path $projectDir
        Write-Host "[delete #1] project row gone = $rowGone; sandbox dir still on disk = $dirStillThere"

        if ($first.Status -eq 200 -and $rowGone -and $dirStillThere) {
            Write-Host "[BROKEN] delete reported success, the row is gone, and the sandbox directory is orphaned"
        } elseif ($first.Status -ge 400 -and -not $rowGone) {
            Write-Host "[ok] the delete was refused with a message (project kept, nothing orphaned)"
            Write-Host "      message: $($first.Body.error.message)"
        } else {
            Write-Host "[?] unexpected combination: status=$($first.Status) rowGone=$rowGone dir=$dirStillThere"
        }
    } finally {
        $handle.Dispose()
    }

    # With the blocker released, a retry must finish the job: the row goes and
    # the directory goes with it, so there is no orphan either way.
    $second = Invoke-Api -Method DELETE -Path "/v1/projects/$projectID" -Session $session -Csrf $csrf
    $listAfterSecond = Invoke-Api -Method GET -Path "/v1/projects" -Session $session
    $rowGone2 = -not ($listAfterSecond.Body.projects | Where-Object { $_.id -eq $projectID })
    $dirGone2 = -not (Test-Path $projectDir)
    Write-Host "[retry after release] status=$($second.Status) project row gone = $rowGone2; sandbox dir gone = $dirGone2"

    # The janitor half: a directory with no project row must be swept by the
    # control plane itself (this is what cleans what older builds left behind).
    # The name has to look like a project id — the sweep refuses to touch
    # anything else in the data directory.
    $orphanProject = "prj_01ORPHAN" + $stamp + "000"
    $orphanDir = Join-Path $sandboxRoot "$userID\$orphanProject"
    New-Item -ItemType Directory -Force -Path (Join-Path $orphanDir "repo") | Out-Null
    Set-Content -Path (Join-Path $orphanDir "repo\README.md") -Value "orphan"
    $orphanState = Join-Path $sandboxRoot "_state\$userID\$orphanProject.json"
    New-Item -ItemType Directory -Force -Path (Split-Path $orphanState) | Out-Null
    Set-Content -Path $orphanState -Value '{"id":"sbx_orphan"}'
    Write-Host "[janitor] planted $orphanDir"

    # The sweep runs at startup, so a restart is the operator's remedy for what
    # older builds orphaned. It also proves the pass runs before serving.
    Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 400
    $srv = Start-Process -FilePath $exe -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError "$log.err" -ArgumentList @(
        "-addr", "127.0.0.1:$Port", "-data", $DataDir, "-fake-pi", "-dev"
    )
    Wait-Health
    $swept = $false
    for ($i = 0; $i -lt 100; $i++) {
        if (-not (Test-Path $orphanDir)) { $swept = $true; break }
        Start-Sleep -Milliseconds 100
    }
    $stateGone = -not (Test-Path $orphanState)
    Write-Host "[janitor] orphan directory swept = $swept; orphan state file gone = $stateGone"

    if ($rowGone2 -and $dirGone2 -and $swept -and $stateGone) {
        Write-Host "[RESULT] PASS: a failed delete is refused (project kept, nothing orphaned), a retry removes both row and directory, and the startup sweep removes a directory with no project row"
        exit 0
    }
    Write-Host "[RESULT] FAIL: the delete left the project and its sandbox in an inconsistent state"
    exit 1
} finally {
    if ($srv -and -not $srv.HasExited) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue }
    Write-Host "--- server log (delete / janitor lines) ---"
    foreach ($file in @($log, "$log.err")) {
        if (Test-Path $file) {
            Select-String -Path $file -Pattern "delete project|sandbox destroy|orphan|janitor|sweep" |
                ForEach-Object { $_.Line } | Select-Object -Last 8
        }
    }
}
