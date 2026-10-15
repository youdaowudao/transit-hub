package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

// ReadSiteSnapshot reads one workspace-scoped cache entry and its existing cost
// records. It never refreshes login, synchronizes, enqueues or writes business data.
func (s *Service) ReadSiteSnapshot(ctx context.Context, userID, siteID string) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()
	aid, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return Response{}, err
	}
	site, err := s.cache.Get(ctx, siteID)
	if err != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return Response{}, siteSnapshotReadError(err)
	}
	if site == nil || site.UserID != userID || site.AdminAccountID != aid {
		return Response{}, &RequestError{MessageKey: ErrorNotFound, Stage: "metrics", StatusCode: http.StatusNotFound, UpstreamMessage: "当前工作区找不到这个站点，本次未读取或修改资料；请返回站点列表核查，不会修改主站"}
	}
	// The detached response cannot mutate slices shared by in-memory cache fakes.
	copy := cloneSite(site)
	response := toResponseForDate(&copy, s.keyUsageDate())
	snapshots, err := s.groupCostSnapshotsForSite(ctx, &copy)
	if ctx.Err() != nil {
		return Response{}, siteSnapshotReadError(ctx.Err())
	}
	if err != nil {
		return Response{}, siteSnapshotReadError(err)
	}
	mergeGroupCostSnapshots(&copy, &response, snapshots)
	return response, nil
}

func siteSnapshotReadError(err error) *RequestError {
	cause := safePersistenceCause(err)
	if errors.Is(err, context.DeadlineExceeded) {
		cause = "读取超时"
	} else if errors.Is(err, context.Canceled) {
		cause = "读取已取消"
	}
	return &RequestError{MessageKey: ErrorRequest, Stage: "metrics", StatusCode: http.StatusServiceUnavailable, Cause: err, UpstreamMessage: "读取 Hub 保存的本站资料失败：" + cause + "；本次没有取得新结果，请维护人员检查 Hub 缓存连接和读取日志后重试；不会修改主站"}
}

func groupCostCacheReason(step string, err error, retained bool) string {
	result := "暂无可用金额"
	if retained {
		result = "保留已确认的旧金额"
	}
	return "读取本站" + step + "失败：" + safePersistenceCause(err) + "；" + result + "，请维护人员检查 Hub 缓存连接和读取日志后重试；不会修改主站。"
}

func (s *Service) groupCostStateReason(site *Site, state GroupCostSamplingState, progress groupCostWorkProgress, observedAt *time.Time) string {
	var parts []string
	now := s.keyUsageNow()
	if state.LastAttemptStatus == "failed" {
		reason := strings.TrimSpace(state.LastReason)
		switch reason {
		case "", "request_failed":
			reason = "读取本站分组费用失败，具体原因尚未确认，请维护人员检查 Hub 读取日志"
		case "invalid_response":
			reason = "上次分组费用校验未通过，旧记录没有保留具体校验分支，请同步这个站点并检查读取日志"
		case "auth_401", "auth_403", "auth":
			reason = "上次分组费用读取被上游拒绝，具体原因未保留，请检查本站登录资料和上游提示"
		case "network":
			reason = "上次分组费用读取未能完成连接，请检查 Hub 到上游的网络后重试"
		default:
			reason = safeCostReason(reason)
		}
		if observedAt != nil {
			reason = "沿用截至 " + singaporeCostTime(*observedAt) + " 的费用；" + reason
		}
		if !state.SummaryNextAllowedAt.IsZero() {
			reason += " " + singaporeCostTime(state.SummaryNextAllowedAt) + "后允许重试；"
		}
		s.mu.Lock()
		auto := s.refreshConfigs[refreshWorkspaceKey{userID: site.UserID, adminAccountID: site.AdminAccountID}].Enabled && !s.backgroundTasksDisabled
		s.mu.Unlock()
		if auto {
			reason += "系统下次同步时会再读取；不会修改主站。"
		} else {
			reason += "请关闭弹窗后点击本站同步按钮重试；不会修改主站。"
		}
		parts = append(parts, reason)
	}
	if progress.status != "" {
		if observedAt != nil {
			action := "等待读取"
			if progress.status == "running" {
				action = "读取"
			}
			parts = append(parts, fmt.Sprintf("当前显示截至 %s 的费用，系统正在%s更新记录；不会修改主站。", singaporeCostTime(*observedAt), action))
		} else if progress.status == "running" {
			parts = append(parts, "正在读取这个站点今天的分组费用；不会修改主站。")
		} else {
			parts = append(parts, "分组费用正在等待读取，无需重复点击；不会修改主站。")
		}
	}
	if len(parts) != 0 {
		return strings.Join(parts, " ")
	}
	if observedAt != nil {
		return ""
	}
	if !state.LastSuccessAt.IsZero() && businesstime.DateAt(state.LastSuccessAt) == businesstime.DateAt(now) && now.Sub(state.LastSuccessAt) > groupCostSampleTTL {
		return "暂时没有可用的今日分组费用。上次成功读取于 " + singaporeCostTime(state.LastSuccessAt) + "，记录已超过九十分钟；请关闭弹窗后同步这个站点重试，不会修改主站。"
	}
	reason := "今天尚未取得可用的分组费用。"
	if !state.LastSuccessAt.IsZero() {
		reason += "上次成功读取于 " + singaporeCostTime(state.LastSuccessAt) + "；"
	}
	return reason + "请关闭弹窗后同步这个站点；不会修改主站。"
}

func singaporeCostTime(at time.Time) string {
	return at.In(businesstime.Location()).Format("2006-01-02 15:04:05")
}
