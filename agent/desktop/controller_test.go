package desktop

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func TestSnapshotFromEncodedFrameH264(t *testing.T) {
	frame := &desktopmedia.EncodedFrame{FrameID: 7, Timestamp: 1234, KeyFrame: true, Data: []byte{0, 0, 0, 1, 0x65}}
	got, ok := snapshotFromEncodedFrame(frame, protocol.DesktopVideoConfig{
		Codec: "h264", CodecString: "avc1.42E01F", Width: 1280, Height: 720,
	}, true)
	if !ok || got.MimeType != "video/h264" || got.Codec != "avc1.42E01F" || !got.KeyFrame || got.Width != 1280 {
		t.Fatalf("snapshot=%+v ok=%v", got, ok)
	}
}

func TestSnapshotFromEncodedFrameLegacyJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 8))
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	got, ok := snapshotFromEncodedFrame(&desktopmedia.EncodedFrame{FrameID: 1, Data: out.Bytes()}, protocol.DesktopVideoConfig{}, false)
	if !ok || got.MimeType != "image/jpeg" || got.Width != 16 || got.Height != 8 {
		t.Fatalf("snapshot=%+v ok=%v", got, ok)
	}
}


func TestLatestCursorOmitsKnownShape(t *testing.T) {
	session := &ControllerSession{
		done: make(chan struct{}),
		latestCursor: protocol.DesktopCursorState{
			Sequence: 3, CursorID: "cursor-a", Visible: true, PNG: []byte{1, 2, 3},
		},
	}
	cursor, ok := session.LatestCursor("")
	if !ok || len(cursor.PNG) != 3 {
		t.Fatalf("cursor=%+v ok=%v", cursor, ok)
	}
	cursor.PNG[0] = 9
	if session.latestCursor.PNG[0] != 1 {
		t.Fatal("LatestCursor returned internal PNG storage")
	}
	cursor, ok = session.LatestCursor("cursor-a")
	if !ok || len(cursor.PNG) != 0 {
		t.Fatalf("known-shape cursor=%+v ok=%v", cursor, ok)
	}
}
