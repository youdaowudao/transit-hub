package connection_health

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// Account identity uses TrimSpace even when older checkpoint keys differ.
// Every alias must participate in the same keep-or-clear decision.
func TestActionCheckpointCleanupAliasesKeepBothKinds(t *testing.T) {
	for _, mode := range []string{"target_sending", "priority_prepared", "target_updated_equal", "priority_updated_after", "idle", "overdue_sender"} {
		for _, entry := range []string{"repository", "sweep"} {
			t.Run(mode+"/"+entry, func(t *testing.T) {
				s, repo, _, now, inventory := actionCleanupHarness()
				inventory.visible["43"] = "其他账号"
				priority := actionCleanupPair(" 42 ", *now)
				priority.Target = nil
				target := actionCleanupPair("42", *now)
				target.Priority = nil
				extra := actionCleanupPair("\t42\n", *now)
				extra.Target = nil
				switch mode {
				case "target_sending":
					actionCleanupPending(&target, ActionKindTarget, DispatchSending, now.Add(-time.Minute))
				case "priority_prepared":
					actionCleanupPending(&priority, ActionKindPriority, DispatchPrepared, now.Add(-time.Minute))
				case "target_updated_equal":
					target.Target.UpdatedAt = *now
				case "priority_updated_after":
					extra.Priority.UpdatedAt = now.Add(time.Second)
				case "overdue_sender":
					actionCleanupPending(&target, ActionKindTarget, DispatchSending, now.Add(-6*time.Minute))
				}
				for _, pair := range []RemoteActionCheckpoints{priority, target, extra} {
					actionCleanupStore(repo, pair)
				}
				// Same ID outside this scope and a distinct ID must remain untouched.
				for _, foreign := range []string{"user", "workspace", "platform", "account"} {
					pair := actionCleanupPair(" 42 ", *now)
					switch foreign {
					case "user":
						pair.Priority.UserID, pair.Target.UserID = "other", "other"
					case "workspace":
						pair.Priority.AdminAccountID, pair.Target.AdminAccountID = "other", "other"
						pair.Priority.TargetID, pair.Target.TargetID = "sub2api:other:42", "sub2api:other:42"
					case "platform":
						pair.Priority.TargetID, pair.Target.TargetID = "new-api:w:42", "new-api:w:42"
					case "account":
						pair.Priority.TargetID, pair.Target.TargetID = "sub2api:w:43", "sub2api:w:43"
					}
					actionCleanupStore(repo, pair)
				}
				beforePriority, beforeTarget := make(map[string]PrioritySyncState), make(map[string]TargetActionState)
				for key, value := range repo.priorityStates {
					beforePriority[key] = value
				}
				for key, value := range repo.targetActionStates {
					beforeTarget[key] = value
				}
				clear := mode == "idle" || mode == "overdue_sender"
				if entry == "repository" {
					removed, err := repo.ClearDeletedAccountCheckpoint(context.Background(), RemoteActionScope{"u", "w", "sub2api:w:42"}, *now, *now)
					if err != nil || removed != clear {
						t.Fatalf("alias decision removed=%t want=%t err=%v", removed, clear, err)
					}
				} else {
					s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", inventory)
				}
				if !clear {
					if !reflect.DeepEqual(beforePriority, repo.priorityStates) || !reflect.DeepEqual(beforeTarget, repo.targetActionStates) {
						t.Fatal("a protected alias must preserve every checkpoint and original value")
					}
					return
				}
				for key, value := range beforePriority {
					id, sameWorkspace := scopedActionAccountID(value.TargetID, "w")
					if value.UserID == "u" && value.AdminAccountID == "w" && sameWorkspace && id == "42" {
						delete(beforePriority, key)
					}
				}
				for key, value := range beforeTarget {
					id, sameWorkspace := scopedActionAccountID(value.TargetID, "w")
					if value.UserID == "u" && value.AdminAccountID == "w" && sameWorkspace && id == "42" {
						delete(beforeTarget, key)
					}
				}
				if !reflect.DeepEqual(beforePriority, repo.priorityStates) || !reflect.DeepEqual(beforeTarget, repo.targetActionStates) {
					t.Fatal("cleanup must remove all aliases together and preserve every unrelated row")
				}
			})
		}
	}
}

func TestActionDiagnosticsAliasesKeepOlderRisk(t *testing.T) {
	now := actionCleanupNow()
	for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
		for _, reason := range []string{"uncertain", "conflict", "legacy", "overdue"} {
			for _, newest := range []RemoteDispatchPhase{"", DispatchSending, DispatchConfirmedApplied} {
				t.Run(kind+"/"+reason+"/"+string(newest), func(t *testing.T) {
					repo := newFakeRepository()
					older, latest := actionCleanupPair(" 42 ", now), actionCleanupPair("42", now)
					if kind == ActionKindPriority {
						older.Target, latest.Target = nil, nil
					} else {
						older.Priority, latest.Priority = nil, nil
					}
					phase := DispatchUncertain
					if reason == "conflict" || reason == "overdue" || reason == "legacy" {
						phase = DispatchConfirmedApplied
					}
					age := time.Minute
					if reason == "overdue" {
						age = 6 * time.Minute
					}
					actionCleanupPending(&older, kind, phase, now.Add(-age))
					if reason == "conflict" {
						if kind == ActionKindPriority {
							older.Priority.Conflict = true
						} else {
							older.Target.Conflict = true
						}
					}
					if reason == "legacy" {
						if kind == ActionKindPriority {
							older.Priority.PendingDispatchID = ""
						} else {
							older.Target.PendingDispatchID = ""
						}
					}
					if newest != "" {
						actionCleanupPending(&latest, kind, newest, now)
					}
					if latest.Priority != nil {
						latest.Priority.UpdatedAt = now
					}
					if latest.Target != nil {
						latest.Target.UpdatedAt = now
					}
					actionCleanupStore(repo, older)
					actionCleanupStore(repo, latest)
					s := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, actionNow: func() time.Time { return now }}
					actionCleanupRemember(s, now, true, upstream.AdminGroupAccountInfo{ID: "42", Name: "旧风险账号"})
					got, err := s.PrioritySyncStatus(context.Background(), "u")
					want := reason
					if reason == "overdue" && newest != "" {
						want = "dual_claim"
					}
					if err != nil || len(got.ActionDiagnostics) != 1 {
						t.Fatalf("older risk must remain one visible account row: %+v err=%v", got, err)
					}
					d := got.ActionDiagnostics[0]
					if d.AccountID != "42" || d.AccountName != "旧风险账号" || d.Reason != want || d.Reason == "pending" {
						t.Fatalf("all alias evidence must select %s, got %+v", want, d)
					}
				})
			}
		}
	}
}

func TestActionDiagnosticsAliasesDetailKeepsOlderRisk(t *testing.T) {
	for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
		for _, reason := range []string{"uncertain", "conflict", "legacy"} {
			t.Run(kind+"/"+reason, func(t *testing.T) {
				now := actionCleanupNow()
				repo := newFakeRepository()
				older, latest := actionCleanupPair(" 42 ", now), actionCleanupPair("42", now)
				if kind == ActionKindPriority {
					older.Target, latest.Target = nil, nil
				} else {
					older.Priority, latest.Priority = nil, nil
				}
				phase := DispatchUncertain
				if reason != "uncertain" {
					phase = DispatchConfirmedApplied
				}
				actionCleanupPending(&older, kind, phase, now.Add(-time.Minute))
				if reason == "conflict" {
					if kind == ActionKindPriority {
						older.Priority.Conflict = true
					} else {
						older.Target.Conflict = true
					}
				}
				if reason == "legacy" {
					if kind == ActionKindPriority {
						older.Priority.PendingDispatchID = ""
					} else {
						older.Target.PendingDispatchID = ""
					}
				}
				if latest.Priority != nil {
					latest.Priority.UpdatedAt = now
				}
				if latest.Target != nil {
					latest.Target.UpdatedAt = now
				}
				actionCleanupStore(repo, older)
				actionCleanupStore(repo, latest)
				s := &Service{
					repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, actionNow: func() time.Time { return now },
					mySites:        fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
					platformGroups: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g", Name: "分组"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g": {{ID: "42", Name: "旧风险账号", Status: "active", Schedulable: boolPointer(true)}}}},
				}
				got, err := s.AdminGroups(context.Background(), "u")
				if err != nil || len(got) != 1 || len(got[0].Accounts) != 1 {
					t.Fatalf("detail fixture groups=%+v err=%v", got, err)
				}
				pending := got[0].Accounts[0].RemoteActionPending
				if pending == nil || pending.Reason != reason {
					t.Fatalf("detail must retain older alias %s: %+v", reason, pending)
				}
			})
		}
	}
}

func TestActionDiagnosticsAliasesInvisibleLogUsesAccountIdentity(t *testing.T) {
	s, repo, _, now, inventory := actionCleanupHarness()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	for _, id := range []string{" 42 ", "42", "\t42\n"} {
		s.logInvisibleAction(RemoteActionScope{"u", "w", "sub2api:w:" + id})
	}
	if count := strings.Count(output.String(), RemoteActionTargetNotVisible); count != 1 {
		t.Fatalf("same account aliases must log once per hour, got %d", count)
	}
	actionCleanupStore(repo, actionCleanupPair(" 42 ", *now))
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", inventory)
	s.logInvisibleAction(RemoteActionScope{"u", "w", "sub2api:w:42"})
	if count := strings.Count(output.String(), RemoteActionTargetNotVisible); count != 2 {
		t.Fatalf("clearing an alias must reset the canonical account log clock, got %d", count)
	}
}
