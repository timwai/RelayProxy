package messageutil

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	MessageTypeVerification = "verification_code"
	MessageTypeMessage      = "message"
	MessageTypeImportant    = "important"

	MatchTypeKeywords = "keywords"
	MatchTypeContains = "contains"
	MatchTypeRegex    = "regex"
	MatchTypeAll      = "all"

	KeywordModeAny = "any"
	KeywordModeAll = "all"

	ExtractorAuto  = "auto"
	ExtractorRegex = "regex"
)

type MessageMatch struct {
	MatchType     string   `json:"matchType"`
	Keywords      []string `json:"keywords,omitempty"`
	KeywordMode   string   `json:"keywordMode,omitempty"`
	Pattern       string   `json:"pattern,omitempty"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
}

type VerificationExtractor struct {
	Type         string `json:"type"`
	Pattern      string `json:"pattern,omitempty"`
	MaxDistance  int    `json:"maxDistance,omitempty"`
	MinLength    int    `json:"minLength,omitempty"`
	MaxLength    int    `json:"maxLength,omitempty"`
	AllowLetters bool   `json:"allowLetters,omitempty"`
	AllowDigits  bool   `json:"allowDigits,omitempty"`
	RequireDigit bool   `json:"requireDigit,omitempty"`
}

type MessageRule struct {
	Name         string                 `json:"name"`
	Type         string                 `json:"type"`
	Enabled      bool                   `json:"enabled"`
	Default      bool                   `json:"default,omitempty"`
	Match        MessageMatch           `json:"match"`
	Verification *VerificationExtractor `json:"verification,omitempty"`
	Popup        *bool                  `json:"popup,omitempty"`
}

type MessageRuleMatch struct {
	Matched          bool
	Type             string
	RuleName         string
	Popup            bool
	VerificationCode string
}

func DefaultMessageRule() MessageRule {
	enabled := true
	return MessageRule{
		Name:    "默认验证码",
		Type:    MessageTypeVerification,
		Enabled: true,
		Default: true,
		Match: MessageMatch{
			MatchType: MatchTypeKeywords,
			Keywords: []string{
				"验证码", "短信验证码", "校验码", "验证代码", "确认码", "登录码", "登录验证码",
				"动态码", "动态密码", "动态密钥", "一次性密码", "一次性验证码", "安全码", "安全验证码",
				"短信码", "附加码", "登录附加码", "认证码", "授权码", "口令码",
				"OTP", "TOTP", "verification code", "verify code", "one-time code", "one time code",
				"one-time password", "one time password", "passcode", "security code",
				"authentication code", "auth code",
			},
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
		Popup: &enabled,
	}
}

func RulePopup(rule MessageRule) bool {
	return rule.Popup == nil || *rule.Popup
}

func ValidateMessageRule(rule MessageRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" || len(rule.Name) > 80 {
		return fmt.Errorf("message rule name must contain 1-80 characters")
	}
	switch rule.Type {
	case MessageTypeVerification, MessageTypeMessage, MessageTypeImportant:
	default:
		return fmt.Errorf("message rule %q has unsupported type %q", rule.Name, rule.Type)
	}
	if err := validateMessageMatch(rule.Name, rule.Match); err != nil {
		return err
	}
	if rule.Type != MessageTypeVerification {
		if rule.Verification != nil {
			return fmt.Errorf("message rule %q may only define verification extraction for verification_code type", rule.Name)
		}
		return nil
	}
	if rule.Verification == nil {
		return fmt.Errorf("verification rule %q requires a verification extractor", rule.Name)
	}
	return validateVerificationExtractor(rule.Name, *rule.Verification)
}

func validateMessageMatch(name string, match MessageMatch) error {
	switch match.MatchType {
	case MatchTypeKeywords:
		keywords := normalizeKeywords(match.Keywords)
		if len(keywords) == 0 {
			return fmt.Errorf("message rule %q requires at least one keyword", name)
		}
		if len(keywords) > 32 {
			return fmt.Errorf("message rule %q has too many keywords", name)
		}
		for _, keyword := range keywords {
			if len(keyword) > 120 {
				return fmt.Errorf("message rule %q has a keyword longer than 120 characters", name)
			}
		}
		if match.KeywordMode != "" && match.KeywordMode != KeywordModeAny && match.KeywordMode != KeywordModeAll {
			return fmt.Errorf("message rule %q keywordMode must be any or all", name)
		}
	case MatchTypeContains:
		if strings.TrimSpace(match.Pattern) == "" || len(match.Pattern) > 500 {
			return fmt.Errorf("message rule %q contains text must contain 1-500 characters", name)
		}
	case MatchTypeRegex:
		if strings.TrimSpace(match.Pattern) == "" || len(match.Pattern) > 500 {
			return fmt.Errorf("message rule %q regex must contain 1-500 characters", name)
		}
		pattern := match.Pattern
		if !match.CaseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid message regex in %q: %w", name, err)
		}
	case MatchTypeAll:
	default:
		return fmt.Errorf("message rule %q matchType must be keywords, contains, regex or all", name)
	}
	return nil
}

func validateVerificationExtractor(name string, extractor VerificationExtractor) error {
	switch extractor.Type {
	case ExtractorAuto:
		minLength := extractor.MinLength
		maxLength := extractor.MaxLength
		if minLength == 0 {
			minLength = 4
		}
		if maxLength == 0 {
			maxLength = 8
		}
		if minLength < 1 || maxLength > 64 || minLength > maxLength {
			return fmt.Errorf("verification rule %q auto length must be between 1 and 64", name)
		}
		if !extractor.AllowLetters && !extractor.AllowDigits {
			return fmt.Errorf("verification rule %q auto extractor must allow letters or digits", name)
		}
		if extractor.RequireDigit && !extractor.AllowDigits {
			return fmt.Errorf("verification rule %q cannot require digits when digits are disabled", name)
		}
		if extractor.MaxDistance < 0 || extractor.MaxDistance > 1024 {
			return fmt.Errorf("verification rule %q maxDistance must be between 0 and 1024", name)
		}
	case ExtractorRegex:
		if strings.TrimSpace(extractor.Pattern) == "" || len(extractor.Pattern) > 500 {
			return fmt.Errorf("verification rule %q regex extractor must contain 1-500 characters", name)
		}
		if _, err := regexp.Compile(extractor.Pattern); err != nil {
			return fmt.Errorf("invalid verification extractor regex in %q: %w", name, err)
		}
	default:
		return fmt.Errorf("verification rule %q extractor type must be auto or regex", name)
	}
	return nil
}

func MatchMessageRules(text string, rules []MessageRule) MessageRuleMatch {
	text = strings.TrimSpace(text)
	if text == "" {
		return MessageRuleMatch{}
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		matched, keywordIndexes := messageRuleMatches(text, rule.Match)
		if !matched {
			continue
		}
		result := MessageRuleMatch{
			Matched:  true,
			Type:     rule.Type,
			RuleName: strings.TrimSpace(rule.Name),
			Popup:    RulePopup(rule),
		}
		if rule.Type == MessageTypeVerification {
			code := extractVerificationForMessageRule(text, keywordIndexes, *rule.Verification)
			if code == "" {
				continue
			}
			result.VerificationCode = code
		}
		return result
	}
	return MessageRuleMatch{}
}

func messageRuleMatches(text string, match MessageMatch) (bool, [][]int) {
	switch match.MatchType {
	case MatchTypeKeywords:
		keywords := normalizeKeywords(match.Keywords)
		indexes := keywordMatches(text, keywords, match.CaseSensitive)
		if len(indexes) == 0 {
			return false, nil
		}
		mode := match.KeywordMode
		if mode == "" {
			mode = KeywordModeAny
		}
		if mode == KeywordModeAny {
			return true, indexes
		}
		searchText := text
		if !match.CaseSensitive {
			searchText = strings.ToLower(text)
		}
		for _, keyword := range keywords {
			needle := keyword
			if !match.CaseSensitive {
				needle = strings.ToLower(keyword)
			}
			if !strings.Contains(searchText, needle) {
				return false, nil
			}
		}
		return true, indexes
	case MatchTypeContains:
		if match.CaseSensitive {
			return strings.Contains(text, match.Pattern), nil
		}
		return strings.Contains(strings.ToLower(text), strings.ToLower(match.Pattern)), nil
	case MatchTypeRegex:
		pattern := match.Pattern
		if !match.CaseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		re, err := regexp.Compile(pattern)
		return err == nil && re.MatchString(text), nil
	case MatchTypeAll:
		return true, nil
	default:
		return false, nil
	}
}

func extractVerificationForMessageRule(text string, keywordIndexes [][]int, extractor VerificationExtractor) string {
	switch extractor.Type {
	case ExtractorRegex:
		re, err := regexp.Compile(extractor.Pattern)
		if err != nil {
			return ""
		}
		for _, match := range re.FindAllStringSubmatchIndex(text, -1) {
			start, end := captureBounds(match)
			if start < 0 || !codeBoundary(text, start, end) {
				continue
			}
			return text[start:end]
		}
		return ""
	case ExtractorAuto:
		minLength := extractor.MinLength
		maxLength := extractor.MaxLength
		if minLength == 0 {
			minLength = 4
		}
		if maxLength == 0 {
			maxLength = 8
		}
		var class string
		switch {
		case extractor.AllowLetters && extractor.AllowDigits:
			class = "A-Za-z0-9"
		case extractor.AllowDigits:
			class = "0-9"
		case extractor.AllowLetters:
			class = "A-Za-z"
		default:
			return ""
		}
		re, err := regexp.Compile(fmt.Sprintf("[%s]{%d,%d}", class, minLength, maxLength))
		if err != nil {
			return ""
		}
		maxDistance := extractor.MaxDistance
		if maxDistance <= 0 {
			maxDistance = 64
		}
		if len(keywordIndexes) > 0 {
			return bestCandidate(text, re, keywordIndexes, maxDistance, true, extractor.RequireDigit)
		}
		for _, match := range re.FindAllStringIndex(text, -1) {
			if !codeBoundary(text, match[0], match[1]) {
				continue
			}
			value := text[match[0]:match[1]]
			if extractor.RequireDigit && !containsASCIIDigit(value) {
				continue
			}
			return value
		}
	}
	return ""
}
