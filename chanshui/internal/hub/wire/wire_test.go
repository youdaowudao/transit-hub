package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func limitFiles(overrides map[string]string) func(string) ([]byte, error) {
	files := map[string]string{"/proc/self/cgroup": "0::/user.slice/transithub.slice/transithub-chanshui.slice/task.service\n", "/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/task.service/memory.max": "67108864", "/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/task.service/memory.swap.max": "0", "/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/memory.max": "500000000", "/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/memory.swap.max": "0"}
	for k, v := range overrides {
		files[k] = v
	}
	return func(path string) ([]byte, error) {
		v, ok := files[path]
		if !ok {
			return nil, fmt.Errorf("missing %s", path)
		}
		return []byte(v), nil
	}
}
func TestActualCgroupHardLimitsRequiredNoUnlimitedFallback(t *testing.T) {
	l, e := readLimits("query", limitFiles(nil))
	if e != nil || l.TaskMax != 64<<20 || l.SliceMax != 500000000 {
		t.Fatal(l, e)
	}
	for _, overrides := range []map[string]string{{"/proc/self/cgroup": "0::/unrelated.service"}, {"/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/memory.max": "500000001"}, {"/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/task.service/memory.max": "max"}, {"/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/task.service/memory.max": "67108865"}, {"/sys/fs/cgroup/user.slice/transithub.slice/transithub-chanshui.slice/memory.swap.max": "1"}} {
		if _, e := readLimits("query", limitFiles(overrides)); e == nil {
			t.Fatal("unsafe limits accepted", overrides)
		}
	}
}
func TestHandshakeRequiresVerifiedLimitsBeforeContinue(t *testing.T) {
	env := `{"v":1,"operation":"read-settings","owner":{"userId":"u","workspaceId":"w"},"args":{}}`
	control := `{"v":1,"command":"continue"}`
	var out bytes.Buffer
	inspect := func(string) (Limits, error) { return Limits{64 << 20, 500000000, 0}, nil }
	got, _, e := handshake(strings.NewReader(env+"\n"+control+"\n"), &out, "query", inspect)
	if e != nil || got.Operation != "read-settings" || !strings.Contains(out.String(), `"event":"ready"`) {
		t.Fatal(got, e, out.String())
	}
	out.Reset()
	_, _, e = handshake(strings.NewReader(env+"\n"+control+"\n"), &out, "query", func(string) (Limits, error) { return Limits{}, fmt.Errorf("limit mismatch") })
	if e == nil || out.Len() != 0 {
		t.Fatal("continued without verified quota")
	}
	out.Reset()
	_, _, e = handshake(strings.NewReader(env+"\n"+`{"v":1,"command":"continue","credential":{"key":"not-allowed"}}`+"\n"), &out, "query", inspect)
	if e == nil {
		t.Fatal("secret accepted for history-only query")
	}
}
func TestWireUnknownFieldsLengthAndExactPayload(t *testing.T) {
	var v Envelope
	if Decode([]byte(`{"v":1,"unknown":"hidden"}`), &v) == nil {
		t.Fatal("unknown envelope field accepted")
	}
	if Decode([]byte(`{} {}`), &v) == nil {
		t.Fatal("trailing JSON accepted")
	}
	var out bytes.Buffer
	if e := Result(&out, 200, map[string]string{"message": "中文结果"}, ""); e != nil {
		t.Fatal(e)
	}
	decoder := json.NewDecoder(&out)
	var header struct {
		Length int `json:"length"`
	}
	if e := decoder.Decode(&header); e != nil {
		t.Fatal(e)
	}
	remaining, e := io.ReadAll(io.MultiReader(decoder.Buffered(), &out))
	if e != nil {
		t.Fatal(e)
	}
	remaining = bytes.TrimPrefix(remaining, []byte("\n"))
	if len(remaining) != header.Length || !json.Valid(remaining) {
		t.Fatal("payload length mismatch")
	}
	out.Reset()
	if Result(&out, 200, strings.Repeat("x", 16<<20), "") == nil {
		t.Fatal("oversized payload accepted")
	}
}
