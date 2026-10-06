package connection_health

import "context"

type ruleSettingsRepository interface {
	GetWorkspaceHealthSettings(context.Context, string, string) (WorkspaceHealthSettings, error)
	ListRulePresets(context.Context, string, string) ([]RulePreset, error)
	SaveRulePreset(context.Context, RulePreset) (RulePreset, error)
	DeleteRulePreset(context.Context, string, string, string) error
	ApplyRulePresetToAll(context.Context, string, string, string) (WorkspaceHealthSettings, error)
	SwitchWorkspaceRule(context.Context, string, string, string) (WorkspaceHealthSettings, error)
	SaveWorkspaceProbeConcurrency(context.Context, string, string, int, int64) (WorkspaceHealthSettings, error)
}

func (s *Service) workspaceHealthSettings(ctx context.Context, userID, workspace string) (WorkspaceHealthSettings, error) {
	if repo, ok := s.repo.(interface {
		GetWorkspaceHealthSettings(context.Context, string, string) (WorkspaceHealthSettings, error)
	}); ok {
		return repo.GetWorkspaceHealthSettings(ctx, userID, workspace)
	}
	return defaultWorkspaceHealthSettings(userID, workspace), nil
}
func (s *Service) WorkspaceHealthSettings(ctx context.Context, userID string) (WorkspaceHealthSettings, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	return s.workspaceHealthSettings(ctx, userID, workspace)
}
func (s *Service) ListRulePresets(ctx context.Context, userID string) ([]RulePreset, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, err
	}
	repo, ok := s.repo.(ruleSettingsRepository)
	if !ok {
		return nil, requestError(ErrorRequest)
	}
	return repo.ListRulePresets(ctx, userID, workspace)
}
func (s *Service) SaveRulePreset(ctx context.Context, userID string, p RulePreset) (RulePreset, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return p, err
	}
	repo, ok := s.repo.(ruleSettingsRepository)
	if !ok {
		return p, requestError(ErrorRequest)
	}
	p.UserID = userID
	p.AdminAccountID = workspace
	return repo.SaveRulePreset(ctx, p)
}
func (s *Service) DeleteRulePreset(ctx context.Context, userID, id string) error {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return err
	}
	repo, ok := s.repo.(ruleSettingsRepository)
	if !ok {
		return requestError(ErrorRequest)
	}
	return repo.DeleteRulePreset(ctx, userID, workspace, id)
}
func (s *Service) ApplyRulePresetToAll(ctx context.Context, userID, id string) (WorkspaceHealthSettings, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	repo, ok := s.repo.(ruleSettingsRepository)
	if !ok {
		return WorkspaceHealthSettings{}, requestError(ErrorRequest)
	}
	return repo.ApplyRulePresetToAll(ctx, userID, workspace, id)
}
func (s *Service) SwitchWorkspaceRule(ctx context.Context, userID, rule string) (WorkspaceHealthSettings, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	repo, ok := s.repo.(ruleSettingsRepository)
	if !ok {
		return WorkspaceHealthSettings{}, requestError(ErrorRequest)
	}
	// Rule conversion has no remote side effects. The next scheduled round
	// consumes the converted state through its existing reconciliation paths.
	return repo.SwitchWorkspaceRule(ctx, userID, workspace, rule)
}
func (s *Service) SaveWorkspaceProbeConcurrency(ctx context.Context, userID string, cap int, version int64) (WorkspaceHealthSettings, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	repo, ok := s.repo.(ruleSettingsRepository)
	if !ok {
		return WorkspaceHealthSettings{}, requestError(ErrorRequest)
	}
	result, err := repo.SaveWorkspaceProbeConcurrency(ctx, userID, workspace, cap, version)
	if err == nil {
		s.applyWorkspaceProbeSettings(result)
	}
	return result, err
}
