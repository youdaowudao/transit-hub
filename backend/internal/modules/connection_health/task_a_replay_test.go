package connection_health

import (
	"encoding/csv"
	"os"
	"strconv"
	"testing"
	"time"
)

func taskAReplayRows(t *testing.T) [][]string {
	t.Helper()
	f, err := os.Open("testdata/task_a_replay.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 16475 {
		t.Fatalf("replay must retain all 16474 exported events, got %d", len(rows)-1)
	}
	return rows[1:]
}

func taskAReplayInt(t *testing.T, s string) int {
	t.Helper()
	if s == "" {
		return 0
	}
	v, e := strconv.Atoi(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestTaskALegacyProductionReplay(t *testing.T) {
	states := map[string]TransitionOutput{}
	checked, mismatches := 0, 0
	for _, row := range taskAReplayRows(t) {
		if row[3] == "*" {
			continue
		}
		key := row[1] + ":" + row[3]
		current, ok := states[key]
		if !ok {
			weight := 0
			if row[6] == "healthy" {
				weight = 100
			}
			current = TransitionOutput{NextState: State(row[6]), Weight: weight}
		}
		now := time.UnixMilli(int64(taskAReplayInt(t, row[4])))
		policy := Policy{FailureThreshold: 3, SuccessThreshold: 2, CooldownSeconds: 300, ObservationSeconds: 50, RecoveryStepPercent: 25}
		if row[3] == "claude-sonnet-5" || row[2] == "group-5" {
			policy.ObservationSeconds = 300
		}
		next := current
		if row[11] == "applied" {
			next = Transition(TransitionInput{Current: current.NextState, CurrentWeight: current.Weight, ConsecutiveFailures: current.ConsecutiveFailures, ConsecutiveSuccesses: current.ConsecutiveSuccesses, CooldownUntil: current.CooldownUntil, ObservingUntil: current.ObservingUntil, Now: now, Result: ResultKey(row[5]), Policy: policy})
		}
		states[key] = next
		checked++
		if string(next.NextState) != row[7] {
			mismatches++
			t.Logf("historical difference sequence=%s target=%s model=%s result=%s from=%s actual=%s recorded=%s", row[0], row[1], row[3], row[5], row[6], next.NextState, row[7])
		}
	}
	t.Logf("legacy replay probes=%d differences=%d", checked, mismatches)
	if mismatches != 0 {
		t.Fatalf("historical differences need individually documented fixed allowances: %d", mismatches)
	}
}

func TestTaskAV2ProductionReplay(t *testing.T) {
	states := map[string]ConnectionHealthState{}
	checked := 0
	for _, row := range taskAReplayRows(t) {
		if row[3] == "*" {
			continue
		}
		key := row[1] + ":" + row[3]
		current, ok := states[key]
		if !ok {
			state := State(row[6])
			weight := 100
			successes := 0
			switch state {
			case StateObserving:
				state = StateSuspended
				weight = 0
				successes = 1
			case StateRecovering, StateDegraded:
				state = StateDegraded
				weight = 75
			case StateSuspended, StateDisabled:
				weight = 0
			}
			current = ConnectionHealthState{State: state, CurrentWeight: weight, ConsecutiveSuccesses: successes, CounterProtocol: protocolPointer(TestProtocol(row[9]))}
		}
		now := time.UnixMilli(int64(taskAReplayInt(t, row[4])))
		outcome := ProbeOutcome{Protocol: TestProtocol(row[9]), ProbeTimeoutSeconds: taskAReplayInt(t, row[10]), Result: ResultKey(row[5]), LatencyMs: taskAReplayInt(t, row[8])}
		next, _ := applyProbeOutcome(current, outcome, taskAV2Policy(), now)
		states[key] = next
		checked++
		if string(next.State) != row[12] || next.CurrentWeight != taskAReplayInt(t, row[13]) || next.ConsecutiveFailures != taskAReplayInt(t, row[14]) || next.ConsecutiveSuccesses != taskAReplayInt(t, row[15]) || strconv.FormatBool(taskABool(next, "RecheckPending")) != row[16] {
			t.Fatalf("v2 oracle differs sequence=%s target=%s result=%s got=%s/%d/%d/%d/%v expected=%s/%s/%s/%s/%s", row[0], row[1], row[5], next.State, next.CurrentWeight, next.ConsecutiveFailures, next.ConsecutiveSuccesses, taskABool(next, "RecheckPending"), row[12], row[13], row[14], row[15], row[16])
		}
	}
	t.Logf("SPEC 3.2 v2 replay probes=%d matched=%d", checked, checked)
}
