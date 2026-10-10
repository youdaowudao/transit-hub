package connection_health

import (
	"fmt"
	"strings"
	"testing"
)

func TestC3ModelControlREDWriteProtectionBeforeStorage(t *testing.T) {
	f := newC3REDFixture(t)
	f.service.backgroundTasksDisabled = true
	for _, route := range []struct{ method, path string }{
		{"PUT", "settings"}, {"POST", "managed"}, {"DELETE", "managed"},
		{"POST", "preview"}, {"POST", "close"}, {"POST", "restore"}, {"POST", "add"}, {"POST", "close-account"}, {"POST", "verify"},
	} {
		code, _ := f.request(route.method, "model-control/"+route.path, map[string]any{})
		if code != 409 {
			t.Errorf("APIOnly %s %s got=%d want=409", route.method, route.path, code)
		}
	}
	if f.writeCount() != 0 || f.scheduleWrites != 0 {
		t.Fatal("APIOnly wrote main site")
	}
}

func TestC3ModelControlREDChangedBasisRejectsBeforeRemoteWrite(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.round("1", "A", 3, 0, 0)
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "modelControlBasisChanged") || f.writeCount() != 0 {
		t.Fatalf("old basis was accepted: %d %v", code, result)
	}
}

func TestC3ModelControlREDRestoreHealthUsesCurrentProbeModel(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(fmt.Sprint(current), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			f.close("1", "A")
			_, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_states(connection_id,model_name,user_id,admin_account_id,upstream_site_id,upstream_group_name,state) VALUES($1,'A',$2,'ws1','fixture','fixture','suspended')`, c3REDTarget("1"), c3REDUser)
			if err != nil {
				t.Fatal(err)
			}
			if current {
				_, err = f.pool.Exec(t.Context(), `INSERT INTO connection_health_policies(id,user_id,admin_account_id,name) VALUES('c3-health',$1,'ws1','C3 health')`, c3REDUser)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.pool.Exec(t.Context(), `INSERT INTO connection_health_model_targets(id,policy_id,user_id,admin_account_id,model_name,provider_family) VALUES('c3-health-model','c3-health',$1,'ws1','A','openai')`, c3REDUser)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.pool.Exec(t.Context(), `INSERT INTO connection_health_policy_assignments(id,user_id,admin_account_id,target_id,policy_id) VALUES('c3-health-assignment',$1,'ws1',$2,'c3-health')`, c3REDUser, c3REDTarget("1"))
				if err != nil {
					t.Fatal(err)
				}
			}
			p := f.preview("1", "A", "restore")
			if current {
				if p["blockReasonKey"] == "" || p["blockReasonKey"] == nil || f.writeCount() != 1 {
					t.Fatal("current suspended probe model restored")
				}
				return
			}
			code, result := f.execute("1", "A", "restore", p)
			if code != 200 || result["outcome"] != "restored" || f.writeCount() != 2 {
				t.Fatalf("obsolete health state blocked restore: %d %v", code, result)
			}
		})
	}
}

func TestC3ModelControlREDRestoreInventoryIncompleteNeverWrites(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.close("1", "A")
	f.mu.Lock()
	f.failInventory = true
	f.mu.Unlock()
	p := f.preview("1", "A", "restore")
	if p["blockReasonKey"] == "" || p["blockReasonKey"] == nil || f.writeCount() != 1 {
		t.Fatal("incomplete inventory allowed restore")
	}
}

func TestC3ModelControlREDVerifyIgnoresUnrelatedMappingChange(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.afterWrite = func() { f.mu.Lock(); f.failDetail = true; f.mu.Unlock() }
	result := f.close("1", "A")
	if result["outcome"] != "unknown" {
		t.Fatal("failed readback returned success")
	}
	f.mapping("1", map[string]string{"b": "B", "c": "C"})
	f.mu.Lock()
	f.failDetail = false
	f.mu.Unlock()
	f.verify("1")
	if len(f.closed("1", "A")) != 2 {
		t.Fatal("unrelated added model invalidated ownership")
	}
}

func TestC3ModelControlREDCurrentRoundManualJudgmentChangesDecision(t *testing.T) {
	f := newC3REDFixture(t)
	old := f.round("1", "A", 3, 0, 0)
	current := f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	_, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_records SET answer_judgment='incorrect' WHERE batch_id=$1`, old)
	if err != nil {
		t.Fatal(err)
	}
	if f.item("1", "A")["decision"] != "close_recommended" {
		t.Fatal("old-round judgment changed current decision")
	}
	_, err = f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_records SET answer_judgment='correct' WHERE batch_id=$1`, current)
	if err != nil {
		t.Fatal(err)
	}
	if f.item("1", "A")["decision"] != "usable" {
		t.Fatal("current-round judgment did not change decision")
	}
}

func TestC3ModelControlREDFilteredPageStillProjectsAccountPending(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.round("1", "B", 1, 2, 0)
	f.manage("1", "A")
	f.manage("1", "B")
	f.bulkCode = 502
	f.close("1", "A")
	page := f.expect("GET", "model-control/items?view=all&modelName=B", nil, 200)
	items := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("B filter: %v", page)
	}
	control := items[0].(map[string]any)["control"].(map[string]any)
	if control["accountPending"] == nil || control["accountPending"].(map[string]any)["modelName"] != "A" {
		t.Fatal("filtered row lost other model's account-level freeze")
	}
}
