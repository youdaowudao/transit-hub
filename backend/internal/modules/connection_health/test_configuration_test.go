package connection_health

import (
	"context"
	"testing"
	"time"
)

func (f *fakeRepository) CommitTargetProbe(ctx context.Context, input TargetProbeCommit) (TargetProbeCommitResult, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	if f.testConfigurationErr != nil {
		return TargetProbeCommitResult{}, f.testConfigurationErr
	}
	configs := []GroupTestConfig{}
	for _, config := range f.testConfigurations {
		if config.UserID == input.UserID && config.AdminAccountID == input.AdminAccountID {
			configs = append(configs, config)
		}
	}
	configuration := ResolveGroupTestConfiguration("sub2api", input.Target.TestMemberships, input.Target.InventoryComplete, configs)
	current, err := f.GetState(ctx, input.Target.TargetID, input.ModelName)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	result, event := buildProbeCommitResult(input, current, configuration)
	if f.insertEventErr != nil {
		return TargetProbeCommitResult{}, f.insertEventErr
	}
	if result.Disposition != "stale" {
		if err := f.UpsertState(ctx, result.State); err != nil {
			return TargetProbeCommitResult{}, err
		}
	}
	if err := f.InsertEvent(ctx, event); err != nil {
		return TargetProbeCommitResult{}, err
	}
	return result, nil
}

func (f *fakeRepository) DecorateProbeEventAction(_ context.Context, userID, workspace, eventID, action, groupID, groupName string) error {
	for i := range f.events {
		event := &f.events[i]
		if event.ID == eventID && event.UserID == userID && event.AdminAccountID == workspace && event.ProbeDisposition == "applied" {
			event.RemoteAction = action
			if !targetActionAuditOnly(action) {
				state := f.states[event.ConnectionID][event.ModelName]
				if state.LastAppliedProbeAt != nil && state.LastAppliedProbeAt.Equal(event.CreatedAt) {
					state.LastRemoteAction = action
					f.states[event.ConnectionID][event.ModelName] = state
				}
			}

			if groupID != "" {
				event.AdminGroupID = groupID
				event.OwnGroupName = groupName
			}
		}
	}
	return nil
}

func (f *fakeRepository) ListGroupTestConfigurations(_ context.Context, userID, workspace string) ([]GroupTestConfig, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	if f.testConfigurationErr != nil {
		return nil, f.testConfigurationErr
	}
	configs := []GroupTestConfig{}
	for _, config := range f.testConfigurations {
		if config.UserID == userID && config.AdminAccountID == workspace {
			configs = append(configs, config)
		}
	}
	return configs, nil
}

func (f *fakeRepository) SaveGroupTestConfiguration(_ context.Context, userID, workspace, groupID string, configuration *GroupTestConfiguration) error {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	if f.testConfigurationErr != nil {
		return f.testConfigurationErr
	}
	if configuration != nil && !validGroupTestConfiguration(*configuration) {
		return requestError(ErrorRequest)
	}
	configs := []GroupTestConfig{}
	for _, config := range f.testConfigurations {
		if config.UserID != userID || config.AdminAccountID != workspace || config.AdminGroupID != groupID {
			configs = append(configs, config)
		}
	}
	if configuration != nil {
		configs = append(configs, GroupTestConfig{userID, workspace, groupID, configuration.Protocol, configuration.ProbeTimeoutSeconds})
	}
	f.testConfigurations = configs
	return nil
}

func TestGroupTestConfigurationValidation(t *testing.T) {
	for _, seconds := range []int{5, 10, 30, 120} {
		for _, protocol := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses} {
			if !validGroupTestConfiguration(GroupTestConfiguration{protocol, seconds}) {
				t.Fatalf("rejected %s/%d", protocol, seconds)
			}
		}
	}
	for _, config := range []GroupTestConfiguration{{TestProtocolResponses, 4}, {TestProtocolResponses, 121}, {"messages", 30}, {"", 10}} {
		if validGroupTestConfiguration(config) {
			t.Fatalf("accepted invalid %+v", config)
		}
	}
}

func TestGroupTestConfigurationIsolationAndClear(t *testing.T) {
	repo := newFakeRepository()
	ctx := t.Context()
	for _, scope := range [][2]string{{"u", "w1"}, {"u", "w2"}, {"v", "w1"}} {
		if err := repo.SaveGroupTestConfiguration(ctx, scope[0], scope[1], "1", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SaveGroupTestConfiguration(ctx, "u", "w1", "1", nil); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]string{{"u", "w1"}, {"u", "w2"}, {"v", "w1"}} {
		configs, err := repo.ListGroupTestConfigurations(ctx, scope[0], scope[1])
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if scope == [2]string{"u", "w1"} {
			want = 0
		}
		if len(configs) != want {
			t.Fatalf("scope %+v count=%d", scope, len(configs))
		}
	}
}

func (f *fakeRepository) ListLatestProbeAttemptEventsByWorkspace(_ context.Context, userID, workspace string, since time.Time) ([]ConnectionHealthEvent, error) {
	result := []ConnectionHealthEvent{}
	for _, event := range f.events {
		if event.UserID == userID && event.AdminAccountID == workspace && !event.CreatedAt.Before(since) && (event.ProbeDisposition == "applied" || event.ProbeDisposition == "invalid") {
			result = append(result, event)
		}
	}
	return result, nil
}
