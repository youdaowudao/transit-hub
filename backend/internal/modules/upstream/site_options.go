package upstream

import (
	"context"
	"net/http"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type SiteOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type SiteOptionsResponse struct {
	WorkspaceAdminAccountID string       `json:"workspaceAdminAccountId"`
	Items                   []SiteOption `json:"items"`
}

// ListOptions reads only the existing site cache. It deliberately bypasses
// response enrichment, cost samples, synchronization and session maintenance.
func (s *Service) ListOptions(ctx context.Context, userID, workspaceID string) (SiteOptionsResponse, error) {
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return SiteOptionsResponse{}, err
	}
	result := SiteOptionsResponse{WorkspaceAdminAccountID: workspaceID, Items: make([]SiteOption, 0)}
	for _, site := range sites {
		if site != nil && site.UserID == userID && site.AdminAccountID == workspaceID {
			result.Items = append(result.Items, SiteOption{ID: site.ID, Name: site.Name})
		}
	}
	return result, nil
}
func (h *Handler) listOptions(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	workspace, err := h.requireWorkspace(r.Context(), userID)
	if err != nil {
		httpjson.Write(w, http.StatusConflict, map[string]string{"message": "网站选项未读取：尚未选定工作区", "detail": "当前工作区无法确认，本次未读取其他工作区资料", "next": "请选择工作区后刷新"})
		return
	}
	result, err := h.service.ListOptions(r.Context(), userID, workspace)
	if err != nil {
		httpjson.Write(w, http.StatusInternalServerError, map[string]string{"message": "网站选项暂时无法加载", "detail": "Hub读取已保存的网站名称失败，缓存连接的具体原因无法安全确认；本次未修改网站", "next": "请刷新重试；仍失败时请检查Hub后端缓存读取记录"})
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
