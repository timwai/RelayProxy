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
$cat = Join-Path $DriverPath "RelayProxyWfp.cat"

if (-not (Test-Path $inf)) { throw "Missing $inf" }
if (-not (Test-Path $sys)) { throw "Missing $sys" }
if (-not (Test-Path $cat)) { throw "Missing $cat. Build an installable package without -SkipCatalog." }

$bfe = Get-Service BFE -ErrorAction Stop
if ($bfe.Status -ne 'Running') {
    Start-Service BFE
}

if ($ForceReinstall) {
    & (Join-Path $PSScriptRoot "uninstall-wfp.ps1")
}

Write-Host "[WFP] Installing $inf" -ForegroundColor Cyan
& pnputil.exe /add-driver $inf /install
if ($LASTEXITCODE -ne 0) { throw "pnputil failed with exit code $LASTEXITCODE" }

$service = Get-Service RelayProxyWfp -ErrorAction SilentlyContinue
if (-not $service) {
    Write-Host "[WFP] Applying primitive-driver DefaultInstall section" -ForegroundColor Cyan
    & rundll32.exe setupapi.dll,InstallHinfSection DefaultInstall 132 $inf
    Start-Sleep -Milliseconds 500
    $service = Get-Service RelayProxyWfp -ErrorAction SilentlyContinue
}
if (-not $service) {
    throw "RelayProxyWfp service was not created by the driver package."
}

if ($service.Status -ne 'Running') {
    & sc.exe start RelayProxyWfp | Out-Host
    $startCode = $LASTEXITCODE
    if ($startCode -ne 0) {
        throw "RelayProxyWfp did not start (sc.exe exit $startCode). Check driver signing policy and Event Viewer."
    }
    $service = Get-Service RelayProxyWfp -ErrorAction Stop
    $service.WaitForStatus('Running', [TimeSpan]::FromSeconds(15))
}

$service = Get-Service RelayProxyWfp -ErrorAction Stop
if ($service.Status -ne 'Running') {
    throw "RelayProxyWfp failed to reach Running state."
}
Write-Host "[WFP] RelayProxyWfp status: $($service.Status)" -ForegroundColor Green
Write-Host "[WFP] Driver installed. Start relay-agent as Administrator with network.mode=divert."
