package codec

import (
	"encoding/json"
	"testing"
)

func TestNVCodecValidationIdentityMatchesReport(t *testing.T) {
	report := NVCodecH265444RoundTripReport{
		Passed:               true,
		AdapterVendorID:      0x10de,
		AdapterDeviceID:      0x2684,
		AdapterSubSysID:      0x12345678,
		AdapterRevision:      0xa1,
		AdapterDriverVersion: 0x0001000200030004,
	}
	identity := NVCodecValidationIdentity{
		AdapterVendorID:      report.AdapterVendorID,
		AdapterDeviceID:      report.AdapterDeviceID,
		AdapterSubSysID:      report.AdapterSubSysID,
		AdapterRevision:      report.AdapterRevision,
		AdapterDriverVersion: report.AdapterDriverVersion,
	}
	if !identity.MatchesReport(report) {
		t.Fatal("matching NVIDIA adapter identity was rejected")
	}
	identity.AdapterDriverVersion++
	if identity.MatchesReport(report) {
		t.Fatal("driver version change did not invalidate NVIDIA identity")
	}
	identity.AdapterDriverVersion = report.AdapterDriverVersion
	identity.AdapterDeviceID++
	if identity.MatchesReport(report) {
		t.Fatal("adapter device change did not invalidate NVIDIA identity")
	}
}

func TestNVCodecValidationIdentityRequiresDriverVersion(t *testing.T) {
	report := NVCodecH265444RoundTripReport{
		AdapterVendorID: 0x10de,
		AdapterDeviceID: 0x2684,
	}
	identity := NVCodecValidationIdentity{
		AdapterVendorID: report.AdapterVendorID,
		AdapterDeviceID: report.AdapterDeviceID,
	}
	if identity.MatchesReport(report) {
		t.Fatal("identity without a driver version was accepted")
	}
}

func TestNVCodecReportCarriesStandaloneNVDECCapabilities(t *testing.T) {
	report := NVCodecH265444RoundTripReport{
		Unsupported:           true,
		UnsupportedReason:     "NVENC unavailable",
		NVDECProbeChecked:     true,
		NVDECDeviceCount:      1,
		NVDECH264Known:        true,
		NVDECH264Supported:    true,
		NVDECHEVC420Known:     true,
		NVDECHEVC420Supported: true,
		NVDECHEVC444Known:     true,
		NVDECHEVC444Supported: false,
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded NVCodecH265444RoundTripReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Unsupported || !decoded.NVDECProbeChecked || decoded.NVDECDeviceCount != 1 {
		t.Fatalf("standalone NVDEC metadata was lost: %+v", decoded)
	}
	if !decoded.NVDECH264Known || !decoded.NVDECH264Supported ||
		!decoded.NVDECHEVC420Known || !decoded.NVDECHEVC420Supported ||
		!decoded.NVDECHEVC444Known || decoded.NVDECHEVC444Supported {
		t.Fatalf("standalone NVDEC capabilities were lost: %+v", decoded)
	}
}
