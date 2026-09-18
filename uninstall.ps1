[CmdletBinding()]
param([string]$Serial = $env:ANDROID_SERIAL, [switch]$Reboot, [switch]$Help)
$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'install.ps1') -Operation uninstall @PSBoundParameters
exit $LASTEXITCODE
