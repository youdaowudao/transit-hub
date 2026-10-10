package connection_health

import (
	"errors"
	"os"
	"testing"
)

func TestModelControlV2911SettingsDefaultSaveConflictAndEvent(t *testing.T) {
	f := newC3REDFixture(t)
	defaults := f.expect("GET", "model-control/settings", nil, 200)
	if defaults["minAccuracyPercent"] != float64(50) || defaults["minJudgedAnswers"] != float64(3) || defaults["version"] != float64(0) {
		t.Fatal(defaults)
	}
	for _, input := range []map[string]any{
		{"minAccuracyPercent": 0, "minJudgedAnswers": 3, "expectedVersion": 0},
		{"minAccuracyPercent": 101, "minJudgedAnswers": 3, "expectedVersion": 0},
		{"minAccuracyPercent": 50, "minJudgedAnswers": 0, "expectedVersion": 0},
		{"minAccuracyPercent": 50, "minJudgedAnswers": 51, "expectedVersion": 0},
		{"minAccuracyPercent": 50, "minJudgedAnswers": 3, "expectedVersion": -1},
		{"minAccuracyPercent": 50, "minJudgedAnswers": 3},
	} {
		f.expect("PUT", "model-control/settings", input, 400)
	}
	input := map[string]any{"minAccuracyPercent": 70, "minJudgedAnswers": 4, "expectedVersion": 0}
	saved := f.expect("PUT", "model-control/settings", input, 200)
	if saved["version"] != float64(1) {
		t.Fatal(saved)
	}
	conflict := f.expect("PUT", "model-control/settings", input, 409)
	current := conflict["current"].(map[string]any)
	if current["version"] != float64(1) || current["minAccuracyPercent"] != float64(70) {
		t.Fatal(conflict)
	}
	input["expectedVersion"] = 1
	f.expect("PUT", "model-control/settings", input, 200)
	events, _, err := f.service.modelControls.ListModelControlEvents(t.Context(), c3REDUser, c3REDWorkspace, "", "", 1)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for _, e := range events {
		detail := e.Detail.(map[string]any)
		if e.EventType != "settings_saved" || e.TargetID != "" || e.ModelName != "" || detail["minAccuracyPercent"] != float64(70) || detail["minJudgedAnswers"] != float64(4) || len(detail) != 2 {
			t.Fatal(e)
		}
	}
	if f.writeCount() != 0 {
		t.Fatal("settings changed upstream")
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		f.expect(method, "model-control/rules", nil, 404)
	}
}

func TestModelControlV2911PostgresSettingsMigrationBackfillsOnlyConsistentWorkspaces(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO connection_health_model_control_rules(id,user_id,admin_account_id,model_name,min_accuracy_percent,min_judged_answers,include_manual,include_scheduled) VALUES('one','u','consistent','A',72,5,false,true),('two','u','consistent','B',72,5,true,false),('three','u','mixed','A',60,3,true,true),('four','u','mixed','B',70,3,true,true)`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../database/migrations/000036_connection_health_model_control_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ws   string
		want ModelControlSettings
	}{{"consistent", ModelControlSettings{72, 5, 1}}, {"mixed", ModelControlSettings{50, 3, 0}}, {"empty", ModelControlSettings{50, 3, 0}}} {
		got, err := repo.GetModelControlSettings(t.Context(), "u", tc.ws)
		if err != nil || got != tc.want {
			t.Fatalf("%s=%+v %v", tc.ws, got, err)
		}
	}
	saved, err := repo.SaveModelControlSettings(t.Context(), "u", "consistent", ModelControlSettings{65, 6, 0}, 1)
	if err != nil || saved.Version != 2 {
		t.Fatal(saved, err)
	}
	if _, err = pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetModelControlSettings(t.Context(), "u", "consistent")
	if err != nil || got != saved {
		t.Fatal("migration overwrote user settings", got, err)
	}
	_, err = repo.SaveModelControlSettings(t.Context(), "u", "empty", ModelControlSettings{65, 6, 0}, 1)
	var conflict *ModelControlConflictError
	if !errors.As(err, &conflict) || conflict.Current != (ModelControlSettings{50, 3, 0}) {
		t.Fatal("missing current default", err)
	}
}
