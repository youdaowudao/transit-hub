package connection_health

import (
	"sort"
	"strings"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type ModelControlGroupSupply struct {
	Items             []ModelControlSupplyItem `json:"items"`
	OtherTypeAccounts int                      `json:"otherTypeAccounts"`
}
type ModelControlSupplyItem struct {
	ModelName      string   `json:"modelName"`
	Open           int      `json:"open"`
	Closed         int      `json:"closed"`
	Unknown        int      `json:"unknown"`
	OpenAccounts   []string `json:"openAccounts"`
	ClosedAccounts []string `json:"closedAccounts"`
}

func modelControlUpstreamKeys(a upstream.AdminGroupAccountInfo, model string) (keys []string, known, eligible bool) {
	if a.Type != "apikey" || (a.Platform != "openai" && a.Platform != "anthropic") {
		return nil, false, false
	}
	if a.Platform == "openai" && a.OpenAIPassthrough {
		return []string{model}, true, true
	}
	if !a.ModelMappingKnown {
		return nil, false, true
	}
	if len(a.ModelMapping) == 0 {
		return []string{model}, true, true
	}
	keys = []string{}
	for key, value := range a.ModelMapping {
		if value == model {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, true, true
}
func modelControlSupplyState(a upstream.AdminGroupAccountInfo, keys []string, mappingKnown bool, upstreamModel string, now time.Time) (open, unknown bool) {
	status, statusKnown := normalizeFloorTargetStatus("sub2api", a.Status)
	if statusKnown && status != "active" || a.Schedulable != nil && !*a.Schedulable {
		return false, false
	}
	for _, deadline := range []struct {
		at    *time.Time
		known bool
	}{{a.TempUnschedulableUntil, a.TempUnschedulableKnown}, {a.OverloadUntil, a.OverloadKnown}, {a.RateLimitResetAt, a.RateLimitKnown}} {
		if deadline.known && !upstream.Sub2APIDeadlineExpired(deadline.at, true, now) {
			return false, false
		}
	}
	accountUnknown := !statusKnown || a.Schedulable == nil || !a.TempUnschedulableKnown || !a.OverloadKnown || !a.RateLimitKnown || !a.ExpiresAtKnown || !a.QuotaKnown || a.AutoPauseOnExpired == nil
	for _, quota := range [][2]*float64{{a.QuotaLimit, a.QuotaUsed}, {a.QuotaDailyLimit, a.QuotaDailyUsed}, {a.QuotaWeeklyLimit, a.QuotaWeeklyUsed}} {
		if quota[0] != nil && *quota[0] > 0 {
			if quota[1] == nil {
				accountUnknown = true
			} else if a.QuotaKnown && *quota[1] >= *quota[0] {
				return false, false
			}
		}
	}
	if a.ExpiresAtKnown && a.AutoPauseOnExpired != nil && *a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now) {
		return false, false
	}
	if !mappingKnown {
		return false, true
	}
	if len(keys) == 0 {
		return false, false
	}
	anyAvailable, anyUnknown := false, false
	for _, key := range keys {
		blocked, keyUnknown := false, !a.ModelRateLimitsKnown
		for _, limit := range a.ModelRateLimits {
			if limit.Model != key && limit.Model != upstreamModel {
				continue
			}
			if !limit.Known {
				keyUnknown = true
			} else if !upstream.Sub2APIDeadlineExpired(limit.ResetAt, true, now) {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		if accountUnknown || keyUnknown {
			anyUnknown = true
		} else {
			anyAvailable = true
		}
	}
	if anyAvailable {
		return true, false
	}
	return false, anyUnknown
}
func buildModelControlGroupSupply(accounts []upstream.AdminGroupAccountInfo, platform, adminAccountID string, summaries map[string]*ModelControlAccountSummary, now time.Time) *ModelControlGroupSupply {
	unique := []upstream.AdminGroupAccountInfo{}
	seen := map[string]bool{}
	models := map[string]bool{}
	for _, account := range accounts {
		if seen[account.ID] {
			continue
		}
		seen[account.ID] = true
		unique = append(unique, account)
		if summary := summaries[buildTargetID(platform, adminAccountID, account.ID)]; summary != nil {
			for _, model := range summary.Models {
				models[model.ModelName] = true
			}
		}
	}
	if len(models) == 0 {
		return nil
	}
	supply := &ModelControlGroupSupply{Items: []ModelControlSupplyItem{}}
	for _, account := range unique {
		_, _, eligible := modelControlUpstreamKeys(account, "")
		if !eligible {
			supply.OtherTypeAccounts++
		}
	}
	for model := range models {
		item := ModelControlSupplyItem{ModelName: model, OpenAccounts: []string{}, ClosedAccounts: []string{}}
		for _, account := range unique {
			name := strings.TrimSpace(account.Name)
			if name == "" {
				name = account.ID
			}
			if summary := summaries[buildTargetID(platform, adminAccountID, account.ID)]; summary != nil {
				for _, managed := range summary.Models {
					if managed.ModelName == model && managed.Status == "closed" {
						item.Closed++
						if len(item.ClosedAccounts) < 10 {
							item.ClosedAccounts = append(item.ClosedAccounts, name)
						}
						break
					}
				}
			}
			keys, known, eligible := modelControlUpstreamKeys(account, model)
			if !eligible {
				continue
			}
			open, unknown := modelControlSupplyState(account, keys, known, model, now)
			if open {
				item.Open++
				if len(item.OpenAccounts) < 10 {
					item.OpenAccounts = append(item.OpenAccounts, name)
				}
			} else if unknown {
				item.Unknown++
			}
		}
		supply.Items = append(supply.Items, item)
	}
	sort.Slice(supply.Items, func(i, j int) bool {
		if supply.Items[i].Open == supply.Items[j].Open {
			return supply.Items[i].ModelName < supply.Items[j].ModelName
		}
		return supply.Items[i].Open < supply.Items[j].Open
	})
	return supply
}
