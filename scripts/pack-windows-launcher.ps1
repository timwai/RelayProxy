#Requires -Version 5.1
param(
    [Parameter(Mandatory = $true)][string]$Launcher,
    [Parameter(Mandatory = $true)][string]$Payload,
    [Parameter(Mandatory = $true)][string]$Output
)

$ErrorActionPreference = "Stop"
$Magic = "RELAYPROXY_GUI1!"
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

$sizeBytes = [BitConverter]::GetBytes([UInt64]$payloadInfo.Length)
if (-not [BitConverter]::IsLittleEndian) { [Array]::Reverse($sizeBytes) }

$out = [System.IO.File]::Open($Output, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
try {
    $stub = [System.IO.File]::OpenRead($Launcher)
    try { $stub.CopyTo($out) } finally { $stub.Dispose() }

    $payloadStream = [System.IO.File]::OpenRead($Payload)
    try { $payloadStream.CopyTo($out) } finally { $payloadStream.Dispose() }

    $out.Write($MagicBytes, 0, $MagicBytes.Length)
    $out.Write($sizeBytes, 0, $sizeBytes.Length)
    $out.Write($hashBytes, 0, $hashBytes.Length)
}
finally {
    $out.Dispose()
}

$final = Get-Item -LiteralPath $Output
Write-Host "[launcher] stub: $([math]::Round((Get-Item $Launcher).Length / 1MB, 2)) MiB"
Write-Host "[launcher] WinUI payload: $([math]::Round($payloadInfo.Length / 1MB, 2)) MiB"
Write-Host "[launcher] final rename-safe EXE: $([math]::Round($final.Length / 1MB, 2)) MiB"
Write-Host "[launcher] payload sha256: $hashHex"
