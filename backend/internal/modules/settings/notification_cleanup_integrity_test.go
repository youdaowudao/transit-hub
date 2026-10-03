package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanupBackupBindsUnchangedSnapshotRowsAndPermittedChanges(t *testing.T) {
	original, err := PlanNotificationCleanup("user", "workspace", []CleanupRow{
		{Table: "strategy_settings", Before: json.RawMessage(`{"defaultBalanceThreshold":700.25,"enableBalanceWarning":true,"balanceNotifyBotIds":["retired"]}`)},
		{Table: "my_site_states", Before: json.RawMessage(`[{"ownGroup":"unchanged","fixedIncrease":0.125,"enableAutoPricing":true}]`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Snapshot) != 2 || len(original.Rows) != 1 {
		t.Fatal("backup must include the complete original snapshot, including unchanged rows")
	}
	if err := ValidateNotificationCleanupBackup(original, "user", "workspace", original.Digest); err != nil {
		t.Fatal("valid backup rejected")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CleanupPlan)
	}{
		{"before amount", func(p *CleanupPlan) {
			p.Rows[0].Before = json.RawMessage(`{"defaultBalanceThreshold":701.25,"enableBalanceWarning":true,"balanceNotifyBotIds":["retired"]}`)
		}},
		{"after amount", func(p *CleanupPlan) {
			p.Rows[0].After = json.RawMessage(`{"defaultBalanceThreshold":701.25,"enableBalanceWarning":false,"balanceNotifyBotIds":[],"balanceNotifyRecipientsInvalid":true}`)
		}},
		{"table scope", func(p *CleanupPlan) { p.Rows[0].Table = "my_site_states" }},
		{"row scope", func(p *CleanupPlan) { p.Rows[0].ID = "another" }},
		{"missing change", func(p *CleanupPlan) { p.Rows = nil }},
		{"unmodified snapshot amount", func(p *CleanupPlan) {
			p.Snapshot[1].Before = json.RawMessage(`[{"ownGroup":"unchanged","fixedIncrease":0.25,"enableAutoPricing":true}]`)
		}},
		{"snapshot missing", func(p *CleanupPlan) { p.Snapshot = nil }},
		{"workspace scope", func(p *CleanupPlan) { p.AdminAccountID = "another" }},
		{"summary", func(p *CleanupPlan) { p.Summary.RowsChanged++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(original)
			var backup CleanupPlan
			if err := json.Unmarshal(encoded, &backup); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&backup)
			err := ValidateNotificationCleanupBackup(backup, "user", "workspace", original.Digest)
			if err == nil || !strings.Contains(err.Error(), "backup integrity") {
				t.Fatal("backup corruption must fail with a safe integrity error")
			}
		})
	}
	if err := ValidateNotificationCleanupBackup(original, "user", "workspace", strings.Repeat("0", 64)); err == nil {
		t.Fatal("an editable backup digest label must not replace the independently reviewed digest")
	}
}

func TestCleanupPreviewDoesNotClassifyElementsOrUnitsAsCurrency(t *testing.T) {
	for _, content := range []string{"数组元素与业务单元", "返回 100 个元素", "已有 100元素", "第10单元", "返回2元组", "包含7元数据", "使用12元件"} {
		before, _ := json.Marshal(map[string]string{"balanceTemplate": content})
		plan, err := PlanNotificationCleanup("user", "workspace", []CleanupRow{{Table: "strategy_settings", Before: before}})
		if err != nil || plan.Summary.CustomLegacyTemplates != 0 || len(plan.Rows) != 0 {
			t.Fatal("non-currency 元 in element/unit vocabulary must not be reported or rewritten")
		}
	}
}

func TestCleanupEmptySnapshotHasStableBackupDigest(t *testing.T) {
	plan, err := PlanNotificationCleanup("user", "workspace", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNotificationCleanupBackup(plan, "user", "workspace", plan.Digest); err != nil {
		t.Fatal("empty canonical snapshot must remain a valid backup")
	}
}

func TestCleanupAmountUnitUsesVisibleHTMLTextAndPreservesCustomSource(t *testing.T) {
	for _, tc := range []struct {
		source string
		count  int
	}{
		{`<div><strong>{amount}</strong>&#160;<span>元</span></div>`, 1},
		{`<span data-unit="700元">元素与业务单元</span>`, 0},
		{`<script>price="700元"</script><p>业务单元</p>`, 0},
		{`<!-- {amount} 元 --><p>数组元素</p>`, 0},
		{`返回100<strong>元</strong>素`, 0},
	} {
		before, _ := json.Marshal(map[string]string{"balanceTemplate": tc.source})
		plan, err := PlanNotificationCleanup("user", "workspace", []CleanupRow{{Table: "strategy_settings", Before: before}})
		if err != nil || plan.Summary.CustomLegacyTemplates != tc.count || len(plan.Rows) != 0 {
			t.Fatal("HTML currency inspection must count visible amount units and retain the original custom source")
		}
	}
}

func TestCleanupAmountUnitUsesMarkdownDisplayTextWithoutChangingSource(t *testing.T) {
	for _, tc := range []struct {
		source string
		count  int
	}{
		{`__{balance}__ 元`, 1},
		{`***{threshold}*** 元`, 1},
		{`[**{price}**](https://fixture.invalid/path(nested)) 元`, 1},
		{`[{amount}](https://fixture.invalid "amount")&nbsp;元`, 1},
		{`[业务单元](https://fixture.invalid/700元)`, 0},
		{`**100元**素`, 0},
		{`<script>**{balance}** 元</script>业务单元`, 0},
	} {
		before, _ := json.Marshal(map[string]string{"balanceTemplate": tc.source})
		plan, err := PlanNotificationCleanup("user", "workspace", []CleanupRow{{Table: "strategy_settings", Before: before}})
		if err != nil || plan.Summary.CustomLegacyTemplates != tc.count || len(plan.Rows) != 0 {
			t.Fatal("Markdown amount inspection must count displayed units without rewriting custom source or counting destinations")
		}
	}
}
