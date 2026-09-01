package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

func TestAdditionalCostHandlersReadAndUpdateCurrentSource(t *testing.T) {
	repository := &fakeAdditionalCostRepository{items: []AdditionalCostRecord{{ID: "source-1-0", SourceID: "source-1", Type: AdditionalCostFixed, BusinessDate: "2026-08-20", Amount: 50}}}
	metricsService := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	metricsService.additionalCosts = repository
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, metricsService)

	getRequest := httptest.NewRequest(http.MethodGet, "/api/dashboard/additional-costs/source-1", nil)
	getRequest = getRequest.WithContext(authctx.WithUserID(context.Background(), "user-1"))
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), "source-1-0") {
		t.Fatalf("GET source response = %d %s", getResponse.Code, getResponse.Body.String())
	}

	updateRequest := httptest.NewRequest(http.MethodPut, "/api/dashboard/additional-costs/source-1", strings.NewReader(`{"type":"fixed","name":"服务器","businessDate":"2026-08-20","amount":100,"days":2}`))
	updateRequest = updateRequest.WithContext(authctx.WithUserID(context.Background(), "user-1"))
	updateResponse := httptest.NewRecorder()
	mux.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("PUT source response = %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	if repository.updatedUser != "user-1" || repository.updatedAdmin != "workspace-1" || repository.updatedSource != "source-1" || repository.updatedInput.Days != 2 {
		t.Fatalf("PUT call = %#v, want current workspace/source and days=2", repository)
	}

	repository.updateErr = ErrAdditionalCostProtected
	protectedRequest := httptest.NewRequest(http.MethodPut, "/api/dashboard/additional-costs/source-1", strings.NewReader(`{"type":"fixed","name":"服务器","businessDate":"2026-08-20","amount":100,"days":2}`))
	protectedRequest = protectedRequest.WithContext(authctx.WithUserID(context.Background(), "user-1"))
	protectedResponse := httptest.NewRecorder()
	mux.ServeHTTP(protectedResponse, protectedRequest)
	if protectedResponse.Code != http.StatusConflict {
		t.Fatalf("protected PUT response = %d %s, want %d", protectedResponse.Code, protectedResponse.Body.String(), http.StatusConflict)
	}
}

func TestAdditionalCostHandlersRejectUnauthenticatedAndMissingSource(t *testing.T) {
	repository := &fakeAdditionalCostRepository{getErr: ErrAdditionalCostNotFound}
	metricsService := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	metricsService.additionalCosts = repository
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, metricsService)

	unauthenticated := httptest.NewRecorder()
	mux.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/dashboard/additional-costs/missing", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET response = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	missing := httptest.NewRequest(http.MethodGet, "/api/dashboard/additional-costs/missing", nil)
	missing = missing.WithContext(authctx.WithUserID(context.Background(), "user-1"))
	missingResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing source GET response = %d %s, want %d", missingResponse.Code, missingResponse.Body.String(), http.StatusNotFound)
	}
}

func TestAdditionalCostProtectedErrorMapsToConflict(t *testing.T) {
	response := httptest.NewRecorder()
	writeError(response, ErrAdditionalCostProtected)
	if response.Code != http.StatusConflict {
		t.Fatalf("protected error status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestAdditionalCostHandlersProtectSystemSourceRead(t *testing.T) {
	repository := &fakeAdditionalCostRepository{items: []AdditionalCostRecord{{ID: "asset-1", Type: AdditionalCostAccountPurchase, AccountAssetID: "asset-1"}}}
	metricsService := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	metricsService.additionalCosts = repository
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, metricsService)

	request := httptest.NewRequest(http.MethodGet, "/api/dashboard/additional-costs/asset-1", nil)
	request = request.WithContext(authctx.WithUserID(context.Background(), "user-1"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("protected GET response = %d %s, want %d", response.Code, response.Body.String(), http.StatusConflict)
	}
}
