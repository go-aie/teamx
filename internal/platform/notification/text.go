package notification

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// External fields are single-line facts. Escape mentions/Markdown delimiters so
// external titles cannot manufacture role pings or UI instructions.
func Fact(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		if r == '@' {
			return '＠'
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "`", "\\`").Replace(value)
	return boundedText(value, limit)
}

// Quote preserves the lines of external content while keeping it literal and
// preventing it from creating mentions, links, headings or nested Markdown.
func Quote(value string, limit int) string {
	value = strings.TrimSpace(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(value))
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	escape := strings.NewReplacer("#", "\\#", "-", "\\-", "+", "\\+", "!", "\\!", "|", "\\|", ".", "\\.", ":", "\\:", "(", "\\(", ")", "\\)", "~", "\\~")
	for i, line := range lines {
		lines[i] = "> " + escape.Replace(Fact(line, max(3, len(line)*4+3)))
	}
	return boundedText(strings.Join(lines, "\n"), limit)
}

func boundedText(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit-3]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value += "…"
	}
	return value
}
