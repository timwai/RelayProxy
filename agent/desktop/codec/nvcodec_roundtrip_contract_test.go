package codec

import "testing"

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
