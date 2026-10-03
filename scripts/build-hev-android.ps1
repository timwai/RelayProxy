$ErrorActionPreference = "Stop"

$Version = "2.18.0"
$Sha256 = "93b3b33127436b4eab669798f1d50e019008585a27880df8cbdeffe5e70cb665"
$Archive = "hev-socks5-tunnel-$Version.tar.xz"
$Url = "https://github.com/heiher/hev-socks5-tunnel/releases/download/$Version/$Archive"
$Api = "android-26"
$Abis = "arm64-v8a armeabi-v7a"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$SdkRoot = if ($env:ANDROID_SDK_ROOT) { $env:ANDROID_SDK_ROOT } else { $env:ANDROID_HOME }
$NdkRoot = if ($env:ANDROID_NDK_HOME) { $env:ANDROID_NDK_HOME } else { $env:ANDROID_NDK_ROOT }
if (-not $NdkRoot -and $SdkRoot) {
    $NdkRoot = Join-Path $SdkRoot "ndk\27.2.12479018"
}
$NdkBuild = if ($NdkRoot) { Join-Path $NdkRoot "ndk-build.cmd" } else { $null }
if (-not $NdkBuild -or -not (Test-Path -LiteralPath $NdkBuild)) {
    throw "Android NDK 27.2.12479018 not found; set ANDROID_NDK_HOME."
}

$OutputDir = if ($env:HEV_ANDROID_LIBS_OUT) {
    $env:HEV_ANDROID_LIBS_OUT
} else {
    Join-Path $RepoRoot "android\app\build\generated\hev-jniLibs"
}
$CacheRoot = Join-Path ([System.IO.Path]::GetTempPath()) "relayproxy-hev\$Version"
$WorkDir = Join-Path $CacheRoot "build"
$SourceDir = Join-Path $WorkDir "hev-socks5-tunnel-$Version"
$ArchivePath = Join-Path $CacheRoot $Archive
New-Item -ItemType Directory -Path $CacheRoot -Force | Out-Null

$ActualHash = if (Test-Path -LiteralPath $ArchivePath) {
    (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
} else {
    ""
}
if ($ActualHash -ne $Sha256) {
    Remove-Item -LiteralPath $ArchivePath -Force -ErrorAction SilentlyContinue
    Invoke-WebRequest -Uri $Url -OutFile $ArchivePath
}
$ActualHash = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($ActualHash -ne $Sha256) {
    throw "Unexpected hev-socks5-tunnel archive checksum: $ActualHash"
}

if (Test-Path -LiteralPath $WorkDir) {
    Remove-Item -LiteralPath $WorkDir -Recurse -Force
}
New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null
& tar.exe -xf $ArchivePath -C $WorkDir
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $SourceDir "Android.mk"))) {
    throw "hev-socks5-tunnel source archive has an unexpected layout."
}
if (Test-Path -LiteralPath $OutputDir) {
    Remove-Item -LiteralPath $OutputDir -Recurse -Force
}
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null

& $NdkBuild `
    "NDK_PROJECT_PATH=null" `
    "APP_BUILD_SCRIPT=$(Join-Path $SourceDir 'Android.mk')" `
    "NDK_APPLICATION_MK=$(Join-Path $SourceDir 'Application.mk')" `
    "APP_ABI=$Abis" `
    "APP_PLATFORM=$Api" `
    "APP_SUPPORT_FLEXIBLE_PAGE_SIZES=true" `
    "NDK_LIBS_OUT=$OutputDir" `
    "NDK_OUT=$(Join-Path $WorkDir 'obj')"
if ($LASTEXITCODE -ne 0) {
    throw "hev-socks5-tunnel native build failed with exit code $LASTEXITCODE."
}

foreach ($Abi in @("arm64-v8a", "armeabi-v7a")) {
    $Library = Join-Path $OutputDir "$Abi\libhev-socks5-tunnel.so"
    if (-not (Test-Path -LiteralPath $Library) -or (Get-Item -LiteralPath $Library).Length -eq 0) {
        throw "Missing native library: $Library"
    }
}

$ReadElf = Join-Path $NdkRoot "toolchains\llvm\prebuilt\windows-x86_64\bin\llvm-readelf.exe"
if (-not (Test-Path -LiteralPath $ReadElf)) {
    throw "llvm-readelf not found: $ReadElf"
}
foreach ($Abi in @("arm64-v8a", "armeabi-v7a")) {
    $Library = Join-Path $OutputDir "$Abi\libhev-socks5-tunnel.so"
    $LoadSegments = @(& $ReadElf -l $Library | Where-Object { $_ -match '^\s*LOAD\s' })
    if ($LASTEXITCODE -ne 0 -or $LoadSegments.Count -eq 0) {
        throw "Could not inspect native library ELF segments: $Library"
    }
    foreach ($Segment in $LoadSegments) {
        if ($Segment.Trim().Split([char[]]" `t", [StringSplitOptions]::RemoveEmptyEntries)[-1] -ne "0x4000") {
            throw "$Library does not have 16 KB ELF LOAD alignment: $Segment"
        }
    }
}

Write-Host "Built hev-socks5-tunnel $Version for $Abis (API $Api)" -ForegroundColor Green
