package connection_health

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const actionConfirmationWindow = 5 * time.Minute
const actionAccountRefreshInterval = 30 * time.Minute
const actionAccountListTimeout = 60 * time.Second

// The existing upstream reader optionally exposes its complete account inventory.
// A fake or an older reader can continue to implement only PlatformGroupReader.
type PlatformAccountLister interface {
	ListSub2APIAdminAccountsContext(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error)
}

type actionAccountConclusion struct {
	deleted     bool
	accountName string
}

// These maps belong to an immutable snapshot. Failed reads replace only the
// read record; the successful inventory evidence remains available.
type actionSweepView struct {
	lastReadAt        time.Time
	failures          int
	unavailable       bool
	snapshotStartedAt time.Time
	departed          map[string]bool
	conclusions       map[string]actionAccountConclusion
}

type actionSweepInventory struct {
	session           upstream.Session
	snapshotStartedAt time.Time
	visible           map[string]string
}

func (s *Service) actionTime() time.Time {
	if s.actionNow != nil {
		return s.actionNow()
	}
	return time.Now()
}

func scopedActionAccountID(targetID, workspace string) (string, bool) {
	parsed, ok := parseTargetID(targetID)
	if !ok || parsed.platform != string(upstream.PlatformSub2API) || parsed.adminAccountID != workspace {
		return "", false
	}
	id := strings.TrimSpace(parsed.accountID)
	return id, id != ""
}

// Both rows are cleared together, under the existing workspace transaction and
// row locks. A recent sender or a write after the list started protects the pair.
func clearDeletedAccountCheckpoints(pair *RemoteActionCheckpoints, scope RemoteActionScope, snapshotStartedAt, now time.Time) bool {
	if pair == nil || (pair.Priority == nil && pair.Target == nil) || snapshotStartedAt.IsZero() {
		return false
	}
	if _, ok := scopedActionAccountID(scope.TargetID, scope.AdminAccountID); !ok {
		return false
	}
	protected := func(userID, workspace, targetID string, updatedAt time.Time, pending bool, phase RemoteDispatchPhase) bool {
		return userID != scope.UserID || workspace != scope.AdminAccountID || targetID != scope.TargetID ||
			!updatedAt.Before(snapshotStartedAt) ||
			(pending && (phase == DispatchPrepared || phase == DispatchSending) && now.Sub(updatedAt) <= actionConfirmationWindow)
	}
	if p := pair.Priority; p != nil && protected(p.UserID, p.AdminAccountID, p.TargetID, p.UpdatedAt, priorityActionPending(p), p.PendingDispatchPhase) {
		return false
	}
	if p := pair.Target; p != nil && protected(p.UserID, p.AdminAccountID, p.TargetID, p.UpdatedAt, targetActionPending(p), p.PendingDispatchPhase) {
		return false
	}
	pair.Priority, pair.Target = nil, nil
	return true
}

// Inspect all aliases before changing any pair. One protected row keeps the
// complete account baseline, including records with a different raw target ID.
func clearDeletedAccountCheckpointAliases(aliases map[string]RemoteActionCheckpoints, scope RemoteActionScope, snapshotStartedAt, now time.Time) bool {
	id, valid := scopedActionAccountID(scope.TargetID, scope.AdminAccountID)
	if !valid || len(aliases) == 0 {
		return false
	}
	for targetID, pair := range aliases {
		aliasID, valid := scopedActionAccountID(targetID, scope.AdminAccountID)
		if !valid || aliasID != id {
			return false
		}
		aliasScope := RemoteActionScope{scope.UserID, scope.AdminAccountID, targetID}
		if !clearDeletedAccountCheckpoints(&pair, aliasScope, snapshotStartedAt, now) {
			return false
		}
	}
	for targetID := range aliases {
		aliases[targetID] = RemoteActionCheckpoints{}
	}
	return true
}

// The transaction evaluates this process-local read after acquiring row locks.
// It adds no network call or database field to the deletion protocol.
type actionCheckpointVisibilityKey struct{}

func actionCheckpointBecameVisible(ctx context.Context, scope RemoteActionScope) bool {
	visible, _ := ctx.Value(actionCheckpointVisibilityKey{}).(func(RemoteActionScope) bool)
	return visible != nil && visible(scope)
}

func cloneActionSweepView(previous *actionSweepView) *actionSweepView {
	view := &actionSweepView{departed: make(map[string]bool), conclusions: make(map[string]actionAccountConclusion)}
	if previous == nil {
		return view
	}
	*view = *previous
	view.departed = make(map[string]bool, len(previous.departed))
	view.conclusions = make(map[string]actionAccountConclusion, len(previous.conclusions))
	for id, departed := range previous.departed {
		view.departed[id] = departed
	}
	for id, conclusion := range previous.conclusions {
		view.conclusions[id] = conclusion
	}
	return view
}

func (s *Service) actionSweepViewFor(userID, workspace string) *actionSweepView {
	stored, ok := s.actionSweepViews.Load(priorityRuntimeLeaseKey(userID, workspace))
	if !ok {
		return nil
	}
	return stored.(*actionSweepView)
}

func (s *Service) updateActionSweepView(userID, workspace string, update func(*actionSweepView)) *actionSweepView {
	key := priorityRuntimeLeaseKey(userID, workspace)
	for {
		stored, exists := s.actionSweepViews.Load(key)
		var previous *actionSweepView
		if exists {
			previous = stored.(*actionSweepView)
		}
		view := cloneActionSweepView(previous)
		update(view)
		if !exists {
			if _, loaded := s.actionSweepViews.LoadOrStore(key, view); !loaded {
				return view
			}
		} else if s.actionSweepViews.CompareAndSwap(key, previous, view) {
			return view
		}
	}
}

func (s *Service) removeVisibleActionConclusions(userID, workspace string, visible map[string]string) {
	if s.actionSweepViewFor(userID, workspace) == nil {
		return
	}
	s.updateActionSweepView(userID, workspace, func(view *actionSweepView) {
		for id := range visible {
			delete(view.departed, id)
			delete(view.conclusions, id)
		}
	})
}

func actionListUnavailable(err error) bool {
	var requestErr *upstream.RequestError
	return errors.As(err, &requestErr) && (requestErr.MessageKey == upstream.ErrorInvalidResponse || requestErr.StatusCode == 404 || requestErr.StatusCode == 405 || requestErr.StatusCode == 501)
}

func (s *Service) sweepDeletedAccountCheckpoints(ctx context.Context, userID, workspace string, inventory actionSweepInventory) {
	if ctx.Err() != nil || inventory.session.Platform != upstream.PlatformSub2API {
		return
	}
	repository, supported := s.repo.(actionCheckpointRepository)
	if !supported {
		return
	}
	lister, supported := s.platformGroups.(PlatformAccountLister)
	if !supported {
		s.updateActionSweepView(userID, workspace, func(view *actionSweepView) { view.unavailable = true })
		return
	}
	priorities, err := s.repo.ListPrioritySyncStates(ctx, userID, workspace)
	if err != nil {
		log.Printf("[connection-health] account checkpoint sweep read failed workspace=%s", workspace)
		return
	}
	targets, err := s.repo.ListTargetActionStates(ctx, userID, workspace)
	if err != nil {
		log.Printf("[connection-health] account checkpoint sweep read failed workspace=%s", workspace)
		return
	}
	departed := make(map[string][]string)
	add := func(stateUser, stateWorkspace, targetID string) {
		if stateUser != userID || stateWorkspace != workspace {
			return
		}
		id, ok := scopedActionAccountID(targetID, workspace)
		if !ok {
			if isSub2APIActionTarget(targetID) {
				s.logInvisibleAction(RemoteActionScope{userID, workspace, targetID})
			}
			return
		}
		if _, visible := inventory.visible[id]; visible {
			return
		}
		for _, existing := range departed[id] {
			if existing == targetID {
				return
			}
		}
		departed[id] = append(departed[id], targetID)
	}
	for _, state := range priorities {
		add(state.UserID, state.AdminAccountID, state.TargetID)
	}
	for _, state := range targets {
		add(state.UserID, state.AdminAccountID, state.TargetID)
	}
	s.removeVisibleActionConclusions(userID, workspace, inventory.visible)
	if len(departed) == 0 {
		s.updateActionSweepView(userID, workspace, func(view *actionSweepView) {
			view.snapshotStartedAt = time.Time{}
			view.departed = make(map[string]bool)
			view.conclusions = make(map[string]actionAccountConclusion)
		})
		return
	}
	view := s.actionSweepViewFor(userID, workspace)
	now := s.actionTime()
	interval := actionAccountRefreshInterval
	for id := range departed {
		if view == nil || !view.departed[id] || view.conclusions[id].deleted {
			interval = actionConfirmationWindow
			break
		}
	}
	if view != nil && !view.lastReadAt.IsZero() && now.Sub(view.lastReadAt) < interval {
		// Only a successful prior list can authorize cleanup without a new read.
		// A failed read never deletes any checkpoint.
		if view.failures == 0 {
			s.clearConfirmedDeletedAccountCheckpoints(ctx, repository, userID, workspace, departed, view)
		}
		return
	}
	listStartedAt := now
	listCtx, cancel := context.WithTimeout(ctx, actionAccountListTimeout)
	accounts, err := lister.ListSub2APIAdminAccountsContext(listCtx, inventory.session)
	if err == nil {
		err = listCtx.Err()
	}
	cancel()
	if err == nil && len(accounts) == 0 {
		err = errors.New("empty account inventory")
	}
	if err != nil {
		s.updateActionSweepView(userID, workspace, func(next *actionSweepView) {
			next.lastReadAt = listStartedAt
			next.failures++
			next.unavailable = actionListUnavailable(err)
		})
		log.Printf("[connection-health] account checkpoint sweep list failed workspace=%s", workspace)
		return
	}
	listed := make(map[string]string, len(accounts))
	for _, account := range accounts {
		listed[strings.TrimSpace(account.ID)] = account.Name
	}
	view = s.updateActionSweepView(userID, workspace, func(next *actionSweepView) {
		next.lastReadAt, next.snapshotStartedAt = listStartedAt, listStartedAt
		next.failures, next.unavailable = 0, false
		next.departed = make(map[string]bool, len(departed))
		next.conclusions = make(map[string]actionAccountConclusion, len(departed))
		latest := s.completeActionInventory(userID, workspace)
		for id := range departed {
			if latest != nil {
				if _, visible := latest.names[id]; visible {
					continue
				}
			}
			name, exists := listed[id]
			next.departed[id] = true
			next.conclusions[id] = actionAccountConclusion{deleted: !exists, accountName: name}
		}
	})
	s.clearConfirmedDeletedAccountCheckpoints(ctx, repository, userID, workspace, departed, view)
}

func (s *Service) clearConfirmedDeletedAccountCheckpoints(ctx context.Context, repository actionCheckpointRepository, userID, workspace string, departed map[string][]string, view *actionSweepView) {
	cleared, skipped, failed := 0, 0, 0
	for id, targetIDs := range departed {
		conclusion, confirmed := view.conclusions[id]
		if !confirmed || !view.departed[id] || !conclusion.deleted {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// A complete grouped read newer than the list may have restored visibility.
		if latest := s.completeActionInventory(userID, workspace); latest != nil {
			if _, visible := latest.names[id]; visible {
				skipped++
				continue
			}
		}
		scope := RemoteActionScope{userID, workspace, buildTargetID("sub2api", workspace, id)}
		clearCtx := context.WithValue(ctx, actionCheckpointVisibilityKey{}, func(scope RemoteActionScope) bool {
			id, valid := scopedActionAccountID(scope.TargetID, scope.AdminAccountID)
			if !valid {
				return true
			}
			latest := s.completeActionInventory(scope.UserID, scope.AdminAccountID)
			if latest == nil {
				return false
			}
			_, visible := latest.names[id]
			return visible
		})
		removed, err := repository.ClearDeletedAccountCheckpoint(clearCtx, scope, view.snapshotStartedAt, s.actionTime())
		if err != nil {
			failed++
			log.Printf("[connection-health] account checkpoint sweep clear failed target_id=%s", scope.TargetID)
		} else if removed {
			cleared++
			for _, targetID := range targetIDs {
				s.clearInvisibleActionLog(RemoteActionScope{userID, workspace, targetID})
			}
			s.clearInvisibleActionLog(scope)
		} else {
			skipped++
		}
	}
	log.Printf("[connection-health] account checkpoint sweep workspace=%s cleared=%d skipped=%d failed=%d", workspace, cleared, skipped, failed)
}

func (s *Service) startDeletedAccountSweeps(ctx context.Context, cache adminInventoryCache) {
	if ctx.Err() != nil {
		return
	}
	for key, entry := range cache {
		if entry.err != nil || entry.inventory == nil || !adminInventoryComplete(*entry.inventory) || entry.inventory.session.Platform != upstream.PlatformSub2API {
			continue
		}
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		userID, workspace := parts[0], parts[1]
		if _, supported := s.repo.(actionCheckpointRepository); !supported {
			continue
		}
		if _, supported := s.platformGroups.(PlatformAccountLister); !supported {
			s.updateActionSweepView(userID, workspace, func(view *actionSweepView) { view.unavailable = true })
			continue
		}
		flightKey := priorityRuntimeLeaseKey(userID, workspace)
		if _, running := s.actionSweepFlights.LoadOrStore(flightKey, true); running {
			continue
		}
		// Copy on the scheduler thread: later safeguards mutate the tick cache.
		inventory := actionSweepInventory{session: entry.inventory.session, snapshotStartedAt: entry.inventory.snapshotStartedAt, visible: make(map[string]string)}
		for _, group := range entry.inventory.groups {
			for _, account := range group.accounts {
				inventory.visible[strings.TrimSpace(account.ID)] = account.Name
			}
		}
		done, err := s.registerActionDispatch()
		if err != nil {
			s.actionSweepFlights.Delete(flightKey)
			return
		}
		go func() {
			defer done()
			defer s.actionSweepFlights.Delete(flightKey)
			defer func() {
				if recover() != nil {
					log.Printf("[connection-health] account checkpoint sweep panic workspace=%s", workspace)
				}
			}()
			s.sweepDeletedAccountCheckpoints(ctx, userID, workspace, inventory)
		}()
	}
}

func canonicalActionLogScope(scope RemoteActionScope) RemoteActionScope {
	if id, valid := scopedActionAccountID(scope.TargetID, scope.AdminAccountID); valid {
		scope.TargetID = buildTargetID("sub2api", scope.AdminAccountID, id)
	}
	return scope
}

func (s *Service) clearInvisibleActionLog(scope RemoteActionScope) {
	s.actionInvisibleLogs.Delete(canonicalActionLogScope(scope))
}

func (s *Service) logInvisibleAction(scope RemoteActionScope) {
	key := canonicalActionLogScope(scope)
	now := s.actionTime()
	for {
		stored, loaded := s.actionInvisibleLogs.LoadOrStore(key, now)
		if !loaded {
			break
		}
		previous := stored.(time.Time)
		if now.Sub(previous) < time.Hour {
			return
		}
		if s.actionInvisibleLogs.CompareAndSwap(key, previous, now) {
			break
		}
	}
	log.Printf("[connection-health] %s target_id=%s", RemoteActionTargetNotVisible, scope.TargetID)
}
