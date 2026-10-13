package dashboard

import (
	"context"
	"errors"

	"transithub/backend/internal/shared/businesstime"
)

// The service fixes today's business date. Repository queries never infer it
// from the database server or process clock.
func (r *MetricsRepository) UnconfirmedQuotaRecognitionAssets(ctx context.Context, userID, workspace, date, currentDate string) (map[string]bool, error) {
	result := map[string]bool{}
	if date != currentDate {
		return result, nil
	}
	rows, err := r.db.Query(ctx, `SELECT asset.id,asset.batch_id,
 EXISTS(SELECT 1 FROM dashboard_additional_costs cost WHERE cost.user_id=asset.user_id AND cost.admin_account_id=asset.admin_account_id AND cost.account_asset_id=asset.id AND cost.business_date=$3::date AND cost.type=$5)
 FROM dashboard_account_assets asset WHERE asset.user_id=$1 AND asset.admin_account_id=$2 AND asset.recognition_mode=$4
 AND NOT EXISTS(SELECT 1 FROM dashboard_account_daily_stats stat WHERE stat.user_id=asset.user_id AND stat.admin_account_id=asset.admin_account_id AND stat.account_asset_id=asset.id AND stat.business_date=$3::date AND `+usableAutomaticStatSQL("stat", "$3")+`)`, userID, workspace, date, RecognitionModeQuota, AdditionalCostAccountPurchase)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []AccountAsset{}
	hasCurrentRecord := map[string]bool{}
	for rows.Next() {
		asset := AccountAsset{UserID: userID, AdminAccountID: workspace}
		var hasRecord bool
		if err := rows.Scan(&asset.ID, &asset.BatchID, &hasRecord); err != nil {
			return nil, err
		}
		candidates = append(candidates, asset)
		hasCurrentRecord[asset.ID] = hasRecord
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, asset := range candidates {
		status, mode, err := accountAssetStateAtDate(ctx, r.db, asset, date)
		if err != nil {
			return nil, err
		}
		if mode == StatsModeAutomatic && (status == AccountStatusUnactivated || status == AccountStatusActive || hasCurrentRecord[asset.ID]) {
			result[asset.ID] = true
		}
	}
	return result, nil
}
func (r *MetricsRepository) historicalAccountBusinessState(ctx context.Context, db metricsDB, stats []AccountDailyStat, currentDate string) (map[string]string, error) {
	state := map[string]string{}
	if currentDate == "" {
		return state, nil
	}
	for _, stat := range stats {
		if stat.BusinessDate != currentDate {
			continue
		}
		key := stat.UserID + "\x00" + stat.AdminAccountID + "\x00" + stat.AccountAssetID
		if _, exists := state[key]; exists {
			continue
		}
		var before string
		err := db.QueryRow(ctx, `SELECT jsonb_build_object(
   'stats',COALESCE((SELECT jsonb_agg(to_jsonb(stat)-ARRAY['id','created_at','updated_at','observed_at'] ORDER BY business_date,source,quality) FROM dashboard_account_daily_stats stat WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND business_date<$4::date),'[]'::jsonb),
   'costs',COALESCE((SELECT jsonb_agg(to_jsonb(cost)-ARRAY['id','created_at'] ORDER BY business_date,type,amount_cents,source_id) FROM dashboard_additional_costs cost WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND business_date<$4::date),'[]'::jsonb))::text`, stat.UserID, stat.AdminAccountID, stat.AccountAssetID, currentDate).Scan(&before)
		if err != nil {
			return nil, err
		}
		state[key] = before
	}
	return state, nil
}
func (r *MetricsRepository) verifyHistoricalAccountBusinessState(ctx context.Context, db metricsDB, stats []AccountDailyStat, currentDate string, before map[string]string) error {
	after, err := r.historicalAccountBusinessState(ctx, db, stats, currentDate)
	if err != nil {
		return err
	}
	for key, value := range before {
		if after[key] != value {
			return errors.New("刷新账号统计时发现历史金额、质量或费用确认字段发生变化，整批费用未保存；旧记录保留，请联系维护人员核查该账号的历史费用，不会修改主站。")
		}
	}
	return nil
}
func (s *MetricsService) certifiedAccountSnapshotRunID(ctx context.Context, userID, workspace, date, id string) string {
	if id == "" {
		return ""
	}
	repo, ok := s.metricsRepo.(accountSnapshotRunRepository)
	if !ok {
		return ""
	}
	runs, err := repo.AccountKeyCostRunsForSnapshot(ctx, userID, workspace, date, id, businesstime.DateAt(s.currentTime()))
	if err != nil || !usableAccountRunForDate(runs, userID, workspace, date, businesstime.DateAt(s.currentTime())) {
		return ""
	}
	return id
}
func maskedAutomaticAccountGuidance() string {
	return "这份账号费用尚未按新加坡日期重新核对，请点击‘刷新账号统计’。旧历史记录保留；此操作不会修改主站。"
}
