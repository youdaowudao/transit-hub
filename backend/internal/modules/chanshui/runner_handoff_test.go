package chanshui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func handoffPipe(t *testing.T) (*process, *os.File, *os.File) {
	t.Helper()
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		inputRead.Close()
		inputWrite.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { inputRead.Close(); inputWrite.Close(); outputRead.Close(); outputWrite.Close() })
	return &process{input: inputWrite, output: outputRead, reader: bufio.NewReader(outputRead)}, inputRead, outputWrite
}

func TestContinueProcessCancelledBeforeHandoffWritesNoCredentialBytes(t *testing.T) {
	p, input, _ := handoffPipe(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	delivered, err := continueProcess(ctx, p, &Credential{Key: "isolated-key"})
	if delivered || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-handoff cancellation was not confirmed unstarted: delivered=%v error=%v", delivered, err)
	}
	p.input.Close()
	raw, readErr := io.ReadAll(input)
	if readErr != nil || len(raw) != 0 {
		t.Fatalf("pre-handoff cancellation wrote %d credential bytes: %v", len(raw), readErr)
	}
}

func TestContinueProcessCancellationAfterDeliveryCannotClaimZeroBytes(t *testing.T) {
	p, input, _ := handoffPipe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	received := make(chan string, 1)
	go func() { raw, _ := bufio.NewReader(input).ReadBytes('\n'); cancel(); received <- string(raw) }()
	delivered, err := continueProcess(ctx, p, &Credential{Key: "isolated-key"})
	if err != nil || !delivered {
		t.Fatalf("completed handoff lost uncertainty boundary: delivered=%v error=%v", delivered, err)
	}
	if !strings.Contains(<-received, "isolated-key") || ctx.Err() == nil {
		t.Fatal("fixture did not cancel after the receiver got credentials")
	}
	// Runner.Start keeps operationResult=unknown after this delivery boundary,
	// including cancellation while waiting for its accepted event.
}

func TestStoppedControlCancellationWatcherKeepsRunningStreamOpen(t *testing.T) {
	p, _, output := handoffPipe(t)
	ctx, cancel := context.WithCancel(t.Context())
	stop := p.interruptControlIO(ctx)
	if !stop() {
		t.Fatal("uncancelled watcher was already running")
	}
	cancel()
	written := make(chan error, 1)
	go func() { _, err := output.Write([]byte("live\n")); written <- err }()
	line, err := readLine(p.reader)
	if err != nil || string(line) != "live\n" || <-written != nil {
		t.Fatalf("finishing startup closed the running stream: %q %v", line, err)
	}
}

func TestControlCancellationUnblocksPendingHandshakeRead(t *testing.T) {
	p, _, _ := handoffPipe(t)
	ctx, cancel := context.WithCancel(t.Context())
	stop := p.interruptControlIO(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { _, err := readLine(p.reader); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled pending handshake completed without an error")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not unblock the handshake read")
	}
}

func TestUnconfirmedPreparationExitRetainsQueryAndReportAdmission(t *testing.T) {
	p, _, _ := handoffPipe(t)
	p.done = make(chan struct{})
	close(p.done)
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	p.closeAfterConfirmedExit(func() { <-slot })
	if len(slot) != 1 {
		t.Fatal("an unconfirmed query process released its only admission slot")
	}

	s, f, _ := fixture(t)
	active := &activeTask{Owner: Owner{"user-a", "workspace-a"}, ReportID: "retained-unstarted", OperationResult: "not_started", CancelRequested: true, Job: &Job{Done: p.done, info: p.info}}
	s.active = active
	s.waiting = []*waitingTask{{Owner: active.Owner, Input: queueInput("b")}}
	f.fn = func(Envelope) (any, error) {
		t.Fatal("unconfirmed process reached report finalization")
		return nil, nil
	}
	if err := s.recoverExited(t.Context(), active); err == nil {
		t.Fatal("unconfirmed preparation exit was reported complete")
	}
	if s.active != active || len(s.waiting) != 1 {
		t.Fatal("unconfirmed preparation released report capacity or lost queue")
	}
}

func TestFailedLaunchConfirmsMissingOwnedUnitWithoutStrandingAdmission(t *testing.T) {
	p, _, _ := handoffPipe(t)
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { errRead.Close(); errWrite.Close() })
	p.stderr = errRead
	p.done = make(chan struct{})
	p.name = "known-preparation.service"
	manager, connection := managerFixture(t)
	p.manager = manager
	// The manager positively says our generated unit is absent. This is a known
	// unstarted launch failure, unlike an existing group whose exit is unknown.
	connection.exists = false
	started := time.Now()
	retained, err := p.failLaunch(problem(503, "startup", "模拟未创建的启动失败"))
	if err == nil || retained != nil || !p.info().Exited || connection.closed != 1 {
		t.Fatalf("missing unit was not reclaimed: retained=%v exited=%v closed=%d error=%v", retained != nil, p.info().Exited, connection.closed, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("known missing unit waited for the full stop timeout")
	}
}
