package messageutil

import "testing"

func TestMessageRuleVerificationAutoExtractor(t *testing.T) {
	popup := true
	rules := []MessageRule{{
		Name:    "登录验证码",
		Type:    MessageTypeVerification,
		Enabled: true,
		Match: MessageMatch{
			MatchType:   MatchTypeKeywords,
			Keywords:    []string{"验证码", "登录码"},
			KeywordMode: KeywordModeAny,
		},
		Verification: &VerificationExtractor{
			Type:         ExtractorAuto,
			MaxDistance:  64,
			MinLength:    4,
			MaxLength:    8,
			AllowLetters: true,
			AllowDigits:  true,
			RequireDigit: true,
		},
		Popup: &popup,
	}}
	got := MatchMessageRules("【Relay】您的登录验证码为 G931，请勿泄露。", rules)
	if !got.Matched || got.Type != MessageTypeVerification || got.VerificationCode != "G931" || got.RuleName != "登录验证码" || !got.Popup {
		t.Fatalf("unexpected match: %#v", got)
	}
}

func TestMessageRuleNormalMessageNeedsNoVerificationCode(t *testing.T) {
	popup := true
	rules := []MessageRule{{
		Name:    "备份完成",
		Type:    MessageTypeMessage,
		Enabled: true,
		Match: MessageMatch{
			MatchType:   MatchTypeKeywords,
			Keywords:    []string{"备份完成", "任务完成"},
			KeywordMode: KeywordModeAny,
		},
		Popup: &popup,
	}}
	got := MatchMessageRules("生产数据库备份完成，请检查归档。", rules)
	if !got.Matched || got.Type != MessageTypeMessage || got.VerificationCode != "" || got.RuleName != "备份完成" {
		t.Fatalf("normal message incorrectly required a code: %#v", got)
	}
}

func TestMessageRuleImportantMessageNeedsNoVerificationCode(t *testing.T) {
	rules := []MessageRule{{
		Name:    "磁盘告警",
		Type:    MessageTypeImportant,
		Enabled: true,
		Match: MessageMatch{
			MatchType:   MatchTypeKeywords,
			Keywords:    []string{"磁盘空间不足", "服务异常"},
			KeywordMode: KeywordModeAny,
		},
	}}
	got := MatchMessageRules("生产服务器磁盘空间不足，请尽快处理。", rules)
	if !got.Matched || got.Type != MessageTypeImportant || got.VerificationCode != "" || !got.Popup {
		t.Fatalf("important message incorrectly required a code: %#v", got)
	}
}

func TestMessageRuleFirstMatchWins(t *testing.T) {
	rules := []MessageRule{
		{
			Name: "specific", Type: MessageTypeImportant, Enabled: true,
			Match: MessageMatch{MatchType: MatchTypeContains, Pattern: "生产服务器"},
		},
		{
			Name: "fallback", Type: MessageTypeMessage, Enabled: true,
			Match: MessageMatch{MatchType: MatchTypeAll},
		},
	}
	got := MatchMessageRules("生产服务器状态更新", rules)
	if got.RuleName != "specific" || got.Type != MessageTypeImportant {
		t.Fatalf("first matching rule did not win: %#v", got)
	}
}

func TestMessageRuleDisabledRuleIsSkipped(t *testing.T) {
	rules := []MessageRule{
		{
			Name: "disabled", Type: MessageTypeImportant, Enabled: false,
			Match: MessageMatch{MatchType: MatchTypeAll},
		},
		{
			Name: "fallback", Type: MessageTypeMessage, Enabled: true,
			Match: MessageMatch{MatchType: MatchTypeAll},
		},
	}
	got := MatchMessageRules("hello", rules)
	if got.RuleName != "fallback" {
		t.Fatalf("disabled rule matched: %#v", got)
	}
}

func TestMessageRuleRegexExtractorUsesCaptureGroup(t *testing.T) {
	rules := []MessageRule{{
		Name:    "vendor",
		Type:    MessageTypeVerification,
		Enabled: true,
		Match:   MessageMatch{MatchType: MatchTypeContains, Pattern: "访问口令"},
		Verification: &VerificationExtractor{
			Type:    ExtractorRegex,
			Pattern: `TOKEN-([A-Z0-9]{5})`,
		},
	}}
	got := MatchMessageRules("访问口令 TOKEN-X7P31，请及时使用。", rules)
	if got.VerificationCode != "X7P31" {
		t.Fatalf("regex capture = %q, want X7P31", got.VerificationCode)
	}
}

func TestDefaultMessageRuleMatchesExpandedKeywords(t *testing.T) {
	rule := DefaultMessageRule()
	for _, text := range []string{
		"您的动态密码为 482931，请勿泄露。",
		"一次性验证码：7712，5分钟内有效。",
		"Your TOTP is 654321.",
	} {
		got := MatchMessageRules(text, []MessageRule{rule})
		if !got.Matched || got.VerificationCode == "" {
			t.Fatalf("default rule did not match %q: %#v", text, got)
		}
	}
}

func TestValidateMessageRuleRejectsVerificationFieldsOnNormalMessage(t *testing.T) {
	rule := MessageRule{
		Name:    "bad",
		Type:    MessageTypeMessage,
		Enabled: true,
		Match:   MessageMatch{MatchType: MatchTypeAll},
		Verification: &VerificationExtractor{
			Type: ExtractorAuto, AllowDigits: true,
		},
	}
	if err := ValidateMessageRule(rule); err == nil {
		t.Fatal("normal message rule accepted verification extractor")
	}
}


func TestMessageRulePopupTypeCanDifferFromMessageType(t *testing.T) {
	popup := true
	rules := []MessageRule{{
		Name:      "普通消息重要弹窗",
		Type:      MessageTypeMessage,
		Enabled:   true,
		Match:     MessageMatch{MatchType: MatchTypeAll},
		Popup:     &popup,
		PopupType: MessageTypeImportant,
	}}
	got := MatchMessageRules("普通状态更新", rules)
	if !got.Matched || got.Type != MessageTypeMessage || got.PopupType != MessageTypeImportant || !got.Popup {
		t.Fatalf("independent popup type not preserved: %#v", got)
	}
}

func TestMessageRulePopupTypeDefaultsToMessageType(t *testing.T) {
	rules := []MessageRule{{
		Name:    "默认弹窗类型",
		Type:    MessageTypeImportant,
		Enabled: true,
		Match:   MessageMatch{MatchType: MatchTypeAll},
	}}
	got := MatchMessageRules("告警", rules)
	if got.PopupType != MessageTypeImportant {
		t.Fatalf("popup type fallback = %q, want %q", got.PopupType, MessageTypeImportant)
	}
}

func TestValidateMessageRuleRejectsInvalidPopupType(t *testing.T) {
	rule := MessageRule{
		Name:      "bad popup",
		Type:      MessageTypeMessage,
		Enabled:   true,
		PopupType: "unsupported",
		Match:     MessageMatch{MatchType: MatchTypeAll},
	}
	if err := ValidateMessageRule(rule); err == nil {
		t.Fatal("invalid popupType was accepted")
	}
}
