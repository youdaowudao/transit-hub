package group_rates

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type Handler struct {
	service  *Service
	accounts AdminAccountResolver
}

type AdminAccountResolver interface {
	RequireCurrentID(ctx context.Context, userID string) (string, error)
}

func RegisterRoutes(mux *http.ServeMux, service *Service, accounts AdminAccountResolver) {
	handler := &Handler{service: service, accounts: accounts}
	mux.HandleFunc("GET /api/group-rates", handler.list)
	mux.HandleFunc("GET /api/group-rates/history", handler.history)
	mux.HandleFunc("PATCH /api/group-rates/type", handler.updateType)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	query := r.URL.Query()
	adminAccountID, err := h.currentAdminAccountID(r.Context(), userID)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	workspace := strings.TrimSpace(query.Get("workspaceAdminAccountId"))
	siteID := strings.TrimSpace(query.Get("siteId"))
	focusID := strings.TrimSpace(query.Get("focusGroupId"))
	focusName := strings.TrimSpace(query.Get("focusGroup"))
	if len(workspace) > 128 || len(siteID) > 128 || len(focusID) > 128 || len(focusName) > 512 || (siteID == "" && (focusID != "" || focusName != "")) {
		httpjson.Write(w, http.StatusBadRequest, map[string]string{"message": "无法读取分组倍率：网站或分组定位参数不正确", "detail": "网站和分组ID最多128字节，分组名称最多512字节；定位分组必须指定网站", "next": "请从上游管理重新打开对应分组"})
		return
	}
	if workspace != "" && workspace != adminAccountID {
		httpjson.Write(w, http.StatusConflict, map[string]string{"message": "工作区已变化，本次分组倍率未读取", "detail": "请求中的工作区与当前工作区不同", "next": "请刷新页面后重试"})
		return
	}
	rows, err := h.service.List(r.Context(), userID, adminAccountID, ListQuery{
		WorkspaceAdminAccountID: adminAccountID,
		SiteID:                  siteID,
		FocusGroupID:            focusID,
		FocusGroup:              focusName,
		Page:                    intQuery(query.Get("page"), 1),
		PageSize:                30,
		Search:                  query.Get("search"),
		Type:                    query.Get("type"),
		Platform:                query.Get("platform"),
		Status:                  query.Get("status"),
		Sort:                    query.Get("sort"),
	})
	if err != nil {
		log.Printf("list group rates: %v", err)
		httpjson.Write(w, http.StatusInternalServerError, map[string]string{"message": "分组倍率暂时无法读取", "detail": "Hub读取已保存的倍率资料失败，具体数据库原因无法安全确认；本次未更新倍率或账号", "next": "请刷新重试；仍失败时请检查Hub后端读取记录"})
		return
	}
	httpjson.Write(w, http.StatusOK, rows)
}

func (h *Handler) updateType(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var dto UpdateTypeRequest
	if err := httpjson.Decode(r, &dto); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	adminAccountID, err := h.currentAdminAccountID(r.Context(), userID)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	if err := h.service.UpdateType(r.Context(), userID, adminAccountID, GroupRef{SiteID: dto.SiteID, GroupName: dto.GroupName}, dto.Type); err != nil {
		if errors.Is(err, ErrNoEditableSnapshot) {
			httpjson.Write(w, http.StatusConflict, map[string]string{"message": err.Error(), "detail": "当前网站中找不到可唯一编辑的有效分组倍率，分类未保存", "next": "请到上游管理同步网站后重新选择分组"})
			return
		}
		log.Printf("update group rate type: %v", err)
		httpjson.Write(w, http.StatusInternalServerError, map[string]string{"message": "保存分组分类失败", "detail": "数据库未完成分类保存，具体原因无法安全确认", "next": "请维护人员检查Hub分类保存记录和数据库连接，确认恢复后重试"})
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"success": true})
}

func intQuery(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	query := r.URL.Query()
	siteID := strings.TrimSpace(query.Get("siteId"))
	groupName := strings.TrimSpace(query.Get("groupName"))
	platform := strings.TrimSpace(query.Get("platform"))
	if siteID == "" || groupName == "" || platform == "" {
		httpjson.WriteError(w, http.StatusBadRequest, "siteId, groupName, and platform are required")
		return
	}

	adminAccountID, err := h.currentAdminAccountID(r.Context(), userID)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	rows, err := h.service.History(r.Context(), userID, adminAccountID, siteID, groupName, platform)
	if err != nil {
		log.Printf("list group rate history: %v", err)
		httpjson.WriteError(w, http.StatusInternalServerError, "Failed to list group rate history")
		return
	}
	httpjson.Write(w, http.StatusOK, rows)
}

func (h *Handler) currentAdminAccountID(ctx context.Context, userID string) (string, error) {
	if h.accounts == nil {
		return "", requestError("admin.adminAccounts.errors.noCurrentAccount")
	}
	return h.accounts.RequireCurrentID(ctx, userID)
}

func writeWorkspaceError(w http.ResponseWriter, err error) {
	if err != nil && err.Error() == "admin.adminAccounts.errors.noCurrentAccount" {
		httpjson.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httpjson.WriteError(w, http.StatusInternalServerError, "Failed to resolve admin account")
}

type requestError string

func (e requestError) Error() string { return string(e) }
