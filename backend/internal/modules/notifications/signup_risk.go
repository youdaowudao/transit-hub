package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"transithub/backend/internal/shared/businesstime"
)

const (
	SignupRiskKind = "sub2api.signup_risk"
	// Keep these literals aligned with the read-only Sub2API handoff.
	SignupRiskMarker = "[注册风险:高]"
	SignupRiskNote   = "[注册风险:高] 含+别名"
)

type SignupRiskPreferences struct {
	WorkspaceAdminAccountID string     `json:"workspaceAdminAccountId"`
	Enabled                 bool       `json:"enabled"`
	Initialized             bool       `json:"initialized"`
	UpdatedAt               *time.Time `json:"updatedAt"`
}

// SignupRiskTarget identifies an existing administrator connection, never an
// upstream resale site or an implicit current workspace.
type SignupRiskTarget struct {
	UserID         string
	AdminAccountID string
	BaseURL        string
	SiteName       string
	Initialized    bool
	UpdatedAt      time.Time
}

type SignupRiskUser struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

type SignupRiskSummary struct {
	Enabled bool     `json:"enabled"`
	Items   []Notice `json:"items"`
}

func SignupRiskSiteKey(baseURL string) string {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err == nil {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		baseURL = parsed.String()
	}
	sum := sha256.Sum256([]byte(baseURL))
	return hex.EncodeToString(sum[:])
}

// Percent escaping is reversible, retains the complete email, and prevents
// valid email text such as token=abc+1 from tripping the shared secret guard.
func signupRiskEmail(email string) string {
	var out strings.Builder
	for _, r := range email {
		if strings.ContainsRune("%=:/<>\"'\\", r) || unicode.IsSpace(r) || unicode.IsControl(r) {
			for _, b := range []byte(string(r)) {
				fmt.Fprintf(&out, "%%%02X", b)
			}
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func signupRiskNotice(target SignupRiskTarget, user SignupRiskUser, discovered time.Time) (NoticeInput, error) {
	id, err := strconv.ParseInt(user.ID, 10, 64)
	invalid := ""
	switch {
	case err != nil || id <= 0 || strconv.FormatInt(id, 10) != user.ID:
		invalid = "用户编号不是完整的正整数"
	case strings.TrimSpace(user.Email) == "":
		invalid = "注册邮箱为空"
	case len(user.Email) > 255:
		invalid = fmt.Sprintf("注册邮箱超过255字节，实际为%d字节", len(user.Email))
	case user.CreatedAt.IsZero():
		invalid = "注册时间缺失"
	}
	if invalid != "" {
		return NoticeInput{}, fmt.Errorf("注册提醒所需的%s；本轮没有登记或生成消息，请核查 Sub2API 管理员用户列表后重试", invalid)
	}
	site := safeSamplingDisplayText(target.SiteName, "当前Sub2API站点", 48)
	if checkText("站点名称", target.SiteName, 48, true, false) != nil {
		site = "当前Sub2API站点"
	}
	address := target.BaseURL
	if parsed, err := url.Parse(address); err == nil {
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		address = parsed.String()
	}
	address = safeSamplingDisplayText(address, "请在当前管理员工作区查看站点地址", 1000)
	email := signupRiskEmail(user.Email)
	registered := user.CreatedAt.In(businesstime.Location()).Format("2006-01-02 15:04:05")
	details := []Detail{{Label: "站点", Value: site}, {Label: "站点地址", Value: address}, {Label: "注册邮箱", Value: email}, {Label: "用户 ID", Value: user.ID}, {Label: "注册时间", Value: registered}, {Label: "下一步", Value: "主站备注含注册风险标记；请到 Sub2API 用户管理检查。TransitHub 只记录提醒，不会修改主站或补发金额。"}}
	if email != user.Email {
		details = append(details, Detail{Label: "邮箱显示说明", Value: "邮箱特殊字符按百分号编码显示，例如 %3D 表示等号；请按原邮箱检查。"})
	}
	input := NoticeInput{
		UserID: target.UserID, AdminAccountID: target.AdminAccountID, Kind: SignupRiskKind,
		EventKey: SignupRiskSiteKey(target.BaseURL) + ":" + user.ID, SubjectType: "admin_account", SubjectID: target.AdminAccountID,
		Level: LevelImportant, DiscoveredAt: discovered,
		Content: Content{Title: "Sub2API 高风险注册提醒", Summary: fmt.Sprintf("站点：%s\n注册邮箱：%s\n用户 ID：%s\n注册时间：%s\n请到 Sub2API 用户管理检查；本提醒不会修改主站。", site, email, user.ID, registered), Details: details},
	}
	if _, err := validate(input); err != nil {
		return NoticeInput{}, err
	}
	return input, nil
}

func (s *Service) GetSignupRiskPreferences(ctx context.Context, user, workspace string) (SignupRiskPreferences, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.repository.beginRead(ctx, user, workspace)
	if err != nil {
		return SignupRiskPreferences{}, err
	}
	defer tx.Rollback(ctx)
	result, err := readSignupRiskPreferences(ctx, tx, user, workspace)
	if err != nil {
		return result, storageFailure(workspace, "读取高风险注册提醒设置", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, storageFailure(workspace, "完成高风险注册提醒设置读取", err)
	}
	return result, nil
}

func (s *Service) SaveSignupRiskPreferences(ctx context.Context, user, workspace string, enabled bool) (SignupRiskPreferences, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.repository.beginWrite(ctx, user, workspace)
	if err != nil {
		return SignupRiskPreferences{}, err
	}
	defer tx.Rollback(ctx)
	var platform string
	if err := tx.QueryRow(ctx, `SELECT platform FROM admin_accounts WHERE user_id=$1 AND id=$2`, user, workspace).Scan(&platform); err != nil {
		return SignupRiskPreferences{}, storageFailure(workspace, "核查注册提醒所属站点", err)
	}
	if platform != "sub2api" {
		return SignupRiskPreferences{}, invalidRequest(workspace, "这个工作区未连接 Sub2API，无法开启此提醒；请切换到 Sub2API 工作区。原设置没有改变")
	}
	result := SignupRiskPreferences{WorkspaceAdminAccountID: workspace}
	err = tx.QueryRow(ctx, `INSERT INTO signup_risk_settings(user_id,admin_account_id,enabled) VALUES($1,$2,$3)
ON CONFLICT(user_id,admin_account_id) DO UPDATE SET enabled=EXCLUDED.enabled,
initialized=CASE WHEN signup_risk_settings.enabled=EXCLUDED.enabled THEN signup_risk_settings.initialized ELSE false END,
updated_at=CASE WHEN signup_risk_settings.enabled=EXCLUDED.enabled THEN signup_risk_settings.updated_at ELSE GREATEST(clock_timestamp(),signup_risk_settings.updated_at+interval '1 microsecond') END
RETURNING enabled,initialized,updated_at`, user, workspace, enabled).Scan(&result.Enabled, &result.Initialized, &result.UpdatedAt)
	if err != nil {
		return SignupRiskPreferences{}, storageFailure(workspace, "保存高风险注册提醒设置", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SignupRiskPreferences{}, commitFailure(workspace, "高风险注册提醒设置", err)
	}
	return result, nil
}
