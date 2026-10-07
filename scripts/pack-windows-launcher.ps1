#Requires -Version 5.1
param(
    [Parameter(Mandatory = $true)][string]$Launcher,
    [Parameter(Mandatory = $true)][string]$Payload,
    [Parameter(Mandatory = $true)][string]$Output
)

$ErrorActionPreference = "Stop"
$Magic = "RELAYPROXY_GUI2!"
$MagicBytes = [System.Text.Encoding]::ASCII.GetBytes($Magic)
if ($MagicBytes.Length -ne 16) { throw "launcher footer magic must be exactly 16 bytes" }

$Launcher = [System.IO.Path]::GetFullPath($Launcher)
$Payload = [System.IO.Path]::GetFullPath($Payload)
$Output = [System.IO.Path]::GetFullPath($Output)
if (-not (Test-Path -LiteralPath $Launcher)) { throw "launcher stub not found: $Launcher" }
if (-not (Test-Path -LiteralPath $Payload)) { throw "native UI payload not found: $Payload" }
if ($Launcher -eq $Output -or $Payload -eq $Output) { throw "Output must differ from Launcher and Payload" }

$parent = Split-Path -Parent $Output
if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }

$payloadInfo = Get-Item -LiteralPath $Payload
$hashHex = (Get-FileHash -LiteralPath $Payload -Algorithm SHA256).Hash.ToLowerInvariant()
$hashBytes = New-Object byte[] 32
for ($i = 0; $i -lt 32; $i++) {
    $hashBytes[$i] = [Convert]::ToByte($hashHex.Substring($i * 2, 2), 16)
}

$out = [System.IO.File]::Open($Output, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
try {
    $stub = [System.IO.File]::OpenRead($Launcher)
    try { $stub.CopyTo($out) } finally { $stub.Dispose() }

    $payloadStart = $out.Position
    $payloadStream = [System.IO.File]::OpenRead($Payload)
    try {
        $gzip = [System.IO.Compression.GZipStream]::new(
            $out,
            [System.IO.Compression.CompressionLevel]::Optimal,
            $true
        )
        try { $payloadStream.CopyTo($gzip) } finally { $gzip.Dispose() }
    }
    finally {
        $payloadStream.Dispose()
    }
    $compressedSize = [UInt64]($out.Position - $payloadStart)

    $compressedSizeBytes = [BitConverter]::GetBytes($compressedSize)
    $payloadSizeBytes = [BitConverter]::GetBytes([UInt64]$payloadInfo.Length)
    if (-not [BitConverter]::IsLittleEndian) {
        [Array]::Reverse($compressedSizeBytes)
        [Array]::Reverse($payloadSizeBytes)
    }

    $out.Write($MagicBytes, 0, $MagicBytes.Length)
    $out.Write($compressedSizeBytes, 0, $compressedSizeBytes.Length)
    $out.Write($payloadSizeBytes, 0, $payloadSizeBytes.Length)
    $out.Write($hashBytes, 0, $hashBytes.Length)
}
finally {
    $out.Dispose()
}

$final = Get-Item -LiteralPath $Output
$stubBytes = (Get-Item $Launcher).Length
$compressedPayloadBytes = $final.Length - $stubBytes - 64
$ratio = if ($payloadInfo.Length -gt 0) { [math]::Round(($compressedPayloadBytes / $payloadInfo.Length) * 100, 1) } else { 0 }
Write-Host "[launcher] stub: $([math]::Round($stubBytes / 1MB, 2)) MiB"
Write-Host "[launcher] WinUI payload raw: $([math]::Round($payloadInfo.Length / 1MB, 2)) MiB"
Write-Host "[launcher] WinUI payload gzip: $([math]::Round($compressedPayloadBytes / 1MB, 2)) MiB ($ratio%)"
Write-Host "[launcher] final rename-safe EXE: $([math]::Round($final.Length / 1MB, 2)) MiB"
Write-Host "[launcher] payload sha256: $hashHex"
