package upstream

import "testing"

func TestInventoryTimeEvidenceCloneOwnsResponses(t *testing.T) {
	original := &InventoryTimeEvidence{responses: []InventoryResponseTime{{HTTPDate: "original"}}}
	clone := original.Clone()
	if clone == original || clone == nil {
		t.Fatal("evidence clone must own its storage and mutex")
	}
	clone.responses[0].HTTPDate = "changed"
	clone.add(InventoryResponseTime{HTTPDate: "additional"})
	if got := original.Responses(); len(got) != 1 || got[0].HTTPDate != "original" {
		t.Fatal("mutating cloned evidence changed the original read")
	}
	original.add(InventoryResponseTime{HTTPDate: "original only"})
	if got := clone.Responses(); len(got) != 2 || got[1].HTTPDate != "additional" {
		t.Fatal("appending original evidence changed the clone")
	}
	var absent *InventoryTimeEvidence
	if absent.Clone() != nil {
		t.Fatal("absent clock evidence must remain absent")
	}
}
