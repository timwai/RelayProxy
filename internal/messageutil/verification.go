package messageutil

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	defaultVerificationKeyword = regexp.MustCompile(`(?i)(验证码|校验码|动态码|动态密钥|安全码|短信码|附加码|登录附加码|认证码|口令码|verification[\s_-]*code|verify[\s_-]*code|one[\s_-]*time[\s_-]*(?:password|code)|otp|passcode|security[\s_-]*code|auth(?:entication)?[\s_-]*code)`)
	defaultVerificationCode    = regexp.MustCompile(`[A-Za-z0-9]{4,8}`)
)

// VerificationRule extends the built-in detector. Pattern is a Go regular
// expression. If it contains capture groups, the first non-empty capture is
// returned as the code; otherwise the entire match is returned. When Keywords
// is non-empty, a candidate must be close to at least one keyword.
type VerificationRule struct {
	Name          string   `json:"name,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	Pattern       string   `json:"pattern,omitempty"`
	MaxDistance   int      `json:"maxDistance,omitempty"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
}

// ValidateVerificationRule checks a custom detector before it is persisted.
func ValidateVerificationRule(rule VerificationRule) error {
	if len(strings.TrimSpace(rule.Name)) > 80 {
		return fmt.Errorf("verification rule name is too long")
	}
	if len(rule.Keywords) > 32 {
		return fmt.Errorf("verification rule has too many keywords")
	}
	for _, keyword := range rule.Keywords {
		if len(strings.TrimSpace(keyword)) > 120 {
			return fmt.Errorf("verification keyword is too long")
		}
	}
	if rule.MaxDistance < 0 || rule.MaxDistance > 1024 {
		return fmt.Errorf("verification maxDistance must be between 0 and 1024")
	}
	if len(rule.Pattern) > 500 {
		return fmt.Errorf("verification pattern is too long")
	}
	if strings.TrimSpace(rule.Pattern) != "" {
		pattern := rule.Pattern
		if !rule.CaseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid verification pattern: %w", err)
		}
	}
	if strings.TrimSpace(rule.Pattern) == "" && len(normalizeKeywords(rule.Keywords)) == 0 {
		return fmt.Errorf("verification rule requires keywords or pattern")
	}
	return nil
}

// ExtractVerificationCode uses the built-in detector. It supports numeric and
// mixed letter/digit verification codes such as 482931 and G931.
func ExtractVerificationCode(message string) string {
	return ExtractVerificationCodeWithRules(message, true, nil)
}

// ExtractVerificationCodeWithRules evaluates custom rules first, then the
// built-in rule when useDefault is true. Custom rules therefore allow a channel
// to override ambiguous vendor-specific messages without disabling the safe
// default detector.
func ExtractVerificationCodeWithRules(message string, useDefault bool, rules []VerificationRule) string {
	text := strings.TrimSpace(message)
	if text == "" {
		return ""
	}

	for _, rule := range rules {
		code, ok := extractWithRule(text, rule)
		if ok {
			return code
		}
	}
	if !useDefault {
		return ""
	}
	return extractDefault(text)
}

func extractDefault(text string) string {
	keywords := defaultVerificationKeyword.FindAllStringIndex(text, -1)
	if len(keywords) == 0 {
		return ""
	}
	return bestCandidate(text, defaultVerificationCode, keywords, 64, true)
}

func extractWithRule(text string, rule VerificationRule) (string, bool) {
	if err := ValidateVerificationRule(rule); err != nil {
		return "", false
	}
	pattern := strings.TrimSpace(rule.Pattern)
	if pattern == "" {
		pattern = `[A-Za-z0-9]{4,8}`
	}
	if !rule.CaseSensitive {
		pattern = "(?i:" + pattern + ")"
	}
	candidateRE, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}

	keywords := normalizeKeywords(rule.Keywords)
	if len(keywords) == 0 {
		for _, match := range candidateRE.FindAllStringSubmatchIndex(text, -1) {
			start, end := captureBounds(match)
			if start < 0 || !codeBoundary(text, start, end) {
				continue
			}
			code := text[start:end]
			if validCodeCandidate(code) {
				return code, true
			}
		}
		return "", false
	}

	keywordIndexes := keywordMatches(text, keywords, rule.CaseSensitive)
	if len(keywordIndexes) == 0 {
		return "", false
	}
	maxDistance := rule.MaxDistance
	if maxDistance <= 0 {
		maxDistance = 64
	}
	code := bestCandidate(text, candidateRE, keywordIndexes, maxDistance, false)
	return code, code != ""
}

func bestCandidate(text string, candidateRE *regexp.Regexp, keywords [][]int, maxDistance int, preferCommonLengths bool) string {
	type candidate struct {
		value string
		start int
		score int
	}
	best := candidate{score: -1 << 30}
	for _, match := range candidateRE.FindAllStringSubmatchIndex(text, -1) {
		start, end := captureBounds(match)
		if start < 0 || !codeBoundary(text, start, end) {
			continue
		}
		value := text[start:end]
		if !validCodeCandidate(value) {
			continue
		}
		score := -1 << 30
		for _, keyword := range keywords {
			distance := intervalDistance(start, end, keyword[0], keyword[1])
			if distance > maxDistance {
				continue
			}
			current := 160 - distance*2
			if preferCommonLengths {
				switch len(value) {
				case 6:
					current += 28
				case 4:
					current += 22
				case 5:
					current += 12
				}
			}
			if start >= keyword[1] {
				current += 12
			}
			if containsASCIILetter(value) && containsASCIIDigit(value) {
				current += 10
			}
			if current > score {
				score = current
			}
		}
		if score > best.score || (score == best.score && (best.value == "" || start < best.start)) {
			best = candidate{value: value, start: start, score: score}
		}
	}
	if best.score < 0 {
		return ""
	}
	return best.value
}

func captureBounds(match []int) (int, int) {
	if len(match) < 2 {
		return -1, -1
	}
	for i := 2; i+1 < len(match); i += 2 {
		if match[i] >= 0 && match[i+1] >= 0 {
			return match[i], match[i+1]
		}
	}
	return match[0], match[1]
}

func keywordMatches(text string, keywords []string, caseSensitive bool) [][]int {
	searchText := text
	if !caseSensitive {
		searchText = strings.ToLower(text)
	}
	var indexes [][]int
	for _, keyword := range keywords {
		needle := keyword
		if !caseSensitive {
			needle = strings.ToLower(keyword)
		}
		offset := 0
		for {
			index := strings.Index(searchText[offset:], needle)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(needle)
			indexes = append(indexes, []int{start, end})
			offset = end
			if offset >= len(searchText) {
				break
			}
		}
	}
	return indexes
}

func normalizeKeywords(keywords []string) []string {
	seen := make(map[string]bool, len(keywords))
	out := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" || seen[keyword] {
			continue
		}
		seen[keyword] = true
		out = append(out, keyword)
	}
	return out
}

func intervalDistance(startA, endA, startB, endB int) int {
	switch {
	case startA >= endB:
		return startA - endB
	case startB >= endA:
		return startB - endA
	default:
		return 0
	}
}

func validCodeCandidate(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	// Default and keyword-only rules require at least one digit. This avoids
	// common words such as "code" becoming OTPs while still accepting G931.
	return containsASCIIDigit(value)
}

func containsASCIIDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func containsASCIILetter(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func codeBoundary(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8LastRune(text[:start])
		if isCodeRune(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8FirstRune(text[end:])
		if isCodeRune(r) {
			return false
		}
	}
	return true
}

func isCodeRune(r rune) bool {
	return unicode.IsDigit(r) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
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
