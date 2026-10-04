package my_sites

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type failureFixReferenceCounter interface {
	CountSiteReferences(context.Context, string, string, string) (int, int, error)
}
type failureFixReferenceState struct {
	testStateRepo
	err                 error
	userID, workspaceID string
}

func (r *failureFixReferenceState) Get(ctx context.Context, userID, workspaceID string) (*State, error) {
	if r.err != nil {
		return nil, r.err
	}
	if userID != r.userID || workspaceID != r.workspaceID {
		return nil, nil
	}
	return r.testStateRepo.Get(ctx, userID, workspaceID)
}

type failureFixReferenceConnections struct {
	missingConnectionRepository
	err error
}

func (r *failureFixReferenceConnections) ListRealConnections(ctx context.Context, userID, workspaceID string) ([]RealConnection, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.missingConnectionRepository.ListRealConnections(ctx, userID, workspaceID)
}

func TestFailureFixStageDCountsAllLocalReferenceStatesAndMappingsOnce(t *testing.T) {
	repository := &failureFixReferenceState{userID: "fixture-user", workspaceID: "fixture-workspace", testStateRepo: testStateRepo{state: &State{UserID: "fixture-user", AdminAccountID: "fixture-workspace", Mappings: []GroupMapping{
		{OwnGroup: "duplicate_targets_and_primary", UpstreamTargets: []UpstreamGroupRef{{SiteID: "fixture-site"}, {SiteID: "fixture-site"}}, PrimaryUpstreamSiteID: "fixture-site"},
		{OwnGroup: "primary_only", PrimaryUpstreamSiteID: "fixture-site"},
		{OwnGroup: "target_only", UpstreamTargets: []UpstreamGroupRef{{SiteID: "other-site"}, {SiteID: "fixture-site"}}},
		{OwnGroup: "other", PrimaryUpstreamSiteID: "other-site"},
	}}}}
	connections := &failureFixReferenceConnections{}
	for _, status := range []string{"active", "missing", "inactive", "error", ""} {
		connections.items = append(connections.items, RealConnection{UserID: "fixture-user", WorkspaceAdminAccountID: "fixture-workspace", UpstreamSiteID: "fixture-site", Status: status})
	}
	connections.items = append(connections.items, RealConnection{UserID: "other-user", WorkspaceAdminAccountID: "fixture-workspace", UpstreamSiteID: "fixture-site"}, RealConnection{UserID: "fixture-user", WorkspaceAdminAccountID: "other-workspace", UpstreamSiteID: "fixture-site"}, RealConnection{UserID: "fixture-user", WorkspaceAdminAccountID: "fixture-workspace", UpstreamSiteID: "other-site"})
	service := NewService(repository, nil, nil)
	service.connRepository = connections
	counter, ok := any(service).(failureFixReferenceCounter)
	if !ok {
		t.Fatal("local reference counter is not implemented")
	}
	before := cloneState(repository.state)
	n, m, err := counter.CountSiteReferences(context.Background(), "fixture-user", "fixture-workspace", "fixture-site")
	if err != nil || n != 5 || m != 3 {
		t.Errorf("reference result=%d/%d error=%t, want 5/3", n, m, err != nil)
	}
	if !reflect.DeepEqual(before, repository.state) || len(repository.saves) != 0 {
		t.Error("reference read mutated pricing state")
	}
	n, m, err = counter.CountSiteReferences(context.Background(), "fixture-user", "other-workspace", "fixture-site")
	if err != nil || n != 1 || m != 0 {
		t.Error("explicit workspace reference isolation failed")
	}
	repository.err = errors.New("fixture state unavailable")
	if _, _, err = counter.CountSiteReferences(context.Background(), "fixture-user", "fixture-workspace", "fixture-site"); err == nil {
		t.Error("state read failure was accepted as empty")
	}
	repository.err = nil
	connections.err = errors.New("fixture connections unavailable")
	if _, _, err = counter.CountSiteReferences(context.Background(), "fixture-user", "fixture-workspace", "fixture-site"); err == nil {
		t.Error("connection read failure was accepted as empty")
	}
}
