package connection_health

import (
	"context"
	"errors"
	"sort"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type ModelControlVerifyError struct {
	TargetID  string `json:"targetId"`
	ReasonKey string `json:"reasonKey"`
}
type ModelControlVerifyResult struct {
	Items  []ModelControlItem        `json:"items"`
	Errors []ModelControlVerifyError `json:"errors"`
}
type modelControlCompleteInventoryReader interface {
	ListSub2APIAdminAccountsContext(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error)
}

func (s *Service) resolveMissingModelControlAccount(ctx context.Context, user, ws, target, account string, session upstream.Session, at time.Time) error {
	leased := ctx
	release := func() {}
	var err error
	if handle := actionLeaseFromContext(ctx); handle != nil {
		if !modelControlHandleValid(handle) {
			return ErrRemoteActionLeaseLost
		}
	} else {
		var acquired bool
		leased, release, acquired, err = s.acquireActionTargetLease(ctx, target, false)
		if err != nil {
			return err
		}
		if !acquired {
			return modelControlConflict("Processing", nil)
		}
		defer release()
		// A missing response read before taking the lease cannot authorize clearing
		// a newer operation; the account may have been recreated in the meantime.
		at = time.Now().UTC()
		_, readErr := s.modelControlActions.ReadSub2APIModelControlAccountContext(leased, session, account)
		if !errors.Is(readErr, upstream.ErrSub2APIModelControlAccountMissing) {
			return requestError(modelControlError("AccountReadFailed"))
		}
	}
	reader, ok := s.platformGroups.(modelControlCompleteInventoryReader)
	if !ok {
		return requestError(modelControlError("AccountReadFailed"))
	}
	accounts, err := reader.ListSub2APIAdminAccountsContext(leased, session)
	if err != nil {
		return requestError(modelControlError("AccountReadFailed"))
	}
	for _, a := range accounts {
		if a.ID == account {
			return requestError(modelControlError("AccountReadFailed"))
		}
	}
	err = s.modelControls.mutateModelControlOwnership(leased, user, ws, target, false, "finalize", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
		events := []ModelControlEvent{}
		for _, o := range objects {
			if o.Pending != nil || o.UnconfirmedClose != nil || o.Observation.State != "account_missing" {
				events = append(events, modelControlNewEvent(user, ws, target, o.ModelName, "account_missing_resolved", nil, map[string]any{"closedEntries": o.ClosedEntries, "pending": o.Pending, "unconfirmedClose": o.UnconfirmedClose}))
				if o.Pending != nil {
					o.LastPendingID = o.Pending.ID
				}
				o.Pending = nil
				o.UnconfirmedClose = nil
			}
			o.Observation = modelControlObservation{State: "account_missing", Sources: []modelSourceCount{}, ReasonKey: modelControlError("AccountMissing")}
			o.LastAttempt = nil
			o.observationAt = &at
			o.updateAttempt = true
		}
		return events, nil
	})
	var commit *modelControlCommitError
	if errors.As(err, &commit) {
		fresh, committed, readErr := s.confirmModelControlCommit(ctx, user, ws, target, commit)
		if readErr != nil {
			return readErr
		}
		if !committed {
			return modelControlConflict("Storage", nil)
		}
		for _, object := range fresh {
			if object.TargetID == target && (object.Pending != nil || object.UnconfirmedClose != nil || object.Observation.State != "account_missing") {
				return modelControlConflict("Storage", nil)
			}
		}
		return nil
	}
	return err
}
func (s *Service) VerifyModelControl(ctx context.Context, user string, targetIDs []string) (ModelControlVerifyResult, error) {
	result := ModelControlVerifyResult{Items: []ModelControlItem{}, Errors: []ModelControlVerifyError{}}
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return result, err
	}
	if len(targetIDs) == 0 || len(targetIDs) > 50 {
		return result, requestError(ErrorRequest)
	}
	ws, err := s.modelControlScope(ctx, user, "")
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, target := range targetIDs {
		if err = questionAnswerScheduleOwnedTarget(user, ws, target); err != nil {
			return result, err
		}
		if !seen[target] {
			seen[target] = true
			ids = append(ids, target)
		}
	}
	sort.Strings(ids)
	if s.modelControlActions == nil {
		return result, requestError(modelControlError("Unsupported"))
	}
	inventory, inventoryErr := s.loadAdminInventory(ctx, user, ws, adminInventoryCache{})
	if inventory == nil {
		inventory = &adminWorkspaceInventory{}
	}
	hp, reserved, reservationErr := s.modelControlReservations(ctx, user, ws)
	if reservationErr != nil {
		return result, requestError(modelControlError("Storage"))
	}
	type verifiedAccount struct {
		detail upstream.Sub2APIModelControlAccount
		at     time.Time
	}
	verified := map[string]verifiedAccount{}
	for _, target := range ids {
		reason := ""
		leased, release, acquired, err := s.acquireActionTargetLease(ctx, target, false)
		if err != nil {
			reason = modelControlError("Storage")
		} else if !acquired {
			reason = modelControlError("Processing")
		} else {
			func() {
				defer release()
				session, _, account, err := s.resolveManualSession(leased, user, target)
				if err != nil {
					reason = modelControlError("AccountReadFailed")
					return
				}
				at := time.Now().UTC()
				detail, err := s.modelControlActions.ReadSub2APIModelControlAccountContext(leased, session, account)
				if errors.Is(err, upstream.ErrSub2APIModelControlAccountMissing) {
					if err = s.resolveMissingModelControlAccount(leased, user, ws, target, account, session, at); err != nil {
						reason = modelControlError("AccountReadFailed")
						if errors.Is(err, requestError(modelControlError("StorageUnknown"))) {
							reason = modelControlError("StorageUnknown")
						}
					}
					return
				}
				if err != nil {
					reason = modelControlError("AccountReadFailed")
					return
				}
				objects, err := s.modelControls.ListModelControlTargets(leased, user, ws)
				if err != nil {
					reason = modelControlError("Storage")
					return
				}
				for _, o := range objects {
					p := o.Pending
					if o.TargetID == target && p != nil && p.Phase == "sending" && p.Receipt == "" && p.SendStartedAt != nil && time.Since(*p.SendStartedAt) < 30*time.Second {
						reason = modelControlError("Processing")
						return
					}
				}
				outcomes := map[string]string{}
				operations := map[string]string{}
				entries := map[string][]ModelControlEntry{}
				err = s.modelControls.mutateModelControlOwnership(leased, user, ws, target, false, "finalize", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
					events := []ModelControlEvent{}
					for _, o := range objects {
						events = append(events, modelControlReconcileLate(o, detail.ModelMapping, now)...)
						pending := o.Pending
						attr := attributeModelControlEntries(detail.ModelMapping, o.ClosedEntries, pending)
						if pending != nil && pending.Operation == "close" {
							newClosed := false
							for k := range attr.ClosedEntries {
								if _, ok := o.ClosedEntries[k]; !ok {
									newClosed = true
								}
							}
							if newClosed {
								modelControlApplyBasis(o, pending.Basis, pending.ActorUserID, now)
							}
							if pending.Phase == "sending" && pending.Receipt == "" && pending.SendStartedAt != nil && attr.Outcome != "succeeded" {
								uncertain := map[string]string{}
								for k, v := range pending.Entries {
									if actual, ok := detail.ModelMapping[k]; ok && actual == v {
										uncertain[k] = v
									}
								}
								if len(uncertain) > 0 {
									o.UnconfirmedClose = &modelControlUnconfirmed{uncertain, pending.Basis, *pending.SendStartedAt, pending.ActorUserID}
								}
							}
						}
						o.ClosedEntries = attr.ClosedEntries
						modelControlClearClosedBasis(o)
						if pending != nil {
							o.LastPendingID = pending.ID
							o.Pending = nil
							outcomes[o.ID] = attr.Outcome
							operations[o.ID] = pending.Operation
							entries[o.ID] = attr.Entries
							events = append(events, modelControlNewEvent(user, ws, target, o.ModelName, "pending_resolved", pending.Basis, map[string]any{"operation": pending.Operation, "outcome": attr.Outcome, "entries": attr.Entries}))
						}
						if len(attr.ManualEntries) > 0 {
							events = append(events, modelControlNewEvent(user, ws, target, o.ModelName, "manual_takeover", nil, attr.ManualEntries))
						}
					}
					return events, nil
				})
				if err != nil {
					var commit *modelControlCommitError
					if !errors.As(err, &commit) {
						reason = modelControlError("LeaseLost")
						return
					}
					_, committed, readErr := s.confirmModelControlCommit(ctx, user, ws, target, commit)
					if readErr != nil {
						reason = modelControlError("StorageUnknown")
						return
					}
					if !committed {
						reason = modelControlError("Storage")
						return
					}
				}
				observeCtx, observeCancel := modelControlStorageContext(leased)
				defer observeCancel()
				objects, err = s.modelControls.ListModelControlTargets(observeCtx, user, ws)
				if err != nil {
					reason = modelControlError("Storage")
					if observeCtx.Err() != nil {
						reason = modelControlError("StorageUnknown")
					}
					return
				}
				for _, o := range objects {
					if o.TargetID != target {
						continue
					}
					o.AccountName = detail.Name
					o.Observation.State, o.Observation.ReasonKey = modelControlObservedState(detail, o)
					o.Observation.AccountStatus = detail.Status
					o.Observation.AccountSchedulable = detail.Schedulable
					restore := planModelRestore(detail, o.ClosedEntries)
					if len(o.ClosedEntries) > 0 {
						o.ConflictReason = restore.ReasonKey
					} else {
						o.ConflictReason = ""
					}
					keys := o.ClosedEntries
					if o.Observation.State == "last_model" {
						keys = detail.ModelMapping
					}
					if len(keys) > 0 {
						o.Observation.Sources = countModelSources(*inventory, detail.AdminGroupAccountInfo, keys, hp, reserved, time.Now())
						if inventoryErr != nil {
							for n := range o.Observation.Sources {
								o.Observation.Sources[n].Count = nil
								o.Observation.Sources[n].OK = false
							}
						}
					} else {
						o.Observation.Sources = []modelSourceCount{}
					}
					setAttempt := false
					if outcome, ok := outcomes[o.ID]; ok {
						setAttempt = true
						o.LastAttempt = nil
						if outcome != "succeeded" {
							o.LastAttempt = &modelControlAttempt{Operation: operations[o.ID], Outcome: outcome, ReasonKey: modelControlError("AccountReadFailed"), Entries: entries[o.ID], Groups: o.Observation.Sources, At: at}
						}
					}
					if _, err = s.modelControls.observeModelControlTarget(observeCtx, o, at, setAttempt, ""); err != nil {
						reason = modelControlError("Storage")
						if observeCtx.Err() != nil {
							reason = modelControlError("StorageUnknown")
						}
						return
					}
				}
				verified[target] = verifiedAccount{detail, at}
			}()
		}
		if reason != "" {
			result.Errors = append(result.Errors, ModelControlVerifyError{target, reason})
		}
	}

	// Attribution across the batch can release reservations for other accounts.
	// Recalculate displayed counts from the final local state and the one inventory.
	finalCtx, finalCancel := modelControlStorageContext(ctx)
	defer finalCancel()
	if len(verified) > 0 {
		hp, reserved, err = s.modelControlReservations(finalCtx, user, ws)
		if err != nil {
			return result, requestError(modelControlError("StorageUnknown"))
		}
		objects, readErr := s.modelControls.ListModelControlTargets(finalCtx, user, ws)
		if readErr != nil {
			return result, requestError(modelControlError("StorageUnknown"))
		}
		for _, o := range objects {
			account, ok := verified[o.TargetID]
			if !ok {
				continue
			}
			keys := o.ClosedEntries
			if o.Observation.State == "last_model" {
				keys = account.detail.ModelMapping
			}
			o.Observation.Sources = []modelSourceCount{}
			if len(keys) > 0 {
				o.Observation.Sources = countModelSources(*inventory, account.detail.AdminGroupAccountInfo, keys, hp, reserved, time.Now())
				if inventoryErr != nil {
					for n := range o.Observation.Sources {
						o.Observation.Sources[n].Count = nil
						o.Observation.Sources[n].OK = false
					}
				}
			}
			if _, err = s.modelControls.observeModelControlTarget(finalCtx, o, account.at, false, ""); err != nil {
				return result, requestError(modelControlError("StorageUnknown"))
			}
		}
	}
	items, _, err := s.modelControlItems(finalCtx, user, ws)
	if err != nil || finalCtx.Err() != nil {
		return result, requestError(modelControlError("StorageUnknown"))
	}
	for _, item := range items {
		if seen[item.TargetID] {
			result.Items = append(result.Items, item)
		}
	}
	return result, nil
}
