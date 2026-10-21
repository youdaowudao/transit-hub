package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/shared/businesstime"
)

func migrationFixtureResponse(status int, payload any) *http.Response {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
	return w.Result()
}

func TestDomainMigrationAdditionalFreshLoginAndFollowingSync(t *testing.T) {
	for _, tc := range []struct {
		platform  Platform
		old, next AuthMode
	}{
		{PlatformNewAPI, AuthModePassword, AuthModePassword},
		{PlatformNewAPI, AuthModeUserKey, AuthModeUserKey},
		{PlatformSub2API, AuthModePassword, AuthModePassword},
		{PlatformSub2API, AuthModeToken, AuthModeToken},
		{PlatformNewAPI, AuthModePassword, AuthModeUserKey},
		{PlatformNewAPI, AuthModeUserKey, AuthModePassword},
		{PlatformSub2API, AuthModePassword, AuthModeToken},
		{PlatformSub2API, AuthModeToken, AuthModePassword},
	} {
		t.Run(string(tc.platform)+"/"+string(tc.old)+"-"+string(tc.next), func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, tc.platform, tc.old, true)
			oldRequests := migrationOnlyNewHost(t, svc)
			dto := migrationConfirmedDTO(t, site, tc.next, true)
			if tc.old != AuthModePassword && tc.next == AuthModePassword {
				dto.Account = "owner@example.com"
			}
			if _, err := svc.Update(t.Context(), site.UserID, site.ID, dto); err != nil {
				t.Fatalf("新凭据迁移失败：%v", err)
			}
			got, _ := c.Get(t.Context(), site.ID)
			if got.Session.AuthMode != tc.next || !got.Session.UserIDConfirmed || got.Session.BaseURL != dto.SiteURL || !reflect.DeepEqual(cloneSite(got), cloneSite(&r.durable)) {
				t.Fatal("新登录方式或地址没有一起保存")
			}
			before := len(f.paths)
			response, err := svc.Sync(t.Context(), site.UserID, site.ID)
			if err != nil || response.BaseURL != dto.SiteURL || *oldRequests != 0 || len(f.paths) < before {
				t.Fatalf("后续同步应只使用新地址：%v", err)
			}
			if f.paths["/api/v1/usage/stats"]+f.paths["/api/log/self/stat"] < 2 {
				t.Fatal("没有实际完成迁移后的新地址指标读取")
			}
		})
	}
}

func TestDomainMigrationAdditionalStoredIdentitySelection(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		platform                                            Platform
		mode                                                AuthMode
		confirmed, cookie, clearID, oldAllowed, wantSuccess bool
		newID                                               any
		email                                               string
	}{
		{"明确NewAPI密码可用原编号", PlatformNewAPI, AuthModePassword, false, false, false, false, true, 7, ""},
		{"明确Sub2API密码可用邮箱", PlatformSub2API, AuthModePassword, false, false, true, false, true, 7, "  OWNER@example.COM  "},
		{"明确密码但新邮箱不同", PlatformSub2API, AuthModePassword, false, false, true, false, false, 7, "other@example.com"},
		{"明确密码但新邮箱缺失", PlatformSub2API, AuthModePassword, false, false, true, false, false, 7, ""},
		{"已核实编号不回退邮箱", PlatformSub2API, AuthModePassword, true, false, false, false, false, 8, "owner@example.com"},
		{"旧Sub2API方式不推断邮箱", PlatformSub2API, "", false, false, false, false, false, 7, "owner@example.com"},
		{"旧NewAPI方式不推断密码编号", PlatformNewAPI, "", false, true, false, false, false, 7, ""},
		{"手填Key编号不能代替原身份", PlatformNewAPI, AuthModeUserKey, false, false, false, false, false, 7, ""},
		{"无可信资料可读原账号一次", PlatformSub2API, AuthModeToken, false, false, false, true, true, 7, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchFixture()
			f.newID, f.email = tc.newID, tc.email
			svc, c, r, site := switchSetup(t, f, tc.platform, tc.mode, tc.confirmed)
			if tc.cookie {
				site.Session.Cookie, site.Session.AccessToken = "fixture-session=old", ""
			}
			if tc.clearID {
				site.Session.UserID = ""
			}
			c.add(site)
			r.durable = cloneSite(site)
			oldRequests := 0
			transport := svc.platformService.httpClient.client.Transport
			svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "fixture.invalid" {
					oldRequests++
					if !tc.oldAllowed {
						return migrationFixtureResponse(403, map[string]any{"message": "原域名已经停止账号读取"}), nil
					}
				}
				return transport.RoundTrip(req)
			})
			next := AuthModeToken
			if tc.platform == PlatformNewAPI {
				next = AuthModeUserKey
			}
			dto := migrationConfirmedDTO(t, site, next, true)
			if tc.name == "手填Key编号不能代替原身份" {
				dto.UserID = "7"
			}
			_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("原身份可核对应成功：%v", err)
				}
			} else {
				if err == nil {
					t.Fatal("无法核对原账号却迁移成功")
				}
				switchUnchanged(t, c, r, site)
			}
			untrusted := !tc.confirmed && (tc.mode == "" || tc.mode == AuthModeToken || tc.mode == AuthModeUserKey)
			if untrusted && oldRequests != 1 || !untrusted && oldRequests != 0 {
				t.Fatal("原身份依据没有按明确保存资料优先选择")
			}
			if untrusted && !tc.oldAllowed {
				var detail *RequestError
				if f.newReads != 0 || f.logins != 0 || !errors.As(err, &detail) || !strings.Contains(detail.UpstreamMessage, "原域名已经停止账号读取") {
					t.Fatal("原身份不足时发送了新凭据或丢失原失败原因")
				}
			}
		})
	}
}

func TestDomainMigrationAdditionalPreflightAndSameURL(t *testing.T) {
	for _, kind := range []string{"缺确认", "无原Session", "换平台", "换密码账号", "换方式缺凭据", "规范化同址"} {
		t.Run(kind, func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModePassword, true)
			if kind == "无原Session" {
				site.Session = nil
				c.add(site)
				r.durable = cloneSite(site)
			}
			dto := migrationConfirmedDTO(t, site, AuthModePassword, false)
			switch kind {
			case "缺确认":
				dto.ConfirmDomainMigration = false
			case "换平台":
				dto.Platform = PlatformNewAPI
			case "换密码账号":
				dto.Account = "another@example.com"
			case "换方式缺凭据":
				dto.AuthMode = AuthModeToken
			case "规范化同址":
				dto.SiteURL, dto.ConfirmDomainMigration = "  http://fixture.invalid///?page=1#fragment  ", false
			}
			_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
			if kind == "规范化同址" {
				if err != nil || r.checks != 1 {
					t.Fatalf("规范化同址应保留旧编辑：%v", err)
				}
			} else {
				if err == nil {
					t.Fatal("准入失败却保存")
				}
				switchUnchanged(t, c, r, site)
			}
			if len(f.paths) != 0 {
				t.Fatal("本地准入或仅编辑资料不应请求上游")
			}
		})
	}
}

func TestDomainMigrationAdditionalReuseFailures(t *testing.T) {
	for _, kind := range []string{"编号不同", "编号非法", "401", "网络", "整体指标"} {
		t.Run(kind, func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
			oldRequests := migrationOnlyNewHost(t, svc)
			transport := svc.platformService.httpClient.client.Transport
			svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/v1/auth/me" {
					switch kind {
					case "编号不同":
						return migrationFixtureResponse(200, map[string]any{"data": map[string]any{"id": 8}}), nil
					case "编号非法":
						return migrationFixtureResponse(200, map[string]any{"data": map[string]any{"id": "not-a-number"}}), nil
					case "401":
						return migrationFixtureResponse(401, map[string]any{"message": "原令牌已失效，请重新登录"}), nil
					case "网络":
						return nil, errors.New("fixture network interrupted")
					}
				}
				if kind == "整体指标" && req.URL.Path == "/api/v1/usage/dashboard/stats" {
					return migrationFixtureResponse(503, map[string]any{"message": "网站统计资料暂时无法读取"}), nil
				}
				return transport.RoundTrip(req)
			})
			_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeToken, false))
			if err == nil || *oldRequests != 0 || f.logins != 0 || f.paths["/api/v1/auth/refresh"] != 0 {
				t.Fatal("迁移失败或旧RefreshToken保护失效")
			}
			var detail *RequestError
			if !errors.As(err, &detail) {
				t.Fatalf("缺少详细失败原因：%v", err)
			}
			if kind == "401" && (!strings.Contains(detail.UpstreamMessage, "原令牌已失效，请重新登录") || !strings.Contains(detail.UpstreamMessage, "填写当前登录方式的新凭据")) {
				t.Fatal("没有保留上游原因和新凭据处理指引")
			}
			if kind == "整体指标" && (detail.Stage != "metrics" || !strings.Contains(detail.UpstreamMessage, "网站统计资料暂时无法读取")) {
				t.Fatal("整体指标失败没有指向实际步骤和原因")
			}
			switchUnchanged(t, c, r, site)
		})
	}
}

type migrationFailingCache struct{ SiteCache }

func (c migrationFailingCache) Set(context.Context, *Site) error {
	return errors.New("fixture cache unavailable")
}

func TestDomainMigrationAdditionalPersistenceOutcomes(t *testing.T) {
	for _, kind := range []string{"数据库失败", "提交未知", "缓存失败"} {
		t.Run(kind, func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
			migrationOnlyNewHost(t, svc)
			want := "本站资料未保存，原数据库资料保留"
			switch kind {
			case "数据库失败":
				r.saveError = errors.New("fixture database unavailable")
			case "提交未知":
				r.saveError = &siteCommitError{err: errors.New("fixture commit response lost")}
				want = "数据库保存结果尚未确认"
			case "缓存失败":
				svc.cache = migrationFailingCache{SiteCache: c}
				want = "本站资料已保存，当前运行数据未更新"
			}
			_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeToken, true))
			var detail *RequestError
			if !errors.As(err, &detail) || detail.Stage != "save" || !strings.Contains(detail.UpstreamMessage, want) || strings.Count(detail.UpstreamMessage, migrationRefreshWarning) != 1 {
				t.Fatalf("保存实际结果或令牌处理指引不准确：%v", err)
			}
			got, _ := c.Get(t.Context(), site.ID)
			if !reflect.DeepEqual(got, site) || c.writes != 0 {
				t.Fatal("失败后把新登录资料单独写入原地址缓存")
			}
			if kind == "缓存失败" {
				if r.durable.BaseURL != "http://migrated.invalid" || r.durable.Session.BaseURL != r.durable.BaseURL || strings.Contains(detail.UpstreamMessage, "原数据库资料保留") {
					t.Fatal("数据库已保存却声称回滚或双地址不一致")
				}
			} else if !reflect.DeepEqual(r.durable, cloneSite(site)) {
				t.Fatal("数据库失败仍覆盖原资料")
			}
		})
	}
}

func TestDomainMigrationAdditionalConcurrentConfigurationAndCancellation(t *testing.T) {
	for _, kind := range []string{"地址", "平台", "账号", "取消新登录"} {
		t.Run(kind, func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
			migrationOnlyNewHost(t, svc)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "取消新登录" {
				f.beforePrincipal = func(*http.Request) error { cancel(); return nil }
			} else {
				f.beforeOld = func(*http.Request) error {
					current := cloneSite(site)
					switch kind {
					case "地址":
						current.BaseURL = "http://another-migration.invalid"
					case "平台":
						current.Platform = PlatformNewAPI
					case "账号":
						current.Account = "changed@example.com"
					}
					c.add(&current)
					return nil
				}
			}
			_, err := svc.Update(ctx, site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeToken, kind == "取消新登录"))
			if err == nil || r.checks != 0 || c.writes != 0 || !reflect.DeepEqual(r.durable, cloneSite(site)) {
				t.Fatal("旧请求或已取消请求仍发布新地址")
			}
			if kind == "取消新登录" && !errors.Is(err, context.Canceled) {
				t.Fatal("新登录结束后没有检查取消")
			}
		})
	}
}

func TestDomainMigrationAdditionalPartialCostFailurePreservesPriorValue(t *testing.T) {
	f := newSwitchFixture()
	f.failCost = true
	svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
	observed := switchClock()
	prior := 123.456
	site.Metrics.TodayConsume = metric(&prior)
	site.Metrics.TodayConsumeStatus = "ok"
	site.Metrics.TodayConsumeDate = businesstime.DateAt(observed)
	site.Metrics.TodayConsumeTimezone = businesstime.Timezone
	site.Metrics.TodayConsumeAt = &observed
	c.add(site)
	r.durable = cloneSite(site)
	migrationOnlyNewHost(t, svc)
	if _, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeToken, false)); err != nil {
		t.Fatalf("既有局部费用失败不能升级为迁移失败：%v", err)
	}
	got, _ := c.Get(t.Context(), site.ID)
	if got.BaseURL != "http://migrated.invalid" || got.Metrics.TodayConsumeStatus != "unreadable" || got.Metrics.TodayConsume.Value == nil || *got.Metrics.TodayConsume.Value != prior || !reflect.DeepEqual(got.Metrics.TodayConsumeAt, site.Metrics.TodayConsumeAt) {
		t.Fatal("局部失败没有按原规则保留费用和警告")
	}
}

func TestDomainMigrationAdditionalSameAddressPasswordKeepsMetricsFallback(t *testing.T) {
	f := newSwitchFixture()
	svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModePassword, true)
	transport := svc.platformService.httpClient.client.Transport
	failed := false
	svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/user/self" && req.Header.Get("Cookie") == "fixture-session=new" && !failed {
			failed = true
			return migrationFixtureResponse(503, map[string]any{"message": "fixture whole metrics unavailable"}), nil
		}
		return transport.RoundTrip(req)
	})
	if _, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModePassword)); err != nil || !failed || r.checks != 1 || c.writes != 1 {
		t.Fatalf("同址密码登录的既有整体指标降级发生变化：%v", err)
	}
}

func TestDomainMigrationAdditionalReturnedIdentityGuards(t *testing.T) {
	for _, kind := range []string{"自动识别不同平台", "登录返回地址不同", "手填Key编号不同"} {
		t.Run(kind, func(t *testing.T) {
			f := newSwitchFixture()
			platform, mode := PlatformSub2API, AuthModePassword
			if kind == "手填Key编号不同" {
				platform, mode = PlatformNewAPI, AuthModeUserKey
			}
			svc, c, r, site := switchSetup(t, f, platform, mode, true)
			migrationOnlyNewHost(t, svc)
			dto := migrationConfirmedDTO(t, site, mode, true)
			var err error
			switch kind {
			case "自动识别不同平台":
				dto.Platform = PlatformAuto
				_, err = svc.Update(t.Context(), site.UserID, site.ID, dto)
			case "登录返回地址不同":
				// Exercise the result-address guard with a target different from the
				// real PlatformService login result; Update always passes NormalizeURL.
				_, err = svc.domainMigrationLogin(t.Context(), cloneSite(site), dto, "http://unexpected.invalid", true, "")
			case "手填Key编号不同":
				dto.UserID = "8"
				_, err = svc.Update(t.Context(), site.UserID, site.ID, dto)
			}
			if err == nil {
				t.Fatal("实际地址、平台或Key用户归属不符仍通过核对")
			}
			switchUnchanged(t, c, r, site)
		})
	}
}

func TestDomainMigrationAdditionalPartialRateFailureStillMigrates(t *testing.T) {
	f := newSwitchFixture()
	svc, c, _, site := switchSetup(t, f, PlatformNewAPI, AuthModePassword, true)
	migrationOnlyNewHost(t, svc)
	transport := svc.platformService.httpClient.client.Transport
	svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/user/self/groups" || req.URL.Path == "/api/user/groups" || req.URL.Path == "/api/pricing" {
			return migrationFixtureResponse(503, map[string]any{"message": "fixture partial rates unavailable"}), nil
		}
		return transport.RoundTrip(req)
	})
	if _, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModePassword, false)); err != nil {
		t.Fatalf("既有局部分组倍率降级不能拒绝迁移：%v", err)
	}
	got, _ := c.Get(t.Context(), site.ID)
	if got.BaseURL != "http://migrated.invalid" || got.Metrics.RateNoticeObservation == nil || got.Metrics.RateNoticeObservation.Confirmed || got.Metrics.RateNoticeObservation.Reason == "" {
		t.Fatal("没有迁移到新址或丢失未确认倍率的警告")
	}
}
