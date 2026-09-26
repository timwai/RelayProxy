package bridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
)

type NVCodecQualificationMemorySample struct {
	Attempt          int                                  `json:"attempt"`
	Before           *desktopcodec.NVCodecVideoMemoryInfo `json:"before,omitempty"`
	After            *desktopcodec.NVCodecVideoMemoryInfo `json:"after,omitempty"`
	UsageDeltaBytes  int64                                `json:"usageDeltaBytes,omitempty"`
	BudgetDeltaBytes int64                                `json:"budgetDeltaBytes,omitempty"`
	Error            string                               `json:"error,omitempty"`
}

type NVCodecQualificationBatchReport struct {
	Passed                       bool                                         `json:"passed"`
	AlreadyCurrent               bool                                         `json:"alreadyCurrent,omitempty"`
	Attempts                     int                                          `json:"attempts"`
	InitialPasses                int                                          `json:"initialPasses"`
	FinalPasses                  int                                          `json:"finalPasses"`
	RequiredPasses               int                                          `json:"requiredPasses"`
	DurationMs                   int64                                        `json:"durationMs"`
	Reports                      []desktopcodec.NVCodecH265444RoundTripReport `json:"reports,omitempty"`
	Validation                   NVCodecValidationStatus                      `json:"validation"`
	MemoryObservationAvailable   bool                                         `json:"memoryObservationAvailable,omitempty"`
	InitialVideoMemory           *desktopcodec.NVCodecVideoMemoryInfo         `json:"initialVideoMemory,omitempty"`
	FinalVideoMemory             *desktopcodec.NVCodecVideoMemoryInfo         `json:"finalVideoMemory,omitempty"`
	MemorySamples                []NVCodecQualificationMemorySample            `json:"memorySamples,omitempty"`
	NetUsageDeltaBytes           int64                                        `json:"netUsageDeltaBytes,omitempty"`
	PeakUsageDeltaBytes          int64                                        `json:"peakUsageDeltaBytes,omitempty"`
	MaxAttemptUsageDeltaBytes    int64                                        `json:"maxAttemptUsageDeltaBytes,omitempty"`
	Error                        string                                       `json:"error,omitempty"`
}

func signedNVCodecByteDelta(after, before uint64) int64 {
	if after >= before {
		delta := after - before
		if delta > uint64(math.MaxInt64) {
			return math.MaxInt64
		}
		return int64(delta)
	}
	delta := before - after
	if delta > uint64(math.MaxInt64) {
		return math.MinInt64
	}
	return -int64(delta)
}

func sameNVCodecVideoMemoryIdentity(
	a desktopcodec.NVCodecValidationIdentity,
	b desktopcodec.NVCodecValidationIdentity,
) bool {
	return a.AdapterVendorID != 0 &&
		a.AdapterVendorID == b.AdapterVendorID &&
		a.AdapterDeviceID == b.AdapterDeviceID &&
		a.AdapterSubSysID == b.AdapterSubSysID &&
		a.AdapterRevision == b.AdapterRevision &&
		a.AdapterDriverVersion != 0 &&
		a.AdapterDriverVersion == b.AdapterDriverVersion
}

func probeNVCodecVideoMemoryForQualification() (*desktopcodec.NVCodecVideoMemoryInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := desktopcodec.ProbeNVCodecVideoMemory(ctx)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func observeNVCodecQualificationMemory(
	attempt int,
	before *desktopcodec.NVCodecVideoMemoryInfo,
	beforeErr error,
	after *desktopcodec.NVCodecVideoMemoryInfo,
	afterErr error,
) NVCodecQualificationMemorySample {
	sample := NVCodecQualificationMemorySample{
		Attempt: attempt,
		Before:  before,
		After:   after,
	}
	if beforeErr != nil || afterErr != nil {
		sample.Error = errors.Join(beforeErr, afterErr).Error()
		return sample
	}
	if before == nil || after == nil {
		sample.Error = "NVIDIA video-memory telemetry returned an incomplete sample"
		return sample
	}
	if !sameNVCodecVideoMemoryIdentity(before.Identity, after.Identity) {
		sample.Error = "NVIDIA video-memory telemetry adapter/driver identity changed during qualification attempt"
		return sample
	}
	sample.UsageDeltaBytes = signedNVCodecByteDelta(after.CurrentUsageBytes, before.CurrentUsageBytes)
	sample.BudgetDeltaBytes = signedNVCodecByteDelta(after.BudgetBytes, before.BudgetBytes)
	return sample
}

func updateNVCodecQualificationMemorySummary(
	report *NVCodecQualificationBatchReport,
	sample NVCodecQualificationMemorySample,
) {
	if report == nil {
		return
	}
	report.MemorySamples = append(report.MemorySamples, sample)
	if sample.Error != "" || sample.Before == nil || sample.After == nil {
		return
	}
	if report.InitialVideoMemory == nil {
		initial := *sample.Before
		report.InitialVideoMemory = &initial
	}
	final := *sample.After
	report.FinalVideoMemory = &final
	report.MemoryObservationAvailable = true
	if sample.UsageDeltaBytes > report.MaxAttemptUsageDeltaBytes {
		report.MaxAttemptUsageDeltaBytes = sample.UsageDeltaBytes
	}
	if report.InitialVideoMemory != nil &&
		sameNVCodecVideoMemoryIdentity(report.InitialVideoMemory.Identity, sample.After.Identity) {
		delta := signedNVCodecByteDelta(
			sample.After.CurrentUsageBytes,
			report.InitialVideoMemory.CurrentUsageBytes,
		)
		report.NetUsageDeltaBytes = delta
		if delta > report.PeakUsageDeltaBytes {
			report.PeakUsageDeltaBytes = delta
		}
	}
}

func cloneNVCodecQualificationBatchReport(
	report NVCodecQualificationBatchReport,
) NVCodecQualificationBatchReport {
	clone := report
	clone.Reports = append(
		[]desktopcodec.NVCodecH265444RoundTripReport(nil),
		report.Reports...,
	)
	clone.MemorySamples = append(
		[]NVCodecQualificationMemorySample(nil),
		report.MemorySamples...,
	)
	return clone
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
		if b != nil {
			stored := cloneNVCodecQualificationBatchReport(report)
			b.mu.Lock()
			b.nvcodecQualification = &stored
			b.mu.Unlock()
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
		beforeMemory, beforeMemoryErr := probeNVCodecVideoMemoryForQualification()
		single, testErr, receiptErr := b.runRemoteDesktopNVCodecSelfTestLocked()
		afterMemory, afterMemoryErr := probeNVCodecVideoMemoryForQualification()

		report.Attempts++
		report.Reports = append(report.Reports, single)
		memorySample := observeNVCodecQualificationMemory(
			report.Attempts,
			beforeMemory,
			beforeMemoryErr,
			afterMemory,
			afterMemoryErr,
		)
		updateNVCodecQualificationMemorySummary(&report, memorySample)

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
