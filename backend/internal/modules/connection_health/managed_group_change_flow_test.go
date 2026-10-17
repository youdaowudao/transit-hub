package connection_health

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type managedGroupFlowPlatform struct {
	fakePlatformGroupReader
	before, after     []upstream.Sub2APIImportGroup
	readErr, writeErr error
	firstErr          error
	firstPlatform     string
	reads, writes     int
	onReadback        func(context.Context)
}

func (f *managedGroupFlowPlatform) ReadSub2APIImportConfigurationContext(ctx context.Context, _ upstream.Session, id string) (upstream.Sub2APIImportConfiguration, error) {
	f.reads++
	if f.reads == 1 && f.firstErr != nil {
		return upstream.Sub2APIImportConfiguration{}, f.firstErr
	}
	platform := "openai"
	if f.firstPlatform != "" {
		platform = f.firstPlatform
	}
	groups := f.before
	if f.reads > 1 {
		if f.onReadback != nil {
			f.onReadback(ctx)
		}
		if f.readErr != nil {
			return upstream.Sub2APIImportConfiguration{}, f.readErr
		}
		groups = f.after
	}
	return upstream.Sub2APIImportConfiguration{ID: id, Name: "测试账号", Platform: platform, Groups: groups}, nil
}
func (f *managedGroupFlowPlatform) UpdateSub2APIAdminAccountGroupsContext(context.Context, upstream.Session, string, []string) error {
	f.writes++
	return f.writeErr
}
func managedGroupFlowService(f *managedGroupFlowPlatform) (*Service, *fakeRepository) {
	s, r := schedulableActionServiceWithSurvivor(boolPointer(true), &fakeTargetSchedulableActioner{})
	f.fakePlatformGroupReader = s.platformGroups.(fakePlatformGroupReader)
	f.before = []upstream.Sub2APIImportGroup{{ID: "g1", Name: "旧分组"}}
	s.platformGroups = f
	return s, r
}

func TestManagedGroupChangeFlowOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, want        string
		after             []upstream.Sub2APIImportGroup
		writeErr, readErr error
		reads             int
	}{
		{name: "read target", want: "changed", after: []upstream.Sub2APIImportGroup{{ID: "g1"}, {ID: "g2"}}, reads: 2},
		{name: "write error but target confirmed", want: "changed", after: []upstream.Sub2APIImportGroup{{ID: "g1"}, {ID: "g2"}}, writeErr: errors.New("write response lost"), reads: 2},
		{name: "read original", want: "unchanged", after: []upstream.Sub2APIImportGroup{{ID: "g1"}}, writeErr: errors.New("主站拒绝"), reads: 2},
		{name: "third set", want: "unknown", after: []upstream.Sub2APIImportGroup{{ID: "g3"}}, reads: 2},
		{name: "readback failed", want: "unknown", readErr: context.DeadlineExceeded, reads: 2},
		{name: "not sent", want: "unchanged", writeErr: &upstream.RequestError{MessageKey: upstream.ErrorInvalidFields, MutationOutcome: upstream.MutationNotSent}, reads: 1},
		{name: "confirmed rejected", want: "unchanged", writeErr: &upstream.RequestError{MessageKey: upstream.ErrorAuth, MutationOutcome: upstream.MutationConfirmedRejected}, reads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &managedGroupFlowPlatform{after: tc.after, writeErr: tc.writeErr, readErr: tc.readErr}
			s, _ := managedGroupFlowService(f)
			got, err := s.ChangeManagedSub2APIAccountGroups(t.Context(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, "1515", "openai", []string{"g1"}, []string{"g1", "g2"})
			if err != nil || got.Outcome != tc.want || f.reads != tc.reads || f.writes != 1 {
				t.Fatalf("outcome=%s reads=%d writes=%d err=%v", got.Outcome, f.reads, f.writes, err)
			}
			if tc.want == "unknown" && len(got.Groups) != 0 {
				t.Fatal("unknown result must not present unconfirmed memberships")
			}
			if !errors.Is(got.WriteError, tc.writeErr) {
				t.Fatal("write evidence lost")
			}
		})
	}
}

func TestManagedGroupChangeReadbackHoldsLockAndSurvivesBrowserCancel(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	finishReadback := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishReadback()
	f := &managedGroupFlowPlatform{after: []upstream.Sub2APIImportGroup{{ID: "g1"}, {ID: "g2"}}}
	f.onReadback = func(ctx context.Context) {
		close(entered)
		<-finish
		if ctx.Err() != nil {
			t.Error("browser cancellation reached independent readback")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("readback must have independent 10s deadline")
		}
	}
	s, _ := managedGroupFlowService(f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	flowDone := false
	defer func() {
		finishReadback()
		if !flowDone {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("group change goroutine did not finish cleanup")
			}
		}
	}()
	go func() {
		_, err := s.ChangeManagedSub2APIAccountGroups(ctx, "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, "1515", "openai", []string{"g1"}, []string{"g1", "g2"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("readback not reached")
	}
	cancel()
	waitCtx, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, release, err := s.acquireActionMutationLease(waitCtx, "user1", "ws1")
	stop()
	if err == nil {
		release()
		t.Error("workspace lock released before readback")
	}
	finishReadback()
	err = <-done
	flowDone = true
	if err != nil {
		t.Fatal(err)
	}
	_, release, err = s.acquireActionMutationLease(t.Context(), "user1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !reflect.DeepEqual(f.before, []upstream.Sub2APIImportGroup{{ID: "g1", Name: "旧分组"}}) {
		t.Fatal("fixture changed")
	}
}
