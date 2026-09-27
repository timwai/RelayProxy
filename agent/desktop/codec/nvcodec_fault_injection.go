package codec

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

const NVCodecFaultInjectionEnv = "RELAYPROXY_NV_CODEC_FAULT_INJECTION"

const (
	nvcodecFaultNone int32 = iota
	nvcodecFaultEncode
	nvcodecFaultDecode
)

var nvcodecFaultInjection atomic.Int32

type NVCodecFaultInjectionStatus struct {
	Allowed      bool   `json:"allowed"`
	PendingStage string `json:"pendingStage,omitempty"`
}

func NVCodecFaultInjectionAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(NVCodecFaultInjectionEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func nvcodecFaultStageCode(stage string) (int32, string, error) {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "encode", "nvenc":
		return nvcodecFaultEncode, "encode", nil
	case "decode", "nvdec":
		return nvcodecFaultDecode, "decode", nil
	default:
		return nvcodecFaultNone, "", fmt.Errorf("NVCodec fault stage must be encode or decode")
	}
}

func ArmNVCodecFaultInjection(stage string) (NVCodecFaultInjectionStatus, error) {
	if !NVCodecFaultInjectionAllowed() {
		return NVCodecFaultInjectionStatus{}, fmt.Errorf(
			"NVCodec fault injection is disabled; set %s=1 before starting RelayProxy",
			NVCodecFaultInjectionEnv,
		)
	}
	code, normalized, err := nvcodecFaultStageCode(stage)
	if err != nil {
		return NVCodecFaultInjectionStatus{Allowed: true}, err
	}
	nvcodecFaultInjection.Store(code)
	return NVCodecFaultInjectionStatus{Allowed: true, PendingStage: normalized}, nil
}

func NVCodecFaultInjectionState() NVCodecFaultInjectionStatus {
	status := NVCodecFaultInjectionStatus{Allowed: NVCodecFaultInjectionAllowed()}
	if !status.Allowed {
		return status
	}
	switch nvcodecFaultInjection.Load() {
	case nvcodecFaultEncode:
		status.PendingStage = "encode"
	case nvcodecFaultDecode:
		status.PendingStage = "decode"
	}
	return status
}

func ConsumeNVCodecFaultInjection(stage string) bool {
	if !NVCodecFaultInjectionAllowed() {
		return false
	}
	code, _, err := nvcodecFaultStageCode(stage)
	if err != nil {
		return false
	}
	return nvcodecFaultInjection.CompareAndSwap(code, nvcodecFaultNone)
}
