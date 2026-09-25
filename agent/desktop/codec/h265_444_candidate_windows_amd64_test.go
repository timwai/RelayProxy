//go:build windows && amd64

package codec

import "testing"

func TestFormatNVENCMaxSupportedVersion(t *testing.T) {
	tests := []struct {
		version uint32
		want    string
	}{
		{version: 0, want: ""},
		{version: 0x0c2, want: "12.2"},
		{version: 0x0d1, want: "13.1"},
	}
	for _, test := range tests {
		if got := formatNVENCMaxSupportedVersion(test.version); got != test.want {
			t.Fatalf("formatNVENCMaxSupportedVersion(%#x)=%q want=%q", test.version, got, test.want)
		}
	}
}

func TestFormatAMFRuntimeVersion(t *testing.T) {
	if got := formatAMFRuntimeVersion(0); got != "" {
		t.Fatalf("zero AMF version=%q", got)
	}
	if got := formatAMFRuntimeVersion(0x0001000500020003); got != "0x0001000500020003" {
		t.Fatalf("AMF version=%q", got)
	}
}
