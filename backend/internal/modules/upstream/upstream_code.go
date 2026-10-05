package upstream

import "strings"

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
