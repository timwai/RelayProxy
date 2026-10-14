package fileshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestUploadResumeAfterBrokenStream(t *testing.T) {
	store, id, root := uploadFixture(t)
	content := []byte(strings.Repeat("authenticated resumable upload ", 4096))
	req := protocol.FileShareRequest{
		Action: protocol.FileShareUpload, ShareID: id, Path: "resumed.bin",
		Size: int64(len(content)), Resume: true,
	}
	// First authenticated stream is cut after exactly one third of the file.
	client, host := uploadPair()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); store.Serve(ctx, host, "writer") }()
	if err := protocol.WriteJSON(client, req); err != nil {
		t.Fatal(err)
	}
	var first protocol.FileShareResponse
	if err := protocol.ReadJSON(client, &first); err != nil {
		t.Fatal(err)
	}
	if !first.Success || len(first.UploadID) != 48 || first.Offset != 0 {
		t.Fatalf("expected resumable token: %+v", first)
	}
	part := len(content) / 3
	if _, err := client.Write(content[:part]); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-firstDone
	if _, err := os.Stat(filepath.Join(root, "resumed.bin")); !os.IsNotExist(err) {
		t.Fatalf("incomplete upload published: %v", err)
	}
	// The unguessable resume token is bound to the authenticated Server
	// device ID; another device cannot claim or overwrite the partial.
	req.UploadID = first.UploadID
	spoof, spoofHost := uploadPair()
	spoofDone := make(chan struct{})
	go func() { defer close(spoofDone); store.Serve(ctx, spoofHost, "reader") }()
	if err := protocol.WriteJSON(spoof, req); err != nil {
		t.Fatal(err)
	}
	var denied protocol.FileShareResponse
	if err := protocol.ReadJSON(spoof, &denied); err != nil {
		t.Fatal(err)
	}
	_ = spoof.Close()
	<-spoofDone
	if denied.Success {
		t.Fatal("read-only device hijacked resumable upload")
	}
	client2, host2 := uploadPair()
	secondDone := make(chan struct{})
	go func() { defer close(secondDone); store.Serve(ctx, host2, "writer") }()
	if err := protocol.WriteJSON(client2, req); err != nil {
		t.Fatal(err)
	}
	var second protocol.FileShareResponse
	if err := protocol.ReadJSON(client2, &second); err != nil {
		t.Fatal(err)
	}
	prefix := sha256.Sum256(content[:part])
	if !second.Success || second.UploadID != first.UploadID || second.Offset != int64(part) ||
		second.PrefixSHA256 != hex.EncodeToString(prefix[:]) {
		t.Fatalf("resume prefix incorrect: %+v", second)
	}
	if _, err := client2.Write(content[part:]); err != nil {
		t.Fatal(err)
	}
	if err := client2.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var completed protocol.FileShareResponse
	if err := protocol.ReadJSON(client2, &completed); err != nil {
		t.Fatal(err)
	}
	_ = client2.Close()
	<-secondDone
	whole := sha256.Sum256(content)
	if !completed.Success || completed.Size != int64(len(content)) ||
		completed.SHA256 != hex.EncodeToString(whole[:]) {
		t.Fatalf("resumed upload final digest invalid: %+v", completed)
	}
	got, err := os.ReadFile(filepath.Join(root, "resumed.bin"))
	if err != nil || string(got) != string(content) {
		t.Fatalf("resumed file not complete: %v", err)
	}
	if _, ok := store.uploads[first.UploadID]; ok {
		t.Fatal("completed upload token was not discarded")
	}
}

func TestUploadResumeInvalidTokenCannotCreateFile(t *testing.T) {
	store, id, dir := uploadFixture(t)
	req := protocol.FileShareRequest{
		Action: protocol.FileShareUpload, ShareID: id, Path: "x.txt", Size: 5,
		Resume: true, UploadID: strings.Repeat("f", 48),
	}
	client, host := uploadPair()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); store.Serve(ctx, host, "writer") }()
	if err := protocol.WriteJSON(client, req); err != nil {
		t.Fatal(err)
	}
	var response protocol.FileShareResponse
	if err := protocol.ReadJSON(client, &response); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done
	if response.Success {
		t.Fatal("fabricated upload token accepted")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("unexpected published/staged files: %v", files)
	}
}
