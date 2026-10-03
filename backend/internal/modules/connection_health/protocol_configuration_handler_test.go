package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func protocolConfigurationHTTPFixture(t *testing.T) (*Service, *fakeRepository, *http.ServeMux, *int) {
	t.Helper()
	repo := newFakeRepository()
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "1", Name: "one"}, {ID: "2", Name: "two"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"1": {{ID: "7", Name: "shared"}}, "2": {{ID: "7", Name: "shared"}}}}
	s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	sends := 0
	s.probeRunner.client.Transport = protocolContractTransport(func(*http.Request) (*http.Response, error) { sends++; return nil, errors.New("unexpected model call") })
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	return s, repo, mux, &sends
}

func protocolConfigurationRequest(mux *http.ServeMux, user, group, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/connection-health/admin-groups/"+group+"/test-configuration", strings.NewReader(body))
	if user != "" {
		req = req.WithContext(authctx.WithUserID(req.Context(), user))
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}

func TestProtocolConfigurationHTTPValidationAndZeroActions(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{}`, 400}, {`{"configuration":{}}`, 400}, {`{"configuration":{"protocol":"responses"}}`, 400},
		{`{"configuration":{"protocol":"messages","probeTimeoutSeconds":30}}`, 400},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":4}}`, 400},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":121}}`, 400},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":30.5}}`, 400},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":30,"key":"fixture"}}`, 400},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":30},"credentials":{}}`, 400},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":5}}`, 200},
		{`{"configuration":{"protocol":"responses","probeTimeoutSeconds":120}}`, 200},
		{`{"configuration":{"protocol":"chat_completions","probeTimeoutSeconds":10}}`, 200},
		{`{"configuration":null}`, 200},
	} {
		t.Run(tc.body, func(t *testing.T) {
			_, repo, mux, sends := protocolConfigurationHTTPFixture(t)
			res := protocolConfigurationRequest(mux, "user1", "1", http.MethodPut, tc.body)
			if res.Code != tc.status {
				t.Fatalf("HTTP=%d, want %d: %s", res.Code, tc.status, res.Body.String())
			}
			configs, err := repo.ListGroupTestConfigurations(context.Background(), "user1", "ws1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == 400 && len(configs) != 0 {
				t.Error("rejected request changed persistent configuration")
			}
			if *sends != 0 || len(repo.events) != 0 || len(repo.states) != 0 || len(repo.priorityStates) != 0 || len(repo.targetActionStates) != 0 || len(repo.budgetClaims) != 0 {
				t.Error("configuration save caused model/health/budget/action side effects")
			}
		})
	}
}

func TestProtocolConfigurationHTTPIsolationAndConflictSave(t *testing.T) {
	s, repo, mux, _ := protocolConfigurationHTTPFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if res := protocolConfigurationRequest(mux, "", "1", method, `{"configuration":null}`); res.Code != 401 {
			t.Errorf("unauthenticated %s=%d", method, res.Code)
		}
	}
	put := func(user, group, body string) AdminGroupTestConfiguration {
		t.Helper()
		res := protocolConfigurationRequest(mux, user, group, http.MethodPut, body)
		if res.Code != 200 {
			t.Fatalf("save=%d: %s", res.Code, res.Body.String())
		}
		var result AdminGroupTestConfiguration
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	put("user1", "1", `{"configuration":{"protocol":"responses","probeTimeoutSeconds":30}}`)
	conflict := put("user1", "2", `{"configuration":{"protocol":"responses","probeTimeoutSeconds":20}}`)
	if conflict.ConflictAccountCount != 1 || len(conflict.Accounts) != 1 || conflict.Accounts[0].TestConfiguration.Status != "conflict" {
		t.Errorf("saved conflict omitted from authority response: %+v", conflict)
	}
	put("user2", "1", `{"configuration":{"protocol":"chat_completions","probeTimeoutSeconds":10}}`)
	put("user1", "1", `{"configuration":null}`)
	other, err := repo.ListGroupTestConfigurations(context.Background(), "user2", "ws1")
	if err != nil || len(other) != 1 || other[0].Protocol != TestProtocolChatCompletions {
		t.Fatalf("clear leaked across users: %+v %v", other, err)
	}
	s.accounts = fakeAdminAccountResolver{id: "ws2"}
	res := protocolConfigurationRequest(mux, "user1", "1", http.MethodGet, "")
	var different AdminGroupTestConfiguration
	_ = json.Unmarshal(res.Body.Bytes(), &different)
	if res.Code != 200 || different.Configuration != nil {
		t.Error("same group ID leaked another workspace's setting")
	}
	if bad := protocolConfigurationRequest(mux, "user1", "999", http.MethodPut, `{"configuration":null}`); bad.Code == 200 {
		t.Error("nonexistent group accepted")
	}
}

func TestProtocolConfigurationIncompleteInventoryCannotSave(t *testing.T) {
	s, repo, mux, _ := protocolConfigurationHTTPFixture(t)
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.errByGrp = map[string]error{"2": errors.New("unavailable")}
	s.platformGroups = reader
	res := protocolConfigurationRequest(mux, "user1", "1", http.MethodPut, `{"configuration":{"protocol":"responses","probeTimeoutSeconds":30}}`)
	if res.Code == 200 {
		t.Errorf("partially known member range accepted: %s", res.Body.String())
	}
	configs, err := repo.ListGroupTestConfigurations(context.Background(), "user1", "ws1")
	if err != nil || len(configs) != 0 {
		t.Error("incomplete inventory save was not atomic")
	}
}
