package my_sites

import (
	"context"
	"testing"
)

// API-only startup must not rely on schema mutation to connect a repository.
// The primary agent added this after the real import retained its resources
// without ever sending a database save or Commit.
func TestC5REDConstructorConnectsProductionBindingRepositoryWithoutSchemaWrites(t *testing.T) {
	repository := NewRepository(nil)
	service := NewService(repository, nil, nil)
	if service.connRepository != repository {
		t.Fatal("API-only construction left persistence, replay and existing bindings disconnected until EnsureSchema")
	}
}

type c5ConstructorRepository struct {
	*testStateRepo
	*testConnRepo
}

func (r *c5ConstructorRepository) GetRealConnectionByOperationID(ctx context.Context, user, workspace, operation string) (*RealConnection, error) {
	if r.connection == nil || r.connection.UserID != user || r.connection.WorkspaceAdminAccountID != workspace || r.connection.OperationID != operation {
		return nil, nil
	}
	return r.GetRealConnection(ctx, r.connection.ID, user, workspace)
}

func TestC5REDConstructorImportPersistsAndReplaysWithoutSchemaStartup(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	configured, bindings := c5RedService(t, remote)
	state := configured.repository.(*testStateRepo)
	repository := &c5ConstructorRepository{testStateRepo: state, testConnRepo: bindings}
	service := NewService(repository, configured.platformService, configured.upstreamLookup)
	service.SetAdminAccountResolver(configured.accounts)
	service.SetSafeAdminAccountDeletion(configured.safeAdminDeletion)
	request := c5RedRequest(t, "")
	response, err := service.RealConnect(t.Context(), "user-1", request)
	if err != nil {
		t.Fatalf("a fully configured repository must persist without running schema startup: %v", err)
	}
	if bindings.saveCalls != 1 || bindings.connection == nil || response.Connection.ID == "" || response.ConfigurationStatus != "confirmed" {
		t.Fatal("native confirmed account was not saved as one complete local binding")
	}
	response, err = service.RealConnect(t.Context(), "user-1", request)
	if err != nil || response.ConfigurationStatus != "confirmed" || response.Configuration.Observation != "current" {
		t.Fatal("completed import could not replay through the constructor-provided repository")
	}
	if bindings.saveCalls != 1 || remote.keyCreates != 1 || remote.accountCreates != 1 || remote.previews != 1 || remote.reads != 2 {
		t.Fatal("constructor replay recreated resources, repeated sync or failed to use the existing binding")
	}
}
