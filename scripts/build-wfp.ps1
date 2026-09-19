#Requires -Version 5.1
[CmdletBinding()]
param(
    [ValidateSet("all", "x64", "ARM64")]
    [string]$Platform = "all",
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Release",
    [string]$OutDir = "",
    [string]$CertificateThumbprint = "",
    [switch]$SkipCatalog
)

$ErrorActionPreference = "Stop"
$Root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$Project = Join-Path $Root "native\windows\wfp\RelayProxyWfp.vcxproj"
$Inf = Join-Path $Root "native\windows\wfp\RelayProxyWfp.inf"

if (-not $OutDir) {
    $OutDir = Join-Path $Root "dist\windows-wfp"
}
$OutDir = [System.IO.Path]::GetFullPath($OutDir)

function Find-Tool {
    param([string]$Name)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    $kits = "${env:ProgramFiles(x86)}\Windows Kits\10\bin"
    if (Test-Path $kits) {
        $match = Get-ChildItem $kits -Directory -ErrorAction SilentlyContinue |
            Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName "x64\$Name" } |
            Where-Object { Test-Path $_ } |
            Select-Object -First 1
        if ($match) { return $match }
    }
    return $null
}

function Find-MSBuild {
    # WDK 28000 INF verification requires a 64-bit MSBuild host. Prefer the
    # amd64 MSBuild explicitly; a 32-bit host makes the WDK task look for the
    # obsolete x86\InfVerif.dll path and can fail after the driver already linked.
    $vswhere = Join-Path ${env:ProgramFiles(x86)} "Microsoft Visual Studio\Installer\vswhere.exe"
    if (Test-Path $vswhere) {
        $vsPath = & $vswhere -latest -products * -requires Microsoft.Component.MSBuild -property installationPath |
            Select-Object -First 1
        if ($vsPath) {
            $amd64 = Join-Path $vsPath "MSBuild\Current\Bin\amd64\MSBuild.exe"
            if (Test-Path $amd64) { return $amd64 }

            throw "64-bit MSBuild was not found at $amd64. WDK 28000 INF verification requires the amd64 MSBuild host."
        }
    }

    throw "64-bit MSBuild.exe not found. Install/repair Visual Studio 2026 with Desktop development with C++ and the Windows Driver Kit component."
}

function Assert-WDKVisualStudioIntegration {
    $vswhere = Join-Path ${env:ProgramFiles(x86)} "Microsoft Visual Studio\Installer\vswhere.exe"
    if (-not (Test-Path $vswhere)) {
        throw "vswhere.exe not found. Repair Visual Studio Installer before building the WFP driver."
    }

    $vsPath = & $vswhere -latest -products * -requires Microsoft.Component.MSBuild -property installationPath |
        Select-Object -First 1
    if (-not $vsPath) {
        throw "No Visual Studio installation with MSBuild was found."
    }

    $driverKitPath = & $vswhere -latest -products * -requires Component.Microsoft.Windows.DriverKit -property installationPath |
        Select-Object -First 1
    if (-not $driverKitPath) {
        throw @"
Visual Studio Windows Driver Kit integration is missing.

The WDK NuGet packages and Windows SDK are present, but Visual Studio has not
installed the 'Windows Driver Kit' individual component
(Component.Microsoft.Windows.DriverKit). Without that VSIX/MSBuild integration,
MSBuild cannot resolve PlatformToolset=WindowsKernelModeDriver10.0 and fails
with MSB8020.

Fix:
  1. Open Visual Studio Installer -> Visual Studio 2026 -> Modify.
  2. Individual components -> select 'Windows Driver Kit'.
  3. Apply changes, then restart all Visual Studio/PowerShell processes.

Official automated setup:
  winget configure -f 'https://raw.githubusercontent.com/microsoft/Windows-driver-samples/main/_wdk_utils/winget/configs/wdk-vscommunity.dsc.yaml'

Detected Visual Studio:
  $vsPath
"@
    }

    Write-Host "[WFP] Visual Studio DriverKit integration: $driverKitPath" -ForegroundColor DarkGray
}

function Build-One {
    param([string]$TargetPlatform)

    $arch = if ($TargetPlatform -eq "x64") { "amd64" } else { "arm64" }
    Write-Host "[WFP] Building $TargetPlatform / $Configuration" -ForegroundColor Cyan

    $msbuild = Find-MSBuild
    Write-Host "[WFP] MSBuild host: $msbuild" -ForegroundColor DarkGray

    $stampInf = Find-Tool "stampinf.exe"
    if (-not $stampInf) {
        throw @"
stampinf.exe was not found.

Visual Studio DriverKit integration is installed, but the WDK command-line
tools are missing or incomplete. StampInf is supplied by the Windows Driver
Kit, not by Visual Studio itself.

Expected location resembles:
  C:\Program Files (x86)\Windows Kits\10\bin\10.0.28000.0\x64\stampinf.exe

Repair/install the full WDK, then retry:
  winget install Microsoft.WindowsWDK.10.0.28000 --force

You can verify manually with:
  Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin" -Recurse -Filter stampinf.exe
"@
    }
    $stampInfDir = Split-Path -Parent $stampInf
    $stampInfToolPath = $stampInfDir.TrimEnd("\") + "\"
    Write-Host "[WFP] StampInf tool: $stampInf" -ForegroundColor DarkGray

    # Some WDK 28000 command-line builds run DPVerifierTask with a relative
    # x86\InfVerif.dll path and fail even though the driver itself builds.
    # Skip that embedded package-verification task here and perform mandatory
    # validation explicitly with the x64 InfVerif.exe below. StampInf remains
    # enabled, and the WDK host-tools directory is still placed first on PATH.
    $oldPath = $env:PATH
    $env:PATH = $stampInfDir + ";" + $env:PATH
    try {
        & $msbuild $Project `
            /restore `
            /m `
            /t:Build `
            /p:Configuration=$Configuration `
            /p:Platform=$TargetPlatform `
            /p:SignMode=Off `
            /p:SkipPackageVerification=true `
            "/p:StampInfToolPath=$stampInfToolPath" `
            /nologo
    } finally {
        $env:PATH = $oldPath
    }
    if ($LASTEXITCODE -ne 0) {
        throw "WFP driver build failed for $TargetPlatform"
    }

    $builtInf = Join-Path $Root "native\windows\wfp\bin\$TargetPlatform\$Configuration\RelayProxyWfp.inf"
    if (-not (Test-Path $builtInf)) {
        throw "Stamped INF was not produced for ${TargetPlatform}: $builtInf"
    }

    $infVerif = $null
    $toolsRoot = Join-Path ${env:ProgramFiles(x86)} "Windows Kits\10\Tools"
    if (Test-Path $toolsRoot) {
        $infVerif = Get-ChildItem $toolsRoot -Recurse -Filter InfVerif.exe -File -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match "\\x64\\InfVerif\.exe$" } |
            Sort-Object FullName -Descending |
            Select-Object -ExpandProperty FullName -First 1
    }
    if (-not $infVerif) {
        throw "InfVerif.exe was not found under $toolsRoot. Repair the Windows Driver Kit installation."
    }
    Write-Host "[WFP] InfVerif tool: $infVerif" -ForegroundColor DarkGray
    & $infVerif /h $builtInf
    if ($LASTEXITCODE -ne 0) {
        throw "INF verification failed for $TargetPlatform"
    }

    $sys = Join-Path $Root "native\windows\wfp\bin\$TargetPlatform\$Configuration\RelayProxyWfp.sys"
    if (-not (Test-Path $sys)) {
        $sys = Get-ChildItem (Join-Path $Root "native\windows\wfp") -Recurse -Filter RelayProxyWfp.sys |
            Where-Object { $_.FullName -match [regex]::Escape($TargetPlatform) -and $_.FullName -match [regex]::Escape($Configuration) } |
            Select-Object -ExpandProperty FullName -First 1
    }
    if (-not $sys -or -not (Test-Path $sys)) {
        throw "RelayProxyWfp.sys was not produced for $TargetPlatform"
    }

    $package = Join-Path $OutDir $arch
    New-Item -ItemType Directory -Path $package -Force | Out-Null
    Copy-Item $sys (Join-Path $package "RelayProxyWfp.sys") -Force
    Copy-Item $Inf (Join-Path $package "RelayProxyWfp.inf") -Force

    $signtool = $null
    if ($CertificateThumbprint) {
        $signtool = Find-Tool "signtool.exe"
        if (-not $signtool) {
            throw "signtool.exe not found"
        }

        # The catalog hashes the final SYS bytes. Sign the driver first, then
        # build the CAT from that signed image, and sign the CAT last.
        $sysTarget = Join-Path $package "RelayProxyWfp.sys"
        & $signtool sign /sha1 $CertificateThumbprint /fd SHA256 /tr "http://timestamp.digicert.com" /td SHA256 $sysTarget
        if ($LASTEXITCODE -ne 0) {
            throw "Signing failed: $sysTarget"
        }
    }

    if (-not $SkipCatalog) {
        $inf2cat = Find-Tool "Inf2Cat.exe"
        if ($inf2cat) {
            $os = if ($TargetPlatform -eq "x64") { "10_X64" } else { "10_ARM64" }
            & $inf2cat /driver:$package /os:$os /uselocaltime
            if ($LASTEXITCODE -ne 0) {
                throw "Inf2Cat failed for $TargetPlatform"
            }
        } else {
            throw "Inf2Cat.exe not found. Install the Windows Driver Kit, or use -SkipCatalog only for compile-only validation."
        }
    }

    if ($CertificateThumbprint -and -not $SkipCatalog) {
        $cat = Join-Path $package "RelayProxyWfp.cat"
        if (-not (Test-Path $cat)) {
            throw "Catalog was not produced: $cat"
        }
        & $signtool sign /sha1 $CertificateThumbprint /fd SHA256 /tr "http://timestamp.digicert.com" /td SHA256 $cat
        if ($LASTEXITCODE -ne 0) {
            throw "Signing failed: $cat"
        }
    }

    $hash = (Get-FileHash (Join-Path $package "RelayProxyWfp.sys") -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Host "[WFP] $arch OK SHA256=$hash" -ForegroundColor Green
}

if (-not (Test-Path $Project)) { throw "Missing project: $Project" }
if (-not (Test-Path $Inf)) { throw "Missing INF: $Inf" }

Assert-WDKVisualStudioIntegration

$targets = if ($Platform -eq "all") { @("x64", "ARM64") } else { @($Platform) }
foreach ($target in $targets) {
    Build-One $target
}
