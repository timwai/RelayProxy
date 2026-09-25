package codec

import (
	"context"
	"errors"
)

const H265444BackendOneVPL = "onevpl-hevc444"

// H265444BackendProbe is the vendor-neutral runtime capability used by Relay
// Desktop for HEVC 8-bit 4:4:4 backends. A backend must be backed by an actual
// runtime probe; callers must not synthesize support from GPU model names.
type H265444BackendProbe struct {
	Backend         string
	HardwareRuntime bool

	// Encode / Decode mean that RelayProxy has at least one implemented path for
	// the probed hardware direction. The path-specific fields below distinguish
	// system-memory from D3D11-only backends so global capability advertisement
	// cannot confuse opener presence with a validated transport path.
	Encode bool
	Decode bool

	SystemMemoryEncode bool
	SystemMemoryDecode bool
	D3D11Encode        bool
	D3D11Decode        bool
	Error              string
}

func (p H265444BackendProbe) SystemMemoryEndToEnd() bool {
	return p.Backend != "" && p.HardwareRuntime && p.SystemMemoryEncode && p.SystemMemoryDecode
}

func (p H265444BackendProbe) D3D11EndToEnd() bool {
	return p.Backend != "" && p.HardwareRuntime && p.D3D11Encode && p.D3D11Decode
}

func (p H265444BackendProbe) EndToEnd() bool {
	return p.SystemMemoryEndToEnd() || p.D3D11EndToEnd()
}

type h265444Backend struct {
	name string

	probe func(context.Context) H265444BackendProbe

	openEncoder       func(context.Context, VideoConfig) (SequenceHeaderEncoder, error)
	openEncoderD3D11  func(context.Context, VideoConfig, uintptr) (SequenceHeaderEncoder, error)
	openDecoder       func(context.Context, VideoConfig) (Decoder, error)
	openDecoderD3D11  func(context.Context, VideoConfig, uintptr) (Decoder, error)
	probeDecoderD3D11 func(context.Context, VideoConfig, uintptr) error
}

func probeOneVPLH265444Backend(ctx context.Context) H265444BackendProbe {
	probe := ProbeOneVPLHEVC444(ctx)
	return H265444BackendProbe{
		Backend:         H265444BackendOneVPL,
		HardwareRuntime: probe.DispatcherAvailable && probe.HardwareRuntime,
		Encode:          probe.HEVC444Encode,
		Decode:          probe.HEVC444Decode,
		Error:           probe.Error,
	}
}

var h265444BackendRegistry = []h265444Backend{
	{
		name:              H265444BackendOneVPL,
		probe:             probeOneVPLH265444Backend,
		openEncoder:       OpenOneVPLH265Encoder,
		openEncoderD3D11:  OpenOneVPLH265EncoderWithD3D11,
		openDecoder:       OpenOneVPLH265Decoder,
		openDecoderD3D11:  OpenOneVPLH265DecoderWithD3D11,
		probeDecoderD3D11: ProbeOneVPLH265DecoderD3D11,
	},
}

// ProbeH265444Backends probes every registered vendor backend in deterministic
// priority order. The result includes unavailable backends so diagnostics can
// explain why a backend was not selected.
func ProbeH265444Backends(ctx context.Context) []H265444BackendProbe {
	return probeH265444Backends(ctx, h265444BackendRegistry)
}

func probeH265444Backends(
	ctx context.Context,
	backends []h265444Backend,
) []H265444BackendProbe {
	out := make([]H265444BackendProbe, 0, len(backends))
	for _, backend := range backends {
		if err := ctx.Err(); err != nil {
			out = append(out, H265444BackendProbe{
				Backend: backend.name,
				Error:   err.Error(),
			})
			break
		}
		probe := H265444BackendProbe{Backend: backend.name}
		if backend.probe == nil {
			probe.Error = "runtime probe is unavailable"
		} else {
			probe = backend.probe(ctx)
			if probe.Backend == "" {
				probe.Backend = backend.name
			}
		}
		// Runtime support alone is not enough to expose a direction. Preserve the
		// vendor probe result only when RelayProxy has a concrete opener for at
		// least one compatible path. D3D11 decode also requires the explicit
		// device probe hook used by the Host AYUV end-to-end validation.
		rawEncode := probe.Encode
		rawDecode := probe.Decode
		probe.SystemMemoryEncode = rawEncode && backend.openEncoder != nil
		probe.SystemMemoryDecode = rawDecode && backend.openDecoder != nil
		probe.D3D11Encode = rawEncode && backend.openEncoderD3D11 != nil
		probe.D3D11Decode = rawDecode &&
			backend.openDecoderD3D11 != nil &&
			backend.probeDecoderD3D11 != nil
		probe.Encode = probe.SystemMemoryEncode || probe.D3D11Encode
		probe.Decode = probe.SystemMemoryDecode || probe.D3D11Decode
		out = append(out, probe)
	}
	return out
}

func H265444EncodeAvailable(probes []H265444BackendProbe) bool {
	for _, probe := range probes {
		if probe.HardwareRuntime && probe.Encode {
			return true
		}
	}
	return false
}

func H265444DecodeAvailable(probes []H265444BackendProbe) bool {
	for _, probe := range probes {
		if probe.HardwareRuntime && probe.Decode {
			return true
		}
	}
	return false
}

func H265444SystemMemoryEncodeAvailable(probes []H265444BackendProbe) bool {
	for _, probe := range probes {
		if probe.HardwareRuntime && probe.SystemMemoryEncode {
			return true
		}
	}
	return false
}

func H265444SystemMemoryDecodeAvailable(probes []H265444BackendProbe) bool {
	for _, probe := range probes {
		if probe.HardwareRuntime && probe.SystemMemoryDecode {
			return true
		}
	}
	return false
}

func H265444SystemMemoryEndToEndAvailable(probes []H265444BackendProbe) bool {
	return H265444SystemMemoryEncodeAvailable(probes) &&
		H265444SystemMemoryDecodeAvailable(probes)
}

func H265444D3D11EncodeAvailable(probes []H265444BackendProbe) bool {
	for _, probe := range probes {
		if probe.HardwareRuntime && probe.D3D11Encode {
			return true
		}
	}
	return false
}

func H265444D3D11DecodeAvailable(probes []H265444BackendProbe) bool {
	for _, probe := range probes {
		if probe.HardwareRuntime && probe.D3D11Decode {
			return true
		}
	}
	return false
}

func H265444D3D11EndToEndAvailable(probes []H265444BackendProbe) bool {
	return H265444D3D11EncodeAvailable(probes) &&
		H265444D3D11DecodeAvailable(probes)
}

func H265444EndToEndAvailable(probes []H265444BackendProbe) bool {
	return H265444SystemMemoryEndToEndAvailable(probes) ||
		H265444D3D11EndToEndAvailable(probes)
}

func h265444BackendErrors(unavailable error, errs []error) error {
	if len(errs) == 0 {
		return unavailable
	}
	all := make([]error, 0, len(errs)+1)
	all = append(all, unavailable)
	all = append(all, errs...)
	return errors.Join(all...)
}

func OpenH265444Encoder(ctx context.Context, cfg VideoConfig) (SequenceHeaderEncoder, error) {
	var errs []error
	for _, backend := range h265444BackendRegistry {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if backend.openEncoder == nil {
			continue
		}
		encoder, err := backend.openEncoder(ctx, cfg)
		if err == nil && encoder != nil {
			return encoder, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return nil, h265444BackendErrors(ErrEncoderUnavailable, errs)
}

func OpenH265444EncoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (SequenceHeaderEncoder, error) {
	var errs []error
	for _, backend := range h265444BackendRegistry {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if backend.openEncoderD3D11 == nil {
			continue
		}
		encoder, err := backend.openEncoderD3D11(ctx, cfg, device)
		if err == nil && encoder != nil {
			return encoder, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return nil, h265444BackendErrors(ErrEncoderUnavailable, errs)
}

func OpenH265444Decoder(ctx context.Context, cfg VideoConfig) (Decoder, error) {
	var errs []error
	for _, backend := range h265444BackendRegistry {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if backend.openDecoder == nil {
			continue
		}
		decoder, err := backend.openDecoder(ctx, cfg)
		if err == nil && decoder != nil {
			return decoder, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return nil, h265444BackendErrors(ErrDecoderUnavailable, errs)
}

func OpenH265444DecoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (Decoder, error) {
	var errs []error
	for _, backend := range h265444BackendRegistry {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if backend.openDecoderD3D11 == nil {
			continue
		}
		decoder, err := backend.openDecoderD3D11(ctx, cfg, device)
		if err == nil && decoder != nil {
			return decoder, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return nil, h265444BackendErrors(ErrDecoderUnavailable, errs)
}

func ProbeH265444DecoderD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) error {
	var errs []error
	for _, backend := range h265444BackendRegistry {
		if err := ctx.Err(); err != nil {
			return err
		}
		if backend.probeDecoderD3D11 == nil {
			continue
		}
		if err := backend.probeDecoderD3D11(ctx, cfg, device); err == nil {
			return nil
		} else {
			errs = append(errs, err)
		}
	}
	return h265444BackendErrors(ErrDecoderUnavailable, errs)
}
