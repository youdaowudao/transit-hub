package engine

import (
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

// 固定判定标记和题库编号由程序产生，保留它们才能在短密钥脱敏后继续补测。
func restoreModelSummary(source, cleaned map[string]any) {
	pairs := [][2]map[string]any{
		{asMap(source["knowledge"]), asMap(cleaned["knowledge"])},
		{asMap(asMap(source["verdict"])["knowledge"]), asMap(asMap(cleaned["verdict"])["knowledge"])},
	}
	for index, pair := range pairs {
		before, after := pair[0], pair[1]
		beforeReview, afterReview := asMap(before["boundary_review"]), asMap(after["boundary_review"])
		if beforeReview["method"] != knowledge.ModelGroupMethod || afterReview == nil {
			continue
		}
		afterReview["method"] = knowledge.ModelGroupMethod
		for _, field := range []string{"status", "reason"} {
			switch value := beforeReview[field].(string); value {
			case "unconfirmed", "complete", "invalid", "pending", "missing_proof", "retry_pending":
				afterReview[field] = value
			}
		}
		// The saved question group is program data; restore it only when every ID is a known event.
		if name, _ := beforeReview["group"].(string); knowledge.KnownGroupName(name) {
			afterReview["group"] = name
		}
		for _, field := range []string{"know", "newer"} {
			list, _ := beforeReview[field].([]any)
			known := len(list) > 0
			for _, v := range list {
				id, _ := v.(string)
				if _, ok := knowledge.Anchor(id); !ok {
					known = false
				}
			}
			if known {
				afterReview[field] = list
			}
		}
		after["boundary_review"] = afterReview
		switch value := before["status"].(string); value {
		case "unknown", "matched", "mismatch":
			after["status"] = value
		}
		beforeRows, _ := before["rows"].([]any)
		afterRows, _ := after["rows"].([]any)
		if len(beforeRows) == len(afterRows) {
			for i := range beforeRows {
				original, target := asMap(beforeRows[i]), asMap(afterRows[i])
				if id, ok := original["id"].(string); ok {
					if _, known := knowledge.Anchor(id); known {
						target["id"] = id
					}
				}
				restoreModelKind(original, target)
				afterRows[i] = target
			}
			after["rows"] = afterRows
		}
		if index == 0 {
			cleaned["knowledge"] = after
		} else {
			verdict := asMap(cleaned["verdict"])
			verdict["knowledge"] = after
			cleaned["verdict"] = verdict
		}
	}
}

func restoreModelEvidence(result itemResult, cleaned any, questionID string) {
	source := asMap(asMap(result.Evidence)["row"])
	root, ok := cleaned.(map[string]any)
	if !ok || source["id"] != strings.TrimPrefix(questionID, "knowledge:") {
		return
	}
	evidence := asMap(root["evidence"])
	row := asMap(evidence["row"])
	if row == nil {
		return
	}
	row["id"] = source["id"]
	restoreModelKind(source, row)
	evidence["row"] = row
	root["evidence"] = evidence
}

func restoreModelKind(source, target map[string]any) {
	switch kind := source["kind"].(string); kind {
	case "know", "unknown", "wrong", "error", "invalid":
		target["kind"] = kind
	}
}
