package dashboard

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const giftLedgerRecordColumns = `id, user_id, admin_account_id, main_user_id, main_user_email, recharge_date::text, paid_cents, gift_cents, note, created_at, updated_at`

func scanGiftLedgerRecord(row pgx.Row) (GiftLedgerRecord, error) {
	var record GiftLedgerRecord
	err := row.Scan(&record.ID, &record.UserID, &record.AdminAccountID, &record.MainUserID, &record.MainUserEmail,
		&record.RechargeDate, &record.PaidCents, &record.GiftCents, &record.Note, &record.CreatedAt, &record.UpdatedAt)
	return record, err
}

// ListGiftLedgerRecords 按客户、充值日期、录入时间、编号排序返回当前工作区的全部记录。
func (r *MetricsRepository) ListGiftLedgerRecords(ctx context.Context, userID, adminAccountID string) ([]GiftLedgerRecord, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+giftLedgerRecordColumns+`
		FROM dashboard_gift_ledger_records
		WHERE user_id = $1 AND admin_account_id = $2
		ORDER BY main_user_id, recharge_date, created_at, id
	`, userID, adminAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GiftLedgerRecord, 0)
	for rows.Next() {
		record, err := scanGiftLedgerRecord(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, record)
	}
	return items, rows.Err()
}

func (r *MetricsRepository) GetGiftLedgerRecord(ctx context.Context, userID, adminAccountID, id string) (GiftLedgerRecord, error) {
	record, err := scanGiftLedgerRecord(r.db.QueryRow(ctx, `
		SELECT `+giftLedgerRecordColumns+`
		FROM dashboard_gift_ledger_records
		WHERE user_id = $1 AND admin_account_id = $2 AND id = $3
	`, userID, adminAccountID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return GiftLedgerRecord{}, ErrGiftLedgerNotFound
	}
	return record, err
}

func (r *MetricsRepository) CreateGiftLedgerRecord(ctx context.Context, record GiftLedgerRecord) error {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO dashboard_gift_ledger_records
			(id, user_id, admin_account_id, main_user_id, main_user_email, recharge_date, paid_cents, gift_cents, note, created_at, updated_at)
		SELECT $1, $2, $3, $4, $5, $6::date, $7, $8, $9, $10, $10
		WHERE EXISTS (SELECT 1 FROM admin_accounts WHERE user_id = $2 AND id = $3)
	`, record.ID, record.UserID, record.AdminAccountID, record.MainUserID, record.MainUserEmail,
		record.RechargeDate, record.PaidCents, record.GiftCents, record.Note, record.CreatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrGiftLedgerNotFound
	}
	return nil
}

// UpdateGiftLedgerRecord 只改日期、金额和备注；录入时间不变，所以只改金额时先后顺序不变。
func (r *MetricsRepository) UpdateGiftLedgerRecord(ctx context.Context, record GiftLedgerRecord) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE dashboard_gift_ledger_records
		SET recharge_date = $4::date, paid_cents = $5, gift_cents = $6, note = $7, updated_at = $8
		WHERE user_id = $1 AND admin_account_id = $2 AND id = $3
	`, record.UserID, record.AdminAccountID, record.ID, record.RechargeDate, record.PaidCents, record.GiftCents, record.Note, record.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrGiftLedgerNotFound
	}
	return nil
}

func (r *MetricsRepository) DeleteGiftLedgerRecord(ctx context.Context, userID, adminAccountID, id string) error {
	tag, err := r.db.Exec(ctx, `
		DELETE FROM dashboard_gift_ledger_records WHERE user_id = $1 AND admin_account_id = $2 AND id = $3
	`, userID, adminAccountID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrGiftLedgerNotFound
	}
	return nil
}

func (r *MetricsRepository) ListGiftCustomerUsage(ctx context.Context, userID, adminAccountID string, mainUserIDs []int64, from, to string) (map[int64]map[string]int64, error) {
	result := map[int64]map[string]int64{}
	if len(mainUserIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT main_user_id, usage_date::text, actual_cost_micros
		FROM dashboard_gift_customer_usage
		WHERE user_id = $1 AND admin_account_id = $2 AND main_user_id = ANY($3)
		  AND usage_date >= $4::date AND usage_date <= $5::date
	`, userID, adminAccountID, mainUserIDs, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, micros int64
		var date string
		if err := rows.Scan(&id, &date, &micros); err != nil {
			return nil, err
		}
		if result[id] == nil {
			result[id] = map[string]int64{}
		}
		result[id][date] = micros
	}
	return result, rows.Err()
}

// SaveGiftCustomerUsage 每位客户每天只保存一个值：已存在就保持原值并返回它，保证所有计算用同一个数。
func (r *MetricsRepository) SaveGiftCustomerUsage(ctx context.Context, userID, adminAccountID string, mainUserID int64, date string, micros int64, observedAt time.Time) (int64, error) {
	var stored int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO dashboard_gift_customer_usage (user_id, admin_account_id, main_user_id, usage_date, actual_cost_micros, observed_at)
		SELECT $1, $2, $3, $4::date, $5, $6
		WHERE EXISTS (SELECT 1 FROM admin_accounts WHERE user_id = $1 AND id = $2)
		ON CONFLICT (user_id, admin_account_id, main_user_id, usage_date) DO UPDATE SET
			actual_cost_micros = dashboard_gift_customer_usage.actual_cost_micros
		RETURNING actual_cost_micros
	`, userID, adminAccountID, mainUserID, date, micros, observedAt).Scan(&stored)
	return stored, err
}

var _ giftLedgerRepository = (*MetricsRepository)(nil)
