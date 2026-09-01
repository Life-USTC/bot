package textutil

import (
	"io"
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

// RedactingLogWriter applies SafeLogText to complete log records before they
// leave the process. log.Logger performs one Write per record, so installing
// this writer at composition time protects every package that shares the
// process logger without requiring each call site to remember redaction.
func RedactingLogWriter(destination io.Writer) io.Writer {
	return redactingLogWriter{destination: destination}
}

type redactingLogWriter struct {
	destination io.Writer
}

func (w redactingLogWriter) Write(input []byte) (int, error) {
	if w.destination == nil {
		return len(input), nil
	}
	newline := strings.HasSuffix(string(input), "\n")
	output := SafeLogText(string(input))
	if newline {
		output += "\n"
	}
	if _, err := io.WriteString(w.destination, output); err != nil {
		return 0, err
	}
	return len(input), nil
}
