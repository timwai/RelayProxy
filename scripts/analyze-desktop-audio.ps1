[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$Path,

    [switch]$RequireOpus,

    [double]$MaxLossPercent = -1,

    [int]$MaxQueueDrops = -1,

    [int]$MaxGapSkippedFrames = -1,

    [int]$MaxPlayoutTimeouts = -1,

    [double]$MinCompressionRatio = 0
)

$ErrorActionPreference = 'Stop'

function Get-Number {
    param(
        [object]$Value,
        [double]$Default = 0
    )
    if ($null -eq $Value) {
        return $Default
    }
    return [double]$Value
}

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
if ($schemaVersion -lt 5) {
    Write-Error "Diagnostics schema v5 or newer is required; file contains v$schemaVersion."
    exit 2
}

$audio = $report.audioValidation
if ($null -eq $audio) {
    Write-Error "The diagnostics report does not contain audioValidation. Audio may have been explicitly disabled."
    exit 2
}

$codec = [string]$audio.codec
$requestedCodec = [string]$audio.requestedCodec
$active = [bool]$audio.active
$targetBitrate = Get-Integer $audio.targetBitrate
$observedBitrate = Get-Integer $audio.observedPayloadBitrate
$rawBitrate = Get-Integer $audio.rawPcmBitrate
$compressionRatio = Get-Number $audio.observedCompressionRatio
$lossPercent = Get-Number $audio.estimatedNetworkLossPercent
$receivedFrames = Get-Integer $audio.receivedFrames
$consumedFrames = Get-Integer $audio.consumedFrames
$queueDrops = Get-Integer $audio.queueDroppedFrames
$concealmentFrames = Get-Integer $audio.concealmentFrames
$gapSkippedFrames = Get-Integer $audio.gapSkippedFrames
$reorderedFrames = Get-Integer $audio.reorderedFrames
$duplicateFrames = Get-Integer $audio.duplicateFrames
$lateFrames = Get-Integer $audio.lateFrames
$playoutTimeoutFrames = Get-Integer $audio.playoutTimeoutFrames
$maxQueueFrames = Get-Integer $audio.maxQueueFrames
$pcmFallbackSamples = Get-Integer $audio.pcmFallbackSamples

Write-Host ""
Write-Host "Relay Desktop Audio Validation"
Write-Host "=============================="
Write-Host ("File:                 {0}" -f (Resolve-Path -LiteralPath $Path))
Write-Host ("Schema:               v{0}" -f $schemaVersion)
Write-Host ("Requested codec:      {0}" -f $(if ($requestedCodec) { $requestedCodec } else { "(auto)" }))
Write-Host ("Active:               {0}" -f $active)
Write-Host ("Actual codec:         {0}" -f $(if ($codec) { $codec } else { "(none)" }))
Write-Host ("Format:               {0} Hz / {1} ch / {2} bit / {3} ms" -f (Get-Integer $audio.sampleRate), (Get-Integer $audio.channels), (Get-Integer $audio.bitsPerSample), (Get-Integer $audio.frameDurationMs))
Write-Host ("Target bitrate:       {0:N0} bps" -f $targetBitrate)
Write-Host ("Observed payload:     {0:N0} bps" -f $observedBitrate)
Write-Host ("Raw PCM bitrate:      {0:N0} bps" -f $rawBitrate)
Write-Host ("Compression ratio:    {0:N2}x" -f $compressionRatio)
Write-Host ("Estimated net loss:   {0:N2}%" -f $lossPercent)
Write-Host ("Frames recv/consume:  {0} / {1}" -f $receivedFrames, $consumedFrames)
Write-Host ("PLC concealments:     {0}" -f $concealmentFrames)
Write-Host ("Large-gap skipped:    {0}" -f $gapSkippedFrames)
Write-Host ("Queue drops/max:      {0} / {1}" -f $queueDrops, $maxQueueFrames)
Write-Host ("Reorder/dup/late:     {0} / {1} / {2}" -f $reorderedFrames, $duplicateFrames, $lateFrames)
Write-Host ("Playout timeouts:     {0}" -f $playoutTimeoutFrames)
Write-Host ("PCM fallback samples: {0}" -f $pcmFallbackSamples)
Write-Host ""

$failures = New-Object System.Collections.Generic.List[string]
if (-not $active) {
    $failures.Add("audio validation is not active")
}
if ($RequireOpus -and $codec -ne 'opus') {
    $failures.Add("Opus is required but actual codec is '$codec'")
}
if ($RequireOpus -and $pcmFallbackSamples -gt 0) {
    $failures.Add("Opus validation observed $pcmFallbackSamples PCM fallback sample(s)")
}
if ($MaxLossPercent -ge 0 -and $lossPercent -gt $MaxLossPercent) {
    $failures.Add(("estimated network loss {0:N2}% exceeds {1:N2}%" -f $lossPercent, $MaxLossPercent))
}
if ($MaxQueueDrops -ge 0 -and $queueDrops -gt $MaxQueueDrops) {
    $failures.Add("queue drops $queueDrops exceed $MaxQueueDrops")
}
if ($MaxGapSkippedFrames -ge 0 -and $gapSkippedFrames -gt $MaxGapSkippedFrames) {
    $failures.Add("large-gap skipped frames $gapSkippedFrames exceed $MaxGapSkippedFrames")
}
if ($MaxPlayoutTimeouts -ge 0 -and $playoutTimeoutFrames -gt $MaxPlayoutTimeouts) {
    $failures.Add("playout timeouts $playoutTimeoutFrames exceed $MaxPlayoutTimeouts")
}
if ($MinCompressionRatio -gt 0 -and $compressionRatio -lt $MinCompressionRatio) {
    $failures.Add(("compression ratio {0:N2}x is below {1:N2}x" -f $compressionRatio, $MinCompressionRatio))
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
