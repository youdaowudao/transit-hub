package connection_health

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/modules/upstream"
)

func TestModelControlV2911AddPlanNeverOverwritesExistingMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping map[string]string
		edit    func(*upstream.AdminGroupAccountInfo)
		reason  string
	}{
		{"type", map[string]string{"b": "B"}, func(a *upstream.AdminGroupAccountInfo) { a.Type = "oauth" }, "AddTypeUnsupported"},
		{"passthrough", map[string]string{"b": "B"}, func(a *upstream.AdminGroupAccountInfo) { a.OpenAIPassthrough = true }, "AddPassthrough"},
		{"unreadable", nil, func(a *upstream.AdminGroupAccountInfo) { a.ModelMappingKnown = false }, "AccountReadFailed"},
		{"empty", map[string]string{}, nil, "AddMappingEmpty"},
		{"provided", map[string]string{"alias": "M"}, nil, "AlreadyProvided"},
		{"same_key", map[string]string{"M": "B"}, nil, "AddKeyConflict"},
		{"wildcard", map[string]string{"M*": "B"}, nil, "AddWildcardConflict"},
		{"safe", map[string]string{"b": "B"}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := modelControlTestAccount(tc.mapping)
			if tc.edit != nil {
				tc.edit(&a)
			}
			before := cloneModelMapping(a.ModelMapping)
			if a.ModelMapping == nil {
				before = nil
			}
			plan := planModelAdd(upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}, "M")
			if tc.reason != "" {
				if plan.ReasonKey != modelControlError(tc.reason) {
					t.Fatalf("wrong block: %+v", plan)
				}
			} else {
				if !reflect.DeepEqual(plan.Entries, map[string]string{"M": "M"}) || !reflect.DeepEqual(plan.AfterMapping, map[string]string{"b": "B", "M": "M"}) {
					t.Fatalf("unsafe add plan=%+v", plan)
				}
			}
			if !reflect.DeepEqual(before, a.ModelMapping) {
				t.Fatal("planning mutated input mapping")
			}
			if (tc.name == "same_key" || tc.name == "wildcard") && len(plan.Blocking) != 1 {
				t.Fatal("blocking entry missing from preview")
			}
		})
	}
}

func TestModelControlV2911SupplyUsesUpstreamValuesAndKnownBlockingFirst(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		mapping       map[string]string
		edit          func(*upstream.AdminGroupAccountInfo)
		open, unknown bool
	}{
		{"alias", map[string]string{"alias": "M"}, nil, true, false},
		{"key_is_not_value", map[string]string{"M": "B"}, nil, false, false},
		{"multiple_aliases", map[string]string{"a": "M", "b": "M"}, nil, true, false},
		{"wildcard", map[string]string{"a*": "M"}, nil, true, false},
		{"passthrough_ignores_residue", map[string]string{"M": "B"}, func(a *upstream.AdminGroupAccountInfo) { a.OpenAIPassthrough = true }, true, false},
		{"empty", map[string]string{}, nil, true, false},
		{"unreadable", nil, func(a *upstream.AdminGroupAccountInfo) { a.ModelMappingKnown = false }, false, true},
		{"unreadable_disabled", nil, func(a *upstream.AdminGroupAccountInfo) { a.ModelMappingKnown = false; a.Status = "inactive" }, false, false},
		{"unreadable_schedulable_off", nil, func(a *upstream.AdminGroupAccountInfo) { a.ModelMappingKnown = false; no := false; a.Schedulable = &no }, false, false},
		{"unknown_status", map[string]string{"M": "M"}, func(a *upstream.AdminGroupAccountInfo) { a.Status = "unrecognized" }, false, true},
		{"unknown_model_limits", map[string]string{"M": "M"}, func(a *upstream.AdminGroupAccountInfo) { a.ModelRateLimitsKnown = false }, false, true},
		{"unknown_schedulable", map[string]string{"M": "M"}, func(a *upstream.AdminGroupAccountInfo) { a.Schedulable = nil }, false, true},
		{"quota_usage_missing", map[string]string{"M": "M"}, func(a *upstream.AdminGroupAccountInfo) { n := 10.0; a.QuotaLimit = &n; a.QuotaUsed = nil }, false, true},
		{"quota_exhausted_unreadable", nil, func(a *upstream.AdminGroupAccountInfo) {
			n := 10.0
			a.QuotaLimit = &n
			a.QuotaUsed = &n
			a.ModelMappingKnown = false
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := modelControlTestAccount(tc.mapping)
			if tc.edit != nil {
				tc.edit(&a)
			}
			keys, known, eligible := modelControlUpstreamKeys(a, "M")
			if !eligible {
				t.Fatal("fixture API key excluded")
			}
			open, unknown := modelControlSupplyState(a, keys, known, "M", now)
			if open != tc.open || unknown != tc.unknown {
				t.Fatalf("open=%v unknown=%v want=%v/%v", open, unknown, tc.open, tc.unknown)
			}
		})
	}
}

func TestModelControlV2911SummaryUnknownIsNeverOpen(t *testing.T) {
	for _, tc := range []struct{ state, reason, want string }{{"serving", "", "open"}, {"last_model", "", "open"}, {"not_isolatable", "OpenAIPassthrough", "open"}, {"not_isolatable", "EmptyMapping", "open"}, {"not_isolatable", "WildcardMapping", "open"}, {"not_isolatable", "WildcardFallback", "open"}, {"not_isolatable", "AccountReadFailed", "unknown"}, {"not_isolatable", "AccountTypeUnsupported", "unknown"}, {"unverified", "", "unknown"}, {"not_provided", "", "not_provided"}, {"account_missing", "", "account_missing"}} {
		target := modelControlTarget{Observation: modelControlObservation{State: tc.state, ReasonKey: modelControlError(tc.reason)}}
		if got := modelControlSummaryStatus(target); got != tc.want {
			t.Fatalf("%s/%s got=%s want=%s", tc.state, tc.reason, got, tc.want)
		}
	}
}

type v2911PreparedSettingsChange struct {
	modelControlRepository
	t       *testing.T
	pool    *Repository
	changed bool
}

func (r *v2911PreparedSettingsChange) mutateModelControlOwnership(ctx context.Context, user, workspace, target string, mutation bool, stage string, fn func([]*modelControlTarget, time.Time) ([]ModelControlEvent, error), guards ...modelControlTxGuard) error {
	if stage != "prepared" || r.changed {
		return r.modelControlRepository.mutateModelControlOwnership(ctx, user, workspace, target, mutation, stage, fn, guards...)
	}
	r.changed = true
	tx, err := r.pool.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	done := make(chan error, 1)
	go func() {
		done <- r.modelControlRepository.mutateModelControlOwnership(ctx, user, workspace, target, mutation, stage, fn, guards...)
	}()
	waiting := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err = r.pool.db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event='advisory' AND query ILIKE '%pg_advisory_xact_lock%'`).Scan(&n)
		if err != nil {
			return err
		}
		if n > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		r.t.Error("execution never waited on workspace lock")
	}
	_, err = tx.Exec(ctx, `UPDATE connection_health_model_control_settings SET version=version+1 WHERE user_id=$1 AND admin_account_id=$2`, user, workspace)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return context.DeadlineExceeded
	}
}

func TestModelControlV2911PreparedTransactionRejectsSettingsSavedWhileWaiting(t *testing.T) {
	f := newC3REDFixture(t)
	f.expect("PUT", "model-control/settings", map[string]any{"minAccuracyPercent": 50, "minJudgedAnswers": 3, "expectedVersion": 0}, 200)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	repo := f.service.questionAnswers.(*Repository)
	f.service.modelControls = &v2911PreparedSettingsChange{modelControlRepository: f.service.modelControls, t: t, pool: repo}
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "BasisChanged") || f.writeCount() != 0 {
		t.Fatalf("changed settings escaped transaction guard: code=%d writes=%d result=%v", code, f.writeCount(), result)
	}
	if f.item("1", "A")["control"].(map[string]any)["pending"] != nil {
		t.Fatal("changed settings created pending operation")
	}
}

// Compile-time contract: guards must run in the transaction that writes pending.
var _ modelControlTxGuard = func(context.Context, pgx.Tx, time.Time) error { return nil }
