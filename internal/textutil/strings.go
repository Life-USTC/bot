package textutil

import "strings"

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func NonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func TrimEqualFold(value, target string) bool {
	return strings.EqualFold(strings.TrimSpace(value), target)
}

func IndexASCIIToken(text, token string) int {
	if token == "" {
		return -1
	}
	index := strings.Index(text, token)
	for index >= 0 {
		before := index == 0 || !isASCIIAlnum(text[index-1])
		afterIndex := index + len(token)
		after := afterIndex == len(text) || !isASCIIAlnum(text[afterIndex])
		if before && after {
			return index
		}
		next := strings.Index(text[index+1:], token)
		if next < 0 {
			return -1
		}
		index += next + 1
	}
	return -1
}

func MonospaceDigits(text string) string {
	return mapMonospace(text, false)
}

func MonospaceASCII(text string) string {
	return mapMonospace(text, true)
}

func PlainDigits(text string) string {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		if r >= '𝟶' && r <= '𝟿' {
			out = append(out, '0'+(r-'𝟶'))
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func PadRightDisplay(text string, width int) string {
	return PadRightDisplayWith(text, width, " ")
}

func PadRightDisplayWith(text string, width int, pad string) string {
	if text == "" {
		return ""
	}
	padding := width - DisplayWidth(text)
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat(pad, padding)
}

func PadRightDisplayWide(text string, width int) string {
	if text == "" {
		return ""
	}
	padding := width - DisplayWidth(text)/2
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat("\u3000", padding)
}

func DisplayWidth(text string) int {
	width := 0
	for _, r := range text {
		if isWideRune(r) {
			width += 2
			continue
		}
		width++
	}
	return width
}

func mapMonospace(text string, includeUppercase bool) string {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9':
			out = append(out, '𝟶'+(r-'0'))
		case includeUppercase && r >= 'A' && r <= 'Z':
			out = append(out, '𝙰'+(r-'A'))
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

func isWideRune(r rune) bool {
	return (r >= 0x2E80 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6)
}

func isASCIIAlnum(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}
