package my_sites

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

const needsAttentionPageSize = 30

type NeedsAttentionQuery struct {
	WorkspaceAdminAccountID string `json:"workspaceAdminAccountId"`
	SiteID                  string `json:"siteId,omitempty"`
	Page                    int    `json:"page"`
}

type NeedsAttentionItem struct {
	Connection   RealConnection `json:"connection"`
	Reason       string         `json:"reason"`
	Detail       string         `json:"detail"`
	LastSyncedAt *int64         `json:"lastSyncedAt"`
}

type NeedsAttentionResponse struct {
	WorkspaceAdminAccountID string               `json:"workspaceAdminAccountId"`
	Total                   int                  `json:"total"`
	Page                    int                  `json:"page"`
	PageSize                int                  `json:"pageSize"`
	TotalPages              int                  `json:"totalPages"`
	Items                   []NeedsAttentionItem `json:"items"`
}

type NeedsAttentionRepository interface {
	ListConnectionsNeedingAttention(context.Context, string, string, NeedsAttentionQuery) (NeedsAttentionResponse, error)
}

func attentionInvalid(detail string) error {
	return &scopedRequestError{Status: http.StatusBadRequest, Message: "已有对接暂时无法读取：查询范围不正确", Detail: detail, Next: "请在当前工作区重新打开已有对接；本次未修改主站或本地记录"}
}

func attentionReadFailure(cause error) error {
	return &scopedRequestError{Status: http.StatusInternalServerError, Message: "已有对接暂时无法读取", Detail: "读取Hub保存的对接资料失败：" + safeLocalStorageCause(cause) + "。当前不能确认是否有记录需要处理，倍率仍可查看；本次未修改主站或本地记录", Next: "请重试读取，并检查Hub后端对接读取记录", Cause: cause}
}

func attentionSiteNotFound() error {
	return &scopedRequestError{Status: http.StatusNotFound, Message: "这个网站无法在当前工作区找到，已有对接未读取", Detail: "指定网站不属于当前用户和工作区，或网站已移除；本次没有扩大到其他网站查询", Next: "请清除网站筛选后查看当前工作区已有对接；本次未修改主站或本地记录"}
}

func normalizeAttentionQuery(query NeedsAttentionQuery) (NeedsAttentionQuery, error) {
	query.WorkspaceAdminAccountID = strings.TrimSpace(query.WorkspaceAdminAccountID)
	query.SiteID = strings.TrimSpace(query.SiteID)
	if query.WorkspaceAdminAccountID == "" || len(query.WorkspaceAdminAccountID) > 128 || len(query.SiteID) > 128 {
		return query, attentionInvalid("必须提供工作区，工作区和网站ID最多128字节")
	}
	if query.Page < 1 || query.Page > 2147483647 {
		return query, attentionInvalid("页码必须是1到2147483647之间的整数")
	}
	return query, nil
}

func (s *Service) ListConnectionsNeedingAttention(ctx context.Context, userID string, query NeedsAttentionQuery) (NeedsAttentionResponse, error) {
	query, err := normalizeAttentionQuery(query)
	if err != nil {
		return NeedsAttentionResponse{}, err
	}
	workspace, err := s.scopedWorkspace(ctx, userID, query.WorkspaceAdminAccountID)
	if err != nil {
		return NeedsAttentionResponse{}, err
	}
	repository, ok := s.connRepository.(NeedsAttentionRepository)
	if !ok {
		return NeedsAttentionResponse{}, attentionReadFailure(nil)
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := repository.ListConnectionsNeedingAttention(readCtx, userID, workspace, query)
	if err != nil {
		var known *scopedRequestError
		if errors.As(err, &known) {
			return NeedsAttentionResponse{}, err
		}
		log.Printf("real connection attention read failed reason=%s", safeLocalStorageCause(err))
		return NeedsAttentionResponse{}, attentionReadFailure(err)
	}
	if result.Items == nil {
		result.Items = []NeedsAttentionItem{}
	}
	for index := range result.Items {
		conn := &result.Items[index].Connection
		conn.ConnectionName = strings.TrimSpace(conn.AdminAccountName)
		if len(conn.OwnGroupNames) > 0 {
			conn.OwnGroupName = strings.TrimSpace(conn.OwnGroupNames[0])
		}
		*conn = publicRealConnection(*conn)
	}
	return result, nil
}

func (h *Handler) listConnectionsNeedingAttention(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.Write(w, http.StatusUnauthorized, map[string]string{"message": "登录已失效，已有对接未读取", "detail": "Hub无法确认当前用户，本次未修改主站或本地记录", "next": "请登录后重新打开当前工作区"})
		return
	}
	params := r.URL.Query()
	page := 1
	if raw := params.Get("page"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed < 1 {
			writeScopedError(w, attentionInvalid("页码必须是1到2147483647之间的整数"))
			return
		}
		page = int(parsed)
	}
	result, err := h.service.ListConnectionsNeedingAttention(r.Context(), userID, NeedsAttentionQuery{WorkspaceAdminAccountID: params.Get("workspaceAdminAccountId"), SiteID: params.Get("siteId"), Page: page})
	if err != nil {
		writeScopedError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
