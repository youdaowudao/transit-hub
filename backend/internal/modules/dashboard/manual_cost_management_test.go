package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type deletingCostRepository struct {
	fakeAdditionalCostRepository
	user, workspace, source string
	err                     error
}

func (f *deletingCostRepository) DeleteAdditionalCost(_ context.Context, user, workspace, source string) error {
	f.user, f.workspace, f.source = user, workspace, source
	return f.err
}

func TestManualCostDeletionServiceKeepsCurrentWorkspaceAndRejectsBlank(t *testing.T) {
	repo := &deletingCostRepository{}
	s := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	s.additionalCosts = repo
	if err := s.DeleteAdditionalCost(context.Background(), "user-1", " "); !errors.Is(err, ErrAdditionalCostNotFound) || repo.source != "" {
		t.Fatalf("blank source reached storage: %+v %v", repo, err)
	}
	if err := s.DeleteAdditionalCost(context.Background(), "user-1", "source-1"); err != nil || repo.user != "user-1" || repo.workspace != "workspace-1" || repo.source != "source-1" {
		t.Fatalf("delete did not use current server workspace: %+v %v", repo, err)
	}
}

func TestManualCostDeletionHandlerReturnsStorageOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{{"deleted", nil, http.StatusNoContent}, {"missing", ErrAdditionalCostNotFound, http.StatusNotFound}, {"protected", ErrAdditionalCostProtected, http.StatusConflict}, {"storage", errors.New("isolated storage error"), http.StatusInternalServerError}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &deletingCostRepository{err: tc.err}
			s := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
			s.additionalCosts = repo
			mux := http.NewServeMux()
			RegisterRoutes(mux, nil, s)
			w := manualCostRequest(mux, http.MethodDelete, "source-1", `{"adminAccountId":"other"}`, "user-1")
			if w.Code != tc.status || repo.workspace != "workspace-1" {
				t.Fatalf("DELETE = %d workspace=%s want %d/current", w.Code, repo.workspace, tc.status)
			}
			if tc.status == http.StatusNoContent && w.Body.Len() != 0 {
				t.Fatalf("204 must have no response body: %s", w.Body.String())
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(w.Body.String(), "isolated storage error") {
				t.Fatalf("raw storage failure leaked: %s", w.Body.String())
			}
		})
	}
}

func TestManualCostJSONRetainsAnOriginalZero(t *testing.T) {
	for _, kind := range []string{AdditionalCostFixed, AdditionalCostPromotion, AdditionalCostAdjustment} {
		b, err := json.Marshal(AdditionalCostRecord{Type: kind, OriginalAmount: 0})
		if err != nil || !strings.Contains(string(b), `"originalAmount":0`) {
			t.Fatalf("zero original amount lost for %s: %s %v", kind, b, err)
		}
	}
}
