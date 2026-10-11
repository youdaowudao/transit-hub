package connection_health

import (
	"context"
	"errors"
	"testing"
)

// A pgx rows.Err can accompany rows that were already decoded. Keep the real
// stored exclusion data and inject that final read error at its repository boundary.
type c32ReviewPartialClosedRepository struct {
	*Repository
	failed         bool
	reads          int
	settingsReads  int
	failSettingsAt int
}

func (r *c32ReviewPartialClosedRepository) GetModelControlAutoSettings(ctx context.Context, user, ws string) (ModelControlAutoSettings, error) {
	r.settingsReads++
	if r.settingsReads == r.failSettingsAt {
		return ModelControlAutoSettings{}, errors.New("fixture later settings read failed")
	}
	return r.Repository.GetModelControlAutoSettings(ctx, user, ws)
}

func (r *c32ReviewPartialClosedRepository) ListModelControlFullyClosed(ctx context.Context, user, ws string) (map[string]map[string]struct{}, error) {
	closed, err := r.Repository.ListModelControlFullyClosed(ctx, user, ws)
	r.reads++
	if err == nil && r.failed {
		return closed, errors.New("fixture final row stream failed")
	}
	return closed, err
}

func TestModelControlC32ReviewPartialReadNeverExcludes(t *testing.T) {
	f := newC32REDFixture(t)
	f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B", "c": "C"})
	for _, model := range []string{"A", "B"} {
		f.manage("1", model)
		f.freshRound("1", model, 1, 2)
		f.close("1", model)
	}
	f.switches(false, false, true)
	f.monitor([]string{"1"}, "A", "B")
	hook := &c32ReviewPartialClosedRepository{Repository: f.service.modelControls.(*Repository)}
	f.service.modelControls = hook
	for _, tc := range []struct {
		name                 string
		partialClosedFailure bool
		failSettingsAt       int
	}{
		{name: "complete read excludes both models"},
		{name: "partial data plus error preserves all models", partialClosedFailure: true},
		{name: "successful exclusion then failed settings read preserves all models", failSettingsAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook.failed = tc.partialClosedFailure
			hook.settingsReads = 0
			hook.reads = 0
			hook.failSettingsAt = tc.failSettingsAt
			failed := tc.partialClosedFailure || tc.failSettingsAt != 0
			groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
			if err != nil {
				t.Fatal(err)
			}
			found, warnings := false, 0
			for _, group := range groups {
				if group.ModelControlError == modelControlError("Storage") {
					warnings++
				}
				for _, account := range group.Accounts {
					if account.TargetID != c32REDTarget("1") {
						continue
					}
					found = true
					models := map[string]bool{}
					for _, model := range account.ModelHealth {
						models[model.ModelName] = model.ModelControlClosed
					}
					for _, model := range account.UnprobedModels {
						models[model.ModelName] = model.ModelControlClosed
					}
					if len(models) != 2 {
						t.Fatalf("complete probe model list was lost: %v", models)
					}
					for _, model := range []string{"A", "B"} {
						closed, exists := models[model]
						if !exists || closed != !failed {
							t.Errorf("model %s: exists=%t closed=%t after read failure=%t", model, exists, closed, failed)
						}
					}
					if account.ModelControlNoActiveProbe != !failed {
						t.Errorf("all probe models excluded=%t after read failure=%t", account.ModelControlNoActiveProbe, failed)
					}
					wantCount := 0
					if failed {
						wantCount = 2
					}
					if group.HealthSummary.PendingModels != wantCount {
						t.Errorf("health statistics lost unprobed models after read failure=%t: pending=%d want=%d", failed, group.HealthSummary.PendingModels, wantCount)
					}
				}
			}
			if !found || hook.reads == 0 || (warnings > 0) != failed {
				t.Fatalf("account/read/warning path missing: found=%t reads=%d warnings=%d failed=%t", found, hook.reads, warnings, failed)
			}
			if tc.failSettingsAt != 0 && hook.settingsReads != tc.failSettingsAt {
				t.Fatalf("later settings failure was not reached: reads=%d wanted=%d", hook.settingsReads, tc.failSettingsAt)
			}
		})
	}
}
