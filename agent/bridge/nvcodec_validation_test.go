package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
)

func validNVCodecReportForTest(now time.Time) desktopcodec.NVCodecH265444RoundTripReport {
	return desktopcodec.NVCodecH265444RoundTripReport{
		Passed:               true,
		Backend:              "nvcodec-hevc444",
		Adapter:              "NVIDIA Test",
		AdapterVendorID:      0x10de,
		AdapterDeviceID:      0x2684,
		AdapterSubSysID:      0x12345678,
		AdapterRevision:      0xa1,
		AdapterDriverVersion: 0x0001000200030004,
		ValidatedAtUnixMs:    now.Add(-time.Hour).UnixMilli(),
	}
}

func validNVCodecReceiptForTest(now time.Time) (*NVCodecValidationReceipt, desktopcodec.NVCodecValidationIdentity) {
	report := validNVCodecReportForTest(now)
	receipt := &NVCodecValidationReceipt{
		SchemaVersion:       nvcodecValidationReceiptSchema,
		BuildRevision:       "0123456789abcdef",
		SavedAtUnixMs:       now.Add(-time.Hour).UnixMilli(),
		QualificationPasses: nvcodecValidationRequiredPasses,
		LastAttemptPassed:   true,
		LastAttemptAtUnixMs: now.Add(-time.Hour).UnixMilli(),
		Report:              report,
	}
	identity := nvcodecValidationReportIdentity(report)
	return receipt, identity
}

func TestNextNVCodecValidationReceiptRequiresThreeMatchingPasses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	build := "0123456789abcdef"
	report := validNVCodecReportForTest(now)

	var receipt *NVCodecValidationReceipt
	for pass := 1; pass <= nvcodecValidationRequiredPasses; pass++ {
		next, err := nextNVCodecValidationReceipt(
			now.Add(time.Duration(pass)*time.Minute),
			build,
			receipt,
			report,
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if next.QualificationPasses != pass {
			t.Fatalf("pass %d qualification=%d", pass, next.QualificationPasses)
		}
		if !next.LastAttemptPassed || next.LastFailure != "" {
			t.Fatalf("pass %d receipt=%+v", pass, next)
		}
		receipt = next
	}

	identity := nvcodecValidationReportIdentity(report)
	status := evaluateNVCodecValidationReceipt(
		now.Add(4*time.Minute),
		build,
		identity,
		nil,
		receipt,
	)
	if !status.Current {
		t.Fatalf("three matching passes did not qualify: %+v", status)
	}
}

func TestNextNVCodecValidationReceiptFailureResetsQualification(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	build := "0123456789abcdef"
	report := validNVCodecReportForTest(now)
	previous := &NVCodecValidationReceipt{
		SchemaVersion:       nvcodecValidationReceiptSchema,
		BuildRevision:       build,
		SavedAtUnixMs:       now.Add(-time.Minute).UnixMilli(),
		QualificationPasses: 2,
		LastAttemptPassed:   true,
		LastAttemptAtUnixMs: now.Add(-time.Minute).UnixMilli(),
		Report:              report,
	}

	failed, err := nextNVCodecValidationReceipt(
		now,
		build,
		previous,
		desktopcodec.NVCodecH265444RoundTripReport{
			Backend: "nvcodec-hevc444",
			Error:   "decode mismatch",
		},
		errors.New("decode mismatch"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if failed.QualificationPasses != 0 || failed.LastAttemptPassed {
		t.Fatalf("failure did not reset qualification: %+v", failed)
	}
	if !failed.Report.Passed {
		t.Fatalf("failure discarded previous successful evidence: %+v", failed)
	}
	if !strings.Contains(failed.LastFailure, "decode mismatch") {
		t.Fatalf("failure reason=%q", failed.LastFailure)
	}

	restarted, err := nextNVCodecValidationReceipt(
		now.Add(time.Minute),
		build,
		failed,
		report,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.QualificationPasses != 1 {
		t.Fatalf("qualification after failure=%d want=1", restarted.QualificationPasses)
	}
}

func TestNextNVCodecValidationReceiptEvidenceChangeRestartsAtOne(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	build := "0123456789abcdef"
	report := validNVCodecReportForTest(now)
	previous := &NVCodecValidationReceipt{
		SchemaVersion:       nvcodecValidationReceiptSchema,
		BuildRevision:       build,
		SavedAtUnixMs:       now.Add(-time.Minute).UnixMilli(),
		QualificationPasses: 2,
		LastAttemptPassed:   true,
		LastAttemptAtUnixMs: now.Add(-time.Minute).UnixMilli(),
		Report:              report,
	}

	tests := []struct {
		name   string
		build  string
		report desktopcodec.NVCodecH265444RoundTripReport
	}{
		{
			name:   "build",
			build:  "different-build",
			report: report,
		},
		{
			name:  "driver",
			build: build,
			report: func() desktopcodec.NVCodecH265444RoundTripReport {
				copy := report
				copy.AdapterDriverVersion++
				return copy
			}(),
		},
		{
			name:  "adapter",
			build: build,
			report: func() desktopcodec.NVCodecH265444RoundTripReport {
				copy := report
				copy.AdapterDeviceID++
				return copy
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, err := nextNVCodecValidationReceipt(
				now,
				tc.build,
				previous,
				tc.report,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if next.QualificationPasses != 1 {
				t.Fatalf("qualification=%d want=1", next.QualificationPasses)
			}
		})
	}
}

func TestEvaluateNVCodecValidationReceiptCurrent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	receipt, identity := validNVCodecReceiptForTest(now)
	status := evaluateNVCodecValidationReceipt(
		now,
		receipt.BuildRevision,
		identity,
		nil,
		receipt,
	)
	if !status.Current || status.StaleReason != "" {
		t.Fatalf("matching validation receipt is stale: %+v", status)
	}
	if status.CurrentIdentity == nil || status.Report == nil {
		t.Fatalf("current validation omitted evidence: %+v", status)
	}
	if status.QualificationPasses != nvcodecValidationRequiredPasses ||
		status.RequiredPasses != nvcodecValidationRequiredPasses {
		t.Fatalf("qualification status=%+v", status)
	}
}

func TestEvaluateNVCodecValidationReceiptInvalidation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	base, identity := validNVCodecReceiptForTest(now)

	tests := []struct {
		name        string
		receipt     func() *NVCodecValidationReceipt
		build       string
		identity    desktopcodec.NVCodecValidationIdentity
		identityErr error
	}{
		{
			name: "qualification incomplete",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				copy.QualificationPasses = nvcodecValidationRequiredPasses - 1
				return &copy
			},
			build:    base.BuildRevision,
			identity: identity,
		},
		{
			name: "last attempt failed",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				copy.QualificationPasses = 0
				copy.LastAttemptPassed = false
				copy.LastFailure = "decode mismatch"
				return &copy
			},
			build:    base.BuildRevision,
			identity: identity,
		},
		{
			name: "build changed",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				return &copy
			},
			build:    "different-revision",
			identity: identity,
		},
		{
			name: "driver changed",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				return &copy
			},
			build: base.BuildRevision,
			identity: func() desktopcodec.NVCodecValidationIdentity {
				copy := identity
				copy.AdapterDriverVersion++
				return copy
			}(),
		},
		{
			name: "probe failed",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				return &copy
			},
			build:       base.BuildRevision,
			identity:    identity,
			identityErr: errors.New("adapter unavailable"),
		},
		{
			name: "expired",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				copy.SavedAtUnixMs = now.Add(-nvcodecValidationMaxAge - time.Hour).UnixMilli()
				return &copy
			},
			build:    base.BuildRevision,
			identity: identity,
		},
		{
			name: "dirty or unknown build",
			receipt: func() *NVCodecValidationReceipt {
				copy := *base
				return &copy
			},
			build:    "",
			identity: identity,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := evaluateNVCodecValidationReceipt(
				now,
				tc.build,
				tc.identity,
				tc.identityErr,
				tc.receipt(),
			)
			if status.Current || status.StaleReason == "" {
				t.Fatalf("validation should be stale: %+v", status)
			}
		})
	}
}

func TestNVCodecValidationReceiptPathFollowsConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "relay-agent.yaml")
	path := nvcodecValidationReceiptPath(configPath)
	if path != filepath.Join(dir, nvcodecValidationReceiptName) {
		t.Fatalf("validation receipt path=%q", path)
	}
}

func TestReplaceNVCodecValidationFileOverwritesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "receipt.json")
	tmp := filepath.Join(dir, "receipt.tmp")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceNVCodecValidationFile(tmp, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("target contents=%q want=new", data)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temporary receipt still exists: %v", err)
	}
}
