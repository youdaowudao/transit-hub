package connection_health

import (
	_ "embed"
	"encoding/json"
	"testing"
)

type priorityRecoveryFixture struct {
	Scenario string                        `json:"scenario"`
	Cases    []priorityRecoveryFixtureCase `json:"cases"`
}

type priorityRecoveryFixtureCase struct {
	Name             string `json:"name"`
	MultiplierStatus string `json:"multiplier_status"`
	CurrentPriority  int    `json:"current_priority"`
	HealthState      State  `json:"health_state"`
	ExpectedPriority int    `json:"expected_priority"`
}

//go:embed testdata/priority_recovery_matrix.json
var priorityRecoveryFixtureData []byte

func loadPriorityRecoveryFixture(t *testing.T) priorityRecoveryFixture {
	t.Helper()
	var fixture priorityRecoveryFixture
	if err := json.Unmarshal(priorityRecoveryFixtureData, &fixture); err != nil {
		t.Fatalf("load priority recovery fixture: %v", err)
	}
	if fixture.Scenario == "" || len(fixture.Cases) == 0 {
		t.Fatal("priority recovery fixture must include scenario and cases")
	}
	return fixture
}
