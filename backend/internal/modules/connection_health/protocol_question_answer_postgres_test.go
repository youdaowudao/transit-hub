package connection_health

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The database has already completed COMMIT when this reader drops its reply.
// BuildFrontend wraps the decoded protocol stream and preserves the configured
// authentication/TLS. No proxy process or listening socket is created.
type protocolCommitReplyLossReader struct {
	r     io.Reader
	armed *atomic.Bool
	lost  *atomic.Bool
	tail  []byte
}

func (r *protocolCommitReplyLossReader) Read(buffer []byte) (int, error) {
	n, err := r.r.Read(buffer)
	if !r.armed.Load() {
		r.tail = nil
		return n, err
	}
	data := append(r.tail, buffer[:n]...)
	commandComplete := []byte{'C', 0, 0, 0, 11, 'C', 'O', 'M', 'M', 'I', 'T', 0}
	if bytes.Contains(data, commandComplete) && r.armed.CompareAndSwap(true, false) {
		r.lost.Store(true)
		r.tail = nil
		return 0, io.ErrUnexpectedEOF
	}
	if len(data) > len(commandComplete) {
		data = data[len(data)-len(commandComplete):]
	}
	r.tail = append([]byte(nil), data...)
	return n, err
}

type protocolPostgresFinalizationFailure struct {
	*Repository
	fail atomic.Bool
}

func (r *protocolPostgresFinalizationFailure) StopPendingQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, kind string) (bool, error) {
	if r.fail.Load() {
		return true, errors.New("injected storage finalization failure")
	}
	return r.Repository.StopPendingQuestionAnswerBatch(ctx, user, target, batch, status, kind)
}
func (r *protocolPostgresFinalizationFailure) FinalizeQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, kind string) (bool, error) {
	if r.fail.Load() {
		return true, errors.New("injected storage finalization failure")
	}
	return r.Repository.FinalizeQuestionAnswerBatch(ctx, user, target, batch, status, kind)
}

func TestProtocolPostgresQuestionAnswerCommitReplyLostNeverDispatches(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	question, err := r.CreateTestQuestion(ctx, "user1", "commit-lost", "fixture prompt", []string{})
	if err != nil {
		t.Fatal(err)
	}
	var armed, lost atomic.Bool
	config := pool.Config().Copy()
	config.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
		return pgproto3.NewFrontend(&protocolCommitReplyLossReader{r: reader, armed: &armed, lost: &lost}, writer)
	}
	faultPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open isolated reply-loss test pool")
	}
	t.Cleanup(faultPool.Close)
	repo := &protocolPostgresFinalizationFailure{Repository: NewRepository(faultPool)}
	repo.fail.Store(true)
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", repo, newFakeRepository())
	var calls atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/models" {
			calls.Add(1)
			return nil, errors.New("model dispatch forbidden during uncertain commit")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"}]}`)), Request: req}, nil
	})
	service.modelDiscovery.client.Transport = transport
	service.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		repo.fail.Store(false)
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Errorf("shutdown cleanup: %v", err)
		}
	})
	armed.Store(true)
	const target = "sub2api:ws1:acc-a"
	_, err = service.StartQuestionAnswerBatch(ctx, "user1", target, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{question.ID}})
	var uncertain *uncertainQuestionAnswerCreateError
	if !lost.Load() || !errors.As(err, &uncertain) {
		t.Fatalf("real commit reply loss not classified uncertain: dropped=%v err=%v", lost.Load(), err)
	}
	var batch, protocol, status string
	if err := pool.QueryRow(ctx, `SELECT batch_id,request_protocol,status FROM connection_health_question_answer_records WHERE user_id='user1' AND target_id=$1`, target).Scan(&batch, &protocol, &status); err != nil {
		t.Fatal(err)
	}
	if protocol != "chat_completions" || status != "pending" {
		t.Fatalf("committed snapshot was not preserved: protocol=%s status=%s", protocol, status)
	}
	latest, err := service.LatestQuestionAnswerBatch(ctx, "user1", target)
	if err != nil {
		t.Fatal(err)
	}
	if latest.BatchID != batch {
		t.Errorf("reopened latest did not identify committed batch: %s", latest.BatchID)
	}
	service.questionAnswerMu.Lock()
	retained := service.questionAnswerRuns[questionAnswerRunKey("user1", target)] != nil
	service.questionAnswerMu.Unlock()
	if !retained || calls.Load() != 0 {
		t.Errorf("uncertain commit ownership=%v model HTTP=%d", retained, calls.Load())
	}
	repo.fail.Store(false)
	if _, err := service.StopQuestionAnswerBatch(ctx, "user1", target, batch); err != nil {
		t.Fatalf("explicit cleanup retry: %v", err)
	}
	service.questionAnswerMu.Lock()
	retained = service.questionAnswerRuns[questionAnswerRunKey("user1", target)] != nil
	service.questionAnswerMu.Unlock()
	if retained || calls.Load() != 0 {
		t.Errorf("final reservation=%v model HTTP=%d", retained, calls.Load())
	}
	if err := pool.QueryRow(ctx, `SELECT request_protocol,status FROM connection_health_question_answer_records WHERE user_id='user1' AND batch_id=$1`, batch).Scan(&protocol, &status); err != nil {
		t.Fatal(err)
	}
	if protocol != "chat_completions" || status != "failed" {
		t.Errorf("cleanup deleted/changed committed snapshot: protocol=%s status=%s", protocol, status)
	}
}
