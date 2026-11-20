package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/prefixcheck"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/hub/wire"
)

type sqlMutation struct {
	Label      string
	Statements []struct {
		SQL    string
		Params []any
	}
	Changed map[string]json.RawMessage
}

type sqlContract struct {
	Owner     struct{ User, Workspace string }
	Base      map[string]json.RawMessage
	Mutations []sqlMutation
}

type seedRows struct {
	Reports, Items []map[string]any
}

func readContractJSON(t *testing.T, filename string, dst any) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := json.NewDecoder(f)
	reader.UseNumber()
	if err := reader.Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func openContractDB(t *testing.T, owner store.Owner, seed seedRows) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "contract.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tx, err := s.RuntimeDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	insertContractRows(t, tx, owner, "hub_reports", seed.Reports)
	insertContractRows(t, tx, owner, "hub_items", seed.Items)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func insertContractRows(t *testing.T, tx *sql.Tx, owner store.Owner, table string, seeds []map[string]any) {
	t.Helper()
	columns := make([]string, 0, len(seeds[0]))
	for column := range seeds[0] {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	query := fmt.Sprintf("INSERT INTO %s(%s) VALUES (%s?)", table, strings.Join(columns, ","), strings.Repeat("?,", len(columns)-1))
	stmt, err := tx.PrepareContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for _, seed := range seeds {
		args := make([]any, len(columns))
		for i, column := range columns {
			value := seed[column]
			switch value {
			case "{{USER_ID}}":
				value = owner.UserID
			case "{{WORKSPACE_ID}}":
				value = owner.WorkspaceID
			}
			args[i] = value
		}
		if _, err := stmt.ExecContext(t.Context(), args...); err != nil {
			t.Fatalf("insert %s rows: %v", table, err)
		}
	}
}

func TestCrossCheckSQLContract(t *testing.T) {
	var contract sqlContract
	var seed seedRows
	readContractJSON(t, "sql-contract.json", &contract)
	readContractJSON(t, "browser-seed.json", &seed)
	if len(contract.Base) != 18 || len(contract.Mutations) != 46 || len(seed.Reports) != 18 {
		t.Fatalf("contract must test 18 reports and 46 mutations, got %d, %d, %d", len(contract.Base), len(contract.Mutations), len(seed.Reports))
	}
	owner := store.Owner{UserID: contract.Owner.User, WorkspaceID: contract.Owner.Workspace}
	reports := make([]string, 0, len(contract.Base))
	for id := range contract.Base {
		reports = append(reports, id)
	}
	sort.Strings(reports)
	mutations := append([]sqlMutation{{Label: "原样数据"}}, contract.Mutations...)
	for _, mutation := range mutations {
		t.Run(mutation.Label, func(t *testing.T) {
			s := openContractDB(t, owner, seed)
			for _, change := range mutation.Statements {
				if _, err := s.RuntimeDB().ExecContext(t.Context(), change.SQL, change.Params...); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range reports {
				projected, err := s.CrossCheckData(t.Context(), owner, id)
				if err != nil {
					t.Fatalf("%s: %v", id, err)
				}
				want, ok := mutation.Changed[id]
				if !ok {
					want = contract.Base[id]
				}
				result := prefixcheck.CrossCheck(toCrossInput(projected))
				if result.TooLarge {
					t.Errorf("%s: normal contract report exceeded size limit", id)
				}
				got, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				compareContractJSON(t, id, got, want)
			}
		})
	}
}

func compareContractJSON(t *testing.T, id string, got, want []byte) {
	t.Helper()
	var a, b map[string]any
	for i, raw := range [][]byte{got, want} {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode([]any{&a, &b}[i]); err != nil {
			t.Fatal(err)
		}
	}
	if reflect.DeepEqual(a, b) {
		return
	}
	for key, expected := range b {
		if !reflect.DeepEqual(a[key], expected) {
			t.Errorf("%s %s = %#v; want %#v", id, key, a[key], expected)
		}
	}
	for key := range a {
		if _, exists := b[key]; !exists {
			t.Errorf("%s unexpected key %q", id, key)
		}
	}
}

func TestCrossCheckQueryContractAndLimits(t *testing.T) {
	var contract sqlContract
	var seed seedRows
	readContractJSON(t, "sql-contract.json", &contract)
	readContractJSON(t, "browser-seed.json", &seed)
	owner := store.Owner{UserID: contract.Owner.User, WorkspaceID: contract.Owner.Workspace}
	s := openContractDB(t, owner, seed)
	id := seed.Reports[0]["report_id"].(string)

	call := func(op, arg, value string) (any, error) {
		t.Helper()
		raw, err := json.Marshal(map[string]string{arg: value})
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := query(t.Context(), s, wire.Envelope{Owner: owner, Operation: op, Args: raw}, wire.Continue{})
		return body, err
	}
	expectError := func(err error, status int, message string) {
		t.Helper()
		var detail *store.Error
		if !errors.As(err, &detail) || detail.Status != status || detail.Message != message {
			t.Fatalf("call error = %v; want %d %q", err, status, message)
		}
	}
	measure := func(value any) int {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return len(raw)
	}
	originalCross, originalPrompt := crossCheckResult, readPrefixPrompt
	t.Cleanup(func() { crossCheckResult, readPrefixPrompt = originalCross, originalPrompt })
	const crossLimit, promptLimit = 256 << 10, 64 << 10
	empty := prefixcheck.CrossInput{Report: prefixcheck.ReportInput{Status: "done"}}
	overhead := measure(originalCross(empty))
	for _, length := range []int{crossLimit, crossLimit + 1} {
		crossCheckResult = func(prefixcheck.CrossInput) prefixcheck.Result {
			in := empty
			in.Report.ReportID = strings.Repeat("x", length-overhead)
			return originalCross(in)
		}
		out, err := call("cross-check", "reportId", id)
		if length == crossLimit {
			if err != nil || measure(out) != crossLimit {
				t.Fatalf("exact cross-check boundary: %v", err)
			}
		} else {
			expectError(err, 503, "判读结果超过 256KiB，未返回；请核查本报告题目数量后重试。")
		}
	}
	_, err := call("cross-check", "reportId", strings.Repeat("0", 32))
	expectError(err, 404, "报告不存在或不属于当前工作区")
	_, err = call("read-prefix-prompt", "promptId", "app/invalid")
	expectError(err, 422, "提示词编号格式不正确，请重新打开报告后重试；未读取任何内容。")
	_, err = call("read-prefix-prompt", "promptId", "missing-test-id")
	expectError(err, 404, "程序附带的提示词库里没有这一份，可能是程序版本不一致；请刷新页面后重试。")
	for _, length := range []int{promptLimit, promptLimit + 1} {
		readPrefixPrompt = func(string) ([]byte, bool) {
			return []byte(`{"text":"` + strings.Repeat("z", length-len(`{"text":""}`)) + `"}`), true
		}
		out, err := call("read-prefix-prompt", "promptId", "codex-gpt-6.2-sol")
		if length == promptLimit {
			if err != nil || measure(out) != promptLimit {
				t.Fatalf("exact prompt boundary: %v", err)
			}
		} else {
			expectError(err, 503, "提示词全文超过 64KiB，未返回；请核查程序附带的提示词库。")
		}
	}
	_, err = s.RuntimeDB().ExecContext(t.Context(), "UPDATE hub_reports SET summary_json=? WHERE report_id=?", `{"padding":"`+strings.Repeat("a", 63<<10-1200)+`","prefix":{"padding":"`+strings.Repeat("b", 4000)+`"}}`, id)
	if err != nil {
		t.Fatal(err)
	}
	view, err := call("read-report", "reportId", id)
	if err != nil || measure(view) > promptLimit {
		t.Fatalf("existing near-limit report must still fit: %v", err)
	}
	summary := view.(map[string]any)["summary"].(map[string]any)
	if _, ok := summary["prefix"]; ok {
		t.Fatal("regular read-report returned the saved analysis")
	}
}

func TestListItemsKeepsSampleTypeDecisionAcrossJSONDecode(t *testing.T) {
	var contract sqlContract
	var seed seedRows
	readContractJSON(t, "sql-contract.json", &contract)
	readContractJSON(t, "browser-seed.json", &seed)
	owner := store.Owner{UserID: contract.Owner.User, WorkspaceID: contract.Owner.Workspace}
	s := openContractDB(t, owner, seed)
	const id = "e2e00000000000000000000000000007"
	findInjection := func() store.Item {
		t.Helper()
		var found store.Item
		cursor := ""
		for {
			raw, _ := json.Marshal(map[string]any{"reportId": id, "cursor": cursor, "limit": 5})
			value, _, err := query(t.Context(), s, wire.Envelope{Owner: owner, Operation: "list-items", Args: raw}, wire.Continue{})
			if err != nil {
				t.Fatal(err)
			}
			page := value.(store.Page[store.Item])
			for _, item := range page.Items {
				if item.Definition.Kind == "injection" {
					found = item
				} else if item.UsableInjectionPoints != nil {
					t.Fatal("non-injection item has a sample decision")
				}
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		return found
	}
	integer := findInjection()
	if integer.UsableInjectionPoints == nil || *integer.UsableInjectionPoints != 2 {
		t.Fatal("the integer point should be available")
	}
	_, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_items SET result_json=json_set(result_json,'$.evidence.points[0].total',json('7.0'))
WHERE report_id=? AND section_id='injection'`, id)
	if err != nil {
		t.Fatal(err)
	}
	decimal := findInjection()
	if decimal.UsableInjectionPoints == nil || *decimal.UsableInjectionPoints != 0 || !strings.Contains(string(decimal.Result), `"total":7.0`) {
		t.Fatal("the decimal point should be unavailable and the stored value must remain unchanged")
	}
}
