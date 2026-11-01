# The one place that reads the drill's provider key.
#
# Two rounds of this drill leaked the key through error output: once because a
# failed command printed the environment it was given, once because a parameter
# binding echoed its value in an exception. The rule that came out of that is this
# file: every read goes through Get-DrillKeyValue, and every report about the key
# goes through Get-DrillKeyFingerprint, which prints a length and a hash prefix and
# nothing else.
#
#   pwsh scripts/drill-key.ps1              # prints len=... sha256=xxxxxxxx (never the key)
#   pwsh scripts/drill-key.ps1 -Check       # exit 1 when the key is missing or too short
#
# Dot-source it from another script:
#
#   . (Join-Path $PSScriptRoot "drill-key.ps1")
#   $env:PIFORGE_DRILL_KEY = Get-DrillKeyValue     # then run the child process
#
# Nothing here ever writes the value to the output stream except the function's
# own return value, which the caller must not print.

param(
    # The credentials file. Defaults to $env:PIFORGE_DRILL_CREDENTIALS, then
    # ~/.dsh/.credentials.yaml.
    [string]$Path = "",
    # The ref the key lives under. Line 21 of the default file is VOLCE_API_KEY.
    [string]$Name = "VOLCE_API_KEY",
    [switch]$Check
)

$ErrorActionPreference = "Stop"

function Get-DrillKeyPath {
    param([string]$Explicit = "")
    if (-not [string]::IsNullOrWhiteSpace($Explicit)) { return $Explicit }
    if (-not [string]::IsNullOrWhiteSpace($env:PIFORGE_DRILL_CREDENTIALS)) { return $env:PIFORGE_DRILL_CREDENTIALS }
    return (Join-Path $HOME ".dsh/.credentials.yaml")
}

# Get-DrillKeyValue returns the provider key. The value is a *return value* only:
# no Write-Host, no Write-Verbose, no error message that includes it.
function Get-DrillKeyValue {
    param([string]$File = "", [string]$Key = "VOLCE_API_KEY")
    $path = Get-DrillKeyPath -Explicit $File
    if (-not (Test-Path $path)) {
        throw "drill key: no credentials file at $path"
    }
    # Parsed by hand rather than with a YAML parser: the file's shape is fixed
    # (a refs: block of NAME: value), and a parser that fails by printing its input
    # is how a key ends up in a log.
    $inRefs = $false
    foreach ($line in Get-Content -LiteralPath $path) {
        if ($line -match '^refs:\s*$') { $inRefs = $true; continue }
        if ($inRefs -and $line -match '^\S') { $inRefs = $false }
        if (-not $inRefs) { continue }
        if ($line -match ('^\s+' + [regex]::Escape($Key) + ':\s*(.+?)\s*$')) {
            $value = $Matches[1].Trim()
            if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
                $value = $value.Substring(1, $value.Length - 2)
            }
            if ([string]::IsNullOrWhiteSpace($value)) {
                throw "drill key: $Key is present but empty in $path"
            }
            return $value
        }
    }
    throw "drill key: $Key is not under refs: in $path"
}

# Get-DrillKeyFingerprint is the only thing about the key that may be printed:
# how long it is and the first bytes of its SHA-256. Two runs can be compared
# without either of them being readable.
function Get-DrillKeyFingerprint {
    param([string]$Value)
    if ([string]::IsNullOrEmpty($Value)) { return "len=0 sha256=none" }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $hash = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($Value))
    }
    finally { $sha.Dispose() }
    $hex = -join ($hash[0..3] | ForEach-Object { $_.ToString("x2") })
    return ("len={0} sha256={1}..." -f $Value.Length, $hex)
}

if ($MyInvocation.InvocationName -ne '.') {
    try {
        $value = Get-DrillKeyValue -File $Path -Key $Name
        $fingerprint = Get-DrillKeyFingerprint -Value $value
        Write-Host "drill key: $fingerprint"
        if ($Check -and $value.Length -lt 16) {
            Write-Host "ERROR: the key is too short to be a provider key" -ForegroundColor Red
            exit 1
        }
        if ($Check) { Write-Host "OK: the key is present and long enough" -ForegroundColor Green }
        exit 0
    }
    catch {
        # The exception message never carries the value (see above), but print the
        # message only — never $_, which would include the stack and any locals.
        Write-Host "ERROR: $($_.Exception.Message)" -ForegroundColor Red
        exit 1
    }
}
