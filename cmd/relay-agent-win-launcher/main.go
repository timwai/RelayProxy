package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	footerMagic        = "RELAYPROXY_GUI1!"
	footerSize         = 16 + 8 + sha256.Size
	nativeHostName     = "RelayProxy.NativeHost.exe"
	launcherPathEnv    = "RELAYPROXY_LAUNCHER_PATH"
	cacheReadyFile     = ".ready"
	cacheProductDir    = "RelayProxy"
	cacheNativeHostDir = "native-host"
)

type payloadDescriptor struct {
	offset int64
	size   int64
	hash   [sha256.Size]byte
}

func main() {
	code, err := launchNativeHost()
	if err != nil {
		showFatal("RelayProxy 启动失败", err.Error())
		os.Exit(1)
	}
	os.Exit(code)
}

func launchNativeHost() (int, error) {
	selfPath, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("无法确定启动器路径: %w", err)
	}
	selfPath, err = filepath.Abs(selfPath)
	if err != nil {
		return 1, fmt.Errorf("无法规范化启动器路径: %w", err)
	}

	self, err := os.Open(selfPath)
	if err != nil {
		return 1, fmt.Errorf("无法读取启动器: %w", err)
	}
	defer self.Close()

	info, err := self.Stat()
	if err != nil {
		return 1, fmt.Errorf("无法读取启动器信息: %w", err)
	}
	desc, err := readPayloadDescriptor(self, info.Size())
	if err != nil {
		return 1, err
	}

	hostPath, err := ensurePayload(self, desc)
	if err != nil {
		return 1, err
	}

	cmd := exec.Command(hostPath, os.Args[1:]...)
	cmd.Env = upsertEnv(os.Environ(), launcherPathEnv, selfPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, fmt.Errorf("无法启动 RelayProxy Windows UI: %w", err)
}

func readPayloadDescriptor(file *os.File, fileSize int64) (payloadDescriptor, error) {
	var desc payloadDescriptor
	if fileSize < footerSize {
		return desc, errors.New("启动器缺少 RelayProxy UI payload")
	}

	footerOffset := fileSize - footerSize
	footer := make([]byte, footerSize)
	if _, err := io.ReadFull(io.NewSectionReader(file, footerOffset, footerSize), footer); err != nil {
		return desc, fmt.Errorf("读取启动器 payload footer 失败: %w", err)
	}
	if string(footer[:len(footerMagic)]) != footerMagic {
		return desc, errors.New("启动器 payload 标记无效；请重新下载完整 EXE")
	}

	payloadSize := binary.LittleEndian.Uint64(footer[len(footerMagic) : len(footerMagic)+8])
	if payloadSize == 0 || payloadSize > uint64(footerOffset) {
		return desc, errors.New("启动器 payload 长度无效")
	}

	desc.size = int64(payloadSize)
	desc.offset = footerOffset - desc.size
	copy(desc.hash[:], footer[len(footerMagic)+8:])
	return desc, nil
}

func ensurePayload(source *os.File, desc payloadDescriptor) (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheBase) == "" {
		cacheBase = os.TempDir()
	}

	hashText := hex.EncodeToString(desc.hash[:])
	parent := filepath.Join(cacheBase, cacheProductDir, cacheNativeHostDir)
	targetDir := filepath.Join(parent, hashText)
	targetHost := filepath.Join(targetDir, nativeHostName)
	readyPath := filepath.Join(targetDir, cacheReadyFile)

	if cacheReady(readyPath, targetHost, hashText) {
		return targetHost, nil
	}
	_ = os.RemoveAll(targetDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("无法创建 RelayProxy UI 缓存目录: %w", err)
	}

	tempDir, err := os.MkdirTemp(parent, ".extract-")
	if err != nil {
		return "", fmt.Errorf("无法创建 RelayProxy UI 临时目录: %w", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()

	tempHost := filepath.Join(tempDir, nativeHostName)
	out, err := os.OpenFile(tempHost, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("无法写入 RelayProxy UI Host: %w", err)
	}

	hasher := sha256.New()
	reader := io.NewSectionReader(source, desc.offset, desc.size)
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), reader)
	closeErr := out.Close()
	if copyErr != nil {
		return "", fmt.Errorf("解压 RelayProxy UI Host 失败: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("保存 RelayProxy UI Host 失败: %w", closeErr)
	}
	if written != desc.size {
		return "", fmt.Errorf("RelayProxy UI Host 长度不完整: %d/%d", written, desc.size)
	}
	if !bytes.Equal(hasher.Sum(nil), desc.hash[:]) {
		return "", errors.New("RelayProxy UI Host 校验失败；请重新下载 EXE")
	}
	if err := os.WriteFile(filepath.Join(tempDir, cacheReadyFile), []byte(hashText), 0o644); err != nil {
		return "", fmt.Errorf("无法完成 RelayProxy UI Host 缓存: %w", err)
	}

	if err := os.Rename(tempDir, targetDir); err != nil {
		if cacheReady(readyPath, targetHost, hashText) {
			return targetHost, nil
		}
		_ = os.RemoveAll(targetDir)
		if retryErr := os.Rename(tempDir, targetDir); retryErr != nil {
			return "", fmt.Errorf("无法安装 RelayProxy UI Host: %w", retryErr)
		}
	}
	keepTemp = true
	return targetHost, nil
}

func cacheReady(readyPath, hostPath, expectedHash string) bool {
	if _, err := os.Stat(hostPath); err != nil {
		return false
	}
	data, err := os.ReadFile(readyPath)
	return err == nil && strings.TrimSpace(string(data)) == expectedHash
}

func upsertEnv(values []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(values)+1)
	for _, item := range values {
		if len(item) >= len(prefix) && strings.EqualFold(item[:len(prefix)], prefix) {
			continue
		}
		out = append(out, item)
	}
	return append(out, prefix+value)
}
