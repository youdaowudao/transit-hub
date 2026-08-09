package connection_health

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

var errSub2APIMutationReadback = errors.New("sub2api mutation readback unavailable or mismatched")

func confirmedSub2APIStatusAction(active bool) string {
	if active {
		return RemoteActionSub2APIStatusActive
	}
	return RemoteActionSub2APIStatusInactive
}

// mutationCoordinatorForWrites keeps test-only Service literals usable while
// production services always use the repository-backed fencing implementation.
// A missing safety repository never silently becomes a database bypass: the
// process-only fallback is only for injected in-memory test doubles.
func (s *Service) mutationCoordinatorForWrites() *MutationCoordinator {
	s.mutationCoordinatorMu.Lock()
	defer s.mutationCoordinatorMu.Unlock()
	if s.mutationCoordinator != nil {
		return s.mutationCoordinator
	}
	var repo safetyMutationRepository
	if s.safetyRepo != nil {
		repo = s.safetyRepo
	} else if candidate, ok := s.repo.(safetyMutationRepository); ok {
		repo = candidate
	}
	s.mutationCoordinator = NewMutationCoordinator(repo)
	return s.mutationCoordinator
}

func (s *Service) mutationGeneration(ctx context.Context, userID, workspaceID, accountID string) (int64, error) {
	return s.mutationCoordinatorForWrites().Generation(ctx, MutationKey{
		UserID: userID, WorkspaceID: workspaceID, AccountID: accountID,
	})
}

func (s *Service) usesInMemoryMutationTestFallback() bool {
	if s.safetyRepo != nil {
		return false
	}
	_, repositoryBacked := s.repo.(safetyMutationRepository)
	return !repositoryBacked
}

func freshSub2APITargetError(targetID string) error {
	return fmt.Errorf("fresh Sub2API account read failed target=%s: %w", targetID, errSub2APIMutationReadback)
}

func (s *Service) freshSub2APITarget(
	ctx context.Context,
	session upstream.Session,
	workspaceID string,
	targetID string,
	accountID string,
) (AdminProbeTarget, upstream.AdminGroupAccountInfo, error) {
	if session.Platform != upstream.PlatformSub2API || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(accountID) == "" {
		return AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, freshSub2APITargetError(targetID)
	}
	target, account, found, accountsReadError, err := s.findAdminTarget(ctx, session, workspaceID, accountID)
	if err != nil || !found || accountsReadError || target.TargetID != targetID {
		if err != nil {
			return AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, errors.Join(freshSub2APITargetError(targetID), err)
		}
		return AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, freshSub2APITargetError(targetID)
	}
	return target, account, nil
}

// applyAutomaticTargetState is the only automatic status/weight write used by
// the target-action path. NewAPI keeps its existing channel action; Sub2API
// status is fenced by the canonical workspace/account mutation key and verified
// with a fresh account read before and after the remote request.
func (s *Service) applyAutomaticTargetState(
	ctx context.Context,
	userID string,
	workspaceID string,
	session upstream.Session,
	target AdminProbeTarget,
	weight *int,
	desiredStatus string,
	expectedGeneration int64,
) (string, error) {
	if target.Platform != string(upstream.PlatformSub2API) {
		if s.dispatcher == nil {
			return RemoteActionUnsupported, errors.New("remote action dispatcher unavailable")
		}
		return s.dispatcher.ApplyTargetState(ctx, session, target, weight, desiredStatus)
	}
	if s.dispatcher == nil {
		return RemoteActionUnsupported, errors.New("remote action dispatcher unavailable")
	}
	if s.usesInMemoryMutationTestFallback() {
		return s.dispatcher.ApplyTargetState(ctx, session, target, weight, desiredStatus)
	}
	key := MutationKey{UserID: userID, WorkspaceID: workspaceID, AccountID: target.AccountID}
	mutation, err := s.mutationCoordinatorForWrites().BeginAutomatic(ctx, key, true)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	defer mutation.Release()
	return s.applyAutomaticTargetStateUnderMutation(
		ctx, userID, workspaceID, session, target, weight, desiredStatus, expectedGeneration, mutation,
	)
}

func (s *Service) applyAutomaticTargetStateUnderMutation(
	ctx context.Context,
	userID string,
	workspaceID string,
	session upstream.Session,
	target AdminProbeTarget,
	weight *int,
	desiredStatus string,
	expectedGeneration int64,
	mutation *MutationSession,
) (string, error) {
	if target.Platform != string(upstream.PlatformSub2API) {
		if s.dispatcher == nil {
			return RemoteActionUnsupported, errors.New("remote action dispatcher unavailable")
		}
		return s.dispatcher.ApplyTargetState(ctx, session, target, weight, desiredStatus)
	}
	if s.dispatcher == nil {
		return RemoteActionUnsupported, errors.New("remote action dispatcher unavailable")
	}
	if s.usesInMemoryMutationTestFallback() {
		return s.dispatcher.ApplyTargetState(ctx, session, target, weight, desiredStatus)
	}
	if mutation == nil {
		return RemoteActionSafetyGateUnavailable, errors.New("automatic target mutation session unavailable")
	}
	if mutation.Generation != expectedGeneration {
		return RemoteActionSafetyStaleEpoch, errStaleMutation
	}
	if err := mutation.Validate(ctx); err != nil {
		return RemoteActionSafetyStaleEpoch, err
	}
	freshTarget, _, err := s.freshSub2APITarget(ctx, session, workspaceID, target.TargetID, target.AccountID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	if freshTarget.Schedulable != nil && !*freshTarget.Schedulable {
		return RemoteActionSkippedUpstreamScheduling, nil
	}
	currentStatus, known := strictSub2APIStatus(freshTarget.AccountStatus)
	if !known {
		return RemoteActionSafetyGateUnavailable, freshSub2APITargetError(target.TargetID)
	}
	desiredActive, desiredKnown := strictSub2APIStatus(desiredStatus)
	if !desiredKnown {
		return RemoteActionSafetyGateUnavailable, errors.New("unknown Sub2API target status")
	}
	if currentStatus == desiredActive {
		return "", nil
	}
	if err := mutation.Validate(ctx); err != nil {
		return RemoteActionSafetyStaleEpoch, err
	}
	action, writeErr := s.dispatcher.ApplyTargetState(ctx, session, freshTarget, weight, desiredStatus)
	mutationErr := mutation.Validate(ctx)
	readbackTarget, _, err := s.freshSub2APITarget(ctx, session, workspaceID, target.TargetID, target.AccountID)
	if err != nil {
		if writeErr != nil {
			return action, errors.Join(writeErr, mutationErr, err)
		}
		return action, errors.Join(mutationErr, err)
	}
	readbackActive, readbackKnown := strictSub2APIStatus(readbackTarget.AccountStatus)
	if !readbackKnown || readbackActive != desiredActive {
		if writeErr != nil {
			return action, errors.Join(writeErr, mutationErr, errSub2APIMutationReadback)
		}
		return action, errors.Join(mutationErr, errSub2APIMutationReadback)
	}
	s.invalidateAdminInventorySnapshot(userID, workspaceID)
	if mutationErr != nil {
		return action, mutationErr
	}
	return confirmedSub2APIStatusAction(desiredActive), nil
}

// updateAutomaticTargetPriority is the fenced priority counterpart. It is
// deliberately separate from status so a priority write cannot overwrite the
// status field on the upstream bulk-update endpoint.
func (s *Service) updateAutomaticTargetPriority(
	ctx context.Context,
	userID string,
	workspaceID string,
	session upstream.Session,
	targetID string,
	accountID string,
	priority int,
	expectedGeneration int64,
	expectedPriorityValues ...int,
) error {
	if session.Platform != upstream.PlatformSub2API {
		return s.updateAdminTargetPriority(ctx, session, accountID, priority)
	}
	if s.priorityActions == nil {
		return errors.New("priority actioner unavailable")
	}
	if s.usesInMemoryMutationTestFallback() {
		return s.updateAdminTargetPriority(ctx, session, accountID, priority)
	}
	key := MutationKey{UserID: userID, WorkspaceID: workspaceID, AccountID: accountID}
	mutation, err := s.mutationCoordinatorForWrites().BeginAutomatic(ctx, key, true)
	if err != nil {
		return err
	}
	defer mutation.Release()
	if mutation.Generation != expectedGeneration {
		return errStaleMutation
	}
	if err := mutation.Validate(ctx); err != nil {
		return err
	}
	freshTarget, account, err := s.freshSub2APITarget(ctx, session, workspaceID, targetID, accountID)
	if err != nil {
		return err
	}
	if len(expectedPriorityValues) > 0 && (account.Priority == nil || *account.Priority != expectedPriorityValues[0]) {
		return &upstream.PriorityCompareAndSetError{
			TargetID: targetID, ExpectedPriority: expectedPriorityValues[0], ActualPriority: account.Priority,
		}
	}
	if account.Priority != nil && *account.Priority == priority {
		return nil
	}
	if err := mutation.Validate(ctx); err != nil {
		return err
	}
	writeErr := s.updateAdminTargetPriorityWithBeforeWrite(ctx, session, freshTarget.AccountID, priority, func() error {
		return mutation.Validate(ctx)
	})
	mutationErr := mutation.Validate(ctx)
	_, readback, err := s.freshSub2APITarget(ctx, session, workspaceID, targetID, accountID)
	if err != nil || readback.Priority == nil || *readback.Priority != priority {
		if err != nil {
			return errors.Join(writeErr, mutationErr, errSub2APIMutationReadback, err)
		}
		return errors.Join(writeErr, mutationErr, errSub2APIMutationReadback)
	}
	if mutationErr != nil {
		return mutationErr
	}
	return nil
}

// applyManualConnectionSub2APIStatus protects the legacy real_connections
// command without changing the NewAPI channel path.
func (s *Service) applyManualConnectionSub2APIStatus(ctx context.Context, conn my_sites.RealConnection, desiredStatus string) (string, error) {
	if s.sites == nil || s.mySites == nil {
		return RemoteActionSafetyGateUnavailable, errors.New("manual Sub2API mutation dependencies unavailable")
	}
	site, err := s.sites.GetSite(ctx, conn.UpstreamSiteID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	if site == nil {
		return RemoteActionSafetyGateUnavailable, errors.New("manual Sub2API site unavailable")
	}
	if site.Platform != upstream.PlatformSub2API {
		return "", nil
	}
	session, err := s.mySites.RequireSession(ctx, conn.UserID, conn.WorkspaceAdminAccountID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	targetID := buildTargetID(string(upstream.PlatformSub2API), conn.WorkspaceAdminAccountID, conn.AdminAccountID)
	key := MutationKey{UserID: conn.UserID, WorkspaceID: conn.WorkspaceAdminAccountID, AccountID: conn.AdminAccountID}
	mutation, err := s.mutationCoordinatorForWrites().BeginManual(ctx, key)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	defer mutation.Release()
	freshTarget, _, err := s.freshSub2APITarget(ctx, session, conn.WorkspaceAdminAccountID, targetID, conn.AdminAccountID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	if err := mutation.Validate(ctx); err != nil {
		return RemoteActionSafetyStaleEpoch, err
	}
	desiredActive, desiredKnown := strictSub2APIStatus(desiredStatus)
	if !desiredKnown {
		return RemoteActionSafetyGateUnavailable, errors.New("unknown manual Sub2API status")
	}
	action, writeErr := s.dispatcher.ApplyTargetState(ctx, session, freshTarget, nil, desiredStatus)
	mutationErr := mutation.Validate(ctx)
	readback, _, err := s.freshSub2APITarget(ctx, session, conn.WorkspaceAdminAccountID, targetID, conn.AdminAccountID)
	if err != nil {
		if writeErr != nil {
			return action, errors.Join(writeErr, mutationErr, err)
		}
		return action, errors.Join(mutationErr, err)
	}
	active, known := strictSub2APIStatus(readback.AccountStatus)
	if !known || active != desiredActive {
		if writeErr != nil {
			return action, errors.Join(writeErr, mutationErr, errSub2APIMutationReadback)
		}
		return action, errors.Join(mutationErr, errSub2APIMutationReadback)
	}
	s.invalidateAdminInventorySnapshot(conn.UserID, conn.WorkspaceAdminAccountID)
	if mutationErr != nil {
		return action, mutationErr
	}
	return confirmedSub2APIStatusAction(desiredActive), nil
}
