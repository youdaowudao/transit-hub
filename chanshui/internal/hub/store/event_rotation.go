package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

const schemaVersion = 4

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	version, err := databaseVersion(ctx, conn)
	if err != nil {
		return err
	}
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA cache_size=-2048"} {
		if _, err = conn.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	if version == schemaVersion {
		return nil
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return migrationFailure(err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	version, err = databaseVersion(ctx, conn)
	if err != nil || version == schemaVersion {
		return err
	}
	if version < 3 {
		if err = migrateToV3(ctx, conn, version); err != nil {
			return migrationFailure(err)
		}
	}
	if err = rotateKnowledgeDefaults(ctx, conn); err != nil {
		return migrationFailure(err)
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
		return migrationFailure(err)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return migrationFailure(err)
	}
	return nil
}

func databaseVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	if version > schemaVersion {
		return version, Fail(503, "检测数据库由更新版本创建，请使用匹配版本，当前程序未修改数据", "migration")
	}
	return version, nil
}

func migrationFailure(err error) error {
	return Fail(503, "检测数据库升级未完成，本次更改已撤销。请查看检测服务日志，处理数据库占用或存储故障后重试。详情："+err.Error(), "migration")
}

func rotateKnowledgeDefaults(ctx context.Context, conn *sql.Conn) error {
	legacy := make(map[string]string)
	for _, q := range questions.LegacyKnowledge() {
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		legacy[q.ID] = string(raw)
	}
	current := questions.CurrentKnowledge()
	definitions := make([]string, 0, len(current))
	for _, q := range current {
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		definitions = append(definitions, string(raw))
	}

	var owner Owner
	for {
		err := conn.QueryRowContext(ctx, `SELECT user_id,workspace_id FROM hub_settings
			WHERE (user_id,workspace_id)>(?,?) ORDER BY user_id,workspace_id LIMIT 1`,
			owner.UserID, owner.WorkspaceID).Scan(&owner.UserID, &owner.WorkspaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		untouched, err := hasUntouchedKnowledge(ctx, conn, owner, legacy)
		if err != nil {
			return err
		}
		if !untouched {
			continue
		}
		if _, err = conn.ExecContext(ctx, `DELETE FROM hub_questions
			WHERE user_id=? AND workspace_id=? AND json_extract(definition_json,'$.kind')='knowledge'`,
			owner.UserID, owner.WorkspaceID); err != nil {
			return err
		}
		for i, q := range current {
			if _, err = conn.ExecContext(ctx, "INSERT INTO hub_questions VALUES(?,?,?,?,1)", owner.UserID, owner.WorkspaceID, q.ID, definitions[i]); err != nil {
				return err
			}
		}
	}
}

func hasUntouchedKnowledge(ctx context.Context, conn *sql.Conn, owner Owner, legacy map[string]string) (bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT question_id,definition_json,revision FROM hub_questions
		WHERE user_id=? AND workspace_id=? AND json_extract(definition_json,'$.kind')='knowledge'`,
		owner.UserID, owner.WorkspaceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, raw string
		var revision int
		if err = rows.Scan(&id, &raw, &revision); err != nil {
			return false, err
		}
		if revision != 1 || legacy[id] != raw {
			return false, nil
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	return count == len(legacy), nil
}
