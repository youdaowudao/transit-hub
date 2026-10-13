package upstream

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

const (
	groupCostSampleInterval   = 5 * time.Minute
	groupCostSampleTTL        = 90 * time.Minute
	groupCostSampleWindow     = 75 * time.Minute
	groupCostMaxAge           = 15 * time.Minute
	groupCostMaxSamples       = 16
	groupCostSampleTimeout    = 45 * time.Second
	groupCostFallbackInterval = 15 * time.Minute
	groupCostSamplingStateTTL = 25 * time.Hour
)

// GroupCostSample 是上游平台返回的当天累计原始金额。这里只保存短期比较所需的
// 累计值和采样时间，不保存用户、请求或 API Key 明细。
type GroupCostSample struct {
	Date                   string    `json:"date"`
	RawAmount              float64   `json:"rawAmount"`
	ObservedAt             time.Time `json:"observedAt"`
	Source                 string    `json:"source"`
	Complete               bool      `json:"complete"`
	SiteReportedRawCost    *float64  `json:"siteReportedRawCost,omitempty"`
	GroupAttributedRawCost *float64  `json:"groupAttributedRawCost,omitempty"`
	UnattributedRawCost    *float64  `json:"unattributedRawCost,omitempty"`
}

// GroupCostSnapshot 是一个上游站点/分组的短期成本快照。金额已经按站点充值倍率
// 换算为核算金额；nil 表示当前样本或比较基准不足，调用方必须展示未知而不是零。
type GroupCostSnapshot struct {
	SiteID         string     `json:"siteId"`
	SiteName       string     `json:"siteName"`
	Platform       Platform   `json:"platform"`
	GroupID        string     `json:"groupId"`
	GroupName      string     `json:"groupName"`
	TodayCost      *float64   `json:"todayCost,omitempty"`
	RecentHourCost *float64   `json:"recentHourCost,omitempty"`
	ObservedAt     *time.Time `json:"observedAt,omitempty"`
	Mode           string     `json:"mode"`
	Source         string     `json:"source,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	// Complete 表示请求目标分组和站点总额均已完整核对；站点总额中无法唯一归属的部分
	// 不会被分摊，保留在 UnattributedCost 中并将 Complete 设为 false。
	Complete            bool     `json:"complete"`
	SiteReportedCost    *float64 `json:"siteReportedCost,omitempty"`
	GroupAttributedCost *float64 `json:"groupAttributedCost,omitempty"`
	UnattributedCost    *float64 `json:"unattributedCost,omitempty"`
}

// SourceKey 是连接健康模块识别“同一个上游站点/分组”的稳定键。
func (s GroupCostSnapshot) SourceKey() string {
	return GroupCostSourceKey(s.SiteID, s.GroupID, s.GroupName)
}

// GroupCostSourceKey 优先使用上游分组 ID；兼容旧平台未返回 ID 时使用完整分组名。
func GroupCostSourceKey(siteID, groupID, groupName string) string {
	groupKey := strings.TrimSpace(groupID)
	if groupKey == "" {
		groupKey = strings.TrimSpace(groupName)
	}
	return strings.TrimSpace(siteID) + "\x00" + groupKey
}

// GroupCostStore 是 Redis 短期成本样本的可选依赖。保持为独立窄接口，避免现有
// SiteCache 测试替身被迫增加与成本无关的方法。
type GroupCostStore interface {
	TryStartGroupCostSampling(ctx context.Context, siteID string, ttl time.Duration) (bool, error)
	GetGroupCostSamplingState(ctx context.Context, siteID string) (GroupCostSamplingState, error)
	SetGroupCostSamplingState(ctx context.Context, siteID string, state GroupCostSamplingState, ttl time.Duration) error
	AppendGroupCostSamples(ctx context.Context, siteID, groupKey, date string, samples []GroupCostSample, maxSamples int, ttl time.Duration) error
	ListGroupCostSamples(ctx context.Context, siteID, groupKey, date string) ([]GroupCostSample, error)
	DeleteGroupCostSamples(ctx context.Context, siteID string) error
	ClearGroupCostSamples(ctx context.Context) error
}

func (s *Service) groupCostStore() GroupCostStore {
	store, _ := s.cache.(GroupCostStore)
	return store
}

func (s *Service) sampleGroupCosts(site Site, session Session, groups []GroupInfo) {
	store := s.groupCostStore()
	if store == nil || !site.IsEnabled() || site.Session == nil || session.Platform == "" {
		return
	}
	original := cloneSite(&site)
	if !session.IsAuthenticated() || session.Platform == PlatformSub2API && strings.TrimSpace(session.AccessToken) == "" {
		return
	}
	s.mu.Lock()
	currentAtStart, readErr := s.readCurrentSite(context.Background(), site.ID)
	_, deletedAtStart := s.deletedSites[site.ID]
	validStart := !deletedAtStart && readErr == nil && keyUsageSiteMatches(currentAtStart, original) && reflect.DeepEqual(currentAtStart.Session, original.Session) && sameCostGroupIdentifiers(session.Platform, currentAtStart.Metrics.Groups, groups)
	s.mu.Unlock()
	if !validStart {
		return
	}
	if s.groupCostSlots != nil {
		select {
		case s.groupCostSlots <- struct{}{}:
			defer func() { <-s.groupCostSlots }()
		default:
			return
		}
	}
	startedAt := s.keyUsageNow()
	date := businesstime.DateAt(startedAt)
	ctx, cancel := context.WithTimeout(context.Background(), groupCostSampleTimeout)
	defer cancel()
	acquired, err := store.TryStartGroupCostSampling(ctx, site.ID, groupCostSampleInterval)
	if err != nil || !acquired {
		return
	}
	state, err := store.GetGroupCostSamplingState(ctx, site.ID)
	if err != nil {
		return
	}
	if state.LastAttemptAt.After(startedAt) {
		return
	}
	result, fetchErr := s.platformService.FetchGroupCostStatsForDate(ctx, session, groups, date, state)
	if isGroupCostCooldown(fetchErr) {
		return
	}
	completedAt := s.keyUsageNow()
	if businesstime.DateAt(completedAt) != date {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.deletedSites[site.ID]; deleted {
		return
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer saveCancel()
	current, err := s.cache.Get(saveCtx, site.ID)
	if err != nil || !keyUsageSiteMatches(current, original) || !reflect.DeepEqual(current.Session, original.Session) || !sameCostGroupIdentifiers(session.Platform, current.Metrics.Groups, groups) {
		return
	}
	latestState, err := store.GetGroupCostSamplingState(saveCtx, site.ID)
	if err != nil || latestState.LastAttemptAt.After(startedAt) {
		return
	}
	state = latestState
	state.LastAttemptAt = startedAt
	if fetchErr != nil {
		class := groupCostFailureClass(fetchErr)
		state.RecordSummaryFailure(class, startedAt)
		state.LastReason = formatCostFailure("站点“"+safeCostReason(site.Name)+"”分组指定日费用读取", fetchErr)
		if err := store.SetGroupCostSamplingState(saveCtx, site.ID, state, groupCostSamplingStateTTL); err != nil {
			log.Printf("[upstream-cost] save failed sample state failed site_id=%s", site.ID)
		}
		log.Printf("[upstream-cost] group cost sample failed site_id=%s reason=%s", site.ID, class)
		return
	}
	state.RecordSummarySuccess(completedAt)
	state.LastAttemptAt = startedAt
	for _, stat := range result.Stats {
		key := strings.TrimSpace(stat.GroupID)
		if session.Platform == PlatformNewAPI {
			key = strings.TrimSpace(stat.GroupName)
		}
		sample := GroupCostSample{Date: date, RawAmount: stat.TodayActualCost, ObservedAt: completedAt, Source: result.Source, Complete: result.Complete, SiteReportedRawCost: cloneRawPointer(result.SiteReportedRawCost), GroupAttributedRawCost: cloneRawPointer(result.GroupAttributedRawCost), UnattributedRawCost: cloneRawPointer(result.UnattributedRawCost)}
		if err := store.AppendGroupCostSamples(saveCtx, site.ID, key, date, []GroupCostSample{sample}, groupCostMaxSamples, groupCostSampleTTL); err != nil {
			log.Printf("[upstream-cost] save group cost sample failed site_id=%s group=%s", site.ID, key)
			return
		}
	}
	if err := store.SetGroupCostSamplingState(saveCtx, site.ID, state, groupCostSamplingStateTTL); err != nil {
		log.Printf("[upstream-cost] save sample state failed site_id=%s", site.ID)
	}
}
func sameCostGroupIdentifiers(platform Platform, a, b []GroupInfo) bool {
	if len(a) != len(b) {
		return false
	}
	ids := map[string]bool{}
	for _, group := range a {
		key := strings.TrimSpace(group.ID)
		if platform == PlatformNewAPI {
			key = strings.TrimSpace(group.Name)
		}
		if key == "" || ids[key] {
			return false
		}
		ids[key] = true
	}
	for _, group := range b {
		key := strings.TrimSpace(group.ID)
		if platform == PlatformNewAPI {
			key = strings.TrimSpace(group.Name)
		}
		if !ids[key] {
			return false
		}
		delete(ids, key)
	}
	return len(ids) == 0
}

func (s *Service) groupCostSnapshotsForSite(ctx context.Context, site *Site) ([]GroupCostSnapshot, error) {
	if site == nil || !site.IsEnabled() {
		return nil, nil
	}
	if site.Status == StatusConnecting || site.Session == nil || site.Platform == PlatformSub2API && strings.TrimSpace(site.Session.AccessToken) == "" || site.Platform == PlatformNewAPI && !site.Session.IsAuthenticated() {
		result := make([]GroupCostSnapshot, 0, len(site.Metrics.Groups))
		for _, group := range site.Metrics.Groups {
			result = append(result, unavailableGroupCostSnapshot(site, group, "本站登录资料不支持用户分组费用读取，今日费用暂无法读取；请核查上游登录资料后同步这个站点重试，不会修改主站。"))
		}
		return result, nil
	}
	store := s.groupCostStore()
	if store == nil {
		return nil, nil
	}
	date := s.keyUsageDate()
	state, stateErr := store.GetGroupCostSamplingState(ctx, site.ID)
	result := make([]GroupCostSnapshot, 0, len(site.Metrics.Groups))
	for _, group := range site.Metrics.Groups {
		groupKey := strings.TrimSpace(group.ID)
		if site.Platform == PlatformNewAPI {
			groupKey = strings.TrimSpace(group.Name)
		}
		if groupKey == "" && site.Platform == PlatformNewAPI {
			groupKey = strings.TrimSpace(group.Name)
		}
		if groupKey == "" {
			continue
		}
		samples, err := store.ListGroupCostSamples(ctx, site.ID, groupKey, date)
		if err != nil {
			// 单个缓存键损坏不能让其它上游站点或分组丢失已有样本。
			result = append(result, unavailableGroupCostSnapshot(site, group, "sample_decode_failed"))
			continue
		}
		todayCost, recentHourCost, observedAt, sample := calculateGroupCostSnapshot(samples, date, s.keyUsageNow(), site.RechargeRate)
		if sample == nil {
			reason := "sample_unavailable"
			if stateErr == nil && strings.TrimSpace(state.LastReason) != "" {
				reason = state.LastReason
			}
			result = append(result, unavailableGroupCostSnapshot(site, group, reason))
			continue
		}
		snapshot := GroupCostSnapshot{
			SiteID:              site.ID,
			SiteName:            site.Name,
			Platform:            site.Platform,
			GroupID:             group.ID,
			GroupName:           group.Name,
			TodayCost:           todayCost,
			RecentHourCost:      recentHourCost,
			ObservedAt:          observedAt,
			Mode:                groupCostSnapshotMode(sample.ObservedAt, s.keyUsageNow()),
			Source:              sample.Source,
			Complete:            sample.Complete,
			SiteReportedCost:    convertedGroupCost(sample.SiteReportedRawCost, site.RechargeRate),
			GroupAttributedCost: convertedGroupCost(sample.GroupAttributedRawCost, site.RechargeRate),
			UnattributedCost:    convertedGroupCost(sample.UnattributedRawCost, site.RechargeRate),
		}
		if snapshot.Mode == GroupCostModeRetained {
			snapshot.Reason = "沿用截至 " + sample.ObservedAt.In(businesstime.Location()).Format("2006-01-02 15:04:05") + " 的费用；暂未取得更新记录，可在允许重试后同步这个站点；不会修改主站。"
		}
		if stateErr == nil && state.LastAttemptStatus == "failed" && state.LastAttemptAt.After(sample.ObservedAt) {
			snapshot.Mode = GroupCostModeRetained
			snapshot.Reason = "沿用截至 " + sample.ObservedAt.In(businesstime.Location()).Format("2006-01-02 15:04:05") + " 的费用；本轮" + state.LastReason
		}
		for _, newer := range samples {
			if newer.Date == date && newer.Source == sample.Source && newer.ObservedAt.After(sample.ObservedAt) && finiteNonnegative(newer.RawAmount) && newer.RawAmount < sample.RawAmount {
				snapshot.Mode = GroupCostModeRetained
				snapshot.Reason = "本轮累计费用下降，沿用截至 " + sample.ObservedAt.In(businesstime.Location()).Format("2006-01-02 15:04:05") + " 的已确认费用；请到上游核查当天消费记录后同步这个站点重试；不会修改主站。"
				snapshot.RecentHourCost = nil
				break
			}
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func calculateGroupCostSnapshot(samples []GroupCostSample, date string, now time.Time, rechargeRate float64) (*float64, *float64, *time.Time, *GroupCostSample) {
	if len(samples) == 0 || rechargeRate <= 0 || !finiteNonnegative(rechargeRate) {
		return nil, nil, nil, nil
	}
	ordered := append([]GroupCostSample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ObservedAt.Before(ordered[j].ObservedAt) })
	var latest *GroupCostSample
	for index := len(ordered) - 1; index >= 0; index-- {
		candidate := ordered[index]
		if candidate.Date != date || now.Sub(candidate.ObservedAt) < 0 || now.Sub(candidate.ObservedAt) > groupCostSampleTTL || !finiteNonnegative(candidate.RawAmount) {
			continue
		}
		rollback := false
		{
			for _, earlier := range ordered[:index] {
				age := candidate.ObservedAt.Sub(earlier.ObservedAt)
				if earlier.Date == date && earlier.Source == candidate.Source && earlier.Complete && age >= time.Hour && age <= groupCostSampleWindow && finiteNonnegative(earlier.RawAmount) && candidate.RawAmount < earlier.RawAmount {
					rollback = true
					break
				}
			}
		}
		if rollback {
			continue
		}
		copy := candidate
		latest = &copy
		break
	}
	if latest == nil {
		return nil, nil, nil, nil
	}
	today := latest.RawAmount * rechargeRate
	if !finiteNonnegative(today) {
		return nil, nil, nil, nil
	}
	var baseline *GroupCostSample
	if latest.Complete {
		for _, candidate := range ordered {
			age := latest.ObservedAt.Sub(candidate.ObservedAt)
			if candidate.Date != date || candidate.Source != latest.Source || !candidate.Complete || age < time.Hour || age > groupCostSampleWindow || !finiteNonnegative(candidate.RawAmount) {
				continue
			}
			if baseline == nil || candidate.ObservedAt.After(baseline.ObservedAt) {
				copy := candidate
				baseline = &copy
			}
		}
	}
	var hour *float64
	if now.Sub(latest.ObservedAt) <= groupCostMaxAge && baseline != nil && latest.RawAmount >= baseline.RawAmount {
		value := (latest.RawAmount - baseline.RawAmount) * rechargeRate
		if finiteNonnegative(value) {
			hour = &value
		}
	}
	observed := latest.ObservedAt
	return &today, hour, &observed, latest
}

func (s *Service) GroupCostSnapshots(ctx context.Context, userID, adminAccountID string) ([]GroupCostSnapshot, error) {
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]GroupCostSnapshot, 0)
	for _, site := range sites {
		if site == nil || site.AdminAccountID != adminAccountID || !site.IsEnabled() {
			continue
		}
		snapshots, err := s.groupCostSnapshotsForSite(ctx, site)
		if err != nil {
			return nil, err
		}
		result = append(result, snapshots...)
	}
	return result, nil
}

func mergeGroupCostSnapshots(site *Site, response *Response, snapshots []GroupCostSnapshot) {
	if site == nil || response == nil || len(response.Metrics.Groups) == 0 || len(snapshots) == 0 {
		return
	}
	byKey := make(map[string]GroupCostSnapshot, len(snapshots)*2)
	for _, snapshot := range snapshots {
		byKey[GroupCostSourceKey(snapshot.SiteID, snapshot.GroupID, snapshot.GroupName)] = snapshot
		if name := strings.TrimSpace(snapshot.GroupName); name != "" && site.Platform == PlatformNewAPI {
			byKey[GroupCostSourceKey(snapshot.SiteID, "", name)] = snapshot
		}
	}
	for index := range response.Metrics.Groups {
		group := &response.Metrics.Groups[index]
		key := strings.TrimSpace(group.ID)
		if key == "" {
			key = strings.TrimSpace(group.Name)
		}
		if snapshot, ok := byKey[GroupCostSourceKey(site.ID, key, group.Name)]; ok {
			group.TodayCost = snapshot.TodayCost
			group.CostMode = snapshot.Mode
			group.CostSource = snapshot.Source
			group.CostReason = snapshot.Reason
			group.CostComplete = snapshot.Complete
			group.CostObservedAt = snapshot.ObservedAt
			group.SiteReportedCost = snapshot.SiteReportedCost
			group.GroupAttributedCost = snapshot.GroupAttributedCost
			group.UnattributedCost = snapshot.UnattributedCost
		}
	}
}

func unavailableGroupCostSnapshot(site *Site, group GroupInfo, reason string) GroupCostSnapshot {
	return GroupCostSnapshot{SiteID: site.ID, SiteName: site.Name, Platform: site.Platform, GroupID: group.ID, GroupName: group.Name, Mode: GroupCostModeUnknown, Reason: reason}
}

func convertedGroupCost(raw *float64, rechargeRate float64) *float64 {
	if raw == nil || rechargeRate <= 0 || math.IsNaN(rechargeRate) || math.IsInf(rechargeRate, 0) {
		return nil
	}
	value := *raw * rechargeRate
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func cloneRawPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func marshalGroupCostSample(sample GroupCostSample) ([]byte, error) {
	return json.Marshal(sample)
}
