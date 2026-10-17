package my_sites

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

var ownGroupRatesTimeout = 15 * time.Second

type OwnGroupRate struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Multiplier *float64 `json:"multiplier"`
	Status     string   `json:"status"`
}
type OwnGroupRatesResponse struct {
	WorkspaceAdminAccountID string         `json:"workspaceAdminAccountId"`
	Groups                  []OwnGroupRate `json:"groups"`
}

// ReadOwnGroupRates only reads the stored session and a complete group inventory.
// Expired access tokens are deliberately not refreshed or persisted here.
func (s *Service) ReadOwnGroupRates(ctx context.Context, userID, requested string) (OwnGroupRatesResponse, error) {
	fail := func(status int, detail, next, step string) (OwnGroupRatesResponse, error) {
		if step != "" {
			log.Printf("[own-group-rates] 读取失败 workspace=%s step=%s httpStatus=%d", requested, step, status)
		}
		return OwnGroupRatesResponse{}, &scopedRequestError{Status: status, Message: "读取主站分组失败", Detail: detail, Next: next}
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || len(requested) > 128 {
		return fail(400, "请求没有带上当前工作区，或工作区编号超过 128 字节", "请刷新页面后重试", "")
	}
	current, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return fail(409, "Hub 无法确认当前工作区", "请选择工作区后点“刷新数据”", "")
	}
	if current != requested {
		return fail(409, "请求中的工作区与当前工作区不同，本次没有读取主站分组", "请重新进入本页", "")
	}
	session, available, err := s.StoredSession(ctx, userID, current)
	if err != nil {
		return fail(500, "Hub 未能读取已保存的主站登录资料，具体存储原因无法安全确认", "请点“刷新数据”重试；仍失败时查看 Hub 后端日志中以 [own-group-rates] 开头的记录", "load_session")
	}
	if !available {
		return fail(409, "Hub 没有这个工作区可用的主站登录资料", "请到“我的站点”重新连接主站，再点“刷新数据”", "session_unavailable")
	}
	if session.Platform != upstream.PlatformSub2API {
		return fail(409, "当前主站不是 Sub2API，本页只读取 Sub2API 主站的分组倍率", "本页其他内容不受影响", "platform")
	}
	if s.platformService == nil {
		return fail(500, "Hub 读取主站分组的功能没有准备好", "请重启 Hub 后端后再点“刷新数据”", "not_ready")
	}
	readCtx, cancel := context.WithTimeout(ctx, ownGroupRatesTimeout)
	defer cancel()
	groups, err := s.platformService.FetchAdminAllGroupsContext(readCtx, session)
	if err != nil {
		detail, next, status := ownGroupRatesCause(err, readCtx, ctx, session)
		return fail(status, detail, next, "fetch_groups")
	}
	result := OwnGroupRatesResponse{WorkspaceAdminAccountID: current, Groups: make([]OwnGroupRate, 0, len(groups))}
	for _, g := range groups {
		result.Groups = append(result.Groups, OwnGroupRate{ID: g.ID, Name: g.Name, Multiplier: g.Multiplier, Status: g.Status})
	}
	return result, nil
}

func ownGroupRatesCause(err error, readCtx, requestCtx context.Context, session upstream.Session) (string, string, int) {
	next := "请稍后点“刷新数据”重试；仍失败时到主站后台确认分组列表能否打开"
	if errors.Is(readCtx.Err(), context.DeadlineExceeded) && requestCtx.Err() == nil {
		return "读取主站分组超时，本次没有取得结果", next, 504
	}
	if requestCtx.Err() != nil {
		return "这次读取已取消", next, 502
	}
	detail := "主站没有完成这次读取，具体原因无法安全确认"
	status := 502
	var remote *upstream.RequestError
	if errors.As(err, &remote) {
		if remote.StatusCode == 401 || remote.MessageKey == upstream.ErrorAuth {
			next = "请到“我的站点”重新连接主站，再点“刷新数据”"
		} else {
			switch remote.Reason {
			case upstream.ErrorAnnouncementAckRequired, upstream.ErrorUpstreamInsufficientBalance, upstream.ErrorUpstreamKeyQuotaExhausted, upstream.ErrorUpstreamKeyExpired:
				next = "请按上面的原因到主站后台处理，再点“刷新数据”"
			}
		}
		hint := upstream.ImportDiagnosticHint(remote.UpstreamMessage)
		if hint != "" {
			detail = hint
		} else {
			switch remote.Reason {
			case upstream.ErrorAnnouncementAckRequired:
				detail = "主站要求先在网页上确认新公告，确认前无法读取"
			case upstream.ErrorUpstreamInsufficientBalance:
				detail = "主站返回余额不足，不允许读取"
			case upstream.ErrorUpstreamKeyQuotaExhausted:
				detail = "主站返回 Key 额度已用尽"
			case upstream.ErrorUpstreamKeyExpired:
				detail = "主站返回 Key 已过期"
			case upstream.ErrorNetworkTimeout:
				detail = "读取主站分组超时，本次没有取得结果"
				status = 504
			case upstream.ErrorNetworkUnreachable:
				detail = "Hub 暂时无法连接主站"
			case upstream.ErrorTLSFailed:
				detail = "主站安全连接未能建立"
			default:
				switch {
				case remote.StatusCode == 401 || remote.MessageKey == upstream.ErrorAuth:
					detail = "已保存的主站登录资料没有通过主站验证"
				case remote.Reason == upstream.ErrorForbidden:
					detail = "主站拒绝了这次读取，没有返回具体原因"
				case remote.Reason == upstream.ErrorNotFound:
					detail = "主站没有找到分组列表接口"
				case remote.Reason == upstream.ErrorRateLimited:
					detail = "主站提示请求过于频繁"
				case remote.Reason == upstream.ErrorUpstreamServer:
					detail = "主站服务出错，没有返回可显示的具体原因"
				case remote.MessageKey == upstream.ErrorInvalidResponse:
					detail = "主站返回的分组资料格式不正确或不完整"
				}
			}
		}
		if remote.StatusCode > 0 {
			detail += fmt.Sprintf("（主站返回 HTTP %d）", remote.StatusCode)
		}
	}
	for _, secret := range []string{session.AccessToken, session.RefreshToken, session.Cookie, session.AdminAPIKey} {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[已遮盖]")
		}
	}
	return detail, next, status
}
func (h *Handler) readOwnGroupRates(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	result, err := h.service.ReadOwnGroupRates(r.Context(), userID, r.URL.Query().Get("workspaceAdminAccountId"))
	if err != nil {
		writeScopedError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
