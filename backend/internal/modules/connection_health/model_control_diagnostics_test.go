package connection_health

import (
	"testing"
	"transithub/backend/internal/modules/upstream"
)

func TestModelControlExplicitRejectionFinalizes(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.bulkCode = 400
	code, result := f.execute("1", "A", "close", p)
	item := f.item("1", "A")
	if code != 200 || item["control"].(map[string]any)["pending"] != nil {
		t.Fatalf("rejected mutation code=%d result=%v control=%v", code, result, item["control"])
	}
}
func TestModelControlRejectionClassification(t *testing.T) {
	f := newC3REDFixture(t)
	f.bulkCode = 400
	session, _, account, err := f.service.resolveManualSession(t.Context(), c3REDUser, c3REDTarget("1"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.service.modelControlActions.UpdateSub2APIAdminAccountModelMappingContext(t.Context(), session, account, map[string]string{"b": "B"})
	if upstream.RemoteMutationOutcome(err) != upstream.MutationConfirmedRejected {
		t.Fatalf("classification=%s error=%#v", upstream.RemoteMutationOutcome(err), err)
	}
}
