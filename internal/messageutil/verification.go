package messageutil

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	verificationKeyword = regexp.MustCompile(`(?i)(验证码|校验码|动态码|安全码|短信码|verification[\s_-]*code|verify[\s_-]*code|one[\s_-]*time[\s_-]*(?:password|code)|otp|passcode)`)
	verificationDigits  = regexp.MustCompile(`[0-9]{4,8}`)
)

// ExtractVerificationCode returns a likely numeric verification code only when
// the message contains verification-code semantics. Requiring a nearby keyword
// avoids treating order numbers, phone numbers and amounts as OTPs.
func ExtractVerificationCode(message string) string {
	text := strings.TrimSpace(message)
	if text == "" {
		return ""
	}
	keywords := verificationKeyword.FindAllStringIndex(text, -1)
	if len(keywords) == 0 {
		return ""
	}

	type candidate struct {
		value string
		start int
		end   int
		score int
	}
	var best candidate
	for _, match := range verificationDigits.FindAllStringIndex(text, -1) {
		if !digitBoundary(text, match[0], match[1]) {
			continue
		}
		value := text[match[0]:match[1]]
		score := -1 << 30
		for _, keyword := range keywords {
			distance := 0
			switch {
			case match[0] >= keyword[1]:
				distance = match[0] - keyword[1]
			case keyword[0] >= match[1]:
				distance = keyword[0] - match[1]
			default:
				distance = 0
			}
			// Verification code wording is normally close to the code itself.
			// Keep the window deliberately small to reject unrelated numbers in
			// a long notification that happens to mention "验证码" elsewhere.
			if distance > 48 {
				continue
			}
			current := 120 - distance*2
			if len(value) == 6 {
				current += 28
			} else if len(value) == 4 {
				current += 18
			}
			if match[0] >= keyword[1] {
				current += 12
			}
			if current > score {
				score = current
			}
		}
		if score > best.score || (score == best.score && match[0] < best.start) {
			best = candidate{value: value, start: match[0], end: match[1], score: score}
		}
	}
	if best.score < 0 {
		return ""
	}
	return best.value
}

func digitBoundary(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8LastRune(text[:start])
		if unicode.IsDigit(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8FirstRune(text[end:])
		if unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func utf8FirstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func utf8LastRune(s string) (rune, int) {
	var last rune
	var size int
	for _, r := range s {
		last = r
		size = len(string(r))
	}
	return last, size
}
