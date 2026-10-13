package connection_health

import (
	"strings"
	"testing"
	"transithub/backend/internal/modules/upstream"
)

func TestModelControlV300ManualAdmissionAndConfirmationAllDecisions(t *testing.T) {
	for _, decision := range []string{"no_evidence", "awaiting_review", "insufficient", "close_recommended", "usable", "testing"} {
		for _, op := range []string{"close", "add", "restore"} {
			t.Run(decision+"/"+op, func(t *testing.T) {
				item := ModelControlItem{Decision: decision, Control: modelControlControl{Observation: modelControlObservation{State: "serving"}}}
				if op == "add" {
					item.Control.Observation.State = "not_provided"
				}
				if op == "restore" {
					item.Control.Observation.State = "closed"
					item.Control.ClosedEntries = map[string]string{"a": "A"}
				}
				err := modelControlManualAdmission(item, op)
				if (err == nil) != (decision != "testing") {
					t.Fatalf("manual admission=%v decision=%s", err, decision)
				}
				supported := op == "close" && decision == "close_recommended" || op != "close" && decision == "usable"
				if modelControlManualNeedsConfirmation(item, op) == supported {
					t.Fatal("quality support must determine second confirmation")
				}
				if op == "close" && decision != "close_recommended" && modelControlAdmission(item, op) == nil {
					t.Fatal("automatic strict admission was loosened")
				}
				item.Control.Pending = &modelControlPendingView{}
				if modelControlManualAdmission(item, op) == nil {
					t.Fatal("pending write admitted")
				}
				item.Control.Pending = nil
				item.Control.Observation.State = "account_missing"
				if modelControlManualAdmission(item, op) == nil {
					t.Fatal("missing account admitted")
				}
			})
		}
	}
}
func TestModelControlV300ManualConflictAndAccountBoundary(t *testing.T) {
	item := ModelControlItem{Decision: "insufficient", Control: modelControlControl{ClosedEntries: map[string]string{"a": "A"}, ConflictReason: "changed", Observation: modelControlObservation{State: "partially_closed"}}}
	if modelControlManualAdmission(item, "close") != nil {
		t.Fatal("remaining closure must be independent of restoration conflict")
	}
	if modelControlManualAdmission(item, "restore") == nil {
		t.Fatal("restore conflict ignored")
	}
	item.Control.ClosedEntries = nil
	item.Control.Observation.State = "not_provided"
	if modelControlManualAdmission(item, "add") == nil {
		t.Fatal("add conflict ignored")
	}
	if modelControlHumanAdmission(item, "close_account") == nil {
		t.Fatal("whole-account operation was expanded")
	}
	if modelControlManualAdmission(item, "unknown") == nil {
		t.Fatal("invalid operation admitted")
	}
}
func TestModelControlV300ExactQuestionBoundaryAndDuplicateSnapshots(t *testing.T) {
	questions := []ModelControlQuestionResult{{QuestionID: "a", QuestionName: "A", QuestionBody: "  X\r\nY  ", Correct: 1, Incorrect: 2}, {QuestionID: "b", QuestionName: "B", QuestionBody: "X\nY", Correct: 2, Incorrect: 1}, {QuestionID: "c", QuestionName: "C", QuestionBody: "X  Y", Correct: 3}}
	comparison, ok := compareModelControlQuestionAccuracy(questions[:2], 50)
	if !ok || comparison != 0 {
		t.Fatal("exact boundary was rounded", comparison, ok)
	}
	round := modelControlRound{Questions: questions}
	prepareModelControlRoundQuality(&round)
	if len(round.DuplicateQuestionIDs) != 2 || round.DuplicateQuestionIDs[0] != "a" || round.DuplicateQuestionIDs[1] != "b" {
		t.Fatal("duplicate snapshot normalization changed", round.DuplicateQuestionIDs)
	}
	if len(round.Questions) != 3 {
		t.Fatal("duplicate IDs must remain separate quality questions")
	}
	round.Questions[2].Correct = 0
	prepareModelControlRoundQuality(&round)
	if round.AccuracyPercent != nil {
		t.Fatal("zero-sample question was omitted")
	}
}

func TestModelControlV300SettingsValidationHasConcreteFieldReasons(t *testing.T) {
	for _, tc := range []struct {
		input    ModelControlSettings
		expected int64
		reason   string
	}{
		{ModelControlSettings{0, 3, 0}, 0, "AccuracyInvalid"}, {ModelControlSettings{101, 3, 0}, 0, "AccuracyInvalid"},
		{ModelControlSettings{50, 2, 0}, 0, "QuestionMinimumInvalid"}, {ModelControlSettings{50, 11, 0}, 0, "QuestionMinimumInvalid"},
		{ModelControlSettings{50, 3, 0}, -1, "SettingsVersionInvalid"},
	} {
		if err := validateModelControlSettings(tc.input, tc.expected); err == nil || err.Error() != modelControlError(tc.reason) {
			t.Fatalf("concrete field reason=%v want=%s", err, tc.reason)
		}
	}
	if err := validateModelControlSettings(ModelControlSettings{50, 3, 0}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestModelControlV300FailureHintsMaskSessionAndNeverInfer403Cause(t *testing.T) {
	session := upstream.Session{AccessToken: "fixture-access-token", AdminAPIKey: "fixture-admin-key", Cookie: "session=fixture-cookie"}
	err := &upstream.RequestError{StatusCode: 403, UpstreamMessage: "拒绝写入 fixture-access-token fixture-admin-key fixture-cookie"}
	message := modelControlFailureDetail("A", "写入", err, "unknown", session)
	for _, secret := range []string{session.AccessToken, session.AdminAPIKey, "fixture-cookie"} {
		if strings.Contains(message, secret) {
			t.Fatal("session credential escaped")
		}
	}
	if !strings.Contains(message, "HTTP 403") || !strings.Contains(message, "拒绝写入") || !strings.Contains(message, "不要直接重试") {
		t.Fatal("specific safe failure or unknown guidance lost", message)
	}
	err.UpstreamMessage = ""
	message = modelControlFailureDetail("A", "写入", err, "unknown", session)
	if strings.Contains(message, "余额") || strings.Contains(message, "权限不足") {
		t.Fatal("403 was guessed into a cause", message)
	}
	err.UpstreamMessage = "https://private.example.invalid/token?secret=fixture"
	message = modelControlSafeFailureCause(err, session)
	if strings.Contains(message, "private.example") || strings.Contains(message, "secret=") {
		t.Fatal("sensitive URL escaped")
	}
}

func TestModelControlV300ExecutionPreparationReadFailureNeverWrites(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.verify("1")
	preview := f.preview("1", "A", "close")
	f.failDetail = true
	code, result := f.execute("1", "A", "close", preview)
	message, _ := result["message"].(string)
	if code == 200 || !strings.Contains(message, "读取A") || !strings.Contains(message, "尚未发送模型开关") || !strings.Contains(message, "503") || !strings.Contains(message, "具体原因") {
		t.Fatalf("pre-write read failure=%d %q", code, message)
	}
	if f.writeCount() != 0 {
		t.Fatal("execution preparation failure dispatched a write")
	}
}
