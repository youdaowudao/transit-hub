// Package credentialstore contains startup-only verification and in-place encoding.
// It does not initialize tables or change the ordinary repository request paths.
package credentialstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"transithub/backend/internal/shared/secretbox"
)

type Postgres interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type credentialColumn struct {
	table, column, workspace, object string
	json                             bool
}

var columns = []credentialColumn{
	{"my_site_states", "session", "admin_account_id", "", true},
	{"upstream_sites", "session", "admin_account_id", "id", true},
	{"real_connections", "upstream_key", "workspace_admin_account_id", "id", false},
	{"notification_channel_settings", "settings", "admin_account_id", "", true},
}

func tableExists(ctx context.Context, db Postgres, table string) (bool, error) {
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		return false, storageError("核对已有表", table, "", "", "", err)
	}
	return exists, nil
}

func VerifyPostgres(ctx context.Context, db Postgres, box *secretbox.Box) error {
	return visitPostgres(ctx, db, box, false, "")
}
func EncryptPostgres(ctx context.Context, db Postgres, box *secretbox.Box) error {
	if box == nil {
		return nil
	}
	return visitPostgres(ctx, db, box, true, "")
}

type storedCredential struct{ user, workspace, object, raw string }

// VerifyPostgresTable protects an existing schema initializer that reassigns ownership.
func VerifyPostgresTable(ctx context.Context, db Postgres, box *secretbox.Box, table string) error {
	return visitPostgres(ctx, db, box, false, table)
}

func visitPostgres(ctx context.Context, db Postgres, box *secretbox.Box, convert bool, onlyTable string) error {
	for _, column := range columns {
		if onlyTable != "" && column.table != onlyTable {
			continue
		}
		exists, err := tableExists(ctx, db, column.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		objectExpr := `''::text`
		if column.object != "" {
			objectExpr = column.object
		}
		var last storedCredential
		first := true
		for {
			order := "user_id," + column.workspace
			cursor := "($1,$2)"
			firstParameter := "$3"
			args := []any{last.user, last.workspace, first}
			if column.object != "" {
				order += "," + column.object
				cursor = "($1,$2,$3)"
				firstParameter = "$4"
				args = []any{last.user, last.workspace, last.object, first}
			}
			query := fmt.Sprintf(`SELECT user_id, %s, %s, %s::text FROM %s WHERE %s IS NOT NULL AND (%s OR (%s)>%s) ORDER BY %s LIMIT 100`, column.workspace, objectExpr, column.column, column.table, column.column, firstParameter, order, cursor, order)
			rows, err := db.Query(ctx, query, args...)
			if err != nil {
				return storageError("分批读取", column.table, last.user, last.workspace, last.object, err)
			}
			batch := make([]storedCredential, 0, 100)
			for rows.Next() {
				var item storedCredential
				if err := rows.Scan(&item.user, &item.workspace, &item.object, &item.raw); err != nil {
					rows.Close()
					return storageError("读取行", column.table, item.user, item.workspace, item.object, err)
				}
				batch = append(batch, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return storageError("读取完整批次", column.table, last.user, last.workspace, last.object, err)
			}
			for _, item := range batch {
				location := column.table + "." + column.column
				sealed := secretbox.IsSealed(item.raw)
				if column.json {
					sealed = secretbox.IsSealedJSON([]byte(item.raw))
				}
				if sealed && (item.user == "" || item.workspace == "") {
					return secretbox.At(location, item.user, item.workspace, item.object, errors.New("已加密记录缺少归属，不能自动重新分配；请恢复原归属及原密钥后重试，本次未改动这条记录"))
				}
				aad := secretbox.AAD(location, item.user, item.workspace)
				if column.object != "" {
					aad = secretbox.AAD(location, item.user, item.workspace, item.object)
				}
				var plain []byte
				if column.json {
					plain, err = box.OpenJSON([]byte(item.raw), aad)
				} else {
					plain, err = box.Open(item.raw, aad)
				}
				if err != nil {
					return secretbox.At(location, item.user, item.workspace, item.object, err)
				}
				if column.json && !json.Valid(plain) {
					return secretbox.At(location, item.user, item.workspace, item.object, errors.New("解密后的 JSON 内容损坏；请核对原备份后重试，本次未覆盖该记录"))
				}
				if !convert || sealed || (!column.json && item.raw == "") {
					continue
				}
				if item.user == "" || item.workspace == "" {
					return secretbox.At(location, item.user, item.workspace, item.object, errors.New("旧记录仍缺少归属，不能确定加密位置；请先核查工作区分配后重试，本次未加密这条记录"))
				}
				var replacement string
				if column.json {
					var encoded []byte
					encoded, err = box.SealJSON(plain, aad)
					replacement = string(encoded)
				} else {
					replacement, err = box.Seal(plain, aad)
				}
				if err != nil {
					return secretbox.At(location, item.user, item.workspace, item.object, err)
				}
				cast := ""
				if column.json {
					cast = "::jsonb"
				}
				update := fmt.Sprintf(`UPDATE %s SET %s=$3%s WHERE user_id=$1 AND %s=$2 AND %s=$4%s`, column.table, column.column, cast, column.workspace, column.column, cast)
				args := []any{item.user, item.workspace, replacement, item.raw}
				if column.object != "" {
					update = fmt.Sprintf(`UPDATE %s SET %s=$4%s WHERE user_id=$1 AND %s=$2 AND %s=$3 AND %s=$5%s`, column.table, column.column, cast, column.workspace, column.object, column.column, cast)
					args = []any{item.user, item.workspace, item.object, replacement, item.raw}
				}
				result, err := db.Exec(ctx, update, args...)
				if err != nil {
					return storageError("条件转换", location, item.user, item.workspace, item.object, err)
				}
				if result.RowsAffected() != 1 {
					return secretbox.At(location, item.user, item.workspace, item.object, errors.New("记录已被其他请求修改或删除，本次未覆盖；请停止其他旧版本写入实例后重试，已完成转换的记录可继续读取"))
				}
			}
			if len(batch) < 100 {
				break
			}
			last = batch[len(batch)-1]
			first = false
		}
	}
	return nil
}

// KnownSiteReferences identifies existing Redis indexes without scanning its keyspace.
func KnownSiteReferences(ctx context.Context, db Postgres) (users, sites []string, err error) {
	queries := []struct {
		table, query string
		site         bool
	}{
		{"users", `SELECT id FROM users`, false},
		{"upstream_sites", `SELECT DISTINCT user_id FROM upstream_sites`, false},
		{"upstream_sites", `SELECT id FROM upstream_sites`, true},
	}
	seen := map[string]struct{}{}
	for _, query := range queries {
		exists, checkErr := tableExists(ctx, db, query.table)
		if checkErr != nil {
			return nil, nil, checkErr
		}
		if !exists {
			continue
		}
		rows, readErr := db.Query(ctx, query.query)
		if readErr != nil {
			return nil, nil, storageError("读取已有缓存归属", query.table, "", "", "", readErr)
		}
		for rows.Next() {
			var id string
			if readErr = rows.Scan(&id); readErr != nil {
				rows.Close()
				return nil, nil, storageError("读取已有缓存编号", query.table, "", "", "", readErr)
			}
			if query.site {
				sites = append(sites, id)
			} else if _, ok := seen[id]; !ok {
				users = append(users, id)
				seen[id] = struct{}{}
			}
		}
		readErr = rows.Err()
		rows.Close()
		if readErr != nil {
			return nil, nil, storageError("读取完整缓存归属", query.table, "", "", "", readErr)
		}
	}
	return users, sites, nil
}

func storageError(step, location, user, workspace, object string, cause error) error {
	reason := "数据库未完成读取或保存；请检查数据库连接和服务端日志后重试"
	if errors.Is(cause, context.DeadlineExceeded) {
		reason = "操作超过允许时限；请检查数据库负载并停止其他旧版本写入实例后重试"
	}
	if errors.Is(cause, context.Canceled) {
		reason = "操作已取消；请确认服务启动条件后重试"
	}
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		reason = fmt.Sprintf("数据库拒绝操作（SQLSTATE %s）；请检查该表结构、访问权限和数据库日志后重试", pgErr.Code)
	}
	return secretbox.At(location, user, workspace, object, fmt.Errorf("%s未完成：%s；启动已停止，不会继续恢复业务或覆盖未核验内容", step, reason))
}
