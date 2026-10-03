package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transithub/backend/internal/modules/settings"
)

func TestGlobalizationRestoreRejectsCorruptBusinessAmountsBeforeDatabaseAccess(t *testing.T) {
	plan, err := settings.PlanNotificationCleanup("user", "workspace", []settings.CleanupRow{{Table: "strategy_settings", Before: json.RawMessage(`{"defaultBalanceThreshold":700.25,"enableBalanceWarning":true,"balanceNotifyBotIds":["retired"]}`)}})
	if err != nil || len(plan.Rows) != 1 {
		t.Fatal("valid original fixture must contain one notification-only change")
	}
	plan.Rows[0].Before = json.RawMessage(`{"defaultBalanceThreshold":701.25,"enableBalanceWarning":true,"balanceNotifyBotIds":["retired"]}`)
	name := filepath.Base(filepath.Dir(t.TempDir())) + ".json"
	path, err := backupPath(name, true)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plan)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	// A malformed, credential-free URL cannot make any database connection. Backup
	// integrity must fail before connection setup, independent of current DB state.
	t.Setenv("DATABASE_URL", "invalid-connection-format")
	err = run(cleanupOptions{userID: "user", workspaceID: "workspace", restore: name, approved: true, digest: plan.Digest})
	if err == nil || !strings.Contains(err.Error(), "backup integrity") {
		t.Fatal("corrupt backup must be rejected by integrity validation before database setup")
	}
}

func TestGlobalizationSensitiveBackupUsesExistingIgnoredBackendCache(t *testing.T) {
	path, err := backupPath("sensitive-backup-path-contract.json", true)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "backend", "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("project root not found")
		}
		root = parent
	}
	expected := filepath.Join(root, "backend", ".cache", "notification-cleanup", "sensitive-backup-path-contract.json")
	if path != expected {
		t.Fatal("sensitive backups must remain inside the existing ignored backend cache")
	}
}
