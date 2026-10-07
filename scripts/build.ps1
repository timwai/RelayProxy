#Requires -Version 5.1
<#
.SYNOPSIS
  RelayProxy 跨平台发布编译脚本

.DESCRIPTION
  产出：
    - Linux 服务端 / Agent amd64 / arm64
    - macOS Server / Agent amd64 / arm64（含 RelayProxy.app Agent 包装）
    - Windows 客户端 amd64 / arm64（WinUI 3 relay-agent-gui.exe + relay-agent.exe Core + legacy Wails fallback）
    - Windows 服务端 amd64 / arm64（relay-server.exe，Console 子系统，含 Admin UI）
    - 每个平台目录的完整 ZIP 分发包

  说明：Windows 默认桌面客户端使用 WinUI 3 + Windows App SDK，不再依赖 WebView2。
  relay-agent-gui.exe 是原生 UI，启动同目录 relay-agent.exe --no-gui 作为 Go 网络核心；
  relay-agent-wails.exe 暂时保留旧 Wails/WebView2 GUI 作为迁移期回退。
  Windows arm64 产物可运行 Agent / Server，但系统透明代理目前仍只支持 amd64。
  管理界面通过 Linux 服务端的 Admin HTTPS 控制台访问，或直接使用桌面窗口。

.EXAMPLE
  .\scripts\build.ps1
  .\scripts\build.ps1 -OutDir D:\release\relayproxy
#>
param(
    [string]$OutDir = "",
    [string]$Version = "1.0.0"
)

$ErrorActionPreference = "Stop"

$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
if (-not $OutDir) {
    $OutDir = Join-Path $Root "dist"
}

$ldflags = "-s -w -X main.Version=$Version"
$stamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"

function Reset-GoHostEnvironment {
    # Cross-compilation leaves GOOS/GOARCH pointing at the last target. Any
    # subsequent `go run` helper must be built for the machine running this
    # script, otherwise an ARM64 helper can be produced and fail on x64 Windows.
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:GOARM -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
}

function Assert-WindowsNativeBuildPrerequisites {
    $dotnet = Get-Command dotnet -ErrorAction SilentlyContinue
    if (-not $dotnet) {
        throw @"
Windows Native UI requires the .NET 10 SDK, but 'dotnet' was not found.
Install it from an elevated PowerShell:
  winget install --id Microsoft.DotNet.SDK.10 --exact --source winget
Then close this terminal, open a new PowerShell window, and run:
  dotnet --version
"@
    }

    $sdks = @(& dotnet --list-sdks)
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to query installed .NET SDKs with 'dotnet --list-sdks'."
    }

    $net10 = @($sdks | Where-Object { $_ -match '^10\.' })
    if ($net10.Count -eq 0) {
        $installed = if ($sdks.Count -gt 0) { $sdks -join ", " } else { "(none)" }
        throw @"
Windows Native UI targets net10.0 and requires the .NET 10 SDK.
Installed SDKs: $installed
Install it from an elevated PowerShell:
  winget install --id Microsoft.DotNet.SDK.10 --exact --source winget
"@
    }

    Write-Host "[prep] .NET 10 SDK detected: $($net10[-1])" -ForegroundColor Green
}

Write-Host "=================================================="
Write-Host " RelayProxy Build  v$Version"
Write-Host " Root:   $Root"
Write-Host " OutDir: $OutDir"
Write-Host " Time:   $stamp"
Write-Host "=================================================="

Push-Location $Root
try {
    # The caller may already have GOOS/GOARCH set from a previous cross-build.
    # Reset before invoking any host-side Go helper.
    Reset-GoHostEnvironment
    Assert-WindowsNativeBuildPrerequisites

    Write-Host "[prep] Generate brand icons + Windows resources"
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Root "scripts\gen-brand.ps1")
    if ($LASTEXITCODE -ne 0) { throw "gen-brand failed" }

    $OutDir = [System.IO.Path]::GetFullPath($OutDir)
    if ($OutDir.TrimEnd('\', '/') -eq $Root.Path.TrimEnd('\', '/') -or
        $OutDir.TrimEnd('\', '/') -eq [System.IO.Path]::GetPathRoot($OutDir).TrimEnd('\', '/')) {
        throw "OutDir must be a build output directory, not the repository or a drive root"
    }
    if (Test-Path -LiteralPath $OutDir) {
        $resolvedOutput = (Resolve-Path -LiteralPath $OutDir).Path
        if ($resolvedOutput -ne $OutDir) { throw "OutDir does not resolve to the intended output path" }
        Remove-Item -LiteralPath $resolvedOutput -Recurse -Force
    }
    New-Item -ItemType Directory -Path $OutDir | Out-Null

    Write-Host "[prep] Verify and embed the official WinDivert runtime"
    Reset-GoHostEnvironment
    & go run ./scripts/fetch-windivert.go `
        -out (Join-Path $OutDir "windows-amd64/windivert") `
        -embed-archive (Join-Path $Root "agent/divert/windivert/WinDivert-2.2.2-A.zip")
    if ($LASTEXITCODE -ne 0) { throw "WinDivert embedding preparation failed" }

    # .syso is Windows-only; hide it while cross-compiling Linux binaries
    $sysoFiles = @(
        (Join-Path $Root "cmd\relay-server\resource_windows.syso"),
        (Join-Path $Root "cmd\relay-agent\resource_windows.syso"),
        (Join-Path $Root "cmd\relay-server\resource_windows_amd64.syso"),
        (Join-Path $Root "cmd\relay-server\resource_windows_arm64.syso"),
        (Join-Path $Root "cmd\relay-agent\resource_windows_amd64.syso"),
        (Join-Path $Root "cmd\relay-agent\resource_windows_arm64.syso")
    )
    function Hide-Syso {
        foreach ($f in $sysoFiles) {
            if (Test-Path $f) { Rename-Item $f ($f + ".bak") -Force }
        }
    }
    function Show-Syso {
        foreach ($f in $sysoFiles) {
            $bak = $f + ".bak"
            if (Test-Path $bak) { Rename-Item $bak $f -Force }
        }
    }

    function Publish-WindowsNativeUI {
        param(
            [ValidateSet("win-x64", "win-arm64")]
            [string]$Runtime,
            [ValidateSet("x64", "ARM64")]
            [string]$Platform,
            [ValidateSet("amd64", "arm64")]
            [string]$GoArch,
            [string]$OutputDir
        )

        $project = Join-Path $Root "windows\RelayProxy.Native\RelayProxy.Native.csproj"
        $stageDir = Join-Path ([System.IO.Path]::GetTempPath()) ("relayproxy-native-" + [Guid]::NewGuid().ToString("N"))
        $publishDir = Join-Path $stageDir "publish"
        $corePath = Join-Path $stageDir "relay-agent.exe"
        New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
        New-Item -ItemType Directory -Path $publishDir -Force | Out-Null

        Write-Host ""
        Write-Host "[BUILD] Windows Native single EXE $Runtime -> $OutputDir" -ForegroundColor Cyan

        $savedGoos = $env:GOOS
        $savedGoarch = $env:GOARCH
        $savedCgo = $env:CGO_ENABLED
        try {
            $env:GOOS = "windows"
            $env:GOARCH = $GoArch
            $env:CGO_ENABLED = "0"
            & go build -trimpath -ldflags $ldflags -o $corePath .\cmd\relay-agent
            if ($LASTEXITCODE -ne 0) { throw "embedded Go Agent build failed: $GoArch" }

            Reset-GoHostEnvironment
            & dotnet restore $project -r $Runtime -p:PublishTrimmed=true
            if ($LASTEXITCODE -ne 0) { throw "dotnet restore failed: $Runtime" }

            & dotnet publish $project `
                -c Release `
                -r $Runtime `
                -p:Platform=$Platform `
                -p:Version=$Version `
                -p:FileVersion=$Version `
                -p:InformationalVersion=$Version `
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
                -p:RelayAgentCorePath="$corePath" `
                --self-contained true `
                --no-restore `
                -o $publishDir
            if ($LASTEXITCODE -ne 0) { throw "dotnet single-file publish failed: $Runtime" }

            $nativeExe = Join-Path $publishDir "relay-agent-gui.exe"
            if (-not (Test-Path $nativeExe)) { throw "Native Windows single EXE not found: $nativeExe" }
            $unexpected = @(Get-ChildItem -LiteralPath $publishDir -Force | Where-Object { $_.Name -ne "relay-agent-gui.exe" })
            if ($unexpected.Count -gt 0) {
                $names = ($unexpected | ForEach-Object { $_.Name }) -join ", "
                throw "Single-file publish produced unexpected sidecar files: $names"
            }

            Copy-Item $nativeExe (Join-Path $OutputDir "relay-agent-gui.exe") -Force

            $nativeSize = (Get-Item $nativeExe).Length
            $coreSize = (Get-Item $corePath).Length
            $runtimePayloadSize = [Math]::Max(0, $nativeSize - $coreSize)
            Write-Host "  SIZE  native single EXE: $([math]::Round($nativeSize / 1MB, 2)) MB" -ForegroundColor Green
            Write-Host "  SIZE  embedded Go Core: $([math]::Round($coreSize / 1MB, 2)) MB"
            Write-Host "  SIZE  UI + .NET + Windows App SDK payload: ~$([math]::Round($runtimePayloadSize / 1MB, 2)) MB"
            Write-Host "  MODE  single-file compression + partial trimming enabled; ReadyToRun disabled" -ForegroundColor DarkGray
        }
        finally {
            $env:GOOS = $savedGoos
            $env:GOARCH = $savedGoarch
            $env:CGO_ENABLED = $savedCgo
            Remove-Item -LiteralPath $stageDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    function Invoke-GoBuild {
        param(
            [string]$GOOS,
            [string]$GOARCH,
            [string]$Package,
            [string]$Output,
            [string]$ExtraLdFlags = "",
            [string]$BuildTags = ""
        )
        $dir = Split-Path -Parent $Output
        if (-not (Test-Path $dir)) {
            New-Item -ItemType Directory -Path $dir -Force | Out-Null
        }

        Write-Host ""
        Write-Host "[BUILD] $GOOS/$GOARCH  $Package -> $Output" -ForegroundColor Cyan

        $env:CGO_ENABLED = "0"
        $env:GOOS = $GOOS
        $env:GOARCH = $GOARCH
        $env:GOARM = $null

        $buildFlags = $ldflags
        if ($ExtraLdFlags) { $buildFlags = "$ldflags $ExtraLdFlags" }

        $tagArgs = @()
        if ($BuildTags) { $tagArgs = @("-tags", $BuildTags) }
        & go build -trimpath @tagArgs -ldflags $buildFlags -o $Output $Package
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed: $GOOS/$GOARCH $Package"
        }

        $size = (Get-Item $Output).Length
        Write-Host "  OK  $([math]::Round($size/1MB, 2)) MB" -ForegroundColor Green
    }

    function Package-MacOSApp {
        param([string]$Arch)

        $dir = Join-Path $OutDir "darwin-$Arch"
        $app = Join-Path $dir "RelayProxy.app"
        $contents = Join-Path $app "Contents"
        $macOSDir = Join-Path $contents "MacOS"
        $resources = Join-Path $contents "Resources"

        Write-Host ""
        Write-Host "[PACKAGE] darwin/$Arch  RelayProxy.app" -ForegroundColor Cyan

        New-Item -ItemType Directory -Path $macOSDir -Force | Out-Null
        New-Item -ItemType Directory -Path $resources -Force | Out-Null
        Copy-Item (Join-Path $dir "relay-agent") (Join-Path $macOSDir "RelayProxy") -Force
        Copy-Item (Join-Path $Root "assets\brand\app-icon-rounded.png") (Join-Path $resources "AppIcon.png") -Force
        Copy-Item (Join-Path $Root "assets\brand\RelayProxy.icns") (Join-Path $resources "RelayProxy.icns") -Force

        $plist = @"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
  <key>CFBundleDisplayName</key><string>RelayProxy</string>
  <key>CFBundleExecutable</key><string>RelayProxy</string>
  <key>CFBundleIdentifier</key><string>com.relayproxy.agent</string>
  <key>CFBundleIconFile</key><string>RelayProxy.icns</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>RelayProxy</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$Version</string>
  <key>CFBundleVersion</key><string>$Version</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
"@
        $plist | Set-Content -Path (Join-Path $contents "Info.plist") -Encoding utf8
    }

    # --- Linux server/agent and macOS agent ---
    Hide-Syso
    try {
        Invoke-GoBuild -GOOS "linux" -GOARCH "amd64" `
            -Package "./cmd/relay-server" `
            -Output (Join-Path $OutDir "linux-amd64/relay-server")

        Invoke-GoBuild -GOOS "linux" -GOARCH "arm64" `
            -Package "./cmd/relay-server" `
            -Output (Join-Path $OutDir "linux-arm64/relay-server")

        Invoke-GoBuild -GOOS "linux" -GOARCH "amd64" `
            -Package "./cmd/relay-agent" `
            -Output (Join-Path $OutDir "linux-amd64/relay-agent")

        Invoke-GoBuild -GOOS "linux" -GOARCH "arm64" `
            -Package "./cmd/relay-agent" `
            -Output (Join-Path $OutDir "linux-arm64/relay-agent")

        Invoke-GoBuild -GOOS "darwin" -GOARCH "amd64" `
            -Package "./cmd/relay-agent" `
            -Output (Join-Path $OutDir "darwin-amd64/relay-agent")

        Invoke-GoBuild -GOOS "darwin" -GOARCH "amd64" `
            -Package "./cmd/relay-server" `
            -Output (Join-Path $OutDir "darwin-amd64/relay-server")

        Invoke-GoBuild -GOOS "darwin" -GOARCH "arm64" `
            -Package "./cmd/relay-agent" `
            -Output (Join-Path $OutDir "darwin-arm64/relay-agent")

        Invoke-GoBuild -GOOS "darwin" -GOARCH "arm64" `
            -Package "./cmd/relay-server" `
            -Output (Join-Path $OutDir "darwin-arm64/relay-server")
    } finally {
        Show-Syso
    }

    Package-MacOSApp -Arch "amd64"
    Package-MacOSApp -Arch "arm64"

    # --- Windows client ------------------------------------------------------
    # relay-agent-gui.exe is now the WinUI 3 native shell. It launches the
    # sibling relay-agent.exe in --no-gui mode and talks to its loopback
    # management API. Keep the old Wails binary under a legacy name until the
    # migration has been validated in production.
    Invoke-GoBuild -GOOS "windows" -GOARCH "amd64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-amd64/relay-agent.exe")

    Invoke-GoBuild -GOOS "windows" -GOARCH "amd64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-amd64/relay-agent-wails.exe") `
        -ExtraLdFlags "-H=windowsgui" `
        -BuildTags "wailslegacy"

    Publish-WindowsNativeUI -Runtime "win-x64" -Platform "x64" -GoArch "amd64" `
        -OutputDir (Join-Path $OutDir "windows-amd64")

    Invoke-GoBuild -GOOS "windows" -GOARCH "amd64" `
        -Package "./cmd/relay-server" `
        -Output (Join-Path $OutDir "windows-amd64/relay-server.exe")

    Invoke-GoBuild -GOOS "windows" -GOARCH "arm64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-arm64/relay-agent.exe")

    Invoke-GoBuild -GOOS "windows" -GOARCH "arm64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-arm64/relay-agent-wails.exe") `
        -ExtraLdFlags "-H=windowsgui" `
        -BuildTags "wailslegacy"

    Publish-WindowsNativeUI -Runtime "win-arm64" -Platform "ARM64" -GoArch "arm64" `
        -OutputDir (Join-Path $OutDir "windows-arm64")

    Invoke-GoBuild -GOOS "windows" -GOARCH "arm64" `
        -Package "./cmd/relay-server" `
        -Output (Join-Path $OutDir "windows-arm64/relay-server.exe")

    # Primary portable Windows Agent downloads: one EXE per architecture.
    Copy-Item (Join-Path $OutDir "windows-amd64/relay-agent-gui.exe") (Join-Path $OutDir "RelayProxy-agent-windows-amd64.exe") -Force
    Copy-Item (Join-Path $OutDir "windows-arm64/relay-agent-gui.exe") (Join-Path $OutDir "RelayProxy-agent-windows-arm64.exe") -Force
    # Copy brand icon into Windows package for shortcuts / installers
    foreach ($t in @("windows-amd64", "windows-arm64")) {
        $brandOut = Join-Path $OutDir "$t/brand"
        New-Item -ItemType Directory -Path $brandOut -Force | Out-Null
        Copy-Item (Join-Path $Root "assets\brand\icon.ico") $brandOut -Force
        Copy-Item (Join-Path $Root "assets\brand\logo.png") $brandOut -Force
    }
    foreach ($t in @("linux-amd64", "linux-arm64")) {
        $lb = Join-Path $OutDir "$t/brand"
        New-Item -ItemType Directory -Path $lb -Force | Out-Null
        Copy-Item (Join-Path $Root "assets\brand\icon-256.png") (Join-Path $lb "icon.png") -Force
        Copy-Item (Join-Path $Root "assets\brand\logo.png") $lb -Force
    }
    foreach ($t in @("darwin-amd64", "darwin-arm64")) {
        $mb = Join-Path $OutDir "$t/brand"
        New-Item -ItemType Directory -Path $mb -Force | Out-Null
        Copy-Item (Join-Path $Root "assets\brand\icon-256.png") (Join-Path $mb "icon.png") -Force
        Copy-Item (Join-Path $Root "assets\brand\logo.png") $mb -Force
    }

    # --- Package configs ---
    foreach ($target in @("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64")) {
        $cfgDir = Join-Path $OutDir "$target/configs"
        New-Item -ItemType Directory -Path $cfgDir -Force | Out-Null
        Copy-Item (Join-Path $Root "configs/relay-server.yaml") $cfgDir -Force
        Copy-Item (Join-Path $Root "configs/relay-agent.yaml") $cfgDir -Force
    }

    Copy-Item (Join-Path $Root "docs/windows-transparent-proxy.md") (Join-Path $OutDir "windows-amd64/README.md") -Force
    Copy-Item (Join-Path $Root "docs/windows-transparent-proxy.md") (Join-Path $OutDir "windows-arm64/README.md") -Force
    Copy-Item (Join-Path $Root "docs/linux-transparent-proxy.md") (Join-Path $OutDir "linux-amd64/README.md") -Force
    Copy-Item (Join-Path $Root "docs/linux-transparent-proxy.md") (Join-Path $OutDir "linux-arm64/README.md") -Force
    Copy-Item (Join-Path $Root "agent/divert/macos/README.md") (Join-Path $OutDir "darwin-amd64/README.md") -Force
    Copy-Item (Join-Path $Root "agent/divert/macos/README.md") (Join-Path $OutDir "darwin-arm64/README.md") -Force
    Write-Host "[prep] Package Windows agent with verified WinDivert runtime"
    Reset-GoHostEnvironment
    & go run ./scripts/fetch-windivert.go `
        -out (Join-Path $OutDir "windows-amd64/windivert") `
        -agent-zip (Join-Path $OutDir "RelayProxy-agent-windows-amd64.zip")
    if ($LASTEXITCODE -ne 0) { throw "Windows agent packaging failed" }

    # --- Full platform distribution archives ---
    function New-TargetArchive {
        param([string]$Target)

        $source = Join-Path $OutDir $Target
        $archive = Join-Path $OutDir ("RelayProxy-" + $Target + ".zip")
        if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
        Write-Host "[PACKAGE] $Target -> $archive" -ForegroundColor Cyan
        Compress-Archive -Path (Join-Path $source "*") -DestinationPath $archive -CompressionLevel Optimal
    }

    foreach ($target in @("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64")) {
        New-TargetArchive -Target $target
    }


    # --- Checksums ---
    Write-Host ""
    Write-Host "[SHA256]" -ForegroundColor Cyan
    $checksumFile = Join-Path $OutDir "SHA256SUMS.txt"
    $lines = @()
    Get-ChildItem -Path $OutDir -Recurse -File |
        Where-Object { $_.Name -match '^(relay-server|relay-agent(-(gui|wails))?)(\.exe)?$|^WinDivert(64)?\.(dll|sys)$|^RelayProxy-.*\.(zip|exe)$' } |
        ForEach-Object {
            $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
            $rel = $_.FullName.Substring($OutDir.Length).TrimStart('\', '/')
            $rel = $rel -replace '\\', '/'
            $lines += "$hash  $rel"
            Write-Host "  $hash  $rel"
        }
    $lines | Set-Content -Path $checksumFile -Encoding utf8

    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

    Write-Host ""
    Write-Host "=================================================="
    Write-Host " Build complete -> $OutDir" -ForegroundColor Green
    Write-Host "=================================================="
    Write-Host @"

产物布局:
  RelayProxy-agent-windows-amd64.exe Windows x64 Agent 单文件版（WinUI + Core）
  RelayProxy-agent-windows-arm64.exe Windows ARM64 Agent 单文件版（WinUI + Core）
  RelayProxy-agent-windows-amd64.zip Windows x64 Agent 完整目录分发包（兼容/调试）
  RelayProxy-<platform>-<arch>.zip    各平台完整目录分发包
  linux-amd64/relay-agent           Linux x86_64 Agent
  linux-amd64/relay-server          Linux x86_64 Server（含 Admin Web UI）
  linux-arm64/relay-agent           Linux ARM64 Agent
  linux-arm64/relay-server          Linux ARM64 Server（含 Admin Web UI）
  darwin-amd64/relay-agent          macOS Intel Agent
  darwin-amd64/relay-server         macOS Intel Server（可构建实验产物）
  darwin-amd64/RelayProxy.app       macOS Intel Agent App 包装
  darwin-arm64/relay-agent          macOS Apple Silicon Agent
  darwin-arm64/relay-server         macOS Apple Silicon Server（可构建实验产物）
  darwin-arm64/RelayProxy.app       macOS Apple Silicon Agent App 包装
  windows-amd64/relay-agent-gui.exe Windows x64 WinUI 3 单文件桌面客户端
  windows-amd64/relay-agent.exe     Windows x64 Agent Core / CLI
  windows-amd64/relay-agent-wails.exe Windows x64 旧 Wails GUI（迁移期回退）
  windows-amd64/relay-server.exe    Windows x64 Server（含 Admin UI）
  windows-arm64/relay-agent-gui.exe Windows ARM64 WinUI 3 单文件桌面客户端
  windows-arm64/relay-agent.exe     Windows ARM64 Agent Core / CLI
  windows-arm64/relay-agent-wails.exe Windows ARM64 旧 Wails GUI（迁移期回退）
  windows-arm64/relay-server.exe    Windows ARM64 Server（含 Admin UI）
  */configs/*.yaml                  示例配置
  SHA256SUMS.txt

说明:
  Windows 主 Agent 下载为单 EXE；完整平台目录仍保留 CLI / Server / legacy 回退。
  WinUI 单文件首次启动会将运行依赖释放到 Windows 临时提取目录。
  macOS Server 当前作为可构建实验产物输出，不改变 README 中的正式支持范围。
  原生 macOS NetworkExtension Host 需要在 macOS 上使用 scripts/build.sh 构建。

部署提示:
  Linux:  chmod +x relay-server relay-agent
  Admin:  https://<server>:8443
  Windows: 双击 RelayProxy-agent-windows-amd64.exe 或 RelayProxy-agent-windows-arm64.exe
  透明代理: Windows x64 安装 / 修复 RelayProxy Network Service 后使用
  配置:   Windows 默认自动生成 %USERPROFILE%\.relayproxy\relay-agent.yaml
  授权:   首次连接后，在服务端管理控制台批准设备
  无界面: relay-agent.exe --no-gui
  自定义: relay-agent.exe --config <配置文件路径>

"@
}
finally {
    Pop-Location
}
