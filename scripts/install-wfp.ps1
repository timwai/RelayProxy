#Requires -RunAsAdministrator
#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$DriverPath = ".",
    [switch]$ForceReinstall
)

$ErrorActionPreference = "Stop"
$DriverPath = [System.IO.Path]::GetFullPath($DriverPath)
$inf = Join-Path $DriverPath "RelayProxyWfp.inf"
$sys = Join-Path $DriverPath "RelayProxyWfp.sys"

if (-not (Test-Path $inf)) { throw "Missing $inf" }
if (-not (Test-Path $sys)) { throw "Missing $sys" }

$bfe = Get-Service BFE -ErrorAction Stop
if ($bfe.Status -ne 'Running') {
    Start-Service BFE
}

if ($ForceReinstall) {
    & (Join-Path $PSScriptRoot "uninstall-wfp.ps1") -KeepAgent
}

Write-Host "[WFP] Installing $inf" -ForegroundColor Cyan
& pnputil.exe /add-driver $inf /install
if ($LASTEXITCODE -ne 0) { throw "pnputil failed with exit code $LASTEXITCODE" }

& sc.exe start RelayProxyWfp | Out-Host
$startCode = $LASTEXITCODE
if ($startCode -ne 0) {
    $service = Get-Service RelayProxyWfp -ErrorAction SilentlyContinue
    if (-not $service -or $service.Status -ne 'Running') {
        throw "RelayProxyWfp did not start (sc.exe exit $startCode)"
    }
}

$service = Get-Service RelayProxyWfp -ErrorAction Stop
Write-Host "[WFP] RelayProxyWfp status: $($service.Status)" -ForegroundColor Green
Write-Host "[WFP] Driver installed. Start relay-agent as Administrator with network.mode=divert."
