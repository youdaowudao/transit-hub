package signup_risk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/upstream"
)

type riskStore struct {
	mu        sync.Mutex
	targets   []notifications.SignupRiskTarget
	processed map[string]bool
	notified  map[string]int
	commits   int
}

func riskStoreKey(target notifications.SignupRiskTarget, id string) string {
	return target.AdminAccountID + ":" + notifications.SignupRiskSiteKey(target.BaseURL) + ":" + id
}
func newRiskStore() *riskStore {
	return &riskStore{targets: []notifications.SignupRiskTarget{{SignupRiskSchedule: riskActiveSchedule(), UserID: "user", AdminAccountID: "workspace", BaseURL: "https://fixture.invalid", SiteName: "隔离站点", UpdatedAt: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}}, processed: map[string]bool{}, notified: map[string]int{}}
}
func (f *riskStore) ListSignupRiskTargets(context.Context) ([]notifications.SignupRiskTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notifications.SignupRiskTarget(nil), f.targets...), nil
}
func (f *riskStore) ProcessedSignupRiskIDs(_ context.Context, target notifications.SignupRiskTarget, ids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = f.processed[riskStoreKey(target, id)]
	}
	return out, nil
}
func (f *riskStore) CommitSignupRiskRound(_ context.Context, target notifications.SignupRiskTarget, users []notifications.SignupRiskUser, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	for _, user := range users {
		key := riskStoreKey(target, user.ID)
		if f.processed[key] {
			continue
		}
		f.processed[key] = true
		if target.Initialized {
			f.notified[key]++
		}
	}
	for i := range f.targets {
		if f.targets[i].AdminAccountID == target.AdminAccountID {
			f.targets[i].Initialized = true
		}
	}
	return nil
}

type riskSessions struct{}

func (riskSessions) StoredSession(_ context.Context, _ string, workspace string) (upstream.Session, bool, error) {
	base := "https://fixture.invalid"
	if workspace == "slow" {
		base = "https://slow.invalid"
	}
	return upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: base, AdminAPIKey: "isolated-risk-fixture"}, true, nil
}

type riskTransport func(*http.Request) (*http.Response, error)

func (f riskTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type riskRemote struct {
	mu       sync.Mutex
	rows     []map[string]any
	calls    []int
	failPage int
}

func (f *riskRemote) client(t *testing.T) *upstream.PlatformService {
	t.Helper()
	return upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: riskTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/users" || r.URL.Query().Get("search") != "[注册风险:高]" || r.URL.Query().Get("include_subscriptions") != "false" || r.URL.Query().Get("page_size") != "50" {
			t.Error("poll made a request outside the read-only handoff")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, page)
		if page == f.failPage {
			return nil, errors.New("fixture transport disconnected")
		}
		start := (page - 1) * 50
		end := start + 50
		if end > len(f.rows) {
			end = len(f.rows)
		}
		if start > end {
			start = end
		}
		pages := (len(f.rows) + 49) / 50
		if pages == 0 {
			pages = 1
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"items": f.rows[start:end], "total": len(f.rows), "page": page, "page_size": 50, "pages": pages}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}))
}
func riskRow(id int, created time.Time, marked bool) map[string]any {
	notes := ""
	if marked {
		notes = "[注册风险:高] 含+别名"
	}
	return map[string]any{"id": id, "email": fmt.Sprintf("signup-risk-%d+1@example.com", id), "username": "[注册风险:高]", "notes": notes, "created_at": created.Format(time.RFC3339Nano)}
}
func riskRows(first, count int, created time.Time) []map[string]any {
	out := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, riskRow(first-i, created.Add(-time.Duration(i)*time.Second), true))
	}
	return out
}

func TestSignupRiskPollingBootstrapPaginationRestartAndDedup(t *testing.T) {
	store := newRiskStore()
	remote := &riskRemote{rows: riskRows(130, 130, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC))}
	svc := NewService(store, riskSessions{}, remote.client(t))
	if store.commits != 0 || len(remote.calls) != 0 {
		t.Fatal("constructor started background work")
	}
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.processed) != 130 || len(store.notified) != 0 || len(remote.calls) != 3 {
		t.Fatal("first multi-page scan must be silent and complete")
	}
	remote.rows = append(riskRows(245, 115, time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC)), remote.rows...)
	remote.calls = nil
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 115 || len(remote.calls) != 3 {
		t.Fatalf("multi-page new registrations lost: notified=%d pages=%v", len(store.notified), remote.calls)
	}
	restarted := NewService(store, riskSessions{}, remote.client(t))
	for i := 0; i < 2; i++ {
		if err := restarted.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.notified) != 115 {
		t.Fatal("repeat or restart duplicated notifications")
	}
	for _, count := range store.notified {
		if count != 1 {
			t.Fatal("one user notified twice")
		}
	}
}

func TestSignupRiskEmptyBootstrapAndUsernameSpoofNeverStopsPagination(t *testing.T) {
	store := newRiskStore()
	remote := &riskRemote{rows: []map[string]any{}}
	svc := NewService(store, riskSessions{}, remote.client(t))
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !store.targets[0].Initialized || len(store.notified) != 0 {
		t.Fatal("empty baseline did not initialize silently")
	}
	now := time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		row := riskRow(200-i, now.Add(-time.Duration(i)*time.Second), false)
		remote.rows = append(remote.rows, row)
		store.processed[riskStoreKey(store.targets[0], strconv.Itoa(200-i))] = true
	}
	remote.rows = append(remote.rows, riskRow(100, now.Add(-time.Minute), true))
	remote.calls = nil
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 1 || len(remote.calls) != 2 {
		t.Fatal("spoofed marker or previously processed spoof ID stopped the real second page")
	}
}

func TestSignupRiskSameRegistrationTimeAcrossPages(t *testing.T) {
	store := newRiskStore()
	store.targets[0].Initialized = true
	now := time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC)
	store.processed[riskStoreKey(store.targets[0], "1")] = true
	remote := &riskRemote{rows: []map[string]any{riskRow(1, now, true)}}
	for i := 2; i <= 62; i++ {
		remote.rows = append(remote.rows, riskRow(i, now, true))
	}
	remote.rows = append(remote.rows, riskRow(99, now.Add(-time.Second), true))
	if err := NewService(store, riskSessions{}, remote.client(t)).RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 61 || len(remote.calls) != 2 {
		t.Fatal("same-time new users behind a processed first-page ID were lost")
	}
	if store.processed[riskStoreKey(store.targets[0], "99")] {
		t.Fatal("scan advanced into an older time group")
	}
}

func TestSignupRiskSecondPageFailureDoesNotAdvanceAndRetryLosesNobody(t *testing.T) {
	store := newRiskStore()
	store.targets[0].Initialized = true
	remote := &riskRemote{rows: riskRows(75, 75, time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC)), failPage: 2}
	svc := NewService(store, riskSessions{}, remote.client(t))
	if err := svc.RunOnce(t.Context()); err == nil {
		t.Fatal("failed second page was hidden")
	}
	if store.commits != 0 || len(store.processed) != 0 || len(store.notified) != 0 {
		t.Fatal("partial first page advanced past unprocessed users")
	}
	remote.failPage = 0
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 75 {
		t.Fatal("retry lost registrations")
	}
}

func TestSignupRiskStaleFullPageTotalCannotLoseOlderNewRegistrations(t *testing.T) {
	store := newRiskStore()
	store.targets[0].Initialized = true
	initial := riskRows(100, 100, time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC))
	rows := initial
	inserted := false
	var calls []int
	client := upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: riskTransport(func(r *http.Request) (*http.Response, error) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		calls = append(calls, page)
		stale := false
		if page == 2 && !inserted {
			// Sub2API counts before reading the offset page. Ten fresh rows
			// can shift its tail even when this response still says two pages.
			rows = append(riskRows(110, 10, time.Date(2031, 1, 3, 0, 0, 0, 0, time.UTC)), initial...)
			inserted = true
			stale = true
		}
		start := (page - 1) * 50
		end := min(start+50, len(rows))
		start = min(start, end)
		total := len(rows)
		if stale {
			total = 100
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"items": rows[start:end], "total": total, "page": page, "page_size": 50, "pages": max(1, (total+49)/50)}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}))
	svc := NewService(store, riskSessions{}, client)
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 100 {
		t.Errorf("full last-page metadata lost users shifted to its tail: got %d want 100", len(store.notified))
	}
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 110 {
		t.Errorf("later known-ID stop permanently hid registrations: got %d want 110", len(store.notified))
	}
}

type riskClientFunc func(context.Context, upstream.Session, upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error)

func (f riskClientFunc) FetchSub2APIAdminUsersPageWithContext(ctx context.Context, s upstream.Session, q upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error) {
	return f(ctx, s, q)
}
func TestSignupRiskSlowWorkspaceDoesNotDelayHealthyWorkspaceAndShutdown(t *testing.T) {
	store := newRiskStore()
	slow := store.targets[0]
	slow.AdminAccountID = "slow"
	slow.BaseURL = "https://slow.invalid"
	store.targets = append([]notifications.SignupRiskTarget{slow}, store.targets...)
	slowStarted := make(chan struct{})
	healthyDone := make(chan struct{})
	var healthyOnce sync.Once
	client := riskClientFunc(func(ctx context.Context, s upstream.Session, q upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error) {
		if s.BaseURL == slow.BaseURL {
			close(slowStarted)
			<-ctx.Done()
			return upstream.Sub2APIAdminUsersPage{}, ctx.Err()
		}
		healthyOnce.Do(func() { close(healthyDone) })
		return upstream.Sub2APIAdminUsersPage{Items: []upstream.Sub2APIAdminUser{}, Page: 1, PageSize: 50, Pages: 1, TotalKnown: true, PagesKnown: true}, nil
	})
	svc := NewService(store, riskSessions{}, client)
	svc.Start(t.Context())
	svc.Start(t.Context())
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow workspace never started")
	}
	select {
	case <-healthyDone:
	case <-time.After(time.Second):
		t.Fatal("slow workspace blocked healthy workspace")
	}
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	} // an in-flight round is not duplicated
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatal("shutdown did not cancel active GET")
	}
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatal("shutdown is not idempotent")
	}
}

func TestSignupRiskMissingIdentityAndMalformedRiskFactNeverAdvance(t *testing.T) {
	for _, tc := range []struct{ field, reason string }{{"id", "缺少编号"}, {"email", "注册邮箱为空"}, {"created_at", "注册时间缺失"}} {
		t.Run(tc.field, func(t *testing.T) {
			store := newRiskStore()
			row := riskRow(1, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), true)
			delete(row, tc.field)
			remote := &riskRemote{rows: []map[string]any{row}}
			if err := NewService(store, riskSessions{}, remote.client(t)).RunOnce(t.Context()); err == nil || !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), "本轮没有") || !strings.Contains(err.Error(), "重试") {
				t.Fatalf("incomplete risk fact must identify its actual cause, result and retry: %v", err)
			}
			if store.commits != 0 {
				t.Fatal("invalid risk fact advanced initialization")
			}
		})
	}
}

func riskActiveSchedule() notifications.SignupRiskSchedule {
	zone := "UTC"
	if time.Now().UTC().Hour() == 23 {
		zone = "Asia/Singapore"
	}
	return notifications.SignupRiskSchedule{WorkStart: "00:00", WorkEnd: "23:59", TimeZone: zone}
}
