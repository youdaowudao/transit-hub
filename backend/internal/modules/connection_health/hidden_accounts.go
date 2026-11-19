package connection_health

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/modules/upstream"
)

// 屏蔽清单只影响分组健康页的显示、批量/计划问答的选择和模型自动关闭恢复。
// 它不登记 Priority 同步、不递增健康配置代次，也不修改分组排除或任何主站设置。

const hiddenAccountNameMaxRunes = 200

const ErrorHiddenAccountsRead = "admin.connectionHealth.errors.hiddenAccountsRead"

var errHiddenAccountsWorkspaceChanged = errors.New("读取屏蔽清单失败：当前工作区已变化，请重新选择工作区后重新读取；这次只读取，没有改动任何设置。")

type HiddenAccount struct {
	TargetID    string    `json:"targetId"`
	AccountName string    `json:"accountName"`
	HiddenAt    time.Time `json:"hiddenAt"`
}

type HiddenAccountList struct {
	WorkspaceID string          `json:"workspaceId"`
	Items       []HiddenAccount `json:"items"`
}

type HiddenAccountResult struct {
	TargetID string     `json:"targetId"`
	Hidden   bool       `json:"hidden"`
	HiddenAt *time.Time `json:"hiddenAt,omitempty"`
}

// hiddenAccountStore 由生产仓库实现。测试替身未实现时视为没有屏蔽账号，
// 生产环境的 Repository 始终实现它。
type hiddenAccountStore interface {
	ListHiddenAccounts(ctx context.Context, userID, workspaceID string) ([]HiddenAccount, error)
	SaveAccountHidden(ctx context.Context, userID, workspaceID, targetID string, hidden bool, name string) (*time.Time, error)
}

func (r *Repository) ListHiddenAccounts(ctx context.Context, userID, workspaceID string) ([]HiddenAccount, error) {
	rows, err := r.db.Query(ctx, `SELECT target_id, display_hidden_name, display_hidden_at
		FROM connection_health_account_configs
		WHERE user_id = $1 AND admin_account_id = $2 AND display_hidden_at IS NOT NULL
		ORDER BY display_hidden_at DESC, target_id ASC`, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []HiddenAccount{}
	for rows.Next() {
		var item HiddenAccount
		if err := rows.Scan(&item.TargetID, &item.AccountName, &item.HiddenAt); err != nil {
			return nil, err
		}
		item.HiddenAt = item.HiddenAt.UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

// SaveAccountHidden 在一个事务里先用 FOR SHARE 复核工作区仍存在（与删除工作区的
// FOR UPDATE 互斥），再只写两列屏蔽字段，不改档位等其他列。
func (r *Repository) SaveAccountHidden(ctx context.Context, userID, workspaceID, targetID string, hidden bool, name string) (*time.Time, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var found string
	err = tx.QueryRow(ctx, `SELECT id FROM admin_accounts WHERE user_id = $1 AND id = $2 FOR SHARE`, userID, workspaceID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, requestError(ErrorNoCurrentAccount)
	}
	if err != nil {
		return nil, err
	}
	var hiddenAt *time.Time
	if hidden {
		var at time.Time
		err = tx.QueryRow(ctx, `INSERT INTO connection_health_account_configs
			(user_id, admin_account_id, target_id, display_hidden_at, display_hidden_name)
			VALUES ($1, $2, $3, now(), $4)
			ON CONFLICT (user_id, admin_account_id, target_id) DO UPDATE
			SET display_hidden_at = COALESCE(connection_health_account_configs.display_hidden_at, now()),
				display_hidden_name = EXCLUDED.display_hidden_name
			RETURNING display_hidden_at`, userID, workspaceID, targetID, name).Scan(&at)
		if err != nil {
			return nil, err
		}
		at = at.UTC()
		hiddenAt = &at
	} else {
		if _, err := tx.Exec(ctx, `UPDATE connection_health_account_configs
			SET display_hidden_at = NULL, display_hidden_name = ''
			WHERE user_id = $1 AND admin_account_id = $2 AND target_id = $3 AND display_hidden_at IS NOT NULL`, userID, workspaceID, targetID); err != nil {
			return nil, err
		}
	}
	return hiddenAt, tx.Commit(ctx)
}

func normalizeHiddenAccountName(name, targetID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		if parsed, ok := parseTargetID(targetID); ok {
			name = parsed.accountID
		}
	}
	if utf8.RuneCountInString(name) > hiddenAccountNameMaxRunes {
		name = string([]rune(name)[:hiddenAccountNameMaxRunes])
	}
	return name
}

func (s *Service) hiddenAccountStore() (hiddenAccountStore, bool) {
	store, ok := s.repo.(hiddenAccountStore)
	return store, ok
}

// hiddenAccountTargetSet 返回工作区内被屏蔽的账号。仓库未实现（仅测试替身）时为空集合。
func (s *Service) hiddenAccountTargetSet(ctx context.Context, userID, workspaceID string) (map[string]bool, error) {
	store, ok := s.hiddenAccountStore()
	if !ok {
		return map[string]bool{}, nil
	}
	items, err := store.ListHiddenAccounts(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(items))
	for _, item := range items {
		result[item.TargetID] = true
	}
	return result, nil
}

func (s *Service) ListHiddenAccounts(ctx context.Context, userID, workspace string) (HiddenAccountList, error) {
	reader, ok := s.accounts.(accountTierWorkspaceReader)
	if !ok {
		return HiddenAccountList{}, requestError(ErrorNoCurrentAccount)
	}
	account, err := reader.Current(ctx, userID)
	if err != nil {
		return HiddenAccountList{}, err
	}
	if account == nil {
		return HiddenAccountList{}, requestError(ErrorNoCurrentAccount)
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || workspace != account.ID {
		return HiddenAccountList{}, errHiddenAccountsWorkspaceChanged
	}
	if account.Platform != string(upstream.PlatformSub2API) {
		return HiddenAccountList{}, requestError(ErrorProbeTargetNotFound)
	}
	result := HiddenAccountList{WorkspaceID: account.ID, Items: []HiddenAccount{}}
	store, ok := s.hiddenAccountStore()
	if !ok {
		return result, nil
	}
	items, err := store.ListHiddenAccounts(ctx, userID, account.ID)
	if err != nil {
		return HiddenAccountList{}, err
	}
	result.Items = items
	return result, nil
}

func (s *Service) SetAccountHidden(ctx context.Context, userID, targetID string, hidden bool, name string) (HiddenAccountResult, error) {
	targetID = strings.TrimSpace(targetID)
	workspaceID, err := s.accountTierWorkspace(ctx, userID, targetID)
	if err != nil {
		return HiddenAccountResult{}, err
	}
	store, ok := s.hiddenAccountStore()
	if !ok {
		return HiddenAccountResult{}, requestError(ErrorQuestionAnswerStorage)
	}
	hiddenAt, err := store.SaveAccountHidden(ctx, userID, workspaceID, targetID, hidden, normalizeHiddenAccountName(name, targetID))
	if err != nil {
		return HiddenAccountResult{}, err
	}
	return HiddenAccountResult{TargetID: targetID, Hidden: hidden, HiddenAt: hiddenAt}, nil
}
