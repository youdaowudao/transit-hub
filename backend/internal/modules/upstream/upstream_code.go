package upstream

import "strings"

const (
	ErrorAnnouncementAckRequired     = "admin.upstream.errors.announcementAckRequired"
	ErrorUpstreamInsufficientBalance = "admin.upstream.errors.upstreamInsufficientBalance"
	ErrorUpstreamKeyQuotaExhausted   = "admin.upstream.errors.upstreamKeyQuotaExhausted"
	ErrorUpstreamKeyExpired          = "admin.upstream.errors.upstreamKeyExpired"
)

// KnownUpstreamCodeErrorKey maps only the documented refusal identifiers.
// An unknown identifier keeps the caller's existing fallback.
func KnownUpstreamCodeErrorKey(code string) string {
	switch strings.ToUpper(code) {
	case "ANNOUNCEMENT_ACK_REQUIRED":
		return ErrorAnnouncementAckRequired
	case "INSUFFICIENT_BALANCE":
		return ErrorUpstreamInsufficientBalance
	case "API_KEY_QUOTA_EXHAUSTED", "INSUFFICIENT_QUOTA":
		return ErrorUpstreamKeyQuotaExhausted
	case "API_KEY_EXPIRED":
		return ErrorUpstreamKeyExpired
	}
	return ""
}

func applyKnownUpstreamCodeReason(detail *RequestError) {
	if key := KnownUpstreamCodeErrorKey(detail.UpstreamCode); key != "" {
		detail.Reason = key
		detail.MessageKey = ErrorRequest
	}
}

// ParseUpstreamCode only retains an identifier, never the upstream message/body.
func ParseUpstreamCode(record map[string]any) string {
	candidates := []any{record["code"]}
	if nested, ok := record["error"].(map[string]any); ok {
		candidates = append(candidates, nested["code"], nested["type"])
	}
	for _, candidate := range candidates {
		value, ok := candidate.(string)
		if !ok || len(value) == 0 || len(value) > 64 {
			continue
		}
		safe := true
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
				safe = false
				break
			}
		}
		if safe {
			return strings.ToUpper(value)
		}
	}
	return ""
}
