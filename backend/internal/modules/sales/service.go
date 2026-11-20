package sales

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

var channels = []string{"payment", "redeem", "admin_add", "admin_deduct", "affiliate"}

var ErrInvalid = errors.New("销售入账请求不正确；请检查日期、渠道和所选明细后重试。")

type requestError struct{ reason string }

func (e *requestError) Error() string        { return e.reason }
func (e *requestError) Is(target error) bool { return target == ErrInvalid }

func invalid(reason string) error {
	return &requestError{reason: reason}
}

func contains(values []string, value string) bool {
	for _, known := range values {
		if known == value {
			return true
		}
	}
	return false
}

func shift(day string, days int) string {
	start, _, _ := businesstime.Bounds(day)
	return start.AddDate(0, 0, days).Format("2006-01-02")
}

func ResolveRange(query RangeQuery, today string) (DayRange, error) {
	if (query.From == "") != (query.To == "") {
		return DayRange{}, invalid("开始日期和结束日期必须同时填写；请补齐后重新读取。")
	}
	if query.From == "" {
		if query.Days == 0 {
			query.Days = 30
		}
		if query.Days < 1 || query.Days > 400 {
			return DayRange{}, invalid("统计天数必须是1至400的整数；请选择7、30、90天或有效的自定义日期。")
		}
		return DayRange{From: shift(today, -query.Days), To: shift(today, -1), Days: query.Days}, nil
	}
	if query.Days != 0 {
		return DayRange{}, invalid("自定义日期和天数不能同时提交；请只使用一种日期选择方式。")
	}
	from, err := parseDay(query.From)
	if err != nil {
		return DayRange{}, invalid("开始日期格式不正确；请使用有效的年-月-日重新选择。")
	}
	to, err := parseDay(query.To)
	if err != nil {
		return DayRange{}, invalid("结束日期格式不正确；请使用有效的年-月-日重新选择。")
	}
	days := int(to.Sub(from)/(24*time.Hour)) + 1
	if query.From > query.To || query.To >= today || days < 1 || days > 400 {
		return DayRange{}, invalid("统计范围须为1至400个已结束的自然日，结束日期最晚为昨天；请调整日期后重新读取。")
	}
	return DayRange{From: query.From, To: query.To, Days: days}, nil
}

func previousRange(span DayRange) DayRange {
	return DayRange{From: shift(span.From, -span.Days), To: shift(span.From, -1), Days: span.Days}
}

type Service struct {
	repo    *Repository
	apiOnly bool
}

func NewService(repo *Repository, apiOnly bool) *Service {
	return &Service{repo: repo, apiOnly: apiOnly}
}

func (s *Service) Summary(ctx context.Context, scope Scope, span DayRange) (Summary, error) {
	result := Summary{Range: span, PreviousRange: previousRange(span)}
	err := s.repo.snapshot(ctx, scope, func(tx pgx.Tx) error {
		cfg, err := readSettings(ctx, tx, scope)
		if err != nil {
			return err
		}
		state, err := readSync(ctx, tx, scope)
		if err != nil {
			return err
		}
		result.Settings = cfg
		result.Coverage = coverageFor(state, span)
		result.Totals, result.Coverage.HasKnownData, err = readTotals(ctx, tx, scope, span, state)
		if err != nil {
			return err
		}
		if result.Coverage.Complete && result.Coverage.PreviousComplete {
			previous, _, err := readTotals(ctx, tx, scope, result.PreviousRange, state)
			if err != nil {
				return err
			}
			result.Previous = &previous
		}
		result.Channels, err = readChannels(ctx, tx, scope, span, cfg, result.Coverage.HasKnownData)
		if err != nil {
			return err
		}
		result.Daily, err = readDaily(ctx, tx, scope, span, state)
		if err != nil {
			return err
		}
		result.TopPayers, result.Concentration, err = readPayers(ctx, tx, scope, span)
		if err != nil {
			return err
		}
		result.Exclusions, err = readExclusions(ctx, tx, scope, span, cfg, result.Coverage.HasKnownData)
		return err
	})
	return result, err
}

func (s *Service) Entries(ctx context.Context, scope Scope, span DayRange, query EntryQuery) (EntriesResponse, error) {
	result := EntriesResponse{Range: span, Page: query.Page, PageSize: query.PageSize}
	err := s.repo.snapshot(ctx, scope, func(tx pgx.Tx) error {
		cfg, err := readSettings(ctx, tx, scope)
		if err != nil {
			return err
		}
		state, err := readSync(ctx, tx, scope)
		if err != nil {
			return err
		}
		result.Settings = cfg
		result.Coverage = coverageFor(state, span)
		var known bool
		// Coverage is for the whole range, independently of management filters.
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_activity_recharge_daily
            WHERE user_id=$1 AND admin_account_id=$2 AND day BETWEEN $3::date AND $4::date)`,
			scope.UserID, scope.AdminAccountID, span.From, span.To).Scan(&known)
		if err != nil {
			return err
		}
		result.Coverage.HasKnownData = result.Coverage.HasKnownData || known
		result.Items, result.Total, result.Page, err = readEntries(ctx, tx, scope, span, query)
		if err != nil {
			return err
		}
		result.Exclusions, err = readExclusions(ctx, tx, scope, span, cfg, result.Coverage.HasKnownData)
		return err
	})
	return result, err
}

func normalizeChannels(values []string) ([]string, error) {
	result := []string{}
	for _, channel := range values {
		if !contains(channels, channel) {
			return nil, invalid("排除渠道包含不支持的值；只可选择在线支付、兑换、管理员加款、管理员扣款或返利。")
		}
		if !contains(result, channel) {
			result = append(result, channel)
		}
	}
	return result, nil
}

func (s *Service) SaveSettings(ctx context.Context, scope Scope, values []string) (Settings, error) {
	normalized, err := normalizeChannels(values)
	if err != nil {
		return Settings{}, err
	}
	err = workspacescope.Transaction(ctx, s.repo.db, scope, true, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO sales_settings(user_id,admin_account_id,excluded_channels)
            VALUES($1,$2,$3) ON CONFLICT(user_id,admin_account_id)
            DO UPDATE SET excluded_channels=excluded.excluded_channels`, scope.UserID, scope.AdminAccountID, normalized)
		return err
	})
	return Settings{ExcludedChannels: normalized}, err
}

func normalizeEntries(keys []EntryKey, today string) ([]EntryKey, DayRange, error) {
	if len(keys) < 1 || len(keys) > 100 {
		return nil, DayRange{}, invalid("每次须选择1至100条明细；请调整选择后重试。")
	}
	result := []EntryKey{}
	seen := map[EntryKey]bool{}
	span := DayRange{}
	for _, key := range keys {
		if key.SiteUserID == "" || utf8.RuneCountInString(key.SiteUserID) > 128 || !contains(channels, key.Channel) {
			return nil, DayRange{}, invalid("所选明细的客户编号为空、超过128字或渠道无效；请重新读取明细后选择。")
		}
		if _, err := parseDay(key.Day); err != nil || key.Day > today {
			return nil, DayRange{}, invalid("所选明细的日期无效或晚于今天；请重新读取本地明细后选择。")
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, key)
		if span.From == "" || key.Day < span.From {
			span.From = key.Day
		}
		if span.To == "" || key.Day > span.To {
			span.To = key.Day
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SiteUserID != result[j].SiteUserID {
			return result[i].SiteUserID < result[j].SiteUserID
		}
		if result[i].Day != result[j].Day {
			return result[i].Day < result[j].Day
		}
		return result[i].Channel < result[j].Channel
	})
	start, _ := parseDay(span.From)
	end, _ := parseDay(span.To)
	span.Days = int(end.Sub(start)/(24*time.Hour)) + 1
	return result, span, nil
}

func (s *Service) ChangeExclusions(ctx context.Context, scope Scope, keys []EntryKey, restore bool) (MutationResult, error) {
	unique, span, err := normalizeEntries(keys, businesstime.Today())
	if err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{Requested: len(unique), Range: span}
	err = workspacescope.Transaction(ctx, s.repo.db, scope, true, func(tx pgx.Tx) error {
		snapshots := make([]rawEntry, 0, len(unique))
		if !restore {
			// Validate and lock the full selection before writing any exclusion snapshot.
			for _, key := range unique {
				item := rawEntry{Key: key}
				err := tx.QueryRow(ctx, `SELECT amount,events FROM user_activity_recharge_daily
                    WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3 AND day=$4::date AND channel=$5 FOR SHARE`,
					scope.UserID, scope.AdminAccountID, key.SiteUserID, key.Day, key.Channel).Scan(&item.Amount, &item.Events)
				if errors.Is(err, pgx.ErrNoRows) {
					return invalid(fmt.Sprintf("客户%s在%s的所选入账已不存在；整批没有排除，请重新读取后选择。", key.SiteUserID, key.Day))
				}
				if err != nil {
					return err
				}
				if item.Amount <= 0 || math.IsNaN(item.Amount) || math.IsInf(item.Amount, 0) || item.Events < 0 {
					return invalid(fmt.Sprintf("客户%s在%s的所选记录不是有效正向入账；整批没有排除，请只选正向记录。", key.SiteUserID, key.Day))
				}
				snapshots = append(snapshots, item)
			}
		}
		if restore {
			for _, key := range unique {
				tag, err := tx.Exec(ctx, `DELETE FROM sales_excluded_entries
                    WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3 AND day=$4::date AND channel=$5`,
					scope.UserID, scope.AdminAccountID, key.SiteUserID, key.Day, key.Channel)
				if err != nil {
					return err
				}
				result.Changed += int(tag.RowsAffected())
			}
		} else {
			for _, item := range snapshots {
				tag, err := tx.Exec(ctx, `INSERT INTO sales_excluded_entries(user_id,admin_account_id,site_user_id,day,channel,amount,events)
                    VALUES($1,$2,$3,$4::date,$5,$6,$7) ON CONFLICT(user_id,admin_account_id,site_user_id,day,channel) DO NOTHING`,
					scope.UserID, scope.AdminAccountID, item.Key.SiteUserID, item.Key.Day, item.Key.Channel, item.Amount, item.Events)
				if err != nil {
					return err
				}
				result.Changed += int(tag.RowsAffected())
			}
		}
		return nil
	})
	result.Unchanged = result.Requested - result.Changed
	if err != nil {
		return result, err
	}
	// A post-commit refresh failure cannot turn a successful save into a claim of no save.
	err = s.repo.snapshot(ctx, scope, func(tx pgx.Tx) error {
		cfg, err := readSettings(ctx, tx, scope)
		if err != nil {
			return err
		}
		state, err := readSync(ctx, tx, scope)
		if err != nil {
			return err
		}
		_, known, err := readTotals(ctx, tx, scope, span, state)
		if err != nil {
			return err
		}
		summary, err := readExclusions(ctx, tx, scope, span, cfg, known)
		if err == nil {
			result.Exclusions = &summary
		}
		return err
	})
	if err != nil {
		result.RefreshMessage = "本地选择已保存，但本次选择日期内的排除概览读取未完成：" + publicReason(err) + "请重新读取页面，不要重复提交。"
	}
	return result, nil
}
