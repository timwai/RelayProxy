package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDesktopVideoControlDisplayIDDistinguishesVirtualDesktopRequest(t *testing.T) {
	without, err := json.Marshal(DesktopVideoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "displayId") {
		t.Fatalf("unset display control serialized displayId: %s", without)
	}

	virtualDesktop := ""
	with, err := json.Marshal(DesktopVideoControl{DisplayID: &virtualDesktop})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"displayId":""`) {
		t.Fatalf("virtual desktop display request lost empty displayId: %s", with)
	}
}
