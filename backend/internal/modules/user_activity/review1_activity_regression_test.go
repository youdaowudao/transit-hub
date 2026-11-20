package user_activity

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/modules/vip"
	"transithub/backend/internal/shared/businesstime"
)

type review1ActivityTransport func(*http.Request) (*http.Response, error)

func (f review1ActivityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func review1Response(r *http.Request, code int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
func review1Platform(rt review1ActivityTransport) *upstream.PlatformService {
	return upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: rt}))
}
func review1Session() upstream.Session {
	return upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}
}

type review1StoredSession struct{}

func (review1StoredSession) StoredSession(context.Context, string, string) (upstream.Session, bool, error) {
	return review1Session(), true, nil
}

func TestAbsorbReview1InventoryUsesSupportedBoundedHTTPQuery(t *testing.T) {
	calls := 0
	p := review1Platform(func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/users" || q.Get("sort_by") != "created_at" || q.Get("sort_order") != "asc" || q.Get("include_subscriptions") != "false" || q.Get("page_size") != "100" {
			t.Errorf("inventory issued unexpected actual query: %s", r.URL.RequestURI())
		}
		return review1Response(r, 200, `{"data":{"items":[{"id":1}],"page":1,"page_size":100,"total":1,"pages":1}}`)
	})
	s := NewService(nil, nil, p, false)
	defer s.cancel()
	rows, err := s.fetchInventory(t.Context(), &pacedReader{}, review1Session())
	if err != nil || len(rows) != 1 || calls != 1 {
		t.Fatalf("bounded inventory failed: %v %+v calls=%d", err, rows, calls)
	}
}

func TestAbsorbReview1IncomeAuthenticationRetainsCauseAndStopsWindow(t *testing.T) {
	for _, target := range []string{"balance", "admin_balance", "affiliate"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			p := review1Platform(func(r *http.Request) (*http.Response, error) {
				calls++
				kind := r.URL.Query().Get("type")
				if strings.Contains(r.URL.Path, "affiliates") {
					kind = "affiliate"
				}
				if kind == target {
					return review1Response(r, 401, `{"message":"主站管理员已撤销这次会话；token=fixture"}`)
				}
				return review1Response(r, 200, `{"data":{"items":[],"total":0}}`)
			})
			s := NewService(nil, nil, p, false)
			defer s.cancel()
			_, channels, all, _, err := newRechargeCollector(s, &pacedReader{}, review1Session()).window(t.Context(), "2026-11-18", "2026-11-20")
			var requestErr *upstream.RequestError
			want := map[string]int{"balance": 1, "admin_balance": 2, "affiliate": 3}[target]
			reason := safeReason(err)
			source := map[string]string{"balance": "支付充值与兑换码入账", "admin_balance": "管理员加款与扣款", "affiliate": "返利转入"}[target]
			if all || !errors.As(err, &requestErr) || requestErr.StatusCode != 401 || calls != want || len(channels) != (want-1)*2 || !strings.Contains(reason, "重新连接") || !strings.Contains(reason, "主站管理员已撤销这次会话") || !strings.Contains(reason, source) || strings.Contains(reason, "fixture") || strings.Contains(reason, "token=") {
				t.Fatalf("authentication cause or stop lost: calls=%d channels=%v error=%v", calls, channels, err)
			}
		})
	}
	forbidden := &upstream.RequestError{StatusCode: 403, UpstreamMessage: "request blocked by site policy"}
	if reason := safeReason(fmt.Errorf("wrapped: %w", forbidden)); strings.Contains(reason, "登录资料已失效") || strings.Contains(reason, "余额") || !strings.Contains(reason, forbidden.UpstreamMessage) {
		t.Fatal("403 was guessed to be an authentication or balance failure")
	}
	joined := errors.Join(fmt.Errorf("first source: %w", forbidden), fmt.Errorf("second source: %w", &upstream.RequestError{StatusCode: 401, UpstreamMessage: "这次凭证已被主站管理员撤回"}))
	if reason := safeReason(joined); !authenticationFailure(joined) || !strings.Contains(reason, "重新连接") || !strings.Contains(reason, "这次凭证已被主站管理员撤回") || strings.Contains(reason, forbidden.UpstreamMessage) {
		t.Fatal("earlier non-authentication error hid the later joined authentication cause")
	}
	for _, status := range []int{401, 403} {
		reason := safeReason(&upstream.RequestError{StatusCode: status})
		if !strings.Contains(reason, "无法确认") || strings.Contains(reason, "余额不足") || strings.Contains(reason, "权限不足") || strings.Contains(reason, "已过期") {
			t.Fatalf("missing upstream reason was guessed for HTTP %d", status)
		}
	}
}

func TestAbsorbReview2IncomeSourcesKeepPublicFailureReason(t *testing.T) {
	for _, source := range []struct{ kind, name string }{
		{"balance", "支付充值与兑换码入账"},
		{"admin_balance", "管理员加款与扣款"},
		{"affiliate", "返利转入"},
	} {
		t.Run(source.kind, func(t *testing.T) {
			calls := 0
			p := review1Platform(func(r *http.Request) (*http.Response, error) {
				calls++
				kind := r.URL.Query().Get("type")
				if strings.Contains(r.URL.Path, "affiliates") {
					kind = "affiliate"
				}
				if kind == source.kind {
					return review1Response(r, 403, `{"message":"主站要求核查访问记录后再试；token=fixture"}`)
				}
				return review1Response(r, 200, `{"data":{"items":[],"total":0}}`)
			})
			s := NewService(nil, nil, p, false)
			defer s.cancel()
			_, _, all, _, err := newRechargeCollector(s, &pacedReader{}, review1Session()).window(t.Context(), "2026-11-18", "2026-11-20")
			reason := safeReason(err)
			if all || calls != 3 || !strings.Contains(reason, source.name) || !strings.Contains(reason, "主站要求核查访问记录后再试") || strings.Contains(reason, "fixture") || strings.Contains(reason, "token=") || strings.Contains(reason, "认证未通过") || strings.Contains(reason, "余额不足") || strings.Contains(reason, "权限不足") {
				t.Fatalf("known public source failure was lost or guessed: calls=%d reason=%q", calls, reason)
			}
		})
	}
}

func TestAbsorbReview3EveryFailedIncomeSourceHasWarning(t *testing.T) {
	for _, authTarget := range []string{"", "admin_balance", "affiliate"} {
		t.Run("authentication_at_"+authTarget, func(t *testing.T) {
			type source struct{ kind, name, reason string }
			sources := []source{{"balance", "支付充值与兑换码入账", "支付核对接口正在维护"}, {"admin_balance", "管理员加款与扣款", "管理员入账需要检查访问记录"}, {"affiliate", "返利转入", "返利明细尚未准备完成"}}
			calls := 0
			p := review1Platform(func(r *http.Request) (*http.Response, error) {
				calls++
				kind := r.URL.Query().Get("type")
				if strings.Contains(r.URL.Path, "affiliates") {
					kind = "affiliate"
				}
				for _, src := range sources {
					if src.kind == kind {
						status := 503
						if kind == "admin_balance" {
							status = 403
						}
						if kind == authTarget {
							status = 401
						}
						return review1Response(r, status, fmt.Sprintf(`{"message":%q}`, src.reason+"；token=fixture"))
					}
				}
				t.Fatal("unexpected income request")
				return nil, nil
			})
			s := NewService(nil, nil, p, false)
			defer s.cancel()
			_, _, all, warning, err := newRechargeCollector(s, &pacedReader{}, review1Session()).window(t.Context(), "2026-11-18", "2026-11-20")
			wantCalls := 3
			if authTarget == "admin_balance" {
				wantCalls = 2
			}
			if all || err == nil || calls != wantCalls {
				t.Fatalf("income failures or authentication stop changed: calls=%d err=%v", calls, err)
			}
			for _, src := range sources[:wantCalls] {
				if !strings.Contains(warning, src.name) || !strings.Contains(warning, src.reason) {
					t.Errorf("warning omitted failed source %q or its known reason", src.kind)
				}
			}
			if strings.Contains(warning, "fixture") || strings.Contains(warning, "token=") || strings.Contains(warning, "余额不足") || strings.Contains(warning, "权限不足") {
				t.Error("warning leaked a credential or guessed a reason")
			}
			if authTarget != "" {
				main := safeReason(err)
				if !strings.Contains(main, "HTTP 401") || !strings.Contains(main, "重新连接") || strings.Contains(main, sources[0].reason) {
					t.Error("earlier non-authentication cause replaced primary authentication error")
				}
			}
		})
	}
}

func TestAbsorbReview3KnownTransportReasonWithoutBodyStaysSpecific(t *testing.T) {
	for _, tc := range []struct {
		name, key, text string
		err             error
	}{
		{"timeout", upstream.ErrorNetworkTimeout, "连接上游超时。", context.DeadlineExceeded},
		{"tls", upstream.ErrorTLSFailed, "与上游建立 HTTPS 连接失败。", x509.UnknownAuthorityError{}},
		{"unreachable", upstream.ErrorNetworkUnreachable, "无法连接上游地址，请检查域名和端口。", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("isolated transport unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := review1Platform(func(*http.Request) (*http.Response, error) { return nil, tc.err })
			s := NewService(nil, nil, p, false)
			defer s.cancel()
			_, err := s.fetchInventory(t.Context(), &pacedReader{}, review1Session())
			var requestErr *upstream.RequestError
			if !errors.As(err, &requestErr) || requestErr.Reason != tc.key || requestErr.UpstreamMessage != "" {
				t.Fatal("fixture did not reach existing transport classification")
			}
			if reason := safeReason(err); !strings.Contains(reason, tc.text) || strings.Contains(reason, "无法确认") {
				t.Error("known transport reason was replaced by a generic missing-body message")
			}
			_, _, _, warning, _ := newRechargeCollector(s, &pacedReader{}, review1Session()).window(t.Context(), "2026-11-18", "2026-11-20")
			for _, source := range []string{"支付充值与兑换码入账", "管理员加款与扣款", "返利转入"} {
				if !strings.Contains(warning, source) {
					t.Errorf("network warning omitted source %q", source)
				}
			}
			if !strings.Contains(warning, tc.text) || strings.Contains(warning, "无法确认") {
				t.Error("source wrapper replaced the known transport reason")
			}
			if reason := safeReason(&upstream.RequestError{MessageKey: tc.key}); !strings.Contains(reason, tc.text) {
				t.Error("existing reason key fallback was lost")
			}
		})
	}
}

type review1PagePlatform struct {
	Platform
	p       *pacedReader
	calls   int
	invalid string
}

type review4FailedBody struct {
	data []byte
	err  error
}

func (b *review4FailedBody) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	return 0, b.err
}
func (*review4FailedBody) Close() error { return nil }

func TestAbsorbReview4SharedHTTPCategoriesKeepKnownReason(t *testing.T) {
	for _, tc := range []struct {
		name, body, key, reason, text string
		status                        int
		readErr                       error
		invalidURL, unknownCause      bool
	}{
		{name: "non JSON 200", status: 200, body: "<html>fixture-private-body</html>", key: upstream.ErrorInvalidResponse, text: "内容无法解析或读取不完整"},
		{name: "truncated JSON 200", status: 200, body: `{"data":`, key: upstream.ErrorInvalidResponse, text: "内容无法解析或读取不完整"},
		{name: "read failure 200", status: 200, readErr: errors.New("isolated body failure token=fixture"), key: upstream.ErrorInvalidResponse, text: "内容无法解析或读取不完整"},
		{name: "early EOF 200", status: 200, body: `{"data":`, readErr: io.ErrUnexpectedEOF, key: upstream.ErrorInvalidResponse, text: "内容无法解析或读取不完整"},
		{name: "invalid URL", invalidURL: true, key: upstream.ErrorInvalidURL, text: "网站地址无效"},
		{name: "safe JSON description wins", status: 403, body: `{"message":"主站正在核对这笔资料，请稍后重试；token=fixture"}`, key: upstream.ErrorRequest, reason: upstream.ErrorForbidden, text: "主站正在核对这笔资料，请稍后重试"},
		{name: "forbidden no detail", status: 403, body: `{}`, key: upstream.ErrorRequest, reason: upstream.ErrorForbidden, text: "主站拒绝访问", unknownCause: true},
		{name: "not found", status: 404, body: `{}`, key: upstream.ErrorRequest, reason: upstream.ErrorNotFound, text: "未找到请求的资料或接口"},
		{name: "rate limit", status: 429, body: `{}`, key: upstream.ErrorRequest, reason: upstream.ErrorRateLimited, text: "上游请求过于频繁"},
		{name: "server error", status: 503, body: `{}`, key: upstream.ErrorRequest, reason: upstream.ErrorUpstreamServer, text: "上游服务异常"},
		{name: "unknown upstream code", status: 400, body: `{"code":"UNRECOGNIZED_REFUSAL"}`, key: upstream.ErrorRequest, text: "请求未成功", unknownCause: true},
		{name: "auth without detail", status: 401, body: `{}`, key: upstream.ErrorAuth, text: "认证未通过", unknownCause: true},
		{name: "explicit announcement", status: 403, body: `{"code":"ANNOUNCEMENT_ACK_REQUIRED"}`, key: upstream.ErrorRequest, reason: upstream.ErrorAnnouncementAckRequired, text: "确认新公告"},
		{name: "explicit balance", status: 403, body: `{"code":"INSUFFICIENT_BALANCE"}`, key: upstream.ErrorRequest, reason: upstream.ErrorUpstreamInsufficientBalance, text: "上游账户余额不足"},
		{name: "explicit quota", status: 403, body: `{"code":"API_KEY_QUOTA_EXHAUSTED"}`, key: upstream.ErrorRequest, reason: upstream.ErrorUpstreamKeyQuotaExhausted, text: "额度已用尽"},
		{name: "explicit quota alias", status: 200, body: `{"success":false,"code":"INSUFFICIENT_QUOTA"}`, key: upstream.ErrorRequest, reason: upstream.ErrorUpstreamKeyQuotaExhausted, text: "额度已用尽"},
		{name: "explicit expiry", status: 403, body: `{"code":"API_KEY_EXPIRED"}`, key: upstream.ErrorRequest, reason: upstream.ErrorUpstreamKeyExpired, text: "已过期"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := review1Platform(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.readErr != nil {
					return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: &review4FailedBody{data: []byte(tc.body), err: tc.readErr}, Request: r}, nil
				}
				return review1Response(r, tc.status, tc.body)
			})
			s := NewService(nil, nil, p, false)
			defer s.cancel()
			session := review1Session()
			if tc.invalidURL {
				session.BaseURL = "://invalid"
			}
			_, err := s.fetchInventory(t.Context(), &pacedReader{}, session)
			var detail *upstream.RequestError
			if !errors.As(err, &detail) || detail.MessageKey != tc.key || detail.Reason != tc.reason {
				t.Fatalf("fixture did not reach intended existing HTTP category: %+v", detail)
			}
			if tc.invalidURL && calls != 0 {
				t.Fatal("invalid URL unexpectedly made HTTP request")
			}
			reason := failure("用户清单", "完整库存", err)
			if !strings.Contains(reason, "用户清单") || !strings.Contains(reason, tc.text) || !strings.Contains(reason, "请") {
				t.Errorf("known reason or check instruction missing: %q", reason)
			}
			if tc.unknownCause && (!strings.Contains(reason, "无法确认") || strings.Contains(reason, "余额不足") || strings.Contains(reason, "权限不足") || strings.Contains(reason, "已过期")) {
				t.Error("missing underlying cause was guessed")
			}
			if strings.Contains(reason, "fixture") || strings.Contains(reason, "token=") {
				t.Error("failure description exposed response/credential material")
			}
			_, _, _, warning, _ := newRechargeCollector(s, &pacedReader{}, session).window(t.Context(), "2026-11-18", "2026-11-20")
			if !strings.Contains(warning, "支付充值与兑换码入账") || !strings.Contains(warning, tc.text) {
				t.Errorf("income source or known reason lost: %q", warning)
			}
			if strings.Contains(warning, "fixture") || strings.Contains(warning, "token=") {
				t.Error("income warning exposed response/credential material")
			}
		})
	}
	for _, detail := range []*upstream.RequestError{
		{MessageKey: "unrecognized.failure", Reason: "unrecognized.reason"},
		{MessageKey: upstream.ErrorAuth},
		{MessageKey: upstream.ErrorNetwork},
	} {
		reason := safeReason(detail)
		if !strings.Contains(reason, "无法确认") || strings.Contains(reason, "已过期") || strings.Contains(reason, "余额不足") || strings.Contains(reason, "权限不足") {
			t.Errorf("unconfirmed error was guessed: %q", reason)
		}
		if detail.MessageKey == upstream.ErrorNetwork && !strings.Contains(reason, "网络请求未完成") {
			t.Error("known network category was lost when its underlying cause was unconfirmed")
		}
	}
}

func (f *review1PagePlatform) FetchSub2APIUsedRedeemRecordsContext(_ context.Context, _ upstream.Session, kind string, page, _ int) (upstream.Sub2APIRedeemRecordsPage, error) {
	f.calls++
	f.p.last = time.Time{}
	today, _, _ := businesstime.Bounds("2026-11-20")
	old := today.AddDate(0, 0, -10)
	newer := today.Add(time.Hour)
	total := 4
	if f.invalid == "count" {
		total = 2
	}
	if page == 1 {
		return upstream.Sub2APIRedeemRecordsPage{Total: total, TotalKnown: true, Items: []upstream.Sub2APIRedeemRecord{{ID: "1", UsedBy: "1", Type: kind, UsedAt: &newer, Value: 10}}}, nil
	}
	// The first row moves the stream before today's start, then the second invalidates this page.
	rows := []upstream.Sub2APIRedeemRecord{{ID: "2", UsedBy: "1", Type: kind, UsedAt: &old, Value: 20}, {ID: "3", UsedBy: "1", Type: kind, UsedAt: &old, Value: 30}}
	switch f.invalid {
	case "order":
		rows[1].UsedAt = &newer
	case "missing id":
		rows[1].ID = ""
	case "duplicate in page":
		rows[1].ID = "2"
	case "duplicate prior page":
		rows[1].ID = "1"
	}
	return upstream.Sub2APIRedeemRecordsPage{Total: total, TotalKnown: true, Items: rows}, nil
}
func TestAbsorbReview1InvalidRedeemPageCannotCompleteLaterWindow(t *testing.T) {
	for _, invalid := range []string{"order", "missing id", "duplicate in page", "duplicate prior page", "count"} {
		t.Run(invalid, func(t *testing.T) {
			p := &pacedReader{}
			f := &review1PagePlatform{p: p, invalid: invalid}
			s := NewService(nil, nil, f, false)
			defer s.cancel()
			c := newRechargeCollector(s, p, upstream.Session{})
			if _, err := c.redeems(t.Context(), "balance", "2026-11-18", "2026-11-20"); err == nil {
				t.Fatal("invalid page was accepted")
			}
			stream := c.streams["balance"]
			if len(stream.rows) != 1 || len(stream.seen) != 1 || stream.page != 2 || stream.requests != 2 || stream.last == nil || dayAt(*stream.last) != "2026-11-20" {
				t.Errorf("invalid page changed previously verified stream: %+v", stream)
			}
			if _, err := c.redeems(t.Context(), "balance", "2026-11-20", "2026-11-21"); err == nil {
				t.Fatal("invalid page's old timestamp made today's window appear complete")
			}
			if f.calls != 3 || stream.requests != 3 {
				t.Fatal("failed page did not consume shared request budget or was skipped")
			}
		})
	}
}

// The whole collector publishes real local rows; only upstream HTTP is replaced in memory.
func TestAbsorbReview1CollectCoreSuccessAndAuthenticationBoundary(t *testing.T) {
	for _, scenario := range []string{"affiliate404", "all_income_failures", "closed_income_failure", "today_income_failure", "older_income_failure", "older_usage_failure", "today_usage_failure", "recent_minus2_failure", "recent_minus1_failure", "auth_inventory", "auth_trend", "auth_stats", "auth_balance", "auth_admin_balance", "auth_affiliate", "auth_admin_after_balance_failure", "auth_affiliate_after_balance_failure"} {
		t.Run(scenario, func(t *testing.T) {
			pool, scope := absorbStore(t)
			ctx := t.Context()
			started := time.Now().Truncate(time.Microsecond)
			today := dayAt(started)
			oldSyncedAt := started.Add(-time.Hour)
			previous := SyncState{Status: "success", LastSyncedAt: &oldSyncedAt, UsageFrom: shift(today, -14), UsageTo: shift(today, -1), RechargeFrom: shift(today, -10), RechargeTo: shift(today, -1), Warnings: []string{}}
			st := previous
			st.Status, st.StartedAt = "running", &started
			if err := publish(ctx, pool, scope, func(tx pgx.Tx) error { return writeSync(ctx, tx, scope, st) }); err != nil {
				t.Fatal(err)
			}
			for n := 1; n <= 14; n++ {
				if _, err := pool.Exec(ctx, `INSERT INTO user_activity_daily VALUES($1,$2,'1',$3::date,1,0,10)`, scope.UserID, scope.AdminAccountID, shift(today, -n)); err != nil {
					t.Fatal(err)
				}
			}
			calls, authAt := 0, 0
			p := review1Platform(func(r *http.Request) (*http.Response, error) {
				calls++
				path, q := r.URL.Path, r.URL.Query()
				step := ""
				switch path {
				case "/api/v1/admin/users":
					step = "inventory"
				case "/api/v1/admin/dashboard/users-trend":
					step = "trend"
				case "/api/v1/admin/usage/stats":
					step = "stats"
				case "/api/v1/admin/redeem-codes":
					step = q.Get("type")
				case "/api/v1/admin/affiliates/transfers":
					step = "affiliate"
				default:
					t.Fatalf("unexpected upstream request: %s", path)
				}
				authTarget := strings.TrimPrefix(scenario, "auth_")
				if scenario == "auth_admin_after_balance_failure" {
					authTarget = "admin_balance"
				} else if scenario == "auth_affiliate_after_balance_failure" {
					authTarget = "affiliate"
				}
				if strings.HasSuffix(scenario, "_after_balance_failure") && step == "balance" {
					return review1Response(r, 503, `{"message":"普通入账读取被主站暂时拒绝；token=fixture"}`)
				}
				if scenario == "all_income_failures" {
					for _, income := range []struct{ step, reason string }{{"balance", "支付核对接口正在维护"}, {"admin_balance", "管理员入账需要检查访问记录"}, {"affiliate", "返利明细尚未准备完成"}} {
						if step == income.step {
							return review1Response(r, 503, fmt.Sprintf(`{"message":%q}`, income.reason+"；token=fixture"))
						}
					}
				}
				if strings.HasPrefix(scenario, "auth_") && authTarget == step {
					if authAt == 0 {
						authAt = calls
					}
					return review1Response(r, 401, `{"message":"主站管理员已撤销这次会话；token=fixture"}`)
				}
				switch step {
				case "inventory":
					return review1Response(r, 200, `{"data":{"items":[{"id":1,"role":"user","status":"active","balance":5}],"page":1,"page_size":100,"pages":1,"total":1}}`)
				case "trend":
					day := q.Get("start_date")
					if (scenario == "older_usage_failure" && day < shift(today, -14)) || (scenario == "today_usage_failure" && day == today) || (scenario == "recent_minus2_failure" && day == shift(today, -2)) || (scenario == "recent_minus1_failure" && day == shift(today, -1)) {
						return review1Response(r, 503, `{"message":"fixture usage unavailable"}`)
					}
					// Stop optional historical extension after the first additional day.
					if day < shift(today, -14) {
						return review1Response(r, 200, `{"data":{"trend":[]}}`)
					}
					return review1Response(r, 200, fmt.Sprintf(`{"data":{"trend":[{"user_id":1,"date":%q,"requests":1,"tokens":0,"actual_cost":10}]}}`, day))
				case "stats":
					if q.Get("start_date") < shift(today, -14) {
						return review1Response(r, 200, `{"data":{"total_requests":0,"total_actual_cost":0}}`)
					}
					return review1Response(r, 200, `{"data":{"total_requests":1,"total_actual_cost":10}}`)
				case "affiliate":
					if scenario == "affiliate404" {
						return review1Response(r, 404, `{"message":"legacy route unavailable"}`)
					}
					from := q.Get("start_at")
					if (scenario == "closed_income_failure" && from == shift(today, -2)) || (scenario == "today_income_failure" && from == today) || (scenario == "older_income_failure" && from < shift(today, -2)) {
						return review1Response(r, 503, `{"message":"fixture income unavailable"}`)
					}
				}
				return review1Response(r, 200, `{"data":{"items":[],"total":0}}`)
			})
			s := NewService(NewRepository(pool), review1StoredSession{}, p, false)
			defer s.cancel()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s.collect(ctx, conn, scope, previous, st)
			conn.Release()
			got, err := s.repo.Sync(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			fatal := strings.HasPrefix(scenario, "auth_") || strings.HasPrefix(scenario, "recent_")
			if (got.Status == "success") == fatal || got.LastSyncedAt == nil || (fatal && !got.LastSyncedAt.Equal(oldSyncedAt)) || (!fatal && got.LastSyncedAt.Before(started)) {
				t.Errorf("core sync result wrong: %+v", got)
			}
			if len(got.Warnings) == 0 {
				t.Error("optional/fatal problem lost its warning")
			}
			if scenario == "all_income_failures" || strings.HasSuffix(scenario, "_after_balance_failure") {
				warnings := strings.Join(got.Warnings, "\n")
				if scenario == "all_income_failures" {
					for _, known := range []string{"支付充值与兑换码入账", "支付核对接口正在维护", "管理员加款与扣款", "管理员入账需要检查访问记录", "返利转入", "返利明细尚未准备完成"} {
						if !strings.Contains(warnings, known) {
							t.Errorf("persisted warnings omitted %q", known)
						}
					}
				} else if !strings.Contains(warnings, "支付充值与兑换码入账") || !strings.Contains(warnings, "普通入账读取被主站暂时拒绝") {
					t.Error("preceding non-authentication cause was not persisted as warning")
				}
				if strings.Contains(warnings, "fixture") || strings.Contains(warnings, "token=") {
					t.Error("persisted warnings leaked a credential")
				}
				view, err := s.Summary(ctx, scope, 14)
				if err != nil || strings.Join(view.Warnings, "\n") != warnings {
					t.Error("summary lost persisted per-source warnings")
				}
			}
			if authAt > 0 && (calls != authAt || !strings.Contains(got.Error, "重新连接") || strings.Contains(got.Error, "已被取消")) {
				t.Errorf("authentication did not stop at original cause: calls=%d authAt=%d state=%+v", calls, authAt, got)
			}
			if authAt > 0 {
				if !strings.Contains(got.Error, "主站管理员已撤销这次会话") || strings.Contains(got.Error, "fixture") || strings.Contains(got.Error, "token=") || strings.Contains(got.Error, "普通入账读取被主站暂时拒绝") {
					t.Error("terminal authentication reason lost its safe detail or selected the preceding non-authentication error")
				}
				source := map[string]string{"auth_balance": "支付充值与兑换码入账", "auth_admin_balance": "管理员加款与扣款", "auth_affiliate": "返利转入", "auth_admin_after_balance_failure": "管理员加款与扣款", "auth_affiliate_after_balance_failure": "返利转入"}[scenario]
				if source != "" && !strings.Contains(got.Error, source) {
					t.Errorf("terminal failure omitted actual Chinese income source %q", source)
				}
			}
			if scenario == "auth_inventory" {
				var n int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_activity_profiles`).Scan(&n); err != nil || n != 0 {
					t.Fatal("failed inventory was published")
				}
			}
			if scenario == "auth_balance" || scenario == "auth_admin_balance" || scenario == "auth_affiliate" {
				var n int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_activity_daily WHERE day=$1::date`, shift(today, -1)).Scan(&n); err != nil || n != 1 {
					t.Fatal("later authentication erased committed usage")
				}
			}
			if scenario == "affiliate404" && !strings.Contains(strings.Join(got.Warnings, "\n"), "legacy route unavailable") {
				t.Error("affiliate 404 lost its safe concrete cause")
			}
			if scenario == "affiliate404" && (got.RechargeFrom != previous.RechargeFrom || got.RechargeTo != previous.RechargeTo) {
				t.Error("partial funding claimed complete expanded coverage")
			}
			vr := vip.NewRepository(pool)
			if err := vr.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings VALUES($1,$2,$3::jsonb,now())`, scope.UserID, scope.AdminAccountID, `{"telegram":[{"id":"fixture-bot","name":"fixture","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}`); err != nil {
				t.Fatal(err)
			}
			notifier := settings.NewService(&http.Client{Transport: review1ActivityTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("VIP consumer test must not send")
				return nil, nil
			})}, settings.NewRepository(pool))
			vs := vip.NewService(vr, notifier, false)
			defer vs.Shutdown(context.Background())
			cfg := vip.DefaultSettings()
			cfg.AlertsEnabled = true
			cfg.BotIDs = []string{"fixture-bot"}
			if err := vs.SaveSettings(ctx, scope, cfg); err != nil {
				t.Fatal(err)
			}
			summary, err := vs.Summary(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			if (summary.PauseReason == "") == fatal {
				t.Errorf("VIP consumer coherence wrong: pause=%q state=%+v", summary.PauseReason, got)
			}
		})
	}
}
