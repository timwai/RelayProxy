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
	DurationMs     int64                                        `json:"durationMs"`
	Reports        []desktopcodec.NVCodecH265444RoundTripReport `json:"reports,omitempty"`
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
