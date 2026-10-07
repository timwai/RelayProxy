package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPayloadDescriptorAndExtract(t *testing.T) {
	payload := []byte("fake-winui-host-payload")
	sum := sha256.Sum256(payload)
	stub := []byte("MZ-fake-launcher")

	var compressed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	footer := make([]byte, footerSize)
	copy(footer[:len(footerMagic)], []byte(footerMagic))
	binary.LittleEndian.PutUint64(footer[len(footerMagic):len(footerMagic)+8], uint64(compressed.Len()))
	binary.LittleEndian.PutUint64(footer[len(footerMagic)+8:len(footerMagic)+16], uint64(len(payload)))
	copy(footer[len(footerMagic)+16:], sum[:])

	selfPath := filepath.Join(t.TempDir(), "renamed-anything.exe")
	data := append(append(append([]byte{}, stub...), compressed.Bytes()...), footer...)
	if err := os.WriteFile(selfPath, data, 0o755); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(selfPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := readPayloadDescriptor(file, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if desc.compressedSize != int64(compressed.Len()) {
		t.Fatalf("compressed payload size = %d, want %d", desc.compressedSize, compressed.Len())
	}
	if desc.size != int64(len(payload)) {
		t.Fatalf("payload size = %d, want %d", desc.size, len(payload))
	}
	if desc.hash != sum {
		t.Fatalf("payload hash mismatch")
	}

	cacheRoot := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	t.Setenv("LOCALAPPDATA", cacheRoot)
	hostPath, err := ensurePayload(file, desc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("extracted payload = %q, want %q", got, payload)
	}
}

func TestUpsertEnv(t *testing.T) {
	got := upsertEnv([]string{"A=1", "relayproxy_launcher_path=old", "B=2"}, launcherPathEnv, "new")
	want := []string{"A=1", "B=2", launcherPathEnv + "=new"}
	if len(got) != len(want) {
		t.Fatalf("env length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
