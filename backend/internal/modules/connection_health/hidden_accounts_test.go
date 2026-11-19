package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// hiddenFakeRepository 在通用替身上补屏蔽清单存储，模拟生产仓库只写两列、保留原屏蔽时间。
type hiddenFakeRepository struct {
	*fakeRepository
	mu      sync.Mutex
	hidden  map[string]HiddenAccount
	listErr error
	saves   int
}

func newHiddenFakeRepository() *hiddenFakeRepository {
	return &hiddenFakeRepository{fakeRepository: newFakeRepository(), hidden: map[string]HiddenAccount{}}
}

func (r *hiddenFakeRepository) ListHiddenAccounts(_ context.Context, userID, workspaceID string) ([]HiddenAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	prefix := userID + "|" + workspaceID + "|"
	result := []HiddenAccount{}
	for key, item := range r.hidden {
		if strings.HasPrefix(key, prefix) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TargetID < result[j].TargetID })
	return result, nil
}

func (r *hiddenFakeRepository) SaveAccountHidden(_ context.Context, userID, workspaceID, targetID string, hidden bool, name string) (*time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves++
	key := userID + "|" + workspaceID + "|" + targetID
	if !hidden {
		delete(r.hidden, key)
		return nil, nil
	}
	item, exists := r.hidden[key]
	if !exists {
		item = HiddenAccount{TargetID: targetID, HiddenAt: time.Now().UTC()}
	}
	item.AccountName = name
	r.hidden[key] = item
	at := item.HiddenAt
	return &at, nil
}

func hiddenAccountsFixture() (*Service, *hiddenFakeRepository) {
	repo := newHiddenFakeRepository()
	members := []upstream.AdminGroupAccountInfo{{ID: "img", Name: "画图账号", Models: "image-1"}, {ID: "txt", Name: "文本账号", Models: "gpt-5"}}
	reader := &c2ScheduleInventoryReader{
		fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "绘图组"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": members}},
		allAccounts:             members,
	}
	svc := &Service{
		repo:           repo,
		mySites:        fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		accounts:       fakeAdminAccountResolver{id: "ws1"},
		dispatcher:     noopRemoteActionRunner{},
		probeRunner:    NewRealProbeRunner(),
		platformGroups: reader,
	}
	return svc, repo
}

func hiddenRequest(svc *Service, method, path, user, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	RegisterRoutes(mux, svc)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		request = request.WithContext(authctx.WithUserID(request.Context(), user))
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestHiddenAccountsHTTPRoundTripKeepsHealthStateUntouched(t *testing.T) {
	svc, repo := hiddenAccountsFixture()
	before := len(repo.priorityStates)

	put := hiddenRequest(svc, "PUT", "/api/connection-health/hidden-accounts/sub2api:ws1:img", "user1", `{"accountName":"  画图账号  "}`)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	var putBody map[string]any
	if err := json.Unmarshal(put.Body.Bytes(), &putBody); err != nil {
		t.Fatal(err)
	}
	if putBody["targetId"] != "sub2api:ws1:img" || putBody["hidden"] != true || putBody["hiddenAt"] == nil {
		t.Fatalf("PUT response=%v", putBody)
	}
	firstAt := putBody["hiddenAt"]

	again := hiddenRequest(svc, "PUT", "/api/connection-health/hidden-accounts/sub2api:ws1:img", "user1", `{"accountName":"新名字"}`)
	var againBody map[string]any
	_ = json.Unmarshal(again.Body.Bytes(), &againBody)
	if again.Code != http.StatusOK || againBody["hiddenAt"] != firstAt {
		t.Fatalf("repeat hide must keep the original time: %d %v", again.Code, againBody)
	}

	list := hiddenRequest(svc, "GET", "/api/connection-health/hidden-accounts?workspace=ws1", "user1", "")
	var listBody HiddenAccountList
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil || list.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s err=%v", list.Code, list.Body.String(), err)
	}
	if listBody.WorkspaceID != "ws1" || len(listBody.Items) != 1 || listBody.Items[0].AccountName != "新名字" {
		t.Fatalf("list=%+v", listBody)
	}

	del := hiddenRequest(svc, "DELETE", "/api/connection-health/hidden-accounts/sub2api:ws1:img", "user1", "")
	var delBody map[string]any
	_ = json.Unmarshal(del.Body.Bytes(), &delBody)
	if del.Code != http.StatusOK || !reflect.DeepEqual(delBody, map[string]any{"targetId": "sub2api:ws1:img", "hidden": false}) {
		t.Fatalf("DELETE status=%d body=%v", del.Code, delBody)
	}
	// 未屏蔽的账号也可以取消屏蔽（主站已删账号的清理入口）。
	if again := hiddenRequest(svc, "DELETE", "/api/connection-health/hidden-accounts/sub2api:ws1:gone", "user1", ""); again.Code != http.StatusOK {
		t.Fatalf("DELETE of unknown account status=%d", again.Code)
	}

	if len(repo.priorityStates) != before || len(repo.accountTiers) != 0 || len(repo.groupExclusions) != 0 || len(repo.assignments) != 0 {
		t.Fatal("hiding must not touch Priority, tiers, exclusions or assignments")
	}
	if len(repo.healthConfigGenerations) != 0 {
		t.Fatal("hiding must not bump the health configuration generation")
	}
}

func TestHiddenAccountsRejectForeignTargetsAndWorkspaceMismatch(t *testing.T) {
	svc, repo := hiddenAccountsFixture()
	for _, tc := range []struct {
		name, method, path, user string
		status                   int
	}{
		{"foreign workspace", "PUT", "/api/connection-health/hidden-accounts/sub2api:ws2:img", "user1", 400},
		{"newapi target", "PUT", "/api/connection-health/hidden-accounts/newapi:ws1:img", "user1", 400},
		{"anonymous", "PUT", "/api/connection-health/hidden-accounts/sub2api:ws1:img", "", 401},
		{"list other workspace", "GET", "/api/connection-health/hidden-accounts?workspace=ws2", "user1", 409},
		{"list without workspace", "GET", "/api/connection-health/hidden-accounts", "user1", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := hiddenRequest(svc, tc.method, tc.path, tc.user, `{"accountName":"x"}`)
			if response.Code != tc.status {
				t.Fatalf("status=%d want %d body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.status == 409 {
				var body map[string]string
				_ = json.Unmarshal(response.Body.Bytes(), &body)
				if body["message"] != ErrorWorkspaceChanged || !strings.Contains(body["detail"], "当前工作区已变化") {
					t.Fatalf("409 body=%v", body)
				}
			}
		})
	}
	if repo.saves != 0 {
		t.Fatalf("rejected requests wrote %d times", repo.saves)
	}

	svc.accounts = tierNonSub2APIAccountResolver{fakeAdminAccountResolver{id: "ws1"}}
	if response := hiddenRequest(svc, "GET", "/api/connection-health/hidden-accounts?workspace=ws1", "user1", ""); response.Code != 400 {
		t.Fatalf("non-Sub2API workspace list status=%d", response.Code)
	}
}

func TestHiddenAccountsSkipOnlyGroupModeScheduleMembers(t *testing.T) {
	svc, repo := hiddenAccountsFixture()
	repo.hidden["user1|ws1|sub2api:ws1:img"] = HiddenAccount{TargetID: "sub2api:ws1:img", AccountName: "画图账号", HiddenAt: time.Now()}
	ids := func(inventory questionAnswerScheduleInventory) []string {
		result := []string{}
		for _, item := range inventory.preview {
			result = append(result, item.TargetID)
		}
		return result
	}

	groups, err := svc.resolveQuestionAnswerScheduleTargets(t.Context(), "user1", "ws1", QuestionAnswerScheduleConfig{TargetMode: "groups", SelectedGroupIDs: []string{"g1"}})
	if err != nil || !reflect.DeepEqual(ids(groups), []string{"sub2api:ws1:txt"}) || groups.allMissing {
		t.Fatalf("group mode targets=%v err=%v", ids(groups), err)
	}

	accounts, err := svc.resolveQuestionAnswerScheduleTargets(t.Context(), "user1", "ws1", QuestionAnswerScheduleConfig{TargetMode: "accounts", SelectedAccountTargetIDs: []string{"sub2api:ws1:img", "sub2api:ws1:txt"}})
	if err != nil || !reflect.DeepEqual(ids(accounts), []string{"sub2api:ws1:img", "sub2api:ws1:txt"}) {
		t.Fatalf("explicit accounts must stay as configured: %v err=%v", ids(accounts), err)
	}

	repo.hidden["user1|ws1|sub2api:ws1:txt"] = HiddenAccount{TargetID: "sub2api:ws1:txt", HiddenAt: time.Now()}
	empty, err := svc.resolveQuestionAnswerScheduleTargets(t.Context(), "user1", "ws1", QuestionAnswerScheduleConfig{TargetMode: "groups", SelectedGroupIDs: []string{"g1"}})
	if err != nil || len(empty.preview) != 0 || empty.allMissing || empty.blockedReason != "" {
		t.Fatalf("all hidden members must give an empty, non-blocked scope: %+v err=%v", empty, err)
	}

	repo.listErr = errors.New("storage down")
	if _, err := svc.resolveQuestionAnswerScheduleTargets(t.Context(), "user1", "ws1", QuestionAnswerScheduleConfig{TargetMode: "groups", SelectedGroupIDs: []string{"g1"}}); !errors.Is(err, requestError(ErrorHiddenAccountsRead)) {
		t.Fatalf("group mode read failure err=%v", err)
	}
	if _, err := svc.resolveQuestionAnswerScheduleTargets(t.Context(), "user1", "ws1", QuestionAnswerScheduleConfig{TargetMode: "accounts", SelectedAccountTargetIDs: []string{"sub2api:ws1:txt"}}); err != nil {
		t.Fatalf("account mode must not read the hidden list: %v", err)
	}
}
