package upstream

import (
	"context"
	"errors"
	"strings"
)

const migrationRefreshWarning = "填写的 Refresh Token 可能已被使用，重试前请到上游重新获取。"

// domainMigrationLogin verifies a user-confirmed address change without relying
// on a reachable old domain when the stored login already identifies its owner.
// It only prepares this request's result; Update owns all publication.
func (s *Service) domainMigrationLogin(ctx context.Context, original Site, dto UpdateRequest, normalizedURL string, relogin bool, providedUserID string) (LoginResult, error) {
	if !dto.ConfirmDomainMigration {
		return LoginResult{}, identityCheckError(original.Platform, "更换站点地址前，请勾选确认这是原站点的新地址。本次没有请求上游，资料和费用未保存，不会修改主站。")
	}
	if original.Session == nil {
		return LoginResult{}, identityCheckError(original.Platform, "原站点没有保存过登录资料，无法核对新地址上的账号。请新增站点；本次没有请求上游，原资料和费用未保存，不会修改主站。")
	}
	if err := ctx.Err(); err != nil {
		return LoginResult{}, err
	}
	oldID, oldEmail := "", ""
	typedStoredID := false
	storedID := strings.TrimSpace(original.Session.UserID)
	switch {
	case original.Session.UserIDConfirmed && storedID != "":
		oldID = storedID
	case original.Platform == PlatformNewAPI && storedID != "":
		oldID = storedID
		typedStoredID = siteAuthMode(&original) == AuthModeUserKey
	case original.Platform == PlatformSub2API && original.Session.AuthMode == AuthModePassword && strings.TrimSpace(original.Account) != "":
		oldEmail = strings.TrimSpace(original.Account)
	default:
		var readErr error
		oldID, _, readErr = s.platformService.readLoginPrincipal(ctx, *original.Session)
		if err := ctx.Err(); err != nil {
			return LoginResult{}, err
		}
		if readErr != nil || oldID == "" {
			cause := "上游回复成功，但没有返回合法的用户编号"
			if readErr != nil {
				cause = identityReadCause(readErr)
			}
			return LoginResult{}, identityCheckError(original.Platform, "原登录没能读到账号编号（"+cause+"），原站点也没有保存可以核对的已核实编号或明确的密码登录资料，无法确认新地址上的账号。本次没有向新地址发送登录资料，填写的新凭据没有被使用；请核查原地址及原账号后重试，原地址已停用且无法核对时请新增站点。原资料和费用未保存，不会修改主站。")
		}
	}

	var result LoginResult
	var err error
	if relogin {
		result, err = s.updateLogin(dto)
	} else {
		result.Platform = original.Platform
		result.Session = *original.Session
		result.Session.BaseURL = normalizedURL
	}
	if ctx.Err() != nil {
		return LoginResult{}, ctx.Err()
	}
	if err != nil {
		return LoginResult{}, err
	}
	refreshWarning := ""
	if relogin && normalizedAuthMode(dto.AuthMode) == AuthModeToken && strings.TrimSpace(dto.RefreshToken) != "" {
		refreshWarning = migrationRefreshWarning
	}
	if result.Platform != original.Platform || result.Session.Platform != original.Platform || result.Session.BaseURL != normalizedURL {
		return LoginResult{}, identityCheckError(original.Platform, "新登录返回的站点地址与填写的新地址不同，或平台与原站点不同，无法确认这次域名修改。请核查新地址和平台后重试，或新增站点。"+refreshWarning+"原资料和费用未保存，不会修改主站。")
	}
	if result.MetricsError != nil {
		return LoginResult{}, withFailureStage(result.MetricsError, "metrics", original.Platform)
	}
	newID, newEmail, readErr := s.platformService.readLoginPrincipal(ctx, result.Session)
	if ctx.Err() != nil {
		return LoginResult{}, ctx.Err()
	}
	if readErr != nil {
		if !relogin {
			readErr = migrationReuseFailure(readErr)
		}
		return LoginResult{}, readErr
	}
	if newID == "" {
		return LoginResult{}, identityCheckError(original.Platform, "新地址没有返回合法的用户编号，无法确认是原账号。请在上游确认账号状态，并填写当前登录方式的新凭据后重试，或新增站点。"+refreshWarning+"原资料和费用未保存，不会修改主站。")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && providedUserID != "" && providedUserID != newID {
		return LoginResult{}, identityCheckError(original.Platform, "Key 实际属于用户编号 "+newID+"，与填写的用户 ID "+providedUserID+" 不同。请改正用户 ID，或留空让系统自动读取；原资料和费用未保存，不会修改主站。")
	}
	if oldEmail != "" {
		if !strings.EqualFold(oldEmail, strings.TrimSpace(newEmail)) {
			cause := "新地址返回的邮箱与原站点保存的登录邮箱不同"
			if strings.TrimSpace(newEmail) == "" {
				cause = "上游没有返回新地址上账号的邮箱"
			}
			return LoginResult{}, identityCheckError(original.Platform, "使用原密码登录邮箱核对："+cause+"，无法确认是原账号。请核查原账号与新地址后重试，或新增站点。"+refreshWarning+"原资料和费用未保存，不会修改主站。")
		}
	} else if oldID != newID {
		if typedStoredID {
			return LoginResult{}, identityCheckError(original.Platform, "新地址上的登录资料属于另一个账号：上游返回的用户编号是 "+newID+"，原站点保存的用户编号是 "+oldID+"。这个编号是旧版本保存的，可能是当初手填的，没有经过上游核实。请使用原账号的凭据并核查新地址后重试；如果确认当初填错了编号，请新增站点。"+refreshWarning+"原资料和费用未保存，不会修改主站。")
		}
		return LoginResult{}, identityCheckError(original.Platform, "新地址上的登录资料属于另一个账号：上游用户编号与原账号不同。请使用原账号的凭据并核查新地址后重试，或新增站点。"+refreshWarning+"原资料和费用未保存，不会修改主站。")
	}
	result.Session.UserID, result.Session.UserIDConfirmed = newID, true
	if !relogin {
		result.Metrics, err = s.platformService.FetchMetricsForDate(result.Session, s.keyUsageDate())
		if ctx.Err() != nil {
			return LoginResult{}, ctx.Err()
		}
		if err != nil {
			return LoginResult{}, migrationReuseFailure(withFailureStage(err, "metrics", original.Platform))
		}
	}
	return result, nil
}

func migrationReuseFailure(err error) error {
	var detail *RequestError
	if errors.As(err, &detail) {
		next := *detail
		next.UpstreamMessage = identityReadCause(err) + "；新地址未能使用原登录资料完成核对和读取，请填写当前登录方式的新凭据后重试。原资料和费用未保存，不会修改主站。"
		return &next
	}
	return err
}

func migrationRefreshFailure(err error) error {
	var detail *RequestError
	if errors.As(err, &detail) {
		next := *detail
		if !strings.Contains(next.UpstreamMessage, migrationRefreshWarning) {
			next.UpstreamMessage += migrationRefreshWarning
		}
		return &next
	}
	return err
}
