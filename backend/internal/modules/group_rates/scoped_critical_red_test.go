package group_rates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

type scopedCriticalResolver struct{}

func (scopedCriticalResolver) RequireCurrentID(context.Context, string) (string, error) {
	return "workspace-a", nil
}

func TestScopedRatesCriticalHTTPPostgres(t *testing.T) {
	pool := openPostgresTestPool(t)
	repository := prepareGroupRatesRepository(t, pool)
	ctx := context.Background()
	for i := 1; i <= 65; i++ {
		_, err := pool.Exec(ctx, `INSERT INTO group_rate_snapshots(id,user_id,admin_account_id,site_id,site_name,group_id,group_name,platform,type,multiplier,created_at,deleted) VALUES($1,'user-a','workspace-a','site-a','Site A',$2,$3,'sub2api','openai',$4,now(),false)`, fmt.Sprintf("a-%03d", i), fmt.Sprint(i), fmt.Sprintf("Group %03d", i), float64(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO group_rate_snapshots(id,user_id,admin_account_id,site_id,site_name,group_id,group_name,platform,type,multiplier,created_at,deleted) VALUES('b-1','user-a','workspace-a','site-b','Site B','1','Other group','newapi','gemini',0.1,now(),false)`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewService(repository), scopedCriticalResolver{})
	read := func(query string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/group-rates?workspaceAdminAccountId=workspace-a&status=all&sort=multiplierAsc&"+query, nil)
		r = r.WithContext(authctx.WithUserID(r.Context(), "user-a"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("list status=%d", w.Code)
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("one site page and global facets", func(t *testing.T) {
		v := read("siteId=site-a&page=1")
		if v["total"] != float64(65) {
			t.Fatalf("site total=%v want65", v["total"])
		}
		items := v["items"].([]any)
		if len(items) != 30 {
			t.Fatalf("rows=%d", len(items))
		}
		for _, item := range items {
			if item.(map[string]any)["siteId"] != "site-a" {
				t.Fatal("other site leaked into scoped list")
			}
		}
		if len(v["types"].([]any)) != 2 {
			t.Fatal("site filter lost global facets")
		}
	})
	t.Run("target directly selects page three", func(t *testing.T) {
		v := read("siteId=site-a&focusGroupId=61&page=1")
		if v["page"] != float64(3) || v["focusFound"] != true {
			t.Fatalf("page=%v focus=%v", v["page"], v["focusFound"])
		}
		if v["items"].([]any)[0].(map[string]any)["groupId"] != "61" {
			t.Fatal("target not on returned page")
		}
	})
	t.Run("oversized page returns last with metadata", func(t *testing.T) {
		v := read("siteId=site-a&page=999")
		if v["page"] != float64(3) || v["total"] != float64(65) || len(v["items"].([]any)) != 5 {
			t.Fatalf("invalid final-page metadata page=%v total=%v", v["page"], v["total"])
		}
	})
	t.Run("empty page retains metadata and local status counts", func(t *testing.T) {
		v := read("siteId=site-a&type=gemini&page=999")
		if v["page"] != float64(1) || v["total"] != float64(0) || v["totalPages"] != float64(0) || len(v["types"].([]any)) != 2 {
			t.Fatalf("invalid empty metadata page=%v total=%v", v["page"], v["total"])
		}
		if v["statusCounts"].(map[string]any)["all"] != float64(0) {
			t.Fatal("empty scoped counts include other site")
		}
	})
	t.Run("ambiguous names and hidden or renamed targets keep the current filter", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE group_rate_snapshots SET group_name='Repeated' WHERE site_id='site-a' AND group_id IN ('61','62')`); err != nil {
			t.Fatal(err)
		}
		ambiguous := read("siteId=site-a&focusGroup=Repeated&page=999")
		if ambiguous["focusFound"] != false || ambiguous["page"] != float64(1) || ambiguous["total"] != float64(65) {
			t.Fatal("same-name ambiguity arbitrarily selected a group")
		}
		renamed := read("siteId=site-a&focusGroupId=61&focusGroup=OldName&page=1")
		if renamed["focusFound"] != true || renamed["page"] != float64(3) || renamed["items"].([]any)[0].(map[string]any)["groupName"] != "Repeated" {
			t.Fatal("ID did not keep priority after a name changed")
		}
		hidden := read("siteId=site-a&focusGroupId=61&type=gemini&page=1")
		if hidden["focusFound"] != false || hidden["total"] != float64(0) || hidden["page"] != float64(1) {
			t.Fatal("focus cleared the current type filter to expose its target")
		}
		gone := read("siteId=site-a&focusGroup=OldName&page=1")
		if gone["focusFound"] != false || gone["total"] != float64(65) || gone["page"] != float64(1) {
			t.Fatal("missing old name did not retain the normal first page")
		}
	})
}

func TestScopedRatesCriticalBlankIDsPostgres(t *testing.T) {
	pool := openPostgresTestPool(t)
	repository := prepareGroupRatesRepository(t, pool)
	insertSnapshot(t, pool, "workspace-a", "")
	insertRealConnection(t, pool, "workspace-a", "", "different-name")
	v, err := repository.List(context.Background(), "user-a", "workspace-a", ListQuery{Page: 1, PageSize: 30, Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Items) != 1 || v.Items[0].Mapped || v.StatusCounts.Mapped != 0 {
		t.Fatal("blank IDs incorrectly connect different names")
	}
}

func TestScopedRatesRetainedHistoryPostgres(t *testing.T) {
	pool := openPostgresTestPool(t)
	repo := prepareGroupRatesRepository(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO upstream_sites(id,user_id,admin_account_id,base_url,recharge_rate) VALUES('site-a','user-a','workspace-a','https://shared.invalid',2),('site-b','user-a','workspace-a','https://shared.invalid',3)`); err != nil {
		t.Fatal(err)
	}
	for i, site := range []string{"site-a", "site-b"} {
		if _, err := pool.Exec(ctx, `INSERT INTO group_rate_snapshots(id,user_id,admin_account_id,site_id,site_name,group_id,group_name,platform,type,multiplier,created_at,deleted) VALUES($1,'user-a','workspace-a',$2,$2,'54','current-name','sub2api','openai',$3,$4,false)`, fmt.Sprint(i), site, float64(i+1), time.Now().Add(time.Duration(i-1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []struct {
		site string
		want int
	}{{"site-a", 0}, {"site-b", 1}, {"", 1}} {
		// JSON keeps this input using the same public query names as the route.
		var q ListQuery
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"Page":1,"PageSize":30,"Status":"all","SiteID":%q}`, input.site)), &q); err != nil {
			t.Fatal(err)
		}
		v, err := NewService(repo).List(ctx, "user-a", "workspace-a", q)
		if err != nil {
			t.Fatal(err)
		}
		if v.Total != input.want || len(v.Items) != input.want || len(v.Types) != 1 {
			t.Fatalf("URL dedup/site filter %s total=%d rows=%d", input.site, v.Total, len(v.Items))
		}
		if input.want == 1 {
			r := v.Items[0]
			if r.SiteID != "site-b" || r.CurrentMultiplier != 6 || r.Delta == nil || *r.Delta != 1 || r.DeltaPercent == nil || *r.DeltaPercent != 100 {
				t.Fatal("site filter altered recharge calculation or previous multiplier window")
			}
		}
	}
}
