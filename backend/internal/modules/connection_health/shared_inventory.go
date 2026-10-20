package connection_health

import (
	"context"
	"errors"
	"log"
	"reflect"

	"transithub/backend/internal/modules/upstream"
)

var errScheduledInventoryPanic = errors.New("定时探活读取主站分组和账号时发生内部错误，本次未执行探活，请查看服务日志后重试")

type adminInventoryRead struct {
	session   upstream.Session
	done      chan struct{}
	inventory adminWorkspaceInventory
	err       error
}

// Only unfinished scheduler reads are shared. Manual and locked mutation reads
// retain their original independent entry points.
func (s *Service) sharedWorkspaceInventory(ctx context.Context, userID, adminAccountID string, session upstream.Session) (adminWorkspaceInventory, error) {
	if err := ctx.Err(); err != nil {
		return adminWorkspaceInventory{}, err
	}
	key := userID + "|" + adminAccountID
	s.schedulerMu.Lock()
	if s.schedulerClosed {
		s.schedulerMu.Unlock()
		return adminWorkspaceInventory{}, context.Canceled
	}
	if s.schedulerCtx == nil {
		// Normal dispatch already has its scheduler root. Direct internal job
		// callers still need a root independent of any one account's lease.
		s.schedulerCtx, s.schedulerCancel = context.WithCancel(context.Background())
	}
	root := s.schedulerCtx
	if err := root.Err(); err != nil {
		s.schedulerMu.Unlock()
		return adminWorkspaceInventory{}, err
	}
	var read *adminInventoryRead
	for _, active := range s.schedulerInventoryReads[key] {
		if reflect.DeepEqual(active.session, session) {
			read = active
			break
		}
	}
	if read == nil {
		read = &adminInventoryRead{session: session, done: make(chan struct{})}
		read.session.ExpiresAt = cloneInventoryPointer(session.ExpiresAt)
		if s.schedulerInventoryReads == nil {
			s.schedulerInventoryReads = make(map[string][]*adminInventoryRead)
		}
		s.schedulerInventoryReads[key] = append(s.schedulerInventoryReads[key], read)
		// Admission and Add are protected by the same lock as Shutdown's
		// closed flag, so its wait also covers detached shared reads.
		s.schedulerJobs.Add(1)
		go s.runSharedInventoryRead(root, key, read)
	}
	s.schedulerMu.Unlock()
	select {
	case <-ctx.Done():
		return adminWorkspaceInventory{}, ctx.Err()
	case <-read.done:
		if err := ctx.Err(); err != nil {
			return adminWorkspaceInventory{}, err
		}
		return cloneAdminWorkspaceInventory(read.inventory), read.err
	}
}

func (s *Service) runSharedInventoryRead(ctx context.Context, key string, read *adminInventoryRead) {
	defer s.schedulerJobs.Done()
	defer func() {
		if recover() != nil {
			read.inventory = adminWorkspaceInventory{}
			read.err = errScheduledInventoryPanic
			log.Printf("[connection-health] scheduled workspace inventory panic recovered workspace=%s", key)
		}
		s.schedulerMu.Lock()
		active := s.schedulerInventoryReads[key]
		for i, candidate := range active {
			if candidate == read {
				active = append(active[:i], active[i+1:]...)
				break
			}
		}
		if len(active) == 0 {
			delete(s.schedulerInventoryReads, key)
		} else {
			s.schedulerInventoryReads[key] = active
		}
		close(read.done)
		s.schedulerMu.Unlock()
	}()
	read.inventory, read.err = s.readAdminWorkspaceInventory(ctx, read.session)
}

func cloneInventoryPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneAdminWorkspaceInventory(inventory adminWorkspaceInventory) adminWorkspaceInventory {
	copy := inventory
	copy.session.ExpiresAt = cloneInventoryPointer(inventory.session.ExpiresAt)
	copy.groups = make([]adminInventoryGroup, len(inventory.groups))
	evidenceCopies := make(map[*upstream.InventoryTimeEvidence]*upstream.InventoryTimeEvidence)
	for i, group := range inventory.groups {
		copy.groups[i] = group
		copy.groups[i].group.Multiplier = cloneFloat64Pointer(group.group.Multiplier)
		copy.groups[i].group.InventoryResponseTimes = append([]upstream.InventoryResponseTime(nil), group.group.InventoryResponseTimes...)
		if evidence := group.group.InventoryTimeEvidence; evidence != nil {
			if evidenceCopies[evidence] == nil {
				evidenceCopies[evidence] = evidence.Clone()
			}
			copy.groups[i].group.InventoryTimeEvidence = evidenceCopies[evidence]
		}
		copy.groups[i].accounts = make([]upstream.AdminGroupAccountInfo, len(group.accounts))
		for j, account := range group.accounts {
			a := account
			if account.ModelMapping != nil {
				a.ModelMapping = make(map[string]string, len(account.ModelMapping))
				for key, value := range account.ModelMapping {
					a.ModelMapping[key] = value
				}
			}
			a.Priority = cloneIntPointer(account.Priority)
			a.Concurrency = cloneIntPointer(account.Concurrency)
			a.RateMultiplier = cloneFloat64Pointer(account.RateMultiplier)
			a.LoadFactor = cloneIntPointer(account.LoadFactor)
			a.Weight = cloneIntPointer(account.Weight)
			a.GroupIDs = append([]string(nil), account.GroupIDs...)
			a.Schedulable = cloneBoolPointer(account.Schedulable)
			a.UpdatedAt = cloneInventoryPointer(account.UpdatedAt)
			a.TempUnschedulableUntil = cloneInventoryPointer(account.TempUnschedulableUntil)
			a.RateLimitResetAt = cloneInventoryPointer(account.RateLimitResetAt)
			a.OverloadUntil = cloneInventoryPointer(account.OverloadUntil)
			a.ExpiresAt = cloneInventoryPointer(account.ExpiresAt)
			a.AutoPauseOnExpired = cloneBoolPointer(account.AutoPauseOnExpired)
			a.ModelRateLimits = append([]upstream.Sub2APIModelRateLimit(nil), account.ModelRateLimits...)
			for index := range a.ModelRateLimits {
				a.ModelRateLimits[index].ResetAt = cloneInventoryPointer(account.ModelRateLimits[index].ResetAt)
			}
			a.QuotaLimit = cloneFloat64Pointer(account.QuotaLimit)
			a.QuotaUsed = cloneFloat64Pointer(account.QuotaUsed)
			a.QuotaDailyLimit = cloneFloat64Pointer(account.QuotaDailyLimit)
			a.QuotaDailyUsed = cloneFloat64Pointer(account.QuotaDailyUsed)
			a.QuotaWeeklyLimit = cloneFloat64Pointer(account.QuotaWeeklyLimit)
			a.QuotaWeeklyUsed = cloneFloat64Pointer(account.QuotaWeeklyUsed)
			a.InventoryResponseTimes = append([]upstream.InventoryResponseTime(nil), account.InventoryResponseTimes...)
			copy.groups[i].accounts[j] = a
		}
	}
	return copy
}
