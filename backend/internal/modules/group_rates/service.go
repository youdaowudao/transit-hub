package group_rates

import (
	"context"
	"errors"
	"math"
	"strings"
)

type Service struct {
	repository *Repository
}

func NewService(repository *Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	return s.repository.EnsureSchema(ctx)
}

func (s *Service) SaveSiteSnapshot(ctx context.Context, userID string, adminAccountID string, siteID string, siteName string, sitePlatform string, groups []SnapshotGroup, lastSyncedAt int64, sourceRowVersion string, allowCleanup bool) error {
	ownerID, workspaceID := strings.TrimSpace(userID), strings.TrimSpace(adminAccountID)
	if ownerID == "" || workspaceID == "" || strings.TrimSpace(siteID) == "" || strings.TrimSpace(sitePlatform) == "" || lastSyncedAt <= 0 || strings.TrimSpace(sourceRowVersion) == "" {
		return errors.New("倍率保存来源无法确认：网站、工作区、同步时间或数据库版本缺失，请重新同步网站")
	}
	if err := validateSnapshotGroups(groups); err != nil {
		return err
	}
	return s.repository.ApplySiteSnapshot(ctx, ownerID, workspaceID, strings.TrimSpace(siteID), strings.TrimSpace(siteName), strings.TrimSpace(sitePlatform), groups, lastSyncedAt, sourceRowVersion, allowCleanup)
}

func (s *Service) List(ctx context.Context, userID string, adminAccountID string, query ListQuery) (ListResult, error) {
	query = normalizeListQuery(query)
	records, err := s.repository.List(ctx, strings.TrimSpace(userID), strings.TrimSpace(adminAccountID), query)
	if err != nil {
		return ListResult{}, err
	}

	rows := make([]RateRow, 0, len(records.Items))
	for _, record := range records.Items {
		delta, deltaPercent := change(record.Multiplier, record.PreviousMultiplier)
		rows = append(rows, RateRow{
			SiteID:             record.SiteID,
			SiteName:           record.SiteName,
			GroupID:            record.GroupID,
			GroupName:          record.GroupName,
			Platform:           record.Platform,
			Type:               record.Type,
			Mapped:             record.Mapped,
			Connected:          record.Mapped,
			PricingMapped:      record.PricingMapped,
			Deleted:            record.Deleted,
			UpstreamMultiplier: record.Multiplier,
			RechargeRate:       record.RechargeRate,
			CurrentMultiplier:  math.Round(record.Multiplier*record.RechargeRate*1000) / 1000,
			Delta:              delta,
			DeltaPercent:       deltaPercent,
			UpdatedAt:          record.LastSeenAt,
		})
	}
	return ListResult{
		Items:                   rows,
		Total:                   records.Total,
		Page:                    records.Page,
		WorkspaceAdminAccountID: strings.TrimSpace(adminAccountID),
		FocusFound:              records.FocusFound,
		PageSize:                query.PageSize,
		TotalPages:              totalPages(records.Total, query.PageSize),
		Types:                   records.Types,
		Platforms:               records.Platforms,
		StatusCounts:            records.StatusCounts,
	}, nil
}

func (s *Service) UpdateType(ctx context.Context, userID string, adminAccountID string, ref GroupRef, groupType string) error {
	return s.repository.UpdateType(ctx, strings.TrimSpace(userID), strings.TrimSpace(adminAccountID), normalizeGroupRef(ref), strings.TrimSpace(groupType))
}

// ListGroupNames 按 type/search/platform 从最新分组快照中筛出匹配的分组名集合，
// 供 group_rate_campaigns 模块（group_rate_campaigns.GroupTypeLookup）解析
// "按分组类型"/"当前筛选结果" 两种选择模式使用。
func (s *Service) ListGroupNames(ctx context.Context, userID string, adminAccountID string, search string, groupType string, platform string) ([]string, error) {
	return s.repository.ListDistinctGroupNames(ctx, strings.TrimSpace(userID), strings.TrimSpace(adminAccountID), search, groupType, platform)
}

func normalizeGroupRef(ref GroupRef) GroupRef {
	return GroupRef{SiteID: strings.TrimSpace(ref.SiteID), GroupName: strings.TrimSpace(ref.GroupName)}
}

func (s *Service) History(ctx context.Context, userID string, adminAccountID string, siteID string, groupName string, platform string) ([]HistoryRow, error) {
	records, err := s.repository.History(ctx, strings.TrimSpace(userID), strings.TrimSpace(adminAccountID), strings.TrimSpace(siteID), strings.TrimSpace(groupName), strings.TrimSpace(platform))
	if err != nil {
		return nil, err
	}

	rows := make([]HistoryRow, 0, len(records))
	for _, record := range records {
		delta, deltaPercent := change(record.Multiplier, record.PreviousMultiplier)
		rows = append(rows, HistoryRow{
			ID:                record.ID,
			SiteID:            record.SiteID,
			SiteName:          record.SiteName,
			GroupID:           record.GroupID,
			GroupName:         record.GroupName,
			Platform:          record.Platform,
			Type:              record.Type,
			Multiplier:        record.Multiplier,
			CurrentMultiplier: math.Round(record.Multiplier*record.RechargeRate*1000) / 1000,
			Deleted:           record.Deleted,
			Delta:             delta,
			DeltaPercent:      deltaPercent,
			CreatedAt:         record.CreatedAt,
			UpdatedAt:         record.LastSeenAt,
		})
	}
	return rows, nil
}

func normalizeListQuery(query ListQuery) ListQuery {
	query.SiteID = strings.TrimSpace(query.SiteID)
	query.FocusGroupID = strings.TrimSpace(query.FocusGroupID)
	query.FocusGroup = strings.TrimSpace(query.FocusGroup)
	query.Search = strings.TrimSpace(query.Search)
	query.Type = strings.TrimSpace(query.Type)
	query.Platform = strings.TrimSpace(query.Platform)
	query.Status = strings.TrimSpace(query.Status)
	query.Sort = strings.TrimSpace(query.Sort)
	switch query.Status {
	case "":
		query.Status = "all"
	case "mapped", "unmapped", "deleted":
	case "all":
	default:
		query.Status = "all"
	}
	switch query.Sort {
	case "multiplierAsc", "multiplierDesc", "siteNameAsc", "groupNameAsc":
	default:
		query.Sort = "default"
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 10
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	return query
}

func totalPages(total int, pageSize int) int {
	if total == 0 || pageSize <= 0 {
		return 0
	}
	pages := total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	return pages
}

func change(current float64, previous *float64) (*float64, *float64) {
	if previous == nil || *previous == 0 {
		return nil, nil
	}
	delta := current - *previous
	deltaPercent := delta / *previous * 100
	return &delta, &deltaPercent
}
