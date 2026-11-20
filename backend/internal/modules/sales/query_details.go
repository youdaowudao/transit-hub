package sales

import (
	"context"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func readDaily(ctx context.Context, db reader, scope Scope, span DayRange, state syncState) ([]DailyPoint, error) {
	rows, err := db.Query(ctx, `
        WITH src AS (`+rechargeSource()+`)
        SELECT day::text,
               sum(effective_amount) FILTER(WHERE raw_amount>0),
               -sum(effective_amount) FILTER(WHERE raw_amount<0)
        FROM src WHERE day BETWEEN $3::date AND $4::date GROUP BY day ORDER BY day
    `, scope.UserID, scope.AdminAccountID, span.From, span.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]DailyPoint{}
	for rows.Next() {
		var item DailyPoint
		if err := rows.Scan(&item.Day, &item.Credits, &item.Deductions); err != nil {
			return nil, err
		}
		values[item.Day] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]DailyPoint, 0, span.Days)
	for day := span.From; day <= span.To; day = shift(day, 1) {
		item := values[day]
		item.Day = day
		item.Complete = state.From != "" && state.To != "" && state.From <= day && state.To >= day
		if item.Complete {
			if item.Credits == nil {
				item.Credits = new(float64)
			}
			if item.Deductions == nil {
				item.Deductions = new(float64)
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func readPayers(ctx context.Context, db reader, scope Scope, span DayRange) ([]Payer, Concentration, error) {
	rows, err := db.Query(ctx, `
        WITH src AS (`+rechargeSource()+`), payers AS (
            SELECT site_user_id,sum(effective_amount) AS credits,count(DISTINCT day) AS paid_days,max(day)::text AS last_day
            FROM src WHERE day BETWEEN $3::date AND $4::date AND effective_amount>0 GROUP BY site_user_id
        ), ranked AS (
            SELECT *,row_number() OVER(ORDER BY credits DESC,site_user_id) AS rank,
                   sum(credits) OVER() AS total FROM payers
        )
        SELECT r.site_user_id,COALESCE(p.email,''),COALESCE(p.username,''),r.credits,r.paid_days,r.last_day,r.total,r.rank
        FROM ranked r
        LEFT JOIN user_activity_profiles p ON p.user_id=$1 AND p.admin_account_id=$2 AND p.site_user_id=r.site_user_id
        WHERE r.rank<=20 ORDER BY r.rank
    `, scope.UserID, scope.AdminAccountID, span.From, span.To)
	if err != nil {
		return nil, Concentration{}, err
	}
	defer rows.Close()
	result := []Payer{}
	concentration := Concentration{}
	var total, top1, top10 float64
	for rows.Next() {
		var item Payer
		var rank int64
		if err := rows.Scan(&item.SiteUserID, &item.Email, &item.Username, &item.Credits, &item.PaidDays, &item.LastDay, &total, &rank); err != nil {
			return nil, concentration, err
		}
		if rank == 1 {
			top1 = item.Credits
		}
		if rank <= 10 {
			top10 += item.Credits
		}
		result = append(result, item)
	}
	if total > 0 {
		first, tenth := top1/total, top10/total
		concentration.Top1Share, concentration.Top10Share = &first, &tenth
	}
	return result, concentration, rows.Err()
}

func readEntries(ctx context.Context, db reader, scope Scope, span DayRange, query EntryQuery) ([]Entry, int64, int, error) {
	const filter = `
        FROM src r
        LEFT JOIN user_activity_profiles p ON p.user_id=$1 AND p.admin_account_id=$2 AND p.site_user_id=r.site_user_id
        WHERE r.day BETWEEN $3::date AND $4::date AND ($5='' OR r.channel=$5)
          AND ($6='' OR position(lower($6) in lower(r.site_user_id||' '||COALESCE(p.email,'')||' '||COALESCE(p.username,'')))>0)
          AND (NOT $7 OR r.has_exclusion OR r.channel_excluded)
    `
	args := []any{scope.UserID, scope.AdminAccountID, span.From, span.To, query.Channel, query.Search, query.ExcludedOnly}
	var total int64
	if err := db.QueryRow(ctx, `WITH src AS (`+rechargeSource()+`) SELECT count(*) `+filter, args...).Scan(&total); err != nil {
		return nil, 0, query.Page, err
	}
	lastPage := (total + int64(query.PageSize) - 1) / int64(query.PageSize)
	if lastPage < 1 {
		lastPage = 1
	}
	if int64(query.Page) > lastPage {
		query.Page = int(lastPage)
	}
	offset := (int64(query.Page) - 1) * int64(query.PageSize)
	args = append(args, query.PageSize, offset)
	rows, err := db.Query(ctx, `
        WITH src AS (`+rechargeSource()+`)
        SELECT r.site_user_id,r.day::text,r.channel,COALESCE(p.email,''),COALESCE(p.username,''),
               r.raw_amount,r.events,
               CASE WHEN r.raw_amount<0 AND r.channel_excluded THEN r.raw_amount ELSE r.excluded_amount END,
               r.excluded_events,r.effective_amount,r.has_exclusion,r.channel_excluded
    `+filter+`
        ORDER BY r.day DESC,r.raw_amount DESC,r.site_user_id,r.channel LIMIT $8 OFFSET $9
    `, args...)
	if err != nil {
		return nil, 0, query.Page, err
	}
	defer rows.Close()
	result := []Entry{}
	for rows.Next() {
		var item Entry
		err := rows.Scan(&item.SiteUserID, &item.Day, &item.Channel, &item.Email, &item.Username,
			&item.RawAmount, &item.Events, &item.ExcludedAmount, &item.ExcludedEvents, &item.EffectiveAmount, &item.HasExclusion, &item.ChannelExcluded)
		if err != nil {
			return nil, 0, query.Page, err
		}
		switch {
		case item.ChannelExcluded:
			item.ExclusionStatus = "channel"
		case item.HasExclusion && item.RawAmount > 0 && item.EffectiveAmount > 0:
			item.ExclusionStatus = "partial"
		case item.HasExclusion && item.RawAmount > 0:
			item.ExclusionStatus = "excluded"
		default:
			item.ExclusionStatus = "included"
		}
		result = append(result, item)
	}
	return result, total, query.Page, rows.Err()
}

// rawEntry is read and locked only for a server-side exclusion snapshot.
type rawEntry struct {
	Key    EntryKey
	Amount float64
	Events int
}

func parseDay(value string) (time.Time, error) {
	start, _, err := businesstime.Bounds(value)
	return start, err
}
