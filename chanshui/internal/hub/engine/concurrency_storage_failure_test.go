package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

// A real SQLite SaveItem failure must cancel the other occupied slot before it
// can fetch more work, while preserving the response saved before the failure.
func TestConcurrentSaveFailureStopsWaitingQuestionsAndKeepsSavedAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o := store.Owner{UserID: "concurrent-save-fixture", WorkspaceID: "isolated"}
	settings, err := s.Settings(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Concurrency = 2
	if _, err = s.SaveSettings(ctx, o, settings); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := 0; i < 5; i++ {
		q := questions.Definition{ID: fmt.Sprintf("custom:%032x", i+1), Title: fmt.Sprintf("保存失败隔离题%d", i), Kind: "iq", Enabled: true, Order: i, Template: fmt.Sprintf("storage-request-%d", i), Parameters: map[string]any{"generator": "static"}, Judge: questions.Judge{Type: "manual"}}
		if _, err = s.SaveQuestion(ctx, o, q, 0, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, q.ID)
	}
	id := strings.Repeat("f", 32)
	c := Credential{BaseURL: "https://fixture.invalid", Key: "isolated-save-fixture-key", Protocol: "chat"}
	if _, err = s.CreateReport(ctx, o, store.CreateRequest{ReportID: id, TargetID: "isolated", Model: "fixture", SelectedQuestionIDs: ids, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	trigger := fmt.Sprintf(`CREATE TRIGGER refuse_second_answer BEFORE UPDATE OF result_json ON hub_items WHEN NEW.item_id='%s' BEGIN SELECT RAISE(ABORT,'isolated concurrent SaveItem unavailable'); END`, ids[1])
	if _, err = db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	firstSaved, secondStarted, thirdStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls, waitingCalls atomic.Int32
	var thirdCancelled atomic.Bool
	events := []string{}
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(raw), "storage-request-0"):
			return fixtureResponse(r, "first answer was durably saved"), nil
		case strings.Contains(string(raw), "storage-request-1"):
			close(secondStarted)
			select {
			case <-releaseSecond:
				return fixtureResponse(r, "second answer cannot be saved"), nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		case strings.Contains(string(raw), "storage-request-2"):
			close(thirdStarted)
			<-r.Context().Done()
			thirdCancelled.Store(true)
			return nil, r.Context().Err()
		default:
			waitingCalls.Add(1)
			return fixtureResponse(r, "waiting question must not start"), nil
		}
	})
	done, finished := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(finished)
		done <- RunOne(ctx, s, o, id, c, Config{DisableBankSync: true, Transport: fake, Notify: func(event string, data map[string]any) {
			events = append(events, event)
			if event == "item-saved" && data["itemId"] == ids[0] {
				close(firstSaved)
			}
		}})
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("storage failure fixture left execution workers running")
		}
	}()
	for _, started := range []<-chan struct{}{firstSaved, secondStarted, thirdStarted} {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("two occupied slots and first saved answer were not established", ctx.Err())
		}
	}
	before, err := s.ListItems(ctx, o, id, store.Filters{})
	if err != nil || len(before.Items) != 5 || before.Items[0].Status != "done" || before.Items[1].Status != "running" || before.Items[2].Status != "running" || before.Items[3].Status != "queued" || before.Items[4].Status != "queued" {
		t.Fatalf("fixture did not establish the real saved/in-flight/waiting state: %+v %v", before, err)
	}
	saved := string(before.Items[0].Result)
	if !strings.Contains(saved, "first answer was durably saved") || before.Items[0].Attempts != 1 {
		t.Fatal("first answer was not actually saved before the failure", before.Items[0])
	}
	close(releaseSecond)
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("SaveItem failure did not promptly cancel other workers", ctx.Err())
	}
	if err == nil || !strings.Contains(err.Error(), "isolated concurrent SaveItem unavailable") {
		t.Fatal("real SaveItem failure was not returned", err)
	}
	if calls.Load() != 3 || waitingCalls.Load() != 0 || !thirdCancelled.Load() {
		t.Fatalf("storage failure fetched waiting work or left another slot running: calls=%d waiting=%d cancelled=%v", calls.Load(), waitingCalls.Load(), thirdCancelled.Load())
	}
	after, err := s.ListItems(ctx, o, id, store.Filters{})
	if err != nil || len(after.Items) != 5 {
		t.Fatal(after, err)
	}
	if after.Items[0].Status != "done" || string(after.Items[0].Result) != saved || after.Items[0].Attempts != 1 {
		t.Fatal("SaveItem failure replaced a previously saved answer", after.Items[0])
	}
	if after.Items[1].Status != "running" || string(after.Items[1].Result) != "{}" || after.Items[1].Attempts != 1 {
		t.Fatal("failed SaveItem was falsely acknowledged", after.Items[1])
	}
	for _, item := range after.Items[3:] {
		if item.Status != "queued" || item.Attempts != 0 || item.StartedAtMs != 0 {
			t.Fatal("waiting question was started after the SaveItem failure", item)
		}
	}
	if strings.Contains(strings.Join(events, ","), "persisted") {
		t.Fatal("storage failure emitted a persisted completion event", events)
	}
	var result itemResult
	if err = json.Unmarshal(after.Items[2].Result, &result); err != nil || result.Judgment.Status != "unknown" || after.Items[2].Attempts != 1 {
		t.Fatal("cancelled in-flight response was misrepresented", after.Items[2], err)
	}
	report, err := s.Report(ctx, o, id)
	if err != nil || report.Status != "running" {
		t.Fatal("unconfirmed storage state was marked completed", report, err)
	}
}
