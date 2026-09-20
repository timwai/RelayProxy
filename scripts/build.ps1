#Requires -Version 5.1
<#
.SYNOPSIS
  RelayProxy 跨平台发布编译脚本

.DESCRIPTION
  产出：
    - Linux 服务端 / Agent amd64 / arm64
    - macOS Server / Agent amd64 / arm64（含 RelayProxy.app Agent 包装）
    - Windows 客户端 amd64 / arm64（relay-agent-gui.exe 桌面窗口 + relay-agent.exe CLI）
    - Windows 服务端 amd64 / arm64（relay-server.exe，Console 子系统，含 Admin UI）
    - 每个平台目录的完整 ZIP 分发包

  说明：Windows 客户端为原生 WebView2 桌面窗口（含系统托盘），
  relay-agent-gui.exe 使用 -H=windowsgui 子系统，双击不会弹出控制台窗口；
  relay-agent.exe 保留 Console 子系统供 CLI / 脚本调用，带参数时会自动保持无窗口。
  Windows arm64 产物可运行 Agent / Server，但系统透明代理目前仍只支持 amd64。
  Windows ARM64 桌面窗口需要 ARM64 WebView2 Runtime；缺失时会提示并继续提供本地 Web 管理页。
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

    function Invoke-GoBuild {
        param(
            [string]$GOOS,
            [string]$GOARCH,
            [string]$Package,
            [string]$Output,
            [string]$ExtraLdFlags = ""
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

        & go build -trimpath -ldflags $buildFlags -o $Output $Package
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
        Copy-Item (Join-Path $Root "assets\brand\logo.png") (Join-Path $resources "logo.png") -Force

        $plist = @"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
  <key>CFBundleDisplayName</key><string>RelayProxy</string>
  <key>CFBundleExecutable</key><string>RelayProxy</string>
  <key>CFBundleIdentifier</key><string>com.relayproxy.agent</string>
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

    # --- Windows client (icons + manifest embedded via resource_windows.syso) ---
    # Desktop build first: it is the artifact users are told to double-click.
    Invoke-GoBuild -GOOS "windows" -GOARCH "amd64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-amd64/relay-agent-gui.exe") `
        -ExtraLdFlags "-H=windowsgui"

    Invoke-GoBuild -GOOS "windows" -GOARCH "amd64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-amd64/relay-agent.exe")

    Invoke-GoBuild -GOOS "windows" -GOARCH "amd64" `
        -Package "./cmd/relay-server" `
        -Output (Join-Path $OutDir "windows-amd64/relay-server.exe")

    Invoke-GoBuild -GOOS "windows" -GOARCH "arm64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-arm64/relay-agent-gui.exe") `
        -ExtraLdFlags "-H=windowsgui"

    Invoke-GoBuild -GOOS "windows" -GOARCH "arm64" `
        -Package "./cmd/relay-agent" `
        -Output (Join-Path $OutDir "windows-arm64/relay-agent.exe")

    Invoke-GoBuild -GOOS "windows" -GOARCH "arm64" `
        -Package "./cmd/relay-server" `
        -Output (Join-Path $OutDir "windows-arm64/relay-server.exe")

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
        Where-Object { $_.Name -match '^(relay-server|relay-agent(-gui)?)(\.exe)?$|^WinDivert(64)?\.(dll|sys)$|^RelayProxy-.*\.zip
            $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
            $rel = $_.FullName.Substring($OutDir.Length).TrimStart('\', '/')
            $rel = $rel -replace '\\', '/'
            $lines += "$hash  $rel"
            Write-Host "  $hash  $rel"
        }
    $lines | Set-Content -Path $checksumFile -Encoding utf8

    # Restore host env
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

    Write-Host ""
    Write-Host "=================================================="
    Write-Host " Build complete -> $OutDir" -ForegroundColor Green
    Write-Host "=================================================="
    Write-Host @"

产物布局:
  RelayProxy-agent-windows-amd64.zip Windows x64 Agent 完整分发包（含 WinDivert）
  RelayProxy-<platform>-<arch>.zip    各平台完整目录分发包
  linux-amd64/relay-server          Linux x86_64 服务端（含 Admin Web UI）
  linux-arm64/relay-server          Linux ARM64  服务端（含 Admin Web UI）
  darwin-amd64/relay-agent          macOS Intel Agent
  darwin-amd64/relay-server         macOS Intel Server（可构建实验产物）
  darwin-amd64/RelayProxy.app       macOS Intel Agent App 包装
  darwin-arm64/relay-agent          macOS Apple Silicon Agent
  darwin-arm64/relay-server         macOS Apple Silicon Server（可构建实验产物）
  darwin-arm64/RelayProxy.app        macOS Apple Silicon Agent App 包装
  windows-amd64/relay-agent-gui.exe Windows 桌面客户端（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-agent.exe     Windows 客户端 CLI（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-server.exe    Windows 本地服务端（可选，含 Admin UI）
  windows-amd64/windivert/          可选外置运行库及许可证（EXE 已内嵌）
  windows-arm64/relay-agent-gui.exe Windows ARM64 桌面客户端（不含 x64 WinDivert）
  windows-arm64/relay-agent.exe     Windows ARM64 客户端 CLI
  windows-arm64/relay-server.exe    Windows ARM64 本地服务端（含 Admin UI）
  */configs/*.yaml                  示例配置
  SHA256SUMS.txt

部署提示:
  Linux:  chmod +x relay-server && ./relay-server -config configs/relay-server.yaml
  Admin:  https://<server>:8443
  桌面:   双击 relay-agent-gui.exe
  透明代理: 以管理员身份启动 Windows 客户端；保存启用设置后重启
  自启动: 透明代理模式使用管理员登录任务，首次设置需管理员权限
  配置:   Windows 默认自动生成 %USERPROFILE%\.relayproxy\relay-agent.yaml
  授权:   首次连接后，在服务端管理控制台批准设备
  无界面: relay-agent.exe --no-gui
  自定义: relay-agent.exe --config <配置文件路径>

"@
}
finally {
    Pop-Location
}
 } |
        ForEach-Object {
            $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
            $rel = $_.FullName.Substring($OutDir.Length).TrimStart('\', '/')
            $rel = $rel -replace '\\', '/'
            $lines += "$hash  $rel"
            Write-Host "  $hash  $rel"
        }
    $lines | Set-Content -Path $checksumFile -Encoding utf8

    # Restore host env
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

    Write-Host ""
    Write-Host "=================================================="
    Write-Host " Build complete -> $OutDir" -ForegroundColor Green
    Write-Host "=================================================="
    Write-Host @"

产物布局:
  RelayProxy-agent-windows-amd64.zip Windows 客户端完整分发包（含 WinDivert）
  linux-amd64/relay-server          Linux x86_64 服务端（含 Admin Web UI）
  linux-arm64/relay-server          Linux ARM64  服务端（含 Admin Web UI）
  windows-amd64/relay-agent-gui.exe Windows 桌面客户端（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-agent.exe     Windows 客户端 CLI（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-server.exe    Windows 本地服务端（可选，含 Admin UI）
  windows-amd64/windivert/          可选外置运行库及许可证（EXE 已内嵌）
  windows-arm64/relay-agent-gui.exe Windows ARM64 桌面客户端（不含 x64 WinDivert）
  windows-arm64/relay-agent.exe     Windows ARM64 客户端 CLI
  windows-arm64/relay-server.exe    Windows ARM64 本地服务端（含 Admin UI）
  */configs/*.yaml                  示例配置
  SHA256SUMS.txt

部署提示:
  Linux:  chmod +x relay-server && ./relay-server -config configs/relay-server.yaml
  Admin:  https://<server>:8443
  桌面:   双击 relay-agent-gui.exe
  透明代理: 以管理员身份启动 Windows 客户端；保存启用设置后重启
  自启动: 透明代理模式使用管理员登录任务，首次设置需管理员权限
  配置:   Windows 默认自动生成 %USERPROFILE%\.relayproxy\relay-agent.yaml
  授权:   首次连接后，在服务端管理控制台批准设备
  无界面: relay-agent.exe --no-gui
  自定义: relay-agent.exe --config <配置文件路径>

"@
}
finally {
    Pop-Location
}
 } |
        ForEach-Object {
            $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
            $rel = $_.FullName.Substring($OutDir.Length).TrimStart('\', '/')
            $rel = $rel -replace '\\', '/'
            $lines += "$hash  $rel"
            Write-Host "  $hash  $rel"
        }
    $lines | Set-Content -Path $checksumFile -Encoding utf8

    # Restore host env
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

    Write-Host ""
    Write-Host "=================================================="
    Write-Host " Build complete -> $OutDir" -ForegroundColor Green
    Write-Host "=================================================="
    Write-Host @"

产物布局:
  RelayProxy-agent-windows-amd64.zip Windows x64 Agent 完整分发包（含 WinDivert）
  RelayProxy-<platform>-<arch>.zip    各平台完整目录分发包
  linux-amd64/relay-server          Linux x86_64 服务端（含 Admin Web UI）
  linux-arm64/relay-server          Linux ARM64  服务端（含 Admin Web UI）
  darwin-amd64/relay-agent          macOS Intel Agent
  darwin-amd64/relay-server         macOS Intel Server（可构建实验产物）
  darwin-amd64/RelayProxy.app       macOS Intel Agent App 包装
  darwin-arm64/relay-agent          macOS Apple Silicon Agent
  darwin-arm64/relay-server         macOS Apple Silicon Server（可构建实验产物）
  darwin-arm64/RelayProxy.app        macOS Apple Silicon Agent App 包装
  windows-amd64/relay-agent-gui.exe Windows 桌面客户端（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-agent.exe     Windows 客户端 CLI（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-server.exe    Windows 本地服务端（可选，含 Admin UI）
  windows-amd64/windivert/          可选外置运行库及许可证（EXE 已内嵌）
  windows-arm64/relay-agent-gui.exe Windows ARM64 桌面客户端（不含 x64 WinDivert）
  windows-arm64/relay-agent.exe     Windows ARM64 客户端 CLI
  windows-arm64/relay-server.exe    Windows ARM64 本地服务端（含 Admin UI）
  */configs/*.yaml                  示例配置
  SHA256SUMS.txt

部署提示:
  Linux:  chmod +x relay-server && ./relay-server -config configs/relay-server.yaml
  Admin:  https://<server>:8443
  桌面:   双击 relay-agent-gui.exe
  透明代理: 以管理员身份启动 Windows 客户端；保存启用设置后重启
  自启动: 透明代理模式使用管理员登录任务，首次设置需管理员权限
  配置:   Windows 默认自动生成 %USERPROFILE%\.relayproxy\relay-agent.yaml
  授权:   首次连接后，在服务端管理控制台批准设备
  无界面: relay-agent.exe --no-gui
  自定义: relay-agent.exe --config <配置文件路径>

"@
}
finally {
    Pop-Location
}
 } |
        ForEach-Object {
            $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
            $rel = $_.FullName.Substring($OutDir.Length).TrimStart('\', '/')
            $rel = $rel -replace '\\', '/'
            $lines += "$hash  $rel"
            Write-Host "  $hash  $rel"
        }
    $lines | Set-Content -Path $checksumFile -Encoding utf8

    # Restore host env
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

    Write-Host ""
    Write-Host "=================================================="
    Write-Host " Build complete -> $OutDir" -ForegroundColor Green
    Write-Host "=================================================="
    Write-Host @"

产物布局:
  RelayProxy-agent-windows-amd64.zip Windows 客户端完整分发包（含 WinDivert）
  linux-amd64/relay-server          Linux x86_64 服务端（含 Admin Web UI）
  linux-arm64/relay-server          Linux ARM64  服务端（含 Admin Web UI）
  windows-amd64/relay-agent-gui.exe Windows 桌面客户端（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-agent.exe     Windows 客户端 CLI（单 EXE，内嵌 WinDivert）
  windows-amd64/relay-server.exe    Windows 本地服务端（可选，含 Admin UI）
  windows-amd64/windivert/          可选外置运行库及许可证（EXE 已内嵌）
  windows-arm64/relay-agent-gui.exe Windows ARM64 桌面客户端（不含 x64 WinDivert）
  windows-arm64/relay-agent.exe     Windows ARM64 客户端 CLI
  windows-arm64/relay-server.exe    Windows ARM64 本地服务端（含 Admin UI）
  */configs/*.yaml                  示例配置
  SHA256SUMS.txt

部署提示:
  Linux:  chmod +x relay-server && ./relay-server -config configs/relay-server.yaml
  Admin:  https://<server>:8443
  桌面:   双击 relay-agent-gui.exe
  透明代理: 以管理员身份启动 Windows 客户端；保存启用设置后重启
  自启动: 透明代理模式使用管理员登录任务，首次设置需管理员权限
  配置:   Windows 默认自动生成 %USERPROFILE%\.relayproxy\relay-agent.yaml
  授权:   首次连接后，在服务端管理控制台批准设备
  无界面: relay-agent.exe --no-gui
  自定义: relay-agent.exe --config <配置文件路径>

"@
}
finally {
    Pop-Location
}
