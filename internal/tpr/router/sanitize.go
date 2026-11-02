package router

import (
	"encoding/json"
	"regexp"
	"strings"
)

// maxSanitized bounds a stored error message. A response body must never be kept.
const maxSanitized = 200

var secretPatterns = []*regexp.Regexp{
	// Authorization values and token-looking key=value pairs.
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)"?\b(access_token|refresh_token|id_token|api[_-]?key|x-api-key|authorization|password|secret|token)\b"?\s*[:=]\s*"?[^"\s,;}&]+"?`),
	// JWTs and provider key formats.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}(\.[A-Za-z0-9_-]+)?`),
	regexp.MustCompile(`\b(sk|rk|pk|xai|ya29|gsk)[-_.][A-Za-z0-9_-]{8,}`),
	// Any other long opaque run: hex digests, base64 tokens, session IDs.
	regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`),
}

// SanitizeError reduces an upstream failure to a short sentence that is safe to
// show and store. It prefers the provider's own error message, removes anything
// shaped like a credential, flattens it to one line and truncates it.
func SanitizeError(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return ""
	}
	if extracted := errorMessageFromJSON(msg); extracted != "" {
		msg = extracted
	}
	for _, pattern := range secretPatterns {
		msg = pattern.ReplaceAllString(msg, "[redacted]")
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxSanitized {
		msg = strings.ToValidUTF8(msg[:maxSanitized], "") + "…"
	}
	return msg
}

// errorMessageFromJSON returns the human message of a JSON error body. A JSON
// body with no recognizable message gives a fixed phrase, so its contents are
// dropped rather than stored.
func errorMessageFromJSON(body string) string {
	if !strings.HasPrefix(body, "{") && !strings.HasPrefix(body, "[") {
		return ""
	}
	var parsed struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Error   any    `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	switch e := parsed.Error.(type) {
	case string:
		if strings.TrimSpace(e) != "" {
			return e
		}
	case map[string]any:
		for _, key := range []string{"message", "code", "type"} {
			if v, ok := e[key].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	if strings.TrimSpace(parsed.Message) != "" {
		return parsed.Message
	}
	if strings.TrimSpace(parsed.Detail) != "" {
		return parsed.Detail
	}
	return "upstream returned an error"
}
