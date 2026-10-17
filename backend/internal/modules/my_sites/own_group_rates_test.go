package my_sites

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

const ownNextRetry = "请稍后点“刷新数据”重试；仍失败时到主站后台确认分组列表能否打开"
const ownNextConnect = "请到“我的站点”重新连接主站，再点“刷新数据”"
const ownNextHandle = "请按上面的原因到主站后台处理，再点“刷新数据”"

type ownFailResolver struct{}

func (ownFailResolver) RequireCurrentID(context.Context, string) (string, error) {
	return "", errors.New("resolver unavailable")
}

type ownFailRepo struct{ testStateRepo }

func (*ownFailRepo) Get(context.Context, string, string) (*State, error) {
	return nil, errors.New("storage unavailable")
}
func ownService(repo StateRepository, client *http.Client) *Service {
	s := NewService(repo, upstream.NewPlatformService(upstream.NewHTTPClient(client)), nil)
	s.SetAdminAccountResolver(testAdminResolver{currentID: "ws-own"})
	return s
}
func ownState(url string) *State {
	session := platformTestSession(upstream.PlatformSub2API, url)
	expired := int64(1)
	session.ExpiresAt = &expired
	session.RefreshToken = "refresh-token"
	return &State{Session: session}
}
func ownError(t *testing.T, err error, status int, detail, next string) {
	t.Helper()
	var e *scopedRequestError
	if !errors.As(err, &e) {
		t.Fatalf("expected scoped error, got %T", err)
	}
	if e.Status != status || e.Message != "读取主站分组失败" || e.Detail != detail || e.Next != next {
		t.Fatalf("unexpected error: status=%d detail=%q next=%q", e.Status, e.Detail, e.Next)
	}
	if e.Status == 401 {
		t.Fatal("upstream failures must not expire Hub authentication")
	}
}
func TestOwnGroupRatesO1ReadOnly(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "标准", "rate_multiplier": 1.2, "status": "active"}, {"id": 2, "name": "停用组", "rate_multiplier": 0.8, "status": "inactive"}, {"id": 3, "name": "无倍率", "status": "active"}}})
	}))
	defer server.Close()
	repo := &testStateRepo{state: ownState(server.URL)}
	s := ownService(repo, server.Client())
	result, err := s.ReadOwnGroupRates(context.Background(), "u", " ws-own ")
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceAdminAccountID != "ws-own" || len(result.Groups) != 3 {
		t.Fatalf("wrong result: %+v", result)
	}
	if result.Groups[2].Multiplier != nil || result.Groups[1].Status != "inactive" || *result.Groups[0].Multiplier != 1.2 {
		t.Fatalf("lost group values: %+v", result.Groups)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(paths, []string{"GET /api/v1/admin/groups"}) || len(repo.saves) != 0 {
		t.Fatalf("unexpected side effects: paths=%v saves=%d", paths, len(repo.saves))
	}
}
func TestOwnGroupRatesO2ThroughO8Preconditions(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	for _, tc := range []struct {
		name, workspace, detail, next string
		status                        int
	}{
		{"O2", "other", "请求中的工作区与当前工作区不同，本次没有读取主站分组", "请重新进入本页", 409},
		{"O3-empty", "", "请求没有带上当前工作区，或工作区编号超过 128 字节", "请刷新页面后重试", 400},
		{"O3-space", "  ", "请求没有带上当前工作区，或工作区编号超过 128 字节", "请刷新页面后重试", 400},
		{"O3-long", strings.Repeat("a", 129), "请求没有带上当前工作区，或工作区编号超过 128 字节", "请刷新页面后重试", 400},
		{"O4", "ws-own", "Hub 无法确认当前工作区", "请选择工作区后点“刷新数据”", 409},
		{"O5", "ws-own", "Hub 没有这个工作区可用的主站登录资料", ownNextConnect, 409},
		{"O6", "ws-own", "当前主站不是 Sub2API，本页只读取 Sub2API 主站的分组倍率", "本页其他内容不受影响", 409},
		{"O7", "ws-own", "Hub 未能读取已保存的主站登录资料，具体存储原因无法安全确认", "请点“刷新数据”重试；仍失败时查看 Hub 后端日志中以 [own-group-rates] 开头的记录", 500},
		{"O8", "ws-own", "Hub 读取主站分组的功能没有准备好", "请重启 Hub 后端后再点“刷新数据”", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &testStateRepo{state: ownState(server.URL)}
			s := ownService(repo, server.Client())
			switch tc.name {
			case "O4":
				s.SetAdminAccountResolver(ownFailResolver{})
			case "O5":
				repo.state = nil
			case "O6":
				repo.state.Session = platformTestSession(upstream.PlatformNewAPI, server.URL)
			case "O7":
				s = ownService(&ownFailRepo{}, server.Client())
			case "O8":
				s.platformService = nil
			}
			_, err := s.ReadOwnGroupRates(context.Background(), "u", tc.workspace)
			ownError(t, err, tc.status, tc.detail, tc.next)
		})
	}
	if requests != 0 {
		t.Fatalf("preconditions sent %d requests", requests)
	}
}
func TestOwnGroupRatesO9RemoteFailures(t *testing.T) {
	for _, tc := range []struct {
		name               string
		status             int
		body, detail, next string
	}{
		{"401", 401, ``, "已保存的主站登录资料没有通过主站验证（主站返回 HTTP 401）", ownNextConnect},
		{"401-expired", 401, `{"code":"API_KEY_EXPIRED"}`, "主站返回 Key 已过期（主站返回 HTTP 401）", ownNextConnect},
		{"403-expired", 403, `{"code":"API_KEY_EXPIRED"}`, "主站返回 Key 已过期（主站返回 HTTP 403）", ownNextHandle},
		{"403", 403, ``, "主站拒绝了这次读取，没有返回具体原因（主站返回 HTTP 403）", ownNextRetry},
		{"404", 404, ``, "主站没有找到分组列表接口（主站返回 HTTP 404）", ownNextRetry},
		{"429", 429, ``, "主站提示请求过于频繁（主站返回 HTTP 429）", ownNextRetry},
		{"503", 503, ``, "主站服务出错，没有返回可显示的具体原因（主站返回 HTTP 503）", ownNextRetry},
		{"business", 200, `{"success":false,"message":"boom access-token"}`, "boom [已遮盖]（主站返回 HTTP 200）", ownNextRetry},
		{"filtered", 500, `{"message":"relation \"users\" does not exist"}`, "主站服务出错，没有返回可显示的具体原因（主站返回 HTTP 500）", ownNextRetry},
		{"duplicate", 200, `{"data":[{"id":1},{"id":1}]}`, "主站返回的分组资料格式不正确或不完整", ownNextRetry},
		{"announcement", 403, `{"code":"ANNOUNCEMENT_ACK_REQUIRED"}`, "主站要求先在网页上确认新公告，确认前无法读取（主站返回 HTTP 403）", ownNextHandle},
		{"balance", 403, `{"code":"INSUFFICIENT_BALANCE"}`, "主站返回余额不足，不允许读取（主站返回 HTTP 403）", ownNextHandle},
		{"quota", 403, `{"code":"API_KEY_QUOTA_EXHAUSTED"}`, "主站返回 Key 额度已用尽（主站返回 HTTP 403）", ownNextHandle},
		{"quota-alias", 403, `{"code":"INSUFFICIENT_QUOTA"}`, "主站返回 Key 额度已用尽（主站返回 HTTP 403）", ownNextHandle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s := ownService(&testStateRepo{state: ownState(server.URL)}, server.Client())
			_, err := s.ReadOwnGroupRates(context.Background(), "u", "ws-own")
			ownError(t, err, 502, tc.detail, tc.next)
		})
	}
}
func TestOwnGroupRatesO10Timeout(t *testing.T) {
	previous := ownGroupRatesTimeout
	ownGroupRatesTimeout = 50 * time.Millisecond
	defer func() { ownGroupRatesTimeout = previous }()
	for _, status := range []int{0, 200, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status != 0 {
					w.WriteHeader(status)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			s := ownService(&testStateRepo{state: ownState(server.URL)}, server.Client())
			_, err := s.ReadOwnGroupRates(context.Background(), "u", "ws-own")
			ownError(t, err, 504, "读取主站分组超时，本次没有取得结果", ownNextRetry)
		})
	}
}
func TestOwnGroupRatesO11O12O13Transport(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	s := ownService(&testStateRepo{state: ownState(server.URL)}, http.DefaultClient)
	_, err := s.ReadOwnGroupRates(context.Background(), "u", "ws-own")
	ownError(t, err, 502, "Hub 暂时无法连接主站", ownNextRetry)
	tls := httptest.NewTLSServer(http.NotFoundHandler())
	defer tls.Close()
	s = ownService(&testStateRepo{state: ownState(tls.URL)}, http.DefaultClient)
	_, err = s.ReadOwnGroupRates(context.Background(), "u", "ws-own")
	ownError(t, err, 502, "主站安全连接未能建立", ownNextRetry)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.ReadOwnGroupRates(ctx, "u", "ws-own")
	ownError(t, err, 502, "这次读取已取消", ownNextRetry)
}
func TestOwnGroupRatesO14Handler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": 3, "name": "无倍率"}}})
	}))
	defer server.Close()
	repo := &testStateRepo{state: ownState(server.URL)}
	s := ownService(repo, server.Client())
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	call := func(auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/my-sites/own-group-rates?workspaceAdminAccountId=ws-own", nil)
		if auth {
			r = r.WithContext(authctx.WithUserID(r.Context(), "u"))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call(false)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "auth.errors.unauthorized") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"multiplier":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
	repo.state = nil
	w = call(true)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	for _, key := range []string{`"message":`, `"detail":`, `"next":`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Fatal("missing", key)
		}
	}
}
