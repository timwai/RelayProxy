package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
)

func validNVCodecReceiptForTest(now time.Time) (*NVCodecValidationReceipt, desktopcodec.NVCodecValidationIdentity) {
	report := desktopcodec.NVCodecH265444RoundTripReport{
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
	receipt := &NVCodecValidationReceipt{
		SchemaVersion: nvcodecValidationReceiptSchema,
		BuildRevision: "0123456789abcdef",
		SavedAtUnixMs: now.Add(-time.Hour).UnixMilli(),
		Report:        report,
	}
	identity := desktopcodec.NVCodecValidationIdentity{
		Adapter:              report.Adapter,
		AdapterVendorID:      report.AdapterVendorID,
		AdapterDeviceID:      report.AdapterDeviceID,
		AdapterSubSysID:      report.AdapterSubSysID,
		AdapterRevision:      report.AdapterRevision,
		AdapterDriverVersion: report.AdapterDriverVersion,
	}
	return receipt, identity
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
