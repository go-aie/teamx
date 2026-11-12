package room

import (
	_ "embed"
	"encoding/json"
	"strings"
)

type adminPasswordPolicy struct {
	MinLength       int      `json:"min_length"`
	MaxLength       int      `json:"max_length"`
	CommonPasswords []string `json:"common_passwords"`
	Sequences       []string `json:"sequences"`
}

//go:embed access_password_policy.json
var adminPasswordPolicyJSON []byte

var accessPasswordPolicy = func() adminPasswordPolicy {
	var policy adminPasswordPolicy
	if err := json.Unmarshal(adminPasswordPolicyJSON, &policy); err != nil {
		panic(err) // Invalid embedded policy is a build defect.
	}
	return policy
}()

// The creation policy is deliberately separate from credential decoding:
// existing passwords retain their authority until the owner replaces them.
func newAdminPasswordIssue(value string) string {
	for _, c := range value {
		if c < 32 || c > 126 {
			return "characters"
		}
	}
	if len(value) < accessPasswordPolicy.MinLength || len(value) > accessPasswordPolicy.MaxLength {
		return "length"
	}
	if strings.TrimSpace(value) != value {
		return "whitespace"
	}
	folded := strings.ToLower(value)
	normalized := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, folded)
	stem := strings.Trim(normalized, "0123456789")
	leet := strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t").Replace(stem)
	for _, common := range accessPasswordPolicy.CommonPasswords {
		if normalized == common || stem == common || leet == common {
			return "common"
		}
	}
	candidates := []string{folded}
	if len(normalized) >= 6 {
		candidates = append(candidates, normalized)
	}
	for _, candidate := range candidates {
		for size := 1; size <= len(candidate)/2; size++ {
			if len(candidate)%size == 0 && strings.Repeat(candidate[:size], len(candidate)/size) == candidate {
				return "repeat"
			}
		}
		for _, sequence := range accessPasswordPolicy.Sequences {
			reverse := []byte(sequence)
			for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
				reverse[i], reverse[j] = reverse[j], reverse[i]
			}
			if strings.Contains(sequence, candidate) || strings.Contains(string(reverse), candidate) {
				return "sequence"
			}
		}
	}
	return ""
}

func newAdminPasswordError(value string) string {
	switch newAdminPasswordIssue(value) {
	case "length":
		return "请输入 12–256 个字符。"
	case "characters":
		return "请使用英文字符、数字、空格或符号。"
	case "whitespace":
		return "密码首尾不能包含空白。"
	case "common":
		return "这是常见密码，请换一个不易猜到的密码。"
	case "repeat":
		return "请避免重复字符或重复组合。"
	case "sequence":
		return "请避免连续数字、字母或键盘顺序。"
	default:
		return ""
	}
}
