package messageutil

import (
	"strings"
	"testing"
)

func TestExtractVerificationCode(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"chinese", "【Relay】您的登录验证码为 482931，5分钟内有效。", "482931"},
		{"code before keyword", "839214 是您的验证码，请勿向他人透露。", "839214"},
		{"english otp", "Your OTP code is 7712. It expires in 5 minutes.", "7712"},
		{"nearest number wins", "订单 20260930，登录验证码 654321，金额 100 元。", "654321"},
		{"dynamic secret", "512360是您的4A系统动态密钥，请遵守法规，严禁非法使用客户信息，系统将记录审计您的操作行为【中国移动】 &#x20;", "512360"},
		{"eip addon code", "【四川移动管信系统】您的EIP登录附加码是：G931，在当日有效。https://m.scmcc.com.cn/m/aumC", "G931"},
		{"mixed code", "您的认证码为 A7K9P2，请勿泄露。", "A7K9P2"},
		{"no semantics", "订单号 482931 已支付，金额 99 元。", ""},
		{"long number rejected", "验证码 13800138000，请核对。", ""},
		{"keyword too far", "验证码：" + strings.Repeat("说明", 30) + " 123456", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractVerificationCode(tc.text); got != tc.want {
				t.Fatalf("ExtractVerificationCode(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestExtractVerificationCodeWithRules(t *testing.T) {
	rules := []VerificationRule{
		{
			Name:        "vendor token",
			Keywords:    []string{"访问口令"},
			Pattern:     `TOKEN-([A-Z0-9]{5})`,
			MaxDistance: 80,
		},
	}
	text := "供应商通知：访问口令 TOKEN-X7P31，请在十分钟内使用。"
	if got := ExtractVerificationCodeWithRules(text, true, rules); got != "X7P31" {
		t.Fatalf("custom rule = %q, want X7P31", got)
	}
}

func TestExtractVerificationCodeWithPatternOnlyRule(t *testing.T) {
	rules := []VerificationRule{{
		Name:    "embedded",
		Pattern: `AUTH#([A-Z0-9]{4})`,
	}}
	if got := ExtractVerificationCodeWithRules("message AUTH#Q91Z done", false, rules); got != "Q91Z" {
		t.Fatalf("pattern-only rule = %q, want Q91Z", got)
	}
}

func TestCustomRuleAllowsLetterOnlyCode(t *testing.T) {
	rules := []VerificationRule{{
		Name:     "letter-only",
		Keywords: []string{"授权码"},
		Pattern:  `([A-Z]{5})`,
	}}
	if got := ExtractVerificationCodeWithRules("您的授权码是 QWERT，请妥善保管。", false, rules); got != "QWERT" {
		t.Fatalf("letter-only custom code = %q, want QWERT", got)
	}
}

func TestCustomRulesPrecedeDefault(t *testing.T) {
	rules := []VerificationRule{{
		Name:     "preferred",
		Keywords: []string{"验证码"},
		Pattern:  `[A-Z][0-9]{3}`,
	}}
	if got := ExtractVerificationCodeWithRules("验证码 G931，备用验证码 482931", true, rules); got != "G931" {
		t.Fatalf("custom precedence = %q, want G931", got)
	}
}

func TestValidateVerificationRule(t *testing.T) {
	if err := ValidateVerificationRule(VerificationRule{Name: "bad", Pattern: "("}); err == nil {
		t.Fatal("invalid regexp accepted")
	}
	if err := ValidateVerificationRule(VerificationRule{Name: "empty"}); err == nil {
		t.Fatal("empty rule accepted")
	}
	if err := ValidateVerificationRule(VerificationRule{Name: "ok", Keywords: []string{"附加码"}}); err != nil {
		t.Fatalf("valid keyword rule rejected: %v", err)
	}
}

func TestMatchVerificationCodeWithPopupMetadata(t *testing.T) {
	disabled := false
	rules := []VerificationRule{
		{
			Name: "default editable", Default: true, Keywords: []string{"验证码"},
			MaxDistance: 64, Popup: &disabled, PopupType: "important",
		},
		{
			Name: "vendor", Keywords: []string{"附加码"}, Pattern: `([A-Z][0-9]{3})`,
			MaxDistance: 64, PopupType: "message",
		},
	}
	match := MatchVerificationCodeWithRules("您的附加码是 G931，请及时使用。", true, rules)
	if match.Code != "G931" || match.RuleName != "vendor" || !match.Popup || match.PopupType != "message" {
		t.Fatalf("unexpected custom match: %#v", match)
	}

	match = MatchVerificationCodeWithRules("验证码 482931", true, rules)
	if match.Code != "482931" || match.Popup || match.PopupType != "important" {
		t.Fatalf("unexpected default match: %#v", match)
	}
}
