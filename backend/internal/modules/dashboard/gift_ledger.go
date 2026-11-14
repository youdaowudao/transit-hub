package dashboard

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

// 单独客户赠送记录：少数客户每次赠送比例不同，每送一笔记一笔。客户花钱时先用最早的充值：
// 花到已记录部分按那一笔的赠送占比从营收扣，不再按整站折扣率；超出部分按整站折扣率。
// 计算规则见 docs/changes/20261114/2-单独客户赠送记录/01-SPEC.md 第 6 节。

const (
	// 保存记录时充值日期最早可填的天数。
	giftLedgerMaxBackdateDays = 60
	// 向主站按天补查消费的最早日期（主站保留 90 天，留 10 天余量）。
	giftLedgerMainSiteFetchDays = 80
	// 主站按客户查询的并发数。
	giftLedgerFetchConcurrency = 4
	// 单独客户日结排队用的固定日期键：复用日结锁，让同一工作区所有日期的单独客户部分排队。
	giftLedgerSettlementLockDate = "gift-ledger"
)

var ErrGiftLedgerNotFound = errors.New("dashboard.giftLedger.errors.notFound")

// giftLedgerRepository 由 PostgreSQL 仓储实现；未实现时视为没有单独客户记录。
type giftLedgerRepository interface {
	ListGiftLedgerRecords(ctx context.Context, userID, adminAccountID string) ([]GiftLedgerRecord, error)
	GetGiftLedgerRecord(ctx context.Context, userID, adminAccountID, id string) (GiftLedgerRecord, error)
	CreateGiftLedgerRecord(ctx context.Context, record GiftLedgerRecord) error
	UpdateGiftLedgerRecord(ctx context.Context, record GiftLedgerRecord) error
	DeleteGiftLedgerRecord(ctx context.Context, userID, adminAccountID, id string) error
	// ListGiftCustomerUsage 返回 主站用户 → 日期 → 微单位消费。
	ListGiftCustomerUsage(ctx context.Context, userID, adminAccountID string, mainUserIDs []int64, from, to string) (map[int64]map[string]int64, error)
	// SaveGiftCustomerUsage 第一次保存为准：已存在就不改，并返回库里的值。
	SaveGiftCustomerUsage(ctx context.Context, userID, adminAccountID string, mainUserID int64, date string, micros int64, observedAt time.Time) (int64, error)
}

// giftLedgerPlatform 是按客户查询所需的主站能力；生产 PlatformService 已实现，测试替身按需实现。
type giftLedgerPlatform interface {
	FetchSub2APIAdminUserUsageCost(session upstream.Session, mainUserID, startDate, endDate string) (float64, error)
	FetchSub2APIAdminUser(session upstream.Session, userID string) (upstream.Sub2APIAdminUser, error)
}

func (s *MetricsService) giftLedgerRepository() (giftLedgerRepository, bool) {
	repository, ok := s.metricsRepo.(giftLedgerRepository)
	return repository, ok
}

func (s *MetricsService) giftLedgerPlatform() (giftLedgerPlatform, bool) {
	platform, ok := s.platform.(giftLedgerPlatform)
	return platform, ok
}

// giftLedgerComputeError 表示单独客户赠送未知；Reason 是白话原因（SPEC 第 10 节“具体原因”）。
type giftLedgerComputeError struct{ Reason string }

func (e giftLedgerComputeError) Error() string { return e.Reason }

const (
	giftLedgerReasonStorage     = "读取单独客户赠送记录或已结算明细失败（本站数据库）"
	giftLedgerReasonUnsupported = "当前主站不是 Sub2API，不能按客户查询消费"
)

func giftLedgerCustomerLabel(record GiftLedgerRecord) string {
	return fmt.Sprintf("客户 %s（主站 ID %d）", record.MainUserEmail, record.MainUserID)
}

func giftLedgerAddDays(date string, days int) string {
	day, err := time.ParseInLocation("2006-01-02", date, businesstime.Location())
	if err != nil {
		return date
	}
	return day.AddDate(0, 0, days).Format("2006-01-02")
}

// giftLedgerSettled 判断快照行是否已日结：日结来源，且写入时间不早于这一天新加坡 24:00。
func giftLedgerSettled(snapshot DailySnapshot) bool {
	if snapshot.SnapshotSource != SnapshotSourceDatedQuery && snapshot.SnapshotSource != SnapshotSourceBackfill {
		return false
	}
	if snapshot.ObservedAt == nil {
		return false
	}
	_, end, err := businesstime.Bounds(snapshot.Date.In(businesstime.Location()).Format("2006-01-02"))
	if err != nil {
		return false
	}
	dayEnd := end.Add(time.Nanosecond)
	return !snapshot.ObservedAt.Before(dayEnd)
}

func snapshotDate(snapshot DailySnapshot) string {
	return snapshot.Date.In(businesstime.Location()).Format("2006-01-02")
}

type giftLedgerFetchTask struct {
	record GiftLedgerRecord // 该客户最早的一笔，用于原因文字
	date   string
	save   bool
}

// giftLedgerDay 计算 date 这一天的单独客户结果。没有充值日期 ≤ date 的记录时返回 nil, nil，
// 不读快照、不调主站。settling 为真表示日结（date 早于 today）：正在日结的这一天会保存消费，并做晚结保护。
func (s *MetricsService) giftLedgerDay(ctx context.Context, session upstream.Session, userID, adminAccountID, date, today string, settling bool) (*giftLedgerDayResult, error) {
	repository, ok := s.giftLedgerRepository()
	if !ok {
		return nil, nil
	}
	all, err := repository.ListGiftLedgerRecords(ctx, userID, adminAccountID)
	if err != nil {
		return nil, giftLedgerComputeError{Reason: giftLedgerReasonStorage}
	}
	byCustomer := map[int64][]GiftLedgerRecord{}
	firstDate := ""
	for _, record := range all {
		if record.RechargeDate > date {
			continue
		}
		byCustomer[record.MainUserID] = append(byCustomer[record.MainUserID], record)
		if firstDate == "" || record.RechargeDate < firstDate {
			firstDate = record.RechargeDate
		}
	}
	if len(byCustomer) == 0 {
		return nil, nil
	}
	platform, ok := s.giftLedgerPlatform()
	if !ok || session.Platform != upstream.PlatformSub2API {
		return nil, giftLedgerComputeError{Reason: giftLedgerReasonUnsupported}
	}
	customerIDs := make([]int64, 0, len(byCustomer))
	for id := range byCustomer {
		sortGiftLedgerRecords(byCustomer[id])
		customerIDs = append(customerIDs, id)
	}
	sort.Slice(customerIDs, func(i, j int) bool { return customerIDs[i] < customerIDs[j] })

	// 已日结日的明细：补齐保存值、晚结保护都要用。
	yesterday := giftLedgerAddDays(today, -1)
	settledCustomers := map[string]map[int64]int64{} // 日期 → 客户 → 结算时消费
	if firstDate <= yesterday {
		snapshots, err := s.metricsRepo.ListDailyStats(ctx, userID, adminAccountID, firstDate, yesterday)
		if err != nil {
			return nil, giftLedgerComputeError{Reason: giftLedgerReasonStorage}
		}
		for _, snapshot := range snapshots {
			day := snapshotDate(snapshot)
			if day < firstDate || day > yesterday || !giftLedgerSettled(snapshot) {
				continue
			}
			detail, err := parseRevenueDiscountDetail(snapshot.RevenueDiscountDetail)
			if err != nil {
				return nil, giftLedgerComputeError{Reason: fmt.Sprintf("%s 已结算的单独客户明细无法读取（数据格式不对），请联系开发者检查这一天的数据", day)}
			}
			customers := map[int64]int64{}
			if detail != nil {
				for _, customer := range detail.Customers {
					id, convErr := strconv.ParseInt(customer.MainUserID, 10, 64)
					if convErr != nil {
						return nil, giftLedgerComputeError{Reason: fmt.Sprintf("%s 已结算的单独客户明细无法读取（数据格式不对），请联系开发者检查这一天的数据", day)}
					}
					customers[id] = customer.ConsumptionMicros
				}
			}
			settledCustomers[day] = customers
		}
	}

	stored, err := repository.ListGiftCustomerUsage(ctx, userID, adminAccountID, customerIDs, firstDate, date)
	if err != nil {
		return nil, giftLedgerComputeError{Reason: giftLedgerReasonStorage}
	}
	consumption := map[int64]map[string]int64{}
	fetchLimit := giftLedgerAddDays(today, -giftLedgerMainSiteFetchDays)
	observedAt := s.currentTime().UTC()
	tasks := map[int64][]giftLedgerFetchTask{}
	for _, id := range customerIDs {
		records := byCustomer[id]
		consumption[id] = map[string]int64{}
		for day := records[0].RechargeDate; day <= date; day = giftLedgerAddDays(day, 1) {
			if value, ok := stored[id][day]; ok {
				consumption[id][day] = value
				continue
			}
			if value, ok := settledCustomers[day][id]; ok {
				saved, saveErr := repository.SaveGiftCustomerUsage(ctx, userID, adminAccountID, id, day, value, observedAt)
				if saveErr != nil {
					return nil, giftLedgerComputeError{Reason: fmt.Sprintf("保存%s %s 的消费失败（本站数据库）", giftLedgerCustomerLabel(records[0]), day)}
				}
				consumption[id][day] = saved
				continue
			}
			save := day < yesterday || (settling && day == date)
			if save && day < fetchLimit {
				return nil, giftLedgerComputeError{Reason: fmt.Sprintf("%s %s 的消费超过本站 80 天安全补查范围，本站也没有存，无法推算他用到了哪一笔；请把这位客户最早的记录改为按今天零点剩余录入（见填写说明）后刷新", giftLedgerCustomerLabel(records[0]), day)}
			}
			tasks[id] = append(tasks[id], giftLedgerFetchTask{record: records[0], date: day, save: save})
		}
	}
	if err := s.runGiftLedgerFetches(ctx, session, platform, repository, userID, adminAccountID, observedAt, tasks, consumption); err != nil {
		return nil, err
	}

	result := &giftLedgerDayResult{customerResults: map[int64]giftLedgerCustomerResult{}, records: byCustomer}
	for _, id := range customerIDs {
		records := byCustomer[id]
		late := false
		if settling {
			for day, customers := range settledCustomers {
				if day > date {
					if _, ok := customers[id]; ok {
						late = true
						break
					}
				}
			}
		}
		history := make([]giftLedgerDayInput, 0)
		for day := records[0].RechargeDate; day < date; day = giftLedgerAddDays(day, 1) {
			if value := consumption[id][day]; value > 0 {
				history = append(history, giftLedgerDayInput{Date: day, ConsumptionMicros: value})
			}
		}
		customer := giftLedgerCustomerDay(records, history, giftLedgerDayInput{Date: date, ConsumptionMicros: consumption[id][date]}, late)
		result.customerResults[id] = customer
		result.Customers = append(result.Customers, RevenueDiscountCustomer{
			MainUserID: strconv.FormatInt(id, 10), Email: records[0].MainUserEmail,
			ConsumptionMicros: customer.ConsumptionMicros, CoveredMicros: customer.CoveredMicros,
			UncoveredMicros: customer.UncoveredMicros, GiftCents: customer.GiftCents, LateProtected: customer.LateProtected,
		})
		result.ConsumptionMicros += customer.ConsumptionMicros
		result.CoveredMicros += customer.CoveredMicros
		result.GiftCents += customer.GiftCents
	}
	return result, nil
}

// runGiftLedgerFetches 以并发 4 按天向主站查询缺少的消费；需要保存的保存后用库里的值。任何一项失败整份作废。
func (s *MetricsService) runGiftLedgerFetches(ctx context.Context, session upstream.Session, platform giftLedgerPlatform, repository giftLedgerRepository,
	userID, adminAccountID string, observedAt time.Time, tasks map[int64][]giftLedgerFetchTask, consumption map[int64]map[string]int64) error {
	type job struct {
		id   int64
		task giftLedgerFetchTask
	}
	jobs := make([]job, 0)
	ids := make([]int64, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		for _, task := range tasks[id] {
			jobs = append(jobs, job{id: id, task: task})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	var mu sync.Mutex
	var firstErr error
	var firstIndex = len(jobs)
	semaphore := make(chan struct{}, giftLedgerFetchConcurrency)
	var wg sync.WaitGroup
	for index, item := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		semaphore <- struct{}{}
		go func(index int, item job) {
			defer wg.Done()
			defer func() { <-semaphore }()
			fail := func(reason string) {
				mu.Lock()
				defer mu.Unlock()
				if index < firstIndex {
					firstIndex, firstErr = index, giftLedgerComputeError{Reason: reason}
				}
			}
			label := giftLedgerCustomerLabel(item.task.record)
			cost, err := platform.FetchSub2APIAdminUserUsageCost(session, strconv.FormatInt(item.id, 10), item.task.date, item.task.date)
			if err != nil {
				fail(fmt.Sprintf("读取%s %s 的主站消费失败（%s）", label, item.task.date, giftLedgerUpstreamReason("按客户查询消费", err, item.task.date)))
				return
			}
			value := giftLedgerMicros(cost)
			if item.task.save {
				saved, saveErr := repository.SaveGiftCustomerUsage(ctx, userID, adminAccountID, item.id, item.task.date, value, observedAt)
				if saveErr != nil {
					fail(fmt.Sprintf("保存%s %s 的消费失败（本站数据库）", label, item.task.date))
					return
				}
				value = saved
			}
			mu.Lock()
			consumption[item.id][item.task.date] = value
			mu.Unlock()
		}(index, item)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return giftLedgerComputeError{Reason: "计算被取消，请稍后刷新"}
	}
	return nil
}

const giftLedgerReasonLiveFallback = "主站总营收读取失败，今天还没有保存过单独客户结果，暂时无法扣除赠送"

// liveRevenueDiscountSource 选首页实时的折扣来源（PLAN 3.5）。主站总营收成功时现算单独客户部分（昨天、今天不保存）；
// 总营收失败、改用今天快照营收时不按客户查询主站：快照有明细用快照已存值，没有明细时只在没有相关记录时按 V3.1.6 现算。
func (s *MetricsService) liveRevenueDiscountSource(ctx context.Context, session upstream.Session, userID, adminAccountID, today string,
	revenue *float64, revenueErr error, latest *DailySnapshot) revenueDiscountSource {
	if revenue == nil || s.additionalCosts == nil {
		return revenueDiscountSource{}
	}
	if revenueErr == nil {
		ledger, err := s.giftLedgerDay(ctx, session, userID, adminAccountID, today, today, false)
		return revenueDiscountSource{ledger: ledger, ledgerErr: err}
	}
	if latest != nil && len(nullableJSON(latest.RevenueDiscountDetail)) > 0 {
		return revenueDiscountSource{snapshot: latest}
	}
	repository, ok := s.giftLedgerRepository()
	if !ok {
		return revenueDiscountSource{}
	}
	records, err := repository.ListGiftLedgerRecords(ctx, userID, adminAccountID)
	if err != nil {
		return revenueDiscountSource{ledgerErr: giftLedgerComputeError{Reason: giftLedgerReasonStorage}}
	}
	for _, record := range records {
		if record.RechargeDate <= today {
			return revenueDiscountSource{ledgerErr: giftLedgerComputeError{Reason: giftLedgerReasonLiveFallback}}
		}
	}
	return revenueDiscountSource{}
}

// storedRevenueDiscountSource 用于账号补结和每日明细今天行：快照有明细时用快照已存值，否则按 V3.1.6 现算。
func storedRevenueDiscountSource(snapshot DailySnapshot) revenueDiscountSource {
	if len(nullableJSON(snapshot.RevenueDiscountDetail)) > 0 {
		return revenueDiscountSource{snapshot: &snapshot}
	}
	return revenueDiscountSource{}
}

// settlementRevenueDiscountSource 选日结的折扣来源（PLAN 3.5）。这一天已日结时冻结单独客户部分（沿用已存明细，
// 明细为空表示没有单独客户）；否则按 ledgerToday 现算。读取已存快照失败时返回错误，由调用方记录站点尝试后返回。
func (s *MetricsService) settlementRevenueDiscountSource(ctx context.Context, session upstream.Session, userID, adminAccountID, date, ledgerToday string) (revenueDiscountSource, error) {
	strict := date < ledgerToday
	if s.additionalCosts == nil {
		// 没有费用汇总时不计算营收折扣（现有行为），也不按客户查询。
		return revenueDiscountSource{}, nil
	}
	snapshots, err := s.metricsRepo.ListDailyStats(ctx, userID, adminAccountID, date, date)
	if err != nil {
		return revenueDiscountSource{}, err
	}
	for _, snapshot := range snapshots {
		if snapshotDate(snapshot) != date || !giftLedgerSettled(snapshot) {
			continue
		}
		detail, detailErr := parseRevenueDiscountDetail(snapshot.RevenueDiscountDetail)
		if detailErr != nil {
			return revenueDiscountSource{ledgerErr: giftLedgerComputeError{Reason: date + " 已结算的单独客户明细无法读取（数据格式不对），请联系开发者检查这一天的数据"}, strict: strict}, nil
		}
		return revenueDiscountSource{ledger: giftLedgerResultFromDetail(detail), strict: strict}, nil
	}
	ledger, ledgerErr := s.giftLedgerDay(ctx, session, userID, adminAccountID, date, ledgerToday, strict)
	return revenueDiscountSource{ledger: ledger, ledgerErr: ledgerErr, strict: strict}, nil
}
