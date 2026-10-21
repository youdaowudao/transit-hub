package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 主代理冻结已确认的业务期望；JSON 注入允许在旧代码上得到业务 RED，禁止以编译错误代替。
func migrationConfirmedDTO(t *testing.T, site *Site, mode AuthMode, credentials bool) UpdateRequest {
	t.Helper()
	dto := switchDTO(site, mode)
	dto.SiteURL = "http://migrated.invalid"
	dto.Remark = site.Remark
	if !credentials {
		dto.Password, dto.AccessToken, dto.RefreshToken = "", "", ""
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["confirmDomainMigration"] = true
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

func migrationOnlyNewHost(t *testing.T, svc *Service) *int {
	t.Helper()
	oldRequests := new(int)
	transport := svc.platformService.httpClient.client.Transport
	svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "migrated.invalid" {
			*oldRequests++
			return nil, errors.New("旧域名已停止服务")
		}
		return transport.RoundTrip(r)
	})
	return oldRequests
}

func TestDomainMigrationCriticalReuseOriginalLogin(t *testing.T) {
	for _, tc := range []struct {
		platform Platform
		mode     AuthMode
	}{
		{PlatformNewAPI, AuthModePassword}, {PlatformNewAPI, AuthModeUserKey},
		{PlatformSub2API, AuthModePassword}, {PlatformSub2API, AuthModeToken},
	} {
		t.Run(string(tc.platform)+"/"+string(tc.mode), func(t *testing.T) {
			f := newSwitchFixture()
			svc, cache, repo, site := switchSetup(t, f, tc.platform, tc.mode, true)
			site.Enabled = boolPointer(false)
			site.Remark = "保留原备注"
			cache.add(site)
			repo.durable = cloneSite(site)
			oldRequests := migrationOnlyNewHost(t, svc)
			response, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, tc.mode, false))
			if err != nil {
				t.Fatalf("同站换域名应成功：%v", err)
			}
			got, _ := cache.Get(t.Context(), site.ID)
			if response.ID != site.ID || got.ID != site.ID || got.BaseURL != "http://migrated.invalid" || got.Session.BaseURL != got.BaseURL || repo.durable.BaseURL != got.BaseURL || !reflect.DeepEqual(got.Session, repo.durable.Session) {
				t.Fatal("原编号、站点地址、登录地址与持久化结果必须一致")
			}
			if *oldRequests != 0 || f.logins != 0 {
				t.Fatal("已有可信身份时不能访问旧域名，也不能刷新或重新登录")
			}
			if got.IsEnabled() || got.Remark != site.Remark || !reflect.DeepEqual(got.Settings, site.Settings) || got.AdminAccountID != site.AdminAccountID || got.Account != site.Account {
				t.Fatal("迁移改变原设置或账号")
			}
		})
	}
}

func TestDomainMigrationCriticalFreshCredentialsAndIdentity(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "旧址停用仍能用新凭据", true: "另一账号不能迁移"}[mismatch], func(t *testing.T) {
			f := newSwitchFixture()
			if mismatch {
				f.newID = 8
			}
			svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModeUserKey, true)
			oldRequests := migrationOnlyNewHost(t, svc)
			_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeUserKey, true))
			if mismatch {
				if err == nil {
					t.Fatal("不同账号不能保存")
				}
				switchUnchanged(t, c, r, site)
			} else if err != nil {
				t.Fatalf("可信旧账号新凭据应成功：%v", err)
			}
			if *oldRequests != 0 {
				t.Fatal("可信旧编号应避免请求停用旧域名")
			}
		})
	}
}

func TestDomainMigrationCriticalRequiresExplicitConfirmation(t *testing.T) {
	f := newSwitchFixture()
	svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModeUserKey, true)
	dto := switchDTO(site, AuthModeUserKey)
	dto.SiteURL = "http://migrated.invalid"
	_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
	if err == nil || !strings.Contains(err.Error(), "identityCheckFailed") {
		t.Fatal("缺少迁移确认必须拒绝")
	}
	if len(f.paths) != 0 {
		t.Fatal("确认前不能把凭据发送到新网站")
	}
	switchUnchanged(t, c, r, site)
}

func TestDomainMigrationCriticalHandlerKeepsOriginalRecord(t *testing.T) {
	f := newSwitchFixture()
	svc, cache, repo, site := switchSetup(t, f, PlatformNewAPI, AuthModeUserKey, true)
	oldRequests := migrationOnlyNewHost(t, svc)
	status, body := switchHandler(svc, site, migrationConfirmedDTO(t, site, AuthModeUserKey, false))
	if status != http.StatusOK || body["id"] != site.ID || body["baseUrl"] != "http://migrated.invalid" || body["authMode"] != string(AuthModeUserKey) {
		t.Fatalf("实际编辑接口必须接收迁移确认并返回原站点及新地址，status=%d", status)
	}
	current, err := cache.Get(t.Context(), site.ID)
	if err != nil || current == nil || current.Session.BaseURL != body["baseUrl"] || repo.durable.ID != site.ID || repo.durable.BaseURL != current.BaseURL || *oldRequests != 0 {
		t.Fatal("实际接口、运行资料与保存的原站点记录必须一致")
	}
}

func TestDomainMigrationCriticalDatabaseFailureKeepsBothOldAddresses(t *testing.T) {
	f := newSwitchFixture()
	svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
	migrationOnlyNewHost(t, svc)
	r.saveError = errors.New("fixture database unavailable")
	_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeToken, true))
	if err == nil {
		t.Fatal("数据库失败不能报告成功")
	}
	got, _ := c.Get(t.Context(), site.ID)
	if r.checks != 1 {
		t.Fatal("必须在实际数据库保存步骤失败")
	}
	if !reflect.DeepEqual(got, site) || !reflect.DeepEqual(r.durable, cloneSite(site)) || c.writes != 0 {
		t.Fatal("不能把新地址登录单独塞进旧地址缓存")
	}
}

func TestDomainMigrationCriticalPasswordMetricsFailureIsNotSuccess(t *testing.T) {
	f := newSwitchFixture()
	svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModePassword, true)
	transport := svc.platformService.httpClient.client.Transport
	reads := 0
	svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "migrated.invalid" {
			t.Error("可信编号不应访问旧域名")
			return nil, errors.New("旧址停用")
		}
		if req.URL.Path == "/api/user/self" {
			reads++
			if reads == 1 {
				return nil, errors.New("fixture 网站资料读取中断")
			}
		}
		return transport.RoundTrip(req)
	})
	_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModePassword, true))
	if err == nil {
		t.Fatal("登录成功但整体指标读取失败不能保存迁移")
	}
	if f.logins != 1 {
		t.Fatal("必须实际验证登录后吞指标错误的路径")
	}
	var detail *RequestError
	if !errors.As(err, &detail) || detail.Stage != "metrics" {
		t.Fatal("失败步骤必须指向读取指标")
	}
	switchUnchanged(t, c, r, site)
}

func TestDomainMigrationCriticalCancellation(t *testing.T) {
	f := newSwitchFixture()
	svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModeUserKey, true)
	migrationOnlyNewHost(t, svc)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.beforeOld = func(*http.Request) error { cancel(); return ctx.Err() }
	_, err := svc.Update(ctx, site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeUserKey, false))
	if err == nil {
		t.Fatal("取消请求不能保存")
	}
	switchUnchanged(t, c, r, site)
}

func TestDomainMigrationCriticalOldSyncCannotReplaceNewAddress(t *testing.T) {
	f := newSwitchFixture()
	svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModeUserKey, true)
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	transport := svc.platformService.httpClient.client.Transport
	svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "fixture.invalid" {
			enterOnce.Do(func() { close(entered); <-release })
		}
		return transport.RoundTrip(req)
	})
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); _, err := svc.syncOnce(context.Background(), site.ID); done <- err }()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("旧同步测试未回收")
		}
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("旧同步没有进入读取")
	}
	_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeUserKey, false))
	if err != nil {
		t.Fatalf("旧同步读取中仍可保存迁移：%v", err)
	}
	after, _ := c.Get(t.Context(), site.ID)
	saved := cloneSite(after)
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("旧同步应被识别为过期")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("旧同步未退出")
	}
	current, _ := c.Get(t.Context(), site.ID)
	if !reflect.DeepEqual(cloneSite(current), saved) || !reflect.DeepEqual(cloneSite(&r.durable), saved) {
		t.Fatal("旧同步覆盖了新地址或费用")
	}
}
