//go:build !windows

package startup

func SetAutoStart(appName, exePath, configPath string, enable, requireAdmin bool) error {
	return nil
}

func SyncAutoStart(appName, exePath, configPath string, requireAdmin bool) (func() error, error) {
	return nil, nil
}

func AutoStartCommand(appName string) string {
	return ""
}

func IsAutoStartEnabled(appName string) bool {
	return false
}
