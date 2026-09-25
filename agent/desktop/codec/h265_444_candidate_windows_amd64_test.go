//go:build windows && amd64

package codec

import (
	"testing"
	"unsafe"
)

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

func TestNVDECDecodeCapsABI(t *testing.T) {
	if got := unsafe.Sizeof(nvcuvidDecodeCaps{}); got != 88 {
		t.Fatalf("CUVIDDECODECAPS size=%d want=88", got)
	}
	if nvVideoCodecHEVC != 8 || nvVideoChroma444 != 3 {
		t.Fatalf("NVDEC enum constants codec=%d chroma=%d", nvVideoCodecHEVC, nvVideoChroma444)
	}
}

func TestNVENCProbeABI(t *testing.T) {
	if nvencAPIVersion != 0x0100000d || nvencMaxVersionCode != 0x0d1 {
		t.Fatalf("NVENC API versions api=%#x max=%#x", nvencAPIVersion, nvencMaxVersionCode)
	}
	if got := nvencStructVersion(1); got != 0x7101000d {
		t.Fatalf("NVENC struct v1=%#x want=%#x", got, uint32(0x7101000d))
	}
	if got := nvencStructVersion(2); got != 0x7102000d {
		t.Fatalf("NVENC struct v2=%#x want=%#x", got, uint32(0x7102000d))
	}
	if got := unsafe.Sizeof(nvencGUID{}); got != 16 {
		t.Fatalf("NVENC GUID size=%d want=16", got)
	}
	if got := unsafe.Sizeof(nvEncodeAPIFunctionList{}); got != 2552 {
		t.Fatalf("NV_ENCODE_API_FUNCTION_LIST size=%d want=2552", got)
	}
	var functions nvEncodeAPIFunctionList
	if got := unsafe.Offsetof(functions.NvEncGetEncodeCaps); got != 64 {
		t.Fatalf("nvEncGetEncodeCaps offset=%d want=64", got)
	}
	if got := unsafe.Offsetof(functions.NvEncDestroyEncoder); got != 224 {
		t.Fatalf("nvEncDestroyEncoder offset=%d want=224", got)
	}
	if got := unsafe.Offsetof(functions.NvEncOpenEncodeSessionEx); got != 240 {
		t.Fatalf("nvEncOpenEncodeSessionEx offset=%d want=240", got)
	}
	if got := unsafe.Offsetof(functions.Reserved2); got != 352 {
		t.Fatalf("NVENC reserved2 offset=%d want=352", got)
	}
	if got := unsafe.Sizeof(nvencOpenEncodeSessionExParams{}); got != 1552 {
		t.Fatalf("NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS size=%d want=1552", got)
	}
	if got := unsafe.Sizeof(nvencCapsParam{}); got != 256 {
		t.Fatalf("NV_ENC_CAPS_PARAM size=%d want=256", got)
	}
	if nvencCapsSupportYUV444Encode != 33 || nvencDeviceTypeCUDA != 1 {
		t.Fatalf("NVENC enums yuv444=%d cuda=%d", nvencCapsSupportYUV444Encode, nvencDeviceTypeCUDA)
	}
	if nvencCodecHEVCGUID != (nvencGUID{
		Data1: 0x790cdc88,
		Data2: 0x4522,
		Data3: 0x4d7b,
		Data4: [8]byte{0x94, 0x25, 0xbd, 0xa9, 0x97, 0x5f, 0x76, 0x03},
	}) {
		t.Fatalf("NVENC HEVC GUID=%+v", nvencCodecHEVCGUID)
	}
}

func TestNVENCHasGUID(t *testing.T) {
	if nvencHasGUID(nil, nvencCodecHEVCGUID) {
		t.Fatal("empty GUID list unexpectedly contains HEVC")
	}
	if !nvencHasGUID([]nvencGUID{{Data1: 1}, nvencCodecHEVCGUID}, nvencCodecHEVCGUID) {
		t.Fatal("HEVC GUID was not found")
	}
}
