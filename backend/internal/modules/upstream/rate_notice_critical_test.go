package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/shared/authctx"
)

// 关键业务期望由主代理在 c247400 上冻结；仅用原有服务入口验证消息结果。
// 此夹具预建确认设计的记录表，旧实现可编译但不会写消息，不以缺少新函数作为 RED。
func rateNoticeCriticalPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := singaporeCostPGPool(t)
	_, err := pool.Exec(t.Context(), `
ALTER TABLE admin_accounts ADD COLUMN IF NOT EXISTS platform text NOT NULL DEFAULT 'sub2api';
CREATE TABLE IF NOT EXISTS group_rate_snapshots(user_id text NOT NULL,admin_account_id text NOT NULL,site_id text NOT NULL);
CREATE TABLE IF NOT EXISTS real_connections (
 id text PRIMARY KEY, user_id text NOT NULL, workspace_admin_account_id text NOT NULL,
 upstream_site_id text NOT NULL, admin_account_id text NOT NULL, admin_account_name text NOT NULL,
 upstream_key_id text NOT NULL, status text NOT NULL DEFAULT 'active', admin_platform text NOT NULL DEFAULT 'sub2api'
);
CREATE TABLE IF NOT EXISTS site_notifications (
 id text PRIMARY KEY, user_id text NOT NULL, admin_account_id text NOT NULL REFERENCES admin_accounts(id) ON DELETE CASCADE,
 kind text NOT NULL, event_key text NOT NULL, subject_type text NOT NULL, subject_id text NOT NULL,
 level smallint NOT NULL CHECK(level BETWEEN 1 AND 4), discovered_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), content jsonb NOT NULL,
 read_at timestamptz, reminder_completed_at timestamptz,
 UNIQUE(user_id,admin_account_id,kind,event_key)
);
CREATE TABLE IF NOT EXISTS upstream_rate_notice_baselines (
 user_id text NOT NULL, admin_account_id text NOT NULL REFERENCES admin_accounts(id) ON DELETE CASCADE,
 site_id text NOT NULL REFERENCES upstream_sites(id) ON DELETE CASCADE, group_key text NOT NULL,
 group_id text NOT NULL, group_name text NOT NULL, multiplier double precision NOT NULL,
 observed_at timestamptz NOT NULL, PRIMARY KEY(user_id,admin_account_id,site_id,group_key)
);`)
	if err != nil {
		t.Fatal("通知业务夹具建表失败")
	}
	return pool
}

type rateNoticeCriticalSample struct {
	rate   float64
	status int
	name   string
}

func rateNoticeCriticalService(t *testing.T, pool *pgxpool.Pool, sample *rateNoticeCriticalSample) (*Service, *syncTestCache) {
	t.Helper()
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
	site.RequestedPlatform = PlatformSub2API
	site.Account = "fixture-user"
	site.Metrics.Groups = []GroupInfo{{ID: "1", Name: "a", Multiplier: float64Pointer(9)}}
	site.Metrics.Group = site.Metrics.Groups[0]
	if err := NewRepository(pool).SaveSite(t.Context(), *site); err != nil {
		t.Fatal("通知业务夹具保存旧站点失败")
	}
	p := singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return 200, map[string]any{"data": map[string]any{"id": 1, "email": "fixture-user", "balance": 10}}
		case "/api/v1/usage/dashboard/stats", "/api/v1/usage/stats":
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 7}}
		case "/api/v1/groups/available":
			return 200, map[string]any{"data": []any{map[string]any{"id": 1, "name": sample.name, "rate_multiplier": 9}}}
		case "/api/v1/groups/rates":
			if sample.status != 200 {
				return sample.status, map[string]any{"message": "专属倍率暂不可读取"}
			}
			return 200, map[string]any{"data": map[string]any{"1": sample.rate}}
		default:
			t.Errorf("通知新增了意外请求 %s", r.URL.Path)
			return 500, map[string]any{}
		}
	})
	cache := newSyncTestCache(site)
	svc := NewService(p, NewRepository(pool), nil, cache)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	svc.SetBackgroundTasksDisabled(true)
	t.Cleanup(svc.Close)
	return svc, cache
}

func rateNoticeCriticalCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications`).Scan(&got); err != nil {
		t.Fatal("不能读取业务消息结果")
	}
	if got != want {
		t.Fatalf("倍率业务消息数量=%d，期望=%d", got, want)
	}
}

func rateNoticeCriticalSync(t *testing.T, svc *Service) {
	t.Helper()
	if _, err := svc.syncOnce(t.Context(), "site"); err != nil {
		t.Fatalf("原同步入口失败：%v", err)
	}
}

func TestRateNoticeCriticalFallbackRecoveryRoundTripsPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 0) // 旧9倍率不得回填为可信起点。
	sample.rate = 2
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	sample.status = 404
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	var confirmed bool
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE((metrics->'rateNoticeObservation'->>'confirmed')::boolean,false) FROM upstream_sites WHERE id='site'`).Scan(&confirmed); err != nil || confirmed {
		t.Fatal("失败回退不能继承上轮可信状态")
	}
	sample.status, sample.rate = 200, 1
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 2) // 应从最后可信2比较，不从失败回退9比较。
	sample.rate, sample.name = 2, "改名后"
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 3)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 3)
	var level int
	var content []byte
	if err := pool.QueryRow(t.Context(), `SELECT level,content FROM site_notifications ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&level, &content); err != nil || level != 1 || !strings.Contains(string(content), "改名后") {
		t.Fatal("未登记接入应为普通；有ID改名仍按同组比较，并保存当时名称")
	}
}

func TestRateNoticeCriticalFailedFirstReadCannotSeedBaselinePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 404, name: "a"}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	var confirmed bool
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE((metrics->'rateNoticeObservation'->>'confirmed')::boolean,false) FROM upstream_sites WHERE id='site'`).Scan(&confirmed); err != nil || confirmed {
		t.Fatal("初次失败不得作为可信值")
	}
	sample.status = 200
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 0)
	sample.rate = 2
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
}

func TestRateNoticeCriticalMessageFailureRollsBackSiteAndBaselinePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_notice_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture message write rejected'; END $$; CREATE TRIGGER reject_notice_fixture BEFORE INSERT ON site_notifications FOR EACH ROW EXECUTE FUNCTION reject_notice_fixture()`); err != nil {
		t.Fatal("不能设置隔离记录失败夹具")
	}
	sample.rate = 2
	_, err := svc.syncOnce(t.Context(), "site")
	if err == nil {
		t.Fatal("必要消息写入失败却把对应站点保存报告为成功")
	}
	var actual, baseline float64
	if err := pool.QueryRow(t.Context(), `SELECT (metrics->'groups'->0->>'multiplier')::double precision FROM upstream_sites WHERE id='site'`).Scan(&actual); err != nil || actual != 1 {
		t.Fatal("消息失败后站点数值没有一起回滚")
	}
	if err := pool.QueryRow(t.Context(), `SELECT multiplier FROM upstream_rate_notice_baselines WHERE site_id='site' AND group_key='id:1'`).Scan(&baseline); err != nil || baseline != 1 {
		t.Fatal("消息失败后可信起点没有一起回滚")
	}
	rateNoticeCriticalCount(t, pool, 0)
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_notice_fixture ON site_notifications`); err != nil {
		t.Fatal("不能恢复隔离夹具")
	}
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
}

func TestRateNoticeCriticalReloginPersistsChangesPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	sample.rate = 2
	_, err := svc.Update(t.Context(), "user", "site", UpdateRequest{Name: "site-site", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1})
	if err != nil {
		t.Fatalf("原同身份重登入口失败：%v", err)
	}
	rateNoticeCriticalCount(t, pool, 1)
	var content map[string]any
	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT content FROM site_notifications LIMIT 1`).Scan(&raw); err != nil || json.Unmarshal(raw, &content) != nil || content["title"] == nil {
		t.Fatal("重登消息缺少安全可显示的内容")
	}
}

func TestRateNoticeCriticalCreateRestartAndSiteDeletePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	created, err := svc.Create(t.Context(), "user", CreateRequest{Name: "通知新建夹具", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1})
	if err != nil {
		t.Fatalf("原新建入口失败：%v", err)
	}
	rateNoticeCriticalCount(t, pool, 0)
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_rate_notice_baselines WHERE site_id=$1`, created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("新建首次可信值未建立比较起点")
	}
	// 新的Service/Repository实例必须使用持久化起点，无进程内旧值可借用。
	svc.Close()
	restarted := NewService(svc.platformService, NewRepository(pool), nil, cache)
	restarted.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	restarted.SetBackgroundTasksDisabled(true)
	t.Cleanup(restarted.Close)
	sample.rate = 2
	if _, err := restarted.syncOnce(t.Context(), created.ID); err != nil {
		t.Fatal("重建服务后同步失败")
	}
	rateNoticeCriticalCount(t, pool, 1)
	if err := NewRepository(pool).DeleteSite(t.Context(), "user", created.ID); err != nil {
		t.Fatal("原站点删除入口失败")
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_rate_notice_baselines WHERE site_id=$1`, created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("删除站点未清除比较起点")
	}
	rateNoticeCriticalCount(t, pool, 1)
}

func TestRateNoticeCriticalUnknownRegisteredAndMissingClassificationPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, account, status string
		level                 int
	}{
		{"已登记但归属未知", "7", "active", 4},
		{"旧空状态保守保留", "7", "", 4},
		{"完整检查确认missing", "7", "missing", 1},
		{"登记字段无法证明接入", "", "active", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := rateNoticeCriticalPool(t)
			sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
			svc, _ := rateNoticeCriticalService(t, pool, sample)
			rateNoticeCriticalSync(t, svc)
			if _, err := pool.Exec(t.Context(), `INSERT INTO real_connections(id,user_id,workspace_admin_account_id,upstream_site_id,admin_account_id,admin_account_name,upstream_key_id,status) VALUES('fixture','user','workspace','site',$1,'已登记账号','key-1',$2)`, tc.account, tc.status); err != nil {
				t.Fatal("分类夹具创建失败")
			}
			sample.rate = 2
			rateNoticeCriticalSync(t, svc)
			rateNoticeCriticalCount(t, pool, 1)
			var level int
			if err := pool.QueryRow(t.Context(), `SELECT level FROM site_notifications`).Scan(&level); err != nil || level != tc.level {
				t.Fatalf("分类=%d，期望%d", level, tc.level)
			}
		})
	}
}

type rateNoticeCriticalCacheFailure struct {
	*syncTestCache
	reject bool
}

func (c *rateNoticeCriticalCacheFailure) Set(ctx context.Context, site *Site) error {
	if c.reject && site.Status == StatusConnected {
		return errors.New("运行数据写入被隔离夹具拒绝")
	}
	return c.syncTestCache.Set(ctx, site)
}

func TestRateNoticeCriticalPostgresCommitSurvivesRedisPublicationFailurePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	failure := &rateNoticeCriticalCacheFailure{syncTestCache: cache, reject: true}
	svc.cache = failure
	sample.rate = 2
	_, err := svc.syncOnce(t.Context(), "site")
	if err == nil || !strings.Contains(taskErrorDetail(err), "本站资料已保存") || !strings.Contains(taskErrorDetail(err), "当前运行数据未更新") {
		t.Fatal("PG后运行发布失败未说明实际保存结果")
	}
	rateNoticeCriticalCount(t, pool, 1)
	var baseline float64
	if err := pool.QueryRow(t.Context(), `SELECT multiplier FROM upstream_rate_notice_baselines WHERE site_id='site'`).Scan(&baseline); err != nil || baseline != 2 {
		t.Fatal("运行数据发布失败丢失已提交的比较起点")
	}
	// 恢复连接后用户必须重读，真实同值不重复；不重放失败样本。
	failure.reject = false
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
}

func TestRateNoticeCriticalPartialDirectoryAutoAndCorruptConversionPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	site := groupGuardSite(t)
	if err := NewRepository(pool).SaveSite(t.Context(), *site); err != nil {
		t.Fatal("NewAPI通知夹具站点失败")
	}
	values := map[string]*float64{"a": float64Pointer(1), "b": float64Pointer(3), "auto": float64Pointer(1)}
	partial, corrupt := false, false
	p := singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/status":
			return 200, map[string]any{"data": map[string]any{"quota_per_unit": 500000}}
		case "/api/user/self":
			return 200, map[string]any{"data": map[string]any{"id": 1, "quota": 500000, "used_quota": 500000, "group": "a"}}
		case "/api/log/self/stat":
			return 200, map[string]any{"data": map[string]any{"quota": 3500000}}
		case "/api/user/self/groups":
			if corrupt {
				return 200, map[string]any{"data": map[string]any{"auto": "自动选择说明"}}
			}
			groups := map[string]any{}
			for name, value := range values {
				if value == nil {
					groups[name] = map[string]any{}
				} else {
					groups[name] = map[string]any{"ratio": *value}
				}
			}
			return 200, map[string]any{"data": groups}
		case "/api/pricing":
			if corrupt {
				return 200, map[string]any{}
			}
			ratios, usable := map[string]any{}, map[string]any{}
			for name, value := range values {
				usable[name] = name
				if value != nil {
					ratios[name] = *value
				}
			}
			payload := map[string]any{"group_ratio": ratios, "usable_group": usable}
			if partial {
				payload["total"] = 1
			}
			return 200, payload
		default:
			t.Errorf("NewAPI通知新增请求%s", r.URL.Path)
			return 500, map[string]any{}
		}
	})
	svc := NewService(p, NewRepository(pool), nil, newSyncTestCache(site))
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	svc.SetBackgroundTasksDisabled(true)
	t.Cleanup(svc.Close)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 0)
	partial = true
	values = map[string]*float64{"a": float64Pointer(2), "auto": nil}
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_rate_notice_baselines WHERE group_key='id:b'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("部分目录删除了未返回b起点")
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_rate_notice_baselines WHERE group_key='id:auto'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("逐项fixed→auto未清固定起点")
	}
	partial = false
	values = map[string]*float64{"a": float64Pointer(2), "b": float64Pointer(3), "auto": float64Pointer(2)}
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1) // auto恢复fixed重新建起点，不能跨模式报1→2。
	corrupt = true
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_rate_notice_baselines`).Scan(&count); err != nil || count != 3 {
		t.Fatal("auto原文身份丢失却推进或清理起点")
	}
	corrupt = false
	values = map[string]*float64{"a": float64Pointer(2), "auto": float64Pointer(2)}
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_rate_notice_baselines WHERE group_key='id:b'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("完整且可信目录未清除消失b起点")
	}
}

func TestRateNoticeCriticalZeroAndSmallChangeRemainVisiblePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 0, status: 200, name: "a"}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	sample.rate = 1
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT content FROM site_notifications`).Scan(&raw); err != nil {
		t.Fatal("读取零倍率消息失败")
	}
	if !strings.Contains(string(raw), "0×变为1×") || strings.Contains(string(raw), "变化比例") || strings.Contains(string(raw), "Infinity") {
		t.Fatal("零倍率不得显示无穷涨幅，应显示实际零到新值")
	}
	sample.rate = 1.0000000000001
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 2)
	if err := pool.QueryRow(t.Context(), `SELECT content FROM site_notifications WHERE content::text LIKE '%1.0000000000001%'`).Scan(&raw); err != nil {
		t.Fatal("小于显示精度的实际变化必须保留可辨认有效数字")
	}
}

// 真实PG提交后模拟回包丢失；只模拟提交返回，不伪造PG保存结果。
type rateNoticeCriticalUnknownCommit struct {
	*Repository
	loseNext bool
}

func (r *rateNoticeCriticalUnknownCommit) SaveExistingSiteWithGroupGuard(ctx context.Context, expected, candidate, baseline Site, inputs ...*RateNoticeInput) (Site, string, bool, bool, error) {
	actual, version, saved, accepted, err := r.Repository.SaveExistingSiteWithGroupGuard(ctx, expected, candidate, baseline, inputs...)
	if err == nil && saved && r.loseNext {
		r.loseNext = false
		return actual, version, saved, accepted, &siteCommitError{err: errors.New("隔离夹具模拟已提交后的回包中断")}
	}
	return actual, version, saved, accepted, err
}

func (r *rateNoticeCriticalUnknownCommit) SaveSiteWithSnapshotVersion(ctx context.Context, site Site, inputs ...*RateNoticeInput) (string, error) {
	version, err := r.Repository.SaveSiteWithSnapshotVersion(ctx, site, inputs...)
	if err == nil && r.loseNext {
		r.loseNext = false
		return version, &siteCommitError{err: errors.New("隔离夹具模拟已提交后的回包中断")}
	}
	return version, err
}

func TestRateNoticeCriticalUnknownCommitRequiresFreshReadAndDoesNotDuplicatePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	svc.repository = &rateNoticeCriticalUnknownCommit{Repository: NewRepository(pool), loseNext: true}
	sample.rate = 2
	_, err := svc.syncOnce(t.Context(), "site")
	if err == nil || !strings.Contains(taskErrorDetail(err), "数据库保存结果尚未确认") {
		t.Fatal("提交回包中断却确认保存成功或确认回滚")
	}
	rateNoticeCriticalCount(t, pool, 1)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	sample.rate = 3
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 2)
}

func TestRateNoticeCriticalLateGroupsAndNilConfigurationKeepObservationPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	before, _ := cache.Get(t.Context(), "site")
	baseline := cloneSite(before)
	sample.rate = 2
	rateNoticeCriticalSync(t, svc)
	current, _ := cache.Get(t.Context(), "site")
	late := cloneSite(before)
	late.Metrics.Groups[0].Multiplier = float64Pointer(8)
	late.Metrics.Group = late.Metrics.Groups[0]
	input := &RateNoticeInput{Observation: RateNoticeObservation{Confirmed: true, ObservedAt: time.Now(), Reason: "旧观测不得覆盖"}, AllowCleanup: true}
	actual, _, saved, accepted, err := NewRepository(pool).SaveExistingSiteWithGroupGuard(t.Context(), *current, late, baseline, input)
	if err != nil || !saved || accepted || actual.Metrics.RateNoticeObservation == nil || actual.Metrics.RateNoticeObservation.Reason == "旧观测不得覆盖" || *actual.Metrics.Groups[0].Multiplier != 2 {
		t.Fatal("迟到分组覆盖当前观测或分组")
	}
	rateNoticeCriticalCount(t, pool, 1)
	configured := cloneSite(&actual)
	configured.Remark = "配置保存夹具"
	configured.Metrics.RateNoticeObservation = &RateNoticeObservation{Confirmed: false, Reason: "配置不得伪造新倍率观测"}
	if saved, err := NewRepository(pool).SaveExistingSiteIfUnchanged(t.Context(), actual, configured); err != nil || !saved {
		t.Fatal("原配置保存失败")
	}
	var reason string
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE(metrics->'rateNoticeObservation'->>'reason','') FROM upstream_sites WHERE id='site'`).Scan(&reason); err != nil || reason == "配置不得伪造新倍率观测" {
		t.Fatal("配置覆盖了PG安全观测")
	}
	rateNoticeCriticalCount(t, pool, 1)
}

type rateNoticeCriticalAssignments struct {
	value RateNoticeKeyAssignment
	calls int
}

func (r *rateNoticeCriticalAssignments) PeekKeyAssignments(user, workspace, site string, platform Platform, bindings []RateNoticeBinding) map[string]RateNoticeKeyAssignment {
	r.calls++
	if user != "user" || workspace != "workspace" || site != "site" || platform != PlatformSub2API {
		return nil
	}
	return map[string]RateNoticeKeyAssignment{"key-1": r.value}
}

func TestRateNoticeCriticalCurrentAssignmentWinsOverHistoricalConnectionGroupPostgres(t *testing.T) {
	for _, tc := range []struct {
		name       string
		assignment RateNoticeKeyAssignment
		level      int
	}{
		{"当前匹配", RateNoticeKeyAssignment{Confirmed: true, GroupID: "1", GroupName: "a", ConfirmedAt: time.Now()}, 4},
		{"当前已知其他组", RateNoticeKeyAssignment{Confirmed: true, GroupID: "2", GroupName: "其他组", ConfirmedAt: time.Now()}, 1},
		{"已删Key未解绑", RateNoticeKeyAssignment{Reason: "关联Key已确认删除且仍保留原关联，请核查并解绑"}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := rateNoticeCriticalPool(t)
			sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
			svc, _ := rateNoticeCriticalService(t, pool, sample)
			reader := &rateNoticeCriticalAssignments{value: tc.assignment}
			svc.repository.(*Repository).SetRateNoticeKeyAssignmentReader(reader)
			if _, err := pool.Exec(t.Context(), `INSERT INTO real_connections(id,user_id,workspace_admin_account_id,upstream_site_id,admin_account_id,admin_account_name,upstream_key_id,status) VALUES('f1','user','workspace','site','7','同一登记账号','key-1','active'),('f2','user','workspace','site','7','同一登记账号','key-1','')`); err != nil {
				t.Fatal("当前归属夹具失败")
			}
			rateNoticeCriticalSync(t, svc)
			if reader.calls != 0 {
				t.Fatal("首次起点不应读取分类缓存")
			}
			sample.rate = 2
			rateNoticeCriticalSync(t, svc)
			rateNoticeCriticalCount(t, pool, 1)
			var level int
			if err := pool.QueryRow(t.Context(), `SELECT level FROM site_notifications`).Scan(&level); err != nil || level != tc.level {
				t.Fatal("当前Key归属分类未应用")
			}
			var count string
			if err := pool.QueryRow(t.Context(), `SELECT detail->>'value' FROM site_notifications, jsonb_array_elements(content->'details') detail WHERE detail->>'label'='已登记主站账号数量'`).Scan(&count); err != nil || count != "1" {
				t.Fatal("登记主站账号未去重")
			}
			rateNoticeCriticalSync(t, svc)
			if reader.calls != 1 {
				t.Fatal("本站无变化同步不应再读取分类缓存")
			}
		})
	}
}

func TestRateNoticeCriticalCreateHandlerRetainsFailureAndUnknownCommitPostgres(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "起点写入失败", true: "提交结果未知"}[unknown], func(t *testing.T) {
			pool := rateNoticeCriticalPool(t)
			sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
			svc, cache := rateNoticeCriticalService(t, pool, sample)
			if unknown {
				svc.repository = &rateNoticeCriticalUnknownCommit{Repository: NewRepository(pool), loseNext: true}
			} else if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_baseline_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE SQLSTATE '42P01' USING MESSAGE='fixture-raw-secret-do-not-display',DETAIL='password=fixture-raw-secret-do-not-display'; END $$; CREATE TRIGGER reject_baseline_fixture BEFORE INSERT ON upstream_rate_notice_baselines FOR EACH ROW EXECUTE FUNCTION reject_baseline_fixture()`); err != nil {
				t.Fatal("新建保存失败夹具创建失败")
			}
			body, _ := json.Marshal(CreateRequest{Name: "通知新建失败夹具", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1})
			request := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(body))
			request = request.WithContext(authctx.WithUserID(request.Context(), "user"))
			response := httptest.NewRecorder()
			(&Handler{service: svc, accounts: svc.accounts}).create(response, request)
			var result struct {
				Failure struct {
					ErrorDetail string `json:"errorDetail"`
					Stage       string `json:"stage"`
				} `json:"failure"`
			}
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != 503 || result.Failure.Stage != "save" || result.Failure.ErrorDetail == "" {
				t.Fatalf("新建HTTP保存失败缺少详细结果，status=%d", response.Code)
			}
			detail := result.Failure.ErrorDetail
			if strings.Contains(detail, "fixture-raw-secret") || strings.Contains(detail, "password=") {
				t.Fatal("新建错误透出了SQL诊断或凭据")
			}
			if unknown {
				if !strings.Contains(detail, "尚未确认") || !strings.Contains(detail, "核查") || strings.Contains(detail, "本次没有保存") {
					t.Fatal("新建未知提交必须明确核查，不能声称未保存")
				}
			} else if !strings.Contains(detail, "倍率比较") || !strings.Contains(detail, "缺少必要结构") || !strings.Contains(detail, "迁移") || !strings.Contains(detail, "未保存") {
				t.Fatal("新建失败未保留起点步骤、已知结构原因、实际回滚和指引")
			}
			var sites int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_sites`).Scan(&sites); err != nil || sites != map[bool]int{false: 1, true: 2}[unknown] {
				t.Fatal("新建保存结果与错误说明不对应")
			}
			cached, _ := cache.ListByUser(t.Context(), "user")
			if len(cached) != 1 {
				t.Fatal("原新建失败缓存回收未保留")
			}
		})
	}
}

func TestRateNoticeCriticalComparisonAndBindingErrorsRetainKnownCausePostgres(t *testing.T) {
	for _, permission := range []bool{false, true} {
		t.Run(map[bool]string{false: "比较表缺失", true: "登记读取未获允许"}[permission], func(t *testing.T) {
			pool := rateNoticeCriticalPool(t)
			sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
			svc, _ := rateNoticeCriticalService(t, pool, sample)
			rateNoticeCriticalSync(t, svc)
			if permission {
				// 本机测试用户可能为superuser，REVOKE不能模拟实际拒绝。
				// 隔离视图在原SELECT位置抛出真实42501，无需修改数据库角色。
				if _, err := pool.Exec(t.Context(), `CREATE FUNCTION blocked_bindings_fixture() RETURNS text LANGUAGE plpgsql AS $$ BEGIN RAISE SQLSTATE '42501' USING MESSAGE='fixture-raw-secret-do-not-display',DETAIL='password=fixture-raw-secret-do-not-display'; END $$; ALTER TABLE real_connections RENAME TO real_connections_fixture; CREATE VIEW real_connections AS SELECT 'fixture'::text AS id,'user'::text AS user_id,'workspace'::text AS workspace_admin_account_id,'site'::text AS upstream_site_id,'7'::text AS admin_account_id,'登记夹具'::text AS admin_account_name,'key-1'::text AS upstream_key_id,blocked_bindings_fixture() AS status,'sub2api'::text AS admin_platform`); err != nil {
					t.Fatal("隔离登记读取权限夹具失败")
				}
			} else if _, err := pool.Exec(t.Context(), `ALTER TABLE upstream_rate_notice_baselines RENAME TO rate_notice_baselines_fixture`); err != nil {
				t.Fatal("隔离比较表缺失夹具失败")
			}
			sample.rate = 2
			_, err := svc.syncOnce(t.Context(), "site")
			detail := taskErrorDetail(err)
			if err == nil || !strings.Contains(detail, "未保存") || !strings.Contains(detail, "重试") {
				t.Fatal("通知步骤失败未说明原站点保存结果和重试")
			}
			if permission {
				if !strings.Contains(detail, "已登记主站接入") || !strings.Contains(detail, "数据库未允许") || !strings.Contains(detail, "数据库权限") {
					t.Fatal("登记查询仅泛化数据库失败，没有已知原因和权限指引")
				}
			} else if !strings.Contains(detail, "倍率比较记录") || !strings.Contains(detail, "缺少必要结构") || !strings.Contains(detail, "启动迁移") {
				t.Fatal("比较查询仅泛化SQLSTATE，没有已知缺表原因和迁移指引")
			}
			var actual float64
			if err := pool.QueryRow(t.Context(), `SELECT (metrics->'groups'->0->>'multiplier')::double precision FROM upstream_sites WHERE id='site'`).Scan(&actual); err != nil || actual != 1 {
				t.Fatal("必要通知查询失败没有原子回滚站点")
			}
			rateNoticeCriticalCount(t, pool, 0)
		})
	}
}

func TestRateNoticeCriticalPasswordLoginFallbackKeepsDirectSafeObservationPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	requests := 0
	client := &http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		status, payload := 200, `{"success":true,"data":{"id":1}}`
		header := http.Header{"Content-Type": {"application/json"}}
		switch r.URL.Path {
		case "/api/user/login":
			header.Set("Set-Cookie", "fixture-session=fixture-only; Path=/")
		case "/api/status":
			payload = `{"data":{"quota_per_unit":500000}}`
		case "/api/user/self":
			status, payload = 403, `{"success":false,"message":"账号资料暂被上游拒绝读取"}`
		default:
			t.Errorf("密码回退新增意外请求%s", r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	})}
	svc := NewService(NewPlatformService(NewHTTPClient(client)), NewRepository(pool), nil, newSyncTestCache())
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	svc.SetBackgroundTasksDisabled(true)
	t.Cleanup(svc.Close)
	response, err := svc.Create(t.Context(), "user", CreateRequest{Name: "密码登录回退夹具", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, AuthMode: AuthModePassword, Account: "fixture-user", Password: "fixture-only", RechargeRate: 1})
	if err != nil || response.Status != StatusConnected {
		t.Fatal("新增安全观测不得改变原密码登录成功与默认展示")
	}
	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT metrics->'rateNoticeObservation' FROM upstream_sites WHERE id=$1`, response.ID).Scan(&raw); err != nil {
		t.Fatal("未保存密码回退安全观测")
	}
	var observation RateNoticeObservation
	if json.Unmarshal(raw, &observation) != nil || observation.Confirmed || observation.ObservedAt.IsZero() || !strings.Contains(observation.Reason, "账号资料暂被上游拒绝读取") {
		t.Fatal("密码登录已知读取失败丢成未知原因或伪装可信")
	}
	if requests != 3 {
		t.Fatalf("密码回退请求数量改变：%d", requests)
	}
	rateNoticeCriticalCount(t, pool, 0)
}

type rateNoticeCriticalCleanupFailure struct{ *syncTestCache }

func (c *rateNoticeCriticalCleanupFailure) Delete(context.Context, string, string) error {
	return errors.New("测试中的运行记录清理连接已中断")
}

func TestRateNoticeCriticalCreateFailedCleanupExplainsBothActualResultsPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	svc.cache = &rateNoticeCriticalCleanupFailure{syncTestCache: cache}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_baseline_cleanup_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE SQLSTATE '23514'; END $$; CREATE TRIGGER reject_baseline_cleanup_fixture BEFORE INSERT ON upstream_rate_notice_baselines FOR EACH ROW EXECUTE FUNCTION reject_baseline_cleanup_fixture()`); err != nil {
		t.Fatal("新建双失败夹具创建失败")
	}
	_, err := svc.Create(t.Context(), "user", CreateRequest{Name: "新建双失败夹具", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1})
	detail := taskErrorDetail(err)
	if err == nil || !strings.Contains(detail, "资料未保存") || !strings.Contains(detail, "测试中的运行记录清理连接已中断") || !strings.Contains(detail, "核查当前运行资料") {
		t.Fatal("必须同时说明PG回滚和运行记录清理失败的实际结果")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_sites`).Scan(&count); err != nil || count != 1 {
		t.Fatal("双失败不应保留已回滚的PG站点")
	}
	cached, _ := cache.ListByUser(t.Context(), "user")
	if len(cached) != 2 {
		t.Fatal("隔离清理失败应保留实际未清理的运行记录")
	}
}

func TestRateNoticeCriticalCreateSiteSQLFailureNeverShowsRawDiagnosticsPostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_site_insert_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE SQLSTATE '42P01' USING MESSAGE='fixture-raw-secret-do-not-display',DETAIL='password=fixture-raw-secret-do-not-display'; END $$; CREATE TRIGGER reject_site_insert_fixture BEFORE INSERT ON upstream_sites FOR EACH ROW EXECUTE FUNCTION reject_site_insert_fixture()`); err != nil {
		t.Fatal("原站点INSERT失败夹具创建失败")
	}
	body, _ := json.Marshal(CreateRequest{Name: "原站点SQL失败夹具", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1})
	request := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(body))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user"))
	response := httptest.NewRecorder()
	(&Handler{service: svc, accounts: svc.accounts}).create(response, request)
	var result struct {
		Failure struct {
			ErrorDetail string `json:"errorDetail"`
			Stage       string `json:"stage"`
		} `json:"failure"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != 503 || result.Failure.Stage != "save" {
		t.Fatal("原站点SQL失败必须经HTTP详细保存说明返回")
	}
	detail := result.Failure.ErrorDetail
	if strings.Contains(detail, "fixture-raw-secret") || strings.Contains(detail, "password=") || strings.Contains(detail, "P0001:") {
		t.Fatal("原站点SQL诊断或凭据原文进入了HTTP响应")
	}
	if !strings.Contains(detail, "保存新网站资料失败") || !strings.Contains(detail, "缺少必要结构") || !strings.Contains(detail, "启动迁移") || !strings.Contains(detail, "资料未保存") {
		t.Fatal("原站点SQL失败缺具体结构原因、失败对象、实际结果或迁移指引")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM upstream_sites`).Scan(&count); err != nil || count != 1 {
		t.Fatal("原站点INSERT失败必须回滚")
	}
	cached, _ := cache.ListByUser(t.Context(), "user")
	if len(cached) != 1 {
		t.Fatal("原站点INSERT失败必须沿用新建缓存清理")
	}
}

func TestRateNoticeCriticalCreateMissingParentKeepsExistingNotFoundResponsePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	if _, err := pool.Exec(t.Context(), `DELETE FROM admin_accounts WHERE id='workspace'`); err != nil {
		t.Fatal("父工作区消失夹具失败")
	}
	body, _ := json.Marshal(CreateRequest{Name: "父工作区消失夹具", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1})
	request := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(body))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user"))
	response := httptest.NewRecorder()
	(&Handler{service: svc, accounts: svc.accounts}).create(response, request)
	var result struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != http.StatusNotFound || result.Message != ErrorNotFound {
		t.Fatal("新通知保存包装不得把原父工作区消失404变成存储503")
	}
	cached, _ := cache.ListByUser(t.Context(), "user")
	if len(cached) != 1 {
		t.Fatal("父工作区消失仍应沿用新建失败缓存清理")
	}
}
