package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"relayproxy/internal/config"
)

const agentConfigName = "relay-agent.yaml"

func resolveConfigPath(requested string, explicit bool) (string, error) {
	if explicit {
		if strings.TrimSpace(requested) == "" {
			return "", fmt.Errorf("--config must specify a file path")
		}
		return filepath.Abs(requested)
	}
	return defaultConfigPath()
}

// Windows keeps mutable settings with the user, independent of installation
// directory, shortcut working directory, or which agent executable is used.
func defaultConfigPath() (string, error) {
	if runtime.GOOS == "windows" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get user home directory: %w", err)
		}
		if !filepath.IsAbs(homeDir) {
			return "", fmt.Errorf("user home directory must be an absolute path")
		}
		return filepath.Join(homeDir, ".relayproxy", agentConfigName), nil
	}
	return filepath.Abs(filepath.Join("configs", agentConfigName))
}

func legacyConfigPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "configs", agentConfigName))
	}
	if cwd, err := os.Getwd(); err == nil {
		path := filepath.Join(cwd, "configs", agentConfigName)
		if len(paths) == 0 || paths[0] != path {
			paths = append(paths, path)
		}
	}
	return paths
}

// loadOrCreateAgentConfig runs before the agent starts, under its instance
// lock. Existing or invalid files are never replaced by generated defaults.
// Only a default Windows launch supplies legacy paths for one-time migration.
func loadOrCreateAgentConfig(path string, _ []string) (*config.AgentConfigFile, error) {
	cfg, err := config.LoadAgentConfig(path)
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	cfg = defaultConfigFile()
	if err := config.NormalizeAgentConfig(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create configuration directory: %w", err)
	}
	if err := config.SaveBootstrapAgentConfig(path, cfg.Server.Address); err != nil {
		return nil, err
	}
	return cfg, nil
}
