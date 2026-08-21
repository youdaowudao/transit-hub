package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type upstreamKeyUsageForDateReader interface {
	KeyUsageForDate(ctx context.Context, userID, adminAccountID, date string) (upstream.KeyUsageForDateResult, error)
}

type accountKeyCostRunRepository interface {
	SaveUpstreamKeyCostRuns(ctx context.Context, runs []UpstreamKeyCostRun) error
	PublishAccountStatsRefresh(ctx context.Context, runs []UpstreamKeyCostRun, stats []AccountDailyStat) error
	GetPublishedAccountStatsRefresh(ctx context.Context, userID, adminAccountID, runID, date string) (AccountStatsRefreshResponse, bool, error)
}

type AccountStatsRefreshResponse struct {
	Date              string `json:"date"`
	SnapshotRunID     string `json:"snapshotRunId"`
	ExpectedSites     int    `json:"expectedSites"`
	CompletedSites    int    `json:"completedSites"`
	Quality           string `json:"quality"`
	ExpectedAccounts  int    `json:"expectedAccounts"`
	CompletedAccounts int    `json:"completedAccounts"`
}

func accountRefreshRunID(userID, adminAccountID, date, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + adminAccountID + "\x00" + date + "\x00" + idempotencyKey))
	return "account-refresh-" + hex.EncodeToString(sum[:16])
}

func (s *MetricsService) RefreshAccountStats(ctx context.Context, userID, date, idempotencyKey string) (AccountStatsRefreshResponse, error) {
	if s == nil || s.upstreams == nil || s.accounts == nil || s.keyUsageForDate == nil || s.keyCostRuns == nil || strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 {
		return AccountStatsRefreshResponse{}, errInvalidAccountBatch
	}
	if strings.TrimSpace(date) == "" {
		date = businesstime.Today()
	}
	if _, err := time.ParseInLocation("2006-01-02", date, businesstime.Location()); err != nil {
		return AccountStatsRefreshResponse{}, ErrAdditionalCostInvalidDate
	}
	if date != businesstime.Today() {
		return AccountStatsRefreshResponse{}, ErrAdditionalCostInvalidDate
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountStatsRefreshResponse{}, err
	}
	runID := accountRefreshRunID(userID, adminAccountID, date, strings.TrimSpace(idempotencyKey))
	if published, found, err := s.keyCostRuns.GetPublishedAccountStatsRefresh(ctx, userID, adminAccountID, runID, date); err != nil {
		return AccountStatsRefreshResponse{}, err
	} else if found {
		return published, nil
	}
	siteTotals, err := s.upstreams.FetchSiteCostsForDate(ctx, userID, adminAccountID, date)
	if err != nil {
		return AccountStatsRefreshResponse{}, err
	}
	keys, err := s.keyUsageForDate.KeyUsageForDate(ctx, userID, adminAccountID, date)
	if err != nil {
		return AccountStatsRefreshResponse{}, err
	}
	runs := buildAccountKeyCostRuns(userID, adminAccountID, runID, date, siteTotals, keys, time.Now().UTC())
	result := AccountStatsRefreshResponse{Date: date, SnapshotRunID: runID, ExpectedSites: len(runs), Quality: KeyCostQualityComplete}
	for _, run := range runs {
		if run.Complete {
			result.CompletedSites++
			continue
		}
		if run.Quality == KeyCostQualityMismatch {
			result.Quality = KeyCostQualityMismatch
		} else if result.Quality == KeyCostQualityComplete {
			result.Quality = KeyCostQualityMissing
		}
	}
	if keys.ExpectedSites != len(runs) || keys.CompletedSites != keys.ExpectedSites {
		if result.Quality == KeyCostQualityComplete {
			result.Quality = KeyCostQualityMissing
		}
	}
	if len(runs) == 0 && keys.ExpectedSites > 0 {
		return AccountStatsRefreshResponse{}, errors.New("account stats refresh has no matching site totals")
	}
	if result.Quality != KeyCostQualityComplete || result.CompletedSites != result.ExpectedSites {
		return result, nil
	}
	var stats []AccountDailyStat
	if s.accountStats != nil {
		targets, targetErr := s.accountStats.ListAutomaticAccountTargets(ctx, userID, adminAccountID, date)
		if targetErr != nil {
			return AccountStatsRefreshResponse{}, targetErr
		}
		result.ExpectedAccounts = len(targets)
		if len(targets) > 0 {
			record, sessionErr := s.store.Get(ctx, userID, adminAccountID)
			if sessionErr != nil || record == nil || !record.Session.IsAuthenticated() {
				result.Quality = KeyCostQualityMissing
				return result, nil
			}
			session, sessionErr := s.freshAdminSession(ctx, userID, adminAccountID, record)
			if sessionErr != nil {
				result.Quality = KeyCostQualityMissing
				return result, nil
			}
			var accountQuality string
			stats, accountQuality = s.buildAutomaticAccountStatsForTargets(ctx, userID, adminAccountID, date, runs, session, targets)
			result.CompletedAccounts = len(stats)
			if accountQuality != KeyCostQualityComplete {
				result.Quality = KeyCostQualityMissing
				return result, nil
			}
		}
	}
	if err := s.keyCostRuns.PublishAccountStatsRefresh(ctx, runs, stats); err != nil {
		return AccountStatsRefreshResponse{}, err
	}
	return result, nil
}
