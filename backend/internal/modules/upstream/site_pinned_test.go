package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func pinnedTestPlatform() *PlatformService {
	return failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/user/login" {
			w.Header().Set("Set-Cookie", "fixture-session=fixture; Path=/")
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"id":7}}`)
	})
}

func pinnedRequest(t *testing.T, handler *Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	switch value := body.(type) {
	case string:
		payload = []byte(value)
	default:
		payload, _ = json.Marshal(value)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request = request.WithContext(authctx.WithUserID(request.Context(), "fixture-user"))
	response := httptest.NewRecorder()
	switch method {
	case http.MethodPost:
		handler.create(response, request)
	default:
		handler.update(response, request)
	}
	return response
}

func decodePinnedResponse(t *testing.T, recorder *httptest.ResponseRecorder) Response {
	t.Helper()
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func TestSitePinnedCreateToggleAndEditKeepPinned(t *testing.T) {
	failureFixCaptureLogs(t)
	service, cache, repository := failureFixService(t, pinnedTestPlatform())
	handler := &Handler{service: service, accounts: service.accounts}
	service.SetWorkspaceRefreshConfig("fixture-user", "fixture-workspace", RefreshConfig{Enabled: true, Interval: time.Hour})

	plain := CreateRequest{Name: "验收-未置顶", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, Account: "fixture-account", Password: "fixture-password", RechargeRate: 1}
	recorder := pinnedRequest(t, handler, http.MethodPost, "/api/upstream-sites", plain)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("plain create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if created := decodePinnedResponse(t, recorder); created.Pinned || !strings.Contains(recorder.Body.String(), `"pinned":false`) {
		t.Fatalf("site created without pinned must stay unpinned: %s", recorder.Body.String())
	}

	pinnedCreate := plain
	pinnedCreate.Name = "验收-新增置顶"
	pinnedCreate.Pinned = true
	recorder = pinnedRequest(t, handler, http.MethodPost, "/api/upstream-sites", pinnedCreate)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("pinned create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	created := decodePinnedResponse(t, recorder)
	if !created.Pinned {
		t.Fatal("create with pinned=true did not return a pinned site")
	}
	if saved := repository.saved[len(repository.saved)-1]; saved.ID != created.ID || !saved.Pinned {
		t.Fatalf("create with pinned=true did not persist pinned: %#v", saved)
	}

	timerBefore := service.timers[created.ID]
	if timerBefore == nil {
		t.Fatal("created site was not scheduled for automatic refresh")
	}
	savedBefore := len(repository.saved)

	recorder = pinnedRequest(t, handler, http.MethodPatch, "/api/upstream-sites/"+created.ID+"/pinned", map[string]bool{"pinned": false})
	if recorder.Code != http.StatusOK {
		t.Fatalf("unpin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if decodePinnedResponse(t, recorder).Pinned {
		t.Fatal("unpin response still pinned")
	}
	if len(repository.saved) != savedBefore+1 || repository.saved[len(repository.saved)-1].Pinned {
		t.Fatalf("unpin did not persist exactly one unpinned save: %d -> %d", savedBefore, len(repository.saved))
	}
	if cached := cache.sites[created.ID]; cached == nil || cached.Pinned {
		t.Fatal("unpin did not update the running site data")
	}
	if service.timers[created.ID] != timerBefore {
		t.Fatal("changing pinned re-armed the automatic refresh timer")
	}

	recorder = pinnedRequest(t, handler, http.MethodPatch, "/api/upstream-sites/"+created.ID+"/pinned", map[string]bool{"pinned": true})
	if recorder.Code != http.StatusOK || !decodePinnedResponse(t, recorder).Pinned {
		t.Fatalf("pin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.timers[created.ID] != timerBefore {
		t.Fatal("pinning re-armed the automatic refresh timer")
	}

	update := UpdateRequest{Name: "验收-修改后仍置顶", SiteURL: plain.SiteURL, Platform: plain.Platform, Account: plain.Account, Password: plain.Password, RechargeRate: 2}
	recorder = pinnedRequest(t, handler, http.MethodPut, "/api/upstream-sites/"+created.ID, update)
	if recorder.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if edited := decodePinnedResponse(t, recorder); edited.Name != update.Name || !edited.Pinned {
		t.Fatalf("editing site details dropped pinned: %#v", edited)
	}
}

func TestSitePinnedEndpointRejectsInvalidBodyAndOtherWorkspace(t *testing.T) {
	failureFixCaptureLogs(t)
	service, _, repository := failureFixService(t, pinnedTestPlatform())
	handler := &Handler{service: service, accounts: service.accounts}
	other := newTestSite("other-site", "fixture-user", "other-workspace", 1, &Session{Platform: PlatformSub2API, AccessToken: "token"})
	service.cache.(*fakeSiteCache).add(other)

	recorder := pinnedRequest(t, handler, http.MethodPatch, "/api/upstream-sites/other-site/pinned", `{"pinned":true,"extra":1}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d, want 400", recorder.Code)
	}
	recorder = pinnedRequest(t, handler, http.MethodPatch, "/api/upstream-sites/other-site/pinned", map[string]bool{"pinned": true})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("other workspace status=%d, want 404", recorder.Code)
	}
	if len(repository.saved) != 0 {
		t.Fatal("rejected pin request saved a site")
	}
	if service.cache.(*fakeSiteCache).sites["other-site"].Pinned {
		t.Fatal("rejected pin request changed another workspace site")
	}
}

func TestSitePinnedConditionalSaveGuardMergeAndPayload(t *testing.T) {
	base := *newTestSite("site-1", "user-1", "acc-1", 1, &Session{Platform: PlatformSub2API, AccessToken: "token"})
	pinned := base
	pinned.Pinned = true

	if unchangedSiteConfiguration(pinned, base) {
		t.Fatal("conditional save ignored a pinned change made after the request started")
	}
	if !unchangedSiteConfiguration(pinned, pinned) {
		t.Fatal("same pinned state was treated as a configuration change")
	}
	if next := mergeExistingSiteChanges(base, base, pinned); !next.Pinned {
		t.Fatal("merge dropped the requested pinned change")
	}
	syncCandidate := pinned
	syncCandidate.Status = StatusError
	if next := mergeExistingSiteChanges(pinned, pinned, syncCandidate); !next.Pinned || next.Status != StatusError {
		t.Fatalf("merge of an unrelated change lost pinned or the change: %#v", next)
	}

	if round := fromPayload(toPayload(&pinned)); !round.Pinned {
		t.Fatal("running site data lost pinned")
	}
	var legacy sitePayload
	if err := json.Unmarshal([]byte(`{"id":"site-1","enabled":true}`), &legacy); err != nil || fromPayload(legacy).Pinned {
		t.Fatalf("old running site data without pinned must read as unpinned: err=%v", err)
	}
	encoded, _ := json.Marshal(toResponse(&pinned))
	if !strings.Contains(string(encoded), `"pinned":true`) {
		t.Fatalf("response missing pinned: %s", encoded)
	}
}
