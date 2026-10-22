package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

func TestDeviceListBatteryIsOnlineAndroidOnlyAndOwnerScoped(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	owner, cookie := apiUser(t, router, "battery-owner")
	other, _ := apiUser(t, router, "battery-other")
	for _, item := range []struct{ id, platform, owner string }{
		{"android-battery", "android", owner.ID},
		{"windows-battery", "windows", owner.ID},
		{"offline-battery", "android", owner.ID},
		{"private-battery", "android", other.ID},
	} {
		if err := router.db.UpsertDevice(&repository.Device{
			ID: item.id, Name: item.id, Platform: item.platform,
			OwnerUserID: item.owner, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"android-battery", "windows-battery", "private-battery"} {
		conn := &session.DeviceSession{DeviceID: id}
		conn.SetBatteryStatus(&protocol.BatteryStatus{Level: 86, Charging: true})
		router.sessions.Register(conn)
	}
	read := func() map[string]map[string]any {
		t.Helper()
		resp := apiRequest(router, cookie, http.MethodGet, "/api/v1/devices")
		var rows []map[string]any
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &rows) != nil {
			t.Fatalf("list devices: %d %s", resp.Code, resp.Body.String())
		}
		mapped := make(map[string]map[string]any)
		for _, row := range rows {
			mapped[row["id"].(string)] = row
		}
		return mapped
	}
	rows := read()
	if _, leaked := rows["private-battery"]; leaked {
		t.Fatal("other owner's battery/device leaked")
	}
	battery, ok := rows["android-battery"]["battery"].(map[string]any)
	if !ok || battery["level"] != float64(86) || battery["charging"] != true || battery["receivedAt"] == nil {
		t.Fatalf("missing online Android battery: %+v", rows["android-battery"])
	}
	if rows["windows-battery"]["battery"] != nil || rows["offline-battery"]["battery"] != nil {
		t.Fatal("offline or non-Android clients must not expose battery data")
	}
	router.sessions.Unregister("android-battery")
	rows = read()
	if rows["android-battery"]["battery"] != nil {
		t.Fatal("battery must disappear on disconnect")
	}
}
