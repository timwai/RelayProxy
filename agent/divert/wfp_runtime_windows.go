//go:build windows

package divert

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	wfpAutoInstallEnv = "RELAYPROXY_WFP_AUTO_INSTALL"
	wfpPackageMaxFile = 32 << 20
)

var bundledWFPFiles = sync.OnceValues(func() (map[string][]byte, error) {
	return parseEmbeddedWFPPackage(embeddedWFPArchive)
})

func parseEmbeddedWFPPackage(data []byte) (map[string][]byte, error) {
	if len(data) < 4 || data[0] != 'P' || data[1] != 'K' {
		return nil, errors.New("当前客户端没有内嵌可安装的 WFP 驱动包，请使用 scripts/build.ps1 生成正式 Windows 客户端")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("解析内嵌 WFP 驱动包失败: %w", err)
	}

	required := map[string]bool{
		"RelayProxyWfp.sys": false,
		"RelayProxyWfp.inf": false,
		"RelayProxyWfp.cat": false,
	}
	files := make(map[string][]byte, len(required))
	for _, entry := range reader.File {
		if _, ok := required[entry.Name]; !ok {
			return nil, fmt.Errorf("内嵌 WFP 驱动包包含未知文件 %q", entry.Name)
		}
		if required[entry.Name] {
			return nil, fmt.Errorf("内嵌 WFP 驱动包重复文件 %q", entry.Name)
		}
		if entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > wfpPackageMaxFile {
			return nil, fmt.Errorf("内嵌 WFP 文件大小无效 %s: %d", entry.Name, entry.UncompressedSize64)
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(stream, wfpPackageMaxFile+1))
		closeErr := stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(content) == 0 || len(content) > wfpPackageMaxFile {
			return nil, fmt.Errorf("内嵌 WFP 文件大小无效 %s: %d", entry.Name, len(content))
		}
		files[entry.Name] = content
		required[entry.Name] = true
	}
	for name, present := range required {
		if !present {
			return nil, fmt.Errorf("内嵌 WFP 驱动包缺少 %s", name)
		}
	}

	inf := strings.ToLower(string(files["RelayProxyWfp.inf"]))
	if !strings.Contains(inf, "relayproxywfp.sys") ||
		!strings.Contains(inf, "catalogfile=relayproxywfp.cat") ||
		!strings.Contains(inf, "addservice=relayproxywfp") {
		return nil, errors.New("内嵌 WFP INF 与 RelayProxy 驱动包不匹配")
	}
	if err := validateEmbeddedWFPMachine(files["RelayProxyWfp.sys"]); err != nil {
		return nil, err
	}
	return files, nil
}

func validateEmbeddedWFPMachine(image []byte) error {
	file, err := pe.NewFile(bytes.NewReader(image))
	if err != nil {
		return fmt.Errorf("内嵌 RelayProxyWfp.sys 不是有效 PE 驱动: %w", err)
	}
	defer file.Close()

	var expected uint16
	switch runtime.GOARCH {
	case "amd64":
		expected = 0x8664
	case "arm64":
		expected = 0xaa64
	default:
		return fmt.Errorf("Windows WFP 驱动不支持 %s", runtime.GOARCH)
	}
	if file.FileHeader.Machine != expected {
		return fmt.Errorf("内嵌 WFP 驱动架构 0x%x 与客户端 %s 不匹配", file.FileHeader.Machine, runtime.GOARCH)
	}
	return nil
}

func wfpAutoInstallDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(wfpAutoInstallEnv))) {
	case "0", "false", "off", "no":
		return true
	default:
		return false
	}
}

// ensureEmbeddedWFPInstalled is called only when transparent interception is
// actually starting. Capability/readiness probes remain side-effect free.
func ensureEmbeddedWFPInstalled() error {
	readyErr := wfpPlatformReadiness()
	if wfpAutoInstallDisabled() {
		return readyErr
	}

	files, packageErr := bundledWFPFiles()
	if readyErr == nil {
		if packageErr != nil {
			// A developer build may intentionally have only the placeholder.
			// A compatible already-installed driver is still safe to use.
			return nil
		}
		current, err := installedWFPMatches(files["RelayProxyWfp.sys"])
		if err != nil {
			// Do not replace a working driver just because its backing file
			// cannot be inspected.
			return nil
		}
		if current {
			return nil
		}
	}

	if !windows.GetCurrentProcessToken().IsElevated() {
		if readyErr != nil {
			return readyErr
		}
		return errors.New("RelayProxyWfp 驱动需要更新，请以管理员身份启动客户端")
	}
	if packageErr != nil {
		if readyErr != nil {
			return fmt.Errorf("WFP 驱动不可用，且客户端没有有效的内嵌驱动包: %w", packageErr)
		}
		return nil
	}
	if err := installEmbeddedWFPPackage(files); err != nil {
		return err
	}
	return wfpPlatformReadiness()
}

func installedWFPMatches(expected []byte) (bool, error) {
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	path := filepath.Join(systemRoot, "System32", "drivers", "RelayProxyWfp.sys")
	actual, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return sha256.Sum256(actual) == sha256.Sum256(expected), nil
}

func materializeEmbeddedWFPPackage(files map[string][]byte) (string, error) {
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	root := filepath.Join(programData, "RelayProxy-WFP")
	if err := ensureWinDivertDirectory(root); err != nil {
		return "", fmt.Errorf("创建 WFP 驱动缓存目录失败: %w", err)
	}

	sum := sha256.Sum256(files["RelayProxyWfp.sys"])
	directory := filepath.Join(root, fmt.Sprintf("%s-%x", runtime.GOARCH, sum[:8]))
	if err := ensureWinDivertDirectory(directory); err != nil {
		return "", fmt.Errorf("创建 WFP 驱动版本目录失败: %w", err)
	}
	if err := materializeWinDivert(directory, files); err != nil {
		return "", fmt.Errorf("释放内嵌 WFP 驱动失败: %w", err)
	}
	return directory, nil
}

func installEmbeddedWFPPackage(files map[string][]byte) error {
	directory, err := materializeEmbeddedWFPPackage(files)
	if err != nil {
		return err
	}
	inf := filepath.Join(directory, "RelayProxyWfp.inf")

	// No controller exists yet, so stopping an older service is safe. Ignore a
	// missing/stopped service; the package installation below is authoritative.
	_, _ = runWFPSystemTool("sc.exe", "stop", "RelayProxyWfp")
	time.Sleep(300 * time.Millisecond)

	if output, err := runWFPSystemTool("pnputil.exe", "/add-driver", inf, "/install"); err != nil {
		return fmt.Errorf("自动安装 RelayProxyWfp 驱动失败 (pnputil): %w\n%s", err, output)
	}

	// Primitive-driver packages normally create the service through pnputil.
	// Apply DefaultInstall as a compatibility fallback and continue if it reports
	// an error: a subsequent successful device open is the final authority.
	setupOutput, setupErr := runWFPSystemTool(
		"rundll32.exe", "setupapi.dll,InstallHinfSection", "DefaultInstall", "132", inf,
	)
	startOutput, startErr := runWFPSystemTool("sc.exe", "start", "RelayProxyWfp")

	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := wfpPlatformReadiness(); err == nil {
			matches, hashErr := installedWFPMatches(files["RelayProxyWfp.sys"])
			if hashErr == nil && matches {
				return nil
			}
			if hashErr != nil {
				lastErr = hashErr
			} else {
				lastErr = errors.New("Windows 已启动 RelayProxyWfp，但加载的 SYS 与当前 EXE 内嵌版本不一致")
			}
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}

	var details []error
	if setupErr != nil {
		details = append(details, fmt.Errorf("DefaultInstall: %w (%s)", setupErr, setupOutput))
	}
	if startErr != nil {
		details = append(details, fmt.Errorf("start service: %w (%s)", startErr, startOutput))
	}
	if lastErr != nil {
		details = append(details, lastErr)
	}
	return fmt.Errorf("自动安装 RelayProxyWfp 后驱动未就绪: %w", errors.Join(details...))
}

func runWFPSystemTool(name string, args ...string) (string, error) {
	switch strings.ToLower(name) {
	case "pnputil.exe", "rundll32.exe", "sc.exe":
	default:
		return "", fmt.Errorf("refusing unexpected elevated system tool %q", name)
	}
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	tool := filepath.Join(systemRoot, "System32", name)
	info, err := os.Stat(tool)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return "", fmt.Errorf("Windows system tool unavailable %s: %w", tool, err)
	}

	command := exec.Command(tool, args...)
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
