package sales

import (
	"context"
	"strings"

	"transithub/backend/internal/shared/businesstime"
)

func covered(state syncState, span DayRange) bool {
	return state.From != "" && state.To != "" && state.From <= span.From && state.To >= span.To
}

func coverageFor(state syncState, span DayRange) Coverage {
	result := Coverage{From: state.From, To: state.To, Complete: covered(state, span),
		PreviousComplete: covered(state, previousRange(span)), LastSyncedAt: state.LastSyncedAt, Warnings: []string{}}
	result.HasKnownData = state.From != "" && state.To != "" && state.From <= span.To && state.To >= span.From
	if state.Warning != "" {
		result.Warnings = strings.Split(state.Warning, "\n")
	}
	return result
}

func readTotals(ctx context.Context, db reader, scope Scope, span DayRange, state syncState) (Totals, bool, error) {
	start, _, err := businesstime.Bounds(span.From)
	if err != nil {
		return Totals{}, false, err
	}
	end, _, err := businesstime.Bounds(span.To)
	if err != nil {
		return Totals{}, false, err
	}
	var credits, deductions *float64
	var count, payers, repeat, newPayers, registered, registeredPaid int64
	var inventoryKnown bool
	err = db.QueryRow(ctx, `
        WITH src AS (`+rechargeSource()+`), selected AS (
            SELECT * FROM src WHERE day BETWEEN $3::date AND $4::date
        ), payers AS (
            SELECT site_user_id,count(DISTINCT day) AS paid_days
            FROM selected WHERE effective_amount>0 GROUP BY site_user_id
        ), first_credit AS (
            SELECT site_user_id,min(day) AS first_day
            FROM src WHERE effective_amount>0 AND day>=NULLIF($7,'')::date AND day<=$4::date GROUP BY site_user_id
        ), registered AS (
            SELECT site_user_id FROM user_activity_profiles
            WHERE user_id=$1 AND admin_account_id=$2 AND role='user' AND status<>'disabled'
              AND site_created_at>=$5 AND site_created_at<$6
        )
        SELECT (SELECT count(*) FROM selected),
               (SELECT sum(effective_amount) FROM selected WHERE raw_amount>0),
               (SELECT -sum(effective_amount) FROM selected WHERE raw_amount<0),
               (SELECT count(*) FROM payers),
               (SELECT count(*) FROM payers WHERE paid_days>=2),
               (SELECT count(*) FROM first_credit WHERE first_day>=$3::date),
               (SELECT count(*) FROM registered),
               (SELECT count(*) FROM registered p WHERE EXISTS (
                   SELECT 1 FROM selected r WHERE r.site_user_id=p.site_user_id AND r.effective_amount>0)),
               EXISTS(SELECT 1 FROM user_activity_site_daily WHERE user_id=$1 AND admin_account_id=$2)
    `, scope.UserID, scope.AdminAccountID, span.From, span.To, start, end.AddDate(0, 0, 1), state.From).Scan(
		&count, &credits, &deductions, &payers, &repeat, &newPayers, &registered, &registeredPaid, &inventoryKnown)
	if err != nil {
		return Totals{}, false, err
	}
	complete := covered(state, span)
	known := coverageFor(state, span).HasKnownData || count > 0
	result := Totals{}
	if inventoryKnown {
		result.Registered = &registered
	}
	if complete {
		if credits == nil {
			credits = new(float64)
		}
		if deductions == nil {
			deductions = new(float64)
		}
	}
	result.Credits, result.Deductions = credits, deductions
	if credits != nil {
		result.Payers = &payers
		if payers > 0 {
			average := *credits / float64(payers)
			result.AveragePerPayer = &average
		}
	}
	if complete {
		result.RepeatPayers = &repeat
		if inventoryKnown {
			result.RegisteredPaid = &registeredPaid
			if registered > 0 {
				conversion := float64(registeredPaid) / float64(registered)
				result.Conversion = &conversion
			}
		}
		if state.From < span.From {
			result.NewPayers = &newPayers
		}
	}
	return result, known, nil
}

func readExclusions(ctx context.Context, db reader, scope Scope, span DayRange, cfg Settings, complete bool) (ExclusionSummary, error) {
	result := ExclusionSummary{Channels: cfg.ExcludedChannels}
	err := db.QueryRow(ctx, `
        WITH src AS (`+rechargeSource()+`)
        SELECT count(*) FILTER(WHERE has_exclusion AND NOT channel_excluded),
               sum(excluded_amount) FILTER(WHERE raw_amount>0),
               sum(effective_amount-raw_amount) FILTER(WHERE raw_amount<0)
        FROM src WHERE day BETWEEN $3::date AND $4::date
    `, scope.UserID, scope.AdminAccountID, span.From, span.To).Scan(&result.Entries, &result.Amount, &result.Deductions)
	if err != nil {
		return result, err
	}
	if complete {
		if result.Amount == nil {
			result.Amount = new(float64)
		}
		if result.Deductions == nil {
			result.Deductions = new(float64)
		}
	}
	return result, nil
}

func readChannels(ctx context.Context, db reader, scope Scope, span DayRange, cfg Settings, complete bool) ([]ChannelTotal, error) {
	rows, err := db.Query(ctx, `
        WITH src AS (`+rechargeSource()+`)
        SELECT channel,sum(raw_amount),sum(raw_amount-effective_amount),sum(effective_amount)
        FROM src WHERE day BETWEEN $3::date AND $4::date GROUP BY channel
    `, scope.UserID, scope.AdminAccountID, span.From, span.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]ChannelTotal{}
	for rows.Next() {
		var item ChannelTotal
		var raw, excluded, effective float64
		if err := rows.Scan(&item.Channel, &raw, &excluded, &effective); err != nil {
			return nil, err
		}
		item.RawAmount, item.ExcludedAmount, item.EffectiveAmount = &raw, &excluded, &effective
		values[item.Channel] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := []ChannelTotal{}
	for _, channel := range channels {
		item, exists := values[channel]
		item.Channel = channel
		item.ChannelExcluded = contains(cfg.ExcludedChannels, channel)
		if !exists && complete {
			zero := float64(0)
			item.RawAmount, item.ExcludedAmount, item.EffectiveAmount = &zero, &zero, &zero
		}
		result = append(result, item)
	}
	return result, nil
}
