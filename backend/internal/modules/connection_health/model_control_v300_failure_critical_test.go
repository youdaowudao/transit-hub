package connection_health

import (
	"strings"
	"testing"
)

// Primary-agent RED for the approved concrete failure-description requirement.
// Outcome and ownership assertions keep the existing readback-based semantics.
func TestModelControlV300CriticalWriteFailureKeepsConcreteReasonAndOriginalValues(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.verify("1")
	preview := f.preview("1", "A", "close")
	f.bulkCode = 400
	code, result := f.execute("1", "A", "close", preview)
	if code != 200 || result["outcome"] != "failed" {
		t.Fatal("readback must confirm the unchanged main-site mapping", code, result)
	}
	detail, ok := result["errorKey"].(string)
	if !ok || !strings.Contains(detail, "Invalid request") || !strings.Contains(detail, "写入") {
		t.Errorf("concrete sanitized main-site rejection and failed step lost: %q", detail)
	}
	if len(f.closed("1", "A")) != 0 {
		t.Fatal("rejected write must not create closed ownership")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mapping := f.accounts["1"]["credentials"].(map[string]any)["model_mapping"].(map[string]string)
	if mapping["a"] != "A" || mapping["a-alias"] != "A" || mapping["b"] != "B" {
		t.Fatal("failed write changed original entries", mapping)
	}
}

func TestModelControlV300CriticalReadbackFailureDescribesUnknownResult(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.verify("1")
	preview := f.preview("1", "A", "close")
	f.afterWrite = func() { f.mu.Lock(); f.failDetail = true; f.mu.Unlock() }
	code, result := f.execute("1", "A", "close", preview)
	if code != 200 || result["outcome"] != "unknown" {
		t.Fatal("readback failure must keep the sent action uncertain", code, result)
	}
	detail, ok := result["errorKey"].(string)
	if !ok || !strings.Contains(detail, "读回") || !strings.Contains(detail, "无法确认") || strings.Contains(detail, "没有修改主站") {
		t.Errorf("sent action needs its readback step and concrete uncertainty guidance: %q", detail)
	}
	if f.writeCount() != 1 {
		t.Fatal("unconfirmed action must not be resent", f.writeCount())
	}
}

func TestModelControlV300CriticalPreparationReadFailureDescribesUnsentAction(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.verify("1")
	item := f.item("1", "A")
	f.failDetail = true
	code, result := f.request("POST", "model-control/preview", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "operation": "close", "basis": item["basis"]})
	message, _ := result["message"].(string)
	if code == 200 || !strings.Contains(message, "读取") || !strings.Contains(message, "尚未") || !strings.Contains(message, "503") || !strings.Contains(message, "具体原因") {
		t.Errorf("preparation failure must name the read step, unknown actual cause and unsent action: status=%d message=%q", code, message)
	}
	if f.writeCount() != 0 {
		t.Fatal("failed preparation sent a main-site write", f.writeCount())
	}
}
