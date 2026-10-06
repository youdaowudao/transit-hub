package connection_health

import "context"

type workspaceRuleSnapshotKey struct{}
type workspaceRuleSnapshot struct {
	all      bool
	settings map[string]WorkspaceHealthSettings
}

// Capture before reading policies, including workspaces whose enabled-policy
// result is empty. Their old unmanaged decision still needs a generation guard.
func (s *Service) captureAllWorkspaceRules(ctx context.Context) (context.Context, error) {
	reader, ok := s.repo.(interface {
		ListWorkspaceHealthSettings(context.Context) ([]WorkspaceHealthSettings, error)
	})
	if !ok {
		return ctx, nil
	}
	rows, err := reader.ListWorkspaceHealthSettings(ctx)
	if err != nil {
		return ctx, err
	}
	snapshot := workspaceRuleSnapshot{all: true, settings: map[string]WorkspaceHealthSettings{}}
	for _, settings := range rows {
		snapshot.settings[settings.UserID+"|"+settings.AdminAccountID] = settings
	}
	return context.WithValue(ctx, workspaceRuleSnapshotKey{}, snapshot), nil
}

func (s *Service) captureWorkspaceRules(ctx context.Context, userID, workspace string) (context.Context, error) {
	settings, err := s.workspaceHealthSettings(ctx, userID, workspace)
	if err != nil {
		return ctx, err
	}
	snapshot := workspaceRuleSnapshot{settings: map[string]WorkspaceHealthSettings{userID + "|" + workspace: settings}}
	return context.WithValue(ctx, workspaceRuleSnapshotKey{}, snapshot), nil
}

func (s *Service) capturedWorkspaceRules(ctx context.Context, userID, workspace string, policies []Policy) (WorkspaceHealthSettings, error) {
	if snapshot, ok := ctx.Value(workspaceRuleSnapshotKey{}).(workspaceRuleSnapshot); ok {
		if settings, found := snapshot.settings[userID+"|"+workspace]; found {
			return settings, nil
		}
		if snapshot.all {
			// A row created after the initial read must not turn an absent old
			// configuration into permission to act with its new generation.
			return defaultWorkspaceHealthSettings(userID, workspace), nil
		}
	}
	for _, policy := range policies {
		if policy.UserID == userID && policy.AdminAccountID == workspace && policy.RuleVersion != "" {
			settings := defaultWorkspaceHealthSettings(userID, workspace)
			settings.RuleVersion, settings.ConfigGeneration = policy.RuleVersion, policy.ConfigGeneration
			return settings, nil
		}
	}
	return s.workspaceHealthSettings(ctx, userID, workspace)
}
