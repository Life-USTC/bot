package textutil

import (
	"regexp"
	"strings"
)

var (
	logURLPattern    = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	logBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	logSecretPattern = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|api[_-]?key|authorization|client_secret|token|secret)(\s*["']?\s*[:=]\s*["']?)([^\s,"'}]+)`)
	logJWTPattern    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]{8,})?\b`)
)

// SafeLogText removes URLs and common credential forms before diagnostic text
// reaches process logs. Full evidence may remain in the private database, but
// logs are intentionally low-cardinality and non-secret.
func SafeLogText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = logURLPattern.ReplaceAllString(value, "<url>")
	value = logBearerPattern.ReplaceAllString(value, "Bearer <redacted>")
	value = logSecretPattern.ReplaceAllString(value, "$1$2<redacted>")
	value = logJWTPattern.ReplaceAllString(value, "<redacted-jwt>")
	const maxRunes = 1000
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes]) + "…"
	}
	return value
}

func SafeLogError(err error) string {
	if err == nil {
		return ""
	}
	return SafeLogText(err.Error())
}
