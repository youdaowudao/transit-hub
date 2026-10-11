package connection_health

import (
	"testing"
	"time"
)

func TestModelControlAutoEligibilityOrderedStates(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	evidence := now.Add(-time.Minute)
	base := modelControlAutoStatusInput{Settings: ModelControlAutoSettings{AutoCloseEnabled: true, AutoRestoreEnabled: true, AutoCloseEnabledAt: &old, AutoRestoreEnabledAt: &old, RetryIntervalMinutes: 5}, Item: ModelControlItem{Decision: "close_recommended", Basis: modelControlBasis{BusinessDay: "today", BatchID: "batch", RuleVersion: 3, Decision: "close_recommended"}, Control: modelControlControl{Observation: modelControlObservation{State: "serving"}}}, Object: modelControlTarget{UpdatedAt: old}, EvidenceAt: &evidence, Now: now}
	cases := []struct {
		name, state, reason string
		change              func(*modelControlAutoStatusInput)
	}{
		{"eligible", "eligible", "", func(*modelControlAutoStatusInput) {}},
		{"off", "off", "", func(i *modelControlAutoStatusInput) {
			i.Settings.AutoCloseEnabled = false
			i.Settings.AutoRestoreEnabled = false
		}},
		{"no decision", "none", "", func(i *modelControlAutoStatusInput) { i.Item.Decision = "testing" }},
		{"unenabled operation", "none", "", func(i *modelControlAutoStatusInput) { i.Settings.AutoCloseEnabled = false }},
		{"account busy", "busy", "", func(i *modelControlAutoStatusInput) { i.Item.Control.AccountPending = &modelControlAccountPending{} }},
		{"object busy", "busy", "", func(i *modelControlAutoStatusInput) { i.Item.Control.Pending = &modelControlPendingView{} }},
		{"last model", "manual", "AutoLastModel", func(i *modelControlAutoStatusInput) { i.Item.Control.Observation.State = "last_model" }},
		{"not isolatable", "manual", "AutoNotIsolatable", func(i *modelControlAutoStatusInput) { i.Item.Control.Observation.State = "not_isolatable" }},
		{"not provided", "manual", "AutoNotProvided", func(i *modelControlAutoStatusInput) { i.Item.Control.Observation.State = "not_provided" }},
		{"missing", "manual", "AutoAccountMissing", func(i *modelControlAutoStatusInput) { i.Item.Control.Observation.State = "account_missing" }},
		{"partial close", "manual", "AutoPartiallyClosed", func(i *modelControlAutoStatusInput) { i.Item.Control.ClosedEntries = map[string]string{"a": "A"} }},
		{"unconfirmed", "manual", "AutoUnconfirmedClose", func(i *modelControlAutoStatusInput) {
			i.Object.UnconfirmedClose = &modelControlUnconfirmed{SentAt: now}
		}},
		{"expired unconfirmed", "eligible", "", func(i *modelControlAutoStatusInput) {
			i.Object.UnconfirmedClose = &modelControlUnconfirmed{SentAt: now.Add(-25 * time.Hour)}
		}},
		{"done precedes time", "done", "", func(i *modelControlAutoStatusInput) {
			i.Item.Control.ClosedEntries = map[string]string{"a": "A"}
			i.Item.Control.Observation.State = "closed"
			i.EvidenceAt = nil
		}},
		{"no evidence", "waiting", "", func(i *modelControlAutoStatusInput) { i.EvidenceAt = nil }},
		{"missing enable time", "waiting", "", func(i *modelControlAutoStatusInput) { i.Settings.AutoCloseEnabledAt = nil }},
		{"retry", "retry", "Busy", func(i *modelControlAutoStatusInput) {
			i.Object.AutoState = &modelControlAutoAttempt{Operation: "close", Basis: autoBasis(i.Item.Basis, i.EvidenceAt), AttemptAt: now, ReasonKey: modelControlError("Busy")}
		}},
		{"retry ignored", "eligible", "", func(i *modelControlAutoStatusInput) {
			i.IgnoreRetry = true
			i.Object.AutoState = &modelControlAutoAttempt{Operation: "close", Basis: autoBasis(i.Item.Basis, i.EvidenceAt), AttemptAt: now}
		}},
		{"retry expired", "eligible", "", func(i *modelControlAutoStatusInput) {
			i.Object.AutoState = &modelControlAutoAttempt{Operation: "close", Basis: autoBasis(i.Item.Basis, i.EvidenceAt), AttemptAt: now.Add(-5 * time.Minute)}
		}},
		{"restore", "eligible", "", func(i *modelControlAutoStatusInput) {
			i.Item.Decision = "usable"
			i.Item.Control.ClosedEntries = map[string]string{"a": "A"}
			i.Item.Control.Observation.State = "closed"
		}},
		{"restore only owned", "none", "", func(i *modelControlAutoStatusInput) { i.Item.Decision = "usable" }},
		{"restore conflict", "manual", "AutoConflict", func(i *modelControlAutoStatusInput) {
			i.Item.Decision = "usable"
			i.Item.Control.ClosedEntries = map[string]string{"a": "A"}
			i.Item.Control.ConflictReason = "conflict"
		}},
		{"restore missing", "manual", "AutoAccountMissing", func(i *modelControlAutoStatusInput) {
			i.Item.Decision = "usable"
			i.Item.Control.ClosedEntries = map[string]string{"a": "A"}
			i.Item.Control.Observation.State = "account_missing"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.change(&in)
			out := modelControlAutoStatus(in)
			reason := ""
			if c.reason != "" {
				reason = modelControlError(c.reason)
			}
			if out.State != c.state || out.ReasonKey != reason || !out.ChangedAt.Equal(old) {
				t.Fatalf("status=%+v want %s/%s", out, c.state, reason)
			}
			if c.state == "retry" && (out.NextAt == nil || !out.NextAt.Equal(now.Add(5*time.Minute))) {
				t.Fatal("retry time missing")
			}
		})
	}
	for _, op := range []string{"close", "restore"} {
		for _, source := range []string{"enabled", "rule", "ownership"} {
			for _, relation := range []string{"older", "equal", "newer"} {
				t.Run(op+"/"+source+"/"+relation, func(t *testing.T) {
					in := base
					if op == "restore" {
						in.Item.Decision = "usable"
						in.Item.Control.ClosedEntries = map[string]string{"a": "A"}
						in.Item.Control.Observation.State = "closed"
					}
					start := evidence
					if relation == "older" {
						start = evidence.Add(-time.Nanosecond)
					} else if relation == "newer" {
						start = evidence.Add(time.Nanosecond)
					}
					switch source {
					case "enabled":
						if op == "close" {
							in.Settings.AutoCloseEnabledAt = &start
						} else {
							in.Settings.AutoRestoreEnabledAt = &start
						}
					case "rule":
						in.RuleSavedAt = &start
					case "ownership":
						in.Object.UpdatedAt = start
					}
					want := "waiting"
					if relation == "older" {
						want = "eligible"
					}
					if out := modelControlAutoStatus(in); out.State != want {
						t.Fatalf("%+v want %s", out, want)
					}
				})
			}
		}
	}
	for _, field := range []string{"operation", "businessDay", "batchId", "ruleVersion", "decision", "evidenceAt"} {
		t.Run("new basis/"+field, func(t *testing.T) {
			in := base
			attempt := &modelControlAutoAttempt{Operation: "close", Basis: autoBasis(in.Item.Basis, in.EvidenceAt), AttemptAt: now}
			switch field {
			case "operation":
				attempt.Operation = "restore"
			case "businessDay":
				attempt.Basis.BusinessDay = "other"
			case "batchId":
				attempt.Basis.BatchID = "other"
			case "ruleVersion":
				attempt.Basis.RuleVersion++
			case "decision":
				attempt.Basis.Decision = "usable"
			case "evidenceAt":
				at := evidence.Add(-time.Second)
				attempt.Basis.EvidenceAt = &at
			}
			in.Object.AutoState = attempt
			if out := modelControlAutoStatus(in); out.State != "eligible" {
				t.Fatal(out)
			}
		})
	}
}
