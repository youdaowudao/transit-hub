package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
)

func TestGroupRateAccountConsumptionRecordedTotalsRequireUserAndWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authenticated bool
		wantStatus    int
	}{
		{name: "not logged in", wantStatus: http.StatusUnauthorized},
		{name: "no selected workspace", authenticated: true, wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &capturingMetricsDB{queryErr: errors.New("should not read")}
			svc := NewMetricsService(nil, nil, nil, newMetricsRepository(db), &fakeAdminAccounts{current: map[string]string{}})
			mux := http.NewServeMux()
			RegisterRoutes(mux, nil, svc)
			req := httptest.NewRequest(http.MethodGet, "/api/dashboard/upstream-key-recorded-totals", nil)
			if tc.authenticated {
				req = req.WithContext(authctx.WithUserID(req.Context(), "user"))
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if response.Code != tc.wantStatus || db.query != "" {
				t.Fatalf("status=%d query=%q", response.Code, db.query)
			}
		})
	}
}

func TestGroupRateAccountConsumptionRecordedTotalsReadFailureDoesNotExposeDatabaseDetail(t *testing.T) {
	db := &capturingMetricsDB{queryErr: errors.New("private database connection detail")}
	svc := NewMetricsService(nil, nil, nil, newMetricsRepository(db), &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, svc)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/upstream-key-recorded-totals", nil)
	req = req.WithContext(authctx.WithUserID(context.Background(), "user"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), ErrorUpstreamKeyRecordedTotalsUnavailable) || strings.Contains(response.Body.String(), "private database") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if want := []any{"user", "workspace", businesstime.Timezone}; !reflect.DeepEqual(db.queryArgs, want) {
		t.Fatalf("read scope=%#v, want %#v", db.queryArgs, want)
	}
}
