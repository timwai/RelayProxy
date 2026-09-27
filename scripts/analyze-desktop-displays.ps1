[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$Path,

    [string]$BeforePath = '',

    [switch]$RequireMultipleDisplays,

    [switch]$RequireNegativeCoordinates,

    [switch]$RequireMixedDPI,

    [switch]$RequireLiveTopology,

    [switch]$RequireMultipleSessions,

    [switch]$RequireUniqueSessionDisplays,

    [switch]$RequireTopologyChanged
)

$ErrorActionPreference = 'Stop'

function Read-Report {
    param([Parameter(Mandatory = $true)][string]$ReportPath)
    if (-not (Test-Path -LiteralPath $ReportPath -PathType Leaf)) {
        throw "Diagnostics file not found: $ReportPath"
    }
    try {
        return Get-Content -LiteralPath $ReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
    } catch {
        throw "Failed to read diagnostics JSON '$ReportPath': $($_.Exception.Message)"
    }
}

function Get-DisplayFingerprint {
    param([object[]]$Displays)
    if ($null -eq $Displays) {
        return ''
    }
    $items = @()
    foreach ($display in @($Displays)) {
        $items += ("{0}|{1}|{2}|{3}|{4}|{5}|{6}" -f
            [string]$display.name,
            [int]$display.x,
            [int]$display.y,
            [int]$display.width,
            [int]$display.height,
            [int]$display.dpi,
            [double]$display.scale)
    }
    return (($items | Sort-Object) -join ';')
}

function Get-TopologyDisplays {
    param([object]$Report)
    $sessions = @($Report.desktopSessions)
    $ready = @($sessions | Where-Object { [bool]$_.displaysReady -and $null -ne $_.displays })
    if ($ready.Count -gt 0) {
        return @($ready[0].displays)
    }
    if ($sessions.Count -gt 0 -and $null -ne $sessions[0].displays) {
        return @($sessions[0].displays)
    }
    return @()
}

function Write-Displays {
    param([object[]]$Displays)
    if ($Displays.Count -eq 0) {
        Write-Host '  (none)'
        return
    }
    foreach ($display in $Displays) {
        Write-Host ("  {0} id={1} pos=({2},{3}) size={4}x{5} dpi={6} scale={7:N2} primary={8}" -f
            $(if ($display.name) { [string]$display.name } else { '(unnamed)' }),
            [string]$display.id,
            [int]$display.x,
            [int]$display.y,
            [int]$display.width,
            [int]$display.height,
            [int]$display.dpi,
            [double]$display.scale,
            [bool]$display.primary)
    }
}

$report = Read-Report $Path
$sessions = @($report.desktopSessions)
$displays = Get-TopologyDisplays $report

Write-Host ''
Write-Host 'Relay Desktop Multi-Display Validation'
Write-Host '======================================'
Write-Host ("File:             {0}" -f (Resolve-Path -LiteralPath $Path))
Write-Host ("Sessions:         {0}" -f $sessions.Count)
Write-Host ("Displays:         {0}" -f $displays.Count)
Write-Host 'Topology:'
Write-Displays $displays
Write-Host ''

if ($sessions.Count -gt 0) {
    Write-Host 'Active sessions:'
    foreach ($session in $sessions) {
        Write-Host ("  session={0} display={1} name={2} ready={3} path={4}" -f
            [string]$session.sessionId,
            $(if ($session.displayId) { [string]$session.displayId } else { '(virtual/all)' }),
            $(if ($session.displayName) { [string]$session.displayName } else { '(none)' }),
            [bool]$session.displaysReady,
            [string]$session.pathUdp)
    }
    Write-Host ''
}

$negative = @($displays | Where-Object { [int]$_.x -lt 0 -or [int]$_.y -lt 0 })
$dpis = @($displays | ForEach-Object { [int]$_.dpi } | Where-Object { $_ -gt 0 } | Sort-Object -Unique)
$scales = @($displays | ForEach-Object { [double]$_.scale } | Where-Object { $_ -gt 0 } | Sort-Object -Unique)
$liveSessions = @($sessions | Where-Object { [bool]$_.displaysReady })
$displayBoundSessions = @($sessions | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_.displayId) })
$uniqueSessionDisplayIDs = @($displayBoundSessions | ForEach-Object { [string]$_.displayId } | Sort-Object -Unique)

Write-Host ("Negative-coordinate displays: {0}" -f $negative.Count)
Write-Host ("Distinct DPI values:           {0}" -f $(if ($dpis.Count) { $dpis -join ', ' } else { '(none)' }))
Write-Host ("Distinct scale values:         {0}" -f $(if ($scales.Count) { ($scales | ForEach-Object { '{0:N2}' -f $_ }) -join ', ' } else { '(none)' }))
Write-Host ("Live-topology sessions:        {0}/{1}" -f $liveSessions.Count, $sessions.Count)

$topologyChanged = $false
if (-not [string]::IsNullOrWhiteSpace($BeforePath)) {
    $before = Read-Report $BeforePath
    $beforeDisplays = Get-TopologyDisplays $before
    $beforeFingerprint = Get-DisplayFingerprint $beforeDisplays
    $afterFingerprint = Get-DisplayFingerprint $displays
    $topologyChanged = $beforeFingerprint -ne $afterFingerprint

    Write-Host ''
    Write-Host ("Before file:       {0}" -f (Resolve-Path -LiteralPath $BeforePath))
    Write-Host ("Before displays:   {0}" -f $beforeDisplays.Count)
    Write-Host ("Topology changed:  {0}" -f $topologyChanged)
    if ($topologyChanged) {
        Write-Host 'Before topology:'
        Write-Displays $beforeDisplays
        Write-Host 'After topology:'
        Write-Displays $displays
    }
}

$failures = New-Object System.Collections.Generic.List[string]

if ($RequireMultipleDisplays -and $displays.Count -lt 2) {
    $failures.Add("expected at least 2 displays, observed $($displays.Count)")
}
if ($RequireNegativeCoordinates -and $negative.Count -lt 1) {
    $failures.Add('expected at least one display with a negative X or Y coordinate')
}
if ($RequireMixedDPI -and $dpis.Count -lt 2 -and $scales.Count -lt 2) {
    $failures.Add('expected mixed DPI/scale values across displays')
}
if ($RequireLiveTopology) {
    if ($sessions.Count -lt 1) {
        $failures.Add('no active Relay Desktop sessions were exported')
    } elseif ($liveSessions.Count -ne $sessions.Count) {
        $failures.Add("only $($liveSessions.Count)/$($sessions.Count) sessions reported displaysReady=true")
    }
}
if ($RequireMultipleSessions -and $sessions.Count -lt 2) {
    $failures.Add("expected at least 2 active desktop sessions, observed $($sessions.Count)")
}
if ($RequireUniqueSessionDisplays) {
    if ($displayBoundSessions.Count -lt 2) {
        $failures.Add('at least 2 display-bound sessions are required to validate independent multi-window routing')
    } elseif ($uniqueSessionDisplayIDs.Count -ne $displayBoundSessions.Count) {
        $failures.Add("display-bound sessions are not unique: $($displayBoundSessions.Count) sessions use $($uniqueSessionDisplayIDs.Count) unique DisplayID(s)")
    }
}
if ($RequireTopologyChanged) {
    if ([string]::IsNullOrWhiteSpace($BeforePath)) {
        $failures.Add('-RequireTopologyChanged requires -BeforePath')
    } elseif (-not $topologyChanged) {
        $failures.Add('display topology did not change between before/after diagnostics')
    }
}

if ($failures.Count -gt 0) {
    Write-Host ''
    Write-Host 'RESULT: FAIL'
    foreach ($failure in $failures) {
        Write-Host (" - {0}" -f $failure)
    }
    exit 1
}

Write-Host ''
Write-Host 'RESULT: PASS'
exit 0
