package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type groupAPIFixture struct {
	service *Service
	repo    *criticalGroupRepo
	changer *criticalGroupChanger
	account map[string]any
	groups  []map[string]any
	status  int
	calls   int
}

func newGroupAPIFixture(t *testing.T) *groupAPIFixture {
	t.Helper()
	f := &groupAPIFixture{account: map[string]any{"id": 101, "name": "测试账号", "platform": "openai", "type": "apikey", "priority": 100, "concurrency": 10, "credentials": map[string]any{}, "group_ids": []int{10}, "groups": []map[string]any{{"id": 10, "name": "详情旧名"}}}, groups: []map[string]any{{"id": 11, "name": "A新分组", "platform": "openai", "status": "active"}, {"id": 10, "name": "Z旧分组", "platform": "openai"}, {"id": 12, "name": "不属于同平台", "platform": "anthropic"}}}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		if r.Method != http.MethodGet {
			t.Error("new read/sync paths wrote remote")
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/accounts/101":
			if f.status != 0 {
				w.WriteHeader(f.status)
				writeConnectionTestJSON(w, map[string]any{"message": "主站详情不可读"})
				return
			}
			writeConnectionTestJSON(w, map[string]any{"code": 0, "data": f.account})
		case "/api/v1/admin/groups":
			writeConnectionTestJSON(w, map[string]any{"data": f.groups})
		default:
			t.Errorf("unexpected remote %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(remote.Close)
	session := platformTestSession(upstream.PlatformSub2API, remote.URL)
	expired := int64(1)
	session.ExpiresAt = &expired
	session.RefreshToken = "unused-test-refresh"
	conn := RealConnection{ID: "c", UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "s", UpstreamGroupID: "1", UpstreamGroupName: "up", AdminAccountID: "101", AdminAccountName: "测试账号", GroupType: "openai", AdminPlatform: "sub2api", ProvisioningMode: "managed", Status: "active", OwnGroupIDs: []string{"10"}, OwnGroupNames: []string{"Z旧分组"}}
	f.repo = &criticalGroupRepo{testStateRepo: testStateRepo{state: &State{Session: session}}, testConnRepo: testConnRepo{connection: &conn}}
	f.service = NewService(f.repo, upstream.NewPlatformService(upstream.NewHTTPClient(remote.Client())), nil)
	f.service.SetAdminAccountResolver(testAdminResolver{currentID: "w"})
	f.changer = &criticalGroupChanger{result: GroupChangeResult{Outcome: "changed", Groups: []MainSiteGroup{{ID: "11", Name: "A新分组"}}}}
	f.service.SetManagedAccountGroupChanger(f.changer)
	return f
}
func (f *groupAPIFixture) request() MainSiteGroupsRequest {
	expected := expectedConnectionIdentity(*f.repo.connection)
	return MainSiteGroupsRequest{WorkspaceAdminAccountID: "w", ExpectedConnection: &expected, ExpectedMainSiteGroupIDs: []string{"10"}, TargetGroupIDs: []string{"11"}}
}
func TestMainSiteGroupsReadAndSyncUseStoredSession(t *testing.T) {
	f := newGroupAPIFixture(t)
	f.repo.connection.GroupType = " OPENAI "
	read, err := f.service.ReadConnectionMainSiteGroups(t.Context(), "u", "c", "w")
	if err != nil || !read.Consistent || read.CurrentGroups[0].Name != "Z旧分组" || len(read.Groups) != 2 || read.Groups[0].ID != "11" || len(f.repo.saves) != 0 || f.changer.calls != 0 {
		t.Fatalf("invalid read: %+v %v", read, err)
	}
	// The stored expired session is used without auth refresh or state Save.
	f.account["group_ids"] = []int{11}
	f.account["groups"] = []map[string]any{{"id": 11, "name": "详情新名"}}
	req := f.request()
	req.ExpectedMainSiteGroupIDs = []string{"11"}
	result, err := f.service.SyncConnectionHubGroups(t.Context(), "u", "c", req)
	if err != nil || result.Outcome != "completed" || result.WorkspaceAdminAccountID != "w" || f.changer.calls != 0 || len(f.repo.saves) != 0 || !reflect.DeepEqual(f.repo.connection.OwnGroupNames, []string{"A新分组"}) {
		t.Fatalf("sync used wrong path: %+v %v", result, err)
	}
}
func TestMainSiteGroupsReadAndSyncPreflightReasons(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"404", "已不存在"}, {"empty", "没有任何分组"}, {"platform", "平台与 Hub 记录不一致"}, {"expected", "资料已变化"}, {"missing table group", "分组资料不一致"}, {"same", "没有需要保存"}, {"unsupported", "不能在这里"}, {"no session", "没有可用的主站登录资料"}, {"missing connection", "资料已变化"}, {"wrong workspace", "工作区已变化"}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newGroupAPIFixture(t)
			req := f.request()
			switch tc.mode {
			case "404":
				f.status = 404
				f.account["group_ids"] = []int{}
			case "empty":
				f.account["group_ids"] = []int{}
				f.account["groups"] = []map[string]any{}
				f.account["platform"] = "anthropic"
			case "platform":
				f.account["platform"] = "anthropic"
				req.ExpectedMainSiteGroupIDs = []string{"11"}
			case "expected":
				req.ExpectedMainSiteGroupIDs = []string{"11"}
			case "missing table group":
				f.groups = f.groups[:1]
			case "unsupported":
				f.repo.connection.ProvisioningMode = "existing"
				req = f.request()
			case "no session":
				f.repo.state.Session = upstream.Session{}
			case "missing connection":
				f.repo.connection = nil
			case "wrong workspace":
				req.WorkspaceAdminAccountID = "other"
			}
			_, err := f.service.SyncConnectionHubGroups(t.Context(), "u", "c", req)
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.changer.calls != 0 || f.repo.reassigns != 0 || len(f.repo.saves) != 0 {
				t.Fatalf("bad preflight %s: %v", tc.mode, err)
			}
		})
	}
}
func TestMainSiteGroupsChangeValidationAndSpecificTargetError(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"missing target", "修改请求不正确"}, {"duplicate target", "修改请求不正确"}, {"negative target", "修改请求不正确"}, {"wrong expected workspace", "修改请求不正确"}, {"empty expected", "没有任何分组"}, {"stale", "资料已变化"}, {"invalid group", "所选主站分组不能使用"}, {"different platform", "所选主站分组不能使用"}, {"same", "没有需要保存"}, {"not wired", "未启用"}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newGroupAPIFixture(t)
			req := f.request()
			switch tc.mode {
			case "missing target":
				req.TargetGroupIDs = nil
			case "duplicate target":
				req.TargetGroupIDs = []string{"11", "11"}
			case "negative target":
				req.TargetGroupIDs = []string{"-1"}
			case "wrong expected workspace":
				req.ExpectedConnection.WorkspaceAdminAccountID = "other"
			case "empty expected":
				req.ExpectedMainSiteGroupIDs = nil
			case "stale":
				req.ExpectedConnection.OwnGroupIDs = []string{"99"}
			case "invalid group":
				req.TargetGroupIDs = []string{"999"}
			case "different platform":
				req.TargetGroupIDs = []string{"12"}
			case "same":
				req.TargetGroupIDs = []string{"10"}
			case "not wired":
				f.service.SetManagedAccountGroupChanger(nil)
			}
			_, err := f.service.ChangeConnectionMainSiteGroups(t.Context(), "u", "c", req)
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.changer.calls != 0 || f.repo.reassigns != 0 || len(f.repo.saves) != 0 {
				t.Fatalf("invalid validation %s: %v", tc.mode, err)
			}
			var local *scopedRequestError
			if tc.mode == "invalid group" && (!errors.As(err, &local) || !strings.Contains(local.Detail, "999 已不存在")) {
				t.Fatalf("existing group validator cause was lost: %v", err)
			}
			if tc.mode == "different platform" && (!errors.As(err, &local) || !strings.Contains(local.Detail, "平台与所选平台不符")) {
				t.Fatalf("platform validator cause lost: %v", err)
			}
		})
	}
}
func TestMainSiteGroupsSyncStorageAndOutcomeFacts(t *testing.T) {
	for _, mode := range []string{"removed", "changed", "storage", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupAPIFixture(t)
			req := f.request()
			f.account["group_ids"] = []int{11}
			f.account["groups"] = []map[string]any{{"id": 11, "name": "A新分组"}}
			req.ExpectedMainSiteGroupIDs = []string{"11"}
			f.repo.reassignErr = &GroupReassignmentError{Kind: mode, Step: "保存分组", Cause: context.DeadlineExceeded, Uncertain: mode == "uncertain"}
			result, err := f.service.SyncConnectionHubGroups(t.Context(), "u", "c", req)
			if mode == "uncertain" {
				if err != nil || result.Outcome != "hub_unknown" || result.WorkspaceAdminAccountID != "w" {
					t.Fatalf("wrong uncertain sync: %+v %v", result, err)
				}
			} else {
				var local *scopedRequestError
				if !errors.As(err, &local) || !strings.Contains(local.Detail, "主站没有改动") {
					t.Fatalf("wrong rejected sync: %+v %v", result, err)
				}
			}
			if f.changer.calls != 0 || len(f.repo.saves) != 0 {
				t.Fatal("sync wrote main site or whole state")
			}
		})
	}
	for _, mode := range []string{"unchanged no write error", "third group", "partial target name", "unknown save target name"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupAPIFixture(t)
			switch mode {
			case "unchanged no write error":
				f.changer.result.Outcome = "unchanged"
			case "third group":
				f.changer.result.Outcome = "unknown"
			case "partial target name":
				f.repo.reassignErr = &GroupReassignmentError{Kind: "changed"}
			case "unknown save target name":
				f.repo.reassignErr = &GroupReassignmentError{Kind: "storage", Uncertain: true}
			}
			result, err := f.service.ChangeConnectionMainSiteGroups(t.Context(), "u", "c", f.request())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unchanged no write error":
				if !strings.Contains(result.Detail, "读回仍为修改前分组") {
					t.Fatal(result)
				}
			case "third group":
				if !strings.Contains(result.Detail, "既不是修改前") || len(result.MainSiteGroups) != 0 {
					t.Fatal(result)
				}
			default:
				if !strings.Contains(result.Message, "主站已改为 A新分组") {
					t.Fatal(result)
				}
			}
		})
	}
}
func TestMainSiteGroupsHandlerRoutesAuthAndErrorJSON(t *testing.T) {
	for _, path := range []string{"/api/my-sites/real-connections/c/main-site-groups", "/api/my-sites/real-connections/c/main-site-groups/hub-record"} {
		for _, method := range []string{"GET", "POST"} {
			if strings.HasSuffix(path, "hub-record") && method == "GET" {
				continue
			}
			t.Run(method+path, func(t *testing.T) {
				f := newGroupAPIFixture(t)
				mux := http.NewServeMux()
				RegisterRoutes(mux, f.service)
				req := httptest.NewRequest(method, path, nil)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != 401 {
					t.Fatalf("auth status=%d", w.Code)
				}
				if method == "POST" {
					req = httptest.NewRequest(method, path, strings.NewReader(`{}`))
					req = req.WithContext(authctx.WithUserID(req.Context(), "u"))
					w = httptest.NewRecorder()
					mux.ServeHTTP(w, req)
					var body map[string]any
					if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["message"] == nil || body["detail"] == nil || body["next"] == nil {
						t.Fatalf("missing error contract: %s", w.Body.String())
					}
				}
			})
		}
	}
	f := newGroupAPIFixture(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, f.service)
	req := httptest.NewRequest("GET", "/api/my-sites/real-connections/c/main-site-groups?workspaceAdminAccountId=w", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var body struct {
		Groups []map[string]any `json:"groups"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("read failed: %s", w.Body.String())
	}
	for _, g := range body.Groups {
		if _, ok := g["status"].(string); !ok {
			t.Fatal("optional upstream status must remain a required JSON string")
		}
	}
}

type sharedGroupRepo struct{ *criticalGroupRepo }

func (r *sharedGroupRepo) ListRealConnections(ctx context.Context, user, workspace string) ([]RealConnection, error) {
	connections, err := r.criticalGroupRepo.ListRealConnections(ctx, user, workspace)
	if len(connections) > 0 {
		other := connections[0]
		other.ID = "shared"
		connections = append(connections, other)
	}
	return connections, err
}
func TestMainSiteGroupsSharedAccountAndMissingRepositories(t *testing.T) {
	f := newGroupAPIFixture(t)
	f.service.connRepository = &sharedGroupRepo{f.repo}
	_, err := f.service.ReadConnectionMainSiteGroups(t.Context(), "u", "c", "w")
	if err == nil || !strings.Contains(err.Error(), "其他对接") || f.calls != 0 {
		t.Fatal(err)
	}
	f.service.connRepository = nil
	_, err = f.service.ChangeConnectionMainSiteGroups(t.Context(), "u", "c", f.request())
	if err == nil || !strings.Contains(err.Error(), "资料已变化") {
		t.Fatal(err)
	}
}

func TestMainSiteGroupsUnknownPreservesSafeWriteAndReadReasons(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		writeFailure, readFailure bool
	}{
		{name: "write and read failures", writeFailure: true, readFailure: true},
		{name: "read failure only", readFailure: true},
		{name: "write failure and third membership", writeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGroupAPIFixture(t)
			const secret = "unknown-result-test-secret"
			f.repo.state.Session.AccessToken = secret
			f.changer.result = GroupChangeResult{Outcome: "unknown"}
			if tc.writeFailure {
				f.changer.result.WriteError = &upstream.RequestError{MessageKey: upstream.ErrorRequest, UpstreamMessage: "主站表示目标分组不接受该渠道 " + secret}
			}
			if tc.readFailure {
				f.changer.result.ReadError = &upstream.RequestError{MessageKey: upstream.ErrorNetworkTimeout, Reason: upstream.ErrorNetworkTimeout, UpstreamMessage: "账号详情读取超过主站时限 " + secret}
			}
			before := append([]string(nil), f.repo.connection.OwnGroupIDs...)
			result, err := f.service.ChangeConnectionMainSiteGroups(t.Context(), "u", "c", f.request())
			if err != nil || result.Outcome != "main_unknown" || result.WorkspaceAdminAccountID != "w" || len(result.MainSiteGroups) != 0 {
				t.Fatalf("uncertain operation was misreported: %+v %v", result, err)
			}
			if tc.writeFailure {
				if !strings.Contains(result.Detail, "主站写入回复：") || !strings.Contains(result.Detail, "主站表示目标分组不接受该渠道") {
					t.Fatalf("known write reason was discarded: %s", result.Detail)
				}
			} else if strings.Contains(result.Detail, "主站写入回复：") {
				t.Fatalf("invented write failure: %s", result.Detail)
			}
			if tc.readFailure {
				if !strings.Contains(result.Detail, "读回主站分组失败：") || !strings.Contains(result.Detail, "账号详情读取超过主站时限") {
					t.Fatalf("known read reason was discarded: %s", result.Detail)
				}
			} else if !strings.Contains(result.Detail, "既不是修改前的分组，也不是所选目标分组") {
				t.Fatalf("third membership not explained: %s", result.Detail)
			}
			if strings.Contains(result.Detail, secret) || !strings.Contains(result.Detail, "[已遮盖]") {
				t.Fatal("known remote causes were not safely masked")
			}
			if !strings.Contains(result.Detail, "Hub 记录没有改动") || !reflect.DeepEqual(before, f.repo.connection.OwnGroupIDs) || f.repo.reassigns != 0 || len(f.repo.saves) != 0 || f.changer.calls != 1 {
				t.Fatal("unknown main result altered or misreported Hub state")
			}
		})
	}
}
