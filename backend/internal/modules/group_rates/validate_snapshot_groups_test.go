package group_rates

import (
	"context"
	"testing"
)

// 20261016/4：倍率保存时的第二道名字检查。有编号的分组按编号对应，名字首尾空白只是显示差异；
// 没有编号时名字就是身份，仍然拒绝首尾空白。
func TestValidateSnapshotGroupsEdgeWhitespaceRules(t *testing.T) {
	rate := 1.0
	for _, tc := range []struct {
		name  string
		group SnapshotGroup
		ok    bool
	}{
		{"V1 trailing space with id", SnapshotGroup{ID: "9", Name: "Claude-Max ", Multiplier: &rate}, true},
		{"V2 trailing space without id", SnapshotGroup{ID: "", Name: "a ", Multiplier: &rate}, false},
		{"V3 blank name with id", SnapshotGroup{ID: "9", Name: "   ", Multiplier: &rate}, false},
		{"V4 id with edge space", SnapshotGroup{ID: " 9", Name: "a", Multiplier: &rate}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSnapshotGroups([]SnapshotGroup{tc.group})
			if (err == nil) != tc.ok {
				t.Fatalf("want ok=%v, got err=%v", tc.ok, err)
			}
		})
	}
}

// V5：真实数据库。名字带尾空格的有编号分组按原名保存，并能按编号查到历史。
func TestValidateSnapshotGroupsTrailingSpaceSavedPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	groups := []SnapshotGroup{cleanupGroup("9", "Claude-Max ", 1)}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err != nil {
		t.Fatalf("trailing-space group with id must be saved: %v", err)
	}
	var name string
	if err := pool.QueryRow(context.Background(), `SELECT group_name FROM group_rate_snapshots WHERE site_id='site-a' AND group_id='9' ORDER BY created_at DESC LIMIT 1`).Scan(&name); err != nil || name != "Claude-Max " {
		t.Fatalf("saved name=%q err=%v", name, err)
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "9", "")
	if err != nil || len(history) == 0 || history[0].GroupName != "Claude-Max " {
		t.Fatalf("history by id: rows=%d err=%v", len(history), err)
	}
}
