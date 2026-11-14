package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func TestGiftLedgerInputValidation(t *testing.T) {
	today := giftTestDate(0)
	valid := GiftLedgerInput{MainUserID: "123", RechargeDate: today, PaidAmount: 100, GiftAmount: 20}
	cases := []struct {
		name    string
		mutate  func(*GiftLedgerInput)
		editing bool
		orig    string
		want    error
	}{
		{"valid", func(*GiftLedgerInput) {}, false, "", nil},
		{"user letters", func(i *GiftLedgerInput) { i.MainUserID = "12a" }, false, "", ErrGiftLedgerInvalidUser},
		{"user zero", func(i *GiftLedgerInput) { i.MainUserID = "0" }, false, "", ErrGiftLedgerInvalidUser},
		{"user too long", func(i *GiftLedgerInput) { i.MainUserID = "1234567890123456789" }, false, "", ErrGiftLedgerInvalidUser},
		{"user max safe ok", func(i *GiftLedgerInput) { i.MainUserID = "9007199254740991" }, false, "", nil},
		{"user beyond json range", func(i *GiftLedgerInput) { i.MainUserID = "9007199254740992" }, false, "", ErrGiftLedgerInvalidUser},
		{"negative", func(i *GiftLedgerInput) { i.PaidAmount = -1 }, false, "", ErrGiftLedgerInvalidAmount},
		{"three decimals", func(i *GiftLedgerInput) { i.PaidAmount = 1.2309 }, false, "", ErrGiftLedgerInvalidAmount},
		{"float noise ok", func(i *GiftLedgerInput) { i.PaidAmount = 0.1 + 0.2 }, false, "", nil},
		{"too large", func(i *GiftLedgerInput) { i.GiftAmount = 10_000_000.01 }, false, "", ErrGiftLedgerInvalidAmount},
		{"max ok", func(i *GiftLedgerInput) { i.PaidAmount = 10_000_000 }, false, "", nil},
		{"both zero", func(i *GiftLedgerInput) { i.PaidAmount, i.GiftAmount = 0, 0 }, false, "", ErrGiftLedgerInvalidAmount},
		{"gift only ok", func(i *GiftLedgerInput) { i.PaidAmount = 0 }, false, "", nil},
		{"note 200 ok", func(i *GiftLedgerInput) { i.Note = strings.Repeat("赠", 200) }, false, "", nil},
		{"note 201", func(i *GiftLedgerInput) { i.Note = strings.Repeat("赠", 201) }, false, "", ErrGiftLedgerInvalidNote},
		{"bad date", func(i *GiftLedgerInput) { i.RechargeDate = "2026-02-30" }, false, "", ErrGiftLedgerInvalidDate},
		{"future", func(i *GiftLedgerInput) { i.RechargeDate = giftTestDate(1) }, false, "", ErrGiftLedgerFutureDate},
		{"60 days ok", func(i *GiftLedgerInput) { i.RechargeDate = giftTestDate(-60) }, false, "", nil},
		{"61 days", func(i *GiftLedgerInput) { i.RechargeDate = giftTestDate(-61) }, false, "", ErrGiftLedgerTooOld},
		{"edit keeps old date", func(i *GiftLedgerInput) { i.RechargeDate = giftTestDate(-70); i.MainUserID = "" }, true, giftTestDate(-70), nil},
		{"edit moves to old date", func(i *GiftLedgerInput) { i.RechargeDate = giftTestDate(-70) }, true, giftTestDate(-69), ErrGiftLedgerTooOld},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutate(&input)
			got, err := normalizeGiftLedgerInput(input, today, tc.editing, tc.orig)
			if !errors.Is(err, tc.want) && !(err == nil && tc.want == nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.name == "float noise ok" && got.paidCents != 30 {
				t.Fatalf("cents = %d, want 30", got.paidCents)
			}
		})
	}
}

func giftLedgerHandlerFixture() (*http.ServeMux, *giftLedgerTestRepo, *giftLedgerTestPlatform) {
	repo := newGiftLedgerTestRepo()
	platform := newGiftLedgerTestPlatform(100)
	balance := 50.0
	platform.users["123"] = upstream.Sub2APIAdminUser{ID: "123", Email: "a@example.com", Balance: &balance}
	service := newGiftLedgerTestService(repo, platform)
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, service)
	return mux, repo, platform
}

func TestGiftLedgerHandlersMapErrors(t *testing.T) {
	mux, repo, platform := giftLedgerHandlerFixture()
	body := func(id, date string, paid, gift float64) string {
		raw, _ := json.Marshal(GiftLedgerInput{MainUserID: id, RechargeDate: date, PaidAmount: paid, GiftAmount: gift})
		return string(raw)
	}
	cases := []struct {
		name, method, path, body string
		status                   int
		key                      string
	}{
		{"invalid user", http.MethodPost, "/api/dashboard/gift-ledger/records", body("x", giftTestDate(0), 100, 20), 400, "invalidUser"},
		{"invalid amount", http.MethodPost, "/api/dashboard/gift-ledger/records", body("123", giftTestDate(0), 1.234, 20), 400, "invalidAmount"},
		{"future", http.MethodPost, "/api/dashboard/gift-ledger/records", body("123", giftTestDate(1), 100, 20), 400, "futureDate"},
		{"too old", http.MethodPost, "/api/dashboard/gift-ledger/records", body("123", giftTestDate(-61), 100, 20), 400, "tooOld"},
		{"user not found", http.MethodPost, "/api/dashboard/gift-ledger/records", body("999", giftTestDate(0), 100, 20), 400, "userNotFound"},
		{"edit missing", http.MethodPut, "/api/dashboard/gift-ledger/records/nope", body("", giftTestDate(0), 100, 20), 404, "notFound"},
		{"delete missing", http.MethodDelete, "/api/dashboard/gift-ledger/records/nope", "", 404, "notFound"},
	}
	for _, tc := range cases {
		recorder := discountRequest(mux, tc.method, tc.path, tc.body, "user-1")
		if recorder.Code != tc.status || !strings.Contains(recorder.Body.String(), "dashboard.giftLedger.errors."+tc.key) {
			t.Fatalf("%s = %d %s, want %d %s", tc.name, recorder.Code, recorder.Body.String(), tc.status, tc.key)
		}
	}
	if len(repo.records) != 0 {
		t.Fatalf("records written on failures: %+v", repo.records)
	}

	// 其他核对失败：502 + 脱敏原因（步骤、状态码、提示原文）。
	platform.userErr = &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 403, UpstreamMessage: "admin only"}
	recorder := discountRequest(mux, http.MethodPost, "/api/dashboard/gift-ledger/records", body("123", giftTestDate(0), 100, 20), "user-1")
	var payload map[string]string
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if recorder.Code != 502 || payload["message"] != "dashboard.giftLedger.errors.userLookupFailed" || !strings.Contains(payload["reason"], "读取主站用户资料") ||
		!strings.Contains(payload["reason"], "HTTP 403") || !strings.Contains(payload["reason"], "admin only") {
		t.Fatalf("lookup failure = %d %v", recorder.Code, payload)
	}
	platform.userErr = nil

	// 主站返回 200 但资料编号缺失或不是这个用户：按核对失败处理，不保存。
	for _, returned := range []string{"", "124"} {
		balance := 50.0
		platform.users["123"] = upstream.Sub2APIAdminUser{ID: returned, Email: "other@example.com", Balance: &balance}
		recorder = discountRequest(mux, http.MethodPost, "/api/dashboard/gift-ledger/records", body("123", giftTestDate(0), 100, 20), "user-1")
		payload = map[string]string{}
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		if recorder.Code != 502 || payload["message"] != "dashboard.giftLedger.errors.userLookupFailed" || !strings.Contains(payload["reason"], "主站用户 ID 123") || len(repo.records) != 0 {
			t.Fatalf("mismatched user %q = %d %v records=%d", returned, recorder.Code, payload, len(repo.records))
		}
	}
	balance := 50.0
	platform.users["123"] = upstream.Sub2APIAdminUser{ID: "123", Email: "a@example.com", Balance: &balance}

	// 新增、编辑（日期不变时保留原日期）、删除。
	recorder = discountRequest(mux, http.MethodPost, "/api/dashboard/gift-ledger/records", `{"mainUserId":"123","rechargeDate":"`+giftTestDate(-3)+`","paidAmount":100,"giftAmount":20,"note":" 测试 "}`, "user-1")
	if recorder.Code != 200 || len(repo.records) != 1 || repo.records[0].MainUserEmail != "a@example.com" || repo.records[0].Note != "测试" || repo.records[0].PaidCents != 10000 || repo.records[0].GiftCents != 2000 {
		t.Fatalf("create = %d %s records=%+v", recorder.Code, recorder.Body.String(), repo.records)
	}
	id := repo.records[0].ID
	recorder = discountRequest(mux, http.MethodPut, "/api/dashboard/gift-ledger/records/"+id, body("999", giftTestDate(-3), 100, 30), "user-1")
	if recorder.Code != 200 || repo.records[0].GiftCents != 3000 || repo.records[0].MainUserID != 123 {
		t.Fatalf("update = %d %s records=%+v", recorder.Code, recorder.Body.String(), repo.records)
	}
	recorder = discountRequest(mux, http.MethodDelete, "/api/dashboard/gift-ledger/records/"+id, "", "user-1")
	if recorder.Code != http.StatusNoContent || len(repo.records) != 0 {
		t.Fatalf("delete = %d records=%+v", recorder.Code, repo.records)
	}
}

func TestGiftLedgerUnsupportedPlatform(t *testing.T) {
	repo := newGiftLedgerTestRepo()
	platform := newGiftLedgerTestPlatform(100)
	service := newGiftLedgerTestService(repo, platform)
	newAPI := authenticatedSession()
	newAPI.Platform = upstream.PlatformNewAPI
	newAPI.Cookie, newAPI.UserID = "cookie", "1"
	service.store.(*fakeSessionStore).set("user-1", "account-1", AdminSession{Session: newAPI})
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, service)
	recorder := discountRequest(mux, http.MethodPost, "/api/dashboard/gift-ledger/records", `{"mainUserId":"1","rechargeDate":"`+giftTestDate(0)+`","paidAmount":1,"giftAmount":0}`, "user-1")
	if recorder.Code != http.StatusNotImplemented || !strings.Contains(recorder.Body.String(), "unsupported") {
		t.Fatalf("unsupported = %d %s", recorder.Code, recorder.Body.String())
	}
	overview, err := service.GiftLedgerOverview(context.Background(), "user-1")
	if err != nil || overview.Supported || overview.StatusAvailable {
		t.Fatalf("overview = %+v err=%v", overview, err)
	}
}

func TestGiftLedgerCustomerCheck(t *testing.T) {
	mux, _, platform := giftLedgerHandlerFixture()
	platform.costs["123|"+giftTestDate(0)] = 12.5
	recorder := discountRequest(mux, http.MethodGet, "/api/dashboard/gift-ledger/customer?mainUserId=123", "", "user-1")
	var check GiftLedgerCustomerCheck
	_ = json.Unmarshal(recorder.Body.Bytes(), &check)
	if recorder.Code != 200 || check.Email != "a@example.com" || check.Balance == nil || *check.Balance != 50 || check.TodayConsumption != 12.5 {
		t.Fatalf("check = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = discountRequest(mux, http.MethodGet, "/api/dashboard/gift-ledger/customer?mainUserId=999", "", "user-1")
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "userNotFound") {
		t.Fatalf("not found = %d %s", recorder.Code, recorder.Body.String())
	}
	// 用户资料成功、今天消费返回 404：是 usageLookupFailed，不是 userNotFound。
	platform.errs["123|"+giftTestDate(0)] = &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 404, Reason: upstream.ErrorNotFound}
	recorder = discountRequest(mux, http.MethodGet, "/api/dashboard/gift-ledger/customer?mainUserId=123", "", "user-1")
	var payload map[string]string
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if recorder.Code != 502 || payload["message"] != "dashboard.giftLedger.errors.usageLookupFailed" || !strings.Contains(payload["reason"], "查到了 a@example.com") ||
		!strings.Contains(payload["reason"], "读取主站今天消费") || !strings.Contains(payload["reason"], "HTTP 404") {
		t.Fatalf("usage failure = %d %v", recorder.Code, payload)
	}
}

func TestGiftLedgerOverviewStatus(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r1", 123, -2, 100, 20), giftTestRecord("r2-later", 123, -1, 100, 0))
	repo.usage[123] = map[string]int64{giftTestDate(-2): 90e6}
	platform := newGiftLedgerTestPlatform(100)
	balance := 80.0
	platform.users["123"] = upstream.Sub2APIAdminUser{ID: "123", Email: "u123@example.com", Balance: &balance}
	platform.costs["123|"+giftTestDate(-1)] = 20
	platform.costs["123|"+giftTestDate(0)] = 40
	service := newGiftLedgerTestService(repo, platform)
	overview, err := service.GiftLedgerOverview(context.Background(), "user-1")
	if err != nil || !overview.Supported || !overview.StatusAvailable || len(overview.Customers) != 1 {
		t.Fatalf("overview = %+v err=%v", overview, err)
	}
	c := overview.Customers[0]
	// 到账 220；已用 90+20+40 = 150；今天 40：r1 剩 10（覆盖，赠送 10×20/120 进位），r2 30。
	if c.CreditedTotal != 220 || *c.UsedTotal != 150 || *c.Remaining != 70 || *c.TodayConsumption != 40 || *c.TodayCovered != 40 || *c.TodayUncovered != 0 || c.MainBalance == nil || *c.MainBalance != 80 {
		t.Fatalf("customer = %+v", c)
	}
	if *c.TodayGift != 1.67 || c.Records[0].State != giftLedgerStateUsedUp || *c.Records[0].UsedAmount != 120 || c.Records[1].State != giftLedgerStateUsing || *c.Records[1].UsedAmount != 30 {
		t.Fatalf("records = %+v gift=%v", c.Records, *c.TodayGift)
	}
	// 余额接口返回别的用户资料时不显示余额。
	platform.users["123"] = upstream.Sub2APIAdminUser{ID: "124", Email: "u124@example.com", Balance: &balance}
	overview, err = service.GiftLedgerOverview(context.Background(), "user-1")
	if err != nil || overview.Customers[0].MainBalance != nil {
		t.Fatalf("mismatched balance shown: %+v err=%v", overview.Customers[0].MainBalance, err)
	}
	platform.users["123"] = upstream.Sub2APIAdminUser{ID: "123", Email: "u123@example.com", Balance: &balance}
	// 状态失败：记录照常返回，数字为空并有原因。
	platform.errs["123|"+giftTestDate(0)] = &upstream.RequestError{MessageKey: upstream.ErrorNetwork}
	overview, err = service.GiftLedgerOverview(context.Background(), "user-1")
	if err != nil || overview.StatusAvailable || !strings.Contains(overview.StatusError, "没有收到主站响应") || len(overview.Customers[0].Records) != 2 || overview.Customers[0].UsedTotal != nil {
		t.Fatalf("failed overview = %+v err=%v", overview, err)
	}
}
