//go:build windows && amd64

package codec

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

const (
	nvencPicParamsSize     = 3360
	nvencLockBitstreamSize = 1552
	nvencSequenceParamSize = 1544

	nvencPicVersionOffset         = 0
	nvencPicInputWidthOffset      = 4
	nvencPicInputHeightOffset     = 8
	nvencPicEncodeFlagsOffset     = 16
	nvencPicFrameIndexOffset      = 20
	nvencPicInputTimestampOffset  = 24
	nvencPicInputDurationOffset   = 32
	nvencPicInputBufferOffset     = 40
	nvencPicOutputBitstreamOffset = 48
	nvencPicBufferFormatOffset    = 64
	nvencPicPictureStructOffset   = 68

	nvencLockVersionOffset       = 0
	nvencLockOutputBitstream     = 8
	nvencLockBitstreamSizeOffset = 36
	nvencLockBitstreamPtrOffset  = 56
	nvencLockPictureTypeOffset   = 64

	nvencSequenceVersionOffset = 0
	nvencSequenceInputSize     = 4
	nvencSequenceBuffer        = 16
	nvencSequenceOutputSize    = 24

	nvencPicStructFrame uint32 = 1

	nvencPicTypeI   uint32 = 2
	nvencPicTypeIDR uint32 = 3

	nvencPicFlagForceIDR         uint32 = 0x2
	nvencPicFlagOutputSPSPPS     uint32 = 0x4
	nvencSequenceBufferBytes            = 64 * 1024
	nvencMaxLockedBitstreamBytes        = 64 * 1024 * 1024
)

type nvencPicParamsBlob struct {
	_    [0]uintptr
	Data [nvencPicParamsSize]byte
}

type nvencLockBitstreamBlob struct {
	_    [0]uintptr
	Data [nvencLockBitstreamSize]byte
}

type nvencSequenceParamBlob struct {
	_    [0]uintptr
	Data [nvencSequenceParamSize]byte
}

type nvencH265Encoder struct {
	mu sync.Mutex

	session   *nvencD3D11Session
	bitstream *nvencBitstreamBuffer
	cfg       VideoConfig
	sequence  []byte
	forceIDR  bool
	frameID   uint32
	stats     EncoderStats
	closed    bool
}

func nvencH265KeyFrame(data []byte, pictureType uint32) bool {
	if pictureType == nvencPicTypeI || pictureType == nvencPicTypeIDR {
		return true
	}
	for _, nal := range annexBNALUnits(data) {
		typ := H265NALUnitType(nal)
		if typ >= 16 && typ <= 21 {
			return true
		}
	}
	return false
}

func nvencH265ParameterSets(data []byte) []byte {
	var out []byte
	for _, nal := range annexBNALUnits(data) {
		switch H265NALUnitType(nal) {
		case 32, 33, 34:
			out = append(out, 0, 0, 0, 1)
			out = append(out, nal...)
		}
	}
	if H265HasParameterSets(out) {
		return out
	}
	return nil
}

func (s *nvencD3D11Session) sequenceHeader() ([]byte, error) {
	if s == nil {
		return nil, ErrEncoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 || !s.initialized {
		return nil, ErrEncoderUnavailable
	}

	buffer := make([]byte, nvencSequenceBufferBytes)
	var outputSize uint32
	params := &nvencSequenceParamBlob{}
	binary.LittleEndian.PutUint32(
		params.Data[nvencSequenceVersionOffset:nvencSequenceVersionOffset+4],
		nvencStructVersion(1),
	)
	binary.LittleEndian.PutUint32(
		params.Data[nvencSequenceInputSize:nvencSequenceInputSize+4],
		uint32(len(buffer)),
	)
	binary.LittleEndian.PutUint64(
		params.Data[nvencSequenceBuffer:nvencSequenceBuffer+8],
		uint64(uintptr(unsafe.Pointer(&buffer[0]))),
	)
	binary.LittleEndian.PutUint64(
		params.Data[nvencSequenceOutputSize:nvencSequenceOutputSize+8],
		uint64(uintptr(unsafe.Pointer(&outputSize))),
	)

	status := nvencCall(
		s.api.NvEncGetSequenceParams,
		s.encoder,
		uintptr(unsafe.Pointer(&params.Data[0])),
	)
	runtime.KeepAlive(params)
	runtime.KeepAlive(buffer)
	runtime.KeepAlive(&outputSize)
	if status != 0 {
		return nil, fmt.Errorf("%w: nvEncGetSequenceParams returned %d", ErrEncoderUnavailable, status)
	}
	if outputSize == 0 || outputSize > uint32(len(buffer)) {
		return nil, fmt.Errorf(
			"%w: NVENC sequence payload size=%d capacity=%d",
			ErrEncoderUnavailable,
			outputSize,
			len(buffer),
		)
	}
	sequence := append([]byte(nil), buffer[:outputSize]...)
	if !H265HasParameterSets(sequence) {
		return nil, fmt.Errorf("%w: NVENC sequence payload is missing HEVC VPS/SPS/PPS", ErrEncoderUnavailable)
	}
	return sequence, nil
}

func OpenNVENCH265EncoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (SequenceHeaderEncoder, error) {
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	if normalized.Chroma != Chroma444 || normalized.BitDepth != 8 {
		return nil, fmt.Errorf("%w: NVENC HEVC requires 8-bit 4:4:4 video", ErrInvalidVideoConfig)
	}

	session, err := openNVENCHEVC444D3D11Session(ctx, device)
	if err != nil {
		return nil, err
	}
	cleanupSession := true
	defer func() {
		if cleanupSession {
			_ = session.Close()
		}
	}()

	normalized, err = session.initializeHEVC444(ctx, normalized)
	if err != nil {
		return nil, err
	}
	bitstream, err := session.createBitstreamBuffer()
	if err != nil {
		return nil, err
	}
	cleanupBitstream := true
	defer func() {
		if cleanupBitstream {
			_ = bitstream.Close()
		}
	}()

	sequence, err := session.sequenceHeader()
	if err != nil {
		return nil, err
	}
	encoder := &nvencH265Encoder{
		session:   session,
		bitstream: bitstream,
		cfg:       normalized,
		sequence:  sequence,
		forceIDR:  true,
		stats: EncoderStats{
			Hardware: true,
			Backend:  "nvenc-hevc444-d3d11",
		},
	}
	cleanupBitstream = false
	cleanupSession = false
	return encoder, nil
}

func (e *nvencH265Encoder) Encode(context.Context, RawFrame) ([]EncodedPacket, error) {
	return nil, fmt.Errorf("%w: NVENC HEVC 4:4:4 accepts D3D11 frames only", ErrEncoderUnavailable)
}

func (e *nvencH265Encoder) EncodeD3D11(
	ctx context.Context,
	frame D3D11EncodeFrame,
) ([]EncodedPacket, error) {
	if e == nil {
		return nil, ErrEncoderUnavailable
	}
	start := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == nil || e.bitstream == nil {
		return nil, ErrEncoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	if frame.Width != e.cfg.Width || frame.Height != e.cfg.Height {
		return nil, fmt.Errorf(
			"%w: NVENC frame=%dx%d encoder=%dx%d",
			ErrInvalidFrame,
			frame.Width,
			frame.Height,
			e.cfg.Width,
			e.cfg.Height,
		)
	}
	if frame.PixelFormat() != PixelFormatAYUV {
		return nil, fmt.Errorf("%w: NVENC HEVC 4:4:4 requires AYUV input", ErrInvalidFrame)
	}

	resource, err := e.session.registerAYUVD3D11Input(frame)
	if err != nil {
		return nil, err
	}
	defer resource.Close()

	mapped, mappedFormat, err := resource.Map()
	if err != nil {
		return nil, err
	}
	output := e.bitstream.Handle()
	if output == 0 {
		return nil, ErrEncoderUnavailable
	}

	params := &nvencPicParamsBlob{}
	binary.LittleEndian.PutUint32(
		params.Data[nvencPicVersionOffset:nvencPicVersionOffset+4],
		nvencVersionWithReservedBit(7),
	)
	binary.LittleEndian.PutUint32(params.Data[nvencPicInputWidthOffset:nvencPicInputWidthOffset+4], uint32(frame.Width))
	binary.LittleEndian.PutUint32(params.Data[nvencPicInputHeightOffset:nvencPicInputHeightOffset+4], uint32(frame.Height))
	flags := uint32(0)
	if e.forceIDR {
		flags = nvencPicFlagForceIDR | nvencPicFlagOutputSPSPPS
	}
	binary.LittleEndian.PutUint32(params.Data[nvencPicEncodeFlagsOffset:nvencPicEncodeFlagsOffset+4], flags)
	binary.LittleEndian.PutUint32(params.Data[nvencPicFrameIndexOffset:nvencPicFrameIndexOffset+4], e.frameID)
	binary.LittleEndian.PutUint64(params.Data[nvencPicInputTimestampOffset:nvencPicInputTimestampOffset+8], uint64(frame.Timestamp.Nanoseconds()))
	frameDuration := time.Second / time.Duration(e.cfg.FPS)
	binary.LittleEndian.PutUint64(params.Data[nvencPicInputDurationOffset:nvencPicInputDurationOffset+8], uint64(frameDuration.Nanoseconds()))
	binary.LittleEndian.PutUint64(params.Data[nvencPicInputBufferOffset:nvencPicInputBufferOffset+8], uint64(mapped))
	binary.LittleEndian.PutUint64(params.Data[nvencPicOutputBitstreamOffset:nvencPicOutputBitstreamOffset+8], uint64(output))
	binary.LittleEndian.PutUint32(params.Data[nvencPicBufferFormatOffset:nvencPicBufferFormatOffset+4], uint32(mappedFormat))
	binary.LittleEndian.PutUint32(params.Data[nvencPicPictureStructOffset:nvencPicPictureStructOffset+4], nvencPicStructFrame)

	api, err := e.session.API()
	if err != nil {
		return nil, err
	}
	encoderHandle := e.session.Encoder()
	if encoderHandle == 0 {
		return nil, ErrEncoderUnavailable
	}
	status := nvencCall(
		api.NvEncEncodePicture,
		encoderHandle,
		uintptr(unsafe.Pointer(&params.Data[0])),
	)
	runtime.KeepAlive(params)
	runtime.KeepAlive(frame)
	if status != 0 {
		return nil, fmt.Errorf("nvEncEncodePicture returned %d", status)
	}

	lock := &nvencLockBitstreamBlob{}
	binary.LittleEndian.PutUint32(
		lock.Data[nvencLockVersionOffset:nvencLockVersionOffset+4],
		nvencVersionWithReservedBit(2),
	)
	binary.LittleEndian.PutUint64(
		lock.Data[nvencLockOutputBitstream:nvencLockOutputBitstream+8],
		uint64(output),
	)
	status = nvencCall(
		api.NvEncLockBitstream,
		encoderHandle,
		uintptr(unsafe.Pointer(&lock.Data[0])),
	)
	runtime.KeepAlive(lock)
	if status != 0 {
		return nil, fmt.Errorf("nvEncLockBitstream returned %d", status)
	}

	size := binary.LittleEndian.Uint32(
		lock.Data[nvencLockBitstreamSizeOffset : nvencLockBitstreamSizeOffset+4],
	)
	pointer := uintptr(binary.LittleEndian.Uint64(
		lock.Data[nvencLockBitstreamPtrOffset : nvencLockBitstreamPtrOffset+8],
	))
	pictureType := binary.LittleEndian.Uint32(
		lock.Data[nvencLockPictureTypeOffset : nvencLockPictureTypeOffset+4],
	)
	var copyErr error
	var data []byte
	switch {
	case size == 0:
		copyErr = errors.New("NVENC returned an empty bitstream")
	case size > nvencMaxLockedBitstreamBytes:
		copyErr = fmt.Errorf("NVENC returned unreasonable bitstream size %d", size)
	case pointer == 0:
		copyErr = errors.New("NVENC returned a nil bitstream pointer")
	default:
		data = append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(pointer)), int(size))...)
	}
	unlockStatus := nvencCall(api.NvEncUnlockBitstream, encoderHandle, output)
	if copyErr != nil {
		return nil, copyErr
	}
	if unlockStatus != 0 {
		return nil, fmt.Errorf("nvEncUnlockBitstream returned %d", unlockStatus)
	}

	keyFrame := nvencH265KeyFrame(data, pictureType)
	if sequence := nvencH265ParameterSets(data); len(sequence) > 0 {
		e.sequence = sequence
	}
	e.forceIDR = false
	e.frameID++
	e.stats.Frames++
	e.stats.Bytes += uint64(len(data))
	e.stats.LastEncodeTime = time.Since(start)
	return []EncodedPacket{{
		Codec:     "h265",
		Data:      data,
		Timestamp: frame.Timestamp,
		KeyFrame:  keyFrame,
	}}, nil
}

func (e *nvencH265Encoder) ForceIDR(ctx context.Context) error {
	if e == nil {
		return ErrEncoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrEncoderUnavailable
	}
	e.forceIDR = true
	return nil
}

func (e *nvencH265Encoder) Reconfigure(ctx context.Context, cfg VideoConfig) error {
	if e == nil {
		return ErrEncoderUnavailable
	}
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == nil {
		return ErrEncoderUnavailable
	}
	if !bitrateOnlyReconfigure(e.cfg, normalized) {
		return ErrEncoderRebuildRequired
	}
	if normalized.TargetBitrate == e.cfg.TargetBitrate {
		return nil
	}
	updated, err := e.session.reconfigureHEVC444Bitrate(ctx, normalized)
	if err != nil {
		return err
	}
	e.cfg = updated
	e.forceIDR = true
	return nil
}

func (e *nvencH265Encoder) SequenceHeader() []byte {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]byte(nil), e.sequence...)
}

func (e *nvencH265Encoder) Stats() EncoderStats {
	if e == nil {
		return EncoderStats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}

func (e *nvencH265Encoder) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	bitstream := e.bitstream
	session := e.session
	e.bitstream = nil
	e.session = nil
	e.sequence = nil
	e.mu.Unlock()

	var err error
	if bitstream != nil {
		err = errors.Join(err, bitstream.Close())
	}
	if session != nil {
		err = errors.Join(err, session.Close())
	}
	return err
}
