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
$NativeExeName = if ($Runtime -eq "win-arm64") { "RelayProxy-agent-windows-arm64.exe" } else { "RelayProxy-agent-windows-amd64.exe" }
$NativeHostName = "RelayProxy.NativeHost.exe"
$PackScript = Join-Path $Root "scripts\pack-windows-launcher.ps1"
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $Root "dist\windows-native-$($Runtime.Substring(4))"
}

$OutDir = [System.IO.Path]::GetFullPath($OutDir)
$StageDir = Join-Path ([System.IO.Path]::GetTempPath()) ("relayproxy-native-" + [Guid]::NewGuid().ToString("N"))
$CorePath = Join-Path $StageDir "relay-agent.exe"
$HostDir = Join-Path $StageDir "host"
$LauncherStub = Join-Path $StageDir "relayproxy-launcher.exe"

New-Item -ItemType Directory -Path $OutDir -Force | Out-Null
New-Item -ItemType Directory -Path $StageDir -Force | Out-Null
New-Item -ItemType Directory -Path $HostDir -Force | Out-Null

$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED
try {
    Write-Host "[native-ui] Build embedded Go Agent core ($GoArch)"
    $env:GOOS = "windows"
    $env:GOARCH = $GoArch
    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags "-s -w" -o $CorePath .\cmd\relay-agent
    if ($LASTEXITCODE -ne 0) { throw "Go Agent build failed" }
    if (-not (Test-Path $CorePath)) { throw "Embedded Go Agent core missing: $CorePath" }

    Write-Host "[native-ui] Restore $Runtime"
    dotnet restore $Project -r $Runtime -p:PublishTrimmed=true
    if ($LASTEXITCODE -ne 0) { throw "dotnet restore failed" }

    if (Test-Path $OutDir) {
        Get-ChildItem -LiteralPath $OutDir -Force | Remove-Item -Recurse -Force
    }

    Write-Host "[native-ui] Publish fixed-name WinUI 3 host payload ($Platform)"
    dotnet publish $Project `
        -c $Configuration `
        -r $Runtime `
        -p:Platform=$Platform `
        -p:WindowsPackageType=None `
        -p:WindowsAppSDKSelfContained=true `
        -p:SelfContained=true `
        -p:PublishSingleFile=true `
        -p:EnableCompressionInSingleFile=true `
        -p:PublishReadyToRun=false `
        -p:PublishTrimmed=true `
        -p:TrimMode=partial `
        -p:SuppressTrimAnalysisWarnings=false `
        -p:EnableMsixTooling=true `
        -p:IncludeAllContentForSelfExtract=true `
        -p:IncludeNativeLibrariesForSelfExtract=true `
        -p:DebugType=None `
        -p:DebugSymbols=false `
        -p:RelayAgentCorePath="$CorePath" `
        --self-contained true `
        --no-restore `
        -o $HostDir
    if ($LASTEXITCODE -ne 0) { throw "WinUI single-file publish failed" }

    $NativeHost = Join-Path $HostDir $NativeHostName
    if (-not (Test-Path $NativeHost)) { throw "WinUI host payload missing: $NativeHost" }
    $Unexpected = @(Get-ChildItem -LiteralPath $HostDir -Force | Where-Object { $_.Name -ne $NativeHostName })
    if ($Unexpected.Count -gt 0) {
        $Names = ($Unexpected | ForEach-Object { $_.Name }) -join ", "
        throw "WinUI host publish produced unexpected sidecar files: $Names"
    }

    Write-Host "[native-ui] Build rename-safe launcher ($GoArch)"
    go build -trimpath -ldflags "-s -w -H=windowsgui" -o $LauncherStub .\cmd\relay-agent-win-launcher
    if ($LASTEXITCODE -ne 0) { throw "rename-safe launcher build failed" }

    $NativeExe = Join-Path $OutDir $NativeExeName
    & powershell -NoProfile -ExecutionPolicy Bypass -File $PackScript -Launcher $LauncherStub -Payload $NativeHost -Output $NativeExe
    if ($LASTEXITCODE -ne 0) { throw "launcher payload packing failed" }
    if (-not (Test-Path $NativeExe)) { throw "Final rename-safe executable missing: $NativeExe" }

    $Size = (Get-Item $NativeExe).Length
    $HostSize = (Get-Item $NativeHost).Length
    $CoreSize = (Get-Item $CorePath).Length
    $LauncherOverhead = [Math]::Max(0, $Size - $HostSize)
    Write-Host "[native-ui] Rename-safe EXE: $NativeExe ($([math]::Round($Size / 1MB, 2)) MB)" -ForegroundColor Green
    Write-Host "[native-ui] Trimmed WinUI host payload: $([math]::Round($HostSize / 1MB, 2)) MB"
    Write-Host "[native-ui] Embedded Go Core: $([math]::Round($CoreSize / 1MB, 2)) MB"
    Write-Host "[native-ui] Launcher/footer overhead: ~$([math]::Round($LauncherOverhead / 1MB, 2)) MB"
    Write-Host "[native-ui] Final EXE may be renamed freely; fixed-name XAML host is extracted to the user cache." -ForegroundColor DarkGray
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgo
    Remove-Item -LiteralPath $StageDir -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host "[native-ui] Output: $OutDir"
