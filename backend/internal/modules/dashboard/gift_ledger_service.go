package dashboard

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

// 单独客户赠送记录的管理接口（PLAN 3.6、3.7）。记录只存在本站，查询主站只读。
var (
	ErrGiftLedgerInvalidUser   = errors.New("dashboard.giftLedger.errors.invalidUser")
	ErrGiftLedgerInvalidAmount = errors.New("dashboard.giftLedger.errors.invalidAmount")
	ErrGiftLedgerInvalidNote   = errors.New("dashboard.giftLedger.errors.invalidNote")
	ErrGiftLedgerInvalidDate   = errors.New("dashboard.giftLedger.errors.invalidDate")
	ErrGiftLedgerFutureDate    = errors.New("dashboard.giftLedger.errors.futureDate")
	ErrGiftLedgerTooOld        = errors.New("dashboard.giftLedger.errors.tooOld")
	ErrGiftLedgerUserNotFound  = errors.New("dashboard.giftLedger.errors.userNotFound")
	ErrGiftLedgerUnsupported   = errors.New("dashboard.giftLedger.errors.unsupported")
	errGiftLedgerUnavailable   = errors.New("dashboard.additionalCost.errors.unavailable")
)

const (
	giftLedgerKeyUserLookupFailed  = "dashboard.giftLedger.errors.userLookupFailed"
	giftLedgerKeyUsageLookupFailed = "dashboard.giftLedger.errors.usageLookupFailed"
	giftLedgerMaxAmount            = 10_000_000
	giftLedgerMaxNoteRunes         = 200
)

// giftLedgerLookupError 是向主站核对失败的错误：Key 为错误 key，Reason 为脱敏白话原因，处理函数原样返回。
type giftLedgerLookupError struct {
	Key    string
	Reason string
}

func (e giftLedgerLookupError) Error() string { return e.Key }

type GiftLedgerInput struct {
	MainUserID   string  `json:"mainUserId"`
	RechargeDate string  `json:"rechargeDate"`
	PaidAmount   float64 `json:"paidAmount"`
	GiftAmount   float64 `json:"giftAmount"`
	Note         string  `json:"note"`
}

type giftLedgerNormalizedInput struct {
	mainUserID int64
	date       string
	paidCents  int64
	giftCents  int64
	note       string
}

// giftLedgerCents 校验金额：有限、0 到 10,000,000、最多两位小数（只容许浮点表示误差）。
func giftLedgerCents(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > giftLedgerMaxAmount {
		return 0, false
	}
	scaled := value * 100
	rounded := math.Round(scaled)
	if math.Abs(scaled-rounded) > 1e-6 {
		return 0, false
	}
	return int64(rounded), true
}

// giftLedgerMaxMainUserID 是能被 JSON 数字（float64）精确表示的最大整数。主站用户资料的编号以数字返回，
// 超过它会被解析成别的数，核对时会误判为别的用户，所以不接受更大的编号。
const giftLedgerMaxMainUserID = 1<<53 - 1

func parseGiftLedgerMainUserID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 18 {
		return 0, ErrGiftLedgerInvalidUser
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, ErrGiftLedgerInvalidUser
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || id > giftLedgerMaxMainUserID {
		return 0, ErrGiftLedgerInvalidUser
	}
	return id, nil
}

// normalizeGiftLedgerInput 校验输入。editing 时不校验主站用户（不能改），日期没变时允许保留早于 60 天的原日期。
func normalizeGiftLedgerInput(input GiftLedgerInput, today string, editing bool, originalDate string) (giftLedgerNormalizedInput, error) {
	var result giftLedgerNormalizedInput
	if !editing {
		id, err := parseGiftLedgerMainUserID(input.MainUserID)
		if err != nil {
			return result, err
		}
		result.mainUserID = id
	}
	date := strings.TrimSpace(input.RechargeDate)
	parsed, err := time.ParseInLocation("2006-01-02", date, businesstime.Location())
	if err != nil || parsed.Format("2006-01-02") != date {
		return result, ErrGiftLedgerInvalidDate
	}
	if date > today {
		return result, ErrGiftLedgerFutureDate
	}
	if !(editing && date == originalDate) && date < giftLedgerAddDays(today, -giftLedgerMaxBackdateDays) {
		return result, ErrGiftLedgerTooOld
	}
	paid, okPaid := giftLedgerCents(input.PaidAmount)
	gift, okGift := giftLedgerCents(input.GiftAmount)
	if !okPaid || !okGift || paid+gift == 0 {
		return result, ErrGiftLedgerInvalidAmount
	}
	note := strings.TrimSpace(input.Note)
	if utf8.RuneCountInString(note) > giftLedgerMaxNoteRunes {
		return result, ErrGiftLedgerInvalidNote
	}
	result.date, result.paidCents, result.giftCents, result.note = date, paid, gift, note
	return result, nil
}

type GiftLedgerRecordView struct {
	ID           string   `json:"id"`
	MainUserID   string   `json:"mainUserId"`
	Email        string   `json:"email"`
	RechargeDate string   `json:"rechargeDate"`
	PaidAmount   float64  `json:"paidAmount"`
	GiftAmount   float64  `json:"giftAmount"`
	CreditAmount float64  `json:"creditAmount"`
	GiftShare    float64  `json:"giftShare"`
	Note         string   `json:"note"`
	UsedAmount   *float64 `json:"usedAmount"`
	State        string   `json:"state"`
}

type GiftLedgerCustomerView struct {
	MainUserID       string                 `json:"mainUserId"`
	Email            string                 `json:"email"`
	CreditedTotal    float64                `json:"creditedTotal"`
	UsedTotal        *float64               `json:"usedTotal"`
	Remaining        *float64               `json:"remaining"`
	TodayConsumption *float64               `json:"todayConsumption"`
	TodayCovered     *float64               `json:"todayCovered"`
	TodayUncovered   *float64               `json:"todayUncovered"`
	TodayGift        *float64               `json:"todayGift"`
	MainBalance      *float64               `json:"mainBalance"`
	Records          []GiftLedgerRecordView `json:"records"`
}

type GiftLedgerOverview struct {
	Supported       bool                     `json:"supported"`
	StatusAvailable bool                     `json:"statusAvailable"`
	StatusError     string                   `json:"statusError"`
	Today           string                   `json:"today"`
	Customers       []GiftLedgerCustomerView `json:"customers"`
}

type GiftLedgerCustomerCheck struct {
	MainUserID       string   `json:"mainUserId"`
	Email            string   `json:"email"`
	Balance          *float64 `json:"balance"`
	TodayConsumption float64  `json:"todayConsumption"`
}

func giftLedgerRecordView(record GiftLedgerRecord) GiftLedgerRecordView {
	credit := record.PaidCents + record.GiftCents
	share := 0.0
	if credit > 0 {
		share = math.Round(float64(record.GiftCents)/float64(credit)*1e4) / 1e4
	}
	return GiftLedgerRecordView{
		ID: record.ID, MainUserID: strconv.FormatInt(record.MainUserID, 10), Email: record.MainUserEmail,
		RechargeDate: record.RechargeDate, PaidAmount: float64(record.PaidCents) / 100, GiftAmount: float64(record.GiftCents) / 100,
		CreditAmount: float64(credit) / 100, GiftShare: share, Note: record.Note,
	}
}

func giftLedgerEmail(user upstream.Sub2APIAdminUser) string {
	if email := strings.TrimSpace(user.Email); email != "" {
		return email
	}
	return strings.TrimSpace(user.Username)
}

func amountPtr(micros int64) *float64 {
	value := math.Round(microsToAmount(micros)*1e6) / 1e6
	return &value
}

// giftLedgerAdminSession 取当前工作区的主站管理员会话；平台不是 Sub2API 时返回 ErrGiftLedgerUnsupported。
func (s *MetricsService) giftLedgerAdminSession(ctx context.Context, userID, adminAccountID string) (upstream.Session, giftLedgerPlatform, error) {
	record, err := s.store.Get(ctx, userID, adminAccountID)
	if err != nil {
		return upstream.Session{}, nil, err
	}
	if record == nil || !record.Session.IsAuthenticated() {
		return upstream.Session{}, nil, requestError(ErrorAdminOnly)
	}
	session, err := s.freshAdminSession(ctx, userID, adminAccountID, record)
	if err != nil {
		return upstream.Session{}, nil, requestError(ErrorAdminOnly)
	}
	platform, ok := s.giftLedgerPlatform()
	if !ok || session.Platform != upstream.PlatformSub2API {
		return upstream.Session{}, nil, ErrGiftLedgerUnsupported
	}
	return session, platform, nil
}

// lookupGiftLedgerUser 向主站核对用户：用户资料接口 404 为“找不到用户”，其他失败带脱敏原因；
// 返回的资料编号必须与查询的主站用户 ID 相同，否则按资料不完整处理，不保存。
func lookupGiftLedgerUser(platform giftLedgerPlatform, session upstream.Session, mainUserID int64, today string) (upstream.Sub2APIAdminUser, error) {
	id := strconv.FormatInt(mainUserID, 10)
	user, err := platform.FetchSub2APIAdminUser(session, id)
	if err == nil {
		if strings.TrimSpace(user.ID) != id {
			return upstream.Sub2APIAdminUser{}, giftLedgerLookupError{Key: giftLedgerKeyUserLookupFailed, Reason: giftLedgerReasonUserMismatch(id)}
		}
		return user, nil
	}
	var requestErr *upstream.RequestError
	if errors.As(err, &requestErr) && requestErr.StatusCode == http.StatusNotFound {
		return upstream.Sub2APIAdminUser{}, ErrGiftLedgerUserNotFound
	}
	return upstream.Sub2APIAdminUser{}, giftLedgerLookupError{Key: giftLedgerKeyUserLookupFailed, Reason: giftLedgerUpstreamReason("读取主站用户资料", err, today)}
}

func giftLedgerReasonUserMismatch(id string) string {
	return "读取主站用户资料：主站有响应，但返回的资料里没有主站用户 ID " + id + "（内容不完整或不是这个用户），没有保存；请稍后重试，仍失败请到主站用户管理核对这个用户"
}

func (s *MetricsService) CreateGiftLedgerRecord(ctx context.Context, userID string, input GiftLedgerInput) (GiftLedgerRecordView, error) {
	repository, ok := s.giftLedgerRepository()
	if !ok {
		return GiftLedgerRecordView{}, errGiftLedgerUnavailable
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	today := businesstime.DateAt(s.currentTime())
	normalized, err := normalizeGiftLedgerInput(input, today, false, "")
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	session, platform, err := s.giftLedgerAdminSession(ctx, userID, adminAccountID)
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	user, err := lookupGiftLedgerUser(platform, session, normalized.mainUserID, today)
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	now := time.Now().UTC()
	record := GiftLedgerRecord{
		ID: mustMetricsID(), UserID: userID, AdminAccountID: adminAccountID, MainUserID: normalized.mainUserID,
		MainUserEmail: giftLedgerEmail(user), RechargeDate: normalized.date, PaidCents: normalized.paidCents,
		GiftCents: normalized.giftCents, Note: normalized.note, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateGiftLedgerRecord(ctx, record); err != nil {
		return GiftLedgerRecordView{}, err
	}
	return giftLedgerRecordView(record), nil
}

// UpdateGiftLedgerRecord 只改日期、金额和备注；主站用户和邮箱不变。
func (s *MetricsService) UpdateGiftLedgerRecord(ctx context.Context, userID, id string, input GiftLedgerInput) (GiftLedgerRecordView, error) {
	repository, ok := s.giftLedgerRepository()
	if !ok {
		return GiftLedgerRecordView{}, errGiftLedgerUnavailable
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	if strings.TrimSpace(id) == "" {
		return GiftLedgerRecordView{}, ErrGiftLedgerNotFound
	}
	record, err := repository.GetGiftLedgerRecord(ctx, userID, adminAccountID, id)
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	normalized, err := normalizeGiftLedgerInput(input, businesstime.DateAt(s.currentTime()), true, record.RechargeDate)
	if err != nil {
		return GiftLedgerRecordView{}, err
	}
	record.RechargeDate, record.PaidCents, record.GiftCents, record.Note = normalized.date, normalized.paidCents, normalized.giftCents, normalized.note
	record.UpdatedAt = time.Now().UTC()
	if err := repository.UpdateGiftLedgerRecord(ctx, record); err != nil {
		return GiftLedgerRecordView{}, err
	}
	return giftLedgerRecordView(record), nil
}

func (s *MetricsService) DeleteGiftLedgerRecord(ctx context.Context, userID, id string) error {
	repository, ok := s.giftLedgerRepository()
	if !ok {
		return errGiftLedgerUnavailable
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return ErrGiftLedgerNotFound
	}
	return repository.DeleteGiftLedgerRecord(ctx, userID, adminAccountID, id)
}

// CheckGiftLedgerCustomer 返回邮箱、主站余额和今天已花，供按“今天零点的剩余”录入时估算。
func (s *MetricsService) CheckGiftLedgerCustomer(ctx context.Context, userID, rawMainUserID string) (GiftLedgerCustomerCheck, error) {
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return GiftLedgerCustomerCheck{}, err
	}
	mainUserID, err := parseGiftLedgerMainUserID(rawMainUserID)
	if err != nil {
		return GiftLedgerCustomerCheck{}, err
	}
	session, platform, err := s.giftLedgerAdminSession(ctx, userID, adminAccountID)
	if err != nil {
		return GiftLedgerCustomerCheck{}, err
	}
	today := businesstime.DateAt(s.currentTime())
	user, err := lookupGiftLedgerUser(platform, session, mainUserID, today)
	if err != nil {
		return GiftLedgerCustomerCheck{}, err
	}
	email := giftLedgerEmail(user)
	cost, err := platform.FetchSub2APIAdminUserUsageCost(session, strconv.FormatInt(mainUserID, 10), today, today)
	if err != nil {
		// 这一步的任何失败（包括 404）都不是“找不到用户”：用户资料已查到。
		return GiftLedgerCustomerCheck{}, giftLedgerLookupError{
			Key:    giftLedgerKeyUsageLookupFailed,
			Reason: "查到了 " + email + "，但读取他今天的消费失败（" + giftLedgerUpstreamReason("读取主站今天消费", err, today) + "）；今天已花没有查到，请稍后重试",
		}
	}
	return GiftLedgerCustomerCheck{MainUserID: strconv.FormatInt(mainUserID, 10), Email: email, Balance: user.Balance, TodayConsumption: math.Round(cost*1e6) / 1e6}, nil
}

// GiftLedgerOverview 返回记录和今天的状态。状态查询失败时仍列出记录，数字为空并给出原因。
func (s *MetricsService) GiftLedgerOverview(ctx context.Context, userID string) (GiftLedgerOverview, error) {
	repository, ok := s.giftLedgerRepository()
	if !ok {
		return GiftLedgerOverview{}, errGiftLedgerUnavailable
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return GiftLedgerOverview{}, err
	}
	today := businesstime.DateAt(s.currentTime())
	records, err := repository.ListGiftLedgerRecords(ctx, userID, adminAccountID)
	if err != nil {
		return GiftLedgerOverview{}, err
	}
	overview := GiftLedgerOverview{Supported: true, Today: today, Customers: make([]GiftLedgerCustomerView, 0)}
	byCustomer := map[int64][]GiftLedgerRecord{}
	ids := make([]int64, 0)
	for _, record := range records {
		if _, seen := byCustomer[record.MainUserID]; !seen {
			ids = append(ids, record.MainUserID)
		}
		byCustomer[record.MainUserID] = append(byCustomer[record.MainUserID], record)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	session, platform, sessionErr := s.giftLedgerAdminSession(ctx, userID, adminAccountID)
	var ledger *giftLedgerDayResult
	switch {
	case errors.Is(sessionErr, ErrGiftLedgerUnsupported):
		overview.Supported = false
		overview.StatusError = giftLedgerReasonUnsupported
	case sessionErr != nil:
		overview.StatusError = "没有可用的主站管理员登录，请重新登录主站后再试"
	case len(records) > 0:
		ledger, err = s.giftLedgerDay(ctx, session, userID, adminAccountID, today, today, false)
		if err != nil {
			overview.StatusError = giftLedgerErrorReason(err, today)
		} else {
			overview.StatusAvailable = true
		}
	default:
		overview.StatusAvailable = true
	}
	var balances map[int64]*float64
	if sessionErr == nil && len(ids) > 0 {
		balances = giftLedgerBalances(platform, session, ids)
	}
	for _, id := range ids {
		customerRecords := byCustomer[id]
		sortGiftLedgerRecords(customerRecords)
		view := GiftLedgerCustomerView{MainUserID: strconv.FormatInt(id, 10), Email: customerRecords[0].MainUserEmail, MainBalance: balances[id]}
		var credited int64
		for _, record := range customerRecords {
			credited += record.creditMicros()
			view.Records = append(view.Records, giftLedgerRecordView(record))
		}
		view.CreditedTotal = microsToAmount(credited)
		if ledger != nil {
			if result, ok := ledger.customerResults[id]; ok {
				var used int64
				uses := map[string]giftLedgerRecordUse{}
				for _, use := range result.Records {
					used += use.UsedMicros
					uses[use.RecordID] = use
				}
				view.UsedTotal, view.Remaining = amountPtr(used), amountPtr(credited-used)
				view.TodayConsumption, view.TodayCovered, view.TodayUncovered = amountPtr(result.ConsumptionMicros), amountPtr(result.CoveredMicros), amountPtr(result.UncoveredMicros)
				gift := float64(result.GiftCents) / 100
				view.TodayGift = &gift
				for i := range view.Records {
					if use, ok := uses[view.Records[i].ID]; ok {
						view.Records[i].UsedAmount = amountPtr(use.UsedMicros)
						view.Records[i].State = use.State
					}
				}
			}
		}
		overview.Customers = append(overview.Customers, view)
	}
	return overview, nil
}

// giftLedgerBalances 以并发 4 读取主站余额；失败的为空，不影响计算。
func giftLedgerBalances(platform giftLedgerPlatform, session upstream.Session, ids []int64) map[int64]*float64 {
	result := map[int64]*float64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, giftLedgerFetchConcurrency)
	for _, id := range ids {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(id int64) {
			defer wg.Done()
			defer func() { <-semaphore }()
			user, err := platform.FetchSub2APIAdminUser(session, strconv.FormatInt(id, 10))
			if err != nil || user.Balance == nil || strings.TrimSpace(user.ID) != strconv.FormatInt(id, 10) {
				return
			}
			balance := *user.Balance
			mu.Lock()
			result[id] = &balance
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return result
}
