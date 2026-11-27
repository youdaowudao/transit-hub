package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
	"transithub/backend/internal/shared/workspacescope"
)

type MainAccountCheckConfig struct {
	Enabled bool     `json:"enabled"`
	Times   []string `json:"times"`
}

type MainAccountCheckWorkspace struct {
	Scope       workspacescope.Scope
	Config      MainAccountCheckConfig
	ConfigError string
}

type MainAccountCheckRecord struct {
	ScheduledAt      time.Time  `json:"scheduledAt"`
	StartedAt        time.Time  `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
	Status           string     `json:"status"`
	ConfirmedActive  int        `json:"confirmedActive"`
	ConfirmedMissing int        `json:"confirmedMissing"`
	Unconfirmed      int        `json:"unconfirmed"`
	Skipped          int        `json:"skipped"`
	Reason           string     `json:"reason,omitempty"`
	Next             string     `json:"next,omitempty"`
}

var errMainAccountCheckConfig = errors.New("定时核对设置需要明确的开关和四个不重复、按时间从早到晚排列的 HH:mm 时间；请修正后保存。")
var errMainAccountCheckPlatform = errors.New("只有 Sub2API 主站支持账号核对；本次未修改设置或主站。")

func defaultMainAccountCheckConfig() MainAccountCheckConfig {
	return MainAccountCheckConfig{Enabled: true, Times: []string{"09:00", "13:00", "18:00", "22:00"}}
}

func validMainAccountCheckConfig(c MainAccountCheckConfig) bool {
	if len(c.Times) != 4 {
		return false
	}
	for i, v := range c.Times {
		parsed, err := time.Parse("15:04", v)
		if err != nil || parsed.Format("15:04") != v || (i > 0 && c.Times[i-1] >= v) {
			return false
		}
	}
	return true
}

func decodeMainAccountCheckConfig(raw []byte) (MainAccountCheckConfig, string) {
	fallback := defaultMainAccountCheckConfig()
	if len(raw) == 0 {
		return fallback, ""
	}
	var input struct {
		Enabled *bool    `json:"enabled"`
		Times   []string `json:"times"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Enabled == nil || !validMainAccountCheckConfig(MainAccountCheckConfig{Times: input.Times}) {
		fallback.Enabled = false
		return fallback, errMainAccountCheckConfig.Error() + " 原配置无效，定时核对已暂停；下方仅显示待修复的初始值。"
	}
	return MainAccountCheckConfig{Enabled: *input.Enabled, Times: input.Times}, ""
}

func validMainCheckScope(scope workspacescope.Scope) bool {
	stamp, err := time.Parse(time.RFC3339Nano, scope.CreatedAt)
	return scope.AdminAccountID != "" && len(scope.AdminAccountID) <= 128 && err == nil && stamp.Format(time.RFC3339Nano) == scope.CreatedAt
}

func (s *Service) ReadMainAccountCheckConfig(ctx context.Context, scope workspacescope.Scope, current bool) (MainAccountCheckWorkspace, error) {
	if !validMainCheckScope(scope) {
		return MainAccountCheckWorkspace{}, workspacescope.ErrChanged
	}
	if current {
		if err := workspacescope.Resolve(ctx, s.repository.db, scope); err != nil {
			return MainAccountCheckWorkspace{}, err
		}
	}
	var created time.Time
	var platform string
	var raw []byte
	err := s.repository.db.QueryRow(ctx, `SELECT a.created_at,a.platform,s.settings->'mainAccountCheck' FROM admin_accounts a LEFT JOIN strategy_settings s ON s.user_id=a.user_id AND s.admin_account_id=a.id WHERE a.user_id=$1 AND a.id=$2`, scope.UserID, scope.AdminAccountID).Scan(&created, &platform, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return MainAccountCheckWorkspace{}, workspacescope.ErrChanged
	}
	if err != nil {
		return MainAccountCheckWorkspace{}, err
	}
	if created.Format(time.RFC3339Nano) != scope.CreatedAt {
		return MainAccountCheckWorkspace{}, workspacescope.ErrChanged
	}
	if platform != "sub2api" {
		return MainAccountCheckWorkspace{}, errMainAccountCheckPlatform
	}
	config, configError := decodeMainAccountCheckConfig(raw)
	if current {
		if err := workspacescope.Resolve(ctx, s.repository.db, scope); err != nil {
			return MainAccountCheckWorkspace{}, err
		}
	}
	return MainAccountCheckWorkspace{Scope: scope, Config: config, ConfigError: configError}, nil
}

func (s *Service) ListMainAccountCheckWorkspaces(ctx context.Context) ([]MainAccountCheckWorkspace, error) {
	rows, err := s.repository.db.Query(ctx, `SELECT a.user_id,a.id,a.created_at,s.settings->'mainAccountCheck' FROM admin_accounts a LEFT JOIN strategy_settings s ON s.user_id=a.user_id AND s.admin_account_id=a.id WHERE a.platform='sub2api' AND EXISTS(SELECT 1 FROM real_connections c WHERE c.user_id=a.user_id AND c.workspace_admin_account_id=a.id AND c.admin_platform IN ('','sub2api') AND c.status IN ('','active','missing') AND btrim(c.admin_account_id)<>'') ORDER BY a.user_id,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MainAccountCheckWorkspace
	for rows.Next() {
		var item MainAccountCheckWorkspace
		var created time.Time
		var raw []byte
		if err = rows.Scan(&item.Scope.UserID, &item.Scope.AdminAccountID, &created, &raw); err != nil {
			return nil, err
		}
		item.Scope.CreatedAt = created.Format(time.RFC3339Nano)
		item.Config, item.ConfigError = decodeMainAccountCheckConfig(raw)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) SaveMainAccountCheckConfig(ctx context.Context, scope workspacescope.Scope, config MainAccountCheckConfig) error {
	if !validMainAccountCheckConfig(config) {
		return errMainAccountCheckConfig
	}
	if !validMainCheckScope(scope) {
		return workspacescope.ErrChanged
	}
	return workspacescope.Transaction(ctx, s.repository.db, scope, true, func(tx pgx.Tx) error {
		var platform string
		if err := tx.QueryRow(ctx, `SELECT platform FROM admin_accounts WHERE user_id=$1 AND id=$2`, scope.UserID, scope.AdminAccountID).Scan(&platform); err != nil {
			return err
		}
		if platform != "sub2api" {
			return errMainAccountCheckPlatform
		}
		original, err := readStrategyForUpdate(ctx, tx, scope.UserID, scope.AdminAccountID)
		if err != nil {
			return err
		}
		var root map[string]json.RawMessage
		if err = json.Unmarshal(original, &root); err != nil {
			return err
		}
		if root == nil {
			root = map[string]json.RawMessage{}
		}
		root["mainAccountCheck"], err = json.Marshal(config)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(root)
		if err != nil {
			return err
		}
		return writeStrategyJSON(ctx, tx, scope.UserID, scope.AdminAccountID, raw)
	})
}

type MainAccountCheckRecentReader interface {
	RecentMainAccountCheck(context.Context, workspacescope.Scope) (*MainAccountCheckRecord, error)
}

type mainAccountCheckResponse struct {
	workspacescope.Scope
	MainAccountCheckConfig
	ConfigError     string                  `json:"configError,omitempty"`
	RecentScheduled *MainAccountCheckRecord `json:"recentScheduled"`
	RecentReadError string                  `json:"recentReadError,omitempty"`
}

func writeMainAccountCheckError(w http.ResponseWriter, err error, saving bool) {
	status := http.StatusInternalServerError
	message := "读取主站账号核对设置失败：" + dailyReportSaveReason(err) + "本次未修改主站。请重新读取。"
	if saving {
		message = "保存主站账号核对设置失败：" + dailyReportSaveReason(err) + "本次未保存设置、未修改主站。请重新读取后重试。"
	}
	var commit *workspacescope.CommitError
	switch {
	case errors.Is(err, workspacescope.ErrChanged):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, errMainAccountCheckConfig), errors.Is(err, errMainAccountCheckPlatform):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.As(err, &commit):
		status = http.StatusServiceUnavailable
		message = "核对设置保存结果尚未确认；请先重新读取设置，不要重复保存。未修改主站。"
	}
	httpjson.Write(w, status, map[string]string{"message": message})
}

func RegisterMainAccountCheckRoutes(mux *http.ServeMux, s *Service, recent MainAccountCheckRecentReader) {
	mux.HandleFunc("GET /api/settings/main-account-check", func(w http.ResponseWriter, r *http.Request) {
		user, ok := authctx.UserID(r.Context())
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
			return
		}
		scope := workspacescope.Scope{UserID: user, AdminAccountID: r.URL.Query().Get("workspaceAdminAccountId"), CreatedAt: r.URL.Query().Get("workspaceCreatedAt")}
		item, err := s.ReadMainAccountCheckConfig(r.Context(), scope, true)
		if err != nil {
			writeMainAccountCheckError(w, err, false)
			return
		}
		out := mainAccountCheckResponse{Scope: scope, MainAccountCheckConfig: item.Config, ConfigError: item.ConfigError}
		if recent != nil {
			out.RecentScheduled, err = recent.RecentMainAccountCheck(r.Context(), scope)
		} else {
			err = errors.New("recent unavailable")
		}
		if err != nil {
			out.RecentReadError = "定时核对记录暂不可读，请检查 Hub 的 Redis 连接，必要时手动核对。"
		}
		if err = workspacescope.Resolve(r.Context(), s.repository.db, scope); err != nil {
			writeMainAccountCheckError(w, err, false)
			return
		}
		httpjson.Write(w, http.StatusOK, out)
	})
	mux.HandleFunc("PUT /api/settings/main-account-check", func(w http.ResponseWriter, r *http.Request) {
		user, ok := authctx.UserID(r.Context())
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
			return
		}
		var input struct {
			workspacescope.Scope
			Enabled *bool    `json:"enabled"`
			Times   []string `json:"times"`
		}
		if _, err := httpjson.DecodeStrictObject(w, r, &input); err != nil || input.Enabled == nil {
			writeMainAccountCheckError(w, errMainAccountCheckConfig, true)
			return
		}
		input.UserID = user
		config := MainAccountCheckConfig{Enabled: *input.Enabled, Times: input.Times}
		if err := s.SaveMainAccountCheckConfig(r.Context(), input.Scope, config); err != nil {
			writeMainAccountCheckError(w, err, true)
			return
		}
		httpjson.Write(w, http.StatusOK, mainAccountCheckResponse{Scope: input.Scope, MainAccountCheckConfig: config})
	})
}
