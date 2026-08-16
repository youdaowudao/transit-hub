package upstream

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

const (
	groupCostSampleInterval = 5 * time.Minute
	groupCostSampleTTL      = 90 * time.Minute
	groupCostSampleWindow   = 75 * time.Minute
	groupCostMaxAge         = 15 * time.Minute
	groupCostMaxSamples     = 16
	groupCostSampleTimeout  = 60 * time.Second
)

// GroupCostSample 是上游平台返回的当天累计原始金额。这里只保存短期比较所需的
// 累计值和采样时间，不保存用户、请求或 API Key 明细。
type GroupCostSample struct {
	Date       string    `json:"date"`
	RawAmount  float64   `json:"rawAmount"`
	ObservedAt time.Time `json:"observedAt"`
}

// GroupCostSnapshot 是一个上游站点/分组的短期成本快照。金额已经按站点充值倍率
// 换算为人民币；nil 表示当前样本或比较基准不足，调用方必须展示未知而不是零。
type GroupCostSnapshot struct {
	SiteID         string     `json:"siteId"`
	SiteName       string     `json:"siteName"`
	Platform       Platform   `json:"platform"`
	GroupID        string     `json:"groupId"`
	GroupName      string     `json:"groupName"`
	TodayCost      *float64   `json:"todayCost,omitempty"`
	RecentHourCost *float64   `json:"recentHourCost,omitempty"`
	ObservedAt     *time.Time `json:"observedAt,omitempty"`
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
	if store == nil || !site.IsEnabled() || session.Platform == "" || len(groups) == 0 {
		return
	}
	if s.groupCostSlots != nil {
		select {
		case s.groupCostSlots <- struct{}{}:
			defer func() { <-s.groupCostSlots }()
		default:
			// 成本只是观测值，跨站点并发达到上限时直接跳过，不积压后台任务。
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), groupCostSampleTimeout)
	defer cancel()
	acquired, err := store.TryStartGroupCostSampling(ctx, site.ID, groupCostSampleInterval)
	if err != nil || !acquired {
		if err != nil {
			log.Printf("[upstream-cost] sample lock failed site_id=%s err=%v", site.ID, err)
		}
		return
	}

	date := businesstime.Today()
	stats, err := s.platformService.FetchGroupCostStatsForDate(session, groups, date)
	if err != nil {
		// 成本采样是可选观测，不得把失败写回站点健康状态或同步结果。
		log.Printf("[upstream-cost] group cost sample failed site_id=%s err=%v", site.ID, err)
		return
	}
	if businesstime.Today() != date {
		// 请求跨过上海业务日边界时丢弃整批，避免把新日累计值写进旧日样本。
		return
	}

	groupsByKey := make(map[string]GroupInfo, len(groups)*2)
	for _, group := range groups {
		key := strings.TrimSpace(group.ID)
		if key == "" {
			key = strings.TrimSpace(group.Name)
		}
		if key == "" {
			continue
		}
		groupsByKey[key] = group
		if name := strings.TrimSpace(group.Name); name != "" {
			groupsByKey[name] = group
		}
	}

	samplesByGroup := make(map[string][]GroupCostSample)
	for _, stat := range stats {
		if math.IsNaN(stat.TodayActualCost) || math.IsInf(stat.TodayActualCost, 0) {
			continue
		}
		key := strings.TrimSpace(stat.GroupID)
		if key == "" {
			key = strings.TrimSpace(stat.GroupName)
		}
		group, ok := groupsByKey[key]
		if !ok {
			continue
		}
		groupKey := strings.TrimSpace(group.ID)
		if groupKey == "" {
			groupKey = strings.TrimSpace(group.Name)
		}
		if groupKey == "" {
			continue
		}
		samplesByGroup[groupKey] = []GroupCostSample{{Date: date, RawAmount: stat.TodayActualCost, ObservedAt: time.Now().UTC()}}
	}

	for groupKey, samples := range samplesByGroup {
		if err := store.AppendGroupCostSamples(ctx, site.ID, groupKey, date, samples, groupCostMaxSamples, groupCostSampleTTL); err != nil {
			log.Printf("[upstream-cost] save group cost sample failed site_id=%s group=%s err=%v", site.ID, groupKey, err)
		}
	}
}

func (s *Service) groupCostSnapshotsForSite(ctx context.Context, site *Site) ([]GroupCostSnapshot, error) {
	if site == nil || !site.IsEnabled() {
		return nil, nil
	}
	store := s.groupCostStore()
	if store == nil {
		return nil, nil
	}
	date := businesstime.Today()
	result := make([]GroupCostSnapshot, 0, len(site.Metrics.Groups))
	for _, group := range site.Metrics.Groups {
		groupKey := strings.TrimSpace(group.ID)
		if groupKey == "" {
			groupKey = strings.TrimSpace(group.Name)
		}
		if groupKey == "" {
			continue
		}
		samples, err := store.ListGroupCostSamples(ctx, site.ID, groupKey, date)
		if err != nil {
			return nil, err
		}
		todayCost, recentHourCost, observedAt := calculateGroupCostSnapshot(samples, time.Now().UTC(), site.RechargeRate)
		result = append(result, GroupCostSnapshot{
			SiteID:         site.ID,
			SiteName:       site.Name,
			Platform:       site.Platform,
			GroupID:        group.ID,
			GroupName:      group.Name,
			TodayCost:      todayCost,
			RecentHourCost: recentHourCost,
			ObservedAt:     observedAt,
		})
	}
	return result, nil
}

func calculateGroupCostSnapshot(samples []GroupCostSample, now time.Time, rechargeRate float64) (*float64, *float64, *time.Time) {
	if len(samples) == 0 || rechargeRate <= 0 || math.IsNaN(rechargeRate) || math.IsInf(rechargeRate, 0) {
		return nil, nil, nil
	}
	ordered := append([]GroupCostSample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ObservedAt.Before(ordered[j].ObservedAt)
	})
	latest := ordered[len(ordered)-1]
	if strings.TrimSpace(latest.Date) == "" || now.Sub(latest.ObservedAt) < 0 || now.Sub(latest.ObservedAt) > groupCostMaxAge {
		return nil, nil, nil
	}
	if math.IsNaN(latest.RawAmount) || math.IsInf(latest.RawAmount, 0) {
		return nil, nil, nil
	}

	todayCost := latest.RawAmount * rechargeRate
	if math.IsNaN(todayCost) || math.IsInf(todayCost, 0) {
		return nil, nil, nil
	}
	observedAt := latest.ObservedAt
	var baseline *GroupCostSample
	for i := range ordered[:len(ordered)-1] {
		candidate := ordered[i]
		age := latest.ObservedAt.Sub(candidate.ObservedAt)
		if candidate.Date != latest.Date || age < time.Hour || age > groupCostSampleWindow {
			continue
		}
		if baseline == nil || candidate.ObservedAt.After(baseline.ObservedAt) {
			candidateCopy := candidate
			baseline = &candidateCopy
		}
	}
	var recentHourCost *float64
	if baseline != nil && !math.IsNaN(baseline.RawAmount) && !math.IsInf(baseline.RawAmount, 0) {
		value := (latest.RawAmount - baseline.RawAmount) * rechargeRate
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			recentHourCost = &value
		}
	}
	return &todayCost, recentHourCost, &observedAt
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
		if name := strings.TrimSpace(snapshot.GroupName); name != "" {
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
		}
	}
}

func marshalGroupCostSample(sample GroupCostSample) ([]byte, error) {
	return json.Marshal(sample)
}
