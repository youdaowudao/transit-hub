package connection_health

import (
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// The browser deletion matrix proved the negative revalidation path. Exercise
// its positive counterpart against real PostgreSQL: resolving inventory must
// clear the block without enabling a paused plan or drifting an enabled grid.
func TestC2QuestionAnswerRevalidationClearsRecoveredInventoryAndPreservesEnabledChoice(t *testing.T) {
	repo, pool, ctx := c2ScheduleRepository(t)
	now := c2FixedSingapore(t, "2026-10-08T07:59:00")
	plan := c2ScheduleFixture(t, repo, ctx, "w", now, true)
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_test_questions(id,user_id,name,body,keywords,enabled,is_default) VALUES('q','u','FAKE C2 recovered inventory','FAKE body','{}',true,false)`); err != nil {
		t.Fatal(err)
	}
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "selected"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {}}, credByAccount: map[string]upstream.ProbeCredential{}}, allAccounts: nil}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	service.accounts = fakeAdminAccountResolver{id: "w"}
	service.questionAnswerSchedules = repo
	service.questionAnswerScheduleNow = func() time.Time { return now }
	t.Cleanup(func() { _ = service.ShutdownQuestionAnswers(ctx) })
	for _, enabled := range []bool{true, false} {
		if !enabled {
			var err error
			plan, err = repo.SetQuestionAnswerScheduleState(ctx, "u", "w", plan.ID, "disable", "", plan.Version, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
		}
		reader.allAccounts = nil
		blocked, err := service.SetQuestionAnswerScheduleState(ctx, "u", plan.ID, "revalidate", plan.Version)
		var conflict *QuestionAnswerScheduleConflictError
		if !errors.As(err, &conflict) || blocked.BlockedReason != "all_accounts_missing" || blocked.Enabled != enabled || blocked.NextRunAt != nil {
			t.Fatalf("missing inventory changed the user's enabled choice: %+v / %v", blocked, err)
		}
		reader.allAccounts = []upstream.AdminGroupAccountInfo{{ID: "1", Name: "FAKE recovered", Status: "inactive", Schedulable: boolPointer(false)}}
		now = now.Add(31 * time.Minute)
		recovered, err := service.SetQuestionAnswerScheduleState(ctx, "u", plan.ID, "revalidate", blocked.Version)
		if err != nil || recovered.BlockedReason != "" || recovered.Enabled != enabled || recovered.Version != blocked.Version+1 {
			t.Fatalf("recovered inventory did not clear only the block: %+v / %v", recovered, err)
		}
		if enabled {
			want, err := nextQuestionAnswerScheduleSlot(recovered.QuestionAnswerScheduleConfig, now, true)
			if err != nil || recovered.NextRunAt == nil || !recovered.NextRunAt.Equal(want) {
				t.Fatalf("enabled revalidation did not use the next strict grid: %+v / %v", recovered, err)
			}
		} else if recovered.NextRunAt != nil {
			t.Fatal("revalidating a paused plan enabled future work")
		}
		unchanged, err := service.SetQuestionAnswerScheduleState(ctx, "u", plan.ID, "revalidate", recovered.Version)
		if err != nil || unchanged.Version != recovered.Version || !equalScheduleTime(unchanged.NextRunAt, recovered.NextRunAt) {
			t.Fatalf("a healthy recheck changed version/cursor: %+v / %v", unchanged, err)
		}
		plan = &unchanged
	}
	var executions, records int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connection_health_question_answer_schedule_executions),(SELECT count(*) FROM connection_health_question_answer_records)`).Scan(&executions, &records); err != nil || executions != 0 || records != 0 || reader.credentialCalls.Load() != 0 {
		t.Fatalf("revalidation performed execution/credential work: %d/%d/%d %v", executions, records, reader.credentialCalls.Load(), err)
	}
}
