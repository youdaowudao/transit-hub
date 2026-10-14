package connection_health

import (
	"encoding/json"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func TestGroupRatesWarningAdditionalSuccessStageAndTerminalKeepIndependentStatus(t *testing.T) {
	warning := &upstream.GroupRatesWarning{Message: "网站的本地倍率保存结果尚未确认", Detail: "当前不能确认哪些记录已更新或清理", Next: "请核查数据库提交结果后再同步"}
	completed, issues := adminGroupsRefreshProgressForSyncSites([]upstream.SyncSiteResult{{SiteID: "site", Status: "success", GroupRatesWarning: warning}}, nil, "")
	if completed != 1 || len(issues) != 1 || issues[0].Status != "success" || issues[0].Phase != "group_rates" || issues[0].ErrorKey != "" || issues[0].GroupRatesWarning != warning {
		t.Fatal("successful site warning was dropped or reclassified as health failure")
	}
	run := newUnitRefreshRun(t)
	defer run.cancel()
	run.publishStage(adminGroupsRefreshStageSiteSync, completed, 1, nil, issues)
	run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 1, 1, nil, nil)
	groups := []AdminGroupHealth{}
	run.publishTerminal(adminGroupsRefreshTerminal{Status: "success", Groups: &groups, Refresh: AdminGroupsRefreshSummary{State: "success"}})
	snapshot := run.latest()
	if snapshot.Terminal == nil || snapshot.Terminal.Status != "success" || snapshot.Terminal.Refresh.State != "success" || len(snapshot.Issues) != 1 || snapshot.Issues[0].GroupRatesWarning == nil {
		t.Fatal("terminal lost multiplier explanation or falsely failed independent refresh")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || !strings.Contains(string(raw), "groupRatesWarning") || !strings.Contains(string(raw), warning.Detail) {
		t.Fatal("terminal DTO dropped human-readable warning")
	}
}
