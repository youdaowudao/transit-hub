package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

type failureFixReferences struct {
	connections, mappings, calls int
	err                          error
	userID, workspaceID, siteID  string
}

func (f *failureFixReferences) CountSiteReferences(_ context.Context, userID, workspaceID, siteID string) (int, int, error) {
	f.calls++
	f.userID, f.workspaceID, f.siteID = userID, workspaceID, siteID
	return f.connections, f.mappings, f.err
}
func failureFixSetReferences(service *Service, checker *failureFixReferences) {
	method := reflect.ValueOf(service).MethodByName("SetSiteReferenceChecker")
	if method.IsValid() {
		method.Call([]reflect.Value{reflect.ValueOf(checker)})
	}
}

type failureFixDeleteRepository struct {
	enabledTestRepository
	deletes int
}

func (r *failureFixDeleteRepository) DeleteSite(context.Context, string, string) error {
	r.deletes++
	return nil
}

type failureFixDeleteCache struct {
	*fakeSiteCache
	costDeletes int
	costSamples []GroupCostSample
}

func (c *failureFixDeleteCache) TryStartGroupCostSampling(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}
func (c *failureFixDeleteCache) GetGroupCostSamplingState(context.Context, string) (GroupCostSamplingState, error) {
	return GroupCostSamplingState{}, nil
}
func (c *failureFixDeleteCache) SetGroupCostSamplingState(context.Context, string, GroupCostSamplingState, time.Duration) error {
	return nil
}
func (c *failureFixDeleteCache) AppendGroupCostSamples(context.Context, string, string, string, []GroupCostSample, int, time.Duration) error {
	return nil
}
func (c *failureFixDeleteCache) ListGroupCostSamples(context.Context, string, string, string) ([]GroupCostSample, error) {
	return c.costSamples, nil
}
func (c *failureFixDeleteCache) DeleteGroupCostSamples(context.Context, string) error {
	c.costDeletes++
	c.costSamples = nil
	return nil
}
func (c *failureFixDeleteCache) ClearGroupCostSamples(context.Context) error { return nil }

func TestFailureFixStageDRemoveChecksReferencesBeforeAnyCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		checker *failureFixReferences
		allowed bool
	}{
		{"connection_reference", &failureFixReferences{connections: 1}, false},
		{"mapping_reference", &failureFixReferences{mappings: 2}, false},
		{"check_error", &failureFixReferences{err: errors.New("fixture repository unavailable")}, false},
		{"checker_not_configured", nil, false},
		{"no_references", &failureFixReferences{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &failureFixDeleteCache{fakeSiteCache: newFakeSiteCache(), costSamples: []GroupCostSample{{}}}
			repository := &failureFixDeleteRepository{}
			service := NewService(nil, repository, nil, cache)
			service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"fixture-user": "fixture-workspace"}})
			t.Cleanup(service.Close)
			site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, AccessToken: "fixture-access"})
			cache.add(site)
			timer := time.NewTimer(time.Hour)
			service.timers[site.ID] = timer
			if tc.checker != nil {
				failureFixSetReferences(service, tc.checker)
			}
			err := service.Remove(context.Background(), "fixture-user", site.ID)
			after, _ := cache.Get(context.Background(), site.ID)
			if tc.allowed {
				if err != nil || after != nil || service.timers[site.ID] != nil || repository.deletes != 1 || cache.costDeletes != 1 {
					t.Error("unreferenced removal did not retain original cleanup")
				}
			} else {
				if err == nil {
					t.Error("unsafe deletion was accepted")
				}
				if !reflect.DeepEqual(site, after) || service.timers[site.ID] != timer || repository.deletes != 0 || cache.costDeletes != 0 || len(cache.costSamples) != 1 {
					t.Error("rejected deletion changed site, timer, cost samples or database")
				}
				if tc.checker != nil && (tc.checker.connections > 0 || tc.checker.mappings > 0) && siteErrorKey(err) != "admin.upstream.errors.siteInUse" {
					t.Error("referenced site failure reason missing")
				}
			}
			if tc.checker != nil && (tc.checker.calls != 1 || tc.checker.userID != "fixture-user" || tc.checker.workspaceID != "fixture-workspace" || tc.checker.siteID != site.ID) {
				t.Error("reference check missing or not scoped to original workspace")
			}
		})
	}
}

func TestFailureFixStageDHandlerReturnsReferenceCountsAndKeepsSite(t *testing.T) {
	service, cache, _ := failureFixService(t, nil)
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, nil)
	cache.add(site)
	failureFixSetReferences(service, &failureFixReferences{connections: 1, mappings: 2})
	request := httptest.NewRequest(http.MethodDelete, "/api/upstream-sites/fixture-site", bytes.NewReader(nil))
	request = request.WithContext(authctx.WithUserID(request.Context(), "fixture-user"))
	response := httptest.NewRecorder()
	(&Handler{service: service, accounts: service.accounts}).remove(response, request)
	if response.Code != http.StatusConflict {
		t.Errorf("referenced removal status=%d, want 409", response.Code)
	}
	var payload struct {
		Message string `json:"message"`
		Failure struct {
			Connections int `json:"connections"`
			Mappings    int `json:"mappings"`
		} `json:"failure"`
	}
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Message != "admin.upstream.errors.siteInUse" || payload.Failure.Connections != 1 || payload.Failure.Mappings != 2 {
		t.Error("reference counts missing in deletion failure")
	}
	after, _ := cache.Get(context.Background(), site.ID)
	if after == nil {
		t.Error("failed removal deleted the site")
	}
}
