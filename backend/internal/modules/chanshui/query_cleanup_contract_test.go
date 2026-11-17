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

// The injected boundary is an already-monitored process. Done has closed, but
// exit can still be unknown after a failed systemd cleanup. The complete Query
// protocol, its real control/data pipes and its admission slot still run here.
func queryCleanupFixture(t *testing.T, stage string, exited bool, releaseError error) (*Runner, *process, *fakeManagerConnection, func(context.Context, string, Envelope, uint64, int) (*process, error)) {
	t.Helper()
	p, input, output := handoffPipe(t)
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stderr = errRead
	p.done = make(chan struct{})
	close(p.done)
	p.monitored = true
	p.setExit(ExitInfo{Exited: exited}, nil)
	manager, connection := managerFixture(t)
	connection.exists = false
	connection.closeErr = releaseError
	p.manager = manager
	p.name = "query-cleanup-fixture.service"
	runner := NewRunner(RunnerConfig{})
	finished := make(chan struct{})
	if stage == "continue" || stage == "launch" {
		p.input.Close()
		close(finished)
	} else {
		go func() {
			defer close(finished)
			defer output.Close()
			_, _ = bufio.NewReader(input).ReadBytes('\n')
			switch stage {
			case "header-read":
				return
			case "header-invalid":
				_, _ = io.WriteString(output, "invalid header\n")
			case "header-error":
				_, _ = io.WriteString(output, "{\"v\":1,\"ok\":false,\"status\":409,\"length\":0,\"error\":{\"message\":\"保存题目失败：隔离磁盘满\",\"step\":\"fixture\",\"operationResult\":\"not_started\"}}\n")
			case "body-partial", "body-complete":
				_, _ = io.WriteString(output, "{\"v\":1,\"ok\":true,\"status\":200,\"length\":2,\"contentType\":\"application/json\"}\n{}")
			}
		}()
	}
	t.Cleanup(func() {
		p.setExit(ExitInfo{Exited: true}, nil)
		_ = p.close()
		input.Close()
		output.Close()
		errRead.Close()
		errWrite.Close()
		for len(runner.querySlot) > 0 {
			<-runner.querySlot
		}
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("query fixture pipe worker remains")
		}
	})
	launch := func(context.Context, string, Envelope, uint64, int) (*process, error) {
		if stage == "launch" {
			return p, problem(503, "startup", "隔离启动失败")
		}
		return p, nil
	}
	return runner, p, connection, launch
}

func TestQueryAllFailurePathsRetainSlotUntilProcessExitConfirmed(t *testing.T) {
	for _, stage := range []string{"launch", "continue", "header-read", "header-invalid", "header-error", "body-partial", "body-complete"} {
		for _, exited := range []bool{false, true} {
			name := stage + "/unconfirmed"
			if exited {
				name = stage + "/confirmed"
			}
			t.Run(name, func(t *testing.T) {
				runner, expectedProcess, connection, launch := queryCleanupFixture(t, stage, exited, nil)
				response, err := runner.query(t.Context(), Envelope{Operation: "read-report"}, nil, false, launch)
				if strings.HasPrefix(stage, "body-") {
					if err != nil || response == nil {
						t.Fatalf("body fixture did not enter actual Query body path: %v", err)
					}
					if len(runner.querySlot) != 1 {
						t.Fatal("query body did not retain its slot while open")
					}
					if stage == "body-complete" {
						raw, readErr := io.ReadAll(response.Body)
						if readErr != nil || string(raw) != "{}" {
							t.Fatalf("query body not read completely: %q %v", raw, readErr)
						}
					}
					err = response.Body.Close()
					if exited && err != nil {
						t.Fatalf("confirmed body exit rejected: %v", err)
					}
				} else if err == nil || response != nil {
					t.Fatalf("query failure was hidden: response=%v error=%v", response, err)
				}
				if stage == "header-error" && !strings.Contains(err.Error(), "隔离磁盘满") {
					t.Fatalf("upstream specific error was lost: %v", err)
				}
				if exited {
					if len(runner.querySlot) != 0 || connection.closed != 1 {
						t.Fatalf("confirmed exit did not release query resources: slot=%d connectionsClosed=%d", len(runner.querySlot), connection.closed)
					}
				} else {
					if len(runner.querySlot) != 1 || connection.closed != 0 {
						t.Fatalf("unconfirmed exit released query capacity/connection: slot=%d connectionsClosed=%d", len(runner.querySlot), connection.closed)
					}
					held := <-runner.querySlot
					if held.process != expectedProcess || held.process.manager != expectedProcess.manager {
						t.Fatal("retained query slot does not own the same unconfirmed process and control connection")
					}
					runner.querySlot <- held
					if err == nil || strings.Contains(err.Error(), "程序已停止") || !strings.Contains(err.Error(), "尚未确认") {
						t.Fatalf("unknown exit was falsely reported stopped or lacked guidance: %v", err)
					}
					_, nextErr := runner.query(t.Context(), Envelope{Operation: "read-report"}, nil, false, func(context.Context, string, Envelope, uint64, int) (*process, error) {
						t.Fatal("next query started while old process exit was unknown")
						return nil, nil
					})
					if nextErr == nil || asProblem(nextErr).Status != 429 {
						t.Fatalf("unconfirmed query allowed another helper: %v", nextErr)
					}
				}
			})
		}
	}
}

func TestQueryConfirmedExitReleasesSlotEvenWhenConnectionCleanupFails(t *testing.T) {
	for _, stage := range []string{"launch", "continue", "header-read", "header-invalid", "header-error", "body-complete"} {
		t.Run(stage, func(t *testing.T) {
			runner, _, connection, launch := queryCleanupFixture(t, stage, true, errors.New("隔离管理连接释放失败"))
			response, err := runner.query(t.Context(), Envelope{Operation: "read-report"}, nil, false, launch)
			if response != nil {
				if _, readErr := io.ReadAll(response.Body); readErr != nil {
					t.Fatal(readErr)
				}
				err = response.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "隔离管理连接释放失败") {
				t.Fatalf("confirmed exit cleanup failure lost diagnostic: %v", err)
			}
			if len(runner.querySlot) != 0 || connection.closed != 1 {
				t.Fatalf("cleanup-only failure stranded query capacity: slot=%d closed=%d", len(runner.querySlot), connection.closed)
			}
		})
	}
}

type queryJSONCloseFault struct {
	io.Reader
	closeErr error
}

func (b queryJSONCloseFault) Close() error { return b.closeErr }

type queryJSONFaultExecutor struct {
	fakeExecutor
	closeErr error
}

func (f *queryJSONFaultExecutor) Query(context.Context, Envelope, *Credential, bool) (*QueryResponse, error) {
	body := `{"reportId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"done"}`
	return &QueryResponse{Status: 200, ContentType: "application/json", Length: int64(len(body)), Body: queryJSONCloseFault{Reader: strings.NewReader(body), closeErr: f.closeErr}}, nil
}

func TestQueryJSONCleanupFailurePreservesCauseWithoutFalseExitClaim(t *testing.T) {
	for _, cause := range []string{"查询程序退出尚未确认，后续查询已暂停，请检查检测运行诊断", "查询程序已退出，但管理连接释放失败"} {
		t.Run(cause, func(t *testing.T) {
			executor := &queryJSONFaultExecutor{closeErr: errors.New(cause)}
			s := &Service{runner: executor}
			_, err := s.queryJSON(t.Context(), "read-report", Owner{"isolated", "workspace"}, map[string]any{"reportId": strings.Repeat("a", 32)}, nil, false)
			if err == nil || !strings.Contains(err.Error(), "辅助程序清理未完成") || !strings.Contains(err.Error(), cause) || strings.Contains(err.Error(), "未能确认辅助程序退出") {
				t.Fatalf("cleanup failure changed its actual cause or falsely denied confirmed exit: %v", err)
			}
			if executor.starts.Load() != 0 {
				t.Fatal("query cleanup failure started a paid report")
			}
		})
	}
}
