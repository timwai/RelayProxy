//go:build !windows

package divert

func NetworkServiceModeFlagName() string   { return "relayproxy-network-service" }
func NetworkServiceSIDFlagName() string    { return "relayproxy-network-service-sid" }
func NetworkServiceHelperFlagName() string { return "relayproxy-network-service-helper" }

func RunWindowsNetworkService(string) error               { return nil }
func RunWindowsNetworkServiceHelper(string, string) error { return nil }
func EnsurePlatformService() error                        { return nil }
func WindowsNetworkServiceInstalled() bool                { return false }
func PlatformServiceReady() bool                          { return true }


func GetPlatformServiceStatus() NetworkServiceStatus {
	return NetworkServiceStatus{Supported: false, State: "unsupported"}
}

func RepairPlatformService() error    { return nil }
func UninstallPlatformService() error { return nil }
