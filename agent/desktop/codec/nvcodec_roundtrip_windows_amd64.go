//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nvcodecValidationVendorNVIDIA     uint32 = 0x10de
	nvcodecValidationAdapterSoftware  uint32 = 0x2
	nvcodecValidationDXGINotFound     uint32 = 0x887a0002
	nvcodecValidationChannelTolerance        = 48
)

var (
	nvcodecValidationDXGIDLL       = windows.NewLazySystemDLL("dxgi.dll")
	nvcodecValidationCreateFactory = nvcodecValidationDXGIDLL.NewProc("CreateDXGIFactory1")
	nvcodecValidationIIDFactory1   = windows.GUID{
		Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba,
		Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87},
	}
)

type nvcodecValidationLUID struct {
	LowPart  uint32
	HighPart int32
}

type nvcodecValidationAdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLUID           nvcodecValidationLUID
	Flags                 uint32
}

type nvcodecValidationSubresourceData struct {
	SysMem           unsafe.Pointer
	SysMemPitch      uint32
	SysMemSlicePitch uint32
}

type nvcodecValidationYUV struct {
	Y byte
	U byte
	V byte
}

func nvcodecValidationExpectedPixel(x, y, width, height int) nvcodecValidationYUV {
	left := x < width/2
	top := y < height/2
	switch {
	case left && top:
		return nvcodecValidationYUV{Y: 48, U: 80, V: 220}
	case !left && top:
		return nvcodecValidationYUV{Y: 80, U: 200, V: 80}
	case left && !top:
		return nvcodecValidationYUV{Y: 176, U: 96, V: 200}
	default:
		return nvcodecValidationYUV{Y: 208, U: 180, V: 40}
	}
}

func nvcodecValidationAYUVPattern(width, height int) []byte {
	pixels := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			expected := nvcodecValidationExpectedPixel(x, y, width, height)
			offset := (y*width + x) * 4
			// DXGI_FORMAT_AYUV is V,U,Y,A in little-endian memory.
			pixels[offset] = expected.V
			pixels[offset+1] = expected.U
			pixels[offset+2] = expected.Y
			pixels[offset+3] = 255
		}
	}
	return pixels
}

func createNVCodecValidationD3D11Device() (
	device unsafe.Pointer,
	context unsafe.Pointer,
	adapterName string,
	err error,
) {
	var factory unsafe.Pointer
	hr, _, _ := nvcodecValidationCreateFactory.Call(
		uintptr(unsafe.Pointer(&nvcodecValidationIIDFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hresultFailed(hr) {
		return nil, nil, "", hresultError("CreateDXGIFactory1", hr)
	}
	if factory == nil {
		return nil, nil, "", errors.New("CreateDXGIFactory1 returned nil")
	}
	defer releaseIUnknown(factory)

	featureLevels := [...]uint32{
		0xb000, // D3D_FEATURE_LEVEL_11_0
		0xa100, // D3D_FEATURE_LEVEL_10_1
		0xa000, // D3D_FEATURE_LEVEL_10_0
	}
	var lastErr error
	for index := uint32(0); index < 32; index++ {
		var adapter unsafe.Pointer
		enumHR := comCall(
			factory,
			12, // IDXGIFactory1::EnumAdapters1
			uintptr(index),
			uintptr(unsafe.Pointer(&adapter)),
		)
		if uint32(enumHR) == nvcodecValidationDXGINotFound {
			break
		}
		if hresultFailed(enumHR) {
			lastErr = hresultError("IDXGIFactory1.EnumAdapters1", enumHR)
			continue
		}
		if adapter == nil {
			continue
		}

		var desc nvcodecValidationAdapterDesc1
		descHR := comCall(
			adapter,
			10, // IDXGIAdapter1::GetDesc1
			uintptr(unsafe.Pointer(&desc)),
		)
		if hresultFailed(descHR) {
			lastErr = hresultError("IDXGIAdapter1.GetDesc1", descHR)
			releaseIUnknown(adapter)
			continue
		}
		if desc.VendorID != nvcodecValidationVendorNVIDIA ||
			desc.Flags&nvcodecValidationAdapterSoftware != 0 {
			releaseIUnknown(adapter)
			continue
		}

		var selectedLevel uint32
		var candidateDevice unsafe.Pointer
		var candidateContext unsafe.Pointer
		createHR, _, _ := procD3D11CreateDevice.Call(
			uintptr(adapter),
			0, // D3D_DRIVER_TYPE_UNKNOWN when an adapter is supplied
			0,
			d3d11CreateDeviceBGRASupport|d3d11CreateDeviceVideoSupport,
			uintptr(unsafe.Pointer(&featureLevels[0])),
			uintptr(len(featureLevels)),
			d3d11SDKVersion,
			uintptr(unsafe.Pointer(&candidateDevice)),
			uintptr(unsafe.Pointer(&selectedLevel)),
			uintptr(unsafe.Pointer(&candidateContext)),
		)
		releaseIUnknown(adapter)
		if hresultFailed(createHR) {
			lastErr = hresultError("D3D11CreateDevice(NVIDIA)", createHR)
			releaseIUnknown(candidateContext)
			releaseIUnknown(candidateDevice)
			continue
		}
		if candidateDevice == nil || candidateContext == nil {
			lastErr = errors.New("D3D11CreateDevice(NVIDIA) returned incomplete objects")
			releaseIUnknown(candidateContext)
			releaseIUnknown(candidateDevice)
			continue
		}
		if multithread, queryErr := comQueryInterface(candidateDevice, &iidID3D10Multithread); queryErr == nil {
			protected := comCall(multithread, id3d10MultithreadSetMultithreadProtected, 1)
			releaseIUnknown(multithread)
			if protected == 0 {
				lastErr = errors.New("ID3D10Multithread.SetMultithreadProtected returned FALSE")
				releaseIUnknown(candidateContext)
				releaseIUnknown(candidateDevice)
				continue
			}
		}
		return candidateDevice, candidateContext, windows.UTF16ToString(desc.Description[:]), nil
	}
	if lastErr != nil {
		return nil, nil, "", fmt.Errorf("%w: no usable NVIDIA D3D11 adapter: %v", ErrDecoderUnavailable, lastErr)
	}
	return nil, nil, "", fmt.Errorf("%w: no NVIDIA D3D11 adapter found", ErrDecoderUnavailable)
}

func createNVCodecValidationAYUVTexture(
	device unsafe.Pointer,
	width int,
	height int,
) (unsafe.Pointer, error) {
	if device == nil {
		return nil, ErrEncoderUnavailable
	}
	pixels := nvcodecValidationAYUVPattern(width, height)
	initial := nvcodecValidationSubresourceData{
		SysMem:           unsafe.Pointer(&pixels[0]),
		SysMemPitch:      uint32(width * 4),
		SysMemSlicePitch: uint32(len(pixels)),
	}
	desc := mfD3D11Texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         dxgiFormatAYUV,
		SampleDesc:     mfD3D11SampleDesc{Count: 1},
		Usage:          d3d11UsageDefault,
		BindFlags:      d3d11BindRenderTarget,
		CPUAccessFlags: 0,
		MiscFlags:      0,
	}
	var texture unsafe.Pointer
	hr := comCall(
		device,
		5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&desc)),
		uintptr(unsafe.Pointer(&initial)),
		uintptr(unsafe.Pointer(&texture)),
	)
	runtime.KeepAlive(pixels)
	runtime.KeepAlive(initial)
	if hresultFailed(hr) {
		return nil, hresultError("ID3D11Device.CreateTexture2D(NVCodec validation AYUV)", hr)
	}
	if texture == nil {
		return nil, errors.New("NVCodec validation AYUV texture is nil")
	}
	return texture, nil
}

func readNVCodecValidationAYUVSamples(
	device unsafe.Pointer,
	context unsafe.Pointer,
	source uintptr,
	subresource uint32,
	width int,
	height int,
) (samples int, maxError int, err error) {
	if device == nil || context == nil || source == 0 {
		return 0, 0, ErrDecoderUnavailable
	}
	desc := mfD3D11Texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         dxgiFormatAYUV,
		SampleDesc:     mfD3D11SampleDesc{Count: 1},
		Usage:          d3d11UsageStaging,
		CPUAccessFlags: d3d11CPUAccessRead,
	}
	var staging unsafe.Pointer
	hr := comCall(
		device,
		5,
		uintptr(unsafe.Pointer(&desc)),
		0,
		uintptr(unsafe.Pointer(&staging)),
	)
	if hresultFailed(hr) {
		return 0, 0, hresultError("ID3D11Device.CreateTexture2D(AYUV staging)", hr)
	}
	if staging == nil {
		return 0, 0, errors.New("NVCodec validation staging texture is nil")
	}
	defer releaseIUnknown(staging)

	comCall(
		context,
		46, // ID3D11DeviceContext::CopySubresourceRegion
		uintptr(staging),
		0,
		0,
		0,
		0,
		source,
		uintptr(subresource),
		0,
	)

	var mapped mfD3D11MappedSubresource
	hr = comCall(
		context,
		14, // ID3D11DeviceContext::Map
		uintptr(staging),
		0,
		d3d11MapRead,
		0,
		uintptr(unsafe.Pointer(&mapped)),
	)
	if hresultFailed(hr) {
		return 0, 0, hresultError("ID3D11DeviceContext.Map(AYUV validation)", hr)
	}
	if mapped.Data == nil || int(mapped.RowPitch) < width*4 {
		comCall(context, 15, uintptr(staging), 0)
		return 0, 0, errors.New("NVCodec validation AYUV map returned invalid row pitch")
	}
	defer comCall(context, 15, uintptr(staging), 0)

	positions := [][2]int{
		{width / 4, height / 4},
		{width * 3 / 4, height / 4},
		{width / 4, height * 3 / 4},
		{width * 3 / 4, height * 3 / 4},
	}
	absDiff := func(a byte, b byte) int {
		if a >= b {
			return int(a - b)
		}
		return int(b - a)
	}
	for _, position := range positions {
		x, y := position[0], position[1]
		expected := nvcodecValidationExpectedPixel(x, y, width, height)
		row := unsafe.Slice(
			(*byte)(unsafe.Pointer(uintptr(mapped.Data)+uintptr(y)*uintptr(mapped.RowPitch))),
			int(mapped.RowPitch),
		)
		offset := x * 4
		if offset+3 >= len(row) {
			return samples, maxError, errors.New("NVCodec validation sample exceeds mapped row")
		}
		actualV := row[offset]
		actualU := row[offset+1]
		actualY := row[offset+2]
		actualA := row[offset+3]
		for _, channelError := range []int{
			absDiff(actualY, expected.Y),
			absDiff(actualU, expected.U),
			absDiff(actualV, expected.V),
		} {
			if channelError > maxError {
				maxError = channelError
			}
		}
		if actualA != 255 {
			return samples, maxError, fmt.Errorf(
				"NVCodec validation alpha=%d at %d,%d want=255",
				actualA,
				x,
				y,
			)
		}
		samples++
	}
	if maxError > nvcodecValidationChannelTolerance {
		return samples, maxError, fmt.Errorf(
			"NVCodec validation max YUV channel error=%d exceeds tolerance=%d",
			maxError,
			nvcodecValidationChannelTolerance,
		)
	}
	return samples, maxError, nil
}

func ValidateNVCodecH265444RoundTrip(
	ctx context.Context,
) (report NVCodecH265444RoundTripReport, retErr error) {
	started := time.Now()
	cfg := DefaultVideoConfig()
	cfg.Width = 640
	cfg.Height = 360
	cfg.FPS = 30
	cfg.TargetBitrate = 8_000_000
	cfg.KeyframeEvery = time.Second
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8

	report = NVCodecH265444RoundTripReport{
		Backend:             H265444BackendNVCodec,
		Width:               cfg.Width,
		Height:              cfg.Height,
		FPS:                 cfg.FPS,
		InitialBitrate:      cfg.TargetBitrate,
		ReconfiguredBitrate: cfg.TargetBitrate / 2,
	}
	defer func() {
		report.DurationMs = time.Since(started).Milliseconds()
		if retErr != nil {
			report.Passed = false
			report.Error = retErr.Error()
		}
	}()

	device, deviceContext, adapterName, err := createNVCodecValidationD3D11Device()
	if err != nil {
		return report, err
	}
	report.Adapter = adapterName
	report.D3D11DeviceCreated = true

	var source unsafe.Pointer
	var encoder SequenceHeaderEncoder
	var decoder Decoder
	defer func() {
		var cleanupErr error
		if decoder != nil {
			cleanupErr = errors.Join(cleanupErr, decoder.Close())
		}
		if encoder != nil {
			cleanupErr = errors.Join(cleanupErr, encoder.Close())
		}
		releaseIUnknown(source)
		releaseIUnknown(deviceContext)
		releaseIUnknown(device)
		if cleanupErr == nil {
			report.CleanupValidated = true
		} else {
			retErr = errors.Join(retErr, cleanupErr)
			report.Passed = false
			report.Error = retErr.Error()
		}
	}()

	source, err = createNVCodecValidationAYUVTexture(device, cfg.Width, cfg.Height)
	if err != nil {
		return report, err
	}
	report.SourceTextureCreated = true

	encoder, err = OpenNVENCH265EncoderWithD3D11(ctx, cfg, uintptr(device))
	if err != nil {
		return report, err
	}
	report.EncoderOpened = true
	sequenceHeader := encoder.SequenceHeader()
	report.SequenceHeaderBytes = len(sequenceHeader)
	if !H265HasParameterSets(sequenceHeader) {
		return report, fmt.Errorf("%w: NVENC self-test sequence header is incomplete", ErrEncoderUnavailable)
	}
	d3d11Encoder, ok := encoder.(D3D11Encoder)
	if !ok {
		return report, fmt.Errorf("%w: NVENC self-test encoder lacks D3D11 input", ErrEncoderUnavailable)
	}

	decoder, err = OpenNVDECH265DecoderWithD3D11(ctx, cfg, uintptr(device))
	if err != nil {
		return report, err
	}
	report.DecoderOpened = true

	encodeFrame := func(timestamp time.Duration) (EncodedPacket, error) {
		packets, encodeErr := d3d11Encoder.EncodeD3D11(ctx, D3D11EncodeFrame{
			Device:      uintptr(device),
			Resource:    uintptr(source),
			Subresource: 0,
			Width:       cfg.Width,
			Height:      cfg.Height,
			Format:      PixelFormatAYUV,
			Timestamp:   timestamp,
		})
		if encodeErr != nil {
			return EncodedPacket{}, encodeErr
		}
		if len(packets) != 1 || len(packets[0].Data) == 0 {
			return EncodedPacket{}, fmt.Errorf(
				"%w: NVENC self-test returned %d packets",
				ErrEncoderUnavailable,
				len(packets),
			)
		}
		report.EncodedFrames++
		return packets[0], nil
	}

	var firstDecoded []DecodedFrame
	for frameIndex := 0; frameIndex < 3 && len(firstDecoded) == 0; frameIndex++ {
		timestamp := time.Duration(frameIndex) * time.Second / time.Duration(cfg.FPS)
		packet, encodeErr := encodeFrame(timestamp)
		if encodeErr != nil {
			return report, encodeErr
		}
		if frameIndex == 0 {
			report.FirstPacketBytes = len(packet.Data)
			report.FirstPacketKeyFrame = packet.KeyFrame
			if !packet.KeyFrame {
				return report, errors.New("NVENC self-test first packet is not a key frame")
			}
		}
		bitstream := packet.Data
		if frameIndex == 0 && !H265HasParameterSets(bitstream) {
			bitstream = make([]byte, 0, len(sequenceHeader)+len(packet.Data))
			bitstream = append(bitstream, sequenceHeader...)
			bitstream = append(bitstream, packet.Data...)
		}
		frames, decodeErr := decoder.Decode(ctx, bitstream, packet.Timestamp)
		if decodeErr != nil {
			return report, decodeErr
		}
		report.DecodedFrames += len(frames)
		firstDecoded = append(firstDecoded, frames...)
	}
	if len(firstDecoded) == 0 {
		return report, errors.New("NVDEC self-test produced no display frame")
	}
	defer func() {
		closeNVDECDecodedFrames(firstDecoded)
	}()

	frame := &firstDecoded[0]
	if !frame.Hardware || frame.Format != PixelFormatAYUV || frame.D3D11 == nil {
		return report, fmt.Errorf(
			"%w: NVDEC self-test output hardware=%v format=%q d3d11=%v",
			ErrDecoderUnavailable,
			frame.Hardware,
			frame.Format,
			frame.D3D11 != nil,
		)
	}
	if frame.D3D11.Device != uintptr(device) || frame.D3D11.Resource == 0 {
		return report, fmt.Errorf("%w: NVDEC self-test returned a foreign/empty D3D11 surface", ErrDecoderUnavailable)
	}
	report.D3D11OutputValidated = true

	samples, maxChannelError, err := readNVCodecValidationAYUVSamples(
		device,
		deviceContext,
		frame.D3D11.Resource,
		frame.D3D11.Subresource,
		cfg.Width,
		cfg.Height,
	)
	if err != nil {
		return report, err
	}
	report.SamplesChecked = samples
	report.MaxChannelError = maxChannelError
	report.ReadbackValidated = true
	closeNVDECDecodedFrames(firstDecoded)
	firstDecoded = nil

	reconfigured := cfg
	reconfigured.TargetBitrate = report.ReconfiguredBitrate
	if err := encoder.Reconfigure(ctx, reconfigured); err != nil {
		return report, err
	}
	reconfigurePacket, err := encodeFrame(4 * time.Second / time.Duration(cfg.FPS))
	if err != nil {
		return report, err
	}
	report.ReconfigurePacketKeyFrame = reconfigurePacket.KeyFrame
	if !reconfigurePacket.KeyFrame {
		return report, errors.New("NVENC bitrate reconfigure did not force an IDR frame")
	}
	reconfigureFrames, err := decoder.Decode(ctx, reconfigurePacket.Data, reconfigurePacket.Timestamp)
	if err != nil {
		return report, err
	}
	report.DecodedFrames += len(reconfigureFrames)
	if len(reconfigureFrames) == 0 {
		return report, errors.New("NVDEC produced no frame after bitrate reconfigure")
	}
	closeNVDECDecodedFrames(reconfigureFrames)
	report.BitrateReconfigureValidated = true

	if err := encoder.ForceIDR(ctx); err != nil {
		return report, err
	}
	forceIDRPacket, err := encodeFrame(5 * time.Second / time.Duration(cfg.FPS))
	if err != nil {
		return report, err
	}
	report.ForceIDRPacketKeyFrame = forceIDRPacket.KeyFrame
	if !forceIDRPacket.KeyFrame {
		return report, errors.New("NVENC explicit ForceIDR did not produce a key frame")
	}
	report.ForceIDRValidated = true

	report.Passed = true
	return report, nil
}
