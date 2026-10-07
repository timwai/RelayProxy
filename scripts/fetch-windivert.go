//go:build ignore

// Fetch the pinned, unmodified WinDivert runtime for Windows release packages.
// This tool only downloads/extracts files; it never installs or opens a driver.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"relayproxy/internal/windivert"
)

func main() {
	out := flag.String("out", "dist/windows-amd64/windivert", "Windows runtime output directory")
	embedArchive := flag.String("embed-archive", "", "Also write the verified upstream archive for go:embed")
	agentZIP := flag.String("agent-zip", "", "Also package the surrounding Windows agent directory into this ZIP")
	flag.Parse()
	err := fetchRuntime(*out, *embedArchive)
	if err == nil && *agentZIP != "" {
		err = packageAgent(filepath.Dir(filepath.Clean(*out)), *agentZIP)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// packageAgent bundles the client and its driver together. The explicit file
// list excludes server binaries/configuration and includes WinDivert's license.
func packageAgent(directory, destination string) error {
	if !strings.EqualFold(filepath.Ext(destination), ".zip") {
		return fmt.Errorf("agent archive must have a .zip extension")
	}

	// WinUI 3 self-contained publishing produces the executable plus managed
	// assemblies, Windows App SDK runtime files, PRI resources and Assets.
	// Keep the client package future-proof by taking the whole Windows client
	// directory and excluding only the server-specific artifacts.
	files := make([]string, 0, 64)
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		switch strings.ToLower(name) {
		case "relay-server.exe", "configs/relay-server.yaml":
			return nil
		}
		if strings.HasSuffix(strings.ToLower(name), ".zip") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("invalid Windows agent package file: %s", name)
		}
		files = append(files, name)
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(files)

	required := []string{
		// The user-visible WinUI client is now the rename-safe launcher. The
		// fixed-name RelayProxy.NativeHost.exe is embedded inside this EXE and
		// extracted at runtime, so it is intentionally not a package sidecar.
		"RelayProxy-agent-windows-amd64.exe",
		"relay-agent.exe",
		"configs/relay-agent.yaml",
		"windivert/WinDivert.dll",
		"windivert/WinDivert64.sys",
		"windivert/LICENSE",
	}
	present := make(map[string]struct{}, len(files))
	for _, name := range files {
		present[strings.ToLower(name)] = struct{}{}
	}
	for _, name := range required {
		if _, ok := present[strings.ToLower(name)]; !ok {
			return fmt.Errorf("incomplete Windows agent package: missing %s", name)
		}
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".relay-agent-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()

	archive := zip.NewWriter(temporary)
	defer archive.Close()
	var sums strings.Builder
	for _, name := range files {
		source, err := os.Open(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		entry, err := archive.Create("windows-amd64/" + name)
		if err != nil {
			_ = source.Close()
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(entry, digest), source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(&sums, "%x  %s\n", digest.Sum(nil), name)
	}
	manifest, err := archive.Create("windows-amd64/SHA256SUMS.txt")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(manifest, sums.String()); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return err
	}
	fmt.Printf("Windows native agent + WinDivert package: %s\n", destination)
	return nil
}

func fetchRuntime(out, embedArchive string) error {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	cache := filepath.Join(cacheRoot, "RelayProxy", windivert.ArchiveName)
	data, _ := os.ReadFile(cache)
	if fmt.Sprintf("%x", sha256.Sum256(data)) != windivert.ArchiveSHA256 {
		client := &http.Client{Timeout: 60 * time.Second}
		response, err := client.Get(windivert.ArchiveURL)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("WinDivert download: HTTP %d", response.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != windivert.ArchiveSHA256 {
			return fmt.Errorf("WinDivert archive SHA256 mismatch")
		}
		if err := os.MkdirAll(filepath.Dir(cache), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(cache, data, 0600); err != nil {
			return err
		}
	}
	files, err := windivert.RuntimeFiles(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(out, name), contents, 0644); err != nil {
			return err
		}
	}
	if embedArchive != "" {
		if err := os.MkdirAll(filepath.Dir(embedArchive), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(embedArchive, data, 0644); err != nil {
			return err
		}
		fmt.Printf("Verified WinDivert archive for embedding: %s\n", embedArchive)
	}
	fmt.Printf("WinDivert %s verified and packaged: %s\n", windivert.Version, out)
	return nil
}
