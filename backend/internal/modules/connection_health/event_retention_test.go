package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeEventRetentionRepository struct {
	acquired       bool
	leaseErr       error
	indexErr       error
	deleteErrAt    int
	deleteResults  []int64
	deleteCalls    int
	panicOnDelete  bool
	ensureCalls    int
	releaseCalls   int
	observedCutoff []time.Time
	observedLimits []int
}

func (f *fakeEventRetentionRepository) TryAcquireEventRetentionLease(ctx context.Context) (func(), bool, error) {
	return func() { f.releaseCalls++ }, f.acquired, f.leaseErr
}

func (f *fakeEventRetentionRepository) EnsureEventRetentionIndex(ctx context.Context) error {
	f.ensureCalls++
	return f.indexErr
}

func (f *fakeEventRetentionRepository) DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if f.panicOnDelete {
		panic("delete panic")
	}
	f.deleteCalls++
	f.observedCutoff = append(f.observedCutoff, cutoff)
	f.observedLimits = append(f.observedLimits, limit)
	if f.deleteErrAt > 0 && f.deleteCalls == f.deleteErrAt {
		return 0, errors.New("delete failed")
	}
	if f.deleteCalls <= len(f.deleteResults) {
		return f.deleteResults[f.deleteCalls-1], nil
	}
	return 0, nil
}

func TestRunEventRetentionSafelyRecoversPanic(t *testing.T) {
	repository := &fakeEventRetentionRepository{acquired: true, panicOnDelete: true}
	service := &Service{eventRetention: repository}

	service.runEventRetentionSafely(context.Background())

	if repository.releaseCalls != 1 {
		t.Fatalf("release calls=%d want 1", repository.releaseCalls)
	}
}

func TestNextEventRetentionRunUsesConfirmedBeijingTimesWithoutStartupCatchup(t *testing.T) {
	beijing := eventRetentionLocation
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before morning run", now: time.Date(2026, 8, 16, 2, 59, 59, 0, beijing),
			want: time.Date(2026, 8, 16, 3, 0, 0, 0, beijing),
		},
		{
			name: "exact morning start waits for afternoon", now: time.Date(2026, 8, 16, 3, 0, 0, 0, beijing),
			want: time.Date(2026, 8, 16, 15, 0, 0, 0, beijing),
		},
		{
			name: "between runs", now: time.Date(2026, 8, 16, 9, 30, 0, 0, beijing),
			want: time.Date(2026, 8, 16, 15, 0, 0, 0, beijing),
		},
		{
			name: "exact afternoon start waits for next day", now: time.Date(2026, 8, 16, 15, 0, 0, 0, beijing),
			want: time.Date(2026, 8, 17, 3, 0, 0, 0, beijing),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := nextEventRetentionRun(test.now)
			if !got.Equal(test.want) {
				t.Fatalf("next run=%s want %s", got.Format(time.RFC3339), test.want.Format(time.RFC3339))
			}
			if !got.After(test.now) {
				t.Fatalf("next run=%s must be after startup time=%s", got.Format(time.RFC3339), test.now.Format(time.RFC3339))
			}
		})
	}
}

func TestRunEventRetentionAtDeletesBoundedBatches(t *testing.T) {
	repository := &fakeEventRetentionRepository{
		acquired:      true,
		deleteResults: []int64{eventRetentionBatchSize, eventRetentionBatchSize, 7},
	}
	service := &Service{eventRetention: repository}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	service.runEventRetentionAt(context.Background(), now)

	if repository.ensureCalls != 1 || repository.deleteCalls != 3 || repository.releaseCalls != 1 {
		t.Fatalf("calls ensure=%d delete=%d release=%d", repository.ensureCalls, repository.deleteCalls, repository.releaseCalls)
	}
	wantCutoff := now.Add(-eventRetentionWindow)
	for index, cutoff := range repository.observedCutoff {
		if !cutoff.Equal(wantCutoff) {
			t.Fatalf("cutoff[%d]=%s want %s", index, cutoff, wantCutoff)
		}
		if repository.observedLimits[index] != eventRetentionBatchSize {
			t.Fatalf("limit[%d]=%d", index, repository.observedLimits[index])
		}
	}
}

func TestRunEventRetentionAtStopsAtPerRunLimit(t *testing.T) {
	results := make([]int64, eventRetentionMaxBatches+2)
	for index := range results {
		results[index] = eventRetentionBatchSize
	}
	repository := &fakeEventRetentionRepository{acquired: true, deleteResults: results}
	service := &Service{eventRetention: repository}

	service.runEventRetentionAt(context.Background(), time.Now())

	if repository.deleteCalls != eventRetentionMaxBatches {
		t.Fatalf("delete calls=%d want %d", repository.deleteCalls, eventRetentionMaxBatches)
	}
}

func TestRunEventRetentionAtRequiresLeaseAndIndex(t *testing.T) {
	t.Run("lease not acquired", func(t *testing.T) {
		repository := &fakeEventRetentionRepository{}
		(&Service{eventRetention: repository}).runEventRetentionAt(context.Background(), time.Now())
		if repository.ensureCalls != 0 || repository.deleteCalls != 0 || repository.releaseCalls != 0 {
			t.Fatalf("unexpected calls: %+v", repository)
		}
	})

	t.Run("index failure", func(t *testing.T) {
		repository := &fakeEventRetentionRepository{acquired: true, indexErr: errors.New("index failed")}
		(&Service{eventRetention: repository}).runEventRetentionAt(context.Background(), time.Now())
		if repository.ensureCalls != 1 || repository.deleteCalls != 0 || repository.releaseCalls != 1 {
			t.Fatalf("unexpected calls: %+v", repository)
		}
	})

	t.Run("lease failure", func(t *testing.T) {
		repository := &fakeEventRetentionRepository{leaseErr: errors.New("lease failed")}
		(&Service{eventRetention: repository}).runEventRetentionAt(context.Background(), time.Now())
		if repository.ensureCalls != 0 || repository.deleteCalls != 0 || repository.releaseCalls != 0 {
			t.Fatalf("unexpected calls: %+v", repository)
		}
	})
}

func TestRunEventRetentionAtStopsAfterDeleteFailureOrCancellation(t *testing.T) {
	t.Run("delete failure", func(t *testing.T) {
		repository := &fakeEventRetentionRepository{
			acquired: true, deleteResults: []int64{eventRetentionBatchSize}, deleteErrAt: 2,
		}
		(&Service{eventRetention: repository}).runEventRetentionAt(context.Background(), time.Now())
		if repository.deleteCalls != 2 || repository.releaseCalls != 1 {
			t.Fatalf("unexpected calls: %+v", repository)
		}
	})

	t.Run("cancelled before delete", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		repository := &fakeEventRetentionRepository{acquired: true}
		(&Service{eventRetention: repository}).runEventRetentionAt(ctx, time.Now())
		if repository.ensureCalls != 1 || repository.deleteCalls != 0 || repository.releaseCalls != 1 {
			t.Fatalf("unexpected calls: %+v", repository)
		}
	})
}
