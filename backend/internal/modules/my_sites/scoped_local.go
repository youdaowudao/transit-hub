package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type ScopedLocalConnectionRepository interface {
	ListScopedRealConnections(context.Context, string, string, []ScopedGroupRef) ([]RealConnection, error)
}

func (s *Service) scopedWorkspace(ctx context.Context, userID, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || len(requested) > 128 {
		return "", scopedInvalid("必须提供当前工作区，工作区ID最多128字节")
	}
	current, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return "", &scopedRequestError{Status: http.StatusConflict, Message: "尚未选定当前工作区，本次未读取账号资料", Detail: "Hub无法确认当前工作区", Next: "请选择工作区后刷新", Cause: err}
	}
	if current != requested {
		return "", scopedWorkspaceMismatch()
	}
	return current, nil
}
func (s *Service) ListScopedRealConnections(ctx context.Context, userID string, req ScopedListRequest) (ScopedListResponse, error) {
	workspace, err := s.scopedWorkspace(ctx, userID, req.WorkspaceAdminAccountID)
	if err != nil {
		return ScopedListResponse{}, err
	}
	if req.GroupRefs == nil {
		return ScopedListResponse{}, scopedInvalid("必须提供当前页分组列表；空页请传空列表")
	}
	refs, err := normalizeScopedRefs(req.GroupRefs)
	if err != nil {
		return ScopedListResponse{}, err
	}
	repo, ok := s.connRepository.(ScopedLocalConnectionRepository)
	if !ok {
		return ScopedListResponse{}, scopedReadFailure("本页对接资料", nil)
	}
	items, err := repo.ListScopedRealConnections(ctx, userID, workspace, refs)
	if err != nil {
		var known *scopedRequestError
		if errors.As(err, &known) {
			return ScopedListResponse{}, err
		}
		return ScopedListResponse{}, scopedReadFailure("本页对接资料", err)
	}
	if items == nil {
		items = make([]RealConnection, 0)
	}
	for i := range items {
		items[i].ConnectionName = strings.TrimSpace(items[i].AdminAccountName)
		items[i].KeyName = safeCredentialPreview(items[i].UpstreamKey)
		if len(items[i].OwnGroupNames) > 0 {
			items[i].OwnGroupName = strings.TrimSpace(items[i].OwnGroupNames[0])
		}
		items[i] = publicRealConnection(items[i])
	}
	return ScopedListResponse{WorkspaceAdminAccountID: workspace, Items: items}, nil
}
func decodeScopedBody(w http.ResponseWriter, r *http.Request, dto any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dto); err != nil {
		return scopedInvalid("请求内容无法读取或超过32KiB，请刷新后按当前页检查")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return scopedInvalid("请求内容包含多份数据，请刷新后重试")
	}
	return nil
}
func writeScopedError(w http.ResponseWriter, err error) {
	var known *scopedRequestError
	if !errors.As(err, &known) {
		known = scopedReadFailure("本页对接资料", err).(*scopedRequestError)
	}
	httpjson.Write(w, known.Status, map[string]string{"message": known.Message, "detail": known.Detail, "next": known.Next})
}
func (h *Handler) listScopedRealConnections(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var req ScopedListRequest
	if err := decodeScopedBody(w, r, &req); err != nil {
		writeScopedError(w, err)
		return
	}
	result, err := h.service.ListScopedRealConnections(r.Context(), userID, req)
	if err != nil {
		writeScopedError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
