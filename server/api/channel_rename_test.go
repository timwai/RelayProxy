package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"relayproxy/server/repository"
)

func TestMessageChannelIDCanBeChangedWithoutLosingHistoryOrTargets(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	cookie := loginAdmin(t, router)
	device := createMessageTestDevice(t, router, "RENAME-ID")
	identityID := messageDeviceIdentityID(t, router, device.ID)
	public := NewPublicPushHandler(router.sessions, router.db)

	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	create := request(http.MethodPost, "/api/v1/message-channels", messageChannelRequest{
		ID: "old.channel_1", IdentityID: identityID, Name: "My notifications",
		DeviceIDs: []string{device.ID},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", create.Code, create.Body.String())
	}
	before, err := router.db.GetMessageChannel("old.channel_1")
	if err != nil {
		t.Fatal(err)
	}
	push := func(id, message string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/push/"+id+"?message="+url.QueryEscape(message), nil)
		rec := httptest.NewRecorder()
		public.ServeHTTP(rec, req)
		return rec
	}
	first := push("old.channel_1", "original")
	if first.Code != http.StatusOK {
		t.Fatalf("original push returned %d: %s", first.Code, first.Body.String())
	}
	var saved repository.MessageRecord
	if err := json.Unmarshal(first.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}

	rename := request(http.MethodPut, "/api/v1/message-channels/old.channel_1", messageChannelRequest{
		ID: "new-channel_2", Name: "Renamed notifications", DeviceIDs: []string{device.ID},
	})
	if rename.Code != http.StatusOK {
		t.Fatalf("rename returned %d: %s", rename.Code, rename.Body.String())
	}
	var returned repository.MessageChannel
	if err := json.Unmarshal(rename.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.ID != "new-channel_2" || returned.IdentityID != identityID {
		t.Fatalf("renamed channel identity changed: %+v", returned)
	}
	after, err := router.db.GetMessageChannel("new-channel_2")
	if err != nil {
		t.Fatal(err)
	}
	if after.CreatedAt.UnixMilli() != before.CreatedAt.UnixMilli() ||
		after.Name != "Renamed notifications" || len(after.DeviceIDs) != 1 || after.DeviceIDs[0] != device.ID {
		t.Fatalf("channel fields or targets lost during ID edit: %+v", after)
	}
	if _, err := router.db.GetMessageChannel("old.channel_1"); err != sql.ErrNoRows {
		t.Fatalf("old channel still exists: %v", err)
	}
	if result := push("old.channel_1", "old path"); result.Code != http.StatusNotFound {
		t.Fatalf("old push URL returned %d: %s", result.Code, result.Body.String())
	}
	if result := push("new-channel_2", "new path"); result.Code != http.StatusOK {
		t.Fatalf("new push URL returned %d: %s", result.Code, result.Body.String())
	}
	list := request(http.MethodGet, "/api/v1/messages?channelId=new-channel_2&limit=500", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list messages after rename: %d %s", list.Code, list.Body.String())
	}
	var messages []repository.MessageRecord
	if err := json.Unmarshal(list.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range messages {
		if m.ID == saved.ID {
			found = true
			if m.ChannelID != "new-channel_2" || len(m.Deliveries) == 0 {
				t.Fatalf("old message or deliveries lost: %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("previous message missing after rename: %+v", messages)
	}

	// Existing clients that omit ID still update the current channel.
	update := request(http.MethodPut, "/api/v1/message-channels/new-channel_2",
		map[string]any{"name": "name only", "deviceIds": []string{device.ID}})
	if update.Code != http.StatusOK {
		t.Fatalf("legacy update returned %d: %s", update.Code, update.Body.String())
	}
	updated, err := router.db.GetMessageChannel("new-channel_2")
	if err != nil || updated.Name != "name only" {
		t.Fatalf("legacy update lost current ID: %+v %v", updated, err)
	}

	other := request(http.MethodPost, "/api/v1/message-channels", messageChannelRequest{
		ID: "occupied", IdentityID: identityID, Name: "Existing", DeviceIDs: []string{device.ID},
	})
	if other.Code != http.StatusCreated {
		t.Fatalf("create other returned %d: %s", other.Code, other.Body.String())
	}
	conflict := request(http.MethodPut, "/api/v1/message-channels/new-channel_2", messageChannelRequest{
		ID: "occupied", Name: "should not persist", DeviceIDs: []string{device.ID},
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("duplicate channel ID returned %d: %s", conflict.Code, conflict.Body.String())
	}
	original, err := router.db.GetMessageChannel("new-channel_2")
	if err != nil || original.Name != "name only" || len(original.DeviceIDs) != 1 {
		t.Fatalf("failed rename did not roll back: %+v %v", original, err)
	}
	invalid := request(http.MethodPut, "/api/v1/message-channels/new-channel_2", messageChannelRequest{
		ID: "invalid/id", Name: "invalid", DeviceIDs: []string{device.ID},
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid channel ID returned %d: %s", invalid.Code, invalid.Body.String())
	}
}
