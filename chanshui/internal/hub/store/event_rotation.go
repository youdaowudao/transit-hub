package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

const schemaVersion = 8

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The first version read can encounter WAL recovery or an exclusive lock.
	// Apply the existing wait window before any read, without changing the file.
	if _, err = conn.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	version, err := databaseVersion(ctx, conn)
	if err != nil {
		return err
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA cache_size=-2048"} {
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
	if version < 4 {
		if err = rotateKnowledgeDefaults(ctx, conn); err != nil {
			return migrationFailure(err)
		}
	}
	if version < 5 {
		if err = addModelIdentityDefault(ctx, conn); err != nil {
			return migrationFailure(err)
		}
	}
	if version < 6 {
		if err = refreshKnowledgeDefaults(ctx, conn); err != nil {
			return migrationFailure(err)
		}
	}
	if version < 7 {
		old := map[string]bool{}
		for _, q := range questions.CurrentKnowledge() {
			old[q.ID] = true
		}
		for _, q := range questions.ModelKnowledge() {
			if old[q.ID] {
				continue
			}
			raw, marshalErr := json.Marshal(q)
			if marshalErr != nil {
				return migrationFailure(marshalErr)
			}
			if _, err = conn.ExecContext(ctx, `INSERT OR IGNORE INTO hub_questions SELECT user_id,workspace_id,?,?,1 FROM hub_settings`, q.ID, string(raw)); err != nil {
				return migrationFailure(err)
			}
		}
	}
	if version < 8 {
		for _, stmt := range []string{
			`CREATE TABLE hub_batches(batch_id TEXT PRIMARY KEY,user_id TEXT NOT NULL,workspace_id TEXT NOT NULL,created_at_ms INTEGER NOT NULL,first_finished_at_ms INTEGER NOT NULL DEFAULT 0,request_json TEXT NOT NULL CHECK(json_valid(request_json)))`,
			`CREATE INDEX hub_batches_owner_date ON hub_batches(user_id,workspace_id,created_at_ms DESC,batch_id DESC)`,
			`ALTER TABLE hub_reports ADD COLUMN batch_id TEXT REFERENCES hub_batches(batch_id)`,
			`CREATE INDEX hub_reports_owner_batch ON hub_reports(user_id,workspace_id,batch_id)`,
		} {
			if _, err = conn.ExecContext(ctx, stmt); err != nil {
				return migrationFailure(err)
			}
		}
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA user_version=8"); err != nil {
		return migrationFailure(err)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return migrationFailure(err)
	}
	return nil
}

func refreshKnowledgeDefaults(ctx context.Context, conn *sql.Conn) error {
	previous := make(map[string]questions.Definition)
	for _, q := range questions.PreviousKnowledge() {
		previous[q.ID] = q
	}
	for _, q := range questions.CurrentKnowledge() {
		id := strings.TrimPrefix(q.ID, "knowledge:")
		oldAnchorID, replaces := knowledge.ReplacedAnchorID(id)
		if !replaces && !knowledge.IsMonthlySupplement(id) {
			continue
		}
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		if replaces {
			oldID := "knowledge:" + oldAnchorID
			oldRaw, err := json.Marshal(previous[oldID])
			if err != nil {
				return err
			}
			// The workspace's primary key retains both rows if the new ID already exists.
			if _, err = conn.ExecContext(ctx, `UPDATE OR IGNORE hub_questions SET question_id=?,definition_json=?
				WHERE question_id=? AND definition_json=? AND revision=1`, q.ID, string(raw), oldID, string(oldRaw)); err != nil {
				return err
			}
			continue
		}
		if _, err = conn.ExecContext(ctx, `INSERT OR IGNORE INTO hub_questions
			SELECT user_id,workspace_id,?,?,1 FROM hub_settings`, q.ID, string(raw)); err != nil {
			return err
		}
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
	defaults := questions.PreviousKnowledge()
	definitions := make([]string, 0, len(defaults))
	for _, q := range defaults {
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
		for i, q := range defaults {
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
