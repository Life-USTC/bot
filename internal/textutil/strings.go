package textutil

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func MonospaceDigits(text string) string {
	return mapMonospace(text, false)
}

func MonospaceASCII(text string) string {
	return mapMonospace(text, true)
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
