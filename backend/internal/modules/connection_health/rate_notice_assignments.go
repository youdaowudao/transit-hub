package connection_health

import (
	"strings"

	"transithub/backend/internal/modules/upstream"
)

// PeekKeyAssignments copies only this process's recently confirmed nonsecret
// assignment. It does not prepare, refresh, wait, or read either storage service.
func (s *Service) PeekKeyAssignments(userID, workspaceID, siteID string, mainPlatform upstream.Platform, bindings []upstream.RateNoticeBinding) map[string]upstream.RateNoticeKeyAssignment {
	keySet := make(map[string]struct{})
	for _, binding := range bindings {
		if status := strings.TrimSpace(binding.Status); status != "" && status != "active" {
			continue
		}
		if strings.TrimSpace(binding.AccountID) == "" {
			continue
		}
		if platform := strings.TrimSpace(binding.AdminPlatform); platform != "" && !strings.EqualFold(platform, string(mainPlatform)) {
			continue
		}
		if keyID := strings.TrimSpace(binding.KeyID); keyID != "" {
			keySet[keyID] = struct{}{}
		}
	}
	result := make(map[string]upstream.RateNoticeKeyAssignment, len(keySet))
	for keyID := range keySet {
		result[keyID] = upstream.RateNoticeKeyAssignment{Reason: "关联Key的最近分组归属尚未确认，请核查原关联"}
	}
	if mainPlatform != upstream.PlatformSub2API {
		return result
	}
	orderedKeys := sortedKeys(keySet)
	now := s.multiplierCurrentTime()
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	entry := s.multiplierSnapshots[multiplierSnapshotKey(userID, workspaceID, siteID)]
	if entry == nil || entry.userID != userID || entry.adminAccountID != workspaceID || entry.siteID != siteID || entry.platform != mainPlatform || entry.workspaceKey != multiplierSnapshotKey(userID, workspaceID, siteID) || entry.bindingSignature != strings.Join(orderedKeys, "\x00") || strings.Join(entry.keyIDs, "\x00") != strings.Join(orderedKeys, "\x00") {
		return result
	}
	for _, keyID := range orderedKeys {
		assignment := result[keyID]
		if _, deleted := entry.confirmedDeletedKeys[keyID]; deleted {
			assignment.Reason = "关联Key已确认删除且仍保留原关联，请核查并解绑"
			result[keyID] = assignment
			continue
		}
		if entry.status != "complete" || entry.inFlight || entry.fetchedAt.IsZero() || entry.fetchedAt.After(now) || !entry.expiresAt.After(now) || now.Sub(entry.fetchedAt) >= multiplierSnapshotTTL {
			assignment.Reason = "关联Key的最近分组资料正在更新、已过期或未能确认，请查看分组健康并核查原关联"
			result[keyID] = assignment
			continue
		}
		if _, failed := entry.keyFailures[keyID]; failed {
			assignment.Reason = "这个关联Key的最近分组读取未成功，请查看分组健康并核查原关联"
			result[keyID] = assignment
			continue
		}
		key, ok := entry.keys[keyID]
		if !ok {
			assignment.Reason = "最近完整读取中未找到这个关联Key，请核查原关联"
			result[keyID] = assignment
			continue
		}
		if key.id != "" && key.id != keyID {
			assignment.Reason = "关联Key编号与已缓存结果不一致，请核查原关联"
			result[keyID] = assignment
			continue
		}
		var matched *upstream.GroupInfo
		ambiguous := false
		for i := range entry.site.groups {
			group := &entry.site.groups[i]
			matches := key.groupID != "" && group.ID == key.groupID || key.groupID == "" && key.groupName != "" && group.Name == key.groupName
			if !matches {
				continue
			}
			if matched != nil {
				ambiguous = true
				break
			}
			matched = group
		}
		if ambiguous || matched == nil || matched.ID == "" && matched.Name == "" {
			assignment.Reason = "关联Key的分组身份缺失或互相冲突，请核查原关联"
			result[keyID] = assignment
			continue
		}
		result[keyID] = upstream.RateNoticeKeyAssignment{Confirmed: true, GroupID: matched.ID, GroupName: matched.Name, ConfirmedAt: entry.fetchedAt}
	}

	accountKeys := make(map[string][]string)
	for _, binding := range bindings {
		if status := strings.TrimSpace(binding.Status); status != "" && status != "active" {
			continue
		}
		if platform := strings.TrimSpace(binding.AdminPlatform); platform != "" && !strings.EqualFold(platform, string(mainPlatform)) {
			continue
		}
		if accountID := strings.TrimSpace(binding.AccountID); accountID != "" {
			accountKeys[accountID] = append(accountKeys[accountID], strings.TrimSpace(binding.KeyID))
		}
	}
	for _, keyIDs := range accountKeys {
		identity := ""
		conflict := false
		for _, keyID := range keyIDs {
			assignment := result[keyID]
			if !assignment.Confirmed {
				continue
			}
			candidate := "name:" + assignment.GroupName
			if assignment.GroupID != "" {
				candidate = "id:" + assignment.GroupID
			}
			if identity != "" && identity != candidate {
				conflict = true
			}
			identity = candidate
		}
		if conflict {
			for _, keyID := range keyIDs {
				result[keyID] = upstream.RateNoticeKeyAssignment{Reason: "同一已登记主站账号关联的Key分组不一致，请核查原关联"}
			}
		}
	}
	return result
}
