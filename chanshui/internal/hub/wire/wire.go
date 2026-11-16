// Package wire implements the bounded pipe protocol, without a listening socket.
package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/hub/engine"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

type Envelope struct {
	V          int               `json:"v"`
	Operation  string            `json:"operation"`
	Owner      store.Owner       `json:"owner"`
	Args       json.RawMessage   `json:"args"`
	Deployment engine.Deployment `json:"deployment"`
}
type Continue struct {
	V          int                `json:"v"`
	Command    string             `json:"command"`
	Credential *engine.Credential `json:"credential,omitempty"`
}
type Limits struct {
	TaskMax  uint64 `json:"taskMax"`
	SliceMax uint64 `json:"sliceMax"`
	SwapMax  uint64 `json:"swapMax"`
}

func Decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return store.Fail(422, "请求内容不符合此操作格式："+e.Error(), "input")
	}
	var trailing any
	if e := d.Decode(&trailing); e != io.EOF {
		return store.Fail(422, "请求含多余JSON内容", "input")
	}
	return nil
}
func ReadLimits(role string) (Limits, error) { return readLimits(role, os.ReadFile) }
func readLimits(role string, readFile func(string) ([]byte, error)) (Limits, error) {
	raw, e := readFile("/proc/self/cgroup")
	if e != nil {
		return Limits{}, e
	}
	path := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			path = strings.TrimPrefix(line, "0::")
		}
	}
	if path == "" {
		return Limits{}, fmt.Errorf("当前进程没有cgroup v2归属，不能在无硬限制环境执行")
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	slicePath := ""
	for i, p := range parts {
		if p == "transithub-chanshui.slice" {
			slicePath = filepath.Join(append([]string{"/sys/fs/cgroup"}, parts[:i+1]...)...)
		}
	}
	if slicePath == "" {
		return Limits{}, fmt.Errorf("检测进程不在专用transithub-chanshui.slice，已拒绝无合计限制运行")
	}
	own := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"))
	read := func(dir, name string) (uint64, error) {
		b, e := readFile(filepath.Join(dir, name))
		if e != nil {
			return 0, e
		}
		n, e := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if e != nil {
			return 0, fmt.Errorf("%s不是有效硬限制（不能为max）", name)
		}
		return n, nil
	}
	var l Limits
	if l.TaskMax, e = read(own, "memory.max"); e != nil {
		return l, e
	}
	if l.SliceMax, e = read(slicePath, "memory.max"); e != nil {
		return l, e
	}
	if l.SwapMax, e = read(own, "memory.swap.max"); e != nil {
		return l, e
	}
	sliceSwap, e := read(slicePath, "memory.swap.max")
	if e != nil {
		return l, e
	}
	max := uint64(384 << 20)
	if role == "query" {
		max = 64 << 20
	}
	if l.TaskMax == 0 || l.TaskMax > max || l.SliceMax == 0 || l.SliceMax > 500000000 || l.SwapMax != 0 || sliceSwap != 0 {
		return l, fmt.Errorf("实际内存限制不符合要求：任务%d、合计%d、swap%d/%d，请核对部署限制", l.TaskMax, l.SliceMax, l.SwapMax, sliceSwap)
	}
	return l, nil
}
func Handshake(in io.Reader, out io.Writer, role string) (Envelope, Continue, error) {
	return handshake(in, out, role, ReadLimits)
}
func handshake(in io.Reader, out io.Writer, role string, inspect func(string) (Limits, error)) (Envelope, Continue, error) {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 128<<10+1)
	if !scan.Scan() {
		return Envelope{}, Continue{}, fmt.Errorf("未收到初始请求：%v", scan.Err())
	}
	first := append([]byte{}, scan.Bytes()...)
	limit := 128 << 10
	if role == "run" {
		limit = 16 << 10
	}
	if len(first) > limit {
		return Envelope{}, Continue{}, fmt.Errorf("初始请求超过%d字节", limit)
	}
	var env Envelope
	if e := Decode(first, &env); e != nil {
		return env, Continue{}, e
	}
	if env.V != 1 {
		return env, Continue{}, fmt.Errorf("不支持此通信版本")
	}
	limits, e := inspect(role)
	if e != nil {
		return env, Continue{}, e
	}
	if e = json.NewEncoder(out).Encode(map[string]any{"v": 1, "event": "ready", "limits": limits}); e != nil {
		return env, Continue{}, e
	}
	if !scan.Scan() {
		return env, Continue{}, fmt.Errorf("未收到继续确认，未调用模型")
	}
	if len(scan.Bytes()) > 16<<10 {
		return env, Continue{}, fmt.Errorf("继续控制信息超过16KiB")
	}
	var control Continue
	if e = Decode(scan.Bytes(), &control); e != nil {
		return env, control, e
	}
	if control.V != 1 || control.Command != "continue" {
		return env, control, fmt.Errorf("没有有效的继续确认，未调用模型")
	}
	if role != "run" && env.Operation != "models" && control.Credential != nil {
		return env, control, fmt.Errorf("此操作不接受账号密钥")
	}
	return env, control, nil
}
func Result(out io.Writer, status int, payload any, contentType string) error {
	if contentType == "" {
		contentType = "application/json"
	}
	var b []byte
	var e error
	if contentType == "image/png" {
		b, _ = payload.([]byte)
	} else {
		b, e = json.Marshal(payload)
		if e != nil {
			return e
		}
	}
	if len(b) > 2<<20 {
		return fmt.Errorf("读取结果超过2MiB，请分页")
	}
	if e = json.NewEncoder(out).Encode(map[string]any{"v": 1, "ok": status >= 200 && status < 300, "status": status, "contentType": contentType, "length": len(b)}); e != nil {
		return e
	}
	_, e = out.Write(b)
	return e
}
func ResultError(out io.Writer, err error) error {
	detail, ok := err.(*store.Error)
	if !ok {
		detail = store.Fail(503, "检测步骤未完成："+err.Error()+"；请按具体原因核查磁盘、权限或部署后重试", "query")
	}
	return json.NewEncoder(out).Encode(map[string]any{"v": 1, "ok": false, "status": detail.Status, "contentType": "application/json", "length": 0, "error": detail})
}
