package user_activity

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

// Each response reads the same published rows and one captured business day.
// The write repository keeps its original pool and publication paths.
type activityReader interface {
	rowReader
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type activityRead struct{ db activityReader }

func (r *Repository) readSnapshot(ctx context.Context, scope Scope, fn func(*activityRead, time.Time) error) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	now := time.Now().In(businesstime.Location())
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := workspacescope.Resolve(ctx, tx, scope); err != nil {
		return err
	}
	return fn(&activityRead{db: tx}, now)
}
func (s *Service) Summary(ctx context.Context, scope Scope, days int) (Summary, error) {
	var result Summary
	err := s.repo.readSnapshot(ctx, scope, func(r *activityRead, now time.Time) error {
		var err error
		result, err = r.summary(ctx, scope, days, now)
		return err
	})
	return result, err
}
func (s *Service) Users(ctx context.Context, scope Scope, query ListQuery) (UserList, error) {
	var result UserList
	err := s.repo.readSnapshot(ctx, scope, func(r *activityRead, now time.Time) error {
		var err error
		result, err = r.usersPage(ctx, scope, query, now)
		return err
	})
	return result, err
}
func (s *Service) Detail(ctx context.Context, scope Scope, id string) (Detail, error) {
	var result Detail
	err := s.repo.readSnapshot(ctx, scope, func(r *activityRead, now time.Time) error {
		var err error
		result, err = r.detail(ctx, scope, id, now)
		return err
	})
	return result, err
}

func pointer[T any](v T) *T { return &v }
func (r *activityRead) users(ctx context.Context, s Scope, st SyncState, id, today string) ([]UserRow, error) {
	rows, e := r.db.Query(ctx, `SELECT p.site_user_id,
        p.email,
        p.username,
        p.role,
        p.status,
        p.balance,
        p.site_created_at,
        p.last_used_at,
        p.first_observed_at,
        p.updated_at,

 COALESCE(sum(d.actual_cost) FILTER(WHERE d.day>=$3::date-7
        AND d.day<$3::date),
        0)::float8,

 COALESCE(sum(d.actual_cost) FILTER(WHERE d.day>=$3::date-30
        AND d.day<$3::date),
        0)::float8,

 COALESCE(sum(d.actual_cost) FILTER(WHERE d.day>=$3::date-14
        AND d.day<$3::date-7),
        0)::float8,

 COALESCE(sum(d.actual_cost) FILTER(WHERE d.day>=$3::date-60
        AND d.day<$3::date-30),
        0)::float8,

 COALESCE(sum(d.requests) FILTER(WHERE d.day>=$3::date-7
        AND d.day<$3::date),
        0)::bigint,

 COALESCE(sum(d.requests) FILTER(WHERE d.day>=$3::date-14
        AND d.day<$3::date-7),
        0)::bigint,

 COALESCE(sum(d.requests) FILTER(WHERE d.day=$3::date),
        0)::bigint,

 COALESCE(sum(d.actual_cost) FILTER(WHERE d.day=$3::date),
        0)::float8,

 count(d.site_user_id) FILTER(WHERE d.day=$3::date)>0
        FROM user_activity_profiles p
        LEFT JOIN user_activity_daily d ON d.user_id=p.user_id
        AND d.admin_account_id=p.admin_account_id
        AND d.site_user_id=p.site_user_id
        AND d.day>=$3::date-60
        AND d.day<=$3::date
        WHERE p.user_id=$1
        AND p.admin_account_id=$2
        AND ($4='' OR p.site_user_id=$4)
        GROUP BY p.user_id,
        p.admin_account_id,
        p.site_user_id
        ORDER BY p.site_user_id`, s.UserID, s.AdminAccountID, today, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		var cost7, cost30, prev7, prev30, costToday float64
		var requests7, requestsPrev7, requestsToday int64
		var todayKnown bool
		if e := rows.Scan(&u.SiteUserID, &u.Email, &u.Username, &u.Role, &u.Status, &u.Balance, &u.CreatedAt, &u.LastUsedAt, &u.FirstObservedAt, &u.ObservedAt, &cost7, &cost30, &prev7, &prev30, &requests7, &requestsPrev7, &requestsToday, &costToday, &todayKnown); e != nil {
			return nil, e
		}
		if covered(st.UsageFrom, st.UsageTo, shift(today, -7), shift(today, -1)) {
			u.Cost7d = pointer(cost7)
			u.Requests7d = pointer(requests7)
		}
		if covered(st.UsageFrom, st.UsageTo, shift(today, -14), shift(today, -8)) {
			u.CostPrev7d = pointer(prev7)
			u.RequestsPrev7d = pointer(requestsPrev7)
		}
		if covered(st.UsageFrom, st.UsageTo, shift(today, -30), shift(today, -1)) {
			u.Cost30d = pointer(cost30)
		}
		if covered(st.UsageFrom, st.UsageTo, shift(today, -60), shift(today, -31)) {
			u.CostPrev30d = pointer(prev30)
		}
		if todayKnown || (st.TodayUsageObservedAt != nil && dayAt(*st.TodayUsageObservedAt) == today) {
			u.TodayUsageObservedAt = st.TodayUsageObservedAt
			u.RequestsToday = pointer(requestsToday)
			u.CostToday = pointer(costToday)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (r *activityRead) daily(ctx context.Context, s Scope, st SyncState, days int, id, today string) ([]DailyPoint, error) {
	from := shift(today, -days+1)
	points := map[string]*DailyPoint{}
	out := make([]DailyPoint, 0, days)
	for day := from; day <= today; day = shift(day, 1) {
		p := DailyPoint{Day: day, Temporary: day == today}
		if day == today && st.TodayUsageObservedAt != nil && dayAt(*st.TodayUsageObservedAt) == today {
			p.ObservedAt = st.TodayUsageObservedAt
		}
		if (day < today && covered(st.UsageFrom, st.UsageTo, day, day)) || (day == today && st.TodayUsageObservedAt != nil && dayAt(*st.TodayUsageObservedAt) == today) {
			p.Requests = pointer(int64(0))
			p.Tokens = pointer(int64(0))
			p.Cost = pointer(float64(0))
			p.ActiveUsers = pointer(0)
		}
		if day < today && covered(st.RechargeFrom, st.RechargeTo, day, day) {
			p.Recharge = pointer(float64(0))
			p.Deduction = pointer(float64(0))
		}
		out = append(out, p)
	}
	for i := range out {
		points[out[i].Day] = &out[i]
	}
	rows, e := r.db.Query(ctx, `SELECT day::text,
        sum(requests)::bigint,
        sum(tokens)::bigint,
        sum(actual_cost)::float8,
        count(DISTINCT site_user_id) FILTER(WHERE requests>0)::int
        FROM user_activity_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND day>=$3::date
        AND day<=$4::date
        AND ($5='' OR site_user_id=$5)
        GROUP BY day
        ORDER BY day`, s.UserID, s.AdminAccountID, from, today, id)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var day string
		var requests, tokens int64
		var cost float64
		var active int
		if e = rows.Scan(&day, &requests, &tokens, &cost, &active); e != nil {
			rows.Close()
			return nil, e
		}
		p := points[day]
		if p != nil {
			p.Requests, p.Tokens, p.Cost, p.ActiveUsers = pointer(requests), pointer(tokens), pointer(cost), pointer(active)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = r.db.Query(ctx, `SELECT day::text,
        sum(amount) FILTER(WHERE amount>0)::float8,
        (-sum(amount) FILTER(WHERE amount<0))::float8
        FROM user_activity_recharge_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND day>=$3::date
        AND day<=$4::date
        AND ($5='' OR site_user_id=$5)
        GROUP BY day
        ORDER BY day`, s.UserID, s.AdminAccountID, from, today, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var positive, negative *float64
		if e = rows.Scan(&day, &positive, &negative); e != nil {
			return nil, e
		}
		p := points[day]
		if p != nil {
			if positive != nil {
				p.Recharge = positive
			}
			if negative != nil {
				p.Deduction = negative
			}
		}
	}
	return out, rows.Err()
}
func (r *activityRead) metrics(ctx context.Context, s Scope, st SyncState, from, to, id string) (*Metrics, error) {
	if !covered(st.UsageFrom, st.UsageTo, from, to) {
		return nil, nil
	}
	m := &Metrics{From: from, To: to}
	e := r.db.QueryRow(ctx, `SELECT COALESCE(sum(requests),
        0)::bigint,
        COALESCE(sum(tokens),
        0)::bigint,
        COALESCE(sum(actual_cost),
        0)::float8,
        count(DISTINCT site_user_id) FILTER(WHERE requests>0)::int
        FROM user_activity_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND day>=$3::date
        AND day<=$4::date
        AND ($5='' OR site_user_id=$5)`, s.UserID, s.AdminAccountID, from, to, id).Scan(&m.Requests, &m.Tokens, &m.Cost, &m.ActiveUsers)
	return m, e
}
func (r *activityRead) periods(ctx context.Context, s Scope, st SyncState, id, today string) (map[string]Period, error) {
	out := map[string]Period{}
	for _, entry := range []struct {
		name string
		days int
	}{{"week", 7}, {"month", 30}} {
		p := Period{Days: entry.days}
		var e error
		p.Current, e = r.metrics(ctx, s, st, shift(today, -entry.days), shift(today, -1), id)
		if e != nil {
			return nil, e
		}
		p.Previous, e = r.metrics(ctx, s, st, shift(today, -entry.days*2), shift(today, -entry.days-1), id)
		if e != nil {
			return nil, e
		}
		if p.Current != nil && p.Previous != nil {
			ret := Retention{}
			e = r.db.QueryRow(ctx, `SELECT count(*)::int
        FROM (SELECT site_user_id
        FROM user_activity_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND ($6='' OR site_user_id=$6)
        AND day>=$3::date
        AND day<$4::date
        AND requests>0 INTERSECT SELECT site_user_id
        FROM user_activity_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND ($6='' OR site_user_id=$6)
        AND day>=$4::date
        AND day<$5::date
        AND requests>0) retained`, s.UserID, s.AdminAccountID, shift(today, -entry.days*2), shift(today, -entry.days), today, id).Scan(&ret.Retained)
			if e != nil {
				return nil, e
			}
			ret.Churned = p.Previous.ActiveUsers - ret.Retained
			ret.Gained = p.Current.ActiveUsers - ret.Retained
			p.Retention = &ret
		}
		out[entry.name] = p
	}
	return out, nil
}
func (r *activityRead) summary(ctx context.Context, scope Scope, days int, now time.Time) (Summary, error) {
	today := businesstime.DateAt(now)
	st, e := readSync(ctx, r.db, scope)
	if e != nil {
		return Summary{}, e
	}
	daily, e := r.daily(ctx, scope, st, max(2, days), "", today)
	if e != nil {
		return Summary{}, e
	}
	periods, e := r.periods(ctx, scope, st, "", today)
	if e != nil {
		return Summary{}, e
	}
	money := Money{Channels: []ChannelAmount{}, Complete: covered(st.RechargeFrom, st.RechargeTo, shift(today, -1), shift(today, -1))}
	var observed time.Time
	e = r.db.QueryRow(ctx, `SELECT total_balance,
        user_count,
        observed_at
        FROM user_activity_site_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        ORDER BY day DESC LIMIT 1`, scope.UserID, scope.AdminAccountID).Scan(&money.TotalBalance, &st.UserCount, &observed)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Summary{}, e
	}
	if e == nil {
		money.ObservedAt = &observed
	}
	money.TodayRecharge, money.TodayDeduction = daily[len(daily)-1].Recharge, daily[len(daily)-1].Deduction
	if len(daily) > 1 {
		money.YesterdayRecharge, money.YesterdayDeduction = daily[len(daily)-2].Recharge, daily[len(daily)-2].Deduction
	}
	rows, e := r.db.Query(ctx, `SELECT channel,
        sum(amount)::float8,
        sum(events)::int
        FROM user_activity_recharge_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND day=$3::date
        GROUP BY channel
        ORDER BY channel`, scope.UserID, scope.AdminAccountID, today)
	if e != nil {
		return Summary{}, e
	}
	for rows.Next() {
		var c ChannelAmount
		if e = rows.Scan(&c.Channel, &c.Amount, &c.Events); e != nil {
			rows.Close()
			return Summary{}, e
		}
		money.Channels = append(money.Channels, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return Summary{}, e
	}
	visibleDaily := daily
	if days == 1 {
		visibleDaily = daily[len(daily)-1:]
	}
	return Summary{st, daily[len(daily)-1], periods, visibleDaily, money, coverage(st), st.Warnings, businesstime.Timezone}, nil
}
func (r *activityRead) usersPage(ctx context.Context, scope Scope, q ListQuery, now time.Time) (UserList, error) {
	today := businesstime.DateAt(now)
	st, e := readSync(ctx, r.db, scope)
	if e != nil {
		return UserList{}, e
	}
	users, e := r.users(ctx, scope, st, "", today)
	if e != nil {
		return UserList{}, e
	}
	filtered := make([]UserRow, 0, len(users))
	search := strings.ToLower(q.Search)
	for _, u := range users {
		if q.Role != "" && u.Role != q.Role || q.Status != "" && u.Status != q.Status {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(u.SiteUserID+" "+u.Email+" "+u.Username), search) {
			continue
		}
		match := true
		switch q.Segment {
		case "active_1d", "active_7d", "active_30d":
			days := 1
			if q.Segment == "active_7d" {
				days = 7
			}
			if q.Segment == "active_30d" {
				days = 30
			}
			match = u.LastUsedAt != nil && !u.LastUsedAt.Before(now.AddDate(0, 0, -days))
		case "inactive":
			match = u.LastUsedAt != nil && u.LastUsedAt.Before(now.AddDate(0, 0, -q.InactiveDays))
		case "churned_7d":
			match = u.Requests7d != nil && u.RequestsPrev7d != nil && *u.Requests7d == 0 && *u.RequestsPrev7d > 0
		case "declining":
			match = u.Cost7d != nil && u.CostPrev7d != nil && *u.CostPrev7d > 0 && *u.Cost7d <= *u.CostPrev7d*.5
		case "growing":
			match = u.Cost7d != nil && u.CostPrev7d != nil && *u.CostPrev7d > 0 && *u.Cost7d >= *u.CostPrev7d*1.5
		}
		if match {
			filtered = append(filtered, u)
		}
	}
	less := func(a, b UserRow) int {
		var left, right *float64
		switch q.Sort {
		case "balance":
			left, right = a.Balance, b.Balance
		case "cost7d":
			left, right = a.Cost7d, b.Cost7d
		case "cost30d":
			left, right = a.Cost30d, b.Cost30d
		case "createdAt", "lastUsedAt":
			l, r := a.LastUsedAt, b.LastUsedAt
			if q.Sort == "createdAt" {
				l, r = a.CreatedAt, b.CreatedAt
			}
			if l != nil {
				v := float64(l.UnixNano())
				left = &v
			}
			if r != nil {
				v := float64(r.UnixNano())
				right = &v
			}
		}
		if left == nil && right == nil {
			return strings.Compare(a.SiteUserID, b.SiteUserID)
		}
		if left == nil {
			return 1
		}
		if right == nil {
			return -1
		}
		result := 0
		if *left < *right {
			result = -1
		} else if *left > *right {
			result = 1
		}
		if q.Order == "desc" {
			result = -result
		}
		if result == 0 {
			return strings.Compare(a.SiteUserID, b.SiteUserID)
		}
		return result
	}
	sort.SliceStable(filtered, func(i, j int) bool { return less(filtered[i], filtered[j]) < 0 })
	total := len(filtered)
	start := min(total, (q.Page-1)*q.PageSize)
	end := min(total, start+q.PageSize)
	return UserList{filtered[start:end], total, q.Page, q.PageSize, st, coverage(st)}, nil
}
func (r *activityRead) detail(ctx context.Context, scope Scope, id string, now time.Time) (Detail, error) {
	today := businesstime.DateAt(now)
	st, e := readSync(ctx, r.db, scope)
	if e != nil {
		return Detail{}, e
	}
	users, e := r.users(ctx, scope, st, id, today)
	if e != nil {
		return Detail{}, e
	}
	if len(users) == 0 {
		return Detail{}, ErrNotFound
	}
	daily, e := r.daily(ctx, scope, st, 90, id, today)
	if e != nil {
		return Detail{}, e
	}
	periods, e := r.periods(ctx, scope, st, id, today)
	if e != nil {
		return Detail{}, e
	}
	return Detail{users[0], daily, periods, coverage(st)}, nil
}
