package upstream

import (
	"context"
	"errors"
	"sort"
)

var ErrBalanceSnapshotUnavailable = errors.New("原上游站点缓存当前不可用，不能读取已保存余额；请检查Redis连接和服务启动日志后重试，本次不会改用其他来源或请求上游。")

// BalanceSnapshot contains only fields already saved by the original site sync.
// It excludes sessions, endpoints, accounts, rates and derived group-cost reads.
type BalanceSnapshot struct {
	SiteID       string   `json:"siteId"`
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	Status       Status   `json:"status"`
	RawBalance   *float64 `json:"rawBalance"`
	LastSyncedAt *int64   `json:"lastSyncedAt"`
}

func (s *Service) BalanceSnapshotForWorkspace(ctx context.Context, userID, adminAccountID string) ([]BalanceSnapshot, error) {
	if s == nil || s.cache == nil {
		return nil, ErrBalanceSnapshotUnavailable
	}
	if cache, ok := s.cache.(*RedisSiteCache); ok && (cache == nil || cache.client == nil) {
		return nil, ErrBalanceSnapshotUnavailable
	}
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := []BalanceSnapshot{}
	for _, site := range sites {
		if site == nil || site.UserID != userID || site.AdminAccountID != adminAccountID {
			continue
		}
		item := BalanceSnapshot{SiteID: site.ID, Name: site.Name, Enabled: site.IsEnabled(), Status: site.Status}
		if site.Metrics.Balance.Value != nil {
			balance := *site.Metrics.Balance.Value
			item.RawBalance = &balance
		}
		if site.LastSyncedAt != nil {
			observed := *site.LastSyncedAt
			item.LastSyncedAt = &observed
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SiteID < result[j].SiteID })
	return result, nil
}
