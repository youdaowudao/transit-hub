package upstream

import (
	"context"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// publicAPIURLTimeout bounds the one best-effort read made while saving a site.
// It is a variable only so tests can shorten it.
var publicAPIURLTimeout = 5 * time.Second

// normalizeImportAPIURL validates the address that imported main-site accounts
// call for models. It only checks the format and never sends a request, so a bad
// value cannot consume a rotating refresh token.
func normalizeImportAPIURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	fail := func(reason string) (string, error) {
		return "", &FieldValidationError{Fields: []string{"apiUrl"}, Detail: "API 地址未保存：" + reason + "。本次没有请求上游或保存站点资料，填写的 Refresh Token 未使用，不会修改主站；改正后可重新保存。"}
	}
	if strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fail("链接中含有空格、换行或其他不能用于网址的字符，请删除后重试")
	}
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fail("请填写带 http:// 或 https:// 和完整域名的地址")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return fail("端口必须是 1 到 65535 的数字，请改正后重试")
		}
		return fail("请填写带 http:// 或 https:// 和完整域名的地址")
	}
	if parsed.User != nil {
		return fail("链接中含有用户名或密码，请删除后重试")
	}
	if port := parsed.Port(); port != "" {
		if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
			return fail("端口必须是 1 到 65535 的数字，请改正后重试")
		}
	}
	normalized, err := (&PlatformService{}).NormalizeURL(value)
	if err != nil {
		return fail("请填写带 http:// 或 https:// 和完整域名的地址")
	}
	return normalized, nil
}

// ImportBaseURL is the address main-site accounts use for model calls after an
// automatic import: the API address when set, otherwise the website address.
func (s Site) ImportBaseURL() string {
	if value := strings.TrimSpace(s.APIURL); value != "" {
		return value
	}
	return s.BaseURL
}

// ReadSub2APIPublicAPIURL reads the API address a Sub2API upstream publishes in
// its public settings. It needs no credentials and is best effort: any failure
// returns "" so imports keep using the website address.
func (s *PlatformService) ReadSub2APIPublicAPIURL(ctx context.Context, baseURL string) string {
	if s == nil || s.httpClient == nil || strings.TrimSpace(baseURL) == "" {
		return ""
	}
	// The public read never carries credentials: drop any user:password in the
	// website address, otherwise Go would turn it into a Basic Authorization header.
	endpoint, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || endpoint.Host == "" {
		return ""
	}
	endpoint.User = nil
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/settings/public"
	endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "", "", ""
	readCtx, cancel := context.WithTimeout(ctx, publicAPIURLTimeout)
	defer cancel()
	response, err := s.httpClient.requestJSONWithContext(readCtx, endpoint.String(), requestOptions{})
	if err != nil {
		log.Printf("[upstream] 读取上游公开 API 地址失败 host=%s category=%s", safeHost(baseURL), siteErrorKey(err))
		return ""
	}
	value := firstString(dataRecord(response.Payload), []string{"api_base_url", "apiBaseUrl"})
	if value == nil {
		return ""
	}
	normalized, err := normalizeImportAPIURL(*value)
	if err != nil {
		log.Printf("[upstream] 上游公开的 API 地址格式不对，已忽略 host=%s", safeHost(baseURL))
		return ""
	}
	return normalized
}

// apiURLChangedError rejects an edit whose API address changed while it was
// reading the published address, so a slower request never overwrites it.
func apiURLChangedError() error {
	return saveFailureError("API 地址在本次保存期间已被其他操作修改，本次请求未保存，原有资料保持不变；请刷新页面核对 API 地址后再保存，不会修改主站。", nil, 409)
}

// resolveSiteAPIURL keeps a filled API address, otherwise reads the Sub2API
// published one once. Other platforms stay empty and fall back to the website.
func (s *Service) resolveSiteAPIURL(ctx context.Context, apiURL string, platform Platform, baseURL string) string {
	if apiURL != "" || platform != PlatformSub2API || s.platformService == nil {
		return apiURL
	}
	return s.platformService.ReadSub2APIPublicAPIURL(ctx, baseURL)
}
