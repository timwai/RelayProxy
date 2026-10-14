package bridge

import (
	"encoding/base64"
	"os"
	"testing"

	"relayproxy/agent/app"
	"relayproxy/agent/fileshare"
)

func TestBrowserUploadStagingIsBoundedAndDiscardable(t *testing.T) {
	m := newFileTransferManager(&app.Agent{})
	stage, err := m.beginUpload("client-device", "share_01", "docs/hello.txt", 5)
	if err != nil { t.Fatal(err) }
	first, err := m.appendUpload(stage.ID, base64.StdEncoding.EncodeToString([]byte("he")))
	if err != nil || first.Done != 2 { t.Fatalf("first chunk: %+v %v", first, err) }
	if _, err := m.appendUpload(stage.ID, base64.StdEncoding.EncodeToString([]byte("llo!"))); err == nil {
		t.Fatal("accepted bytes beyond declared upload size")
	}
	second, err := m.appendUpload(stage.ID, base64.StdEncoding.EncodeToString([]byte("llo")))
	if err != nil || second.Done != 5 { t.Fatalf("second chunk: %+v %v", second, err) }
	stagingPath := m.stages[stage.ID].file.Name()
	if err := m.discardUpload(stage.ID); err != nil { t.Fatal(err) }
	if _, err := os.Stat(stagingPath); !os.IsNotExist(err) { t.Fatalf("staging file leaked: %v", err) }
	if _, err := m.appendUpload(stage.ID, ""); err == nil { t.Fatal("discarded stage remained usable") }
}

func TestBrowserUploadStagingValidatesPathsAndSize(t *testing.T) {
	m := newFileTransferManager(&app.Agent{})
	for _, p := range []string{"", ".", "../escape", "/absolute", "folder/../../escape", "a\\b"} {
		if _, err := m.beginUpload("device", "share", p, 1); err == nil {
			t.Fatalf("accepted path %q", p)
		}
	}
	if _, err := m.beginUpload("device", "share", "ok.txt", fileshare.MaxUploadSize+1); err == nil {
		t.Fatal("accepted over-limit upload")
	}
	if _, err := m.beginUpload("", "share", "ok.txt", 1); err == nil { t.Fatal("accepted missing device") }
}

func TestBrowserUploadStageAcceptsFileNamesContainingDoubleDots(t *testing.T) {
	m := newFileTransferManager(&app.Agent{})
	stage, err := m.beginUpload("device", "share", "reports/final..review.pdf", 0)
	if err != nil {
		t.Fatalf("valid filename rejected: %v", err)
	}
	if err := m.discardUpload(stage.ID); err != nil {
		t.Fatal(err)
	}
}
