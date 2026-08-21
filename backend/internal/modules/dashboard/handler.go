package dashboard

import (
	"errors"
	"net/http"
	"strconv"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/httpjson"
)

// Handler 只负责 HTTP 收参、状态码与 JSON 返回，业务逻辑都在 Service / MetricsService。
type Handler struct {
	service        *Service
	metricsService *MetricsService
}

// RegisterRoutes 注册仪表盘 admin 账户相关路由和指标数据路由。
// 这些路径已纳入 httpserver 的 protectedPath，需先通过 TransitHub 用户鉴权。
func RegisterRoutes(mux *http.ServeMux, service *Service, metricsService *MetricsService) {
	handler := &Handler{service: service, metricsService: metricsService}
	mux.HandleFunc("GET /api/dashboard/admin/status", handler.status)
	mux.HandleFunc("POST /api/dashboard/admin/login", handler.login)
	mux.HandleFunc("POST /api/dashboard/admin/logout", handler.logout)
	mux.HandleFunc("POST /api/dashboard/admin/refresh", handler.refreshAdminSession)
	mux.HandleFunc("GET /api/dashboard/metrics", handler.metrics)
	mux.HandleFunc("GET /api/dashboard/trends", handler.trends)
	mux.HandleFunc("GET /api/dashboard/groups", handler.adminGroups)
	mux.HandleFunc("GET /api/dashboard/group-usage-today", handler.groupUsageToday)
	mux.HandleFunc("GET /api/dashboard/group-profit-today", handler.groupProfitToday)
	mux.HandleFunc("GET /api/dashboard/upstream-key-usage-today", handler.upstreamKeyUsageToday)
	mux.HandleFunc("GET /api/dashboard/upstream-balance-breakdown", handler.upstreamBalanceBreakdown)
	mux.HandleFunc("GET /api/dashboard/balance-filter", handler.getBalanceFilter)
	mux.HandleFunc("PUT /api/dashboard/balance-filter", handler.saveBalanceFilter)
	mux.HandleFunc("GET /api/dashboard/daily-stats", handler.dailyStats)
	mux.HandleFunc("POST /api/dashboard/backfill", handler.backfill)
	mux.HandleFunc("GET /api/dashboard/additional-costs", handler.listAdditionalCosts)
	mux.HandleFunc("POST /api/dashboard/additional-costs", handler.createAdditionalCost)
	mux.HandleFunc("GET /api/dashboard/recharge-fee-rate", handler.getRechargeFeeRate)
	mux.HandleFunc("PUT /api/dashboard/recharge-fee-rate", handler.saveRechargeFeeRate)
	mux.HandleFunc("POST /api/dashboard/account-batches", handler.createAccountBatch)
	mux.HandleFunc("GET /api/dashboard/account-assets", handler.listAccountAssets)
	mux.HandleFunc("GET /api/dashboard/account-assets/{id}", handler.getAccountAsset)
	mux.HandleFunc("POST /api/dashboard/account-assets/{id}/events", handler.createAccountEvent)
	mux.HandleFunc("PUT /api/dashboard/account-assets/{id}/link", handler.replaceAccountLink)
	mux.HandleFunc("GET /api/dashboard/cost-ledger", handler.listAccountCostLedger)
	mux.HandleFunc("POST /api/dashboard/account-stats/refresh", handler.refreshAccountStats)
}

func (h *Handler) replaceAccountLink(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if h.metricsService == nil || h.metricsService.accountAssets == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "dashboard.accountAsset.errors.unavailable")
		return
	}
	var input AccountLinkInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.metricsService.accountAssets.ReplaceLink(
		r.Context(), userID, r.PathValue("id"), r.Header.Get("Idempotency-Key"), input,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) refreshAccountStats(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input struct {
		Date string `json:"date"`
	}
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.metricsService.RefreshAccountStats(r.Context(), userID, input.Date, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) listAccountCostLedger(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if h.metricsService == nil || h.metricsService.accountAssets == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "dashboard.accountAsset.errors.unavailable")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	result, err := h.metricsService.accountAssets.ListCostLedger(r.Context(), userID, AccountCostLedgerFilter{
		From: r.URL.Query().Get("from"), To: r.URL.Query().Get("to"), Type: r.URL.Query().Get("type"),
		Platform: r.URL.Query().Get("platform"), Channel: r.URL.Query().Get("channel"),
		BatchID: r.URL.Query().Get("batchId"), AccountAssetID: r.URL.Query().Get("accountAssetId"),
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) createAccountBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if h.metricsService == nil || h.metricsService.accountAssets == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "dashboard.accountAsset.errors.unavailable")
		return
	}
	var input AccountBatchInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.metricsService.accountAssets.CreateBatch(r.Context(), userID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, result)
}

func (h *Handler) listAccountAssets(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if h.metricsService == nil || h.metricsService.accountAssets == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "dashboard.accountAsset.errors.unavailable")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	result, err := h.metricsService.accountAssets.ListAssets(r.Context(), userID, AccountAssetFilter{
		Platform: r.URL.Query().Get("platform"), Channel: r.URL.Query().Get("channel"),
		AccountType: r.URL.Query().Get("accountType"), Status: r.URL.Query().Get("status"),
		Search: r.URL.Query().Get("search"), Page: page, PageSize: pageSize,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) getAccountAsset(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if h.metricsService == nil || h.metricsService.accountAssets == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "dashboard.accountAsset.errors.unavailable")
		return
	}
	detail, err := h.metricsService.accountAssets.GetAssetDetail(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

func (h *Handler) createAccountEvent(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if h.metricsService == nil || h.metricsService.accountAssets == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "dashboard.accountAsset.errors.unavailable")
		return
	}
	var event AccountEvent
	if err := httpjson.Decode(r, &event); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.metricsService.accountAssets.AppendEvent(r.Context(), userID, r.PathValue("id"), r.Header.Get("Idempotency-Key"), event)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, result)
}

func (h *Handler) getRechargeFeeRate(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = businesstime.Today()
	}
	value, err := h.metricsService.GetRechargeFeeRate(r.Context(), userID, date)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, value)
}

func (h *Handler) saveRechargeFeeRate(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input RechargeFeeRateInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	value, err := h.metricsService.SaveRechargeFeeRate(r.Context(), userID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, value)
}

func (h *Handler) listAdditionalCosts(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = businesstime.Today()
	}
	if to == "" {
		to = from
	}
	items, err := h.metricsService.ListAdditionalCosts(r.Context(), userID, from, to)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createAdditionalCost(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input AdditionalCostInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	items, err := h.metricsService.CreateAdditionalCost(r.Context(), userID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, map[string]any{"items": items})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.service.Status(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var dto LoginRequest
	if err := httpjson.Decode(r, &dto); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	response, err := h.service.Login(r.Context(), userID, dto)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if err := h.service.Logout(r.Context(), userID); err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, StatusResponse{Authenticated: false})
}

// refreshAdminSession 主动刷新当前 admin session 并重新校验 admin 身份。
func (h *Handler) refreshAdminSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.service.RefreshAdminSession(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// metrics 返回当前用户的仪表盘五项核心指标实时数据。
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.metricsService.LiveMetrics(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// trends 返回历史趋势数据，通过 ?days=7 或 ?days=30 指定查询范围。
func (h *Handler) trends(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	days := 7
	if r.URL.Query().Get("days") == "30" {
		days = 30
	}
	response, err := h.metricsService.Trends(r.Context(), userID, days)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// adminGroups 返回管理员站点的分组列表。
func (h *Handler) adminGroups(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.metricsService.AdminGroups(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// groupUsageToday 返回当前工作区「我的站点」所有分组今日的使用额度明细。
func (h *Handler) groupUsageToday(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.metricsService.GroupUsageToday(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

func (h *Handler) groupProfitToday(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.metricsService.GroupProfitToday(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// upstreamKeyUsageToday 返回当前工作区所有上游站点中，今天有消费的 key 明细（仪表盘「今日成本」下钻）。
func (h *Handler) upstreamKeyUsageToday(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.metricsService.UpstreamKeyUsageToday(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// upstreamBalanceBreakdown 返回当前工作区所有上游站点的余额明细（仪表盘「上游总余额」下钻）。
func (h *Handler) upstreamBalanceBreakdown(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.metricsService.UpstreamBalanceBreakdown(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

// getBalanceFilter 返回当前用户的站点用户余额筛选配置。
func (h *Handler) getBalanceFilter(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	config, err := h.metricsService.GetBalanceFilter(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, config)
}

// saveBalanceFilter 保存当前用户的站点用户余额筛选配置。
func (h *Handler) saveBalanceFilter(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var config BalanceFilterConfig
	if err := httpjson.Decode(r, &config); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	config.UserID = userID
	if config.ExcludeBalances == nil {
		config.ExcludeBalances = []float64{}
	}
	if err := h.metricsService.SaveBalanceFilter(r.Context(), userID, config); err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, config)
}

// writeError 把 service 的业务错误映射成合适的 HTTP 状态码与 i18n 错误 key。
func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvalidAccountBatch) || errors.Is(err, errInvalidAllocationCount) {
		httpjson.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrAccountAssetNotFound) {
		httpjson.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrAdditionalCostInvalidType) || errors.Is(err, ErrAdditionalCostInvalidAmount) || errors.Is(err, ErrAdditionalCostInvalidDate) || errors.Is(err, ErrAdditionalCostInvalidDays) || errors.Is(err, ErrAdditionalCostInvalidRate) {
		httpjson.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	var requestErr requestError
	if errors.As(err, &requestErr) {
		status := http.StatusBadRequest
		switch requestErr {
		case requestError(ErrorAdminOnly):
			status = http.StatusForbidden
		case requestError(ErrorPlatformUnsupported):
			status = http.StatusNotImplemented
		case requestError(ErrorUpstreamKeyUsageUnavailable):
			status = http.StatusBadGateway
		}
		httpjson.WriteError(w, status, requestErr.Error())
		return
	}
	httpjson.WriteError(w, http.StatusInternalServerError, ErrorUnknown)
}

// dailyStats 返回指定日期范围内每天的结算状态（缺失日期返回 missing 占位）。
func (h *Handler) dailyStats(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	q := r.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	if from == "" || to == "" {
		httpjson.WriteError(w, http.StatusBadRequest, "dashboard.errors.invalidDate")
		return
	}

	page := 1
	pageSize := 31
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpjson.WriteError(w, http.StatusBadRequest, "dashboard.errors.invalidPage")
			return
		}
		page = n
	}
	if v := q.Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			httpjson.WriteError(w, http.StatusBadRequest, "dashboard.errors.invalidPageSize")
			return
		}
		pageSize = n
	}
	expandStr := q.Get("expand")
	if expandStr != "" && expandStr != "true" && expandStr != "false" {
		httpjson.WriteError(w, http.StatusBadRequest, "dashboard.errors.invalidExpand")
		return
	}
	expand := expandStr == "true"

	items, err := h.metricsService.DailyStats(r.Context(), userID, from, to, page, pageSize, expand)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

// backfill 受控回填指定日期范围的历史数据（需要 admin 权限）。
func (h *Handler) backfill(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var req BackfillRequest
	if err := httpjson.Decode(r, &req); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.metricsService.Backfill(r.Context(), userID, req)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
