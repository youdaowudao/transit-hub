package upstream

import (
	"context"
	"errors"
	"log"
	"strings"
)

// The new error key is the only explicit provenance of a save-only record.
// Missing identity in a legacy/previously connected site is never sufficient.
func siteSavedWithoutVerification(site *Site) bool {
	return site != nil && site.Platform == PlatformSub2API && site.Status == StatusError &&
		site.ErrorKey != nil && *site.ErrorKey == ErrorSavedWithoutVerification &&
		site.LastSyncedAt == nil && site.Session != nil && site.Session.AuthMode == AuthModeToken &&
		strings.TrimSpace(site.Session.UserID) == "" && !site.Session.UserIDConfirmed
}

func (s *Service) prepareForceSavedSite(site *Site, dto CreateRequest, apiURL string) error {
	baseURL, err := s.platformServiceURL(dto.SiteURL)
	if err != nil {
		return err
	}
	tokenType := strings.TrimSpace(dto.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	key := ErrorSavedWithoutVerification
	site.BaseURL, site.Platform = baseURL, PlatformSub2API
	site.APIURL = apiURL
	if site.APIURL == "" {
		site.APIURL = baseURL
	}
	site.Enabled, site.Status, site.ErrorKey = boolPointer(false), StatusError, &key
	site.Session = &Session{AuthMode: AuthModeToken, Platform: PlatformSub2API, BaseURL: baseURL,
		AccessToken: strings.TrimSpace(dto.AccessToken), RefreshToken: strings.TrimSpace(dto.RefreshToken), TokenType: tokenType}
	return nil
}

func unverifiedSiteChangeError(platform Platform) error {
	return identityCheckError(platform, "这条资料尚未验证，不能在这里更换网站地址或登录方式；请重新添加，当前资料保持不变，不会修改主站。")
}

// First verification binds a saved-only record to its first real principal.
// Existing sites continue using the old/new identity comparison in Update.
func (s *Service) verifyForceSavedSite(ctx context.Context, dto UpdateRequest, baseURL string) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, err
	}
	result, err := s.updateLogin(dto)
	if err != nil {
		return LoginResult{}, err
	}
	if result.Platform != PlatformSub2API || result.Session.BaseURL != baseURL {
		return LoginResult{}, identityCheckError(PlatformSub2API, "新登录返回的网站地址或平台与保存资料不同，本次没有保存；请核对网站地址后重试，不会修改主站。")
	}
	userID, _, err := s.platformService.readLoginPrincipal(ctx, result.Session)
	if err != nil {
		return LoginResult{}, err
	}
	if userID == "" {
		return LoginResult{}, identityCheckError(PlatformSub2API, "上游没有返回合法用户编号，登录尚未验证，本次资料未保存；请到上游核对账号后重新填写令牌，不会修改主站。")
	}
	result.Session.UserID, result.Session.UserIDConfirmed = userID, true
	return result, nil
}

// Create alone retains this short, credential-masked refusal summary. Never log
// a raw body or an arbitrary upstream code, which may itself echo a credential.
func logCreateLoginFailure(dto CreateRequest, err error) {
	stage, status, message := "", 0, ""
	var detail *RequestError
	if errors.As(err, &detail) {
		stage, status, message = detail.Stage, detail.StatusCode, detail.UpstreamMessage
	}
	secrets := []string{dto.AccessToken, dto.RefreshToken, dto.Password}
	message = safeUpstreamMessage(message, secrets...)
	if message == "" {
		message = "上游未提供可展示的具体原因"
	}
	log.Printf("[upstream] 登录失败 site=新建 name=%q host=%s stage=%s reason=%s status=%d detail=%q",
		safeUpstreamMessage(dto.Name, secrets...), safeHost(dto.SiteURL), stage, siteErrorKey(err), status, message)
}
