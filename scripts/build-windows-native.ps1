param(
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Release",
    [ValidateSet("win-x64", "win-arm64")]
    [string]$Runtime = "win-x64",
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$Project = Join-Path $Root "windows\RelayProxy.Native\RelayProxy.Native.csproj"
$Platform = if ($Runtime -eq "win-arm64") { "ARM64" } else { "x64" }
$GoArch = if ($Runtime -eq "win-arm64") { "arm64" } else { "amd64" }
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $Root "dist\windows-native-$($Runtime.Substring(4))"
}

New-Item -ItemType Directory -Path $OutDir -Force | Out-Null

Write-Host "[native-ui] Restore $Runtime"
dotnet restore $Project -r $Runtime

Write-Host "[native-ui] Publish WinUI 3 client ($Platform)"
dotnet publish $Project -c $Configuration -r $Runtime -p:Platform=$Platform -p:WindowsPackageType=None -p:WindowsAppSDKSelfContained=true --self-contained true -o $OutDir --no-restore
if ($LASTEXITCODE -ne 0) { throw "WinUI publish failed" }

Write-Host "[native-ui] Build Go Agent companion ($GoArch)"
$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
try {
    $env:GOOS = "windows"
    $env:GOARCH = $GoArch
    go build -trimpath -ldflags "-s -w" -o (Join-Path $OutDir "relay-agent.exe") .\cmd\relay-agent
    if ($LASTEXITCODE -ne 0) { throw "Go Agent build failed" }
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
}

Write-Host "[native-ui] Output: $OutDir"
