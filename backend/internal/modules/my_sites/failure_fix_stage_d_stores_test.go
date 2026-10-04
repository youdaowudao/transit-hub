package my_sites

import (
	"context"
	"testing"
)

func TestFailureFixStageDReferenceCounterRejectsIncompleteLocalStores(t *testing.T) {
	for _, missing := range []string{"state", "connections"} {
		service := NewService(nil, nil, nil)
		if missing != "state" {
			service.repository = &testStateRepo{}
		}
		if missing != "connections" {
			service.connRepository = &missingConnectionRepository{}
		}
		connections, mappings, err := service.CountSiteReferences(context.Background(), "fixture-user", "fixture-workspace", "fixture-site")
		if err == nil || connections != 0 || mappings != 0 {
			t.Error("incomplete reference repository was accepted as proven empty")
		}
	}
}
