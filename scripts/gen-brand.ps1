#Requires -Version 5.1
<#
.SYNOPSIS
  Generate brand icons and Windows .syso resources for relay-server / relay-agent.
#>
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $Root

Write-Host "[1/3] Generate PNG/ICO assets"
go run scripts/genicon.go $Root
if ($LASTEXITCODE -ne 0) { throw "genicon failed" }

Write-Host "[2/3] Ensure goversioninfo"
$gvi = Get-Command goversioninfo -ErrorAction SilentlyContinue
if (-not $gvi) {
    go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
    $env:Path = "$(go env GOPATH)\bin;$env:Path"
}

Write-Host "[3/3] Embed Windows icons into architecture-specific resources"
function New-WindowsResources {
    param([string]$PackageDir)

    Push-Location $PackageDir
    try {
        Remove-Item resource_windows.syso -Force -ErrorAction SilentlyContinue
        goversioninfo -64 -arm=false -o resource_windows_amd64.syso versioninfo.json
        if ($LASTEXITCODE -ne 0) { throw "goversioninfo amd64 failed: $PackageDir" }
        goversioninfo -64 -arm=true -o resource_windows_arm64.syso versioninfo.json
        if ($LASTEXITCODE -ne 0) { throw "goversioninfo arm64 failed: $PackageDir" }
    } finally { Pop-Location }
}
New-WindowsResources (Join-Path $Root "cmd\relay-server")
New-WindowsResources (Join-Path $Root "cmd\relay-agent")

Write-Host "Brand assets ready." -ForegroundColor Green
Get-ChildItem assets\brand, server\web\favicon.ico, server\web\img, cmd\relay-server\resource_windows_*.syso, cmd\relay-agent\resource_windows_*.syso |
    Format-Table Name, Length -AutoSize
