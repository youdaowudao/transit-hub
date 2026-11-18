package engine

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

// Fail SQLite writes at the actual statement boundary, after a paid response.
// A saved/finished notification must never be inferred from an in-memory answer.
func TestStorageFailureNeverClaimsPersistedOrRepeatsModel(t *testing.T) {
	for _, stage := range []string{"item", "finish"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "hub.sqlite")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			o := store.Owner{UserID: "storage-fixture", WorkspaceID: "storage-workspace"}
			id := strings.Repeat("d", 32)
			c := Credential{BaseURL: "https://fixture.invalid", Key: "isolated-storage-fixture-key", Protocol: "chat"}
			_, err = s.CreateReport(ctx, o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}, Target: store.Target{AccountID: "1", AccountName: "存储故障隔离验收", BaseURL: c.BaseURL, Protocol: c.Protocol}})
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			statement := `CREATE TRIGGER fixture_write_failure BEFORE UPDATE OF result_json ON hub_items BEGIN SELECT RAISE(ABORT,'isolated fixture: storage write unavailable'); END`
			if stage == "finish" {
				statement = `CREATE TRIGGER fixture_write_failure BEFORE UPDATE OF status ON hub_reports WHEN NEW.status IN ('done','partial','cancelled','interrupted') BEGIN SELECT RAISE(ABORT,'isolated fixture: storage write unavailable'); END`
			}
			if _, err = db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			calls, events := 0, []string{}
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return fixtureResponse(r, "最终答案: 开车去"), nil
			})
			err = RunOne(ctx, s, o, id, c, Config{DisableBankSync: true, Transport: fake, Notify: func(event string, _ map[string]any) { events = append(events, event) }})
			if err == nil || !strings.Contains(err.Error(), "storage write unavailable") || calls != 1 || strings.Contains(strings.Join(events, ","), "persisted") {
				t.Fatalf("unconfirmed save or paid retry: err=%v calls=%d events=%v", err, calls, events)
			}
			items, err := s.ListItems(ctx, o, id, store.Filters{})
			if err != nil || len(items.Items) != 1 {
				t.Fatal(items, err)
			}
			wantItem := "running"
			if stage == "finish" {
				wantItem = "done"
			}
			if items.Items[0].Status != wantItem {
				t.Fatalf("saved range changed: %s want %s", items.Items[0].Status, wantItem)
			}
			if _, err = db.Exec("DROP TRIGGER fixture_write_failure"); err != nil {
				t.Fatal(err)
			}
			if err = s.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := s.Report(ctx, o, id)
			if err != nil || r.Status != "interrupted" {
				t.Fatal(r, err)
			}
			if err = RunOne(ctx, s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err == nil || calls != 1 {
				t.Fatalf("recovery replayed paid request: err=%v calls=%d", err, calls)
			}
		})
	}
}
