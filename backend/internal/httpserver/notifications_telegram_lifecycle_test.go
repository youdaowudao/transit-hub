package httpserver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
)

type notificationEligibilityFunc func(context.Context, string, string, string) (bool, error)

func (f notificationEligibilityFunc) IsSiteNotificationEnabled(ctx context.Context, u, w, s string) (bool, error) {
	return f(ctx, u, w, s)
}

type notificationSiteReaderFunc func(context.Context, string) (*upstream.Site, error)

func (f notificationSiteReaderFunc) GetSite(ctx context.Context, id string) (*upstream.Site, error) {
	return f(ctx, id)
}

type notificationStrategyReaderFunc func(context.Context, string, string) (settings.StrategySettings, error)

func (f notificationStrategyReaderFunc) GetStrategyForWorkspace(ctx context.Context, u, w string) (settings.StrategySettings, error) {
	return f(ctx, u, w)
}

type notificationFakeSend struct {
	ctx                      context.Context
	user, workspace, message string
	bots                     []string
	format                   settings.NotificationTemplateFormat
}
type notificationFakeSender struct {
	sends  []notificationFakeSend
	onSend func()
}

func (f *notificationFakeSender) SendFormattedToWorkspaceBots(ctx context.Context, u, w string, bots []string, message string, format settings.NotificationTemplateFormat) {
	f.sends = append(f.sends, notificationFakeSend{ctx: ctx, user: u, workspace: w, bots: append([]string(nil), bots...), message: message, format: format})
	if f.onSend != nil {
		f.onSend()
	}
}

func notificationLifecycleStrategy() settings.StrategySettings {
	return settings.StrategySettings{EnableBalanceWarning: true, BalanceNotifyBotIDs: []string{"balance-bot"}, DefaultBalanceThreshold: 10, BalanceTemplate: "balance:{siteName}/{balance}/{threshold}", EnableMultiplierAlert: true, MultiplierNotifyBotIDs: []string{"rate-bot"}, MultiplierTemplate: "rate:{groupName}/{oldRate}/{newRate}/{changeDirection}"}
}
func notificationLifecycleMetrics(rate float64) upstream.Metrics {
	return upstream.Metrics{Balance: upstream.MetricValue{Value: cleanupNotificationRate(1)}, Groups: []upstream.GroupInfo{{ID: "g1", Name: "one", Multiplier: cleanupNotificationRate(rate)}, {ID: "g2", Name: "two", Multiplier: cleanupNotificationRate(rate)}}}
}
func notificationClearCooldown(t *testing.T, id string) {
	t.Helper()
	clear := func() {
		balanceAlertTracker.mu.Lock()
		delete(balanceAlertTracker.lastSent, id)
		balanceAlertTracker.mu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}
func notificationHasCooldown(id string) bool {
	balanceAlertTracker.mu.Lock()
	defer balanceAlertTracker.mu.Unlock()
	_, ok := balanceAlertTracker.lastSent[id]
	return ok
}

func TestNotificationAPITelegramLifecycleSkipsDoNotOccupyBalanceCooldown(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		reader siteNotificationEligibilityReader
	}{
		{"disabled", notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) { return false, nil })},
		{"missing", notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) { return false, nil })},
		{"read-error", notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) {
			return false, errors.New("isolated qualification read failed")
		})},
		{"unconfigured", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			id := "lifecycle-balance-" + scenario.name
			notificationClearCooldown(t, id)
			sender := &notificationFakeSender{}
			sites := notificationSiteReaderFunc(func(_ context.Context, id string) (*upstream.Site, error) {
				return &upstream.Site{ID: id, UserID: "user", AdminAccountID: "workspace", RechargeRate: 2}, nil
			})
			strategy := notificationLifecycleStrategy()
			old, next := notificationLifecycleMetrics(1), notificationLifecycleMetrics(2)
			checkBalanceWarning(t.Context(), sender, sites, scenario.reader, strategy, "user", "workspace", id, "site", old, next)
			checkMultiplierChanges(t.Context(), sender, scenario.reader, strategy, "user", "workspace", id, "site", old, next)
			if len(sender.sends) != 0 || notificationHasCooldown(id) {
				t.Fatal("ineligible or unreadable lifecycle must not send or consume balance cooldown")
			}
			eligible := notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) { return true, nil })
			checkBalanceWarning(t.Context(), sender, sites, eligible, strategy, "user", "workspace", id, "site", next, next)
			if len(sender.sends) != 1 || !notificationHasCooldown(id) || !strings.Contains(sender.sends[0].message, "2.00/10.00") {
				t.Fatal("later valid low balance must retain original conversion, template and cooldown behavior")
			}
			checkBalanceWarning(t.Context(), sender, sites, eligible, strategy, "user", "workspace", id, "site", next, next)
			if len(sender.sends) != 1 {
				t.Fatal("original cooldown must still suppress repeat low balance")
			}
		})
	}
}

func TestNotificationAPITelegramMultiplierRechecksEachGroupAndStopsAtFirstFailure(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		enabled bool
		err     error
	}{{"disabled", false, nil}, {"read-error", false, errors.New("qualification failed")}} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			ctx := context.WithValue(t.Context(), struct{}{}, "original-send-context")
			reader := notificationEligibilityFunc(func(got context.Context, u, w, s string) (bool, error) {
				if got != ctx || u != "user" || w != "workspace" || s != "site" {
					t.Fatal("qualification lost original scope or parent context")
				}
				calls++
				if calls == 1 {
					return true, nil
				}
				return scenario.enabled, scenario.err
			})
			sender := &notificationFakeSender{}
			old, next := notificationLifecycleMetrics(1), notificationLifecycleMetrics(2)
			old.Groups = append(old.Groups, upstream.GroupInfo{ID: "g3", Name: "three", Multiplier: cleanupNotificationRate(1)})
			next.Groups = append(next.Groups, upstream.GroupInfo{ID: "g3", Name: "three", Multiplier: cleanupNotificationRate(2)})
			strategy := notificationLifecycleStrategy()
			checkMultiplierChanges(ctx, sender, reader, strategy, "user", "workspace", "site", "site-name", old, next)
			if calls != 2 || len(sender.sends) != 1 {
				t.Fatal("each pending group needs a read; first failure must stop remaining group checks and sends")
			}
			got := sender.sends[0]
			if got.ctx != ctx || got.user != "user" || got.workspace != "workspace" || !reflect.DeepEqual(got.bots, strategy.MultiplierNotifyBotIDs) || got.format != strategy.MultiplierTemplateFormat || !strings.Contains(got.message, "one/1.0000/2.0000/上升") {
				t.Fatal("send context, scope, selected robots, format or template changed")
			}
		})
	}
}

func TestNotificationAPITelegramMultiplierFailedTransitionIsNotReplayedByUnchangedSync(t *testing.T) {
	calls := 0
	reader := notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("first transition qualification read failed")
		}
		return true, nil
	})
	sender := &notificationFakeSender{}
	a, b := notificationLifecycleMetrics(1), notificationLifecycleMetrics(2)
	strategy := notificationLifecycleStrategy()
	checkMultiplierChanges(t.Context(), sender, reader, strategy, "user", "workspace", "site", "site", a, b)
	checkMultiplierChanges(t.Context(), sender, reader, strategy, "user", "workspace", "site", "site", b, b)
	if calls != 1 || len(sender.sends) != 0 {
		t.Fatal("an unchanged B-to-B sync must not invent a replay of the skipped A-to-B Telegram notification")
	}
	checkMultiplierChanges(t.Context(), sender, reader, strategy, "user", "workspace", "site", "site", b, notificationLifecycleMetrics(3))
	if calls != 3 || len(sender.sends) != 2 {
		t.Fatal("new genuine later changes must still notify once per changed group")
	}
}

func TestNotificationAPITelegramSkipKeepsActualAfterSyncPricingOrder(t *testing.T) {
	for _, mode := range []string{"disabled", "error", "parent-deadline"} {
		t.Run(mode, func(t *testing.T) {
			id := "after-sync-lifecycle-" + mode
			notificationClearCooldown(t, id)
			ctx := t.Context()
			cancel := func() {}
			if mode == "parent-deadline" {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
			}
			defer cancel()
			var order []string
			strategies := notificationStrategyReaderFunc(func(context.Context, string, string) (settings.StrategySettings, error) {
				order = append(order, "settings")
				return notificationLifecycleStrategy(), nil
			})
			sites := notificationSiteReaderFunc(func(_ context.Context, id string) (*upstream.Site, error) {
				order = append(order, "balance-site")
				return &upstream.Site{ID: id, UserID: "user", AdminAccountID: "workspace", RechargeRate: 1}, nil
			})
			reader := notificationEligibilityFunc(func(ctx context.Context, _, _, _ string) (bool, error) {
				order = append(order, "eligibility")
				if mode == "parent-deadline" {
					<-ctx.Done()
					return false, ctx.Err()
				}
				if mode == "error" {
					return false, errors.New("isolated read error")
				}
				return false, nil
			})
			sender := &notificationFakeSender{}
			pricing := func(got context.Context, u, w, s, n string, old, next upstream.Metrics) {
				order = append(order, "pricing")
				if got != ctx || u != "user" || w != "workspace" || s != id || n != "site" || *old.Groups[0].Multiplier != 1 || *next.Groups[0].Multiplier != 2 {
					t.Fatal("pricing inputs changed after skipped notifications")
				}
			}
			started := time.Now()
			runSiteSyncWorkspaceActions(ctx, strategies, sender, sites, reader, pricing, "user", "workspace", id, "site", notificationLifecycleMetrics(1), notificationLifecycleMetrics(2))
			if fmt.Sprint(order) != "[settings balance-site eligibility eligibility pricing]" || len(sender.sends) != 0 || notificationHasCooldown(id) {
				t.Fatalf("notification skip short-circuited the real workspace callback order: %v", order)
			}
			if mode == "parent-deadline" && time.Since(started) > time.Second {
				t.Fatal("cancelled parent qualification must not hold up the callback")
			}
		})
	}
}

func TestNotificationAPITelegramBalanceLateDeleteMissingOrWrongCacheNeverPanicsOrSends(t *testing.T) {
	for _, scenario := range []struct {
		name string
		site *upstream.Site
		err  error
	}{
		{name: "missing-cache"},
		{name: "cache-read-error", err: errors.New("cache read failed")},
		{name: "other-user", site: &upstream.Site{ID: "late-deleted", UserID: "someone-else", AdminAccountID: "workspace"}},
		{name: "other-workspace", site: &upstream.Site{ID: "late-deleted", UserID: "user", AdminAccountID: "different"}},
		{name: "different-id", site: &upstream.Site{ID: "other", UserID: "user", AdminAccountID: "workspace"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			notificationClearCooldown(t, "late-deleted")
			for _, enabled := range []bool{false, true} {
				sender := &notificationFakeSender{}
				reader := notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) { return enabled, nil })
				sites := notificationSiteReaderFunc(func(context.Context, string) (*upstream.Site, error) { return scenario.site, scenario.err })
				checkBalanceWarning(t.Context(), sender, sites, reader, notificationLifecycleStrategy(), "user", "workspace", "late-deleted", "site", notificationLifecycleMetrics(1), notificationLifecycleMetrics(2))
				if len(sender.sends) != 0 || notificationHasCooldown("late-deleted") {
					t.Fatal("missing, unreadable or wrongly scoped cached site must not panic, send or occupy cooldown even if persisted eligibility is stale")
				}
			}
		})
	}
}
