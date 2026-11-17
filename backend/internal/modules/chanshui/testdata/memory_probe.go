// This executable is built only by the explicitly enabled systemd integration test.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func limit(dir, name string) uint64 {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		panic(err)
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		panic(err)
	}
	return v
}
func emit(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		panic(err)
	}
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--descendant" {
		for {
			time.Sleep(time.Hour)
		}
	}
	var input struct {
		Operation string         `json:"operation"`
		Args      map[string]any `json:"args"`
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(line, &input); err != nil {
		panic(err)
	}
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		panic(err)
	}
	cgroup := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			cgroup = strings.TrimPrefix(line, "0::")
		}
	}
	dir := filepath.Join("/sys/fs/cgroup", cgroup)
	sliceDir := dir
	for filepath.Base(sliceDir) != "transithub-chanshui.slice" {
		next := filepath.Dir(sliceDir)
		if next == sliceDir {
			panic("missing test slice")
		}
		sliceDir = next
	}
	max := limit(dir, "memory.max")
	if input.Operation == "bad-limits" {
		max = 0
	}
	if input.Operation == "delayed-ready-run" {
		waiting, _ := input.Args["waitingFile"].(string)
		allow, _ := input.Args["allowReadyFile"].(string)
		if err := os.WriteFile(waiting, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			panic(err)
		}
		for {
			if _, err := os.Stat(allow); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	emit(map[string]any{"v": 1, "event": "ready", "limits": map[string]uint64{"taskMax": max, "sliceMax": limit(sliceDir, "memory.max"), "swapMax": limit(dir, "memory.swap.max")}})
	line, err = reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var cont map[string]any
	if json.Unmarshal(line, &cont) != nil || cont["command"] != "continue" {
		return
	}
	if path, _ := input.Args["continuedFile"].(string); path != "" {
		if err := os.WriteFile(path, []byte("continued"), 0600); err != nil {
			panic(err)
		}
	}
	if input.Operation == "delayed-ready-run" {
		endpoint, _ := input.Args["modelURL"].(string)
		response, err := http.Post(endpoint, "application/json", strings.NewReader(`{"isolated":true}`))
		if err != nil {
			panic(err)
		}
		response.Body.Close()
	}
	if input.Operation == "hold-query" {
		time.Sleep(time.Minute)
		return
	}
	if input.Operation == "normal-query" {
		body, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "memoryMax": max, "sliceMax": limit(sliceDir, "memory.max"), "swapMax": limit(dir, "memory.swap.max")})
		emit(map[string]any{"v": 1, "ok": true, "status": 200, "contentType": "application/json", "length": len(body)})
		_, _ = os.Stdout.Write(body)
		return
	}
	if input.Operation == "oom" || input.Operation == "hold-run" {
		child := exec.Command(os.Args[0], "--descendant")
		if err := child.Start(); err != nil {
			panic(err)
		}
		if path, _ := input.Args["pidFile"].(string); path != "" {
			if err := os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
				panic(err)
			}
		}
	}
	emit(map[string]any{"v": 1, "event": "accepted"})
	if input.Operation == "oom" {
		chunks := make([][]byte, 0, 30)
		for i := 0; i < 30; i++ {
			b := make([]byte, 16*1024*1024)
			for j := 0; j < len(b); j += 4096 {
				b[j] = byte(i + 1)
			}
			chunks = append(chunks, b)
		}
		fmt.Fprintln(os.Stderr, "memory allocation unexpectedly survived", len(chunks))
		time.Sleep(time.Minute)
		return
	}
	if input.Operation == "hold-run" {
		time.Sleep(time.Minute)
		return
	}
	emit(map[string]any{"v": 1, "event": "persisted"})
	if input.Operation == "linger" {
		time.Sleep(time.Minute)
	}
}
