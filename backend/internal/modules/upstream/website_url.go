package upstream

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// This validates a browser destination only. It never normalizes an API address
// or sends a request, so a bad website cannot consume a rotating refresh token.
func normalizeWebsiteURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	fail := func(reason string) (string, error) {
		return "", &FieldValidationError{Fields: []string{"websiteUrl"}, Detail: "网站地址未保存：" + reason + "。本次没有请求上游或保存站点资料，填写的 Refresh Token 未使用，不会修改主站；改正后可重新保存。"}
	}
	if strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fail("链接中含有空格、换行或其他不能用于网址的字符，请删除后重试")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return fail("端口必须是 0 到 65535 的数字，请改正后重试")
		}
		return fail("请填写带 http:// 或 https:// 和完整域名的地址")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "" && scheme != "http" && scheme != "https" {
		return fail("只支持 http:// 或 https://")
	}
	if parsed.Hostname() == "" || (!strings.HasPrefix(strings.ToLower(value), "http://") && !strings.HasPrefix(strings.ToLower(value), "https://")) {
		return fail("请填写带 http:// 或 https:// 和完整域名的地址")
	}
	if parsed.User != nil {
		return fail("链接中含有用户名或密码，请删除后重试")
	}
	host := parsed.Hostname()
	bracketed := strings.HasPrefix(parsed.Host, "[")
	if bracketed || strings.ContainsAny(host, "[]:") {
		if !bracketed || !strings.Contains(host, ":") || net.ParseIP(host) == nil {
			return fail("请填写带 http:// 或 https:// 和完整域名的地址")
		}
	} else {
		host = strings.TrimSuffix(host, ".")
		last := host[strings.LastIndex(host, ".")+1:]
		hex := strings.HasPrefix(strings.ToLower(last), "0x") && strings.Trim(last[2:], "0123456789abcdefABCDEF") == ""
		if (last != "" && strings.Trim(last, "0123456789") == "" || hex) && !validWebsiteIPv4Host(host) {
			return fail("请填写带 http:// 或 https:// 和完整域名的地址")
		}
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 0 || number > 65535 {
			return fail("端口必须是 0 到 65535 的数字，请改正后重试")
		}
	}
	return value, nil
}

// Browser IPv4 URLs also accept shortened, octal and hexadecimal numbers.
// Validate their bounds without rewriting the user's saved destination.
func validWebsiteIPv4Host(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	var last uint64
	for i, part := range parts {
		if part == "" {
			return false
		}
		base := 10
		if strings.HasPrefix(strings.ToLower(part), "0x") {
			base, part = 16, part[2:]
		} else if len(part) > 1 && part[0] == '0' {
			base, part = 8, part[1:]
		}
		var value uint64
		if part != "" {
			parsed, err := strconv.ParseUint(part, base, 32)
			if err != nil {
				return false
			}
			value = parsed
		}
		if i < len(parts)-1 && value > 255 {
			return false
		}
		last = value
	}
	return last < uint64(1)<<(8*(5-len(parts)))
}
