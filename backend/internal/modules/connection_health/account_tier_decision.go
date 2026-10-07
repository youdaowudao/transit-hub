package connection_health

import (
	"context"
	"log"
	"sync"

	"transithub/backend/internal/modules/upstream"
)

type accountTierDecisionContextKey struct{}
type accountTierDecisionResult struct {
	tiers map[string]int
	err   error
}
type accountTierDecisionCache struct {
	mu         sync.Mutex
	workspaces map[string]accountTierDecisionResult
}

func withAccountTierDecisionCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(accountTierDecisionContextKey{}).(*accountTierDecisionCache); ok {
		return ctx
	}
	return context.WithValue(ctx, accountTierDecisionContextKey{}, &accountTierDecisionCache{workspaces: map[string]accountTierDecisionResult{}})
}

func (s *Service) accountTiersForDecision(ctx context.Context, userID, workspace string) (map[string]int, error) {
	cache, ok := ctx.Value(accountTierDecisionContextKey{}).(*accountTierDecisionCache)
	if !ok {
		return s.repo.ListAccountTiers(ctx, userID, workspace)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	key := userID + "|" + workspace
	if result, exists := cache.workspaces[key]; exists {
		return result.tiers, result.err
	}
	tiers, err := s.repo.ListAccountTiers(ctx, userID, workspace)
	cache.workspaces[key] = accountTierDecisionResult{tiers, err}
	return tiers, err
}

func (s *Service) captureWorkspaceRulesIfMissing(ctx context.Context, userID, workspace string, inventory map[string]*priorityTargetInventory) (context.Context, error) {
	policies := []Policy{}
	for _, item := range inventory {
		policies = mergePoliciesByID(policies, item.policies)
	}
	settings, err := s.capturedWorkspaceRules(ctx, userID, workspace, policies)
	if err != nil {
		return ctx, err
	}
	if _, ok := ctx.Value(workspaceRuleSnapshotKey{}).(workspaceRuleSnapshot); !ok {
		ctx = context.WithValue(ctx, workspaceRuleSnapshotKey{}, workspaceRuleSnapshot{settings: map[string]WorkspaceHealthSettings{userID + "|" + workspace: settings}})
	}
	for _, item := range inventory {
		if !item.target.ConfigGenerationKnown {
			item.target.ConfigGeneration, item.target.ConfigGenerationKnown = settings.ConfigGeneration, settings.RuleVersion != ""
		}
	}
	return ctx, nil
}

func oldRulePrimaryUnmanaged(platform string, rule string, tier int) bool {
	return platform == string(upstream.PlatformSub2API) && rule == RuleVersionLegacy && effectiveAccountTier(tier) == 1
}

func healthPrioritySegment(candidate healthPriorityCandidate) int {
	platform := ""
	if candidate.item != nil {
		platform = candidate.item.target.Platform
	} else if parsed, ok := parseTargetID(candidate.targetID); ok {
		platform = parsed.platform
	}
	if platform != string(upstream.PlatformSub2API) {
		return candidate.healthBand
	}
	primary := effectiveAccountTier(candidate.accountTier) == 1
	if candidate.item != nil {
		primary = effectiveAccountTier(candidate.item.accountTier) == 1 && candidate.item.target.Platform == string(upstream.PlatformSub2API)
	}
	if candidate.healthBand == 4 {
		return 6
	}
	if candidate.healthBand < 0 || candidate.healthBand > 4 {
		return 5
	}
	if primary && candidate.healthBand < 3 {
		return candidate.healthBand
	}
	return candidate.healthBand + 3
}

// Encoding consumes the same segments as the display comparator. Legacy and
// NewAPI retain their original bands; non-stopped Sub2API primaries share 10–98.
func encodeHealthPriorityCandidates(platform upstream.Platform, candidates []healthPriorityCandidate) map[string]int {
	sortHealthPriorityCandidates(candidates)
	values := make(map[string]int, len(candidates))
	segment, rank, start, lastPrimary := -1, 0, 10, 9
	primaryOverflow, backupHealthyOverflow := 0, 0
	var previous healthPriorityCandidate
	for _, candidate := range candidates {
		next := candidate.healthBand
		if platform == upstream.PlatformSub2API {
			next = healthPrioritySegment(candidate)
		}
		if next != segment {
			segment, rank = next, 0
			if platform == upstream.PlatformSub2API && segment <= 3 {
				start = minInt(99, lastPrimary+1)
			}
		} else if candidateUsesV2(candidate) && candidateUsesV2(previous) && (candidate.multiplierUnknown != previous.multiplierUnknown || candidate.multiplier != previous.multiplier) {
			rank++
		}
		value := desiredHealthPriorityForPlatform(platform, candidate.healthBand, rank)
		if platform == upstream.PlatformSub2API {
			if segment < 3 {
				if start+rank > 98 {
					primaryOverflow++
				}
				value = minInt(98, start+rank)
				lastPrimary = value
			} else if segment == 3 {
				if start+rank > 99 {
					backupHealthyOverflow++
				}
				value = minInt(99, start+rank)
			}
		}
		values[candidate.targetID] = value
		if !candidateUsesV2(candidate) {
			rank++
		}
		previous = candidate
	}
	if primaryOverflow > 0 || backupHealthyOverflow > 0 {
		log.Printf("[connection-health] priority encoding capacity exceeded platform=%s primary_overflow=%d backup_healthy_overflow=%d candidates=%d", platform, primaryOverflow, backupHealthyOverflow, len(candidates))
	}
	return values
}
