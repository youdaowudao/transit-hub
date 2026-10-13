package my_sites

import (
	"fmt"
	"net/http"
	"strings"
)

type ScopedGroupRef struct {
	SiteID    string `json:"siteId"`
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
}
type ScopedConnectionScope struct {
	WorkspaceAdminAccountID string           `json:"workspaceAdminAccountId"`
	SiteID                  string           `json:"siteId,omitempty"`
	GroupRefs               []ScopedGroupRef `json:"groupRefs,omitempty"`
}
type ScopedListRequest struct {
	WorkspaceAdminAccountID string           `json:"workspaceAdminAccountId"`
	GroupRefs               []ScopedGroupRef `json:"groupRefs"`
}
type ScopedListResponse struct {
	WorkspaceAdminAccountID string           `json:"workspaceAdminAccountId"`
	Items                   []RealConnection `json:"items"`
}
type scopedRequestError struct {
	Status  int
	Message string
	Detail  string
	Next    string
	Cause   error
}

func (e *scopedRequestError) Error() string { return e.Message }
func (e *scopedRequestError) Unwrap() error { return e.Cause }
func scopedInvalid(detail string) error {
	return &scopedRequestError{Status: http.StatusBadRequest, Message: "无法读取这批账号对接资料：检查范围不正确", Detail: detail, Next: "请刷新页面后重新选择网站或当前页"}
}
func scopedNotFound() error {
	return &scopedRequestError{Status: http.StatusNotFound, Message: "这批网站或分组已无法在当前工作区找到", Detail: "本次未读取或更新账号状态，也未修改主站账号", Next: "请刷新页面，检查网站和分组是否仍存在"}
}
func normalizeScopedRefs(refs []ScopedGroupRef) ([]ScopedGroupRef, error) {
	if len(refs) > 30 {
		return nil, scopedInvalid("一批最多选择30个分组，请按当前页检查")
	}
	result := make([]ScopedGroupRef, 0, len(refs))
	seen := make(map[ScopedGroupRef]bool)
	for _, ref := range refs {
		ref.SiteID = strings.TrimSpace(ref.SiteID)
		ref.GroupID = strings.TrimSpace(ref.GroupID)
		ref.GroupName = strings.TrimSpace(ref.GroupName)
		if ref.SiteID == "" || (ref.GroupID == "" && ref.GroupName == "") || len(ref.SiteID) > 128 || len(ref.GroupID) > 128 || len(ref.GroupName) > 512 {
			return nil, scopedInvalid("网站和分组ID最多128字节，名称最多512字节，网站及分组不能为空")
		}
		if !seen[ref] {
			seen[ref] = true
			result = append(result, ref)
		}
	}
	return result, nil
}
func scopedWorkspaceMismatch() error {
	return &scopedRequestError{Status: http.StatusConflict, Message: "工作区已变化，本批账号状态未检查", Detail: "请求中的工作区与当前工作区不同，本次未修改主站或本地账号状态", Next: "请刷新页面后重试"}
}
func scopedReadFailure(step string, cause error) error {
	return &scopedRequestError{Status: http.StatusInternalServerError, Message: fmt.Sprintf("%s暂时无法读取", step), Detail: "Hub读取已保存资料失败，具体数据库原因无法安全确认；本次未修改主站账号或本地状态", Next: "请刷新重试；仍失败时请查看Hub后端读取记录", Cause: cause}
}

type ScopedCheckRequest struct {
	WorkspaceAdminAccountID string           `json:"workspaceAdminAccountId"`
	Mode                    string           `json:"mode"`
	SiteID                  string           `json:"siteId,omitempty"`
	GroupRefs               []ScopedGroupRef `json:"groupRefs,omitempty"`
}
type ScopedCheckIssue struct {
	ConnectionID string `json:"connectionId,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	Step         string `json:"step"`
	Detail       string `json:"detail"`
	Next         string `json:"next"`
	RetryAllowed bool   `json:"retryAllowed"`
	HTTPStatus   int    `json:"httpStatus,omitempty"`
}
type ScopedCheckResult struct {
	WorkspaceAdminAccountID  string             `json:"workspaceAdminAccountId"`
	Total                    int                `json:"total"`
	ConfirmedConnectionIDs   []string           `json:"confirmedConnectionIds"`
	UnconfirmedConnectionIDs []string           `json:"unconfirmedConnectionIds"`
	SkippedConnectionIDs     []string           `json:"skippedConnectionIds"`
	ChangedConnectionIDs     []string           `json:"changedConnectionIds"`
	ConfirmedStatuses        map[string]string  `json:"confirmedStatuses"`
	Issues                   []ScopedCheckIssue `json:"issues"`
	StorageUncertain         bool               `json:"storageUncertain"`
}

// ScopedConnectionSnapshot is an ephemeral PostgreSQL identity/version snapshot.
// It is never serialized or persisted as a new field.
type ScopedConnectionSnapshot struct {
	ID                      string
	UserID                  string
	WorkspaceAdminAccountID string
	UpstreamSiteID          string
	UpstreamGroupID         string
	UpstreamGroupName       string
	AdminAccountID          string
	AdminPlatform           string
	Status                  string
	Xmin                    string
}
type ScopedStatusApplyResult struct {
	ConfirmedConnectionIDs []string
	ChangedConnectionIDs   []string
}
type ScopedStorageError struct {
	Uncertain bool
	Cause     error
}

func (e *ScopedStorageError) Error() string { return "Hub保存本地账号状态未能确认" }
func (e *ScopedStorageError) Unwrap() error { return e.Cause }
