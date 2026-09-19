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

    $kits = Join-Path ${env:ProgramFiles(x86)} "Windows Kits\10\bin"
    if (-not (Test-Path $kits)) {
        return $null
    }

    # WDK tools are not guaranteed to use the same host-architecture folder.
    # In particular, Inf2Cat is commonly installed under x86 even when building
    # x64/ARM64 drivers. Prefer newer versioned kits, then unversioned aliases.
    $hostArches = @("x64", "x86", "arm64")
    $versionDirs = Get-ChildItem $kits -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match '^\d+\.\d+\.\d+\.\d+

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
    $oldPath = $env:PATH
    $extraMSBuildArgs = @()

    if ($stampInf) {
        $stampInfDir = Split-Path -Parent $stampInf
        $stampInfToolPath = $stampInfDir.TrimEnd("\") + "\"
        Write-Host "[WFP] StampInf tool: $stampInf" -ForegroundColor DarkGray
        $env:PATH = $stampInfDir + ";" + $env:PATH
        $extraMSBuildArgs += "/p:StampInfToolPath=$stampInfToolPath"
    } else {
        Write-Host "[WFP] Local StampInf tool not found; using the WDK/NuGet tool resolution." -ForegroundColor DarkGray
    }

    # WDK 28000 command-line builds can run DPVerifierTask with a relative
    # x86\InfVerif.dll path and fail with 0x8007007E even after the SYS links.
    # Disable only that embedded package-verification task. The stamped INF is
    # validated explicitly with the standalone x64 InfVerif.exe below whenever
    # the full WDK tools are available.
    try {
        & $msbuild $Project `
            /restore `
            /m `
            /t:Build `
            /p:Configuration=$Configuration `
            /p:Platform=$TargetPlatform `
            /p:SignMode=Off `
            /p:SkipPackageVerification=true `
            @extraMSBuildArgs `
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

    if ($infVerif) {
        Write-Host "[WFP] InfVerif tool: $infVerif" -ForegroundColor DarkGray
        & $infVerif /h $builtInf
        if ($LASTEXITCODE -ne 0) {
            throw "INF verification failed for $TargetPlatform"
        }
    } elseif ($SkipCatalog) {
        Write-Warning "InfVerif.exe was not found; compile-only validation will continue because -SkipCatalog was requested."
    } else {
        throw "InfVerif.exe was not found under $toolsRoot. A release package requires the full WDK verification tools."
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
            Write-Host "[WFP] Inf2Cat tool: $inf2cat" -ForegroundColor DarkGray
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
 } |
        Sort-Object { [version]$_.Name } -Descending

    foreach ($dir in $versionDirs) {
        foreach ($hostArch in $hostArches) {
            $candidate = Join-Path $dir.FullName ($hostArch + "\" + $Name)
            if (Test-Path $candidate) {
                return $candidate
            }
        }
    }

    foreach ($hostArch in $hostArches) {
        $candidate = Join-Path $kits ($hostArch + "\" + $Name)
        if (Test-Path $candidate) {
            return $candidate
        }
    }

    # Final fallback for WDK layout changes. Restrict this to the bin tree and
    # prefer x64, then x86, then any remaining match.
    $matches = Get-ChildItem $kits -Recurse -File -Filter $Name -ErrorAction SilentlyContinue
    if ($matches) {
        $preferred = $matches |
            Sort-Object @{
                Expression = {
                    if ($_.FullName -match '\\x64\\') { 0 }
                    elseif ($_.FullName -match '\\x86\\') { 1 }
                    else { 2 }
                }
            }, FullName |
            Select-Object -ExpandProperty FullName -First 1
        if ($preferred) { return $preferred }
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
    $oldPath = $env:PATH
    $extraMSBuildArgs = @()

    if ($stampInf) {
        $stampInfDir = Split-Path -Parent $stampInf
        $stampInfToolPath = $stampInfDir.TrimEnd("\") + "\"
        Write-Host "[WFP] StampInf tool: $stampInf" -ForegroundColor DarkGray
        $env:PATH = $stampInfDir + ";" + $env:PATH
        $extraMSBuildArgs += "/p:StampInfToolPath=$stampInfToolPath"
    } else {
        Write-Host "[WFP] Local StampInf tool not found; using the WDK/NuGet tool resolution." -ForegroundColor DarkGray
    }

    # WDK 28000 command-line builds can run DPVerifierTask with a relative
    # x86\InfVerif.dll path and fail with 0x8007007E even after the SYS links.
    # Disable only that embedded package-verification task. The stamped INF is
    # validated explicitly with the standalone x64 InfVerif.exe below whenever
    # the full WDK tools are available.
    try {
        & $msbuild $Project `
            /restore `
            /m `
            /t:Build `
            /p:Configuration=$Configuration `
            /p:Platform=$TargetPlatform `
            /p:SignMode=Off `
            /p:SkipPackageVerification=true `
            @extraMSBuildArgs `
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

    if ($infVerif) {
        Write-Host "[WFP] InfVerif tool: $infVerif" -ForegroundColor DarkGray
        & $infVerif /h $builtInf
        if ($LASTEXITCODE -ne 0) {
            throw "INF verification failed for $TargetPlatform"
        }
    } elseif ($SkipCatalog) {
        Write-Warning "InfVerif.exe was not found; compile-only validation will continue because -SkipCatalog was requested."
    } else {
        throw "InfVerif.exe was not found under $toolsRoot. A release package requires the full WDK verification tools."
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
