package upstream

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// 同步后回调必须带上写 syncing 之前的站点状态和成本状态。
func TestHomeCostStageDSyncCallbackCapturesPreSyncState(t *testing.T) {
	for _, tc := range []struct {
		status Status
		cost   string
	}{{StatusError, "ok"}, {StatusConnected, "unreadable"}, {StatusConnected, ""}} {
		t.Run(string(tc.status)+"/"+tc.cost, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/self":
					writeJSON(w, map[string]any{"data": map[string]any{"id": 1, "quota": 5000000}})
				case "/api/log/self/stat":
					writeJSON(w, map[string]any{"data": map[string]any{"quota": 500000}})
				default:
					writeJSON(w, map[string]any{"data": map[string]any{}})
				}
			}))
			defer server.Close()
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			cache := newSyncTestCache()
			svc := NewService(platform, nil, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			defer svc.Close()
			created, err := svc.Create(t.Context(), "user", CreateRequest{Name: "验收-恢复", SiteURL: server.URL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-only", RechargeRate: 1})
			if err != nil {
				t.Fatal(err)
			}
			site, _ := cache.Get(t.Context(), created.ID)
			site.Status = tc.status
			site.Metrics.TodayConsumeStatus = tc.cost
			if err := cache.Set(t.Context(), site); err != nil {
				t.Fatal(err)
			}
			// Reflect the callback so the old implementation still compiles and fails on business data.
			notices := make(chan []reflect.Value, 1)
			field := reflect.ValueOf(svc).Elem().FieldByName("AfterSync")
			field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value { notices <- args; return nil }))
			if _, err := svc.syncOnce(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case args := <-notices:
				if len(args) != 9 {
					t.Fatalf("callback has %d values, want pre/post site states in 9 values", len(args))
				}
				if old := args[5].Interface().(Metrics); old.TodayConsumeStatus != tc.cost {
					t.Errorf("old cost state=%q want=%q", old.TodayConsumeStatus, tc.cost)
				}
				if next := args[6].Interface().(Metrics); next.TodayConsumeStatus != "ok" {
					t.Errorf("new cost state=%q want ok", next.TodayConsumeStatus)
				}
				if args[7].Interface().(Status) != tc.status || args[8].Interface().(Status) != StatusConnected {
					t.Errorf("pre/post states=%v/%v want=%v/connected", args[7], args[8], tc.status)
				}
			case <-time.After(time.Second):
				t.Fatal("successful sync did not notify")
			}
		})
	}
}
