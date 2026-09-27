[CmdletBinding()]
param(
    [ValidateSet('Prepare','Status','ArmNVENC','ArmNVDEC')]
    [string]$Action = 'Prepare',

    [string]$BaseUri = 'http://127.0.0.1:9090'
)

$ErrorActionPreference = 'Stop'
$BaseUri = $BaseUri.TrimEnd('/')

function Invoke-RelayApi {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [ValidateSet('GET','POST')][string]$Method = 'GET',
        [object]$Body = $null
    )

    $params = @{
        Uri = "$BaseUri$Path"
        Method = $Method
        TimeoutSec = 180
    }
    if ($null -ne $Body) {
        $params.ContentType = 'application/json'
        $params.Body = ($Body | ConvertTo-Json -Compress -Depth 8)
    }
    Invoke-RestMethod @params
}

function Write-Gate {
    param([object]$Report)

    $gate = $Report.nvcodecCanaryEligibility
    $validation = $Report.nvcodecValidation
    $stress = $Report.nvcodecStressQualification

    Write-Host ''
    Write-Host 'NVCodec Canary'
    Write-Host ("  requested : {0}" -f [bool]$gate.requested)
    Write-Host ("  eligible  : {0}" -f [bool]$gate.eligible)
    Write-Host ("  active    : {0}" -f [bool]$gate.active)
    Write-Host ("  tripped   : {0}" -f [bool]$gate.circuitTripped)
    Write-Host ("  qualify   : {0}/{1}" -f [int]$validation.qualificationPasses, [int]$validation.requiredPasses)
    Write-Host ("  stress    : {0}/{1}, passed={2}" -f [int]$gate.stressCompletedRounds, [int]$gate.stressRequiredRounds, [bool]$gate.stressPassed)

    if ($gate.circuitTripReason) {
        Write-Host ("  trip      : {0}" -f $gate.circuitTripReason)
    }
    if ($gate.reasons) {
        Write-Host ("  reasons   : {0}" -f (($gate.reasons | ForEach-Object { [string]$_ }) -join '; '))
    }
    if ($stress -and $stress.gpuMemoryObserved) {
        Write-Host ("  gpu peak  : {0} bytes" -f [uint64]$stress.maxGpuMemoryPeakBytes)
        Write-Host ("  gpu growth: {0} bytes" -f [uint64]$stress.maxGpuMemoryGrowthBytes)
    }
}

try {
    $null = Invoke-RelayApi -Path '/api/status'
    Write-Host ("RelayProxy Agent: {0}" -f $BaseUri)
} catch {
    throw "Cannot reach RelayProxy Agent Web at $BaseUri. Ensure web management is enabled and the Agent is running. $($_.Exception.Message)"
}

switch ($Action) {
    'Status' {
        $report = Invoke-RelayApi -Path '/api/remote-desktop/diagnostics'
        Write-Gate $report
        exit 0
    }

    'Prepare' {
        Write-Host 'Running NVIDIA 3/3 qualification...'
        $qualification = Invoke-RelayApi -Path '/api/remote-desktop/nvcodec-qualification' -Method POST
        Write-Host ("Qualification: passed={0}, attempts={1}, final={2}/{3}" -f
            [bool]$qualification.passed,
            [int]$qualification.attempts,
            [int]$qualification.finalPasses,
            [int]$qualification.requiredPasses)
        if (-not $qualification.passed) {
            if ($qualification.error) { Write-Host ("Error: {0}" -f $qualification.error) }
            exit 2
        }

        Write-Host 'Running NVIDIA 5-round stress qualification...'
        $stress = Invoke-RelayApi -Path '/api/remote-desktop/nvcodec-stress-qualification' -Method POST
        Write-Host ("Stress: passed={0}, completed={1}/{2}, cleanupFailures={3}" -f
            [bool]$stress.passed,
            [int]$stress.completedRounds,
            [int]$stress.requestedRounds,
            [int]$stress.cleanupFailures)
        if (-not $stress.passed) {
            if ($stress.error) { Write-Host ("Error: {0}" -f $stress.error) }
            exit 3
        }

        $report = Invoke-RelayApi -Path '/api/remote-desktop/diagnostics'
        Write-Gate $report
        if (-not $report.nvcodecCanaryEligibility.eligible) {
            Write-Host ''
            Write-Host 'Qualification completed, but the current build/GPU/driver is still not canary-eligible.'
            exit 4
        }

        Write-Host ''
        Write-Host 'Preparation passed.'
        if (-not $report.nvcodecCanaryEligibility.requested) {
            Write-Host 'Next: enable "NVIDIA NVCodec Canary" in Client Settings, then confirm ACTIVE.'
        } elseif (-not $report.nvcodecCanaryEligibility.active) {
            Write-Host 'Canary is requested but not active. Check the trip state/reasons above and restart if this process has already tripped.'
        } else {
            Write-Host 'Canary is ACTIVE. Continue with ArmNVENC / ArmNVDEC fault injection.'
        }
        exit 0
    }

    'ArmNVENC' {
        $fault = Invoke-RelayApi -Path '/api/remote-desktop/nvcodec-fault-injection' -Method POST -Body @{ stage = 'encode' }
        Write-Host ("NVENC one-shot fault armed. pendingStage={0}" -f $fault.pendingStage)
        Write-Host 'Keep an active HEVC 4:4:4 NVCodec session running; the next real NVENC encode will consume the fault.'
        exit 0
    }

    'ArmNVDEC' {
        $fault = Invoke-RelayApi -Path '/api/remote-desktop/nvcodec-fault-injection' -Method POST -Body @{ stage = 'decode' }
        Write-Host ("NVDEC one-shot fault armed. pendingStage={0}" -f $fault.pendingStage)
        Write-Host 'Keep the native viewer on an NVDEC session; the next real NVDEC decode will consume the fault.'
        exit 0
    }
}
