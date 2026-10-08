package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The primary agent fixes this contract: a failed lookup cannot prove that an
// earlier operation has no committed binding, even though this replay made no write.
type c5ReplayLookupFailureRepo struct {
	*testConnRepo
	queries int
}

func (r *c5ReplayLookupFailureRepo) GetRealConnectionByOperationID(context.Context, string, string, string) (*RealConnection, error) {
	r.queries++
	return nil, errors.New("synthetic operation lookup disconnected")
}

func TestC5REDReplayLookupFailureCannotAuthorizeNewOperation(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	service, base := c5RedService(t, remote)
	base.connection = &RealConnection{
		ID: strings.Repeat("a", 32), UserID: "user-1", WorkspaceAdminAccountID: "admin-1",
		UpstreamSiteID: "site-1", UpstreamGroupID: "70", UpstreamGroupName: "upstream-only",
		UpstreamKeyID: "11", AdminAccountID: "22", OwnGroupIDs: []string{"7", "8"},
		OperationID: "c5-fixed-red", Status: ConnectionStatusActive, ProvisioningMode: ProvisioningModeManaged,
	}
	repo := &c5ReplayLookupFailureRepo{testConnRepo: base}
	service.connRepository = repo
	for attempt := 0; attempt < 2; attempt++ {
		_, err := service.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
		var failure *ImportFailure
		if !errors.As(err, &failure) {
			t.Fatalf("lookup failure must propagate a trusted pending contract: %v", err)
		}
		if failure.RetryAllowed || failure.StatusCode != 409 || failure.Cleanup != "pending" || failure.Stage != "persistence" {
			t.Errorf("unknown prior operation must stay pending without authorizing a new operation: stage=%s cleanup=%s retry=%t HTTP=%d", failure.Stage, failure.Cleanup, failure.RetryAllowed, failure.StatusCode)
		}
		public, _ := json.Marshal(failure)
		if strings.Contains(string(public), "synthetic operation lookup") {
			t.Error("lookup cause escaped the safe response")
		}
	}
	if repo.queries != 2 || remote.keyCreates != 0 || remote.accountCreates != 0 || remote.previews != 0 || remote.reads != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 || base.saveCalls != 0 {
		t.Fatal("failed replay lookup must never create, sync, persist or delete resources")
	}
	if base.connection == nil || base.connection.OperationID != "c5-fixed-red" || base.connection.AdminAccountID != "22" || base.connection.UpstreamKeyID != "11" {
		t.Fatal("lookup failure changed the existing completed binding")
	}
}
