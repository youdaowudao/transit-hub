package my_sites

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type stageAResourceTransport func(*http.Request) (*http.Response, error)

func (f stageAResourceTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestStageACompensationWithoutSafeDeletePreservesBothResources(t *testing.T) {
	deleteCalls := 0
	transport := stageAResourceTransport(func(req *http.Request) (*http.Response, error) {
		payload := `{"data":{"role":"admin"}}`
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/auth/me":
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/admin/groups":
			payload = `{"data":[{"id":7,"name":"vip","platform":"openai","status":"active"}]}`
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/keys":
			payload = `{"data":{"id":11,"key":"synthetic-test-key"}}`
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/admin/accounts":
			payload = `{"data":{"id":22}}`
		case req.Method == http.MethodDelete:
			deleteCalls++
			payload = `{"success":true}`
		default:
			t.Fatalf("unexpected resource request %s %s", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	})
	session := platformTestSession(upstream.PlatformSub2API, "http://127.0.0.1:8080")
	stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: session, Mappings: []GroupMapping{}}}
	connRepo := &testConnRepo{stateRepo: stateRepo, saveErr: errors.New("database unavailable")}
	lookup := testUpstreamLookup{sites: map[string]*upstream.Site{"site-1": {ID: "site-1", UserID: "user-1", AdminAccountID: "admin-1", Name: "source", BaseURL: session.BaseURL, Platform: upstream.PlatformSub2API, Session: &session, Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "7", Name: "vip", Platform: stringPointer("openai")}}}}}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "admin-1"})
	service.connRepository = connRepo
	addToPricing := false
	_, err := service.RealConnect(context.Background(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"7"}, AddToPricingMapping: &addToPricing})
	if err == nil {
		t.Fatal("persistence failure was hidden")
	}
	if deleteCalls != 0 {
		t.Fatalf("missing safe deletion dependency deleted resources: calls=%d", deleteCalls)
	}
}
