//go:build windows

package codec

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	imfSampleGetSampleTime       = 35
	imfSampleSetSampleTime       = 36
	imfSampleSetSampleDuration   = 38
	imfSampleConvertToContiguous = 41
	imfSampleAddBuffer           = 42

	imfMediaBufferLock             = 3
	imfMediaBufferUnlock           = 4
	imfMediaBufferGetCurrentLength = 5
	imfMediaBufferSetCurrentLength = 6

	mfENotAccepting           = 0xc00d36b5
	mfETransformNeedMoreInput = 0xc00d6d72
)

var mfSampleExtensionCleanPoint = windows.GUID{
	Data1: 0x9cdf01d8, Data2: 0xa0f0, Data3: 0x43ba,
	Data4: [8]byte{0xb0, 0x77, 0xea, 0xa0, 0x6c, 0xbd, 0x72, 0x8a},
}

func durationToHNS(value time.Duration) int64 {
	return int64(value / (100 * time.Nanosecond))
}

func hnsToDuration(value int64) time.Duration {
	return time.Duration(value) * 100 * time.Nanosecond
}

func createMFSample() (unsafe.Pointer, error) {
	var sample unsafe.Pointer
	hr, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(&sample)))
	if hresultFailed(hr) {
		return nil, hresultError("MFCreateSample", hr)
	}
	if sample == nil {
		return nil, errors.New("MFCreateSample returned nil")
	}
	return sample, nil
}

func createMFBuffer(size, alignment uint32) (unsafe.Pointer, error) {
	if size == 0 {
		return nil, errors.New("Media Foundation buffer size is zero")
	}
	var buffer unsafe.Pointer
	var hr uintptr
	if alignment != 0 {
		hr, _, _ = procMFCreateAlignedMemoryBuffer.Call(
			uintptr(size),
			uintptr(alignment),
			uintptr(unsafe.Pointer(&buffer)),
		)
	} else {
		hr, _, _ = procMFCreateMemoryBuffer.Call(
			uintptr(size),
			uintptr(unsafe.Pointer(&buffer)),
		)
	}
	if hresultFailed(hr) {
		if alignment != 0 {
			return nil, hresultError("MFCreateAlignedMemoryBuffer", hr)
		}
		return nil, hresultError("MFCreateMemoryBuffer", hr)
	}
	if buffer == nil {
		return nil, errors.New("Media Foundation buffer allocation returned nil")
	}
	return buffer, nil
}

func writeMFBuffer(buffer unsafe.Pointer, data []byte) error {
	var ptr unsafe.Pointer
	var maxLength, currentLength uint32
	hr := comCall(
		buffer,
		imfMediaBufferLock,
		uintptr(unsafe.Pointer(&ptr)),
		uintptr(unsafe.Pointer(&maxLength)),
		uintptr(unsafe.Pointer(&currentLength)),
	)
	if hresultFailed(hr) {
		return hresultError("IMFMediaBuffer.Lock", hr)
	}
	if ptr == nil || uint32(len(data)) > maxLength {
		comCall(buffer, imfMediaBufferUnlock)
		return fmt.Errorf("Media Foundation buffer capacity %d is smaller than %d", maxLength, len(data))
	}
	copy(unsafe.Slice((*byte)(ptr), int(maxLength)), data)
	unlockHR := comCall(buffer, imfMediaBufferUnlock)
	if hresultFailed(unlockHR) {
		return hresultError("IMFMediaBuffer.Unlock", unlockHR)
	}
	hr = comCall(buffer, imfMediaBufferSetCurrentLength, uintptr(uint32(len(data))))
	if hresultFailed(hr) {
		return hresultError("IMFMediaBuffer.SetCurrentLength", hr)
	}
	return nil
}

func readMFBuffer(buffer unsafe.Pointer) ([]byte, error) {
	var length uint32
	hr := comCall(buffer, imfMediaBufferGetCurrentLength, uintptr(unsafe.Pointer(&length)))
	if hresultFailed(hr) {
		return nil, hresultError("IMFMediaBuffer.GetCurrentLength", hr)
	}
	if length == 0 {
		return nil, nil
	}
	var ptr unsafe.Pointer
	var maxLength, currentLength uint32
	hr = comCall(
		buffer,
		imfMediaBufferLock,
		uintptr(unsafe.Pointer(&ptr)),
		uintptr(unsafe.Pointer(&maxLength)),
		uintptr(unsafe.Pointer(&currentLength)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IMFMediaBuffer.Lock", hr)
	}
	if ptr == nil || length > maxLength {
		comCall(buffer, imfMediaBufferUnlock)
		return nil, errors.New("Media Foundation returned an invalid buffer")
	}
	data := append([]byte(nil), unsafe.Slice((*byte)(ptr), int(length))...)
	unlockHR := comCall(buffer, imfMediaBufferUnlock)
	if hresultFailed(unlockHR) {
		return nil, hresultError("IMFMediaBuffer.Unlock", unlockHR)
	}
	return data, nil
}

func addSampleBuffer(sample, buffer unsafe.Pointer) error {
	hr := comCall(sample, imfSampleAddBuffer, uintptr(buffer))
	if hresultFailed(hr) {
		return hresultError("IMFSample.AddBuffer", hr)
	}
	return nil
}

func setSampleTiming(sample unsafe.Pointer, timestamp, duration time.Duration) error {
	hr := comCall(sample, imfSampleSetSampleTime, uintptr(durationToHNS(timestamp)))
	if hresultFailed(hr) {
		return hresultError("IMFSample.SetSampleTime", hr)
	}
	hr = comCall(sample, imfSampleSetSampleDuration, uintptr(durationToHNS(duration)))
	if hresultFailed(hr) {
		return hresultError("IMFSample.SetSampleDuration", hr)
	}
	return nil
}

func createInputSample(data []byte, timestamp, duration time.Duration) (unsafe.Pointer, error) {
	sample, err := createMFSample()
	if err != nil {
		return nil, err
	}
	buffer, err := createMFBuffer(uint32(len(data)), 0)
	if err != nil {
		releaseIUnknown(sample)
		return nil, err
	}
	if err := writeMFBuffer(buffer, data); err != nil {
		releaseIUnknown(buffer)
		releaseIUnknown(sample)
		return nil, err
	}
	if err := addSampleBuffer(sample, buffer); err != nil {
		releaseIUnknown(buffer)
		releaseIUnknown(sample)
		return nil, err
	}
	releaseIUnknown(buffer)
	if err := setSampleTiming(sample, timestamp, duration); err != nil {
		releaseIUnknown(sample)
		return nil, err
	}
	return sample, nil
}

func createD3D11InputSample(frame D3D11EncodeFrame, duration time.Duration) (unsafe.Pointer, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	var buffer unsafe.Pointer
	hr, _, _ := procMFCreateDXGISurfaceBuffer.Call(
		uintptr(unsafe.Pointer(&iidID3D11Texture2D)),
		frame.Resource,
		uintptr(frame.Subresource),
		0,
		uintptr(unsafe.Pointer(&buffer)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("MFCreateDXGISurfaceBuffer", hr)
	}
	if buffer == nil {
		return nil, errors.New("MFCreateDXGISurfaceBuffer returned nil")
	}
	defer releaseIUnknown(buffer)

	sample, err := createMFSample()
	if err != nil {
		return nil, err
	}
	if err := addSampleBuffer(sample, buffer); err != nil {
		releaseIUnknown(sample)
		return nil, err
	}
	if err := setSampleTiming(sample, frame.Timestamp, duration); err != nil {
		releaseIUnknown(sample)
		return nil, err
	}
	return sample, nil
}

func createEncodeInputSample(input mfEncodeInput) (unsafe.Pointer, error) {
	if input.surface != nil {
		return createD3D11InputSample(*input.surface, input.duration)
	}
	return createInputSample(input.data, input.timestamp, input.duration)
}

func createOutputSample(size, alignment uint32) (unsafe.Pointer, error) {
	sample, err := createMFSample()
	if err != nil {
		return nil, err
	}
	buffer, err := createMFBuffer(size, alignment)
	if err != nil {
		releaseIUnknown(sample)
		return nil, err
	}
	if err := addSampleBuffer(sample, buffer); err != nil {
		releaseIUnknown(buffer)
		releaseIUnknown(sample)
		return nil, err
	}
	releaseIUnknown(buffer)
	return sample, nil
}

func sampleBytes(sample unsafe.Pointer) ([]byte, error) {
	if sample == nil {
		return nil, errors.New("nil Media Foundation sample")
	}
	var buffer unsafe.Pointer
	hr := comCall(sample, imfSampleConvertToContiguous, uintptr(unsafe.Pointer(&buffer)))
	if hresultFailed(hr) {
		return nil, hresultError("IMFSample.ConvertToContiguousBuffer", hr)
	}
	if buffer == nil {
		return nil, errors.New("IMFSample.ConvertToContiguousBuffer returned nil")
	}
	defer releaseIUnknown(buffer)
	return readMFBuffer(buffer)
}

func sampleTimestamp(sample unsafe.Pointer, fallback time.Duration) time.Duration {
	var value int64
	hr := comCall(sample, imfSampleGetSampleTime, uintptr(unsafe.Pointer(&value)))
	if hresultFailed(hr) {
		return fallback
	}
	return hnsToDuration(value)
}

func sampleIsCleanPoint(sample unsafe.Pointer) bool {
	value, err := attributeGetUINT32(sample, &mfSampleExtensionCleanPoint)
	return err == nil && value != 0
}
