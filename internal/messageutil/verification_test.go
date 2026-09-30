package messageutil

import (\n\t"strings"\n\t"testing"\n)

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
		{"no semantics", "订单号 482931 已支付，金额 99 元。", ""},
		{"long number rejected", "验证码 13800138000，请核对。", ""},
		{"keyword too far", "验证码："+strings.Repeat("说明", 30)+" 123456", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractVerificationCode(tc.text); got != tc.want {
				t.Fatalf("ExtractVerificationCode(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
