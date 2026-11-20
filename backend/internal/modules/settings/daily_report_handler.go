package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"transithub/backend/internal/shared/httpjson"
	"transithub/backend/internal/shared/secretbox"
	"transithub/backend/internal/shared/workspacescope"
)

const strategyReportBodyLimit = 64 << 10

type strategyReportRequest struct {
	StrategySettings
	workspacescope.Scope
	legacyPatch map[string]json.RawMessage
}

func reportPatch(settings StrategySettings) DailyReportPatch {
	return DailyReportPatch{Enabled: settings.DailyReportEnabled, Time: settings.DailyReportTime, BotIDs: settings.DailyReportBotIDs}
}

// This scans top-level JSON keys only; matching text inside an old template is not a patch.
func prefixHasDailyReportField(prefix []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		if strings.EqualFold(name, "dailyReportEnabled") || strings.EqualFold(name, "dailyReportTime") || strings.EqualFold(name, "dailyReportBotIds") {
			return true
		}
		var skipped json.RawMessage
		if decoder.Decode(&skipped) != nil {
			return false
		}
	}
	return false
}

// Pure legacy requests retain Decode's original size, null and field behavior.
// A real report field hidden after a large legacy value is rejected before any save.
func decodeStrategyRequest(w http.ResponseWriter, r *http.Request) (strategyReportRequest, bool, error) {
	var dto strategyReportRequest
	original := r.Body
	prefix, err := io.ReadAll(io.LimitReader(original, strategyReportBodyLimit+1))
	if err != nil {
		return dto, false, err
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.MultiReader(bytes.NewReader(prefix), original), Closer: original}
	if prefixHasDailyReportField(prefix) {
		fields, err := httpjson.DecodeStrictObject(w, r, &dto)
		if err != nil {
			return dto, true, err
		}
		encoded, err := json.Marshal(dto.StrategySettings)
		if err != nil {
			return dto, true, err
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &values); err != nil {
			return dto, true, err
		}
		dto.legacyPatch = map[string]json.RawMessage{}
		for _, key := range legacyStrategyKeys {
			for submitted := range fields {
				if strings.EqualFold(submitted, key) {
					dto.legacyPatch[key] = values[key]
					break
				}
			}
		}
		return dto, true, nil
	}
	// Do not admit scope fields or other new parameters into the original contract.
	err = httpjson.Decode(r, &dto.StrategySettings)
	if reportPatch(dto.StrategySettings).Present() {
		return dto, true, httpjson.ErrStrictSize
	}
	return dto, false, err
}

func dailyReportSaveReason(err error) string {
	var config *DailyReportConfigError
	if errors.As(err, &config) {
		return config.Error()
	}
	var target *TelegramTargetError
	if errors.As(err, &target) {
		return target.Error()
	}
	for _, known := range []error{ErrDailyReportUnavailable, ErrDailyReportWritesDisabled, ErrTelegramProxyInvalid, ErrTelegramChannelFormat, secretbox.ErrNoKey, secretbox.ErrUndecryptable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		switch database.Code {
		case "42P01", "42703":
			return "所需本地资料表或字段尚未初始化；请先用正常服务完成升级。"
		case "42501":
			return "数据库账号没有保存所需的权限；请核查策略与通知资料表权限后重试。"
		case "40001", "40P01":
			return "并发更新导致本次数据库事务回退；请重新读取已保存策略后重试。"
		}
	}
	return "本地策略处理未完成，具体原因尚未确认；请检查本站服务日志与数据库连接。"
}

func writeDailyReportSaveError(w http.ResponseWriter, scope workspacescope.Scope, err error, bad bool) {
	status := http.StatusInternalServerError
	reason := dailyReportSaveReason(err)
	if bad {
		status = http.StatusBadRequest
		reason = err.Error()
	}
	message := "保存经营日报设置失败：" + reason + "原设置仍保留；请核查后重试。"
	impact := "本次没有提交保存或调用Telegram，没有修改主站。"
	retryable := true
	var commit *workspacescope.CommitError
	switch {
	case errors.As(err, &commit):
		message = "保存经营日报设置的结果尚未确认：" + dailyReportSaveReason(commit.Cause) + "请先重新读取，确认当前设置后再操作，避免重复修改。"
		impact = "本地保存可能已经完成；本次没有调用Telegram或修改主站。"
		retryable = false
	case errors.Is(err, workspacescope.ErrChanged):
		status = http.StatusConflict
		message = workspacescope.ErrChanged.Error()
	case errors.Is(err, ErrDailyReportWritesDisabled):
		status = http.StatusConflict
		message = ErrDailyReportWritesDisabled.Error()
	case errors.Is(err, ErrDailyReportUnavailable):
		status = http.StatusServiceUnavailable
	case errors.Is(err, ErrDailyReportInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrTelegramTargetUnavailable), errors.Is(err, ErrTelegramProxyInvalid):
		status = http.StatusConflict
	}
	httpjson.Write(w, status, map[string]any{
		"message": message, "step": "保存经营日报设置", "object": scope.AdminAccountID, "impact": impact,
		"action": "先核查并重新读取已保存策略，修正提示内容后再操作。", "retryable": retryable,
		"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
	})
}

func (h *Handler) saveDailyReportStrategy(w http.ResponseWriter, r *http.Request, userID string, dto strategyReportRequest) {
	dto.Scope.UserID = userID
	_, err := time.Parse(time.RFC3339Nano, dto.Scope.CreatedAt)
	if dto.Scope.AdminAccountID == "" || utf8.RuneCountInString(dto.Scope.AdminAccountID) > 128 || len(dto.Scope.CreatedAt) > 64 || err != nil {
		writeDailyReportSaveError(w, dto.Scope, errors.New("工作区编号或创建时间缺失、格式不正确；请重新读取当前工作区后保存。"), true)
		return
	}
	saved, err := h.service.SaveStrategyWithDailyReport(r.Context(), dto.Scope, dto.legacyPatch, reportPatch(dto.StrategySettings))
	if err != nil {
		writeDailyReportSaveError(w, dto.Scope, err, false)
		return
	}
	// The transaction already checked the original scope. Do not Resolve again after saving.
	httpjson.Write(w, http.StatusOK, struct {
		StrategySettings
		workspacescope.Scope
	}{StrategySettings: saved, Scope: dto.Scope})
}
