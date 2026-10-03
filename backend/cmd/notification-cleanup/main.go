// notification-cleanup previews or explicitly applies a workspace-scoped cleanup.
// It is never invoked by service startup, migrations, tests, or deployment scripts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/settings"
)

type cleanupOptions struct {
	userID, workspaceID, digest, backup, restore string
	apply, approved                              bool
}

func main() {
	var opts cleanupOptions
	flag.StringVar(&opts.userID, "user-id", "", "explicit user ID")
	flag.StringVar(&opts.workspaceID, "admin-account-id", "", "explicit workspace ID")
	flag.BoolVar(&opts.apply, "apply", false, "write the reviewed cleanup in one transaction")
	flag.BoolVar(&opts.approved, "approved", false, "operator has separately approved this database operation")
	flag.StringVar(&opts.digest, "expected-digest", "", "digest from the reviewed preview")
	flag.StringVar(&opts.backup, "backup", "", "new backup filename under project backend/.cache/notification-cleanup")
	flag.StringVar(&opts.restore, "restore", "", "restore a backup under project backend/.cache/notification-cleanup")
	flag.Parse()
	if err := run(opts); err != nil {
		// Database/client errors can contain connection credentials. Do not print their text.
		if errors.Is(err, settings.ErrCleanupBackupIntegrity) {
			fmt.Fprintln(os.Stderr, "notification cleanup stopped: backup integrity validation failed")
		} else {
			fmt.Fprintln(os.Stderr, "notification cleanup stopped: validation, snapshot, transaction, or backup failed; no uncommitted changes were applied")
		}
		os.Exit(1)
	}
}

func validateOptions(opts cleanupOptions) error {
	if strings.TrimSpace(opts.userID) == "" || strings.TrimSpace(opts.workspaceID) == "" {
		return errors.New("workspace required")
	}
	write := opts.apply || opts.restore != ""
	if write && (!opts.approved || len(opts.digest) != 64) {
		return errors.New("separate approval and preview digest required")
	}
	if opts.apply && (opts.backup == "" || opts.restore != "") {
		return errors.New("fresh backup required")
	}
	if !write && (opts.approved || opts.backup != "" || opts.digest != "") {
		return errors.New("preview cannot accept write options")
	}
	return nil
}

func run(opts cleanupOptions) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	var restore settings.CleanupPlan
	if opts.restore != "" {
		path, err := backupPath(opts.restore, false)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe backup")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &restore) != nil {
			return settings.ErrCleanupBackupIntegrity
		}
		if err := settings.ValidateNotificationCleanupBackup(restore, opts.userID, opts.workspaceID, opts.digest); err != nil {
			return err
		}
	}
	var outputPath string
	if opts.apply {
		var err error
		outputPath, err = backupPath(opts.backup, true)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection := os.Getenv("DATABASE_URL")
	if connection == "" {
		return errors.New("DATABASE_URL required")
	}
	pool, err := pgxpool.New(ctx, connection)
	if err != nil {
		return err
	}
	defer pool.Close()
	readOnly := !opts.apply && opts.restore == ""
	txOpts := pgx.TxOptions{IsoLevel: pgx.Serializable}
	if readOnly {
		txOpts.AccessMode = pgx.ReadOnly
	}
	tx, err := pool.BeginTx(ctx, txOpts)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	snapshot, err := readSnapshot(ctx, tx, opts.userID, opts.workspaceID, !readOnly)
	if err != nil {
		return err
	}
	if opts.restore != "" {
		for _, change := range restore.Rows {
			found := false
			for _, row := range snapshot {
				if row.Table == change.Table && row.ID == change.ID {
					if !settings.SameCleanupJSON(row.Before, change.After) {
						return errors.New("rollback drift")
					}
					found = true
					break
				}
			}
			if !found {
				return errors.New("rollback row missing")
			}
			if err := updateRow(ctx, tx, opts.userID, opts.workspaceID, change, change.Before); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			RestoredRows int `json:"restoredRows"`
		}{len(restore.Rows)})
	}
	plan, err := settings.PlanNotificationCleanup(opts.userID, opts.workspaceID, snapshot)
	if err != nil {
		return err
	}
	if opts.apply {
		if plan.Digest != opts.digest {
			return errors.New("preview drift")
		}
		backup, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if _, err := file.Write(backup); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		for _, row := range plan.Rows {
			if err := updateRow(ctx, tx, opts.userID, opts.workspaceID, row, row.After); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	// Preview publishes only a digest and counts; rows and credentials never reach stdout.
	return json.NewEncoder(os.Stdout).Encode(struct {
		Applied bool                    `json:"applied"`
		Digest  string                  `json:"digest"`
		Summary settings.CleanupSummary `json:"summary"`
	}{opts.apply, plan.Digest, plan.Summary})
}

func readSnapshot(ctx context.Context, tx pgx.Tx, userID, workspaceID string, lock bool) ([]settings.CleanupRow, error) {
	queries := []struct{ table, query string }{
		{"notification_channel_settings", `SELECT '', settings FROM notification_channel_settings WHERE user_id=$1 AND admin_account_id=$2`},
		{"strategy_settings", `SELECT '', settings FROM strategy_settings WHERE user_id=$1 AND admin_account_id=$2`},
		{"group_rate_campaigns", `SELECT id, notify FROM group_rate_campaigns WHERE user_id=$1 AND admin_account_id=$2 ORDER BY id`},
		{"my_site_states", `SELECT '', mappings FROM my_site_states WHERE user_id=$1 AND admin_account_id=$2`},
		{"email_templates", `SELECT id, jsonb_build_object('id',id,'name',name,'subject',subject,'htmlBody',html_body,'isBuiltIn',is_builtin) FROM email_templates WHERE user_id=$1 AND admin_account_id=$2 ORDER BY id`},
	}
	result := make([]settings.CleanupRow, 0)
	for _, query := range queries {
		statement := query.query
		if lock {
			statement += " FOR UPDATE"
		}
		rows, err := tx.Query(ctx, statement, userID, workspaceID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			row := settings.CleanupRow{Table: query.table}
			if err := rows.Scan(&row.ID, &row.Before); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func updateRow(ctx context.Context, tx pgx.Tx, userID, workspaceID string, row settings.CleanupRow, raw []byte) error {
	var query string
	args := []any{userID, workspaceID, string(raw)}
	switch row.Table {
	case "notification_channel_settings":
		query = `UPDATE notification_channel_settings SET settings=$3::jsonb WHERE user_id=$1 AND admin_account_id=$2`
	case "strategy_settings":
		query = `UPDATE strategy_settings SET settings=$3::jsonb WHERE user_id=$1 AND admin_account_id=$2`
	case "my_site_states":
		query = `UPDATE my_site_states SET mappings=$3::jsonb WHERE user_id=$1 AND admin_account_id=$2`
	case "group_rate_campaigns":
		query = `UPDATE group_rate_campaigns SET notify=$3::jsonb WHERE user_id=$1 AND admin_account_id=$2 AND id=$4`
		args = append(args, row.ID)
	case "email_templates":
		var template settings.EmailTemplate
		if json.Unmarshal(raw, &template) != nil || template.ID != row.ID {
			return errors.New("invalid email backup")
		}
		query = `UPDATE email_templates SET html_body=$3 WHERE user_id=$1 AND admin_account_id=$2 AND id=$4`
		args[2] = template.HTMLBody
		args = append(args, row.ID)
	default:
		return errors.New("unsupported cleanup table")
	}
	result, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("cleanup row changed")
	}
	return nil
}

func backupPath(filename string, create bool) (string, error) {
	if filename == "" || filepath.Base(filename) != filename || !strings.HasSuffix(filename, ".json") {
		return "", errors.New("backup filename required")
	}
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", errors.New("project root unavailable")
		}
		root = parent
	}
	dir := root
	for _, part := range []string{"backend", ".cache", "notification-cleanup"} {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) && create {
			if err := os.Mkdir(dir, 0700); err != nil {
				return "", err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe backup directory")
		}
	}
	return filepath.Join(dir, filename), nil
}
