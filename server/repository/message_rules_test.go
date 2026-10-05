package repository

import "testing"

func TestMessageRuleV2RoundTripAndMessageMetadata(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentity("Message Rules", "admin")
	if err != nil {
		t.Fatal(err)
	}
	device := &Device{
		ID: "dev_message_rules", Name: "Message Rules Device",
		Fingerprint: "message-rules-fingerprint", InstallationID: "message-rules-install",
		ApprovalState:         EnrollmentApproved,
		RequestedCapabilities: []string{"proxy.client"},
		ApprovedCapabilities:  []string{"proxy.client"},
	}
	if err := db.UpsertDevice(device); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDeviceIdentity(device.ID, identity.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	popup := true
	channel := &MessageChannel{
		ID:         "message-rules-v2",
		IdentityID: identity.ID,
		Name:       "Message Rules V2",
		DeviceIDs:  []string{device.ID},
		MessageRules: []MessageRule{
			{
				Name: "重要告警", Type: "important", Enabled: true,
				Match: MessageMatchConfig{
					MatchType: "keywords", KeywordMode: "any",
					Keywords: []string{"磁盘空间不足"},
				},
				Popup: &popup,
			},
			{
				Name: "默认验证码", Type: "verification_code", Enabled: true, Default: true,
				Match: MessageMatchConfig{
					MatchType: "keywords", KeywordMode: "any",
					Keywords: []string{"验证码"},
				},
				Verification: &VerificationExtractorConfig{
					Type: "auto", MaxDistance: 64, MinLength: 4, MaxLength: 8,
					AllowLetters: true, AllowDigits: true, RequireDigit: true,
				},
				Popup: &popup,
			},
		},
	}
	if err := db.CreateMessageChannel(channel); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetMessageChannel(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MessageRules) != 2 || loaded.MessageRules[0].Type != "important" ||
		loaded.MessageRules[1].Verification == nil || loaded.MessageRules[1].Verification.Type != "auto" {
		t.Fatalf("unexpected message rules after round trip: %#v", loaded.MessageRules)
	}

	message := &MessageRecord{
		IdentityID:  identity.ID,
		ChannelID:   channel.ID,
		Title:       "磁盘告警",
		Content:     "生产服务器磁盘空间不足",
		MessageType: "important",
		MessageRule: "重要告警",
		Popup:       true,
		PopupType:   "important",
		Source:      "monitor",
	}
	if err := db.CreateMessage(message, []*Device{device}); err != nil {
		t.Fatal(err)
	}
	messages, err := db.ListMessagesForIdentity(identity.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	got := messages[0]
	if got.MessageType != "important" || got.MessageRule != "重要告警" ||
		!got.Popup || got.PopupType != "important" {
		t.Fatalf("message metadata was not persisted: %#v", got)
	}
}

func TestLegacyVerificationRulesAreExposedAsMessageRules(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentity("Legacy Messages", "admin")
	if err != nil {
		t.Fatal(err)
	}
	device := &Device{
		ID: "dev_legacy_message_rules", Name: "Legacy Message Rules Device",
		Fingerprint: "legacy-message-rules-fingerprint", InstallationID: "legacy-message-rules-install",
		ApprovalState:         EnrollmentApproved,
		RequestedCapabilities: []string{"proxy.client"},
		ApprovedCapabilities:  []string{"proxy.client"},
	}
	if err := db.UpsertDevice(device); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDeviceIdentity(device.ID, identity.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	channel := &MessageChannel{
		ID:                     "legacy-message-rules",
		IdentityID:             identity.ID,
		Name:                   "Legacy",
		DeviceIDs:              []string{device.ID},
		UseDefaultVerification: true,
		VerificationRules: []VerificationRule{{
			Name: "服务告警", Keywords: []string{"服务异常"},
			Popup: boolValue(true), PopupType: "important",
		}},
	}
	if err := db.CreateMessageChannel(channel); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetMessageChannel(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MessageRules) < 2 {
		t.Fatalf("legacy rules were not converted: %#v", loaded.MessageRules)
	}
	if loaded.MessageRules[0].Type != "important" || loaded.MessageRules[0].Verification != nil {
		t.Fatalf("legacy important rule still behaves like verification: %#v", loaded.MessageRules[0])
	}
	last := loaded.MessageRules[len(loaded.MessageRules)-1]
	if !last.Default || last.Type != "verification_code" || last.Verification == nil {
		t.Fatalf("default verification rule missing after legacy conversion: %#v", last)
	}
}
