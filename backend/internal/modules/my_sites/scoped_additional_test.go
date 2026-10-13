package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type scopedAdditionalRepo struct {
	statusCheckConnectionRepository
	snapshots  []ScopedConnectionSnapshot
	writes     int
	determined map[string]string
	applyErr   error
}

func (r *scopedAdditionalRepo) ReadScopedConnectionSnapshots(context.Context, string, string, ScopedConnectionScope) ([]ScopedConnectionSnapshot, error) {
	return append([]ScopedConnectionSnapshot{}, r.snapshots...), nil
}
func (r *scopedAdditionalRepo) ApplyScopedConnectionStatuses(_ context.Context, _ []ScopedConnectionSnapshot, determined map[string]string) (ScopedStatusApplyResult, error) {
	r.writes++
	r.determined = determined
	if r.applyErr != nil {
		return ScopedStatusApplyResult{}, r.applyErr
	}
	result := ScopedStatusApplyResult{}
	for id := range determined {
		result.ConfirmedConnectionIDs = append(result.ConfirmedConnectionIDs, id)
	}
	return result, nil
}

type scopedRoundTripper func(*http.Request) (*http.Response, error)

func (f scopedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func scopedResponse(payload string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
}
func scopedTestRow(id, account string) ScopedConnectionSnapshot {
	return ScopedConnectionSnapshot{ID: id, UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "site", UpstreamGroupID: "1", UpstreamGroupName: "Group", AdminAccountID: account, Status: ConnectionStatusActive, Xmin: "1"}
}
func scopedTestService(repo *scopedAdditionalRepo, session upstream.Session, transport scopedRoundTripper) (*Service, *testStateRepo) {
	state := &testStateRepo{state: &State{UserID: "u", AdminAccountID: "w", Session: session}}
	svc := NewService(state, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), nil)
	svc.connRepository = repo
	svc.SetAdminAccountResolver(testAdminResolver{currentID: "w"})
	return svc, state
}
func TestScopedCheckAdditionalReadOnlySessionAndFailures(t *testing.T) {
	for _, name := range []string{"expired still accepted", "verify rejected", "other platform", "other platform unauthenticated", "no candidates", "empty IDs", "invalid IDs"} {
		t.Run(name, func(t *testing.T) {
			row := scopedTestRow("a", "101")
			repo := &scopedAdditionalRepo{snapshots: []ScopedConnectionSnapshot{row}}
			expired := time.Now().Add(-time.Hour).UnixMilli()
			session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://scoped.invalid", AccessToken: "fixture-secret", ExpiresAt: &expired}
			if strings.HasPrefix(name, "other platform") {
				session.Platform = upstream.PlatformNewAPI
				if strings.HasSuffix(name, "unauthenticated") {
					session.AccessToken = ""
				}
			}
			if name == "no candidates" {
				repo.snapshots = nil
			}
			if name == "empty IDs" {
				repo.snapshots[0].AdminAccountID = ""
			}
			if name == "invalid IDs" {
				repo.snapshots[0].AdminAccountID = "101.5"
			}
			calls := 0
			svc, state := scopedTestService(repo, session, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/api/v1/auth/me" {
					if name == "verify rejected" {
						return scopedResponse(`{"message":"维护期间不可验证 token=fixture-secret"}`, 401), nil
					}
					return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
				}
				if r.URL.Path != "/api/v1/admin/accounts/101" {
					t.Error("unexpected refresh/full read")
				}
				return scopedResponse(`{"code":0,"data":{"id":101,"status":"disabled"}}`, 200), nil
			})
			result, err := svc.CheckScopedRealConnections(context.Background(), "u", ScopedCheckRequest{WorkspaceAdminAccountID: "w", SiteID: "site", Mode: "automatic"})
			if err != nil {
				t.Fatal(err)
			}
			if len(state.saves) != 0 {
				t.Fatal("scoped check wrote session state")
			}
			switch name {
			case "expired still accepted":
				if calls != 2 || repo.writes != 1 || len(result.ConfirmedConnectionIDs) != 1 {
					t.Fatal("valid expired token did not use read-only verification")
				}
			case "verify rejected":
				if calls != 1 || repo.writes != 0 || len(result.UnconfirmedConnectionIDs) != 1 {
					t.Fatal("verify failure altered statuses")
				}
			default:
				if calls != 0 || repo.writes != 0 {
					t.Fatal("unreadable scope/platform requested remote")
				}
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "fixture-secret") {
				t.Fatal("sensitive detail escaped")
			}
		})
	}
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("storage uncertainty %t", uncertain), func(t *testing.T) {
			repo := &scopedAdditionalRepo{snapshots: []ScopedConnectionSnapshot{scopedTestRow("a", "101")}, applyErr: &ScopedStorageError{Uncertain: uncertain, Cause: errors.New("fixture storage failure")}}
			svc, _ := scopedTestService(repo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://scoped.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/me") {
					return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
				}
				return scopedResponse(`{"code":0,"data":{"id":101}}`, 200), nil
			})
			result, err := svc.CheckScopedRealConnections(context.Background(), "u", ScopedCheckRequest{WorkspaceAdminAccountID: "w", SiteID: "site", Mode: "automatic"})
			var local *scopedRequestError
			if !errors.As(err, &local) || result.StorageUncertain != uncertain || len(result.ConfirmedConnectionIDs) != 0 || len(result.UnconfirmedConnectionIDs) != 1 {
				t.Fatal("storage failure lost conservative batch result")
			}
			want := 500
			if uncertain {
				want = 503
			}
			if local.Status != want {
				t.Fatalf("status=%d", local.Status)
			}
		})
	}
}
func TestScopedCheckAdditionalConcurrencyDeadlinesAndCancellation(t *testing.T) {
	repo := &scopedAdditionalRepo{snapshots: []ScopedConnectionSnapshot{scopedTestRow("a", "101"), scopedTestRow("duplicate", "101"), scopedTestRow("b", "102"), scopedTestRow("later", "103")}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, maxActive atomic.Int32
	started := make(chan struct{}, 2)
	var deadlinesMu sync.Mutex
	var verifyRemaining, detailRemaining time.Duration
	svc, _ := scopedTestService(repo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://scoped.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("read has no deadline")
		}
		if strings.HasSuffix(r.URL.Path, "/me") {
			deadlinesMu.Lock()
			verifyRemaining = time.Until(deadline)
			deadlinesMu.Unlock()
			return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
		}
		current := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); current > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		deadlinesMu.Lock()
		detailRemaining = time.Until(deadline)
		deadlinesMu.Unlock()
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	completed := make(chan ScopedCheckResult, 1)
	go func() {
		result, _ := svc.CheckScopedRealConnections(ctx, "u", ScopedCheckRequest{WorkspaceAdminAccountID: "w", SiteID: "site", Mode: "automatic"})
		completed <- result
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("two bounded reads not started")
		}
	}
	cancel()
	select {
	case result := <-completed:
		if result.Total != 4 || len(result.UnconfirmedConnectionIDs) != 4 || repo.writes != 0 {
			t.Fatal("cancel saved established scope or lost coverage")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled workers did not stop")
	}
	if maxActive.Load() != 2 || len(started) != 0 {
		t.Fatal("unexpected concurrency or queued read after cancellation")
	}
	if verifyRemaining < 19*time.Second || verifyRemaining > 20*time.Second || detailRemaining < 4*time.Second || detailRemaining > 5*time.Second {
		t.Fatalf("automatic deadlines verify=%v detail=%v", verifyRemaining, detailRemaining)
	}
}

func scopedAdditionalPGSetup(t *testing.T) (*Repository, func(string) (string, error)) {
	t.Helper()
	pool, repo := openMySitesCleanupPostgres(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE upstream_sites(id text,user_id text,admin_account_id text,name text,base_url text); CREATE TABLE group_rate_snapshots(id text,user_id text,admin_account_id text,site_id text,group_id text,group_name text,created_at timestamptz); INSERT INTO upstream_sites VALUES('site','u','w','Site','https://site.invalid'); INSERT INTO group_rate_snapshots VALUES('s','u','w','site','1','Group',now())`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := repo.SaveRealConnection(ctx, RealConnection{ID: id, UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "site", UpstreamGroupID: "1", UpstreamGroupName: "Group", AdminAccountID: "101", AdminPlatform: "sub2api", Status: ConnectionStatusActive, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	status := func(id string) (string, error) {
		var value string
		err := pool.QueryRow(ctx, `SELECT status FROM real_connections WHERE id=$1`, id).Scan(&value)
		return value, err
	}
	return repo, status
}
func TestScopedRepositoryAdditionalPostgres(t *testing.T) {
	t.Run("same-value confirmation prevents stale competing check", func(t *testing.T) {
		repo, status := scopedAdditionalPGSetup(t)
		ctx := context.Background()
		snapshots, err := repo.ReadScopedConnectionSnapshots(ctx, "u", "w", ScopedConnectionScope{SiteID: "site"})
		if err != nil {
			t.Fatal(err)
		}
		first, err := repo.ApplyScopedConnectionStatuses(ctx, snapshots, map[string]string{"a": ConnectionStatusActive})
		if err != nil || len(first.ConfirmedConnectionIDs) != 1 {
			t.Fatalf("first=%#v err=%v", first, err)
		}
		second, err := repo.ApplyScopedConnectionStatuses(ctx, snapshots, map[string]string{"a": ConnectionStatusMissing})
		if err != nil || len(second.ChangedConnectionIDs) != 1 || len(second.ConfirmedConnectionIDs) != 0 {
			t.Fatalf("stale=%#v err=%v", second, err)
		}
		value, err := status("a")
		if err != nil || value != ConnectionStatusActive {
			t.Fatal("stale writer overwrote same-value check")
		}
	})
	t.Run("rebinding deletion and inserted rows keep original values", func(t *testing.T) {
		repo, status := scopedAdditionalPGSetup(t)
		ctx := context.Background()
		snapshots, err := repo.ReadScopedConnectionSnapshots(ctx, "u", "w", ScopedConnectionScope{SiteID: "site"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(ctx, `UPDATE real_connections SET admin_account_id='202' WHERE id='a'; DELETE FROM real_connections WHERE id='b'`); err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveRealConnection(ctx, RealConnection{ID: "new", UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "site", AdminAccountID: "101", Status: ConnectionStatusActive, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		applied, err := repo.ApplyScopedConnectionStatuses(ctx, snapshots, map[string]string{"a": ConnectionStatusMissing, "b": ConnectionStatusMissing})
		if err != nil || len(applied.ChangedConnectionIDs) != 2 || len(applied.ConfirmedConnectionIDs) != 0 {
			t.Fatalf("identity change=%#v err=%v", applied, err)
		}
		for _, id := range []string{"a", "new"} {
			value, err := status(id)
			if err != nil || value != ConnectionStatusActive {
				t.Fatal("new/rebound row changed")
			}
		}
	})
	t.Run("one failed update rolls back every determined row", func(t *testing.T) {
		repo, status := scopedAdditionalPGSetup(t)
		ctx := context.Background()
		snapshots, err := repo.ReadScopedConnectionSnapshots(ctx, "u", "w", ScopedConnectionScope{SiteID: "site"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(ctx, `CREATE FUNCTION scoped_refuse_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='b' THEN RAISE EXCEPTION 'fixture refuses second update'; END IF; RETURN NEW; END $$; CREATE TRIGGER scoped_refuse BEFORE UPDATE OF status ON real_connections FOR EACH ROW EXECUTE FUNCTION scoped_refuse_update()`); err != nil {
			t.Fatal(err)
		}
		applied, err := repo.ApplyScopedConnectionStatuses(ctx, snapshots, map[string]string{"a": ConnectionStatusMissing, "b": ConnectionStatusMissing})
		var storage *ScopedStorageError
		if !errors.As(err, &storage) || storage.Uncertain || len(applied.ConfirmedConnectionIDs) != 0 {
			t.Fatal("rollback was reported as committed")
		}
		for _, id := range []string{"a", "b"} {
			value, err := status(id)
			if err != nil || value != ConnectionStatusActive {
				t.Fatal("part of atomic batch escaped rollback")
			}
		}
	})
	t.Run("page scope and historic blank IDs do not expand", func(t *testing.T) {
		repo, _ := scopedAdditionalPGSetup(t)
		ctx := context.Background()
		if _, err := repo.db.Exec(ctx, `UPDATE real_connections SET upstream_group_id='',upstream_group_name='Other' WHERE id='b'; UPDATE group_rate_snapshots SET group_id='' WHERE id='s'; UPDATE real_connections SET upstream_group_id='',status='missing' WHERE id='a'; DELETE FROM upstream_sites`); err != nil {
			t.Fatal(err)
		}
		refs := []ScopedGroupRef{{SiteID: "site", GroupName: "Group"}}
		items, err := repo.ListScopedRealConnections(ctx, "u", "w", refs)
		if err != nil || len(items) != 1 || items[0].ID != "a" || items[0].Status != ConnectionStatusMissing {
			t.Fatalf("scoped missing=%#v err=%v", items, err)
		}
		snapshots, err := repo.ReadScopedConnectionSnapshots(ctx, "u", "w", ScopedConnectionScope{GroupRefs: refs})
		if err != nil || len(snapshots) != 1 || snapshots[0].ID != "a" {
			t.Fatal("blank ID widened check candidates")
		}
		_, err = repo.ListScopedRealConnections(ctx, "u", "other", refs)
		var scoped *scopedRequestError
		if !errors.As(err, &scoped) || scoped.Status != 404 {
			t.Fatal("other workspace ownership did not fail closed")
		}
		_, err = repo.ListScopedRealConnections(ctx, "u", "w", append(refs, ScopedGroupRef{SiteID: "unknown", GroupName: "Group"}))
		if !errors.As(err, &scoped) {
			t.Fatal("mixed invalid batch silently filtered")
		}
	})
}

func TestScopedManualAdditionalDeadlines(t *testing.T) {
	repo := &scopedAdditionalRepo{snapshots: []ScopedConnectionSnapshot{scopedTestRow("a", "101")}}
	var verifyRemaining, detailRemaining time.Duration
	svc, _ := scopedTestService(repo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://scoped.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("manual read has no deadline")
		}
		if strings.HasSuffix(r.URL.Path, "/me") {
			verifyRemaining = time.Until(deadline)
			return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
		}
		detailRemaining = time.Until(deadline)
		return scopedResponse(`{"code":0,"data":{"id":101}}`, 200), nil
	})
	result, err := svc.CheckScopedRealConnections(context.Background(), "u", ScopedCheckRequest{WorkspaceAdminAccountID: "w", SiteID: "site", Mode: "manual"})
	if err != nil || len(result.ConfirmedConnectionIDs) != 1 || repo.writes != 1 {
		t.Fatalf("manual result=%#v err=%v", result, err)
	}
	if verifyRemaining < 44*time.Second || verifyRemaining > 45*time.Second || detailRemaining < 9*time.Second || detailRemaining > 10*time.Second {
		t.Fatalf("manual deadlines verify=%v detail=%v", verifyRemaining, detailRemaining)
	}
}
func TestScopedBatchDeadlineAdditionalSavesEstablishedResults(t *testing.T) {
	repo := &scopedAdditionalRepo{}
	for i := 101; i <= 110; i++ {
		repo.snapshots = append(repo.snapshots, scopedTestRow(fmt.Sprint(i), fmt.Sprint(i)))
	}
	var reads atomic.Int32
	svc, _ := scopedTestService(repo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://scoped.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/me") {
			return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
		}
		reads.Add(1)
		if strings.HasSuffix(r.URL.Path, "/101") {
			return scopedResponse(`{"code":0,"data":{"id":101}}`, 200), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	began := time.Now()
	result, err := svc.CheckScopedRealConnections(context.Background(), "u", ScopedCheckRequest{WorkspaceAdminAccountID: "w", SiteID: "site", Mode: "automatic"})
	elapsed := time.Since(began)
	if err != nil || repo.writes != 1 || len(result.ConfirmedConnectionIDs) != 1 || result.ConfirmedConnectionIDs[0] != "101" || len(result.UnconfirmedConnectionIDs) != 9 || repo.determined["101"] != ConnectionStatusActive {
		t.Fatalf("batch deadline discarded established result: confirmed=%v unconfirmed=%d writes=%d err=%v", result.ConfirmedConnectionIDs, len(result.UnconfirmedConnectionIDs), repo.writes, err)
	}
	if elapsed < 19*time.Second || elapsed > 23*time.Second || reads.Load() > 9 {
		t.Fatalf("batch deadline did not stop queued work elapsed=%v reads=%d", elapsed, reads.Load())
	}
}
