package connection_health

import (
	"context"
	"log"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type schedulerRoundKey struct{}
type schedulerRoundEvidence struct {
	mu         sync.Mutex
	started    time.Time
	seen       map[string]bool
	zero       map[string]int
	unknown    map[string]int
	policies   []Policy
	targets    []PolicyAssignment
	groups     []GroupPolicyAssignment
	exclusions []GroupTargetExclusion
}

type schedulerRoundStats struct {
	mu           sync.Mutex
	firstStarted time.Time
	lastEnded    time.Time
	total        int
	over30       int
	over60       int
	longest      time.Duration
	jobs         int
	jobOver30    int
	jobOver60    int
	jobLongest   time.Duration
	inFlight     int
	maxInFlight  int
	skipped      map[string]int
	zero         map[string]int
	unknown      map[string]int
}

var schedulerStatsByService sync.Map

func (s *Service) schedulerStatistics() *schedulerRoundStats {
	value, _ := schedulerStatsByService.LoadOrStore(s, &schedulerRoundStats{zero: map[string]int{}, unknown: map[string]int{}, skipped: map[string]int{}})
	return value.(*schedulerRoundStats)
}

func (s *Service) recordSchedulerSkip(reason string) {
	stats := s.schedulerStatistics()
	stats.mu.Lock()
	stats.skipped[reason]++
	stats.mu.Unlock()
}

func (s *Service) beginSchedulerJob() time.Time {
	started := time.Now()
	stats := s.schedulerStatistics()
	stats.mu.Lock()
	stats.inFlight++
	if stats.inFlight > stats.maxInFlight {
		stats.maxInFlight = stats.inFlight
	}
	stats.mu.Unlock()
	return started
}

func (s *Service) finishSchedulerJob(started time.Time) {
	duration := time.Since(started)
	stats := s.schedulerStatistics()
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.jobs++
	stats.inFlight--
	if duration > 30*time.Second {
		stats.jobOver30++
	}
	if duration > 60*time.Second {
		stats.jobOver60++
	}
	if duration > stats.jobLongest {
		stats.jobLongest = duration
	}
}

func (s *Service) beginSchedulerRound(ctx context.Context) (context.Context, *schedulerRoundEvidence) {
	round := &schedulerRoundEvidence{started: time.Now(), seen: map[string]bool{}, zero: map[string]int{}, unknown: map[string]int{}}
	return context.WithValue(ctx, schedulerRoundKey{}, round), round
}

func recordSchedulerInventory(ctx context.Context, userID, workspace string, inventory adminWorkspaceInventory) {
	round, ok := ctx.Value(schedulerRoundKey{}).(*schedulerRoundEvidence)
	if !ok || inventory.session.Platform != upstream.PlatformSub2API {
		return
	}
	round.mu.Lock()
	defer round.mu.Unlock()
	key := userID + "|" + workspace
	if round.seen[key] {
		return
	}
	round.seen[key] = true
	// Build the monitored membership from the snapshot we already fetched. Failed
	// groups remain unknown and never become a false zero.
	readable := inventory
	readable.groupsComplete = true
	readable.groups = append([]adminInventoryGroup(nil), inventory.groups...)
	for index := range readable.groups {
		readable.groups[index].err = nil
	}
	scope, err := buildAdminMonitoringScope(userID, workspace, readable, round.policies, round.targets, round.groups, round.exclusions)
	assigned := assignedEnabledPoliciesByGroup(round.policies, round.groups)[key]
	for _, group := range inventory.groups {
		monitored := scope.monitoredByGroup[group.group.ID]
		if len(monitored) == 0 && len(assigned[group.group.ID]) == 0 {
			continue
		}
		groupKey := key + "|" + group.group.ID
		unknown := err != nil || !inventory.groupsComplete || group.err != nil
		usable := map[string]bool{}
		for _, account := range group.accounts {
			targetID := buildTargetID(string(inventory.session.Platform), workspace, account.ID)
			if _, exists := monitored[targetID]; !exists {
				continue
			}
			status, known := normalizeFloorTargetStatus(string(inventory.session.Platform), account.Status)
			if !known || account.Schedulable == nil || !account.TempUnschedulableKnown {
				unknown = true
				continue
			}
			if targetStatusEnabled(string(inventory.session.Platform), status) && *account.Schedulable && upstream.Sub2APIDeadlineExpired(account.TempUnschedulableUntil, account.TempUnschedulableKnown, round.started) {
				usable[targetID] = true
			}
		}
		if unknown {
			round.unknown[groupKey]++
		} else if len(usable) == 0 {
			round.zero[groupKey]++
		}
	}
}

func (s *Service) finishSchedulerRound(round *schedulerRoundEvidence) {
	ended := time.Now()
	stats := s.schedulerStatistics()
	stats.mu.Lock()
	defer stats.mu.Unlock()
	if stats.firstStarted.IsZero() {
		stats.firstStarted = round.started
	}
	stats.lastEnded = ended
	duration := ended.Sub(round.started)
	stats.total++
	if duration > 30*time.Second {
		stats.over30++
	}
	if duration > 60*time.Second {
		stats.over60++
	}
	if duration > stats.longest {
		stats.longest = duration
	}
	round.mu.Lock()
	for key, count := range round.zero {
		stats.zero[key] += count
	}
	for key, count := range round.unknown {
		stats.unknown[key] += count
	}
	round.mu.Unlock()
	if ended.Sub(stats.firstStarted) < 10*time.Minute {
		return
	}
	log.Printf("[connection-health] round stats first_started=%s last_ended=%s scans=%d scan_over_30s=%d scan_longest_ms=%d jobs=%d job_over_30s=%d job_over_60s=%d job_longest_ms=%d max_in_flight=%d skipped_in_flight=%d skipped_no_slot=%d skipped_lease_busy=%d skipped_priority_workspace=%d skipped_restore_busy=%d zero_available=%v unknown=%v", stats.firstStarted.UTC().Format(time.RFC3339Nano), stats.lastEnded.UTC().Format(time.RFC3339Nano), stats.total, stats.over30, stats.longest.Milliseconds(), stats.jobs, stats.jobOver30, stats.jobOver60, stats.jobLongest.Milliseconds(), stats.maxInFlight, stats.skipped["in_flight"], stats.skipped["no_slot"], stats.skipped["lease_busy"], stats.skipped["priority_workspace"], stats.skipped["restore_busy"], stats.zero, stats.unknown)
	stats.firstStarted = time.Time{}
	stats.total, stats.over30, stats.over60, stats.longest = 0, 0, 0, 0
	stats.jobs, stats.jobOver30, stats.jobOver60, stats.jobLongest = 0, 0, 0, 0
	stats.maxInFlight = stats.inFlight
	stats.skipped = map[string]int{}
	stats.zero, stats.unknown = map[string]int{}, map[string]int{}
}
