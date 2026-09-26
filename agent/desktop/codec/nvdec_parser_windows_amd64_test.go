//go:build windows && amd64

package codec

import (
	"testing"
	"unsafe"
)

func TestNVDECParserABISizes(t *testing.T) {
	if got := unsafe.Sizeof(nvdecParserParams{}); got != 136 {
		t.Fatalf("CUVIDPARSERPARAMS size=%d want=136", got)
	}
	if got := unsafe.Alignof(nvdecParserParams{}); got != 8 {
		t.Fatalf("CUVIDPARSERPARAMS alignment=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvdecParserParams{}.UserData); got != 40 {
		t.Fatalf("CUVIDPARSERPARAMS pUserData offset=%d want=40", got)
	}
	if got := unsafe.Offsetof(nvdecParserParams{}.ExtVideoInfo); got != 128 {
		t.Fatalf("CUVIDPARSERPARAMS pExtVideoInfo offset=%d want=128", got)
	}

	if got := unsafe.Sizeof(nvdecSourceDataPacket{}); got != 24 {
		t.Fatalf("CUVIDSOURCEDATAPACKET size=%d want=24", got)
	}
	if got := unsafe.Offsetof(nvdecSourceDataPacket{}.Payload); got != 8 {
		t.Fatalf("CUVIDSOURCEDATAPACKET payload offset=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvdecSourceDataPacket{}.Timestamp); got != 16 {
		t.Fatalf("CUVIDSOURCEDATAPACKET timestamp offset=%d want=16", got)
	}

	if got := unsafe.Sizeof(nvdecVideoFormat{}); got != 64 {
		t.Fatalf("CUVIDEOFORMAT size=%d want=64", got)
	}
	if got := unsafe.Alignof(nvdecVideoFormat{}); got != 4 {
		t.Fatalf("CUVIDEOFORMAT alignment=%d want=4", got)
	}
	if got := unsafe.Offsetof(nvdecVideoFormat{}.CodedWidth); got != 16 {
		t.Fatalf("CUVIDEOFORMAT coded_width offset=%d want=16", got)
	}
	if got := unsafe.Offsetof(nvdecVideoFormat{}.ChromaFormat); got != 40 {
		t.Fatalf("CUVIDEOFORMAT chroma_format offset=%d want=40", got)
	}

	if got := unsafe.Sizeof(nvdecDecodeCreateInfo{}); got != 112 {
		t.Fatalf("CUVIDDECODECREATEINFO size=%d want=112", got)
	}
	if got := unsafe.Alignof(nvdecDecodeCreateInfo{}); got != 8 {
		t.Fatalf("CUVIDDECODECREATEINFO alignment=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvdecDecodeCreateInfo{}.VideoContextLock); got != 72 {
		t.Fatalf("CUVIDDECODECREATEINFO vidLock offset=%d want=72", got)
	}
	if got := unsafe.Offsetof(nvdecDecodeCreateInfo{}.Reserved2); got != 96 {
		t.Fatalf("CUVIDDECODECREATEINFO Reserved2 offset=%d want=96", got)
	}
}

func validNVDECFormatForTest() nvdecVideoFormat {
	return nvdecVideoFormat{
		Codec:                nvVideoCodecHEVC,
		ProgressiveSequence:  1,
		MinNumDecodeSurfaces: 8,
		CodedWidth:           1920,
		CodedHeight:          1088,
		DisplayArea: nvdecRect32{
			Right:  1920,
			Bottom: 1080,
		},
		ChromaFormat: nvVideoChroma444,
	}
}

func validNVDECConfigForTest() VideoConfig {
	cfg := DefaultVideoConfig()
	cfg.Width = 1920
	cfg.Height = 1080
	cfg.FPS = 60
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8
	return cfg
}

func TestValidateNVDECHEVC444Format(t *testing.T) {
	format := validNVDECFormatForTest()
	surfaces, width, height, err := validateNVDECHEVC444Format(&format, validNVDECConfigForTest())
	if err != nil {
		t.Fatal(err)
	}
	if surfaces != 8 || width != 1920 || height != 1080 {
		t.Fatalf("format result surfaces=%d display=%dx%d", surfaces, width, height)
	}

	format.ChromaFormat = 1
	if _, _, _, err := validateNVDECHEVC444Format(&format, validNVDECConfigForTest()); err == nil {
		t.Fatal("4:2:0 format was accepted by NVDEC HEVC 4:4:4 validator")
	}
}

func TestBuildNVDECDecodeCreateInfo(t *testing.T) {
	format := validNVDECFormatForTest()
	info, surfaces, err := buildNVDECDecodeCreateInfo(&format, validNVDECConfigForTest(), 0x1234)
	if err != nil {
		t.Fatal(err)
	}
	if surfaces != 8 || info.NumDecodeSurfaces != 8 {
		t.Fatalf("decode surfaces=%d info=%d", surfaces, info.NumDecodeSurfaces)
	}
	if info.CodecType != nvVideoCodecHEVC || info.ChromaFormat != nvVideoChroma444 {
		t.Fatalf("codec/chroma=%d/%d", info.CodecType, info.ChromaFormat)
	}
	if info.OutputFormat != nvdecSurfaceFormatYUV444 {
		t.Fatalf("output format=%d want=%d", info.OutputFormat, nvdecSurfaceFormatYUV444)
	}
	if info.TargetWidth != 1920 || info.TargetHeight != 1080 {
		t.Fatalf("target=%dx%d", info.TargetWidth, info.TargetHeight)
	}
	if info.VideoContextLock != 0x1234 {
		t.Fatalf("vidLock=%#x", info.VideoContextLock)
	}
}

func TestNVDECParserHandleRegistry(t *testing.T) {
	parser := &nvdecHEVC444Parser{}
	handle := nextNVDECParserHandle()
	nvdecParserHandleMap.Store(handle, parser)
	defer nvdecParserHandleMap.Delete(handle)
	if got := lookupNVDECParser(handle); got != parser {
		t.Fatalf("lookup=%p want=%p", got, parser)
	}
}

func TestNVDECParserCloseIdempotent(t *testing.T) {
	parser := &nvdecHEVC444Parser{}
	if err := parser.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := parser.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
