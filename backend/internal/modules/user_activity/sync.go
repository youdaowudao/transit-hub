package user_activity

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

func dayAt(t time.Time) string { return businesstime.DateAt(t) }
func shift(day string, n int) string {
	t, _, _ := businesstime.Bounds(day)
	return t.AddDate(0, 0, n).Format(DateLayout)
}
func covered(from, to, left, right string) bool {
	return from != "" && to != "" && from <= left && to >= right
}

// New verified ranges replace a disconnected old range; gaps are never bridged.
func mergeCoverage(from, to, left, right string) (string, string) {
	if left == "" || right == "" || left > right {
		return from, to
	}
	if from != "" && to != "" && shift(to, 1) >= left && shift(right, 1) >= from {
		if from < left {
			left = from
		}
		if to > right {
			right = to
		}
	}
	return left, right
}
func (s *Service) fetchInventory(ctx context.Context, p *pacedReader, session upstream.Session) ([]upstream.Sub2APIAdminUser, error) {
	out := []upstream.Sub2APIAdminUser{}
	seen := map[string]bool{}
	expected := -1
	for page := 1; page <= 501; page++ {
		result, e := call(ctx, p, func(c context.Context) (upstream.Sub2APIAdminUsersPage, error) {
			return s.platform.FetchSub2APIAdminUsersPageWithContext(c, session, upstream.Sub2APIAdminUsersQuery{Page: page, PageSize: 100, SortBy: "id", SortOrder: "asc", Timezone: businesstime.Timezone, StrictPagination: true})
		})
		if e != nil {
			return nil, e
		}
		if !result.TotalKnown || result.Page != page || result.PageSize != 100 || result.Total > 50000 || result.Total < 0 {
			return nil, fail("用户分页缺少完整数量、页号冲突或用户超过 50000，尚未更新库存")
		}
		if expected < 0 {
			expected = result.Total
		}
		if expected != result.Total {
			return nil, fail("读取期间用户总数发生变化，尚未更新库存")
		}
		for _, u := range result.Items {
			if u.ID == "" || seen[u.ID] {
				return nil, fail("用户清单存在缺失或重复编号，尚未更新库存")
			}
			seen[u.ID] = true
			out = append(out, u)
		}
		if len(out) == expected {
			if page != max(1, (expected+99)/100) {
				return nil, fail("用户清单最后页与总数不一致")
			}
			return out, nil
		}
		if len(result.Items) != 100 || len(out) > expected {
			return nil, fail("用户清单提前结束或数量冲突，尚未更新库存")
		}
	}
	return nil, fail("用户清单超过 501 页读取上限，尚未更新库存")
}
func (s *Service) usageDay(ctx context.Context, p *pacedReader, session upstream.Session, day, today string, users []upstream.Sub2APIAdminUser) ([]DailyUsage, error) {
	points, e := call(ctx, p, func(c context.Context) ([]upstream.Sub2APIUserUsageTrendPoint, error) {
		return s.platform.FetchSub2APIUserUsageTrendContext(c, session, day, day, len(users)+1000)
	})
	if e != nil {
		return nil, e
	}
	stats, e := call(ctx, p, func(c context.Context) (upstream.Sub2APIActivityUsageStats, error) {
		return s.platform.FetchSub2APIActivityUsageStatsContext(c, session, day)
	})
	if e != nil {
		return nil, e
	}
	rows := make([]DailyUsage, 0, len(points))
	var requests int64
	var cost float64
	for _, v := range points {
		requests += v.Requests
		cost += v.ActualCost
		rows = append(rows, DailyUsage{v.UserID, day, v.Requests, v.Tokens, v.ActualCost})
	}
	if day != today {
		if requests != stats.Requests || math.Abs(cost-stats.ActualCost) > 0.000001 {
			return nil, fail("用户用量与同日主站汇总不一致，保留这一天原资料")
		}
		if requests == 0 && cost == 0 {
			return nil, fail("主站返回这一天没有记录，但无法确认历史是否仍完整保留；暂不用于完整期间比较。")
		}
	}

	return rows, nil
}
func (s *Service) collect(ctx context.Context, conn *pgxpool.Conn, scope Scope, previous, st SyncState) {
	ctx, cancelTask := context.WithCancel(ctx)
	defer cancelTask()
	today := dayAt(*st.StartedAt)
	p := &pacedReader{}
	complete := true
	recordError := func(step, object string, e error) {
		if errors.Is(e, workspacescope.ErrChanged) {
			cancelTask()
		}
		complete = false
		st.Error = failure(step, object, e)
		joinWarnings(&st, st.Error)
	}
	session, exists, sessionErr := s.sessions.StoredSession(ctx, scope.UserID, scope.AdminAccountID)
	if sessionErr == nil && (!exists || !session.IsAuthenticated()) {
		sessionErr = fail("主站登录资料不可用，请到管理员账号入口重新连接后重试")
	}
	if sessionErr == nil && session.Platform != upstream.PlatformSub2API {
		sessionErr = fail("用户分析仅支持Sub2API主站，请切换管理员账号后重试")
	}
	if sessionErr != nil {
		recordError("主站登录资料", scope.AdminAccountID, sessionErr)
		st.Status = "failed"
		terminalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = publish(terminalCtx, conn, scope, func(tx pgx.Tx) error { return writeSync(terminalCtx, tx, scope, st) })
		return
	}
	users, e := s.fetchInventory(ctx, p, session)
	inventoryOK := e == nil
	if inventoryOK {
		e = publishInventory(ctx, conn, scope, users, *st.StartedAt)
		if e != nil {
			cancelTask()
		}
		inventoryOK = e == nil
	}
	if !inventoryOK {
		recordError("用户清单", "完整库存", e)
	} else {
		st.UserCount = len(users)
	}
	if inventoryOK {
		attempted := map[string]bool{}
		freshOK := map[string]bool{}
		readDay := func(day string) bool {
			attempted[day] = true
			rows, e := s.usageDay(ctx, p, session, day, today, users)
			if e != nil {
				recordError("每日用量", day, e)
				return false
			}
			next := st
			if day != today {
				next.UsageFrom, next.UsageTo = mergeCoverage(st.UsageFrom, st.UsageTo, day, day)
			} else {
				// A successful full daily observation distinguishes temporary zero from unknown.
				next.TodayUsageObservedAt = st.StartedAt
			}
			if e = publishUsage(ctx, conn, scope, day, rows, next); e != nil {
				cancelTask()
				recordError("保存每日用量", day, e)
				return false
			}
			st = next
			freshOK[day] = true
			return true
		}
		readDay(shift(today, -2))
		readDay(shift(today, -1))
		readDay(today)
		// Extend only the latest verified closed segment, never jump over an unknown day.
		budget := 31
		if previous.UsageFrom == "" && previous.UsageTo == "" {
			budget = 60
		}
		left := st.UsageFrom
		if left == "" || st.UsageTo < shift(today, -90) {
			left = shift(today, -1)
		}
		for n, day := 0, shift(left, -1); n < budget && day >= shift(today, -90); n, day = n+1, shift(day, -1) {
			if attempted[day] {
				break
			}
			if !readDay(day) {
				break
			}
		}
		if !freshOK[shift(today, -2)] || !freshOK[shift(today, -1)] {
			complete = false
		}
	}
	// Funding sources are independent from inventory and usage publication.
	collector := newRechargeCollector(s, p, session)
	from := shift(today, -2)
	if previous.RechargeFrom == "" && previous.RechargeTo == "" {
		from = shift(today, -62)
	}
	publishWindow := func(left, right string) bool {
		rows, channels, all, warning, e := collector.window(ctx, left, right)
		if warning != "" {
			joinWarnings(&st, warning)
		}
		if e != nil {
			recordError("入账记录", left+"至"+shift(right, -1), e)
		}
		if !all {
			complete = false
		}
		if len(channels) == 0 {
			return false
		}
		next := st
		closedTo := shift(right, -1)
		if closedTo >= today {
			closedTo = shift(today, -1)
		}
		if all && left <= closedTo {
			next.RechargeFrom, next.RechargeTo = mergeCoverage(st.RechargeFrom, st.RechargeTo, left, closedTo)
		}
		if e = publishRechargeSources(ctx, conn, scope, left, right, channels, rows, next); e != nil {
			cancelTask()
			recordError("保存入账", left, e)
			return false
		}
		st = next
		return all
	}
	// Publish recent closed dates before the temporary current day and older backfill.
	if from < today {
		publishWindow(from, today)
	}
	publishWindow(today, shift(today, 1))
	if previous.RechargeFrom != "" {
		left := st.RechargeFrom
		if left == "" || st.RechargeTo < shift(today, -400) {
			left = shift(today, -2)
		}
		older := shift(left, -31)
		if older < shift(today, -399) {
			older = shift(today, -399)
		}
		if older < left {
			publishWindow(older, left)
		}
	}
	retentionDay := businesstime.Today()
	clipCoverage(&st, shift(retentionDay, -399))
	if e = retainHistory(ctx, conn, scope, retentionDay, st); e != nil {
		recordError("清理过期分析资料", scope.AdminAccountID, e)
	}
	if e = ctx.Err(); e != nil {
		complete = false
		if st.Error == "" {
			recordError("本轮读取", scope.AdminAccountID, e)
		}
	}
	st.Status = "failed"
	if complete {
		st.Status = "success"
		st.Error = ""
		now := time.Now()
		st.LastSyncedAt = &now
	} else if st.Error == "" {
		st.Error = "本轮部分资料尚未核实，已核实部分可能已经更新；请查看具体提示后重新同步。"
	}
	terminalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = publish(terminalCtx, conn, scope, func(tx pgx.Tx) error { return writeSync(terminalCtx, tx, scope, st) })
}
func publishRechargeSources(ctx context.Context, db workspacescope.Beginner, s Scope, from, to string, channels []string, rows []Recharge, st SyncState) error {
	return publish(ctx, db, s, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `DELETE
        FROM user_activity_recharge_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND day>=$3::date
        AND day<$4::date
        AND channel=ANY($5::text[])`, s.UserID, s.AdminAccountID, from, to, channels); e != nil {
			return e
		}
		for _, r := range rows {
			if _, e := tx.Exec(ctx, `INSERT INTO user_activity_recharge_daily(user_id,
        admin_account_id,
        site_user_id,
        day,
        channel,
        amount,
        events)
        VALUES($1,
        $2,
        $3,
        $4::date,
        $5,
        $6,
        $7)`, s.UserID, s.AdminAccountID, r.SiteUserID, r.Day, r.Channel, r.Amount, r.Events); e != nil {
				return e
			}
		}
		return writeSync(ctx, tx, s, st)
	})
}

// Retention must never leave coverage claiming that deleted dates are complete.
func clipCoverage(st *SyncState, first string) {
	clip := func(from, to *string) {
		if *to == "" || *to < first {
			*from = ""
			*to = ""
			return
		}
		if *from < first {
			*from = first
		}
	}
	clip(&st.UsageFrom, &st.UsageTo)
	clip(&st.RechargeFrom, &st.RechargeTo)
}
