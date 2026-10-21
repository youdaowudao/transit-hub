package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type governanceInventoryReader struct {
	fakePlatformGroupReader
	mu             sync.Mutex
	fetches, lists int
	release        <-chan struct{}
}

func (r *governanceInventoryReader) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	r.mu.Lock()
	r.fetches++
	r.mu.Unlock()
	<-r.release
	return []upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}}, nil
}
func (r *governanceInventoryReader) ListAdminGroupAccounts(session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.mu.Lock()
	r.lists++
	r.mu.Unlock()
	if group.ID == "g2" {
		return nil, errors.New("fixture partial inventory")
	}
	return r.fakePlatformGroupReader.ListAdminGroupAccounts(session, group)
}

type governanceInventoryRepository struct {
	*fakeRepository
	admitted chan struct{}
}

func (r *governanceInventoryRepository) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	h, ok, err := r.fakeRepository.AcquireActionLease(ctx, key, wait)
	if ok {
		r.admitted <- struct{}{}
	}
	return h, ok, err
}
func TestConcurrentScheduledJobsShareInventoryRead(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	reader := &governanceInventoryReader{release: release, fakePlatformGroupReader: fakePlatformGroupReader{accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a"}, {ID: "b"}, {ID: "c"}}}}}
	repo := &governanceInventoryRepository{fakeRepository: newFakeRepository(), admitted: make(chan struct{}, 3)}
	s := &Service{repo: repo, platformGroups: reader, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		job := adminProbeJob{userID: "user1", adminAccountID: "ws1", session: upstream.Session{Platform: upstream.PlatformSub2API}, target: AdminProbeTarget{TargetID: "sub2api:ws1:" + id, AccountID: id}, floorGuard: newWorkspaceFloorGuard()}
		wg.Add(1)
		go s.runAdminProbeJob(ctx, job, func() {}, &wg)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-repo.admitted:
		case <-time.After(time.Second):
			cancel()
			unblock()
			wg.Wait()
			t.Fatal("jobs did not acquire their independent account locks")
		}
	}
	// Every job is now admitted, while the common read is deliberately unfinished.
	time.Sleep(50 * time.Millisecond)
	unblock()
	wg.Wait()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.fetches != 1 || reader.lists != 2 {
		t.Fatalf("overlapping jobs issued %d group-list and %d membership reads; want 1 and G=2", reader.fetches, reader.lists)
	}
}
