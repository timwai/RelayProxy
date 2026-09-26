package bridge

import (
	"errors"
	"fmt"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
)

type NVCodecQualificationBatchReport struct {
	Passed         bool                                         `json:"passed"`
	AlreadyCurrent bool                                         `json:"alreadyCurrent,omitempty"`
	Attempts       int                                          `json:"attempts"`
	InitialPasses  int                                          `json:"initialPasses"`
	FinalPasses    int                                          `json:"finalPasses"`
	RequiredPasses int                                          `json:"requiredPasses"`
	DurationMs              int64                                        `json:"durationMs"`
	GPUMemoryObserved       bool                                         `json:"gpuMemoryObserved"`
	MinGPUMemoryBudgetBytes uint64                                       `json:"minGpuMemoryBudgetBytes,omitempty"`
	MaxGPUMemoryPeakBytes   uint64                                       `json:"maxGpuMemoryPeakBytes,omitempty"`
	MaxGPUMemoryGrowthBytes uint64                                       `json:"maxGpuMemoryGrowthBytes,omitempty"`
	CleanupFailures         int                                          `json:"cleanupFailures,omitempty"`
	Reports                 []desktopcodec.NVCodecH265444RoundTripReport `json:"reports,omitempty"`
	Validation     NVCodecValidationStatus                      `json:"validation"`
	Error          string                                       `json:"error,omitempty"`
}

func (b *UIBridge) RunRemoteDesktopNVCodecQualification() (
	report NVCodecQualificationBatchReport,
	retErr error,
) {
	started := time.Now()
	report.RequiredPasses = nvcodecValidationRequiredPasses
	defer func() {
		report.DurationMs = time.Since(started).Milliseconds()
		if retErr != nil {
			report.Passed = false
			report.Error = retErr.Error()
		}
	}()

	if b == nil {
		return report, fmt.Errorf("Relay Desktop bridge is unavailable")
	}

	b.nvcodecValidationMu.Lock()
	defer b.nvcodecValidationMu.Unlock()

	initial := b.GetRemoteDesktopNVCodecValidation()
	report.InitialPasses = initial.QualificationPasses
	report.FinalPasses = initial.QualificationPasses
	report.Validation = initial
	if initial.Current {
		report.Passed = true
		report.AlreadyCurrent = true
		return report, nil
	}

	for attempt := 0; attempt < nvcodecValidationRequiredPasses; attempt++ {
		single, testErr, receiptErr := b.runRemoteDesktopNVCodecSelfTestLocked()
		report.Attempts++
		report.Reports = append(report.Reports, single)
		report.observeNVCodecRun(single)

		status := b.GetRemoteDesktopNVCodecValidation()
		report.FinalPasses = status.QualificationPasses
		report.Validation = status

		if testErr != nil || !single.Passed {
			if testErr == nil {
				testErr = fmt.Errorf("NVIDIA GPU self-test did not pass")
			}
			return report, errors.Join(testErr, receiptErr)
		}
		if receiptErr != nil {
			return report, fmt.Errorf(
				"NVIDIA GPU self-test passed but qualification receipt update failed: %w",
				receiptErr,
			)
		}
		if status.Current {
			report.Passed = true
			return report, nil
		}

		// Reaching the threshold but still not becoming current means the final
		// identity confirmation failed or another fail-closed condition applies.
		// Repeating the expensive round trip cannot repair that condition.
		if status.QualificationPasses >= nvcodecValidationRequiredPasses {
			reason := status.StaleReason
			if reason == "" {
				reason = "NVIDIA qualification did not become current"
			}
			return report, fmt.Errorf("%s", reason)
		}
	}

	reason := report.Validation.StaleReason
	if reason == "" {
		reason = fmt.Sprintf(
			"NVIDIA qualification remained %d/%d after %d attempts",
			report.FinalPasses,
			report.RequiredPasses,
			report.Attempts,
		)
	}
	return report, fmt.Errorf("%s", reason)
}


func (report *NVCodecQualificationBatchReport) observeNVCodecRun(
	single desktopcodec.NVCodecH265444RoundTripReport,
) {
	if report == nil {
		return
	}
	if !single.CleanupValidated {
		report.CleanupFailures++
	}
	if !single.GPUMemoryObserved {
		return
	}
	report.GPUMemoryObserved = true
	if single.GPUMemoryBudgetBytes > 0 &&
		(report.MinGPUMemoryBudgetBytes == 0 || single.GPUMemoryBudgetBytes < report.MinGPUMemoryBudgetBytes) {
		report.MinGPUMemoryBudgetBytes = single.GPUMemoryBudgetBytes
	}
	if single.GPUMemoryPeakBytes > report.MaxGPUMemoryPeakBytes {
		report.MaxGPUMemoryPeakBytes = single.GPUMemoryPeakBytes
	}
	growth := single.GPUMemoryGrowthBytes
	var absoluteGrowth uint64
	if growth < 0 {
		absoluteGrowth = uint64(-growth)
	} else {
		absoluteGrowth = uint64(growth)
	}
	if absoluteGrowth > report.MaxGPUMemoryGrowthBytes {
		report.MaxGPUMemoryGrowthBytes = absoluteGrowth
	}
}


const nvcodecStressQualificationRounds = 5

type NVCodecStressMemorySample struct {
	Round            int    `json:"round"`
	Passed           bool   `json:"passed"`
	CleanupValidated bool   `json:"cleanupValidated"`
	BudgetBytes      uint64 `json:"budgetBytes,omitempty"`
	BeforeBytes      uint64 `json:"beforeBytes,omitempty"`
	PeakBytes        uint64 `json:"peakBytes,omitempty"`
	AfterBytes       uint64 `json:"afterBytes,omitempty"`
	GrowthBytes      int64  `json:"growthBytes,omitempty"`
	DurationMs       int64  `json:"durationMs"`
}

type NVCodecStressQualificationReport struct {
	Passed                  bool                          `json:"passed"`
	RequestedRounds         int                           `json:"requestedRounds"`
	CompletedRounds         int                           `json:"completedRounds"`
	DurationMs              int64                         `json:"durationMs"`
	GPUMemoryObserved       bool                          `json:"gpuMemoryObserved"`
	MinGPUMemoryBudgetBytes uint64                        `json:"minGpuMemoryBudgetBytes,omitempty"`
	MaxGPUMemoryPeakBytes   uint64                        `json:"maxGpuMemoryPeakBytes,omitempty"`
	MaxGPUMemoryGrowthBytes uint64                        `json:"maxGpuMemoryGrowthBytes,omitempty"`
	CleanupFailures         int                           `json:"cleanupFailures,omitempty"`
	MemoryTrend             []NVCodecStressMemorySample   `json:"memoryTrend,omitempty"`
	Reports                 []desktopcodec.NVCodecH265444RoundTripReport `json:"reports,omitempty"`
	Validation              NVCodecValidationStatus       `json:"validation"`
	Error                   string                        `json:"error,omitempty"`
}

func (b *UIBridge) RunRemoteDesktopNVCodecStressQualification() (
	report NVCodecStressQualificationReport,
	retErr error,
) {
	started := time.Now()
	report.RequestedRounds = nvcodecStressQualificationRounds
	defer func() {
		report.DurationMs = time.Since(started).Milliseconds()
		if retErr != nil {
			report.Passed = false
			report.Error = retErr.Error()
		}
	}()

	if b == nil {
		return report, fmt.Errorf("Relay Desktop bridge is unavailable")
	}

	b.nvcodecValidationMu.Lock()
	defer b.nvcodecValidationMu.Unlock()

	status := b.GetRemoteDesktopNVCodecValidation()
	report.Validation = status
	if !status.Current {
		reason := status.StaleReason
		if reason == "" {
			reason = "NVIDIA qualification is not current"
		}
		return report, fmt.Errorf("NVIDIA stress qualification requires current 3/3 validation: %s", reason)
	}

	for round := 1; round <= nvcodecStressQualificationRounds; round++ {
		single, testErr, receiptErr := b.runRemoteDesktopNVCodecSelfTestLocked()
		report.CompletedRounds++
		report.Reports = append(report.Reports, single)
		report.observeNVCodecStressRun(round, single)

		status = b.GetRemoteDesktopNVCodecValidation()
		report.Validation = status

		if testErr != nil || !single.Passed {
			if testErr == nil {
				testErr = fmt.Errorf("NVIDIA GPU stress self-test did not pass")
			}
			return report, errors.Join(testErr, receiptErr)
		}
		if receiptErr != nil {
			return report, fmt.Errorf(
				"NVIDIA GPU stress self-test passed but validation receipt update failed: %w",
				receiptErr,
			)
		}
		if !status.Current {
			reason := status.StaleReason
			if reason == "" {
				reason = "NVIDIA validation became non-current during stress qualification"
			}
			return report, fmt.Errorf("%s", reason)
		}
	}

	report.Passed = true
	return report, nil
}

func (report *NVCodecStressQualificationReport) observeNVCodecStressRun(
	round int,
	single desktopcodec.NVCodecH265444RoundTripReport,
) {
	if report == nil {
		return
	}
	sample := NVCodecStressMemorySample{
		Round:            round,
		Passed:           single.Passed,
		CleanupValidated: single.CleanupValidated,
		BudgetBytes:      single.GPUMemoryBudgetBytes,
		BeforeBytes:      single.GPUMemoryBeforeBytes,
		PeakBytes:        single.GPUMemoryPeakBytes,
		AfterBytes:       single.GPUMemoryAfterBytes,
		GrowthBytes:      single.GPUMemoryGrowthBytes,
		DurationMs:       single.DurationMs,
	}
	report.MemoryTrend = append(report.MemoryTrend, sample)
	if !single.CleanupValidated {
		report.CleanupFailures++
	}
	if !single.GPUMemoryObserved {
		return
	}
	report.GPUMemoryObserved = true
	if single.GPUMemoryBudgetBytes > 0 &&
		(report.MinGPUMemoryBudgetBytes == 0 || single.GPUMemoryBudgetBytes < report.MinGPUMemoryBudgetBytes) {
		report.MinGPUMemoryBudgetBytes = single.GPUMemoryBudgetBytes
	}
	if single.GPUMemoryPeakBytes > report.MaxGPUMemoryPeakBytes {
		report.MaxGPUMemoryPeakBytes = single.GPUMemoryPeakBytes
	}
	growth := single.GPUMemoryGrowthBytes
	var absoluteGrowth uint64
	if growth < 0 {
		absoluteGrowth = uint64(-growth)
	} else {
		absoluteGrowth = uint64(growth)
	}
	if absoluteGrowth > report.MaxGPUMemoryGrowthBytes {
		report.MaxGPUMemoryGrowthBytes = absoluteGrowth
	}
}
