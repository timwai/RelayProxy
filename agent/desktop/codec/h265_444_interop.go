package codec

import (
	"errors"
	"fmt"
)

const (
	H265444InteropD3D11Native = "d3d11-native"
	H265444InteropD3D11CUDA   = "d3d11-cuda"
	H265444InteropNVCodec     = "d3d11-nvcodec"
)

// H265444BackendLifecycleContract documents ownership rules that every
// production HEVC 4:4:4 backend must honor. A session owns its codec context
// and any interop registrations it creates; Close must be safe to call more
// than once and must release those resources before returning.
type H265444BackendLifecycleContract struct {
	SessionOwnsCodecContext     bool
	SessionOwnsInteropResources bool
	CloseIdempotent             bool
	CloseReleasesOwnedResources bool
}

func (c H265444BackendLifecycleContract) Validate() error {
	switch {
	case !c.SessionOwnsCodecContext:
		return errors.New("codec session ownership is not defined")
	case !c.SessionOwnsInteropResources:
		return errors.New("interop resource ownership is not defined")
	case !c.CloseIdempotent:
		return errors.New("backend Close must be idempotent")
	case !c.CloseReleasesOwnedResources:
		return errors.New("backend Close must release owned resources")
	default:
		return nil
	}
}

// H265444D3D11InteropContract is the zero-copy boundary shared by D3D11
// capture, a vendor codec backend, and the native viewer.
//
// Encoder input resources are borrowed from the caller for the duration of an
// EncodeD3D11 call. Decoder output resources are owned by the backend until the
// corresponding DecodedFrame is closed. Keeping these rules explicit is
// especially important for D3D11<->CUDA registration where stale registrations
// can keep textures and CUDA contexts alive after a desktop session ends.
type H265444D3D11InteropContract struct {
	Bridge                      string
	EncodeInput                 PixelFormat
	DecodeOutput                PixelFormat
	RequireSameD3D11Device      bool
	EncoderBorrowsInputResource bool
	DecoderOwnsOutputUntilClose bool
}

func (c H265444D3D11InteropContract) Validate() error {
	if c.Bridge == "" {
		return errors.New("D3D11 interop bridge is not defined")
	}
	if c.EncodeInput != PixelFormatAYUV {
		return fmt.Errorf("HEVC 4:4:4 encode input must be AYUV, got %q", c.EncodeInput)
	}
	if c.DecodeOutput != PixelFormatAYUV {
		return fmt.Errorf("HEVC 4:4:4 decode output must be AYUV, got %q", c.DecodeOutput)
	}
	if !c.RequireSameD3D11Device {
		return errors.New("D3D11 interop must stay on the session device")
	}
	if !c.EncoderBorrowsInputResource {
		return errors.New("encoder input ownership must remain with the caller")
	}
	if !c.DecoderOwnsOutputUntilClose {
		return errors.New("decoder output ownership must extend until frame Close")
	}
	return nil
}

func h265444SessionLifecycleContract() *H265444BackendLifecycleContract {
	return &H265444BackendLifecycleContract{
		SessionOwnsCodecContext:     true,
		SessionOwnsInteropResources: true,
		CloseIdempotent:             true,
		CloseReleasesOwnedResources: true,
	}
}

func h265444D3D11NativeAYUVContract() *H265444D3D11InteropContract {
	return &H265444D3D11InteropContract{
		Bridge:                      H265444InteropD3D11Native,
		EncodeInput:                 PixelFormatAYUV,
		DecodeOutput:                PixelFormatAYUV,
		RequireSameD3D11Device:      true,
		EncoderBorrowsInputResource: true,
		DecoderOwnsOutputUntilClose: true,
	}
}

func h265444D3D11CUDAAYUVContract() *H265444D3D11InteropContract {
	return &H265444D3D11InteropContract{
		Bridge:                      H265444InteropD3D11CUDA,
		EncodeInput:                 PixelFormatAYUV,
		DecodeOutput:                PixelFormatAYUV,
		RequireSameD3D11Device:      true,
		EncoderBorrowsInputResource: true,
		DecoderOwnsOutputUntilClose: true,
	}
}

func h265444NVCodecAYUVContract() *H265444D3D11InteropContract {
	return &H265444D3D11InteropContract{
		Bridge:                      H265444InteropNVCodec,
		EncodeInput:                 PixelFormatAYUV,
		DecodeOutput:                PixelFormatAYUV,
		RequireSameD3D11Device:      true,
		EncoderBorrowsInputResource: true,
		DecoderOwnsOutputUntilClose: true,
	}
}

func (b h265444Backend) productionGateError() error {
	if !b.productionReady {
		return errors.New("production backend is not implemented")
	}
	if !b.zeroCopyValidated {
		return errors.New("production backend zero-copy path is not validated")
	}
	if b.lifecycle == nil {
		return errors.New("production backend lifecycle contract is missing")
	}
	if err := b.lifecycle.Validate(); err != nil {
		return fmt.Errorf("invalid production backend lifecycle contract: %w", err)
	}
	if b.interop == nil {
		return errors.New("production backend D3D11 interop contract is missing")
	}
	if err := b.interop.Validate(); err != nil {
		return fmt.Errorf("invalid production backend D3D11 interop contract: %w", err)
	}
	return nil
}
