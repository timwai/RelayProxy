package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
)

const (
	nvcodecValidationReceiptSchema = 1
	nvcodecValidationReceiptName   = ".relayproxy-nvcodec-validation.json"
	nvcodecValidationMaxAge        = 30 * 24 * time.Hour
)

type NVCodecValidationReceipt struct {
	SchemaVersion int                                        `json:"schemaVersion"`
	BuildRevision string                                     `json:"buildRevision"`
	SavedAtUnixMs int64                                      `json:"savedAtUnixMs"`
	Report        desktopcodec.NVCodecH265444RoundTripReport `json:"report"`
}

type NVCodecValidationStatus struct {
	Current              bool                                        `json:"current"`
	StaleReason          string                                      `json:"staleReason,omitempty"`
	BuildRevision        string                                      `json:"buildRevision,omitempty"`
	CurrentBuildRevision string                                      `json:"currentBuildRevision,omitempty"`
	SavedAtUnixMs        int64                                       `json:"savedAtUnixMs,omitempty"`
	CurrentIdentity      *desktopcodec.NVCodecValidationIdentity     `json:"currentIdentity,omitempty"`
	Report               *desktopcodec.NVCodecH265444RoundTripReport `json:"report,omitempty"`
}

func nvcodecValidationBuildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
		}
	}
	if modified {
		return ""
	}
	return revision
}

func nvcodecValidationReceiptPath(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), nvcodecValidationReceiptName)
}

func loadNVCodecValidationReceipt(configPath string) (*NVCodecValidationReceipt, error) {
	path := nvcodecValidationReceiptPath(configPath)
	if path == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var receipt NVCodecValidationReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("decode NVCodec validation receipt: %w", err)
	}
	return &receipt, nil
}

func saveNVCodecValidationReceipt(
	configPath string,
	report desktopcodec.NVCodecH265444RoundTripReport,
) error {
	if !report.Passed {
		return fmt.Errorf("refuse to persist failed NVCodec validation")
	}
	if report.AdapterVendorID == 0 || report.AdapterDriverVersion == 0 {
		return fmt.Errorf("NVCodec validation identity is incomplete")
	}
	buildRevision := nvcodecValidationBuildRevision()
	if buildRevision == "" {
		return fmt.Errorf("current build has no clean VCS revision")
	}
	path := nvcodecValidationReceiptPath(configPath)
	if path == "" {
		return fmt.Errorf("agent configuration path is unavailable")
	}
	receipt := NVCodecValidationReceipt{
		SchemaVersion: nvcodecValidationReceiptSchema,
		BuildRevision: buildRevision,
		SavedAtUnixMs: time.Now().UnixMilli(),
		Report:        report,
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode NVCodec validation receipt: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+nvcodecValidationReceiptName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create NVCodec validation receipt temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod NVCodec validation receipt temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write NVCodec validation receipt temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync NVCodec validation receipt temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close NVCodec validation receipt temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace NVCodec validation receipt: %w", err)
	}
	cleanup = false
	return nil
}

func evaluateNVCodecValidationReceipt(
	now time.Time,
	currentBuild string,
	currentIdentity desktopcodec.NVCodecValidationIdentity,
	identityErr error,
	receipt *NVCodecValidationReceipt,
) NVCodecValidationStatus {
	status := NVCodecValidationStatus{
		CurrentBuildRevision: currentBuild,
	}
	if receipt == nil {
		status.StaleReason = "尚未保存成功的 NVIDIA GPU 自检凭证"
		return status
	}
	report := receipt.Report
	status.BuildRevision = receipt.BuildRevision
	status.SavedAtUnixMs = receipt.SavedAtUnixMs
	status.Report = &report
	if identityErr == nil {
		identity := currentIdentity
		status.CurrentIdentity = &identity
	}
	switch {
	case receipt.SchemaVersion != nvcodecValidationReceiptSchema:
		status.StaleReason = "验证凭证版本已变化"
	case !receipt.Report.Passed:
		status.StaleReason = "保存的 NVIDIA GPU 自检未通过"
	case receipt.SavedAtUnixMs <= 0:
		status.StaleReason = "验证凭证缺少时间"
	case now.Sub(time.UnixMilli(receipt.SavedAtUnixMs)) < 0:
		status.StaleReason = "验证凭证时间晚于当前系统时间"
	case now.Sub(time.UnixMilli(receipt.SavedAtUnixMs)) > nvcodecValidationMaxAge:
		status.StaleReason = "NVIDIA GPU 自检凭证已超过 30 天"
	case currentBuild == "":
		status.StaleReason = "当前构建没有可验证的 VCS revision"
	case receipt.BuildRevision != currentBuild:
		status.StaleReason = "RelayProxy 构建 revision 已变化"
	case identityErr != nil:
		status.StaleReason = "无法确认当前 NVIDIA adapter/driver：" + identityErr.Error()
	case !currentIdentity.MatchesReport(receipt.Report):
		status.StaleReason = "NVIDIA adapter 或驱动版本已变化"
	default:
		status.Current = true
	}
	return status
}

func (b *UIBridge) GetRemoteDesktopNVCodecValidation() NVCodecValidationStatus {
	receipt, err := loadNVCodecValidationReceipt(b.configPath)
	if err != nil && !os.IsNotExist(err) {
		return NVCodecValidationStatus{StaleReason: err.Error()}
	}
	if os.IsNotExist(err) {
		receipt = nil
	}
	now := time.Now()
	currentBuild := nvcodecValidationBuildRevision()
	if receipt == nil || currentBuild == "" {
		return evaluateNVCodecValidationReceipt(
			now,
			currentBuild,
			desktopcodec.NVCodecValidationIdentity{},
			nil,
			receipt,
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	identity, identityErr := desktopcodec.ProbeNVCodecValidationIdentity(ctx)
	return evaluateNVCodecValidationReceipt(
		now,
		currentBuild,
		identity,
		identityErr,
		receipt,
	)
}
