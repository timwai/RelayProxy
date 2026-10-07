//go:build ignore

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

const footerMagic = "RELAYPROXY_GUI3!"

func main() {
	launcher := flag.String("launcher", "", "launcher stub executable")
	payload := flag.String("payload", "", "fixed-name WinUI host payload")
	output := flag.String("output", "", "final executable")
	windowMiB := flag.Int("window-mib", 8, "zstd encoder window in MiB")
	flag.Parse()
	if err := pack(*launcher, *payload, *output, *windowMiB); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(launcher, payload, output string, windowMiB int) error {
	if launcher == "" || payload == "" || output == "" {
		return fmt.Errorf("launcher, payload and output are required")
	}
	if windowMiB <= 0 || windowMiB&(windowMiB-1) != 0 {
		return fmt.Errorf("window-mib must be a positive power of two")
	}
	windowSize := windowMiB << 20
	if windowSize < zstd.MinWindowSize || windowSize > zstd.MaxWindowSize {
		return fmt.Errorf("window size %d is outside zstd limits", windowSize)
	}

	launcher, _ = filepath.Abs(launcher)
	payload, _ = filepath.Abs(payload)
	output, _ = filepath.Abs(output)
	if launcher == output || payload == output {
		return fmt.Errorf("output must differ from launcher and payload")
	}
	launcherInfo, err := os.Stat(launcher)
	if err != nil {
		return fmt.Errorf("launcher stub: %w", err)
	}
	payloadInfo, err := os.Stat(payload)
	if err != nil {
		return fmt.Errorf("WinUI payload: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}

	inPayload, err := os.Open(payload)
	if err != nil {
		return err
	}
	defer inPayload.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, inPayload); err != nil {
		return err
	}
	hashBytes := hash.Sum(nil)
	hashHex := hex.EncodeToString(hashBytes)
	if _, err := inPayload.Seek(0, io.SeekStart); err != nil {
		return err
	}

	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()
	inLauncher, err := os.Open(launcher)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, inLauncher); err != nil {
		inLauncher.Close()
		return err
	}
	if err := inLauncher.Close(); err != nil {
		return err
	}

	payloadStart, err := out.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	encoder, err := zstd.NewWriter(
		out,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(windowSize),
	)
	if err != nil {
		return err
	}
	if _, err := io.Copy(encoder, inPayload); err != nil {
		encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	payloadEnd, err := out.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	compressedSize := payloadEnd - payloadStart

	if _, err := out.Write([]byte(footerMagic)); err != nil {
		return err
	}
	var sizes [16]byte
	binary.LittleEndian.PutUint64(sizes[0:8], uint64(compressedSize))
	binary.LittleEndian.PutUint64(sizes[8:16], uint64(payloadInfo.Size()))
	if _, err := out.Write(sizes[:]); err != nil {
		return err
	}
	if _, err := out.Write(hashBytes); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	finalInfo, err := os.Stat(output)
	if err != nil {
		return err
	}
	ratio := float64(compressedSize) / float64(payloadInfo.Size()) * 100
	fmt.Printf("[zstd-window] window: %d MiB\n", windowMiB)
	fmt.Printf("[zstd-window] stub: %.2f MiB\n", float64(launcherInfo.Size())/(1<<20))
	fmt.Printf("[zstd-window] raw: %.2f MiB\n", float64(payloadInfo.Size())/(1<<20))
	fmt.Printf("[zstd-window] payload: %.2f MiB (%.1f%%)\n", float64(compressedSize)/(1<<20), ratio)
	fmt.Printf("[zstd-window] final: %.2f MiB\n", float64(finalInfo.Size())/(1<<20))
	fmt.Printf("[zstd-window] sha256: %s\n", hashHex)
	return nil
}
