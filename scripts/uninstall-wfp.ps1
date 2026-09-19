#Requires -RunAsAdministrator
#Requires -Version 5.1
[CmdletBinding()]
param(
    [switch]$KeepAgent
)

$ErrorActionPreference = "Stop"

if (-not $KeepAgent) {
    Get-Process -Name "relay-agent", "relay-agent-gui" -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
}

$service = Get-Service RelayProxyWfp -ErrorAction SilentlyContinue
if ($service) {
    if ($service.Status -ne 'Stopped') {
        & sc.exe stop RelayProxyWfp | Out-Host
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(15))
    }
}

$drivers = @(Get-WindowsDriver -Online -All -ErrorAction SilentlyContinue |
    Where-Object {
        $_.OriginalFileName -and
        ([System.IO.Path]::GetFileName($_.OriginalFileName) -ieq 'RelayProxyWfp.inf')
    })

foreach ($driver in $drivers) {
    $published = $driver.Driver
    if ($published) {
        Write-Host "[WFP] Removing Driver Store package $published" -ForegroundColor Cyan
        & pnputil.exe /delete-driver $published /uninstall /force
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to remove $published (exit $LASTEXITCODE)"
        }
    }
}

if (Get-Service RelayProxyWfp -ErrorAction SilentlyContinue) {
    & sc.exe delete RelayProxyWfp | Out-Host
}

Write-Host "[WFP] RelayProxyWfp removed." -ForegroundColor Green
