package connection_health

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestQuestionAnswerStoragePostgresConfirmFullOwnershipAndTerminalIdempotence(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	insert := func(id string, status QuestionAnswerStatus) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,started_at) VALUES($1,'storage-user','storage-target','storage-batch','m','q','Q','body',$2,now())`, id, status); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id string) QuestionAnswerRecord {
		t.Helper()
		records, err := repo.ListQuestionAnswerBatch(ctx, "storage-user", "storage-target", "storage-batch")
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.ID == id {
				return record
			}
		}
		t.Fatal("record missing")
		return QuestionAnswerRecord{}
	}
	insert("owned", QuestionAnswerRunning)
	completion := questionAnswerTestCompletion(QuestionAnswerSucceeded, "original complete answer", "")
	completion.Diagnostics = newQuestionAnswerProgress(time.Now()).snapshot("owned").Diagnostics
	for _, terminal := range []bool{false, true} {
		if terminal {
			if ok, err := repo.CompleteQuestionAnswer(ctx, "storage-user", "storage-batch", "owned", completion); err != nil || !ok {
				t.Fatalf("normal completion: ok=%v err=%v", ok, err)
			}
		}
		before := read("owned")
		for _, broken := range []string{"user", "target", "batch", "record"} {
			user, target, batch, id := "storage-user", "storage-target", "storage-batch", "owned"
			switch broken {
			case "user":
				user = "another-user"
			case "target":
				target = "another-target"
			case "batch":
				batch = "another-batch"
			case "record":
				id = "missing"
			}
			if ok, err := repo.ConfirmOrSaveQuestionAnswerCompletion(ctx, user, target, batch, id, completion, false); err == nil || ok {
				t.Fatalf("%s terminal=%v accepted foreign ownership: ok=%v err=%v", broken, terminal, ok, err)
			}
			if after := read("owned"); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s changed a foreign or terminal row", broken)
			}
		}
	}
	insert("confirm-running", QuestionAnswerRunning)
	if ok, err := repo.ConfirmOrSaveQuestionAnswerCompletion(ctx, "storage-user", "storage-target", "storage-batch", "confirm-running", completion, false); err != nil || !ok {
		t.Fatalf("running confirmation save: %v %v", ok, err)
	}
	confirmed := read("confirm-running")
	if confirmed.Status != QuestionAnswerSucceeded || confirmed.AnswerBody != completion.AnswerBody || confirmed.AnswerJudgment == nil || *confirmed.AnswerJudgment != *completion.AnswerJudgment || confirmed.Diagnostics == nil {
		t.Fatal("running confirmation lost answer, judgment or diagnostic columns")
	}
	before := read("owned")
	judged, err := repo.SetQuestionAnswerJudgment(ctx, "storage-user", "storage-target", "owned", QuestionAnswerIncorrect, before.UpdatedAt)
	if err != nil || judged == nil {
		t.Fatalf("manual judgment: %v", err)
	}
	before = read("owned")
	replacement := questionAnswerTestCompletion(QuestionAnswerFailed, "", QuestionAnswerErrorStorage)
	if ok, err := repo.ConfirmOrSaveQuestionAnswerCompletion(ctx, "storage-user", "storage-target", "storage-batch", "owned", replacement, false); err != nil || !ok {
		t.Fatalf("terminal confirmation: ok=%v err=%v", ok, err)
	}
	if after := read("owned"); !reflect.DeepEqual(after, before) || after.AnswerBody != "original complete answer" || after.JudgmentSource == nil || *after.JudgmentSource != QuestionAnswerJudgmentManual || after.Diagnostics == nil {
		t.Fatalf("confirmation rewrote saved answer, manual judgment, diagnostics or timestamps: %+v", after)
	}
	// A legacy NULL diagnostic is also authoritative once the row is terminal.
	insert("legacy", QuestionAnswerRunning)
	old := questionAnswerTestCompletion(QuestionAnswerSucceeded, "legacy answer", "")
	if ok, err := repo.CompleteQuestionAnswer(ctx, "storage-user", "storage-batch", "legacy", old); err != nil || !ok {
		t.Fatal(err)
	}
	before = read("legacy")
	if ok, err := repo.ConfirmOrSaveQuestionAnswerCompletion(ctx, "storage-user", "storage-target", "storage-batch", "legacy", completion, false); err != nil || !ok {
		t.Fatal(err)
	}
	if after := read("legacy"); !reflect.DeepEqual(after, before) || after.Diagnostics != nil {
		t.Fatal("terminal confirmation retrofitted a legacy diagnostic")
	}
}

func TestQuestionAnswerStoragePostgresPendingGuardAndNormalCompleteRemainDistinct(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for index, broken := range []string{"valid-failed", "valid-cancelled", "normal-complete", "not-allowed", "selected-time", "failure-step", "http-started", "successful"} {
		t.Run(broken, func(t *testing.T) {
			id := fmt.Sprintf("pending-%d", index)
			if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status) VALUES($1,'storage-user','storage-target',$1,'m','q','Q','body','pending')`, id); err != nil {
				t.Fatal(err)
			}
			completion := questionAnswerTestCompletion(QuestionAnswerFailed, "", QuestionAnswerErrorStorage)
			completion.Diagnostics = newQuestionAnswerProgress(time.Now()).snapshot(id).Diagnostics
			completion.Diagnostics.FailureStep = "starting"
			completion.Diagnostics.LocalError = "start statement timed out"
			allow := true
			switch broken {
			case "valid-cancelled":
				completion.Status = QuestionAnswerCancelled
				completion.ErrorType = ""
			case "not-allowed":
				allow = false
			case "selected-time":
				completion.Diagnostics.SelectedAt = nil
			case "failure-step":
				completion.Diagnostics.FailureStep = "request"
			case "http-started":
				stamp := time.Now()
				completion.Diagnostics.RequestStartedAt = &stamp
			case "successful":
				completion = questionAnswerTestCompletion(QuestionAnswerSucceeded, "not sent", "")
				completion.Diagnostics = newQuestionAnswerProgress(time.Now()).snapshot(id).Diagnostics
				completion.Diagnostics.FailureStep = "starting"
			}
			var ok bool
			var err error
			if broken == "normal-complete" {
				ok, err = repo.CompleteQuestionAnswer(ctx, "storage-user", id, id, completion)
				if ok || err != nil {
					t.Fatalf("normal Complete accepted pending or changed its old result: %v %v", ok, err)
				}
			} else {
				ok, err = repo.ConfirmOrSaveQuestionAnswerCompletion(ctx, "storage-user", "storage-target", id, id, completion, allow)
			}
			valid := broken == "valid-failed" || broken == "valid-cancelled"
			if ok != valid || valid && err != nil || !valid && broken != "normal-complete" && err == nil {
				t.Fatalf("pending evidence accepted=%v valid=%v err=%v", ok, valid, err)
			}
			records, readErr := repo.ListQuestionAnswerBatch(ctx, "storage-user", "storage-target", id)
			if readErr != nil || len(records) != 1 {
				t.Fatal(readErr)
			}
			if valid {
				if records[0].Status != completion.Status || records[0].Diagnostics == nil || records[0].Diagnostics.LocalError != "start statement timed out" || records[0].Diagnostics.RequestStartedAt != nil {
					t.Fatalf("selected pre-HTTP result was not preserved: %+v", records[0])
				}
			} else if records[0].Status != QuestionAnswerPending || records[0].AnswerBody != "" || records[0].Diagnostics != nil {
				t.Fatal("invalid pending write modified row")
			}
		})
	}
	// A missing current row can never acknowledge a completion.
	if ok, err := repo.ConfirmOrSaveQuestionAnswerCompletion(ctx, "storage-user", "storage-target", "missing", "missing", questionAnswerTestCompletion(QuestionAnswerFailed, "", QuestionAnswerErrorStorage), false); err == nil || ok {
		t.Fatal("missing row was treated as saved")
	}
}
