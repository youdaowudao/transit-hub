package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	_ "modernc.org/sqlite"
)

// MaxItemBytes accommodates full original DTOs without clipping normal 8MiB responses.
const MaxItemBytes = 12 << 20

type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	if path != ":memory:" && !filepath.IsAbs(path) {
		return nil, Fail(503, "检测数据库路径必须为可信绝对路径", "database")
	}
	if path != ":memory:" {
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
		if e := os.Chmod(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		_ = f.Close()
		if e = os.Chmod(path, 0600); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: path}
	if e = s.migrate(context.Background()); e != nil {
		_ = db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if e := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); e != nil {
		return e
	}
	if version > 3 {
		return Fail(503, "检测数据库由更新版本创建，请使用匹配版本，当前程序未修改数据", "migration")
	}
	for _, p := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA cache_size=-2048"} {
		if _, e := s.db.ExecContext(ctx, p); e != nil {
			return e
		}
	}
	if version == 3 {
		return nil
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS hub_settings(user_id TEXT NOT NULL,workspace_id TEXT NOT NULL,settings_json TEXT NOT NULL CHECK(json_valid(settings_json)),revision INTEGER NOT NULL CHECK(revision>=1),PRIMARY KEY(user_id,workspace_id))`,
		`CREATE TABLE IF NOT EXISTS hub_questions(user_id TEXT NOT NULL,workspace_id TEXT NOT NULL,question_id TEXT NOT NULL,definition_json TEXT NOT NULL CHECK(json_valid(definition_json)),revision INTEGER NOT NULL CHECK(revision>=1),PRIMARY KEY(user_id,workspace_id,question_id))`,
		`CREATE INDEX IF NOT EXISTS hub_questions_order ON hub_questions(user_id,workspace_id,json_extract(definition_json,'$.order'),question_id)`,
		`CREATE TABLE IF NOT EXISTS hub_reports(report_id TEXT PRIMARY KEY,user_id TEXT NOT NULL,workspace_id TEXT NOT NULL,target_id TEXT NOT NULL,account_id TEXT NOT NULL,account_name TEXT NOT NULL,group_id TEXT NOT NULL,group_name TEXT NOT NULL,base_url TEXT NOT NULL,protocol TEXT NOT NULL,model TEXT NOT NULL,created_at_ms INTEGER NOT NULL,updated_at_ms INTEGER NOT NULL,status TEXT NOT NULL CHECK(status IN('queued','running','done','partial','cancelled','interrupted')),request_json TEXT NOT NULL CHECK(json_valid(request_json)),snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),error_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(error_json)),summary_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(summary_json)))`,
		`DROP INDEX IF EXISTS hub_reports_one_active`,
		`CREATE UNIQUE INDEX IF NOT EXISTS hub_reports_one_active ON hub_reports((1)) WHERE status='running'`,
		`CREATE INDEX IF NOT EXISTS hub_reports_queue_order ON hub_reports(created_at_ms,report_id) WHERE status='queued'`,
		`CREATE INDEX IF NOT EXISTS hub_reports_owner_date ON hub_reports(user_id,workspace_id,created_at_ms DESC,report_id DESC)`,
		`CREATE TABLE IF NOT EXISTS hub_items(report_id TEXT NOT NULL REFERENCES hub_reports(report_id),item_id TEXT NOT NULL,section_id TEXT NOT NULL,order_index INTEGER NOT NULL,status TEXT NOT NULL CHECK(status IN('queued','running','done','error','cancelled')),attempts INTEGER NOT NULL DEFAULT 0,started_at_ms INTEGER NOT NULL DEFAULT 0,completed_at_ms INTEGER NOT NULL DEFAULT 0,result_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(result_json)),question_json TEXT NOT NULL CHECK(json_valid(question_json)),image BLOB,PRIMARY KEY(report_id,item_id))`,
		`CREATE INDEX IF NOT EXISTS hub_items_order ON hub_items(report_id,order_index,item_id)`,
		`CREATE TABLE IF NOT EXISTS runtime_state(name TEXT PRIMARY KEY,value BLOB NOT NULL)`,
		`UPDATE hub_settings SET settings_json=json_set(settings_json,'$.timeoutSeconds',CASE WHEN json_extract(settings_json,'$.timeoutSeconds')=120 THEN 360 ELSE json_extract(settings_json,'$.timeoutSeconds') END,'$.retries',CASE WHEN json_extract(settings_json,'$.retries')=0 THEN 3 ELSE json_extract(settings_json,'$.retries') END,'$.maxOutputTokens',CASE WHEN json_extract(settings_json,'$.maxOutputTokens')=4096 THEN 8192 ELSE json_extract(settings_json,'$.maxOutputTokens') END,'$.maxAttempts',CASE WHEN json_extract(settings_json,'$.maxAttempts')=128 THEN 0 ELSE json_extract(settings_json,'$.maxAttempts') END,'$.protocol',COALESCE(json_extract(settings_json,'$.protocol'),'auto')),revision=revision+1 WHERE json_extract(settings_json,'$.protocol') IS NULL OR json_extract(settings_json,'$.timeoutSeconds')=120 OR json_extract(settings_json,'$.retries')=0 OR json_extract(settings_json,'$.maxOutputTokens')=4096 OR json_extract(settings_json,'$.maxAttempts')=128`,
		`PRAGMA user_version=3`} {
		if _, e = tx.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	if version < 2 {
		if _, e = tx.ExecContext(ctx, `UPDATE hub_settings SET settings_json=json_set(settings_json,'$.concurrency',CASE WHEN json_extract(settings_json,'$.concurrency')=2 THEN 32 ELSE json_extract(settings_json,'$.concurrency') END,'$.totalTimeoutSeconds',CASE WHEN json_extract(settings_json,'$.totalTimeoutSeconds')=600 THEN 3600 ELSE json_extract(settings_json,'$.totalTimeoutSeconds') END),revision=revision+1 WHERE json_extract(settings_json,'$.concurrency')=2 OR json_extract(settings_json,'$.totalTimeoutSeconds')=600`); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) writable() error {
	if s.path == ":memory:" {
		return nil
	}
	var stat syscall.Statfs_t
	if e := syscall.Statfs(filepath.Dir(s.path), &stat); e != nil {
		return e
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < 128<<20 {
		return Fail(507, "检测数据目录剩余空间不足128MiB，已暂停新写入；请先备份并释放磁盘空间，旧报告不会被自动删除", "storage")
	}
	return nil
}
func retryWrite(ctx context.Context, f func() error) error {
	e := f()
	if e != nil && (strings.Contains(e.Error(), "SQLITE_BUSY") || strings.Contains(e.Error(), "database is locked")) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return f()
	}
	return e
}
func (s *Store) EnsureScope(ctx context.Context, o Owner) error {
	if e := o.Validate(); e != nil {
		return e
	}
	var n int
	e := s.db.QueryRowContext(ctx, "SELECT 1 FROM hub_settings WHERE user_id=? AND workspace_id=?", o.UserID, o.WorkspaceID).Scan(&n)
	if e == nil {
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e = s.writable(); e != nil {
		return e
	}
	return retryWrite(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		b, _ := json.Marshal(DefaultSettings())
		res, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO hub_settings VALUES(?,?,?,1)", o.UserID, o.WorkspaceID, string(b))
		if e != nil {
			return e
		}
		rows, _ := res.RowsAffected()
		if rows == 1 {
			for _, q := range questions.Presets() {
				q.Revision = 0
				b, _ := json.Marshal(q)
				if _, e = tx.ExecContext(ctx, "INSERT INTO hub_questions VALUES(?,?,?,?,1)", o.UserID, o.WorkspaceID, q.ID, string(b)); e != nil {
					return e
				}
			}
		}
		return tx.Commit()
	})
}
func (s *Store) Settings(ctx context.Context, o Owner) (Settings, error) {
	if e := s.EnsureScope(ctx, o); e != nil {
		return Settings{}, e
	}
	var raw string
	var out Settings
	e := s.db.QueryRowContext(ctx, "SELECT settings_json,revision FROM hub_settings WHERE user_id=? AND workspace_id=?", o.UserID, o.WorkspaceID).Scan(&raw, &out.Revision)
	if e != nil {
		return out, e
	}
	rev := out.Revision
	e = json.Unmarshal([]byte(raw), &out)
	out.Revision = rev
	return out, e
}
func (s *Store) SaveSettings(ctx context.Context, o Owner, v Settings) (Settings, error) {
	if e := v.Validate(); e != nil {
		return Settings{}, Fail(422, e.Error(), "settings")
	}
	if v.Revision < 1 {
		return Settings{}, Fail(422, "缺少设置版本，请重新打开设置后保存", "settings")
	}
	if e := s.EnsureScope(ctx, o); e != nil {
		return Settings{}, e
	}
	if e := s.writable(); e != nil {
		return Settings{}, e
	}
	raw, _ := json.Marshal(v)
	e := retryWrite(ctx, func() error {
		res, e := s.db.ExecContext(ctx, "UPDATE hub_settings SET settings_json=?,revision=revision+1 WHERE user_id=? AND workspace_id=? AND revision=?", string(raw), o.UserID, o.WorkspaceID, v.Revision)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			var current int
			err := s.db.QueryRowContext(ctx, "SELECT revision FROM hub_settings WHERE user_id=? AND workspace_id=?", o.UserID, o.WorkspaceID).Scan(&current)
			if errors.Is(err, sql.ErrNoRows) {
				return Conflict("设置已被删除，请重新打开设置页后再操作", "settings", 0)
			}
			if err != nil {
				return err
			}
			return Conflict("设置已被其他页面修改，请重新读取后再保存", "settings", current)
		}
		return nil
	})
	if e != nil {
		return Settings{}, e
	}
	v.Revision++
	return v, nil
}
func (s *Store) Question(ctx context.Context, o Owner, id string) (questions.Definition, error) {
	if e := s.EnsureScope(ctx, o); e != nil {
		return questions.Definition{}, e
	}
	var raw string
	var rev int
	e := s.db.QueryRowContext(ctx, "SELECT definition_json,revision FROM hub_questions WHERE user_id=? AND workspace_id=? AND question_id=?", o.UserID, o.WorkspaceID, id).Scan(&raw, &rev)
	if errors.Is(e, sql.ErrNoRows) {
		return questions.Definition{}, Fail(404, "题目不存在或不属于当前工作区，请刷新题目列表", "question")
	}
	if e != nil {
		return questions.Definition{}, e
	}
	var q questions.Definition
	e = json.Unmarshal([]byte(raw), &q)
	q.Revision = rev
	return q, e
}
func (s *Store) SaveQuestion(ctx context.Context, o Owner, q questions.Definition, revision int, restore bool) (questions.Definition, error) {
	if restore {
		p, ok := questions.Preset(q.ID)
		if !ok {
			return q, Fail(422, "这道题没有内置默认内容", "question")
		}
		q = p
	}
	if e := questions.Validate(q); e != nil {
		return q, Fail(422, e.Error(), "question")
	}
	if _, e := questions.Generate(q, 1); e != nil {
		return q, Fail(422, e.Error(), "question")
	}
	if e := s.EnsureScope(ctx, o); e != nil {
		return q, e
	}
	if e := s.writable(); e != nil {
		return q, e
	}
	q.Revision = 0
	raw, _ := json.Marshal(q)
	e := retryWrite(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		var old int
		e = tx.QueryRowContext(ctx, "SELECT revision FROM hub_questions WHERE user_id=? AND workspace_id=? AND question_id=?", o.UserID, o.WorkspaceID, q.ID).Scan(&old)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, preset := questions.Preset(q.ID); preset && old == 0 {
			return Conflict("这个预设题已被删除，不能恢复原编号；请从安装包内容新增副本，再保存为新题目", "question", old)
		}
		if old != revision {
			if old == 0 {
				return Conflict("题目已被删除，请刷新列表；如需重新添加，请创建新副本", "question", 0)
			}
			return Conflict("题目已被其他页面修改，请重新读取列表后保存", "question", old)
		}
		if q.Enabled && (q.Kind == "iq" || q.Kind == "knowledge") {
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM hub_questions WHERE user_id=? AND workspace_id=? AND question_id<>? AND json_extract(definition_json,'$.kind')=? AND json_extract(definition_json,'$.enabled')=1`, o.UserID, o.WorkspaceID, q.ID, q.Kind).Scan(&count); e != nil {
				return e
			}
			if count >= 32 {
				return Fail(422, "此分类最多启用32道题，请先禁用或删除其他题", "question")
			}
		}
		if old == 0 {
			_, e = tx.ExecContext(ctx, "INSERT INTO hub_questions VALUES(?,?,?,?,1)", o.UserID, o.WorkspaceID, q.ID, string(raw))
		} else {
			_, e = tx.ExecContext(ctx, "UPDATE hub_questions SET definition_json=?,revision=revision+1 WHERE user_id=? AND workspace_id=? AND question_id=? AND revision=?", string(raw), o.UserID, o.WorkspaceID, q.ID, revision)
		}
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	if e != nil {
		return q, e
	}
	q.Revision = revision + 1
	return q, nil
}
func (s *Store) DeleteQuestion(ctx context.Context, o Owner, id string, revision int) error {
	if revision < 1 {
		return Fail(422, "缺少题目版本，请重新读取题目后删除", "question")
	}
	if e := s.EnsureScope(ctx, o); e != nil {
		return e
	}
	if e := s.writable(); e != nil {
		return e
	}
	return retryWrite(ctx, func() error {
		res, e := s.db.ExecContext(ctx, "DELETE FROM hub_questions WHERE user_id=? AND workspace_id=? AND question_id=? AND revision=?", o.UserID, o.WorkspaceID, id, revision)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			var current int
			err := s.db.QueryRowContext(ctx, "SELECT revision FROM hub_questions WHERE user_id=? AND workspace_id=? AND question_id=?", o.UserID, o.WorkspaceID, id).Scan(&current)
			if errors.Is(err, sql.ErrNoRows) {
				return Conflict("题目已被删除，请刷新列表，未重复删除其他内容", "question", 0)
			}
			if err != nil {
				return err
			}
			return Conflict("题目已被其他页面修改，请刷新列表后再删除", "question", current)
		}
		return nil
	})
}
func errorSQL(err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	if strings.Contains(err.Error(), "hub_reports_one_active") {
		return Fail(409, "当前已有检测任务执行，请等待完成后再开始", "create")
	}
	return fmt.Errorf("检测数据库操作失败：%w；请检查磁盘空间、目录权限及是否被其他程序锁定", err)
}

var Now = time.Now

// RuntimeDB is borrowed only by the original reference-bank state adapter.
func (s *Store) RuntimeDB() *sql.DB { return s.db }
