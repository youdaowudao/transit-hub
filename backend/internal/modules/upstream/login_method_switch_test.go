package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/shared/businesstime"

	"transithub/backend/internal/shared/authctx"
)

const switchRefreshWarning = "填写的 Refresh Token 可能已被使用，重试前请到上游重新获取。"
const switchIdentityKey = "admin.upstream.errors.identityCheckFailed"

// 主代理冻结 PLAN 3.1 的业务期望；全部请求由 httptest recorder 接管，无监听或真实上游。
type switchFixture struct {
	beforeOld                  func(*http.Request) error
	principalBody              func(*http.Request) io.ReadCloser
	expectNoHeaderFirst        bool
	failCost                   bool
	oldError                   error
	noOldHint                  bool
	oldStatus                  int
	oldID, newID               any
	email                      string
	newReads, oldReads, logins int
	paths                      map[string]int
	oldMissing                 bool
	requireHeader              bool
	rejectLogin                bool
	beforePrincipal            func(*http.Request) error
	transportError             error
	newPrincipalError          error
	createReadFailure          bool
}

func newSwitchFixture() *switchFixture {
	return &switchFixture{oldStatus: 200, oldID: 7, newID: 7, email: "owner@example.com", paths: map[string]int{}}
}
func (f *switchFixture) platform(t *testing.T) *PlatformService {
	t.Helper()
	client := &http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if f.transportError != nil {
			return nil, f.transportError
		}
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		f.paths[r.URL.Path]++
		if f.requireHeader && r.Header.Get("Authorization") == "Bearer fixture-new" && r.Header.Get("New-Api-User") != "42" {
			t.Errorf("新Key请求 %s 未使用原编号42", r.URL.Path)
		}
		old := r.Header.Get("Authorization") == "Bearer fixture-old" || r.Header.Get("Cookie") == "fixture-session=old"
		principal := r.URL.Path == "/api/v1/auth/me" || r.URL.Path == "/api/user/self"
		if principal && old {
			if f.beforeOld != nil {
				if err := f.beforeOld(r); err != nil {
					return nil, err
				}
			}
			if f.oldError != nil {
				return nil, f.oldError
			}
			f.oldReads++
			w.WriteHeader(f.oldStatus)
			id := f.oldID
			if f.oldMissing {
				id = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": func() string {
				if f.noOldHint {
					return ""
				}
				return "原登录已失效"
			}(), "data": map[string]any{"id": id, "balance": 20, "quota": 100}})
			return w.Result(), nil
		}
		if principal {
			f.newReads++
			if f.expectNoHeaderFirst && r.URL.Path == "/api/user/self" && f.newReads == 1 && r.Header.Get("New-Api-User") != "" {
				t.Error("新建自动读取用户编号时不应带New-Api-User")
			}
			// Sub2API 登录本身读取一次 me；第二次才是 Update/Create 明确核对编号。
			threshold := 2
			if r.URL.Path == "/api/user/self" {
				threshold = 3
			}
			if f.principalBody != nil && f.newReads == threshold {
				response := w.Result()
				response.Body = f.principalBody(r)
				return response, nil
			}
			if f.beforePrincipal != nil && f.newReads == threshold {
				if err := f.beforePrincipal(r); err != nil {
					return nil, err
				}
			}
			if f.newPrincipalError != nil && f.newReads == threshold {
				return nil, f.newPrincipalError
			}
			if f.createReadFailure && f.newReads >= 2 {
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"message":"编号接口临时不可用"}`)
				return w.Result(), nil
			}
			if f.requireHeader && r.Header.Get("Authorization") == "Bearer fixture-new" && r.Header.Get("New-Api-User") != "42" {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"message":"未提供 New-Api-User"}`)
				return w.Result(), nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": f.newID, "email": f.email, "balance": 20, "quota": 100}})
			return w.Result(), nil
		}
		switch r.URL.Path {
		case "/api/v1/auth/refresh", "/api/v1/auth/login", "/api/user/login":
			f.logins++
			if f.rejectLogin {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"message":"原样保留的登录拒绝"}`)
				return w.Result(), nil
			}
			w.Header().Set("Set-Cookie", "fixture-session=new; Path=/")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": f.newID, "access_token": "fixture-new", "refresh_token": "fixture-rotated", "expires_in": 3600}})
		case "/api/v1/usage/stats":
			if f.failCost {
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"message":"费用接口临时不可用"}`)
				return w.Result(), nil
			}
			_, _ = io.WriteString(w, `{"data":{"total_actual_cost":3}}`)
		case "/api/v1/usage/dashboard/stats":
			_, _ = io.WriteString(w, `{"data":{"today_actual_cost":3,"total_cost":9}}`)
		case "/api/log/self/stat":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":0}}`)
		case "/api/status":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":500000}}`)
		default:
			_, _ = io.WriteString(w, `{"success":true,"data":[]}`)
		}
		return w.Result(), nil
	})}
	platform := NewPlatformService(NewHTTPClient(client))
	platform.now = switchClock
	return platform
}
func switchConfirmed(s *Session, confirmed bool) {
	raw, _ := json.Marshal(s)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	data["UserIDConfirmed"] = confirmed
	raw, _ = json.Marshal(data)
	_ = json.Unmarshal(raw, s)
}
func switchIsConfirmed(s *Session) bool {
	raw, _ := json.Marshal(s)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	return data["UserIDConfirmed"] == true
}
func switchSetup(t *testing.T, f *switchFixture, p Platform, mode AuthMode, confirmed bool) (*Service, *review3WriteCountingCache, *singaporeCostCriticalRepository, *Site) {
	t.Helper()
	failureFixCaptureLogs(t)
	session := &Session{Platform: p, AuthMode: mode, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old", UserID: "7"}
	if p == PlatformNewAPI && mode == AuthModePassword {
		session.Cookie = "fixture-session=old"
		session.AccessToken = ""
	}
	switchConfirmed(session, confirmed)
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, session)
	site.Platform = p
	site.BaseURL = session.BaseURL
	site.RequestedPlatform = p
	site.Account = "Owner@Example.com"
	if p == PlatformNewAPI && mode == AuthModeUserKey {
		site.Account = "7"
	}
	cache := &review3WriteCountingCache{fakeSiteCache: newFakeSiteCache()}
	cache.add(site)
	repo := &singaporeCostCriticalRepository{durable: cloneSite(site)}
	svc := NewService(f.platform(t), repo, nil, cache)
	svc.now = switchClock
	svc.SetBackgroundTasksDisabled(true)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"fixture-user": "fixture-workspace"}})
	t.Cleanup(svc.Close)
	return svc, cache, repo, site
}
func switchDTO(site *Site, mode AuthMode) UpdateRequest {
	dto := UpdateRequest{Name: site.Name, SiteURL: site.BaseURL, Platform: site.Platform, AuthMode: mode, Account: site.Account, RechargeRate: 1}
	switch mode {
	case AuthModeToken:
		dto.RefreshToken = "fixture-refresh"
	case AuthModeUserKey:
		dto.AccessToken = "fixture-new"
	default:
		dto.Password = "fixture-password"
	}
	return dto
}
func switchError(t *testing.T, err error, contains string) *RequestError {
	t.Helper()
	var e *RequestError
	if !errors.As(err, &e) {
		t.Fatalf("缺少结构化失败：%v", err)
	}
	if e.MessageKey != switchIdentityKey || e.Stage != "check" || e.StatusCode != 0 || !strings.Contains(e.UpstreamMessage, contains) {
		t.Fatalf("本地核对失败不符合 SPEC：%+v，期望原因 %s", e, contains)
	}
	return e
}
func switchUnchanged(t *testing.T, c *review3WriteCountingCache, r *singaporeCostCriticalRepository, before *Site) {
	t.Helper()
	got, _ := c.Get(t.Context(), before.ID)
	if !reflect.DeepEqual(got, before) || !reflect.DeepEqual(r.durable, cloneSite(before)) || c.writes != 0 || r.checks != 0 || r.unsafeWrites != 0 {
		t.Fatal("拒绝后缓存、仓库、登录资料或费用发生变化")
	}
}
func switchSuccess(t *testing.T, c *review3WriteCountingCache, r *singaporeCostCriticalRepository, site *Site, mode AuthMode, id, account string) {
	t.Helper()
	got, _ := c.Get(t.Context(), site.ID)
	if got.Session.AuthMode != mode || got.Session.UserID != id || !switchIsConfirmed(got.Session) || got.Account != account || r.checks != 1 || r.unsafeWrites != 0 {
		t.Fatalf("保存的方式、编号、标记、账号或条件保存次数错误：mode=%s id=%s confirmed=%v account=%s checks=%d", got.Session.AuthMode, got.Session.UserID, switchIsConfirmed(got.Session), got.Account, r.checks)
	}
	if !reflect.DeepEqual(got.Metrics, mergeCostReadStatus(got.Metrics, site.Metrics)) {
		t.Fatal("费用合并规则被破坏")
	}
}

func TestLoginMethodSwitchSub2API(t *testing.T) { // PLAN 1,2,3,3a,3b,4,5,11,12,12a,16,17
	cases := []struct {
		name                  string
		oldMode, newMode      AuthMode
		oldStatus             int
		oldMissing, confirmed bool
		newID                 any
		email, account, want  string
		noLogin, accessOnly   bool
	}{
		{"1相同编号换Refresh", AuthModePassword, AuthModeToken, 200, false, false, 7, "owner@example.com", "Owner@Example.com", "", false, false},
		{"2不同编号", AuthModePassword, AuthModeToken, 200, false, false, 8, "owner@example.com", "Owner@Example.com", "上游用户编号与原账号不同", false, false},
		{"3邮箱兜底换方式", AuthModePassword, AuthModeToken, 401, false, false, 7, "owner@example.com", " Owner@Example.com ", "", false, false},
		{"3邮箱兜底同方式", AuthModePassword, AuthModePassword, 401, false, false, 7, "owner@example.com", "Owner@Example.com", "", false, false},
		{"3邮箱不同", AuthModePassword, AuthModeToken, 401, false, false, 7, "other@example.com", "Owner@Example.com", "新登录的邮箱与原站点保存的登录邮箱不同", false, false},
		{"3没有邮箱", AuthModePassword, AuthModeToken, 401, false, false, 7, "", "Owner@Example.com", "上游没有返回新登录的邮箱", false, false},
		{"3a编号优先", AuthModePassword, AuthModeToken, 401, false, true, 8, "owner@example.com", "Owner@Example.com", "原站点保存的已核实编号", false, false},
		{"3b未知旧方式", AuthMode(""), AuthModeToken, 401, false, false, 7, "owner@example.com", "Owner@Example.com", "本次没有用新凭据登录", true, false},
		{"4缺旧编号邮箱兜底", AuthModePassword, AuthModeToken, 200, true, false, 7, "owner@example.com", "Owner@Example.com", "", false, false},
		{"4缺旧编号Token拒绝", AuthModeToken, AuthModeToken, 200, true, false, 7, "owner@example.com", "Owner@Example.com", "上游回复成功，但没有返回合法的用户编号", true, false},
		{"5已核实同编号", AuthModeToken, AuthModeToken, 401, false, true, 7, "owner@example.com", "Owner@Example.com", "", false, false},
		{"5已核实不同编号", AuthModeToken, AuthModeToken, 401, false, true, 8, "owner@example.com", "Owner@Example.com", "原站点保存的已核实编号", false, false},
		{"5隐藏邮箱不可兜底", AuthModeToken, AuthModeToken, 401, false, false, 7, "owner@example.com", "someone@example.com", "本次没有用新凭据登录", true, false},
		{"11Token换密码", AuthModeToken, AuthModePassword, 200, false, false, 7, "owner@example.com", "", "", false, false},
		{"12Token同方式", AuthModeToken, AuthModeToken, 200, false, false, 7, "owner@example.com", "Owner@Example.com", "", false, false},
		{"12aAccess不同编号无提醒", AuthModeToken, AuthModeToken, 200, false, false, 8, "owner@example.com", "Owner@Example.com", "上游用户编号与原账号不同", false, true},
		{"17缺新编号", AuthModeToken, AuthModeToken, 200, false, false, nil, "owner@example.com", "Owner@Example.com", "上游没有返回合法的用户编号", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchFixture()
			f.oldStatus = tc.oldStatus
			f.oldMissing = tc.oldMissing
			f.newID = tc.newID
			f.email = tc.email
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, tc.oldMode, tc.confirmed)
			site.Account = tc.account
			c.add(site)
			r.durable = cloneSite(site)
			dto := switchDTO(site, tc.newMode)
			if tc.name == "11Token换密码" {
				dto.Account = "owner@example.com"
			}
			if tc.accessOnly {
				dto.RefreshToken = ""
				dto.AccessToken = "fixture-new"
			}
			_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
			if tc.want != "" {
				e := switchError(t, err, tc.want)
				switchUnchanged(t, c, r, site)
				wantWarning := tc.newMode == AuthModeToken && !tc.noLogin && !tc.accessOnly
				if strings.Contains(e.UpstreamMessage, switchRefreshWarning) != wantWarning {
					t.Fatal("Refresh 提醒与凭据是否已使用不符")
				}
			} else {
				if err != nil {
					t.Fatalf("应成功：%v", err)
				}
				account := site.Account
				if tc.newMode == AuthModePassword {
					account = strings.TrimSpace(dto.Account)
				}
				switchSuccess(t, c, r, site, tc.newMode, "7", account)
			}
			if tc.noLogin && (f.logins != 0 || f.newReads != 0) {
				t.Fatal("没有可信兜底资料仍使用新凭据")
			}
		})
	}
	t.Run("16上游原文", func(t *testing.T) {
		f := newSwitchFixture()
		f.rejectLogin = true
		svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, false)
		_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
		var e *RequestError
		if !errors.As(err, &e) || e.StatusCode != 401 || e.Stage != "refresh" || e.UpstreamMessage != "原样保留的登录拒绝" {
			t.Fatalf("上游错误被改写：%+v", e)
		}
		switchUnchanged(t, c, r, site)
	})
}

func TestLoginMethodSwitchNewAPI(t *testing.T) { // PLAN 6–10
	for _, tc := range []struct {
		name                        string
		oldMode, newMode            AuthMode
		oldStatus                   int
		confirmed                   bool
		sessionID, account, inputID string
		newID                       int
		want                        string
		noLogin                     bool
	}{
		{"6密码换Key补头", AuthModePassword, AuthModeUserKey, 200, false, "42", "owner", "", 42, "", false},
		{"7Key换密码", AuthModeUserKey, AuthModePassword, 200, false, "42", "42", "", 42, "", false},
		{"8密码兜底不同", AuthModePassword, AuthModeUserKey, 401, false, "42", "owner", "", 43, "上游用户编号", false},
		{"8密码兜底相同", AuthModePassword, AuthModeUserKey, 401, false, "42", "owner", "", 42, "", false},
		{"8旧Key不可信", AuthModeUserKey, AuthModeUserKey, 401, false, "42", "42", "", 42, "本次没有用新凭据登录", true},
		{"8核实Key相同", AuthModeUserKey, AuthModeUserKey, 401, true, "42", "42", "", 42, "", false},
		{"8核实Key不同", AuthModeUserKey, AuthModeUserKey, 401, true, "42", "42", "", 43, "上游用户编号", false},
		{"9账号补头", AuthModeUserKey, AuthModeUserKey, 200, false, "", "42", "", 42, "", false},
		{"10手填编号不符", AuthModeUserKey, AuthModeUserKey, 200, false, "42", "42", "43", 42, "Key 实际属于用户编号 42，与填写的用户 ID 43 不同", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchFixture()
			f.oldID = 42
			f.newID = tc.newID
			f.oldStatus = tc.oldStatus
			f.requireHeader = strings.HasPrefix(tc.name, "6") || strings.HasPrefix(tc.name, "9")
			svc, c, r, site := switchSetup(t, f, PlatformNewAPI, tc.oldMode, tc.confirmed)
			site.Session.UserID = tc.sessionID
			site.Account = tc.account
			c.add(site)
			r.durable = cloneSite(site)
			dto := switchDTO(site, tc.newMode)
			dto.UserID = tc.inputID
			if tc.newMode == AuthModePassword {
				dto.Account = "owner"
			}
			_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
			if tc.want != "" {
				switchError(t, err, tc.want)
				switchUnchanged(t, c, r, site)
			} else {
				if err != nil {
					t.Fatalf("应成功：%v", err)
				}
				account := strconv.Itoa(tc.newID)
				if tc.newMode == AuthModePassword {
					account = dto.Account
				}
				switchSuccess(t, c, r, site, tc.newMode, strconv.Itoa(tc.newID), account)
			}
			if tc.noLogin && (f.newReads != 0 || f.logins != 0) {
				t.Fatal("旧Key未核实仍执行登录")
			}
		})
	}
}

func switchHandler(svc *Service, site *Site, dto UpdateRequest) (int, map[string]any) {
	return switchHandlerContext(context.Background(), svc, site, dto)
}
func switchHandlerContext(ctx context.Context, svc *Service, site *Site, dto UpdateRequest) (int, map[string]any) {
	raw, _ := json.Marshal(dto)
	req := httptest.NewRequest(http.MethodPut, "/api/upstream-sites/"+site.ID, bytes.NewReader(raw))
	req = req.WithContext(authctx.WithUserID(ctx, site.UserID))
	w := httptest.NewRecorder()
	(&Handler{service: svc, accounts: svc.accounts}).update(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}
func TestLoginMethodSwitchBeforeLogin(t *testing.T) { // PLAN 13–15,25
	for _, tc := range []struct {
		p         Platform
		old, next AuthMode
		field     string
	}{{PlatformSub2API, AuthModePassword, AuthModeToken, "refreshToken"}, {PlatformSub2API, AuthModeToken, AuthModePassword, "password"}, {PlatformNewAPI, AuthModePassword, AuthModeUserKey, "accessToken"}} {
		t.Run("13"+tc.field, func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, tc.p, tc.old, false)
			dto := switchDTO(site, tc.next)
			dto.Password = ""
			dto.RefreshToken = ""
			dto.AccessToken = ""
			status, body := switchHandler(svc, site, dto)
			failure, _ := body["failure"].(map[string]any)
			fields, _ := failure["fields"].([]any)
			if status != 400 || body["message"] != ErrorInvalidFields || len(fields) != 1 || fields[0] != tc.field {
				t.Fatalf("字段错误不符：%d %+v", status, body)
			}
			if len(f.paths) != 0 {
				t.Fatal("空凭据请求了上游")
			}
			switchUnchanged(t, c, r, site)
		})
	}
	t.Run("14差异有序", func(t *testing.T) {
		f := newSwitchFixture()
		svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModePassword, false)
		dto := switchDTO(site, AuthModePassword)
		dto.SiteURL = "http://another.invalid"
		dto.Platform = PlatformNewAPI
		dto.Account = "another"
		status, body := switchHandler(svc, site, dto)
		failure, _ := body["failure"].(map[string]any)
		if status != 422 || failure["errorKey"] != switchIdentityKey || failure["stage"] != "check" || failure["httpStatus"] != float64(0) || !strings.Contains(failure["upstreamMessage"].(string), "站点地址、平台、登录账号与原站点不同") {
			t.Fatalf("R1结构错误：%d %+v", status, body)
		}
		if len(f.paths) != 0 {
			t.Fatal("本地拦截请求上游")
		}
		switchUnchanged(t, c, r, site)
	})
	t.Run("15只改备注不请求", func(t *testing.T) {
		f := newSwitchFixture()
		svc, c, r, site := switchSetup(t, f, PlatformNewAPI, AuthModeUserKey, false)
		dto := switchDTO(site, AuthModeUserKey)
		dto.AccessToken = ""
		dto.UserID = ""
		dto.Remark = "备注"
		_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
		got, _ := c.Get(t.Context(), site.ID)
		if err != nil || got.Account != site.Account || got.Remark != "备注" || len(f.paths) != 0 || r.checks != 1 {
			t.Fatalf("仅改资料行为改变：%v", err)
		}
	})
	for _, update := range []bool{false, true} {
		t.Run("25校验"+strconv.FormatBool(update), func(t *testing.T) {
			dto := CreateRequest{Name: "fixture", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, AccessToken: "fixture-new", RechargeRate: 1}
			var err error
			if update {
				err = validateUpdate(UpdateRequest(dto))
			} else {
				err = validateCreate(dto)
			}
			if err != nil {
				t.Fatalf("Key方式留空用户ID不能被字段校验拒绝：%v", err)
			}
		})
	}
}

func TestLoginMethodSwitchCancellationAndSave(t *testing.T) { // PLAN 18,19,19a
	t.Run("18读旧时取消", func(t *testing.T) {
		f := newSwitchFixture()
		svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModePassword, true)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.beforeOld = func(*http.Request) error { cancel(); return ctx.Err() }
		_, err := svc.Update(ctx, site.UserID, site.ID, switchDTO(site, AuthModeToken))
		if !errors.Is(err, context.Canceled) || f.logins != 0 {
			t.Fatalf("取消后仍登录或未返回取消：%v", err)
		}
		switchUnchanged(t, c, r, site)
	})
	for _, cancelled := range []bool{false, true} {
		t.Run("18读新"+strconv.FormatBool(cancelled), func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, false)
			f.newPrincipalError = context.DeadlineExceeded
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				f.newPrincipalError = nil
				f.beforePrincipal = func(*http.Request) error { cancel(); return ctx.Err() }
			}
			status, body := switchHandlerContext(ctx, svc, site, switchDTO(site, AuthModeToken))
			failure, _ := body["failure"].(map[string]any)
			if status != 422 || failure["stage"] != "verify" {
				t.Fatalf("读取错误必须422/verify：%d %+v", status, body)
			}
			if cancelled {
				if failure["upstreamMessage"] != "读取已取消，本次没有保存" {
					t.Fatal("取消原因缺失")
				}
			} else if failure["errorKey"] != ErrorNetworkTimeout {
				t.Fatal("超时原因错误")
			}
			switchUnchanged(t, c, r, site)
		})
	}
	for _, kind := range []string{"换方式", "同方式账号不变", "纠正编号"} {
		t.Run("19"+kind, func(t *testing.T) {
			f := newSwitchFixture()
			p := PlatformSub2API
			old, next := AuthModePassword, AuthModeToken
			if kind == "同方式账号不变" {
				old = AuthModeToken
			}
			if kind == "纠正编号" {
				p = PlatformNewAPI
				old = AuthModeUserKey
				next = AuthModeUserKey
				f.oldID = 42
				f.newID = 42
			}
			svc, c, r, site := switchSetup(t, f, p, old, false)
			if kind == "纠正编号" {
				site.Account = "43"
				site.Session.UserID = "43"
				c.add(site)
				r.durable = cloneSite(site)
			}
			r.saveError = errors.New("fixture PG unavailable")
			dto := switchDTO(site, next)
			_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
			var e *RequestError
			if !errors.As(err, &e) || e.Stage != "save" {
				t.Fatalf("未到达保存失败：%v", err)
			}
			got, _ := c.Get(t.Context(), site.ID)
			if kind == "同方式账号不变" {
				if got.Session.AccessToken != "fixture-new" || got.Account != site.Account {
					t.Fatal("原有保留新登录行为丢失")
				}
			} else if !reflect.DeepEqual(got, site) {
				t.Fatal("保存失败仍发布了换方式或纠正编号")
			}
			if !reflect.DeepEqual(r.durable, cloneSite(site)) {
				t.Fatal("失败后数据库变化")
			}
		})
	}
	t.Run("19a后台先刷新", func(t *testing.T) {
		f := newSwitchFixture()
		svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, false)
		background := cloneSite(site)
		background.Session.AccessToken = "fixture-background"
		f.beforePrincipal = func(*http.Request) error { svc.mu.Lock(); defer svc.mu.Unlock(); c.add(&background); return nil }
		_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
		var e *RequestError
		if !errors.As(err, &e) || e.StatusCode != 409 || !strings.Contains(e.UpstreamMessage, "站点登录资料已更新") {
			t.Fatalf("迟到结果未拒绝：%v", err)
		}
		got, _ := c.Get(t.Context(), site.ID)
		if !reflect.DeepEqual(got, &background) || r.checks != 0 {
			t.Fatal("本次修改覆盖了后台刷新")
		}
	})
}

func TestLoginMethodSwitchCreateAndRefresh(t *testing.T) { // PLAN 20,21,22,23,24
	for _, mode := range []AuthMode{AuthModePassword, AuthModeToken} {
		for _, readFailure := range []bool{false, true} {
			t.Run("20"+string(mode)+strconv.FormatBool(readFailure), func(t *testing.T) {
				failureFixCaptureLogs(t)
				f := newSwitchFixture()
				f.createReadFailure = readFailure
				svc, c, r := failureFixService(t, f.platform(t))
				svc.now = switchClock
				svc.SetBackgroundTasksDisabled(true)
				dto := CreateRequest{Name: "fixture", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: mode, Account: "owner@example.com", Password: "fixture-password", RefreshToken: "fixture-refresh", RechargeRate: 1}
				response, err := svc.Create(t.Context(), "fixture-user", dto)
				if err != nil || len(r.saved) != 1 {
					t.Fatalf("新建编号读取失败不能阻断：%v", err)
				}
				site, _ := c.Get(t.Context(), response.ID)
				if readFailure {
					if site.Session.UserID != "" || switchIsConfirmed(site.Session) {
						t.Fatal("读取失败误记编号")
					}
					if mode == AuthModePassword {
						f.createReadFailure = false
						f.oldStatus = 401
						site.Session.AccessToken = "fixture-old"
						c.add(site)
						_, err = svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
						if err != nil {
							t.Fatalf("连续场景邮箱兜底失败：%v", err)
						}
					}
				} else if site.Session.UserID != "7" || !switchIsConfirmed(site.Session) {
					t.Fatal("新建没有记录已核实编号")
				}
			})
		}
	}
	t.Run("10新建手填不同", func(t *testing.T) {
		f := newSwitchFixture()
		f.newID = 42
		svc, c, r := failureFixService(t, f.platform(t))
		svc.now = switchClock
		svc.SetBackgroundTasksDisabled(true)
		_, err := svc.Create(t.Context(), "fixture-user", CreateRequest{Name: "fixture", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "43", AccessToken: "fixture-new", RechargeRate: 1})
		switchError(t, err, "Key 实际属于用户编号 42，与填写的用户 ID 43 不同")
		if len(c.sites) != 0 || len(r.saved) != 0 {
			t.Fatal("编号不符仍保存")
		}
	})
	for _, id := range []any{42, nil, 0, -1, 1.5, 9007199254740992.0} {
		t.Run("21自动编号"+strconv.FormatBool(id == 42)+"_"+func() string { b, _ := json.Marshal(id); return string(b) }(), func(t *testing.T) {
			failureFixCaptureLogs(t)
			f := newSwitchFixture()
			f.newID = id
			f.expectNoHeaderFirst = true
			svc, c, r := failureFixService(t, f.platform(t))
			svc.now = switchClock
			svc.SetBackgroundTasksDisabled(true)
			_, err := svc.Create(t.Context(), "fixture-user", CreateRequest{Name: "fixture", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, AccessToken: "fixture-new", RechargeRate: 1})
			if id == 42 {
				if err != nil || len(r.saved) != 1 || r.saved[0].Account != "42" || !switchIsConfirmed(r.saved[0].Session) {
					t.Fatalf("自动读取失败：%v", err)
				}
			} else {
				switchError(t, err, "没有合法的用户编号")
				if len(c.sites) != 0 || len(r.saved) != 0 {
					t.Fatal("非法编号仍保存")
				}
			}
		})
	}
	for _, tc := range []struct {
		status         int
		userID, reason string
	}{{401, "", "userKeyDetectFailed"}, {200, "", "userKeyDetectFailed"}, {403, "", "forbidden"}, {429, "", "rateLimited"}, {503, "", "upstreamServerError"}, {401, "42", "accessTokenRejected"}} {
		t.Run("21及22失败"+strconv.Itoa(tc.status)+tc.userID, func(t *testing.T) {
			calls := 0
			p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/user/self" || r.Header.Get("New-Api-User") != tc.userID {
					t.Error("自动读取路径或编号头错误")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"success":false,"message":"无权进行此操作，未提供 New-Api-User"}`)
			})
			_, err := p.LoginWithUserKey("http://fixture.invalid", tc.userID, "fixture-key")
			var e *RequestError
			if !errors.As(err, &e) || e.Reason != "admin.upstream.errors."+tc.reason || e.Stage != "verify" || e.UpstreamMessage != "无权进行此操作，未提供 New-Api-User" || calls != 1 {
				t.Fatalf("Key失败分类或次数错误：%+v calls=%d", e, calls)
			}
		})
	}
	t.Run("23刷新保留编号", func(t *testing.T) {
		f := newSwitchFixture()
		session := Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", RefreshToken: "fixture-refresh", UserID: "7"}
		switchConfirmed(&session, true)
		got, err := f.platform(t).refreshSub2APISessionContext(t.Context(), session)
		if err != nil || got.UserID != "7" || !switchIsConfirmed(&got) {
			t.Fatal("刷新丢失编号或核实标记")
		}
	})
	t.Run("24NewAPI密码标记", func(t *testing.T) {
		f := newSwitchFixture()
		got, err := f.platform(t).Login("http://fixture.invalid", PlatformNewAPI, "owner", "fixture-password")
		if err != nil || !switchIsConfirmed(&got.Session) {
			t.Fatal("NewAPI密码登录未记录核实标记")
		}
	})
}

// PLAN 22a：经 R3 核对原因分类，避免引用消费/存储专用文案。
func TestLoginMethodSwitchIdentityReadCauses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		cause  error
		want   string
	}{
		{"超时", 0, context.DeadlineExceeded, "上游读取超时"},
		{"连不上", 0, errors.New("fixture connection refused"), "无法连接上游"},
		{"HTTPS", 0, errors.New("tls: fixture verification failed"), "上游安全连接验证失败"},
		{"401", 401, nil, "上游未接受原登录资料"},
		{"403", 403, nil, "上游拒绝访问，具体原因未说明"},
		{"404", 404, nil, "上游没有提供读取账号的接口"},
		{"429", 429, nil, "上游要求稍后再读取"},
		{"503", 503, nil, "上游服务异常"},
		{"418", 418, nil, "上游没有正常返回，原因尚未确认"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchFixture()
			f.oldStatus = tc.status
			f.oldError = tc.cause
			f.noOldHint = true
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, false)
			_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
			e := switchError(t, err, tc.want)
			if tc.status > 0 && !strings.Contains(e.UpstreamMessage, "（HTTP "+strconv.Itoa(tc.status)+"）") {
				t.Fatal("HTTP状态未保留")
			}
			if strings.Contains(e.UpstreamMessage, "当天消费记录") || strings.Contains(e.UpstreamMessage, "存储连接") {
				t.Fatal("身份原因混入消费或存储文案")
			}
			if f.logins != 0 {
				t.Fatal("没有可信资料仍登录")
			}
			switchUnchanged(t, c, r, site)
		})
	}
	t.Run("上游原文", func(t *testing.T) {
		f := newSwitchFixture()
		f.oldStatus = 403
		svc, _, _, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, false)
		_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
		switchError(t, err, "原登录已失效（HTTP 403）")
	})
}

func switchClock() time.Time { return time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC) }
func TestLoginMethodSwitchCostPreservation(t *testing.T) {
	f := newSwitchFixture()
	f.failCost = true
	svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModePassword, false)
	observed := switchClock()
	priorCost := 123.456
	site.Metrics.TodayConsume = metric(&priorCost)
	site.Metrics.TodayConsumeStatus = "ok"
	site.Metrics.TodayConsumeDate = businesstime.DateAt(observed)
	site.Metrics.TodayConsumeTimezone = businesstime.Timezone
	site.Metrics.TodayConsumeAt = &observed
	c.add(site)
	r.durable = cloneSite(site)
	_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
	if err != nil {
		t.Fatalf("原账号切换失败：%v", err)
	}
	got, _ := c.Get(t.Context(), site.ID)
	if got.Metrics.TodayConsume.Value == nil || *got.Metrics.TodayConsume.Value != 123.456 || got.Metrics.TodayConsumeStatus != "unreadable" || !reflect.DeepEqual(got.Metrics.TodayConsumeAt, site.Metrics.TodayConsumeAt) || r.unsafeWrites != 0 || r.checks != 1 {
		t.Fatal("切换方式丢失原有同日费用，或绕过条件保存")
	}
}

func TestLoginMethodSwitchMissingSessionAndReturnedPlatform(t *testing.T) {
	t.Run("R2无原登录资料", func(t *testing.T) {
		f := newSwitchFixture()
		svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModePassword, false)
		site.Session = nil
		c.add(site)
		r.durable = cloneSite(site)
		_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModePassword))
		switchError(t, err, "原站点没有保存过登录资料")
		if len(f.paths) != 0 {
			t.Fatal("没有原登录资料仍请求上游")
		}
		switchUnchanged(t, c, r, site)
	})
	t.Run("R4自动识别返回不同平台", func(t *testing.T) {
		f := newSwitchFixture()
		svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModePassword, false)
		dto := switchDTO(site, AuthModePassword)
		dto.Platform = PlatformAuto
		_, err := svc.Update(t.Context(), site.UserID, site.ID, dto)
		e := switchError(t, err, "用新凭据登录后，上游返回的站点地址或平台与原站点不同")
		if strings.Contains(e.UpstreamMessage, switchRefreshWarning) {
			t.Fatal("没有填写RefreshToken却提醒已使用")
		}
		switchUnchanged(t, c, r, site)
	})
}

// Review B1：响应头成功后，正文读取仍可能遇到调用方取消或截止。
type switchInterruptedBody struct {
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
}

func (b *switchInterruptedBody) Read([]byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *switchInterruptedBody) Close() error { b.closed = true; return nil }
func TestLoginMethodSwitchPrincipalBodyInterrupted(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelled), func(t *testing.T) {
			f := newSwitchFixture()
			svc, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, false)
			var ctx context.Context
			var cancel context.CancelFunc
			if cancelled {
				ctx, cancel = context.WithCancel(t.Context())
			} else {
				ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			defer cancel()
			var body *switchInterruptedBody
			f.principalBody = func(req *http.Request) io.ReadCloser {
				body = &switchInterruptedBody{ctx: req.Context()}
				if cancelled {
					body.cancel = cancel
				}
				return body
			}
			status, payload := switchHandlerContext(ctx, svc, site, switchDTO(site, AuthModeToken))
			detail, _ := payload["failure"].(map[string]any)
			if status != 422 || detail["stage"] != "verify" {
				t.Fatalf("正文读取中断应为422/verify：%d %+v", status, payload)
			}
			if cancelled {
				if detail["errorKey"] != ErrorNetwork || detail["upstreamMessage"] != "读取已取消，本次没有保存" {
					t.Fatalf("正文取消被误报：%+v", detail)
				}
			} else if detail["errorKey"] != ErrorNetworkTimeout {
				t.Fatalf("正文超时被误报：%+v", detail)
			}
			if detail["httpStatus"] != float64(200) {
				t.Fatalf("已收到的HTTP200状态应保留：%+v", detail)
			}
			if body == nil || !body.closed {
				t.Fatal("正文资源未关闭")
			}
			switchUnchanged(t, c, r, site)
		})
	}
}
