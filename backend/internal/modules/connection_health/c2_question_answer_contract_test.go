package connection_health

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func c2FixedSingapore(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value+"+08:00")
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestC2QuestionAnswerTimeGridAnchorsAndStrictFuture(t *testing.T) {
	day := QuestionAnswerScheduleConfig{PeakStart: "08:00", PeakEnd: "22:00", PeakIntervalMinutes: 30, OffPeakIntervalMinutes: 180}
	night := QuestionAnswerScheduleConfig{PeakStart: "22:00", PeakEnd: "08:00", PeakIntervalMinutes: 60, OffPeakIntervalMinutes: 180}
	for _, tc := range []struct {
		name        string
		config      QuestionAnswerScheduleConfig
		after, want string
		strict      bool
	}{
		{"peak-start-inclusive", day, "2026-10-01T08:00:00", "2026-10-01T08:00:00", false},
		{"save-strict", day, "2026-10-01T08:00:00", "2026-10-01T08:30:00", true},
		{"end-offpeak", day, "2026-10-01T21:59:59", "2026-10-01T22:00:00", true},
		{"end-new-anchor", day, "2026-10-01T22:00:00", "2026-10-02T01:00:00", true},
		{"offpeak-to-peak", day, "2026-10-01T07:00:00", "2026-10-01T08:00:00", true},
		{"night-to-midnight", night, "2026-10-01T23:00:00", "2026-10-02T00:00:00", true},
		{"night-end", night, "2026-10-02T07:00:00", "2026-10-02T08:00:00", true},
		{"night-offpeak", night, "2026-10-02T08:00:00", "2026-10-02T11:00:00", true},
		{"next-night-anchor", night, "2026-10-02T20:00:00", "2026-10-02T22:00:00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := c2FixedSingapore(t, tc.after)
			want := c2FixedSingapore(t, tc.want)
			got, err := nextQuestionAnswerScheduleSlot(tc.config, at.In(time.FixedZone("caller", -7*60*60)), tc.strict)
			if err != nil || !got.Equal(want) {
				t.Fatalf("grid got=%s err=%v want=%s", got, err, want)
			}
		})
	}
	for _, interval := range []int{30, 60, 120, 180, 1440} {
		config := day
		config.PeakIntervalMinutes = interval
		config.OffPeakIntervalMinutes = interval
		start := c2FixedSingapore(t, "2026-10-01T08:00:00")
		slots, err := questionAnswerScheduleSlotsBetween(config, start, start.Add(24*time.Hour))
		if err != nil || len(slots) < 3 || !slots[0].Equal(start) || !slots[len(slots)-1].Equal(start.Add(24*time.Hour)) {
			t.Fatalf("interval %d must include both grid endpoints: %v / %v", interval, slots, err)
		}
		for i := 1; i < len(slots); i++ {
			if !slots[i].After(slots[i-1]) {
				t.Fatal("duplicate or backward slot")
			}
		}
	}
	for _, mutate := range []func(*QuestionAnswerScheduleConfig){
		func(c *QuestionAnswerScheduleConfig) { c.PeakEnd = c.PeakStart },
		func(c *QuestionAnswerScheduleConfig) { c.PeakStart = "8:00" },
		func(c *QuestionAnswerScheduleConfig) { c.PeakStart = "24:00" },
		func(c *QuestionAnswerScheduleConfig) { c.PeakIntervalMinutes = 0 },
		func(c *QuestionAnswerScheduleConfig) { c.OffPeakIntervalMinutes = 31 },
		func(c *QuestionAnswerScheduleConfig) { c.PeakIntervalMinutes = 1470 },
	} {
		config := day
		mutate(&config)
		if _, err := nextQuestionAnswerScheduleSlot(config, time.Now(), true); err == nil {
			t.Fatalf("invalid time accepted: %+v", config)
		}
	}
}

func TestC2QuestionAnswerSharedSlotsApplyVersionAndDrainWithoutCancelling(t *testing.T) {
	ctx := context.Background()
	run := &activeQuestionAnswerBatch{ctx: ctx, records: make([]QuestionAnswerRecord, 50)}
	service := &Service{questionAnswerCtx: ctx, questionAnswerRuns: map[string]*activeQuestionAnswerBatch{"run": run}, questionAnswerOrder: []string{"run"}, questionAnswerWake: make(chan struct{}, 1)}
	dispatch := func() bool {
		service.questionAnswerMu.Lock()
		defer service.questionAnswerMu.Unlock()
		_, _, _, ok := service.nextQuestionAnswerDispatchLocked()
		return ok
	}
	service.SetQuestionAnswerConcurrency(6, 0)
	for i := 0; i < 6; i++ {
		if !dispatch() {
			t.Fatal("first version zero must apply")
		}
	}
	if dispatch() {
		t.Fatal("six slot limit exceeded")
	}
	service.SetQuestionAnswerConcurrency(15, 2)
	select {
	case <-service.questionAnswerWake:
	default:
		t.Fatal("raising capacity must wake dispatcher")
	}
	for i := 0; i < 5; i++ {
		if !dispatch() {
			t.Fatal("new slots were not available")
		}
	}
	service.SetQuestionAnswerConcurrency(6, 3)
	service.SetQuestionAnswerConcurrency(15, 2)
	service.SetQuestionAnswerConcurrency(15, 3)
	if dispatch() || service.questionAnswerInFlight != 11 || run.ctx.Err() != nil || run.inFlight != 11 {
		t.Fatal("shrink or stale callback must not cancel or release 11 admitted requests")
	}
	service.questionAnswerMu.Lock()
	service.questionAnswerInFlight = 6
	run.inFlight = 6
	service.questionAnswerMu.Unlock()
	if dispatch() {
		t.Fatal("must wait until below new six-slot limit")
	}
	service.questionAnswerMu.Lock()
	service.questionAnswerInFlight = 5
	run.inFlight = 5
	service.questionAnswerMu.Unlock()
	if !dispatch() || dispatch() {
		t.Fatal("only one new request can enter after draining to five")
	}
}

func TestC2QuestionAnswerExecutionOutcomePreservesFailureSemantics(t *testing.T) {
	resolved := &QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "frozen-question"}}}
	base := QuestionAnswerScheduleExecution{Status: "active", ConfigSnapshot: QuestionAnswerScheduleSnapshot{Resolved: resolved}}
	result := func(succeeded, failed, inProgress int) QuestionAnswerScheduleExecutionTarget {
		return QuestionAnswerScheduleExecutionTarget{Status: "batch_created", BatchAvailable: true, Stats: QuestionAnswerStats{Requests: QuestionAnswerRequestStats{Submitted: succeeded + failed + inProgress, Succeeded: succeeded, Failed: failed, InProgress: inProgress}, Reviews: QuestionAnswerReviewStats{Incorrect: succeeded}}}
	}
	unavailable := result(1, 0, 0)
	unavailable.UnavailableModels = []QuestionAnswerScheduleUnavailableModel{{ModelName: "missing", Reason: "model_unavailable"}}
	missing := QuestionAnswerScheduleExecutionTarget{Status: "failed", StatusReason: "account_not_found"}
	for _, tc := range []struct {
		name, cause, want string
		targets           []QuestionAnswerScheduleExecutionTarget
	}{
		{"wrong-answer-is-completed", "", "completed", []QuestionAnswerScheduleExecutionTarget{result(1, 0, 0)}},
		{"mixed-request-failure", "", "partial", []QuestionAnswerScheduleExecutionTarget{result(1, 1, 0)}},
		{"zero-success-is-failed", "", "failed", []QuestionAnswerScheduleExecutionTarget{result(0, 2, 0)}},
		{"missing-model-with-result", "", "partial", []QuestionAnswerScheduleExecutionTarget{unavailable}},
		{"all-target-busy-is-failed", "", "failed", []QuestionAnswerScheduleExecutionTarget{{Status: "skipped", StatusReason: "target_busy"}}},
		{"all-models-unavailable-is-failed", "", "failed", []QuestionAnswerScheduleExecutionTarget{{Status: "failed", StatusReason: "model_unavailable"}}},
		{"partial-accounts-missing", "", "partial", []QuestionAnswerScheduleExecutionTarget{missing, result(1, 0, 0)}},
		{"pending-is-active", "", "active", []QuestionAnswerScheduleExecutionTarget{{Status: "pending"}}},
		{"record-running-is-active", "", "active", []QuestionAnswerScheduleExecutionTarget{result(1, 0, 1)}},
		{"user-cancel-keeps-success", "user_cancel", "cancelled", []QuestionAnswerScheduleExecutionTarget{result(1, 0, 0)}},
		{"timeout-no-batch", "execution_timeout", "skipped", []QuestionAnswerScheduleExecutionTarget{{Status: "cancelled"}}},
		{"timeout-has-success", "execution_timeout", "partial", []QuestionAnswerScheduleExecutionTarget{result(1, 0, 0)}},
		{"timeout-no-success", "execution_timeout", "failed", []QuestionAnswerScheduleExecutionTarget{result(0, 1, 0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := base
			execution.TerminationCause = tc.cause
			status, _ := summarizeQuestionAnswerScheduleExecution(execution, tc.targets)
			if status != tc.want {
				t.Fatalf("got=%s want=%s", status, tc.want)
			}
		})
	}
	missingExecution := base
	missingExecution.Status = "skipped"
	missingExecution.StatusReason = "all_accounts_missing"
	missingExecution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{MissingTargetIDs: []string{"a", "b"}, Questions: []TestQuestion{}}
	if status, reason := summarizeQuestionAnswerScheduleExecution(missingExecution, []QuestionAnswerScheduleExecutionTarget{missing, missing}); status != "skipped" || reason != "all_accounts_missing" {
		t.Fatal("zero-success generic result cannot overwrite confirmed missing accounts")
	}
	if status, _ := summarizeQuestionAnswerScheduleExecution(QuestionAnswerScheduleExecution{Status: "active"}, nil); status != "active" {
		t.Fatal("unresolved zero targets still requires preparation")
	}
}

func TestC2QuestionAnswerDeadlineAndStartWindowUseAcceptedSnapshot(t *testing.T) {
	created := c2FixedSingapore(t, "2026-10-01T08:03:00")
	slot := c2FixedSingapore(t, "2026-10-01T08:00:00")
	started := created.Add(20 * time.Minute)
	e := QuestionAnswerScheduleExecution{Trigger: "scheduled", ScheduledFor: slot, CreatedAt: created, StartedAt: &started, ConfigSnapshot: QuestionAnswerScheduleSnapshot{Requested: QuestionAnswerScheduleRequested{Limits: QuestionAnswerScheduleLimits{ScheduleExecutionTimeoutMinutes: 30, ScheduleLateGraceMinutes: 5}}}}
	if !e.deadline().Equal(created.Add(30*time.Minute)) || !e.startWindowEnd().Equal(slot.Add(5*time.Minute)) {
		t.Fatal("queued/preparation must not extend deadline or scheduled grace")
	}
	e.Trigger = "run_now"
	if !e.startWindowEnd().Equal(created.Add(5 * time.Minute)) {
		t.Fatal("run-now grace must anchor accepted createdAt")
	}
	if !reflect.DeepEqual(defaultQuestionAnswerScheduleLimits(), QuestionAnswerScheduleLimits{MaxEnabledQuestionAnswerSchedules: 20, MaxScheduleTargets: 20, MaxScheduleRequestsPerExecution: 500, DailyScheduledRequestLimit: 1000, MaxActiveScheduleExecutions: 3, MaxQueuedScheduledRequests: 1000, ScheduleExecutionTimeoutMinutes: 360, ScheduleLateGraceMinutes: 5}) {
		t.Fatal("default eight safety limits changed")
	}
}
