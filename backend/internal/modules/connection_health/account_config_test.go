package connection_health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

func TestHandlerSilentlyIgnoresRetiredIntelligenceWeightRequest(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil)

	for _, body := range []string{`{"intelligenceWeight":80}`, `{}`, `not-json`} {
		request := httptest.NewRequest(
			http.MethodPut,
			"/api/connection-health/targets/retired-target/intelligence-weight",
			strings.NewReader(body),
		)
		request = request.WithContext(authctx.WithUserID(request.Context(), "user-a"))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("body=%q status=%d want 204 body=%s", body, response.Code, response.Body.String())
		}
		if response.Body.Len() != 0 {
			t.Fatalf("body=%q silent ignore returned body %q", body, response.Body.String())
		}
	}
}

func TestHandlerRetiredIntelligenceWeightRequestStillRequiresAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, &Service{repo: newFakeRepository()})

	request := httptest.NewRequest(
		http.MethodPut,
		"/api/connection-health/targets/sub2api:ws1:1515/intelligence-weight",
		strings.NewReader(`{"intelligenceWeight":80}`),
	)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", response.Code, response.Body.String())
	}
}
