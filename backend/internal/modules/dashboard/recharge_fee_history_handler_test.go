package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

type rechargeFeeHistoryRepository struct {
	itemsByWorkspace map[string][]RechargeFeeRate
	historyCalls     []string
}

func (r *rechargeFeeHistoryRepository) GetRechargeFeeRate(context.Context, string, string, string) (RechargeFeeRate, error) {
	return RechargeFeeRate{}, nil
}

func (r *rechargeFeeHistoryRepository) ListAdditionalCosts(context.Context, string, string, string, string) ([]AdditionalCostRecord, error) {
	return nil, nil
}

func (r *rechargeFeeHistoryRepository) GetAdditionalCost(context.Context, string, string, string) ([]AdditionalCostRecord, error) {
	return nil, nil
}

func (r *rechargeFeeHistoryRepository) SaveRechargeFeeRate(context.Context, RechargeFeeRate) error {
	return nil
}

func (r *rechargeFeeHistoryRepository) InsertAdditionalCosts(context.Context, []AdditionalCostRecord) error {
	return nil
}

func (r *rechargeFeeHistoryRepository) ReplaceAdditionalCost(context.Context, string, string, string, AdditionalCostInput) ([]AdditionalCostRecord, error) {
	return nil, nil
}

func (r *rechargeFeeHistoryRepository) ListRechargeFeeRates(_ context.Context, userID, adminAccountID string) ([]RechargeFeeRate, error) {
	r.historyCalls = append(r.historyCalls, userID+"|"+adminAccountID)
	return append([]RechargeFeeRate(nil), r.itemsByWorkspace[adminAccountID]...), nil
}

func requestRechargeFeeHistory(t *testing.T, mux *http.ServeMux) []RechargeFeeRate {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard/recharge-fee-rates", nil)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-1"))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET recharge fee history status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Items []RechargeFeeRate `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode recharge fee history: %v", err)
	}
	if payload.Items == nil {
		t.Fatalf("GET recharge fee history items = nil, want []")
	}
	return payload.Items
}

func TestRechargeFeeHistoryIsWorkspaceScopedStableAndEmptySafe(t *testing.T) {
	createdEarly := time.Date(2026, 8, 20, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	createdLate := time.Date(2026, 8, 20, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	repository := &rechargeFeeHistoryRepository{itemsByWorkspace: map[string][]RechargeFeeRate{
		"workspace-a": {
			{ID: "rate-old-date", UserID: "user-1", AdminAccountID: "workspace-a", EffectiveDate: "2026-08-01", Rate: 0.016, CreatedAt: createdLate},
			{ID: "rate-a", UserID: "user-1", AdminAccountID: "workspace-a", EffectiveDate: "2026-08-20", Rate: 0.018, CreatedAt: createdEarly},
			{ID: "rate-z", UserID: "user-1", AdminAccountID: "workspace-a", EffectiveDate: "2026-08-20", Rate: 0.020, CreatedAt: createdLate},
			{ID: "rate-y", UserID: "user-1", AdminAccountID: "workspace-a", EffectiveDate: "2026-08-20", Rate: 0.019, CreatedAt: createdLate},
		},
		"workspace-b": {
			{ID: "rate-b-only", UserID: "user-1", AdminAccountID: "workspace-b", EffectiveDate: "2026-08-18", Rate: 0.015, CreatedAt: createdEarly},
		},
	}}
	accounts := &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-a"}}
	metricsService := &MetricsService{additionalCosts: repository, accounts: accounts}
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, metricsService)

	items := requestRechargeFeeHistory(t, mux)
	wantIDs := []string{"rate-z", "rate-y", "rate-a", "rate-old-date"}
	if len(items) != len(wantIDs) {
		t.Fatalf("workspace-a item count = %d, want %d: %#v", len(items), len(wantIDs), items)
	}
	for index, wantID := range wantIDs {
		if items[index].ID != wantID {
			t.Fatalf("workspace-a item[%d].ID = %q, want %q: %#v", index, items[index].ID, wantID, items)
		}
		if items[index].AdminAccountID != "" || items[index].UserID != "" {
			t.Fatalf("workspace ownership leaked in JSON item: %#v", items[index])
		}
	}

	accounts.current["user-1"] = "workspace-b"
	items = requestRechargeFeeHistory(t, mux)
	if len(items) != 1 || items[0].ID != "rate-b-only" {
		t.Fatalf("workspace-b items = %#v, want only rate-b-only", items)
	}

	accounts.current["user-1"] = "workspace-empty"
	items = requestRechargeFeeHistory(t, mux)
	if len(items) != 0 {
		t.Fatalf("empty workspace items = %#v, want []", items)
	}

	wantCalls := []string{"user-1|workspace-a", "user-1|workspace-b", "user-1|workspace-empty"}
	if len(repository.historyCalls) != len(wantCalls) {
		t.Fatalf("history calls = %#v, want %#v", repository.historyCalls, wantCalls)
	}
	for index := range wantCalls {
		if repository.historyCalls[index] != wantCalls[index] {
			t.Fatalf("history calls = %#v, want %#v", repository.historyCalls, wantCalls)
		}
	}
}
