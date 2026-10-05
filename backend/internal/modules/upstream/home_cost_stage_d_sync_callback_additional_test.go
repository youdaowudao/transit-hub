package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostStageDSyncCallbackKeepsBeginningMetricsAndRequiresPersistence(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, persistenceFails := range []bool{false, true} {
		name := "persisted"
		if persistenceFails {
			name = "persistence_failed"
		}
		t.Run(name, func(t *testing.T) {
			amount := 2.0
			oldAt := now.Add(-time.Minute)
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformNewAPI, UserID: "1", AccessToken: "fixture-only"})
			site.Platform = PlatformNewAPI
			site.Status = StatusError
			site.Metrics = Metrics{TodayConsume: metric(&amount), TodayConsumeStatus: "unreadable", TodayConsumeDate: businesstime.DateAt(now), TodayConsumeAt: &oldAt}
			cache := newSyncTestCache(site)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/self" {
					current, err := cache.Get(r.Context(), site.ID)
					if err != nil || current == nil || current.Status != StatusSyncing {
						t.Error("metrics request did not observe syncing")
					}
					// A local edit while the network request is in flight must not alter
					// what AfterSync reports as the beginning of this synchronization.
					changed := 9.0
					current.Metrics = Metrics{TodayConsume: metric(&changed), TodayConsumeStatus: "ok", TodayConsumeDate: businesstime.DateAt(now), TodayConsumeAt: &now}
					_ = cache.Set(r.Context(), current)
					writeJSON(w, map[string]any{"data": map[string]any{"id": 1, "quota": 5000000}})
					return
				}
				if r.URL.Path == "/api/log/self/stat" {
					writeJSON(w, map[string]any{"data": map[string]any{"quota": 1500000}})
					return
				}
				writeJSON(w, map[string]any{"data": map[string]any{}})
			}))
			defer server.Close()
			site.Session.BaseURL = server.URL
			_ = cache.Set(t.Context(), site)
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			repo := &homeCostCredentialRepository{}
			if persistenceFails {
				repo.saveErr = errors.New("fixture persistence failed")
			}
			svc := NewService(platform, repo, nil, cache)
			svc.now = func() time.Time { return now }
			defer svc.Close()
			notices := make(chan []reflect.Value, 1)
			field := reflect.ValueOf(svc).Elem().FieldByName("AfterSync")
			field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value { notices <- args; return nil }))
			_, err := svc.syncOnce(t.Context(), site.ID)
			if persistenceFails {
				if err == nil {
					t.Fatal("fixture persistence should fail")
				}
				select {
				case <-notices:
					t.Error("failed persistence notified a successful synchronization")
				case <-time.After(50 * time.Millisecond):
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case args := <-notices:
				old := args[5].Interface().(Metrics)
				if old.TodayConsume.Value == nil || *old.TodayConsume.Value != 2 || old.TodayConsumeStatus != "unreadable" || old.TodayConsumeAt == nil || !old.TodayConsumeAt.Equal(oldAt) {
					t.Error("callback reread old metrics after synchronization had started")
				}
				if !repo.committed {
					t.Error("callback ran before persistence")
				}
				if len(args) != 9 {
					t.Fatalf("callback has %d values, want 9", len(args))
				}
				if args[7].Interface().(Status) != StatusError || args[8].Interface().(Status) != StatusConnected {
					t.Error("callback lost beginning and final statuses")
				}
			case <-time.After(time.Second):
				t.Fatal("persisted synchronization did not notify")
			}
		})
	}
}
