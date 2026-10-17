package fileshare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

type fileShareTestStream struct{ net.Conn }

func (s fileShareTestStream) CloseWrite() error { return s.Conn.Close() }

func fileShareTestRequest(t *testing.T, store *FileStore, deviceID string, req protocol.FileShareRequest) (protocol.FileShareResponse, []byte) {
	t.Helper()
	client, host := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.Serve(ctx, fileShareTestStream{host}, deviceID)
	}()
	if err := protocol.WriteJSON(client, req); err != nil {
		t.Fatal(err)
	}
	var response protocol.FileShareResponse
	if err := protocol.ReadJSON(client, &response); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if req.Action == protocol.FileShareDownload && response.Success {
		if response.Size < 0 || response.Size > 1<<20 {
			t.Fatalf("invalid test download size: %d", response.Size)
		}
		data = make([]byte, int(response.Size))
		if _, err := io.ReadFull(client, data); err != nil {
			t.Fatal(err)
		}
		var receipt protocol.FileShareResponse
		if err := protocol.ReadJSON(client, &receipt); err != nil {
			t.Fatalf("final download receipt missing: %v", err)
		}
		hash := sha256.Sum256(data)
		if !receipt.Success || receipt.Size != response.Size ||
			receipt.SHA256 != hex.EncodeToString(hash[:]) {
			t.Fatalf("invalid download integrity receipt: %+v", receipt)
		}
	}
	<-done
	return response, data
}

func TestFileShareServiceChecksAccessAndPathConfinement(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "hello.txt"), []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workdir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(filepath.Join(t.TempDir(), "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	state.Shares = []Share{{Name: "work", RootPath: workdir, Enabled: true,
		Permissions: map[string]Permission{"read-device": PermissionRead, "write-device": PermissionReadWrite}}}
	state, err = store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	id := state.Shares[0].ID
	response, _ := fileShareTestRequest(t, store, "read-device", protocol.FileShareRequest{Action: protocol.FileShareListShares})
	if !response.Success || len(response.Shares) != 1 || response.Shares[0].ID != id {
		t.Fatalf("authorized shares: %+v", response)
	}
	response, _ = fileShareTestRequest(t, store, "stranger", protocol.FileShareRequest{Action: protocol.FileShareListShares})
	if !response.Success || len(response.Shares) != 0 {
		t.Fatalf("unauthorized peer discovered a share: %+v", response)
	}
	response, _ = fileShareTestRequest(t, store, "read-device", protocol.FileShareRequest{
		Action: protocol.FileShareListDir, ShareID: id, Path: "."})
	if !response.Success || len(response.Entries) != 2 {
		t.Fatalf("directory list: %+v", response)
	}
	response, body := fileShareTestRequest(t, store, "read-device", protocol.FileShareRequest{
		Action: protocol.FileShareDownload, ShareID: id, Path: "hello.txt"})
	if !response.Success || string(body) != "hello world" || response.Size != int64(len(body)) {
		t.Fatalf("download: %+v %q", response, body)
	}
	for _, p := range []string{"../outside", "sub/../../outside", "/etc/passwd"} {
		response, _ := fileShareTestRequest(t, store, "read-device", protocol.FileShareRequest{
			Action: protocol.FileShareDownload, ShareID: id, Path: p})
		if response.Success {
			t.Fatalf("path traversal accepted: %q", p)
		}
	}
	response, _ = fileShareTestRequest(t, store, "stranger", protocol.FileShareRequest{
		Action: protocol.FileShareDownload, ShareID: id, Path: "hello.txt"})
	if response.Success {
		t.Fatal("unapproved device downloaded file")
	}
}

func TestFileShareServiceDoesNotFollowEscapingSymlink(t *testing.T) {
	shared := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("secret data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(shared, "escape.txt")); err != nil {
		t.Skipf("symlink creation is not permitted: %v", err)
	}
	store, _ := New(filepath.Join(t.TempDir(), "agent.yaml"))
	state, _ := store.Load()
	state.Enabled = true
	state.Shares = []Share{{Name: "test", RootPath: shared, Enabled: true, Permissions: map[string]Permission{"dev": PermissionRead}}}
	state, err := store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := fileShareTestRequest(t, store, "dev", protocol.FileShareRequest{
		Action: protocol.FileShareDownload, ShareID: state.Shares[0].ID, Path: "escape.txt"})
	if response.Success {
		t.Fatalf("escaped root via symlink: %+v", response)
	}
}

func TestFileShareDownloadResumeValidatesPrefix(t *testing.T) {
	dir := t.TempDir()
	data := []byte("the quick brown fox jumps over the lazy dog")
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(filepath.Join(t.TempDir(), "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	state.Shares = []Share{{
		Name: "test", RootPath: dir, Enabled: true,
		Permissions: map[string]Permission{"client-1": PermissionRead},
	}}
	state, err = store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	const offset = 17
	h := sha256.Sum256(data[:offset])
	issue := func(from string, resumeHash string) (protocol.FileShareResponse, []byte, protocol.FileShareResponse) {
		client, host := net.Pipe()
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			store.Serve(ctx, fileShareTestStream{host}, from)
		}()
		if err := protocol.WriteJSON(client, protocol.FileShareRequest{
			Action: protocol.FileShareDownload, ShareID: state.Shares[0].ID,
			Path: "data.txt", Offset: offset, PrefixSHA256: resumeHash,
		}); err != nil {
			t.Fatal(err)
		}
		var initial protocol.FileShareResponse
		if err := protocol.ReadJSON(client, &initial); err != nil {
			t.Fatal(err)
		}
		var suffix []byte
		var receipt protocol.FileShareResponse
		if initial.Success {
			suffix = make([]byte, len(data)-offset)
			if _, err := io.ReadFull(client, suffix); err != nil {
				t.Fatal(err)
			}
			if err := protocol.ReadJSON(client, &receipt); err != nil {
				t.Fatal(err)
			}
		}
		<-done
		return initial, suffix, receipt
	}
	initial, suffix, receipt := issue("client-1", hex.EncodeToString(h[:]))
	if !initial.Success || initial.Offset != offset || initial.Size != int64(len(data)) ||
		!bytes.Equal(suffix, data[offset:]) || !receipt.Success {
		t.Fatalf("resume failed: initial=%+v suffix=%q receipt=%+v", initial, suffix, receipt)
	}
	whole := sha256.Sum256(data)
	if receipt.SHA256 != hex.EncodeToString(whole[:]) {
		t.Fatalf("resume full digest mismatch: %q", receipt.SHA256)
	}
	rejected, _, _ := issue("client-1", strings.Repeat("0", 64))
	if rejected.Success || !strings.Contains(rejected.Error, "prefix mismatch") {
		t.Fatalf("corrupt resume prefix accepted: %+v", rejected)
	}
	rejected, _, _ = issue("intruder", hex.EncodeToString(h[:]))
	if rejected.Success {
		t.Fatal("unapproved device accessed a partial file")
	}
}

func TestFileMutationRequiresReadWriteAndRevocation(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(t.TempDir(), "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	state.Shares = []Share{{Name: "test", RootPath: dir, Enabled: true,
		Permissions: map[string]Permission{"reader": PermissionRead, "writer": PermissionReadWrite}}}
	state, err = store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	id := state.Shares[0].ID
	req := protocol.FileShareRequest{Action: protocol.FileShareMkdir, ShareID: id, Path: "created"}
	for _, who := range []string{"reader", "unknown", ""} {
		result, _ := fileShareTestRequest(t, store, who, req)
		if result.Success {
			t.Fatalf("mutation accepted from %q", who)
		}
	}
	result, _ := fileShareTestRequest(t, store, "writer", req)
	if !result.Success {
		t.Fatalf("authorized writer rejected: %+v", result)
	}
	state.Revision++
	state.Shares[0].Permissions["writer"] = PermissionNone
	if _, err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	result, _ = fileShareTestRequest(t, store, "writer",
		protocol.FileShareRequest{Action: protocol.FileShareRemove, ShareID: id, Path: "created"})
	if result.Success {
		t.Fatal("revoked writer deleted directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "created")); err != nil {
		t.Fatal("revoked writer changed filesystem:", err)
	}
}
