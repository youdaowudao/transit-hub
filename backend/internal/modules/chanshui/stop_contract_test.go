package chanshui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestKnownRunningStopDoesNotDependOnOrdinaryQuerySlot(t *testing.T) {
	s, f, _ := fixture(t)
	o := Owner{"user-a", "workspace-a"}
	id := strings.Repeat("a", 32)
	var stopped atomic.Bool
	s.active = &activeTask{Owner: o, ReportID: id, Job: &Job{stop: func(context.Context) error { stopped.Store(true); return nil }, info: func() ExitInfo { return ExitInfo{} }}}
	f.fn = func(Envelope) (any, error) { return nil, problem(429, "query", "另一个检测查询正在处理") }
	value, err := s.Cancel(t.Context(), o, id)
	if err != nil || !stopped.Load() {
		t.Fatalf("stop was lost behind a busy query: stopped=%v value=%v error=%v", stopped.Load(), value, err)
	}
}
