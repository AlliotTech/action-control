[CmdletBinding()]
param(
    [ValidateSet('install', 'update', 'uninstall', 'import-config')][string]$Operation = 'install',
    [string]$Serial = $env:ANDROID_SERIAL,
    [switch]$Reboot,
    [Alias('File')][string]$ConfigFile,
    [string]$Known,
    [switch]$Help
)
$ErrorActionPreference = 'Stop'
$Stage = $null; $Forward = $null; $Complete = $false
if ($Help) {
    Write-Output @'
Action Control — native PowerShell (ADB required)
  .\install.ps1 [-Serial SERIAL] [-Reboot]
  .\install.ps1 -Operation update [-Serial SERIAL] [-Reboot]
  .\install.ps1 -Operation import-config [-ConfigFile CONFIG.json] [-Known KNOWN.json] [-Serial SERIAL]
  .\uninstall.ps1 -Reboot [-Serial SERIAL]
Run from an extracted complete release with payload/ and SHA256SUMS.
Checksums detect corruption, not publisher identity. Use trusted releases.
Uninstall restores recorded WiFi/DNS, not BL, root ADB or Factory Mode.
'@
    exit 0
}
function Invoke-Adb {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)
    $Output = & $script:AdbPath -s $script:Serial @Arguments
    if ($LASTEXITCODE -ne 0) { throw "ADB command failed with exit code $LASTEXITCODE ($($Arguments[0]))" }
    return $Output
}
try {
    if ($Operation -eq 'uninstall' -and !$Reboot) { throw 'Uninstall requires -Reboot; nothing changed.' }
    if ($Operation -ne 'import-config' -and ($ConfigFile -or $Known)) { throw 'Import paths require -Operation import-config.' }
    if ($Operation -eq 'import-config' -and !($ConfigFile -or $Known)) { throw 'Select at least one import file.' }
    foreach ($InputFile in @($ConfigFile, $Known)) { if ($InputFile -and !(Test-Path -LiteralPath $InputFile -PathType Leaf)) { throw "Missing import file: $InputFile" } }
    $AdbPath = (Get-Command adb -CommandType Application).Source
    $Payload = Join-Path $PSScriptRoot 'payload'
    $Bootstrap = Join-Path $Payload 'action-control'
    $Sums = Join-Path $PSScriptRoot 'SHA256SUMS'
    if (!(Test-Path -LiteralPath $Bootstrap -PathType Leaf) -or !(Test-Path -LiteralPath $Sums -PathType Leaf)) { throw 'Run this from an extracted complete release.' }
    foreach ($Line in [IO.File]::ReadAllLines($Sums)) {
        if ($Line -notmatch '^([a-f0-9]{64})  (.+)$') { throw 'Invalid release checksum entry.' }
        $Expected = $Matches[1]; $Relative = $Matches[2]
        if ([IO.Path]::IsPathRooted($Relative) -or ($Relative -split '[/\\]' -contains '..')) { throw 'Invalid checksum path.' }
        $Path = Join-Path $PSScriptRoot $Relative
        $Item = Get-Item -LiteralPath $Path
        if ($Item.PSIsContainer -or ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "Unexpected checksum file: $Relative" }
        if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $Expected) { throw "Release checksum mismatch: $Relative" }
    }
    if (!$Serial) {
        $Devices = & $AdbPath devices
        if ($LASTEXITCODE -ne 0) { throw 'Could not enumerate ADB devices.' }
        $Online = @($Devices | ForEach-Object { if ($_ -match '^(\S+)\s+device(?:\s|$)') { $Matches[1] } })
        if ($Online.Count -ne 1) { throw 'Select exactly one online device with -Serial SERIAL.' }
        $Serial = $Online[0]
    }
    if (((Invoke-Adb -Arguments @('get-state')) -join "`n").Trim() -ne 'device') { throw 'Selected device is not online.' }
    if (((Invoke-Adb -Arguments @('shell', 'id', '-u')) -join "`n").Trim() -ne '0') { throw 'Root ADB is required.' }
    if (((Invoke-Adb -Arguments @('shell', 'uname', '-m')) -join "`n").Trim() -ne 'aarch64') { throw 'Expected ARM64 camera.' }
    $Boot = ((Invoke-Adb -Arguments @('shell', 'cat', '/proc/sys/kernel/random/boot_id')) -join "`n").Trim()
    if ($Boot -notmatch '^[a-f0-9-]{36}$') { throw 'Cannot read camera boot identity.' }
    $Stage = ((Invoke-Adb -Arguments @('shell', 'umask 077; mktemp -d /blackbox/.action-control-stage-XXXXXX')) -join "`n").Trim()
    if ($Stage -notmatch '^/blackbox/\.action-control-stage-[a-zA-Z0-9]+$') { $Stage = $null; throw 'Invalid exclusive staging path.' }
    Invoke-Adb -Arguments @('push', $Bootstrap, "$Stage/bootstrap")
    Invoke-Adb -Arguments @('shell', "chmod 700 '$Stage/bootstrap'")
    $Remote = @($Operation)
    if ($Operation -in @('install', 'update')) {
        Invoke-Adb -Arguments @('push', $Payload, "$Stage/payload")
        $Remote += @('--source', "$Stage/payload")
    }
    if ($Operation -eq 'import-config') {
        foreach ($Entry in @(@('file', $ConfigFile), @('known', $Known))) {
            if ($Entry[1]) {
                Invoke-Adb -Arguments @('push', [IO.Path]::GetFullPath($Entry[1]), "$Stage/import-$($Entry[0])")
                $Remote += @("--$($Entry[0])", "$Stage/import-$($Entry[0])")
            }
        }
    }
    if ($Reboot) { $Remote += '--reboot' }
    # Remote arguments are fixed strings or restricted, exclusive staging paths.
    Invoke-Adb -Arguments (@('shell', "$Stage/bootstrap") + $Remote)
    if ($Reboot) {
        Invoke-Adb -Arguments @('reboot')
        $Deadline = [DateTime]::UtcNow.AddSeconds(150); $Changed = $false
        while ([DateTime]::UtcNow -lt $Deadline) {
            try {
                $State = & $AdbPath -s $Serial get-state 2>$null
                if ($LASTEXITCODE -eq 0 -and ($State -join '').Trim() -eq 'device') {
                    $Next = & $AdbPath -s $Serial shell cat /proc/sys/kernel/random/boot_id 2>$null
                    $Next = ($Next -join '').Trim()
                    if ($LASTEXITCODE -eq 0 -and $Next -match '^[a-f0-9-]{36}$' -and $Next -ne $Boot) { $Changed = $true; break }
                }
            } catch { # ADB is expected to disconnect during a reboot.
            }
            Start-Sleep -Seconds 1
        }
        if (!$Changed) { throw 'Reboot not verified within 150s; native runtime restoration is unconfirmed.' }
    }
    if ($Operation -eq 'uninstall') {
        Invoke-Adb -Arguments @('shell', "$Stage/bootstrap", 'verify', '--removed')
    } else {
        Invoke-Adb -Arguments @('shell', "$Stage/bootstrap", 'verify')
        $Forward = ((Invoke-Adb -Arguments @('forward', '--no-rebind', 'tcp:0', 'tcp:8080')) -join '').Trim()
        if ($Forward -notmatch '^\d+$') { $Forward = $null; throw 'ADB did not return a local forwarded port.' }
        Write-Output "Console: http://127.0.0.1:$Forward"
        Write-Output "This ADB forward remains available. Remove it with: adb -s $Serial forward --remove tcp:$Forward"
    }
    Invoke-Adb -Arguments @('shell', "rm -rf '$Stage'")
    $Stage = $null; $Complete = $true
    Write-Output "$Operation completed and verified for device $Serial."
} catch {
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
} finally {
    if (!$Complete) {
        if ($Forward) { try { Invoke-Adb -Arguments @('forward', '--remove', "tcp:$Forward") } catch { [Console]::Error.WriteLine('Could not remove the temporary ADB forward.') } }
        if ($Stage) { [Console]::Error.WriteLine("Operation incomplete. Recovery/staging retained at $Stage; do not force-delete installation backups.") }
    }
}
