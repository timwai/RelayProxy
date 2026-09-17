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

Write-Host "[3/3] Embed Windows icons into resource_windows.syso"
Push-Location (Join-Path $Root "cmd\relay-server")
try {
    goversioninfo -64 -o resource_windows.syso versioninfo.json
    if ($LASTEXITCODE -ne 0) { throw "goversioninfo server failed" }
} finally { Pop-Location }

Push-Location (Join-Path $Root "cmd\relay-agent")
try {
    goversioninfo -64 -o resource_windows.syso versioninfo.json
    if ($LASTEXITCODE -ne 0) { throw "goversioninfo agent failed" }
} finally { Pop-Location }

Write-Host "Brand assets ready." -ForegroundColor Green
Get-ChildItem assets\brand, server\web\favicon.ico, server\web\img, cmd\relay-server\resource_windows.syso, cmd\relay-agent\resource_windows.syso |
    Format-Table Name, Length -AutoSize
