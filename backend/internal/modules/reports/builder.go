package reports

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"transithub/backend/internal/modules/dashboard"
	"transithub/backend/internal/modules/sales"
	"transithub/backend/internal/shared/businesstime"
)

type dailySnapshotReader interface {
	ListDailyStats(context.Context, string, string, string, string) ([]dashboard.DailySnapshot, error)
}

type salesReader interface {
	Summary(context.Context, sales.Scope, sales.DayRange) (sales.Summary, error)
}

func shift(day string, days int) string {
	start, _, _ := businesstime.Bounds(day)
	return start.AddDate(0, 0, days).Format("2006-01-02")
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func amount(value *float64) string {
	if value == nil || !finite(*value) {
		return "未取得"
	}
	return fmt.Sprintf("%.2f", *value)
}

func observed(value *time.Time) string {
	if value == nil {
		return "未取得观察时间"
	}
	return value.In(businesstime.Location()).Format("2006-01-02 15:04:05") + "（" + businesstime.Timezone + "）"
}

func businessSection(day string, rows []dashboard.DailySnapshot) Section {
	section := Section{Title: "昨日经营（原结算核算金额）", Date: day, Coverage: Coverage{From: day, To: day}}
	if len(rows) != 1 || rows[0].Date.Format("2006-01-02") != day {
		section.Text = "未取得唯一的昨日原结算记录；请到原逐日明细核查，不用今日金额代替。"
		return section
	}
	row := rows[0]
	section.ObservedAt = row.ObservedAt
	if row.FinalizedAt != nil {
		section.ObservedAt = row.FinalizedAt
	}
	if row.SettlementStatus != "final" || (row.CostQualityMode != "exact" && row.CostQualityMode != "retained") ||
		row.TodayProfit == nil || row.TodayPurchase == nil || row.OperatingCost == nil || row.AdjustedNetProfit == nil ||
		!finite(*row.TodayProfit) || !finite(*row.TodayPurchase) || !finite(*row.OperatingCost) || !finite(*row.AdjustedNetProfit) {
		section.Text = "昨日原结算尚未完整完成，或原金额缺失/不可用，未取得可报告的最终利润；请到原逐日明细查看具体质量。观察时间：" + observed(section.ObservedAt) + "。"
		return section
	}
	section.Coverage.Complete = true
	quality := "完整成本"
	if row.CostQualityMode == "retained" {
		quality = "沿用已核实成本"
	}
	section.Text = "原营收：" + amount(row.TodayProfit) + "；原总成本：" + amount(row.TodayPurchase) +
		"；原经营成本：" + amount(row.OperatingCost) + "；原调整后净利润：" + amount(row.AdjustedNetProfit) +
		"。原结算已完成，" + quality + "。观察时间：" + observed(section.ObservedAt) + "。"
	return section
}

func salesSection(day string, value sales.Summary) Section {
	section := Section{Title: "昨日销售入账", Date: day, ObservedAt: value.Coverage.LastSyncedAt,
		Coverage: Coverage{From: value.Coverage.From, To: value.Coverage.To, Complete: value.Coverage.Complete}}
	if !value.Coverage.HasKnownData {
		section.Text = "未取得这一天的已知入账资料或完整覆盖依据，不能认为零销售；请到销售入账和用户分析核查。"
		return section
	}
	payer := "未取得"
	if value.Totals.Payers != nil {
		payer = fmt.Sprintf("%d位", *value.Totals.Payers)
	}
	section.Text = "正向有效入账：" + amount(value.Totals.Credits) + "；扣款单列：" + amount(value.Totals.Deductions) + "；入账客户：" + payer +
		"。使用销售视图已保存的整渠道和逐行排除规则，不等于按消费计算的营收。"
	if !value.Coverage.Complete {
		section.Text += "仅为已保存部分，日期覆盖不完整，不作完整比较。"
	}
	section.Text += "资料最近完整同步：" + observed(section.ObservedAt) + "。"
	return section
}

func usageSection(day string, value localActivity) Section {
	section := Section{Title: "昨日使用", Date: day, ObservedAt: value.LastSyncedAt,
		Coverage: Coverage{From: value.UsageFrom, To: value.UsageTo, Complete: value.UsageFrom != "" && value.UsageFrom <= day && value.UsageTo >= day}}
	if (!section.Coverage.Complete && value.UsageRows == 0) || !finite(value.Cost) {
		section.Text = "未取得这一天可确认的使用资料，不能按0处理；请到用户分析核查用量覆盖。"
		return section
	}
	section.Text = fmt.Sprintf("请求%d次；Token数%d；已记录消费%.2f；有请求的客户%d位。", value.Requests, value.Tokens, value.Cost, value.ActiveYesterday)
	if !section.Coverage.Complete {
		section.Text += "仅为已保存部分，无法确认昨日完整用量。"
	}
	section.Text += "资料最近完整同步：" + observed(section.ObservedAt) + "。"
	return section
}

func customerSection(day, today string, value localActivity) Section {
	from, to := shift(today, -7), shift(today, -1)
	known7 := value.UsageFrom != "" && value.UsageFrom <= from && value.UsageTo >= to
	known14 := value.UsageFrom != "" && value.UsageFrom <= shift(today, -14) && value.UsageTo >= to
	section := Section{Title: "客户近况（最近同步资料）", Date: day, ObservedAt: value.ObservedAt,
		Coverage: Coverage{From: value.UsageFrom, To: value.UsageTo, Complete: known14}}
	if value.ObservedAt == nil {
		section.Text = "未取得本地用户清单和余额观察时间；请先到用户分析同步。这一段不是昨日余额。"
		return section
	}
	section.Date = businesstime.DateAt(*value.ObservedAt)
	section.Text = fmt.Sprintf("最近清单%d位，非停用普通用户%d位。", value.ProfileCount, value.OrdinaryUsers)
	if value.UnknownBalances > 0 || !finite(value.KnownBalance) {
		section.Text += fmt.Sprintf("普通用户余额合计未取得（%d位余额未知，或合计数值不可用）。", value.UnknownBalances)
	} else {
		section.Text += fmt.Sprintf("这批普通用户的已保存余额合计%.2f。", value.KnownBalance)
	}
	if known7 && finite(value.Cost7) {
		section.Text += fmt.Sprintf("最近7个完整日（%s至%s）有请求的普通用户%d位，已记录消费%.2f。", from, to, value.Active7, value.Cost7)
	} else {
		section.Text += "最近7日用量覆盖不足，未取得完整的客户活跃与消费趋势。"
	}
	if known14 && finite(value.Cost7) && finite(value.CostPrevious7) {
		section.Text += fmt.Sprintf("较前7日消费下降至一半或更少的普通用户%d位。", value.Declining)
		if value.CostPrevious7 > 0 {
			change := value.Cost7/value.CostPrevious7 - 1
			if finite(change * 100) {
				section.Text += fmt.Sprintf("这批用户近7日消费较前7日变化%.1f%%。", change*100)
			}
		}
	} else {
		section.Text += "前后14日尚未连续完整覆盖，不报告两周比较。"
	}
	section.Text += "名单和余额观察时间：" + observed(value.ObservedAt) + "；这是最新已保存近况，不是昨日余额。"
	return section
}

func (s *Service) build(ctx context.Context, scope Scope, now time.Time) (Document, error) {
	if s.metrics == nil || s.sales == nil || s.notifier == nil {
		return Document{}, ErrUnavailable
	}
	if err := verifyParent(ctx, s.repo.db, scope); err != nil {
		return Document{}, err
	}
	today := businesstime.DateAt(now)
	day := shift(today, -1)
	rows, err := s.metrics.ListDailyStats(ctx, scope.UserID, scope.AdminAccountID, day, day)
	if err != nil {
		return Document{}, &stepError{Step: "昨日原结算", Object: day, Cause: err}
	}
	sale, err := s.sales.Summary(ctx, scope, sales.DayRange{From: day, To: day, Days: 1})
	if err != nil {
		return Document{}, &stepError{Step: "昨日销售入账", Object: day, Cause: err}
	}
	activity, err := s.repo.activity(ctx, scope, day, today)
	if err != nil {
		return Document{}, &stepError{Step: "昨日使用与客户近况", Object: scope.AdminAccountID, Cause: err}
	}
	result := Document{BusinessDate: day, ObservedAt: now, Sections: []Section{
		businessSection(day, rows), salesSection(day, sale), usageSection(day, activity), customerSection(day, today, activity),
	}}
	lines := []string{"Transit Hub 经营日报", day + "（" + businesstime.Timezone + "）"}
	for _, section := range result.Sections {
		lines = append(lines, "", section.Title+"：", section.Text)
	}
	lines = append(lines, "", "只读取本站已有资料，不会修改主站、充值或联系客户。")
	result.Text = strings.Join(lines, "\n")
	if err := verifyParent(ctx, s.repo.db, scope); err != nil {
		return Document{}, err
	}
	return result, nil
}
