package httpserver

import (
	"context"
	"errors"
	"testing"
)

type groupCostShutdownRecorder struct {
	called bool
	ctx    context.Context
	err    error
}

func (r *groupCostShutdownRecorder) ShutdownGroupCostSampling(ctx context.Context) error {
	r.called = true
	r.ctx = ctx
	return r.err
}

func TestGroupCostShutdownBeforeNoLotteryWorkerReturn(t *testing.T) {
	for _, fail := range []bool{false, true} {
		stop := &groupCostShutdownRecorder{}
		if fail {
			stop.err = errors.New("fixture sampling stop deadline")
		}
		s := Server{upstreamService: stop}
		err := s.Shutdown(t.Context())
		if !stop.called || stop.ctx != t.Context() || !errors.Is(err, stop.err) {
			t.Fatal("sampling stop was skipped or its error/context was lost before lottery early return")
		}
	}
}
func TestGroupCostShutdownWithoutUpstreamService(t *testing.T) {
	if err := (&Server{}).Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
