package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

const hiddenHealthTarget = "sub2api:ws1:manual"
const hiddenHealthUser = "user1"
const hiddenHealthWorkspace = "ws1"
const hiddenProbeErrorKey = "admin.connectionHealth.errors.probeTargetHidden"

func hiddenHealthFixture(t *testing.T, reader PlatformGroupReader) (*Service, *hiddenFakeRepository) {
	t.Helper()
	repo := newHiddenFakeRepository()
	policy := Policy{
		ID: "p1", UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, Enabled: true,
		PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeHealthProbe,
		AutoDegradeEnabled: true, AutoRemoteActionEnabled: true, ProbeIntervalSeconds: 60,
		ModelTargets: []ModelTarget{{ModelName: "gpt-4o", Enabled: true}},
	}
	repo.policies = []Policy{policy}
	repo.groupAssignments = []GroupPolicyAssignment{
		{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, AdminGroupID: "g1", PolicyID: policy.ID},
		{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, AdminGroupID: "g2", PolicyID: policy.ID},
	}
	repo.assignments = []PolicyAssignment{{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, TargetID: hiddenHealthTarget, PolicyID: policy.ID}}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo.fakeRepository)
	service.repo = repo
	return service, repo
}

func saveHiddenHealthTarget(t *testing.T, repo *hiddenFakeRepository, hidden bool) {
	t.Helper()
	if _, err := repo.SaveAccountHidden(t.Context(), hiddenHealthUser, hiddenHealthWorkspace, hiddenHealthTarget, hidden, "manual"); err != nil {
		t.Fatal(err)
	}
}

func TestHiddenHealthCandidates(t *testing.T) {
	account := upstream.AdminGroupAccountInfo{ID: "manual", Name: "manual", Status: "active", Models: "gpt-4o"}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}, "g2": {account}},
	}
	service, repo := hiddenHealthFixture(t, &reader)
	exclusions := []GroupTargetExclusion{{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, AdminGroupID: "g1", TargetID: hiddenHealthTarget}}
	jobs := func() []adminProbeJob {
		return service.collectAdminProbeJobsWithGroups(context.Background(), repo.policies, repo.assignments, repo.groupAssignments, exclusions)
	}
	if result := jobs(); len(result) != 1 || result[0].target.TargetID != hiddenHealthTarget {
		t.Fatalf("direct policy must produce one job for the shared target: %v", result)
	}
	saveHiddenHealthTarget(t, repo, true)
	if result := jobs(); len(result) != 0 {
		t.Errorf("screened account with a direct policy must produce zero jobs across both groups: %d", len(result))
	}
	// Remove the direct assignment and move inheritance to a group joined during screening.
	repo.assignments = nil
	reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "g3"})
	reader.accountsByGrp["g3"] = []upstream.AdminGroupAccountInfo{account}
	repo.groupAssignments = []GroupPolicyAssignment{{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, AdminGroupID: "g3", PolicyID: "p1"}}
	if result := jobs(); len(result) != 0 {
		t.Errorf("joining a newly assigned group must not reintroduce the screened target: %d", len(result))
	}
	saveHiddenHealthTarget(t, repo, false)
	if result := jobs(); len(result) != 1 || result[0].target.AdminGroupID != "g3" {
		t.Errorf("unhiding must restore the newly inherited policy: %v", result)
	}
	repo.listErr = errors.New("fixture read failed")
	if result := jobs(); len(result) != 0 {
		t.Errorf("unknown screening scope must produce zero jobs: %d", len(result))
	}
}

type countedHiddenHealthReads struct {
	*hiddenFakeRepository
	reads map[string]int
}

func (repo *countedHiddenHealthReads) ListHiddenAccounts(ctx context.Context, userID, workspace string) ([]HiddenAccount, error) {
	repo.reads[userID+"|"+workspace]++
	if workspace == hiddenHealthWorkspace {
		return nil, errors.New("test workspace lookup failed")
	}
	return repo.hiddenFakeRepository.ListHiddenAccounts(ctx, userID, workspace)
}

func TestHiddenHealthSchedulerSharesOneFailedReadPerRound(t *testing.T) {
	reader := &mutableSub2APISchedulerReader{priority: 30}
	service, base := hiddenHealthFixture(t, reader)
	repo := &countedHiddenHealthReads{hiddenFakeRepository: base, reads: map[string]int{}}
	service.repo = repo
	service.priorityActions = reader
	ctx, round := service.beginSchedulerRound(t.Context())
	round.policies, round.targets, round.groups = repo.policies, repo.assignments, repo.groupAssignments
	cache := make(adminInventoryCache)
	service.syncMultiplierPrioritiesWithCacheMode(ctx, nil, nil, nil, nil, nil, cache, true, nil, nil)
	if len(repo.reads) != 0 {
		t.Fatal("a skipped workspace must not read hidden accounts")
	}
	state := TargetActionState{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, TargetID: hiddenHealthTarget, OriginalStatus: "inactive", LastAppliedStatus: "active"}
	repo.targetActionStates[hiddenHealthUser+"|"+hiddenHealthWorkspace+"|"+hiddenHealthTarget] = state
	service.syncMultiplierPrioritiesWithCacheMode(ctx, repo.policies, repo.assignments, repo.groupAssignments, nil, nil, cache, true, nil, nil)
	service.restoreUnmanagedTargetActions(ctx, repo.policies, repo.assignments, repo.groupAssignments, nil, []TargetActionState{state}, cache)
	if jobs := service.collectAdminProbeJobsWithGroupsAndCache(ctx, repo.policies, repo.assignments, repo.groupAssignments, nil, cache, nil); len(jobs) != 0 {
		t.Fatalf("failed read must block candidates: %d", len(jobs))
	}
	if len(round.zero) != 0 || len(round.unknown) != 0 {
		t.Fatalf("failed scope must not publish statistics: %v / %v", round.zero, round.unknown)
	}
	inventory, err := service.loadAdminInventory(ctx, hiddenHealthUser, hiddenHealthWorkspace, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.schedulerHiddenHealthTargets(ctx, hiddenHealthUser, "ws2", *inventory); err != nil {
		t.Fatal(err)
	}
	if repo.reads[hiddenHealthUser+"|"+hiddenHealthWorkspace] != 1 || repo.reads[hiddenHealthUser+"|ws2"] != 1 {
		t.Fatalf("read scope was not isolated or reused: %v", repo.reads)
	}
}
