package security

import "regexp"

var sensitiveTextPattern = regexp.MustCompile(`(?i)(?:bearer\s+[A-Za-z0-9._~+/=-]+|sk-[A-Za-z0-9_-]{8,}|["']?(?:access[_-]?token|refresh[_-]?token|api[_-]?key|password|secret|cookie)["']?\s*[:=]\s*["']?[A-Za-z0-9._~+/=:@$%_-]+)`)

// RedactSensitiveText keeps local history useful without persisting common
// bearer tokens, API keys, cookies, or credential-shaped values.
func RedactSensitiveText(value string) string {
	return sensitiveTextPattern.ReplaceAllString(value, "[REDACTED]")
}
