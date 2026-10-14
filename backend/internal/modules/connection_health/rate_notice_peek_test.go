package connection_health

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func rateNoticePeekFixture() (*Service, *multiplierSnapshotEntry, []upstream.RateNoticeBinding, time.Time) {
	now := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	key := multiplierSnapshotKey("user", "workspace", "site")
	entry := &multiplierSnapshotEntry{
		workspaceKey: key, siteID: "site", userID: "user", adminAccountID: "workspace",
		platform: upstream.PlatformSub2API, bindingSignature: "key-1", keyIDs: []string{"key-1"}, generation: 4,
		status: "complete", fetchedAt: now.Add(-10 * time.Second), expiresAt: now.Add(50 * time.Second),
		lastAccessAt: now.Add(-5 * time.Second),
		keys:         map[string]upstreamKeyMetadata{"key-1": {id: "key-1", groupID: "group-1", groupName: "甲组"}},
		site:         multiplierSiteMetadata{groups: []upstream.GroupInfo{{ID: "group-1", Name: "甲组"}}},
	}
	service := &Service{multiplierNow: func() time.Time { return now }, multiplierSnapshots: map[string]*multiplierSnapshotEntry{key: entry}}
	bindings := []upstream.RateNoticeBinding{{AccountID: "account-1", AccountName: "登记账号", KeyID: "key-1", Status: "active", AdminPlatform: "sub2api"}}
	return service, entry, bindings, now
}

func rateNoticePeekAssertUnknown(t *testing.T, got map[string]upstream.RateNoticeKeyAssignment, key string) {
	t.Helper()
	assignment, exists := got[key]
	if !exists || assignment.Confirmed || assignment.GroupID != "" || assignment.GroupName != "" || strings.TrimSpace(assignment.Reason) == "" {
		t.Fatalf("unknown assignment must retain a reason and no confident group: %+v", assignment)
	}
}

func TestRateNoticePeekKeyAssignmentsUsesFullSixtySecondsWithoutRefresh(t *testing.T) {
	service, entry, bindings, now := rateNoticePeekFixture()
	for _, elapsed := range []time.Duration{0, 59*time.Second + 999*time.Millisecond, 60 * time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			entry.fetchedAt, entry.expiresAt = now.Add(-elapsed), now.Add(multiplierSnapshotTTL-elapsed)
			got := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
			if elapsed == 60*time.Second {
				rateNoticePeekAssertUnknown(t, got, "key-1")
				return
			}
			assignment := got["key-1"]
			if !assignment.Confirmed || assignment.GroupID != "group-1" || assignment.GroupName != "甲组" || !assignment.ConfirmedAt.Equal(entry.fetchedAt) {
				t.Fatalf("fresh assignment within full TTL was lost: %+v", assignment)
			}
		})
	}
	if entry.inFlight || entry.generation != 4 || entry.status != "complete" || !entry.lastAccessAt.Equal(now.Add(-5*time.Second)) || service.multiplierRefreshJobs != 0 {
		t.Fatal("pure assignment read modified the snapshot or started refresh work")
	}
}

func TestRateNoticePeekKeyAssignmentsRejectsMismatchedOwnershipAndBindings(t *testing.T) {
	cases := []struct {
		name                  string
		mutate                func(*multiplierSnapshotEntry)
		user, workspace, site string
		platform              upstream.Platform
		bindings              []upstream.RateNoticeBinding
	}{
		{name: "wrong-user", user: "other-user"},
		{name: "wrong-workspace", workspace: "other-workspace"},
		{name: "wrong-site", site: "other-site"},
		{name: "wrong-main-platform", platform: upstream.PlatformNewAPI, bindings: []upstream.RateNoticeBinding{{AccountID: "account-1", KeyID: "key-1"}}},
		{name: "entry-user", mutate: func(e *multiplierSnapshotEntry) { e.userID = "other-user" }},
		{name: "entry-workspace", mutate: func(e *multiplierSnapshotEntry) { e.adminAccountID = "other-workspace" }},
		{name: "entry-site", mutate: func(e *multiplierSnapshotEntry) { e.siteID = "other-site" }},
		{name: "entry-map-key", mutate: func(e *multiplierSnapshotEntry) {
			e.workspaceKey = multiplierSnapshotKey("other-user", "workspace", "site")
		}},
		{name: "entry-platform", mutate: func(e *multiplierSnapshotEntry) { e.platform = upstream.PlatformNewAPI }},
		{name: "changed-signature", mutate: func(e *multiplierSnapshotEntry) { e.bindingSignature = "key-1\x00key-old" }},
		{name: "changed-key-set", mutate: func(e *multiplierSnapshotEntry) { e.keyIDs = []string{"key-old"} }},
		{name: "added-binding", bindings: []upstream.RateNoticeBinding{{AccountID: "account-1", KeyID: "key-1"}, {AccountID: "account-2", KeyID: "key-2"}}},
		{name: "replaced-binding", bindings: []upstream.RateNoticeBinding{{AccountID: "account-1", KeyID: "key-2"}}},
	}
	for _, sample := range cases {
		t.Run(sample.name, func(t *testing.T) {
			service, entry, bindings, _ := rateNoticePeekFixture()
			if sample.mutate != nil {
				sample.mutate(entry)
			}
			user, workspace, site, platform := "user", "workspace", "site", upstream.PlatformSub2API
			if sample.user != "" {
				user = sample.user
			}
			if sample.workspace != "" {
				workspace = sample.workspace
			}
			if sample.site != "" {
				site = sample.site
			}
			if sample.platform != "" {
				platform = sample.platform
			}
			if sample.bindings != nil {
				bindings = sample.bindings
			}
			got := service.PeekKeyAssignments(user, workspace, site, platform, bindings)
			for _, binding := range bindings {
				rateNoticePeekAssertUnknown(t, got, binding.KeyID)
			}
			if entry.inFlight || service.multiplierRefreshJobs != 0 {
				t.Fatal("an unknown assignment started refresh work")
			}
		})
	}
}

func TestRateNoticePeekKeyAssignmentsConservativelyHandlesIncompleteDeletedAndConflictingKeys(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*multiplierSnapshotEntry)
	}{
		{"updating", func(e *multiplierSnapshotEntry) { e.status = multiplierResolutionUpdating }},
		{"partial", func(e *multiplierSnapshotEntry) { e.status = multiplierResolutionPartial }},
		{"stale", func(e *multiplierSnapshotEntry) { e.status = multiplierResolutionStale }},
		{"unavailable", func(e *multiplierSnapshotEntry) { e.status = "unavailable" }},
		{"empty-status", func(e *multiplierSnapshotEntry) { e.status = "" }},
		{"in-flight", func(e *multiplierSnapshotEntry) { e.inFlight = true }},
		{"failed-key", func(e *multiplierSnapshotEntry) { e.keyFailures = map[string]string{"key-1": "fixture read failed"} }},
		{"deleted-key", func(e *multiplierSnapshotEntry) { e.confirmedDeletedKeys = map[string]struct{}{"key-1": {}} }},
		{"missing-key", func(e *multiplierSnapshotEntry) { delete(e.keys, "key-1") }},
		{"wrong-key-id", func(e *multiplierSnapshotEntry) {
			e.keys["key-1"] = upstreamKeyMetadata{id: "key-other", groupID: "group-1", groupName: "甲组"}
		}},
		{"missing-group-identity", func(e *multiplierSnapshotEntry) { e.keys["key-1"] = upstreamKeyMetadata{id: "key-1"} }},
		{"ambiguous-group-id", func(e *multiplierSnapshotEntry) {
			e.site.groups = append(e.site.groups, upstream.GroupInfo{ID: "group-1", Name: "冲突组"})
		}},
		{"ambiguous-group-name", func(e *multiplierSnapshotEntry) {
			e.keys["key-1"] = upstreamKeyMetadata{id: "key-1", groupName: "甲组"}
			e.site.groups = append(e.site.groups, upstream.GroupInfo{ID: "group-2", Name: "甲组"})
		}},
		{"id-must-not-fall-back-to-name", func(e *multiplierSnapshotEntry) {
			e.keys["key-1"] = upstreamKeyMetadata{id: "key-1", groupID: "nonexistent-group", groupName: "甲组"}
		}},
	}
	for _, sample := range cases {
		t.Run(sample.name, func(t *testing.T) {
			service, entry, bindings, _ := rateNoticePeekFixture()
			sample.mutate(entry)
			got := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
			rateNoticePeekAssertUnknown(t, got, "key-1")
			if sample.name == "deleted-key" && !strings.Contains(got["key-1"].Reason, "删除") {
				t.Fatal("deleted key must explicitly explain deletion")
			}
		})
	}
	service, entry, bindings, _ := rateNoticePeekFixture()
	entry.bindingSignature, entry.keyIDs = "key-1\x00key-2", []string{"key-1", "key-2"}
	entry.keys["key-2"] = upstreamKeyMetadata{id: "key-2", groupID: "group-2", groupName: "乙组"}
	entry.site.groups = append(entry.site.groups, upstream.GroupInfo{ID: "group-2", Name: "乙组"})
	bindings = append(bindings, upstream.RateNoticeBinding{AccountID: "account-1", KeyID: "key-2", AdminPlatform: "sub2api"})
	got := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
	rateNoticePeekAssertUnknown(t, got, "key-1")
	rateNoticePeekAssertUnknown(t, got, "key-2")
}

func TestRateNoticePeekKeyAssignmentsCopiesSafeResultAndUsesCurrentBindingSet(t *testing.T) {
	service, entry, bindings, _ := rateNoticePeekFixture()
	entry.bindingSignature, entry.keyIDs = "key-1\x00key-2", []string{"key-1", "key-2"}
	entry.keys["key-2"] = upstreamKeyMetadata{id: "key-2", groupName: "仅名称组"}
	entry.site.groups = append(entry.site.groups, upstream.GroupInfo{Name: "仅名称组"})
	bindings = []upstream.RateNoticeBinding{{AccountID: "account-2", KeyID: "key-2"}, bindings[0], bindings[0]}
	before := append([]upstream.RateNoticeBinding(nil), bindings...)
	got := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
	if len(got) != 2 || !got["key-1"].Confirmed || !got["key-2"].Confirmed || got["key-2"].GroupID != "" || got["key-2"].GroupName != "仅名称组" || !reflect.DeepEqual(bindings, before) {
		t.Fatal("current bindings must be treated as a sorted unique set without changing caller input")
	}
	got["key-1"] = upstream.RateNoticeKeyAssignment{Confirmed: true, GroupID: "changed-in-caller"}
	again := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
	if again["key-1"].GroupID != "group-1" || entry.keys["key-1"].groupID != "group-1" {
		t.Fatal("returned results alias cached group metadata")
	}
	service.multiplierSnapshots = nil
	missing := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
	rateNoticePeekAssertUnknown(t, missing, "key-1")
	rateNoticePeekAssertUnknown(t, missing, "key-2")
	if service.multiplierSnapshots != nil || service.multiplierRefreshJobs != 0 {
		t.Fatal("cache miss must remain a pure local read")
	}
}

func TestRateNoticePeekKeyAssignmentsIgnoresInactiveConnectionsInCacheSignature(t *testing.T) {
	for _, status := range []string{"missing", "inactive"} {
		t.Run(status, func(t *testing.T) {
			service, entry, bindings, _ := rateNoticePeekFixture()
			bindings = append(bindings, upstream.RateNoticeBinding{AccountID: "account-2", KeyID: "key-2", Status: status, AdminPlatform: "sub2api"})
			got := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings)
			if !got["key-1"].Confirmed || got["key-1"].GroupID != "group-1" || got["key-2"].Confirmed {
				t.Fatal("nonactive connection polluted the current cached binding set or reused an old key assignment")
			}
			if entry.bindingSignature != "key-1" || len(entry.keyIDs) != 1 || service.multiplierRefreshJobs != 0 {
				t.Fatal("pure read changed cache signature or started refresh")
			}
		})
	}
	service, _, bindings, _ := rateNoticePeekFixture()
	bindings[0].Status = ""
	if got := service.PeekKeyAssignments("user", "workspace", "site", upstream.PlatformSub2API, bindings); !got["key-1"].Confirmed {
		t.Fatal("legacy empty connection status must preserve its recently confirmed key assignment")
	}
}
