#Requires -Version 5.1
<#
.SYNOPSIS
  RelayProxy Android APK one-click build script.

.DESCRIPTION
  Validates the host toolchain, builds the gomobile AAR, builds the Android APK,
  and copies the final artifact to dist/android.

  Debug APKs are signed by the Android debug keystore and are directly installable.
  Release APKs are signed automatically when these environment variables exist:
    RELAY_ANDROID_KEYSTORE
    RELAY_ANDROID_KEY_ALIAS
    RELAY_ANDROID_KEYSTORE_PASSWORD
    RELAY_ANDROID_KEY_PASSWORD

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\scripts\build-android.ps1

.EXAMPLE
  .\scripts\build-android.ps1 -Configuration Release -Clean
#>
param(
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Release",
    [string]$OutDir = "",
    [switch]$Clean,
    [switch]$SkipTests,
    [switch]$SkipSdkInstall,
    [switch]$SkipToolInstall
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$AndroidDir = Join-Path $Root "android"
$AppDir = Join-Path $AndroidDir "app"
$AarPath = Join-Path $AppDir "libs\mobilecore.aar"

if (-not $OutDir) {
    $OutDir = Join-Path $Root "dist\android"
}
$OutDir = [System.IO.Path]::GetFullPath($OutDir)

$SdkPlatform = "android-35"
$BuildToolsVersion = "35.0.0"
$NdkVersion = "27.2.12479018"
$GradleVersion = "8.9"

function Write-Step {
    param([string]$Text)
    Write-Host ""
    Write-Host "==> $Text" -ForegroundColor Cyan
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
    )
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed ($LASTEXITCODE): $Command $($Arguments -join ' ')"
    }
}

function Resolve-CommandPath {
    param([string]$Name)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

function Resolve-AndroidSdk {
    $candidates = @()
    if ($env:ANDROID_SDK_ROOT) { $candidates += $env:ANDROID_SDK_ROOT }
    if ($env:ANDROID_HOME) { $candidates += $env:ANDROID_HOME }
    if ($env:LOCALAPPDATA) { $candidates += (Join-Path $env:LOCALAPPDATA "Android\Sdk") }

    foreach ($candidate in ($candidates | Select-Object -Unique)) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    throw "Android SDK not found. Set ANDROID_SDK_ROOT/ANDROID_HOME or install Android Studio SDK."
}

function Resolve-SdkManager {
    param([string]$SdkRoot)

    $candidates = @(
        (Join-Path $SdkRoot "cmdline-tools\latest\bin\sdkmanager.bat"),
        (Join-Path $SdkRoot "cmdline-tools\bin\sdkmanager.bat"),
        (Join-Path $SdkRoot "tools\bin\sdkmanager.bat")
    )
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }

    $cmdTools = Join-Path $SdkRoot "cmdline-tools"
    if (Test-Path -LiteralPath $cmdTools) {
        $found = Get-ChildItem -LiteralPath $cmdTools -Filter "sdkmanager.bat" -Recurse -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($found) { return $found.FullName }
    }
    return $null
}

function Resolve-Ndk {
    param([string]$SdkRoot)

    if ($env:ANDROID_NDK_HOME -and (Test-Path -LiteralPath $env:ANDROID_NDK_HOME)) {
        return (Resolve-Path -LiteralPath $env:ANDROID_NDK_HOME).Path
    }

    $pinned = Join-Path $SdkRoot "ndk\$NdkVersion"
    if (Test-Path -LiteralPath $pinned) {
        return (Resolve-Path -LiteralPath $pinned).Path
    }

    $ndkRoot = Join-Path $SdkRoot "ndk"
    if (Test-Path -LiteralPath $ndkRoot) {
        $latest = Get-ChildItem -LiteralPath $ndkRoot -Directory |
            Sort-Object Name -Descending |
            Select-Object -First 1
        if ($latest) { return $latest.FullName }
    }

    throw "Android NDK not found. Install NDK $NdkVersion or set ANDROID_NDK_HOME."
}

function Resolve-GoTool {
    param(
        [string]$Name,
        [switch]$Install
    )

    $path = Resolve-CommandPath $Name
    if ($path) { return $path }

    $goPath = (& go env GOPATH).Trim()
    if ($LASTEXITCODE -ne 0 -or -not $goPath) {
        throw "Unable to resolve GOPATH."
    }
    $candidate = Join-Path $goPath "bin\$Name.exe"
    if (Test-Path -LiteralPath $candidate) { return $candidate }

    if (-not $Install) {
        throw "$Name not found. Re-run without -SkipToolInstall or install it manually."
    }

    Write-Step "Install $Name"
    if ($Name -eq "gomobile") {
        Invoke-Checked "go" "install" "golang.org/x/mobile/cmd/gomobile@latest"
    } elseif ($Name -eq "gobind") {
        Invoke-Checked "go" "install" "golang.org/x/mobile/cmd/gobind@latest"
    } else {
        throw "Unknown Go tool: $Name"
    }

    if (-not (Test-Path -LiteralPath $candidate)) {
        throw "$Name installation completed but $candidate was not created."
    }
    return $candidate
}

function Resolve-Gradle {
    $systemGradle = Resolve-CommandPath "gradle"
    if ($systemGradle) { return $systemGradle }

    if ($env:LOCALAPPDATA) {
        $cacheRoot = Join-Path $env:LOCALAPPDATA "RelayProxy\tools"
    } else {
        $cacheRoot = Join-Path $env:TEMP "RelayProxy-tools"
    }

    $gradleHome = Join-Path $cacheRoot "gradle-$GradleVersion"
    $gradleBat = Join-Path $gradleHome "bin\gradle.bat"
    if (Test-Path -LiteralPath $gradleBat) { return $gradleBat }

    Write-Step "Download Gradle $GradleVersion"
    New-Item -ItemType Directory -Path $cacheRoot -Force | Out-Null
    $zip = Join-Path $cacheRoot "gradle-$GradleVersion-bin.zip"
    Invoke-WebRequest -Uri "https://services.gradle.org/distributions/gradle-$GradleVersion-bin.zip" -OutFile $zip -UseBasicParsing

    $extract = Join-Path $cacheRoot "_gradle_extract_$PID"
    if (Test-Path -LiteralPath $extract) {
        Remove-Item -LiteralPath $extract -Recurse -Force
    }
    Expand-Archive -LiteralPath $zip -DestinationPath $extract -Force

    $extractedHome = Join-Path $extract "gradle-$GradleVersion"
    if (-not (Test-Path -LiteralPath $extractedHome)) {
        throw "Gradle archive did not contain gradle-$GradleVersion."
    }
    if (Test-Path -LiteralPath $gradleHome) {
        Remove-Item -LiteralPath $gradleHome -Recurse -Force
    }
    Move-Item -LiteralPath $extractedHome -Destination $gradleHome
    Remove-Item -LiteralPath $extract -Recurse -Force
    Remove-Item -LiteralPath $zip -Force

    if (-not (Test-Path -LiteralPath $gradleBat)) {
        throw "Gradle executable not found after extraction."
    }
    return $gradleBat
}

function Resolve-ApkSigner {
    param([string]$SdkRoot)

    $pinned = Join-Path $SdkRoot "build-tools\$BuildToolsVersion\apksigner.bat"
    if (Test-Path -LiteralPath $pinned) { return $pinned }

    $buildToolsRoot = Join-Path $SdkRoot "build-tools"
    if (Test-Path -LiteralPath $buildToolsRoot) {
        $latest = Get-ChildItem -LiteralPath $buildToolsRoot -Directory |
            Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName "apksigner.bat" } |
            Where-Object { Test-Path -LiteralPath $_ } |
            Select-Object -First 1
        if ($latest) { return $latest }
    }
    return $null
}

function Copy-FinalApk {
    param(
        [string]$Source,
        [string]$DestinationName
    )
    if (-not (Test-Path -LiteralPath $Source)) {
        throw "Expected APK not found: $Source"
    }
    New-Item -ItemType Directory -Path $OutDir -Force | Out-Null
    $destination = Join-Path $OutDir $DestinationName
    Copy-Item -LiteralPath $Source -Destination $destination -Force
    return $destination
}

Write-Host "=================================================="
Write-Host " RelayProxy Android APK Build"
Write-Host " Configuration: $Configuration"
Write-Host " Root:          $Root"
Write-Host " OutDir:        $OutDir"
Write-Host "=================================================="

$modBackup = $null
$sumBackup = $null
$moduleTouched = $false

Push-Location $Root
try {
    Write-Step "Validate host toolchain"
    if (-not (Resolve-CommandPath "go")) { throw "Go was not found in PATH." }
    if (-not (Resolve-CommandPath "java")) { throw "Java was not found in PATH. JDK 17 is recommended." }

    Invoke-Checked "go" "version"
    Invoke-Checked "java" "-version"

    $sdkRoot = Resolve-AndroidSdk
    $env:ANDROID_SDK_ROOT = $sdkRoot
    $env:ANDROID_HOME = $sdkRoot
    Write-Host "Android SDK: $sdkRoot" -ForegroundColor Green

    if (-not $SkipSdkInstall) {
        $sdkManager = Resolve-SdkManager $sdkRoot
        if (-not $sdkManager) {
            throw "sdkmanager.bat not found. Install Android SDK Command-line Tools or use -SkipSdkInstall with preinstalled packages."
        }

        Write-Step "Install/verify Android SDK packages"
        Invoke-Checked $sdkManager "platforms;$SdkPlatform" "build-tools;$BuildToolsVersion" "ndk;$NdkVersion"
    }

    $ndkRoot = Resolve-Ndk $sdkRoot
    $env:ANDROID_NDK_HOME = $ndkRoot
    $env:ANDROID_NDK_ROOT = $ndkRoot
    Write-Host "Android NDK: $ndkRoot" -ForegroundColor Green

    $gomobile = Resolve-GoTool "gomobile" -Install:(-not $SkipToolInstall)
    $gobind = Resolve-GoTool "gobind" -Install:(-not $SkipToolInstall)
    Write-Host "gomobile: $gomobile" -ForegroundColor Green
    Write-Host "gobind:   $gobind" -ForegroundColor Green

    $modPath = Join-Path $Root "go.mod"
    $sumPath = Join-Path $Root "go.sum"
    $modBackup = [System.IO.Path]::GetTempFileName()
    $sumBackup = [System.IO.Path]::GetTempFileName()
    Copy-Item -LiteralPath $modPath -Destination $modBackup -Force
    if (Test-Path -LiteralPath $sumPath) {
        Copy-Item -LiteralPath $sumPath -Destination $sumBackup -Force
    } else {
        Remove-Item -LiteralPath $sumBackup -Force
        $sumBackup = $null
    }

    Write-Step "Prepare gomobile"
    Invoke-Checked "go" "get" "-tool" "golang.org/x/mobile/cmd/gobind@latest"
    $moduleTouched = $true
    Invoke-Checked $gomobile "init"

    if (-not $SkipTests) {
        Write-Step "Run Android core Go tests"
        Invoke-Checked "go" "test" "./mobile/androidcore" "./agent/exit"
    }

    if ($Clean) {
        Write-Step "Clean previous Android outputs"
        $appBuild = Join-Path $AppDir "build"
        if (Test-Path -LiteralPath $appBuild) {
            Remove-Item -LiteralPath $appBuild -Recurse -Force
        }
        if (Test-Path -LiteralPath $AarPath) {
            Remove-Item -LiteralPath $AarPath -Force
        }
    }

    Write-Step "Build gomobile AAR"
    New-Item -ItemType Directory -Path (Split-Path -Parent $AarPath) -Force | Out-Null
    Invoke-Checked $gomobile "bind" "-target=android" "-androidapi" "26" "-javapkg" "com.relayproxy.core" "-o" $AarPath "./mobile/androidcore"

    if (-not (Test-Path -LiteralPath $AarPath)) {
        throw "gomobile did not produce $AarPath"
    }
    $aarSize = [math]::Round((Get-Item -LiteralPath $AarPath).Length / 1MB, 2)
    Write-Host "AAR: $AarPath ($aarSize MB)" -ForegroundColor Green

    $gradle = Resolve-Gradle
    Write-Host "Gradle: $gradle" -ForegroundColor Green

    Write-Step "Build Android $Configuration APK"
    if ($Configuration -eq "Release") {
        $task = ":app:assembleRelease"
    } else {
        $task = ":app:assembleDebug"
    }
    Invoke-Checked $gradle "-p" $AndroidDir $task

    if ($Configuration -eq "Debug") {
        $source = Join-Path $AppDir "build\outputs\apk\debug\app-debug.apk"
        $finalApk = Copy-FinalApk $source "RelayProxy-Android-debug.apk"
    } else {
        $unsigned = Join-Path $AppDir "build\outputs\apk\release\app-release-unsigned.apk"
        $hasSigning = (
            $env:RELAY_ANDROID_KEYSTORE -and
            $env:RELAY_ANDROID_KEY_ALIAS -and
            $env:RELAY_ANDROID_KEYSTORE_PASSWORD -and
            $env:RELAY_ANDROID_KEY_PASSWORD
        )

        if ($hasSigning) {
            if (-not (Test-Path -LiteralPath $env:RELAY_ANDROID_KEYSTORE)) {
                throw "RELAY_ANDROID_KEYSTORE does not exist: $env:RELAY_ANDROID_KEYSTORE"
            }
            $apksigner = Resolve-ApkSigner $sdkRoot
            if (-not $apksigner) {
                throw "apksigner.bat not found in Android build-tools."
            }

            Write-Step "Sign Release APK"
            New-Item -ItemType Directory -Path $OutDir -Force | Out-Null
            $finalApk = Join-Path $OutDir "RelayProxy-Android-release.apk"
            if (Test-Path -LiteralPath $finalApk) {
                Remove-Item -LiteralPath $finalApk -Force
            }
            Invoke-Checked $apksigner "sign" "--ks" $env:RELAY_ANDROID_KEYSTORE "--ks-key-alias" $env:RELAY_ANDROID_KEY_ALIAS "--ks-pass" "env:RELAY_ANDROID_KEYSTORE_PASSWORD" "--key-pass" "env:RELAY_ANDROID_KEY_PASSWORD" "--out" $finalApk $unsigned
            Invoke-Checked $apksigner "verify" "--verbose" $finalApk
        } else {
            $finalApk = Copy-FinalApk $unsigned "RelayProxy-Android-release-unsigned.apk"
            Write-Warning "Release APK is unsigned. Set all RELAY_ANDROID_* signing variables to create an installable signed release APK."
        }
    }

    $hash = (Get-FileHash -LiteralPath $finalApk -Algorithm SHA256).Hash
    $size = [math]::Round((Get-Item -LiteralPath $finalApk).Length / 1MB, 2)

    Write-Host ""
    Write-Host "==================================================" -ForegroundColor Green
    Write-Host " Android APK build completed" -ForegroundColor Green
    Write-Host " APK:    $finalApk" -ForegroundColor Green
    Write-Host " Size:   $size MB"
    Write-Host " SHA256: $hash"
    Write-Host "==================================================" -ForegroundColor Green
}
finally {
    if ($modBackup -and (Test-Path -LiteralPath $modBackup)) {
        Copy-Item -LiteralPath $modBackup -Destination (Join-Path $Root "go.mod") -Force
        Remove-Item -LiteralPath $modBackup -Force
    }
    if ($sumBackup -and (Test-Path -LiteralPath $sumBackup)) {
        Copy-Item -LiteralPath $sumBackup -Destination (Join-Path $Root "go.sum") -Force
        Remove-Item -LiteralPath $sumBackup -Force
    } elseif ($moduleTouched -and -not $sumBackup) {
        $sumPath = Join-Path $Root "go.sum"
        if (Test-Path -LiteralPath $sumPath) {
            Remove-Item -LiteralPath $sumPath -Force
        }
    }
    Pop-Location
}
