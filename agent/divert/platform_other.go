//go:build !windows && !linux && !darwin

package divert

import "runtime"

func platformCapabilities() Capabilities {
	return Capabilities{
		Platform:          runtime.GOOS,
		UnavailableReason: "no system interception adapter is implemented for this platform",
	}
}
