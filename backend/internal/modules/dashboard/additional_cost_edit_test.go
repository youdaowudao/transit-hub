package dashboard

import (
	"context"
	"errors"
	"testing"
)

func TestMetricsServiceUpdateAdditionalCostUsesCurrentWorkspace(t *testing.T) {
	repository := &fakeAdditionalCostRepository{items: []AdditionalCostRecord{{ID: "source-1-0", SourceID: "source-1", Type: AdditionalCostFixed}}}
	service := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	service.additionalCosts = repository
	input := AdditionalCostInput{Type: AdditionalCostFixed, Name: "服务器", BusinessDate: "2026-08-20", Amount: 100, Days: 2}
	items, err := service.UpdateAdditionalCost(context.Background(), "user-1", "source-1", input)
	if err != nil {
		t.Fatalf("UpdateAdditionalCost() error: %v", err)
	}
	if len(items) != 1 || repository.updatedUser != "user-1" || repository.updatedAdmin != "workspace-1" || repository.updatedSource != "source-1" || repository.updatedInput != input {
		t.Fatalf("update call = %#v, want current workspace and source", repository)
	}
}

func TestMetricsServiceUpdateAdditionalCostRejectsBlankSource(t *testing.T) {
	repository := &fakeAdditionalCostRepository{}
	service := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	service.additionalCosts = repository
	_, err := service.UpdateAdditionalCost(context.Background(), "user-1", " ", AdditionalCostInput{Type: AdditionalCostFixed, BusinessDate: "2026-08-20", Amount: 1, Days: 1})
	if !errors.Is(err, ErrAdditionalCostNotFound) {
		t.Fatalf("blank source error = %v, want ErrAdditionalCostNotFound", err)
	}
	if repository.updatedSource != "" {
		t.Fatalf("blank source reached repository: %#v", repository)
	}
}
