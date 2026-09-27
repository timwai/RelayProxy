[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$Path,

    [switch]$RequireIntelOneVPL,

    [switch]$RequireTargetAYUV,

    [switch]$RequireEndToEndZeroCopy,

    [switch]$RequireNoFallback,

    [int]$MinMatchingSamples = 1,

    [int]$MinEndToEndSamples = 1
)

$ErrorActionPreference = 'Stop'

function Get-Integer {
    param(
        [object]$Value,
        [long]$Default = 0
    )
    if ($null -eq $Value) {
        return $Default
    }
    return [long]$Value
}

function Get-MapCount {
    param(
        [object]$Map,
        [string]$Name
    )
    if ($null -eq $Map -or [string]::IsNullOrWhiteSpace($Name)) {
        return 0
    }
    $property = $Map.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return 0
    }
    return Get-Integer $property.Value
}

function Format-Map {
    param([object]$Map)
    if ($null -eq $Map) {
        return '(none)'
    }
    $parts = @()
    foreach ($property in $Map.PSObject.Properties) {
        $parts += ("{0}={1}" -f $property.Name, (Get-Integer $property.Value))
    }
    if ($parts.Count -eq 0) {
        return '(none)'
    }
    return ($parts -join ', ')
}

if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
    Write-Error "Diagnostics file not found: $Path"
    exit 2
}

try {
    $report = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
} catch {
    Write-Error "Failed to read diagnostics JSON: $($_.Exception.Message)"
    exit 2
}

$schemaVersion = Get-Integer $report.schemaVersion
if ($schemaVersion -lt 8) {
    Write-Error "Diagnostics schema v8 or newer is required; file contains v$schemaVersion."
    exit 2
}

$gpu = $report.gpuValidation
if ($null -eq $gpu) {
    Write-Error "The diagnostics report does not contain gpuValidation. Run an active Relay Desktop HEVC 4:4:4 session before exporting diagnostics."
    exit 2
}

$target = $report.targetGpu
$expectedFormat = ([string]$gpu.expectedFormat).ToLowerInvariant()
$targetAdvertised = [bool]$gpu.targetAdvertised
$matchingSamples = Get-Integer $gpu.matchingSamples
$hostZeroCopy = Get-Integer $gpu.hostEncodeZeroCopySamples
$decodeZeroCopy = Get-Integer $gpu.viewerDecodeZeroCopySamples
$displayZeroCopy = Get-Integer $gpu.viewerDisplayZeroCopySamples
$endToEndZeroCopy = Get-Integer $gpu.endToEndZeroCopySamples
$fallbackSamples = Get-Integer $gpu.fallbackSamples

$encoderBackends = $gpu.encoderBackends
$decoderBackends = $gpu.decoderBackends
$renderBackends = $gpu.renderBackends
$captureFormats = $gpu.captureFormats

$oneVPLEncoder = Get-MapCount $encoderBackends 'onevpl-hevc444-d3d11-zero-copy'
$oneVPLDecoder = Get-MapCount $decoderBackends 'onevpl-hevc444-d3d11-zero-copy'
$d3d11RenderSamples = 0
if ($null -ne $renderBackends) {
    foreach ($property in $renderBackends.PSObject.Properties) {
        if ($property.Name -match 'd3d11.*zero-copy|zero-copy.*d3d11') {
            $d3d11RenderSamples += Get-Integer $property.Value
        }
    }
}

$targetFormats = @()
if ($null -ne $target -and $null -ne $target.formats) {
    $targetFormats = @($target.formats | ForEach-Object { ([string]$_).ToLowerInvariant() })
}
$targetHasAYUV = $targetFormats -contains 'ayuv'
$targetEncodeZeroCopy = [bool]($null -ne $target -and $target.encodeZeroCopy)
$targetDecodeZeroCopy = [bool]($null -ne $target -and $target.decodeZeroCopy)
$targetDisplayZeroCopy = [bool]($null -ne $target -and $target.displayZeroCopy)

Write-Host ""
Write-Host "Relay Desktop GPU Validation"
Write-Host "============================"
Write-Host ("File:                     {0}" -f (Resolve-Path -LiteralPath $Path))
Write-Host ("Schema:                   v{0}" -f $schemaVersion)
Write-Host ("Expected format:          {0}" -f $(if ($expectedFormat) { $expectedFormat } else { '(none)' }))
Write-Host ("Target advertised:        {0}" -f $targetAdvertised)
Write-Host ("Target formats:           {0}" -f $(if ($targetFormats.Count) { $targetFormats -join ', ' } else { '(none)' }))
Write-Host ("Target E/D/R zero-copy:   {0}/{1}/{2}" -f $targetEncodeZeroCopy, $targetDecodeZeroCopy, $targetDisplayZeroCopy)
Write-Host ("Matching samples:         {0}" -f $matchingSamples)
Write-Host ("Host zero-copy samples:   {0}" -f $hostZeroCopy)
Write-Host ("Decode zero-copy samples: {0}" -f $decodeZeroCopy)
Write-Host ("Display zero-copy samples:{0}" -f $displayZeroCopy)
Write-Host ("End-to-end zero-copy:     {0}" -f $endToEndZeroCopy)
Write-Host ("Fallback samples:         {0}" -f $fallbackSamples)
Write-Host ("Capture formats:          {0}" -f (Format-Map $captureFormats))
Write-Host ("Encoder backends:         {0}" -f (Format-Map $encoderBackends))
Write-Host ("Decoder backends:         {0}" -f (Format-Map $decoderBackends))
Write-Host ("Render backends:          {0}" -f (Format-Map $renderBackends))
Write-Host ""

$failures = New-Object System.Collections.Generic.List[string]

if ($matchingSamples -lt $MinMatchingSamples) {
    $failures.Add("matching GPU samples $matchingSamples are below required minimum $MinMatchingSamples")
}

if ($RequireTargetAYUV) {
    if ($expectedFormat -ne 'ayuv') {
        $failures.Add("expected format is '$expectedFormat', not AYUV")
    }
    if (-not $targetAdvertised -or -not $targetHasAYUV) {
        $failures.Add("target did not advertise AYUV")
    }
    if (-not $targetEncodeZeroCopy -or -not $targetDecodeZeroCopy -or -not $targetDisplayZeroCopy) {
        $failures.Add("target AYUV capability does not advertise encode/decode/display zero-copy")
    }
}

if ($RequireIntelOneVPL) {
    if ($oneVPLEncoder -lt 1) {
        $failures.Add("oneVPL D3D11 zero-copy encoder was not observed")
    }
    if ($oneVPLDecoder -lt 1) {
        $failures.Add("oneVPL D3D11 zero-copy decoder was not observed")
    }
    if ($d3d11RenderSamples -lt 1) {
        $failures.Add("D3D11 zero-copy renderer was not observed")
    }
}

if ($RequireEndToEndZeroCopy) {
    if ($endToEndZeroCopy -lt $MinEndToEndSamples) {
        $failures.Add("end-to-end zero-copy samples $endToEndZeroCopy are below required minimum $MinEndToEndSamples")
    }
    if ($hostZeroCopy -lt $MinEndToEndSamples) {
        $failures.Add("host encode zero-copy samples $hostZeroCopy are below required minimum $MinEndToEndSamples")
    }
    if ($decodeZeroCopy -lt $MinEndToEndSamples) {
        $failures.Add("viewer decode zero-copy samples $decodeZeroCopy are below required minimum $MinEndToEndSamples")
    }
    if ($displayZeroCopy -lt $MinEndToEndSamples) {
        $failures.Add("viewer display zero-copy samples $displayZeroCopy are below required minimum $MinEndToEndSamples")
    }
}

if ($RequireNoFallback -and $fallbackSamples -gt 0) {
    $failures.Add("observed $fallbackSamples fallback sample(s)")
}

if ($failures.Count -gt 0) {
    Write-Host "RESULT: FAIL"
    foreach ($failure in $failures) {
        Write-Host (" - {0}" -f $failure)
    }
    exit 1
}

Write-Host "RESULT: PASS"
exit 0
