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
	nvcodecValidationReceiptSchema  = 2
	nvcodecValidationReceiptName    = ".relayproxy-nvcodec-validation.json"
	nvcodecValidationMaxAge         = 30 * 24 * time.Hour
	nvcodecValidationRequiredPasses = 3
)

type NVCodecValidationReceipt struct {
	SchemaVersion       int                                        `json:"schemaVersion"`
	BuildRevision       string                                     `json:"buildRevision"`
	SavedAtUnixMs       int64                                      `json:"savedAtUnixMs,omitempty"`
	QualificationPasses int                                        `json:"qualificationPasses"`
	LastAttemptPassed   bool                                       `json:"lastAttemptPassed"`
	LastAttemptAtUnixMs int64                                      `json:"lastAttemptAtUnixMs,omitempty"`
	LastFailure         string                                     `json:"lastFailure,omitempty"`
	Report              desktopcodec.NVCodecH265444RoundTripReport `json:"report"`
	StressQualification *NVCodecStressQualificationReport          `json:"stressQualification,omitempty"`
}

type NVCodecValidationStatus struct {
	Current              bool                                        `json:"current"`
	StaleReason          string                                      `json:"staleReason,omitempty"`
	BuildRevision        string                                      `json:"buildRevision,omitempty"`
	CurrentBuildRevision string                                      `json:"currentBuildRevision,omitempty"`
	SavedAtUnixMs        int64                                       `json:"savedAtUnixMs,omitempty"`
	QualificationPasses  int                                         `json:"qualificationPasses"`
	RequiredPasses       int                                         `json:"requiredPasses"`
	LastAttemptPassed    bool                                        `json:"lastAttemptPassed"`
	LastAttemptAtUnixMs  int64                                       `json:"lastAttemptAtUnixMs,omitempty"`
	LastFailure          string                                      `json:"lastFailure,omitempty"`
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

func nvcodecValidationFailure(report desktopcodec.NVCodecH265444RoundTripReport, attemptErr error) string {
	if attemptErr != nil {
		return attemptErr.Error()
	}
	if message := strings.TrimSpace(report.Error); message != "" {
		return message
	}
	return "NVIDIA GPU 自检未通过"
}

func nvcodecValidationReportIdentity(
	report desktopcodec.NVCodecH265444RoundTripReport,
) desktopcodec.NVCodecValidationIdentity {
	return desktopcodec.NVCodecValidationIdentity{
		Adapter:              report.Adapter,
		AdapterVendorID:      report.AdapterVendorID,
		AdapterDeviceID:      report.AdapterDeviceID,
		AdapterSubSysID:      report.AdapterSubSysID,
		AdapterRevision:      report.AdapterRevision,
		AdapterDriverVersion: report.AdapterDriverVersion,
	}
}

func nextNVCodecValidationReceipt(
	now time.Time,
	buildRevision string,
	previous *NVCodecValidationReceipt,
	report desktopcodec.NVCodecH265444RoundTripReport,
	attemptErr error,
) (*NVCodecValidationReceipt, error) {
	buildRevision = strings.TrimSpace(buildRevision)
	if buildRevision == "" {
		return nil, fmt.Errorf("current build has no clean VCS revision")
	}
	nowUnixMs := now.UnixMilli()
	if nowUnixMs <= 0 {
		return nil, fmt.Errorf("current validation time is invalid")
	}

	if attemptErr != nil || !report.Passed {
		receipt := &NVCodecValidationReceipt{
			SchemaVersion:       nvcodecValidationReceiptSchema,
			BuildRevision:       buildRevision,
			QualificationPasses: 0,
			LastAttemptPassed:   false,
			LastAttemptAtUnixMs: nowUnixMs,
			LastFailure:         nvcodecValidationFailure(report, attemptErr),
		}
		if previous != nil &&
			previous.SchemaVersion == nvcodecValidationReceiptSchema &&
			previous.BuildRevision == buildRevision &&
			previous.Report.Passed {
			receipt.SavedAtUnixMs = previous.SavedAtUnixMs
			receipt.Report = previous.Report
			receipt.StressQualification = cloneNVCodecStressQualificationReport(previous.StressQualification)
		}
		return receipt, nil
	}

	identity := nvcodecValidationReportIdentity(report)
	if identity.AdapterVendorID == 0 || identity.AdapterDriverVersion == 0 {
		return nil, fmt.Errorf("NVCodec validation identity is incomplete")
	}

	passes := 1
	if previous != nil &&
		previous.SchemaVersion == nvcodecValidationReceiptSchema &&
		previous.BuildRevision == buildRevision &&
		previous.LastAttemptPassed &&
		previous.QualificationPasses > 0 &&
		previous.Report.Passed &&
		identity.MatchesReport(previous.Report) {
		lastAttempt := time.UnixMilli(previous.LastAttemptAtUnixMs)
		if previous.LastAttemptAtUnixMs > 0 &&
			now.Sub(lastAttempt) >= 0 &&
			now.Sub(lastAttempt) <= nvcodecValidationMaxAge {
			passes = previous.QualificationPasses + 1
		}
	}

	receipt := &NVCodecValidationReceipt{
		SchemaVersion:       nvcodecValidationReceiptSchema,
		BuildRevision:       buildRevision,
		SavedAtUnixMs:       nowUnixMs,
		QualificationPasses: passes,
		LastAttemptPassed:   true,
		LastAttemptAtUnixMs: nowUnixMs,
		Report:              report,
	}
	if previous != nil &&
		previous.SchemaVersion == nvcodecValidationReceiptSchema &&
		previous.BuildRevision == buildRevision &&
		identity.MatchesReport(previous.Report) {
		receipt.StressQualification = cloneNVCodecStressQualificationReport(previous.StressQualification)
	}
	return receipt, nil
}

func writeNVCodecValidationReceipt(path string, receipt *NVCodecValidationReceipt) error {
	if receipt == nil {
		return fmt.Errorf("NVCodec validation receipt is nil")
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
	if err := replaceNVCodecValidationFile(tmpPath, path); err != nil {
		return fmt.Errorf("replace NVCodec validation receipt: %w", err)
	}
	cleanup = false
	return nil
}

func cloneNVCodecStressQualificationReport(
	report *NVCodecStressQualificationReport,
) *NVCodecStressQualificationReport {
	if report == nil {
		return nil
	}
	copy := *report
	copy.Reports = append([]desktopcodec.NVCodecH265444RoundTripReport(nil), report.Reports...)
	copy.MemoryTrend = append([]NVCodecStressMemorySample(nil), report.MemoryTrend...)
	return &copy
}

func recordNVCodecStressQualification(
	configPath string,
	stress NVCodecStressQualificationReport,
) error {
	buildRevision := nvcodecValidationBuildRevision()
	if buildRevision == "" {
		return fmt.Errorf("current build has no clean VCS revision")
	}
	path := nvcodecValidationReceiptPath(configPath)
	if path == "" {
		return fmt.Errorf("agent configuration path is unavailable")
	}
	receipt, err := loadNVCodecValidationReceipt(configPath)
	if err != nil {
		return fmt.Errorf("load NVCodec validation receipt for stress evidence: %w", err)
	}
	if receipt == nil ||
		receipt.SchemaVersion != nvcodecValidationReceiptSchema ||
		receipt.BuildRevision != buildRevision {
		return fmt.Errorf("NVCodec validation receipt is unavailable for current build")
	}
	receipt.StressQualification = cloneNVCodecStressQualificationReport(&stress)
	return writeNVCodecValidationReceipt(path, receipt)
}

func recordNVCodecValidationAttempt(
	configPath string,
	report desktopcodec.NVCodecH265444RoundTripReport,
	attemptErr error,
) error {
	buildRevision := nvcodecValidationBuildRevision()
	if buildRevision == "" {
		return fmt.Errorf("current build has no clean VCS revision")
	}
	path := nvcodecValidationReceiptPath(configPath)
	if path == "" {
		return fmt.Errorf("agent configuration path is unavailable")
	}

	previous, err := loadNVCodecValidationReceipt(configPath)
	if err != nil && !os.IsNotExist(err) {
		// A corrupt or unreadable receipt must never block new successful evidence
		// from replacing it. Treat it as no prior qualification state.
		previous = nil
	}
	if os.IsNotExist(err) {
		previous = nil
	}
	receipt, err := nextNVCodecValidationReceipt(
		time.Now(),
		buildRevision,
		previous,
		report,
		attemptErr,
	)
	if err != nil {
		return err
	}
	return writeNVCodecValidationReceipt(path, receipt)
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
		RequiredPasses:       nvcodecValidationRequiredPasses,
	}
	if receipt == nil {
		status.StaleReason = "尚未保存 NVIDIA GPU 自检资格凭证"
		return status
	}
	report := receipt.Report
	status.BuildRevision = receipt.BuildRevision
	status.SavedAtUnixMs = receipt.SavedAtUnixMs
	status.QualificationPasses = receipt.QualificationPasses
	status.LastAttemptPassed = receipt.LastAttemptPassed
	status.LastAttemptAtUnixMs = receipt.LastAttemptAtUnixMs
	status.LastFailure = receipt.LastFailure
	if report.Passed {
		status.Report = &report
	}
	if identityErr == nil {
		identity := currentIdentity
		status.CurrentIdentity = &identity
	}

	switch {
	case receipt.SchemaVersion != nvcodecValidationReceiptSchema:
		status.StaleReason = "验证凭证版本已变化"
	case !receipt.LastAttemptPassed:
		status.StaleReason = "最近一次 NVIDIA GPU 自检失败"
		if receipt.LastFailure != "" {
			status.StaleReason += "：" + receipt.LastFailure
		}
	case !receipt.Report.Passed:
		status.StaleReason = "当前构建尚无成功的 NVIDIA GPU 自检"
	case currentBuild == "":
		status.StaleReason = "当前构建没有可验证的 VCS revision"
	case receipt.BuildRevision != currentBuild:
		status.StaleReason = "RelayProxy 构建 revision 已变化"
	case receipt.SavedAtUnixMs <= 0:
		status.StaleReason = "验证凭证缺少时间"
	case now.Sub(time.UnixMilli(receipt.SavedAtUnixMs)) < 0:
		status.StaleReason = "验证凭证时间晚于当前系统时间"
	case now.Sub(time.UnixMilli(receipt.SavedAtUnixMs)) > nvcodecValidationMaxAge:
		status.StaleReason = "NVIDIA GPU 自检凭证已超过 30 天"
	case receipt.QualificationPasses < nvcodecValidationRequiredPasses:
		status.StaleReason = fmt.Sprintf(
			"NVIDIA GPU 资格验证进度 %d/%d，需要连续通过",
			receipt.QualificationPasses,
			nvcodecValidationRequiredPasses,
		)
	case identityErr != nil:
		status.StaleReason = "无法确认当前 NVIDIA adapter/driver：" + identityErr.Error()
	case !currentIdentity.MatchesReport(receipt.Report):
		status.StaleReason = "NVIDIA adapter 或驱动版本已变化"
	default:
		status.Current = true
	}
	return status
}

func nvcodecValidationNeedsIdentityProbe(
	now time.Time,
	currentBuild string,
	receipt *NVCodecValidationReceipt,
) bool {
	if receipt == nil ||
		receipt.SchemaVersion != nvcodecValidationReceiptSchema ||
		!receipt.LastAttemptPassed ||
		!receipt.Report.Passed ||
		receipt.QualificationPasses < nvcodecValidationRequiredPasses ||
		currentBuild == "" ||
		receipt.BuildRevision != currentBuild ||
		receipt.SavedAtUnixMs <= 0 {
		return false
	}
	age := now.Sub(time.UnixMilli(receipt.SavedAtUnixMs))
	return age >= 0 && age <= nvcodecValidationMaxAge
}

func (b *UIBridge) GetRemoteDesktopNVCodecValidation() NVCodecValidationStatus {
	receipt, err := loadNVCodecValidationReceipt(b.configPath)
	if err != nil && !os.IsNotExist(err) {
		return NVCodecValidationStatus{
			RequiredPasses: nvcodecValidationRequiredPasses,
			StaleReason:    err.Error(),
		}
	}
	if os.IsNotExist(err) {
		receipt = nil
	}
	now := time.Now()
	currentBuild := nvcodecValidationBuildRevision()
	if !nvcodecValidationNeedsIdentityProbe(now, currentBuild, receipt) {
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
