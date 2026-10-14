package upstream

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/modules/notifications"
)

// RateNoticeObservation is safe local metadata, independent of pricing or cost.
type RateNoticeObservation struct {
	Confirmed  bool      `json:"confirmed"`
	ObservedAt time.Time `json:"observedAt"`
	Reason     string    `json:"reason,omitempty"`
}

// RateNoticeInput is only the current fetch input. Saved observations never grant
// another comparison or permission to delete groups absent from this response.
type RateNoticeInput struct {
	Observation  RateNoticeObservation
	AllowCleanup bool
}

type RateNoticeBinding struct {
	AccountID     string
	AccountName   string
	KeyID         string
	Status        string
	AdminPlatform string
}

type RateNoticeKeyAssignment struct {
	Confirmed   bool
	GroupID     string
	GroupName   string
	ConfirmedAt time.Time
	Reason      string
}

type RateNoticeKeyAssignmentReader interface {
	PeekKeyAssignments(userID, workspaceID, siteID string, mainPlatform Platform, bindings []RateNoticeBinding) map[string]RateNoticeKeyAssignment
}

type rateNoticeWriter interface {
	AppendTx(context.Context, pgx.Tx, notifications.NoticeInput) (string, bool, error)
}

func (r *Repository) SetRateNoticeKeyAssignmentReader(reader RateNoticeKeyAssignmentReader) {
	r.rateNoticeKeyAssignments = reader
}
func (r *Repository) SetRateNoticeWriter(writer rateNoticeWriter) { r.rateNoticeWriter = writer }

func observationFromGroupAudit(audit *GroupSnapshotAudit, observedAt time.Time) *RateNoticeObservation {
	observation := &RateNoticeObservation{ObservedAt: observedAt}
	if audit == nil {
		observation.Reason = "本次没有取得可核验的分组读取结果，请重新同步并核查上游分组列表"
		return observation
	}
	observation.Confirmed = audit.AllowUpdate
	if audit.Issue != "" {
		observation.Reason = safeCostReason(audit.Issue)
	}
	if !observation.Confirmed && observation.Reason == "" {
		observation.Reason = "本次分组倍率未能确认，请重新同步并核查上游分组列表"
	}
	if observation.Confirmed && !audit.AllowCleanup {
		observation.Reason = "已确认返回的倍率，尚不能确认全部分组"
	}
	return observation
}

func currentRateNoticeInput(metrics Metrics, fetchErr error, observedAt time.Time) *RateNoticeInput {
	observation := metrics.RateNoticeObservation
	if observation == nil {
		observation = &RateNoticeObservation{ObservedAt: observedAt, Reason: "本次没有取得可核验的倍率读取结果，请重新同步并核查上游分组列表"}
	}
	input := &RateNoticeInput{Observation: *observation}
	if fetchErr != nil {
		input.Observation.Confirmed = false
		input.Observation.Reason = "读取本站倍率失败：" + costFailureCause(fetchErr) + "；本次保留比较记录，请重新同步并核查上游分组列表"
	}
	if input.Observation.ObservedAt.IsZero() {
		input.Observation.ObservedAt = observedAt
	}
	input.Observation.Reason = safeCostReason(input.Observation.Reason)
	input.AllowCleanup = input.Observation.Confirmed && metrics.GroupSnapshotAudit != nil && metrics.GroupSnapshotAudit.AllowCleanup
	return input
}

func rateNoticeInput(inputs []*RateNoticeInput) *RateNoticeInput {
	if len(inputs) == 0 {
		return nil
	}
	return inputs[0]
}

func setRateNoticeObservation(site *Site, input *RateNoticeInput) {
	if input == nil {
		return
	}
	observation := input.Observation
	observation.Reason = safeCostReason(observation.Reason)
	site.Metrics.RateNoticeObservation = &observation
}

func rateNoticeGroupKey(id, name string) string {
	if id != "" {
		return "id:" + id
	}
	return "name:" + name
}

type rateNoticeBaseline struct {
	id, name   string
	multiplier float64
	observedAt time.Time
}

// ReadRateNoticeBindingsTx reads only nonsecret columns from this site's current
// workspace. A failed local read aborts the original site transaction.
func (r *Repository) ReadRateNoticeBindingsTx(ctx context.Context, tx pgx.Tx, userID, workspaceID, siteID string) ([]RateNoticeBinding, Platform, error) {
	var platform Platform
	if err := tx.QueryRow(ctx, `SELECT platform FROM admin_accounts WHERE user_id=$1 AND id=$2`, userID, workspaceID).Scan(&platform); err != nil {
		return nil, "", fmt.Errorf("核查本站主站平台失败：%s", notifications.SafeStorageCause(err))
	}
	rows, err := tx.Query(ctx, `SELECT admin_account_id,admin_account_name,upstream_key_id,status,admin_platform FROM real_connections WHERE user_id=$1 AND workspace_admin_account_id=$2 AND upstream_site_id=$3 ORDER BY id`, userID, workspaceID, siteID)
	if err != nil {
		return nil, "", fmt.Errorf("读取本站已登记主站接入失败：%s", notifications.SafeStorageCause(err))
	}
	defer rows.Close()
	bindings := make([]RateNoticeBinding, 0)
	for rows.Next() {
		var b RateNoticeBinding
		if err := rows.Scan(&b.AccountID, &b.AccountName, &b.KeyID, &b.Status, &b.AdminPlatform); err != nil {
			return nil, "", fmt.Errorf("核查本站已登记主站接入资料失败：%s", notifications.SafeStorageCause(err))
		}
		if platform == PlatformSub2API && (strings.TrimSpace(b.AdminPlatform) == "" || strings.EqualFold(strings.TrimSpace(b.AdminPlatform), string(PlatformSub2API))) {
			bindings = append(bindings, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("读取本站已登记主站接入未完成：%s", notifications.SafeStorageCause(err))
	}
	return bindings, platform, nil
}

func (r *Repository) recordRateNoticesTx(ctx context.Context, tx pgx.Tx, site Site, input *RateNoticeInput) error {
	if input == nil || !input.Observation.Confirmed {
		return nil
	}
	if input.Observation.ObservedAt.IsZero() {
		return errors.New("本站倍率的发现时间缺失，本次资料未保存，请重新同步网站")
	}
	groups := append([]GroupInfo(nil), site.Metrics.Groups...)
	sort.Slice(groups, func(i, j int) bool {
		return rateNoticeGroupKey(groups[i].ID, groups[i].Name) < rateNoticeGroupKey(groups[j].ID, groups[j].Name)
	})
	keys := make([]string, 0, len(groups))
	seen := make(map[string]bool, len(groups))
	for _, group := range groups {
		key := rateNoticeGroupKey(group.ID, group.Name)
		if group.Name == "" || seen[key] || group.MultiplierMode != "auto" && (group.Multiplier == nil || !finiteNonnegative(*group.Multiplier)) {
			return errors.New("本站已确认的倍率输入缺失、重复或数值无效，本次资料未保存，请重新同步网站")
		}
		seen[key] = true
		keys = append(keys, key)
	}
	rows, err := tx.Query(ctx, `SELECT group_key,group_id,group_name,multiplier,observed_at FROM upstream_rate_notice_baselines WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3`, site.UserID, site.AdminAccountID, site.ID)
	if err != nil {
		return fmt.Errorf("读取本站倍率比较记录失败：%s", notifications.SafeStorageCause(err))
	}
	baselines := make(map[string]rateNoticeBaseline)
	for rows.Next() {
		var key string
		var baseline rateNoticeBaseline
		if err := rows.Scan(&key, &baseline.id, &baseline.name, &baseline.multiplier, &baseline.observedAt); err != nil {
			rows.Close()
			return fmt.Errorf("核查本站倍率比较记录失败：%s", notifications.SafeStorageCause(err))
		}
		baselines[key] = baseline
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return fmt.Errorf("读取本站倍率比较记录未完成：%s", notifications.SafeStorageCause(rowErr))
	}
	var bindings []RateNoticeBinding
	var assignments map[string]RateNoticeKeyAssignment
	bindingsRead := false
	for _, group := range groups {
		key := rateNoticeGroupKey(group.ID, group.Name)
		old, exists := baselines[key]
		if group.MultiplierMode == "auto" {
			if _, err := tx.Exec(ctx, `DELETE FROM upstream_rate_notice_baselines WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 AND group_key=$4`, site.UserID, site.AdminAccountID, site.ID, key); err != nil {
				return fmt.Errorf("清理本站自动倍率组的比较记录失败：%s", notifications.SafeStorageCause(err))
			}
			continue
		}
		if exists && old.multiplier == *group.Multiplier && old.name == group.Name {
			continue
		}
		if exists && old.multiplier != *group.Multiplier {
			if !bindingsRead {
				var platform Platform
				bindings, platform, err = r.ReadRateNoticeBindingsTx(ctx, tx, site.UserID, site.AdminAccountID, site.ID)
				if err != nil {
					return err
				}
				if r.rateNoticeKeyAssignments != nil {
					assignments = r.rateNoticeKeyAssignments.PeekKeyAssignments(site.UserID, site.AdminAccountID, site.ID, platform, bindings)
				}
				bindingsRead = true
			}
			inputNotice, err := rateChangeNotice(site, group, old.multiplier, input.Observation.ObservedAt, bindings, assignments)
			if err != nil {
				return err
			}
			if r.rateNoticeWriter == nil {
				return errors.New("本站倍率通知记录器未装配，本次资料未保存，请维护人员检查服务启动配置")
			}
			if _, _, err = r.rateNoticeWriter.AppendTx(ctx, tx, inputNotice); err != nil {
				return err
			}
		}
		observedAt := input.Observation.ObservedAt
		if exists && old.multiplier == *group.Multiplier {
			observedAt = old.observedAt
		}
		if _, err := tx.Exec(ctx, `INSERT INTO upstream_rate_notice_baselines(user_id,admin_account_id,site_id,group_key,group_id,group_name,multiplier,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(user_id,admin_account_id,site_id,group_key) DO UPDATE SET group_id=EXCLUDED.group_id,group_name=EXCLUDED.group_name,multiplier=EXCLUDED.multiplier,observed_at=EXCLUDED.observed_at`, site.UserID, site.AdminAccountID, site.ID, key, group.ID, group.Name, *group.Multiplier, observedAt); err != nil {
			return fmt.Errorf("保存本站可信倍率比较记录失败：%s", notifications.SafeStorageCause(err))
		}
	}
	if input.AllowCleanup {
		if _, err := tx.Exec(ctx, `DELETE FROM upstream_rate_notice_baselines WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 AND NOT(group_key=ANY($4::text[]))`, site.UserID, site.AdminAccountID, site.ID, keys); err != nil {
			return fmt.Errorf("清理本站不再返回的倍率比较记录失败：%s", notifications.SafeStorageCause(err))
		}
	}
	return nil
}

type rateNoticeClassification struct {
	level          notifications.Level
	description    string
	accountNames   []string
	accountCount   int
	confirmedAt    time.Time
	unknownReasons []string
}

const rateNoticeUnknownScopeDescription = "这个网站已有登记的主站接入，但当前无法确认受影响范围，请进入分组健康核查。"

func classifyRateNotice(group GroupInfo, bindings []RateNoticeBinding, assignments map[string]RateNoticeKeyAssignment) rateNoticeClassification {
	result := rateNoticeClassification{level: notifications.LevelNormal, description: "未发现这个分组的已登记主站接入"}
	accounts := make(map[string][]RateNoticeBinding)
	unclassifiable := false
	for _, b := range bindings {
		if strings.TrimSpace(b.Status) == "missing" {
			continue
		}
		id := strings.TrimSpace(b.AccountID)
		if id == "" {
			unclassifiable = true
			continue
		}
		accounts[id] = append(accounts[id], b)
	}
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result.accountCount = len(accounts)
	matched, unknown := false, false
	reasons := make(map[string]bool)
	for _, id := range ids {
		rows := accounts[id]
		name := strings.TrimSpace(rows[0].AccountName)
		if name == "" {
			name = "未命名主站账号"
		}
		if len(result.accountNames) < 20 {
			result.accountNames = append(result.accountNames, limitRateNoticeText(name, 40))
		}
		var accountKey string
		accountKnown := true
		for _, b := range rows {
			assignment, ok := assignments[strings.TrimSpace(b.KeyID)]
			if !ok || !assignment.Confirmed || assignment.GroupID == "" && assignment.GroupName == "" {
				accountKnown = false
				reason := assignment.Reason
				if reason == "" {
					reason = "关联Key的最近分组归属尚未确认，请核查原关联"
				}
				reasons[safeCostReason(reason)] = true
				continue
			}
			key := rateNoticeGroupKey(assignment.GroupID, assignment.GroupName)
			if accountKey != "" && accountKey != key {
				accountKnown = false
				reasons["同一已登记主站账号关联的Key分组不一致，请核查原关联"] = true
			}
			accountKey = key
			if !assignment.ConfirmedAt.IsZero() && (result.confirmedAt.IsZero() || assignment.ConfirmedAt.Before(result.confirmedAt)) {
				result.confirmedAt = assignment.ConfirmedAt
			}
		}
		if !accountKnown {
			unknown = true
		} else if accountKey == rateNoticeGroupKey(group.ID, group.Name) {
			matched = true
		}
	}
	if matched {
		result.level = notifications.LevelUrgent
		result.description = "最近确认的Key分组归属与变化分组一致，可能影响已登记主站账号"
	} else if unknown {
		result.level = notifications.LevelUrgent
		result.description = rateNoticeUnknownScopeDescription
	} else if unclassifiable {
		result.level = notifications.LevelImportant
		result.description = "接入情况暂未确认，请核查本站已登记资料"
		result.accountCount = -1
	}
	for reason := range reasons {
		result.unknownReasons = append(result.unknownReasons, reason)
	}
	sort.Strings(result.unknownReasons)
	return result
}

func limitRateNoticeText(value string, limit int) string {
	value = safeCostReason(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func rateChangeNotice(site Site, group GroupInfo, old float64, observedAt time.Time, bindings []RateNoticeBinding, assignments map[string]RateNoticeKeyAssignment) (notifications.NoticeInput, error) {
	eventKey, err := notifications.NewID()
	if err != nil {
		return notifications.NoticeInput{}, err
	}
	classification := classifyRateNotice(group, bindings, assignments)
	oldText, newText := strconv.FormatFloat(old, 'g', -1, 64), strconv.FormatFloat(*group.Multiplier, 'g', -1, 64)
	siteName, groupName := limitRateNoticeText(site.Name, 100), limitRateNoticeText(group.Name, 100)
	details := []notifications.Detail{{Label: "网站", Value: siteName}, {Label: "分组", Value: groupName}, {Label: "上游倍率", Value: oldText + "×变为" + newText + "×"}, {Label: "接入判断", Value: classification.description}, {Label: "发现时间", Value: observedAt.Format(time.RFC3339)}}
	if old != 0 {
		percent := (*group.Multiplier - old) / old * 100
		if !math.IsNaN(percent) && !math.IsInf(percent, 0) {
			details = append(details, notifications.Detail{Label: "变化比例", Value: strconv.FormatFloat(percent, 'g', -1, 64) + "%"})
		}
	}
	if classification.accountCount >= 0 {
		details = append(details, notifications.Detail{Label: "已登记主站账号数量", Value: strconv.Itoa(classification.accountCount)})
	}
	if len(classification.accountNames) > 0 {
		names := strings.Join(classification.accountNames, "、")
		if classification.accountCount > 20 {
			names += fmt.Sprintf("；仅列前20个，共%d个", classification.accountCount)
		}
		details = append(details, notifications.Detail{Label: "已登记主站账号摘要", Value: names})
	}
	if !classification.confirmedAt.IsZero() {
		details = append(details, notifications.Detail{Label: "最近分组归属确认时间", Value: classification.confirmedAt.Format(time.RFC3339)})
	}
	if len(classification.unknownReasons) > 0 {
		details = append(details, notifications.Detail{Label: "需要核查的原因", Value: limitRateNoticeText(strings.Join(classification.unknownReasons, "；"), 1000)})
	}
	action := &notifications.Action{Kind: "group_rates", SiteID: site.ID}
	if group.ID != "" {
		action.FocusGroupID = group.ID
	} else {
		action.FocusGroup = group.Name
	}
	title := siteName + "的上游倍率发生变化"
	summary := groupName + "：" + oldText + "×变为" + newText + "×。" + classification.description + "；请查看倍率和原关联，不会自动修改主站。"
	rateFact := siteName + "·" + groupName + "：上游倍率" + oldText + "×→" + newText + "×。"
	if classification.level == notifications.LevelUrgent {
		title = "已登记主站接入的上游倍率发生变化"
		summary = rateFact + "请检查成本、定价和调度安排；查看或确认不会修改主站。"
		if classification.description == rateNoticeUnknownScopeDescription {
			summary += rateNoticeUnknownScopeDescription
		}
	} else if classification.level == notifications.LevelNormal {
		summary = rateFact + "上游倍率发生变化；未发现这个分组的已登记主站接入。"
	}
	return notifications.NoticeInput{UserID: site.UserID, AdminAccountID: site.AdminAccountID, Kind: "upstream.group_rate_changed", EventKey: eventKey, SubjectType: "upstream_site", SubjectID: site.ID, Level: classification.level, DiscoveredAt: observedAt, Content: notifications.Content{Title: title, Summary: summary, Details: details, Action: action}}, nil
}
