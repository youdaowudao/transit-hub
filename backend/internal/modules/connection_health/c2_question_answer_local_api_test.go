package connection_health

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func TestC2QuestionAnswerAPIOnlyReadsRemainLocalAndDoNotApplySettings(t *testing.T) {
	repo, pool, ctx := c2ScheduleRepository(t)
	settings, err := repo.SaveQuestionAnswerRuntimeSettings(ctx, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan := c2ScheduleFixture(t, repo, ctx, "w", time.Now().UTC(), false)
	execution, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "read-local", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// Every external dependency is absent: accidental inventory, model, session,
	// probe or mutation access cannot succeed as a hidden part of these GETs.
	service := NewServiceWithBackgroundTasks(repo, nil, nil, nil, false)
	service.accounts = fakeAdminAccountResolver{id: "w"}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	request := func(route string, user bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", route, nil)
		if user {
			r = r.WithContext(authctx.WithUserID(r.Context(), "u"))
		}
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, r)
		return out
	}
	var before string
	const fingerprint = `SELECT md5((SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY id),'[]'::jsonb)::text FROM connection_health_question_answer_schedules s)||(SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY id),'[]'::jsonb)::text FROM connection_health_question_answer_schedule_executions e)||(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY singleton_key),'[]'::jsonb)::text FROM connection_health_question_answer_runtime_settings r))`
	if err = pool.QueryRow(ctx, fingerprint).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"/api/connection-health/question-answer-runtime-settings",
		"/api/connection-health/question-answer-schedule-limits",
		"/api/connection-health/question-answer-schedules",
		"/api/connection-health/question-answer-schedules/" + plan.ID,
		"/api/connection-health/question-answer-schedules/" + plan.ID + "/executions",
		"/api/connection-health/question-answer-schedule-executions/" + execution.ID,
		"/api/connection-health/question-answer-recent-summaries?targetId=sub2api:w:1",
	} {
		response := request(route, true)
		if response.Code != 200 {
			t.Fatalf("local %s => %d %s", route, response.Code, response.Body.String())
		}
		if unauthorized := request(route, false); unauthorized.Code != 401 {
			t.Fatalf("unauthenticated %s => %d", route, unauthorized.Code)
		}
	}
	var after string
	if err = pool.QueryRow(ctx, fingerprint).Scan(&after); err != nil || before != after {
		t.Fatalf("GET mutated persistent configuration/execution: before=%s after=%s err=%v", before, after, err)
	}
	if service.questionAnswerConcurrencyLoaded || service.questionAnswerConcurrencyLimit == settings.QuestionAnswerConcurrency || service.questionAnswerScheduleCtx != nil {
		t.Fatal("APIOnly GET loaded/applied singleton or started coordinator")
	}
	for route, want := range map[string]int{
		"/api/connection-health/question-answer-schedules?pageSize=101":                                   400,
		"/api/connection-health/question-answer-schedules?page=zero":                                      400,
		"/api/connection-health/question-answer-recent-summaries":                                         400,
		"/api/connection-health/question-answer-recent-summaries?targetId=malformed":                      400,
		"/api/connection-health/question-answer-recent-summaries?targetId=sub2api:other:1":                404,
		"/api/connection-health/question-answer-schedules/00000000-0000-4000-8000-000000000001":           404,
		"/api/connection-health/question-answer-schedule-executions/00000000-0000-4000-8000-000000000001": 404,
	} {
		if r := request(route, true); r.Code != want {
			t.Fatalf("%s => %d want%d: %s", route, r.Code, want, r.Body.String())
		}
	}
	values := url.Values{}
	for i := 0; i < 51; i++ {
		values.Add("targetId", "sub2api:w:"+strings.Repeat("a", i+1))
	}
	if r := request("/api/connection-health/question-answer-recent-summaries?"+values.Encode(), true); r.Code != 400 {
		t.Fatalf("51 targets accepted %d", r.Code)
	}
	// A broken local query must surface as 500, never as null successful facts.
	if _, err = pool.Exec(ctx, `DROP TABLE connection_health_question_answer_records`); err != nil {
		t.Fatal(err)
	}
	if r := request("/api/connection-health/question-answer-recent-summaries?targetId=sub2api:w:1", true); r.Code != 500 {
		t.Fatalf("storage failure became %d %s", r.Code, r.Body.String())
	}
}
