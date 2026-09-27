[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$Path,

    [switch]$RequireP2PObserved,

    [switch]$RequireRelayObserved,

    [switch]$RequireABRActivity,

    [double]$MaxP95RTTMs = -1,

    [double]$MaxP95JitterMs = -1,

    [double]$MaxP95LossPercent = -1,

    [double]$MaxP95QueueDelayMs = -1,

    [double]$MinP50ReceiveFPS = -1,

    [int]$MaxPathSwitches = -1,

    [int]$MaxResolutionChanges = -1,

    [int]$MaxGenerationChanges = -1,

    [long]$MaxDroppedFrames = -1,

    [double]$MaxPathSwitchesPerMinute = -1
)

$ErrorActionPreference = 'Stop'

function Get-Number {
    param([object]$Value, [double]$Default = 0)
    if ($null -eq $Value) { return $Default }
    return [double]$Value
}

function Get-Integer {
    param([object]$Value, [long]$Default = 0)
    if ($null -eq $Value) { return $Default }
    return [long]$Value
}

function Get-MapCount {
    param([object]$Map, [scriptblock]$Predicate)
    if ($null -eq $Map) { return 0 }
    $count = 0
    foreach ($property in $Map.PSObject.Properties) {
        if (& $Predicate ([string]$property.Name)) {
            $count += Get-Integer $property.Value
        }
    }
    return $count
}

function Format-Map {
    param([object]$Map)
    if ($null -eq $Map) { return '(none)' }
    $parts = @()
    foreach ($property in $Map.PSObject.Properties) {
        $parts += ("{0}={1}" -f $property.Name, (Get-Integer $property.Value))
    }
    if ($parts.Count -eq 0) { return '(none)' }
    return ($parts -join ', ')
}

function Format-Metric {
    param([object]$Metric, [string]$Unit = '')
    if ($null -eq $Metric -or (Get-Integer $Metric.samples) -eq 0) {
        return '(no samples)'
    }
    return ("p50={0:N2}{4} p95={1:N2}{4} avg={2:N2}{4} max={3:N2}{4}" -f
        (Get-Number $Metric.p50),
        (Get-Number $Metric.p95),
        (Get-Number $Metric.avg),
        (Get-Number $Metric.max),
        $Unit)
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
if ($schemaVersion -lt 2) {
    Write-Error "Diagnostics schema v2 or newer is required; file contains v$schemaVersion."
    exit 2
}

$summary = $report.summary
if ($null -eq $summary) {
    Write-Error 'Diagnostics report does not contain summary.'
    exit 2
}

$sampleCount = Get-Integer $summary.sampleCount
$sampleSpanMs = Get-Integer $summary.sampleSpanMs
$durationMs = Get-Integer $summary.sessionDurationMs
$pathSwitches = Get-Integer $summary.pathSwitches
$generationChanges = Get-Integer $summary.generationChanges
$abrChanges = Get-Integer $summary.abrChanges
$resolutionChanges = Get-Integer $summary.resolutionChanges
$droppedFrames = Get-Integer $summary.droppedFrames

$paths = $summary.paths
$p2pSamples = Get-MapCount $paths { param($name) $name -match 'p2p|direct' }
$relaySamples = Get-MapCount $paths { param($name) $name -match 'relay|quic' }

$spanMinutes = [Math]::Max(0.0, ([double]$sampleSpanMs / 60000.0))
$pathSwitchesPerMinute = 0.0
if ($spanMinutes -gt 0) {
    $pathSwitchesPerMinute = [double]$pathSwitches / $spanMinutes
}

Write-Host ''
Write-Host 'Relay Desktop Network / P2P / ABR Validation'
Write-Host '============================================'
Write-Host ("File:                    {0}" -f (Resolve-Path -LiteralPath $Path))
Write-Host ("Schema:                  v{0}" -f $schemaVersion)
Write-Host ("Samples:                 {0}" -f $sampleCount)
Write-Host ("Sample span:             {0:N1}s" -f ([double]$sampleSpanMs / 1000.0))
Write-Host ("Session duration:        {0:N1}s" -f ([double]$durationMs / 1000.0))
Write-Host ("Paths:                   {0}" -f (Format-Map $paths))
Write-Host ("P2P/direct samples:      {0}" -f $p2pSamples)
Write-Host ("Relay samples:           {0}" -f $relaySamples)
Write-Host ("Path switches:           {0} ({1:N2}/min)" -f $pathSwitches, $pathSwitchesPerMinute)
Write-Host ("ABR changes:             {0}" -f $abrChanges)
Write-Host ("ABR reasons:             {0}" -f (Format-Map $summary.abrReasons))
Write-Host ("Resolution changes:      {0}" -f $resolutionChanges)
Write-Host ("Generation changes:      {0}" -f $generationChanges)
Write-Host ("Dropped frames:          {0}" -f $droppedFrames)
Write-Host ("RTT:                     {0}" -f (Format-Metric $summary.rttMs ' ms'))
Write-Host ("Jitter:                  {0}" -f (Format-Metric $summary.jitterMs ' ms'))
Write-Host ("Loss:                    {0}" -f (Format-Metric $summary.lossPercent ' %'))
Write-Host ("Send queue:              {0}" -f (Format-Metric $summary.sendQueueDelayMs ' ms'))
Write-Host ("Receive FPS:             {0}" -f (Format-Metric $summary.receiveFps))
Write-Host ("Actual bitrate:          {0}" -f (Format-Metric $summary.actualBitrate ' bps'))
Write-Host ''

$failures = New-Object System.Collections.Generic.List[string]

if ($sampleCount -lt 1) {
    $failures.Add('diagnostics contains no media samples')
}
if ($RequireP2PObserved -and $p2pSamples -lt 1) {
    $failures.Add('P2P/direct media path was not observed')
}
if ($RequireRelayObserved -and $relaySamples -lt 1) {
    $failures.Add('Relay media path was not observed')
}
if ($RequireABRActivity -and $abrChanges -lt 1) {
    $failures.Add('ABR did not change media settings during this scenario')
}

$p95RTT = Get-Number $summary.rttMs.p95
$p95Jitter = Get-Number $summary.jitterMs.p95
$p95Loss = Get-Number $summary.lossPercent.p95
$p95Queue = Get-Number $summary.sendQueueDelayMs.p95
$p50FPS = Get-Number $summary.receiveFps.p50

if ($MaxP95RTTMs -ge 0 -and $p95RTT -gt $MaxP95RTTMs) {
    $failures.Add(("RTT p95 {0:N2} ms exceeds {1:N2} ms" -f $p95RTT, $MaxP95RTTMs))
}
if ($MaxP95JitterMs -ge 0 -and $p95Jitter -gt $MaxP95JitterMs) {
    $failures.Add(("jitter p95 {0:N2} ms exceeds {1:N2} ms" -f $p95Jitter, $MaxP95JitterMs))
}
if ($MaxP95LossPercent -ge 0 -and $p95Loss -gt $MaxP95LossPercent) {
    $failures.Add(("loss p95 {0:N2}% exceeds {1:N2}%" -f $p95Loss, $MaxP95LossPercent))
}
if ($MaxP95QueueDelayMs -ge 0 -and $p95Queue -gt $MaxP95QueueDelayMs) {
    $failures.Add(("send queue p95 {0:N2} ms exceeds {1:N2} ms" -f $p95Queue, $MaxP95QueueDelayMs))
}
if ($MinP50ReceiveFPS -ge 0 -and $p50FPS -lt $MinP50ReceiveFPS) {
    $failures.Add(("receive FPS p50 {0:N2} is below {1:N2}" -f $p50FPS, $MinP50ReceiveFPS))
}
if ($MaxPathSwitches -ge 0 -and $pathSwitches -gt $MaxPathSwitches) {
    $failures.Add("path switches $pathSwitches exceed $MaxPathSwitches")
}
if ($MaxResolutionChanges -ge 0 -and $resolutionChanges -gt $MaxResolutionChanges) {
    $failures.Add("resolution changes $resolutionChanges exceed $MaxResolutionChanges")
}
if ($MaxGenerationChanges -ge 0 -and $generationChanges -gt $MaxGenerationChanges) {
    $failures.Add("generation changes $generationChanges exceed $MaxGenerationChanges")
}
if ($MaxDroppedFrames -ge 0 -and $droppedFrames -gt $MaxDroppedFrames) {
    $failures.Add("dropped frames $droppedFrames exceed $MaxDroppedFrames")
}
if ($MaxPathSwitchesPerMinute -ge 0 -and $pathSwitchesPerMinute -gt $MaxPathSwitchesPerMinute) {
    $failures.Add(("path switch rate {0:N2}/min exceeds {1:N2}/min" -f $pathSwitchesPerMinute, $MaxPathSwitchesPerMinute))
}

if ($failures.Count -gt 0) {
    Write-Host 'RESULT: FAIL'
    foreach ($failure in $failures) {
        Write-Host (" - {0}" -f $failure)
    }
    exit 1
}

Write-Host 'RESULT: PASS'
exit 0
