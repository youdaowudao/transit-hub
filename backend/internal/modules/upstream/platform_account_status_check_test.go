package upstream

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSub2APIStatusCheckPreservesRawStatusAndAccountMetadata(t *testing.T) {
	for _, tc := range []struct{ raw, strict, legacy string }{
		{`0`, "0", "0"}, {`1`, "1", "1"}, {`0.0`, "0.0", "0"}, {`1e0`, "1e0", "1"},
		{`" active "`, " active ", " active "}, {`true`, "", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			body := fmt.Sprintf(`{"data":{"items":[{"id":42,"status":%s,"priority":9,"group_ids":[12],"updated_at":1700000000,"expires_at":1700000000.0,"extra":{"quota_limit":1.5}}],"total":1}}`, tc.raw)
			service := NewPlatformService(NewHTTPClient(&http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}))
			session := Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "synthetic-token"}
			strict, strictErr := service.ListSub2APIAdminAccountsForStatusCheck(t.Context(), session)
			legacy, legacyErr := service.ListSub2APIAdminAccountsContext(t.Context(), session)
			if strictErr != nil || legacyErr != nil || len(strict) != 1 || len(legacy) != 1 {
				t.Fatalf("read strict=%v, %v legacy=%v, %v", strict, strictErr, legacy, legacyErr)
			}
			if strict[0].Status != tc.strict || legacy[0].Status != tc.legacy {
				t.Fatalf("status strict=%q legacy=%q", strict[0].Status, legacy[0].Status)
			}
			strict[0].Status, legacy[0].Status = "", ""
			strict[0].InventoryResponseTimes, legacy[0].InventoryResponseTimes = nil, nil
			if !reflect.DeepEqual(strict, legacy) {
				t.Fatalf("status check changed non-status fields: strict=%+v legacy=%+v", strict, legacy)
			}
		})
	}
}

func TestSub2APIStatusCheckRejectsIncompleteOrAmbiguousJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantOK     bool
	}{
		{"trailing object", `{"items":[{"id":42,"status":1}],"total":1} {}`, false},
		{"trailing syntax", `{"items":[{"id":42,"status":1}],"total":1} ]`, false},
		{"partial inventory", `{"items":[{"id":42,"status":1}],"total":2}`, false},
		{"contradicting status lexemes", `{"data":{"items":[{"id":42,"status":1}],"total":1},"items":[{"id":42,"status":1.0}]}`, false},
		{"equivalent metadata lexemes", `{"data":{"items":[{"id":42,"status":1}],"total":1},"items":[{"id":42.0,"status":1}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewPlatformService(NewHTTPClient(&http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}))
			session := Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "synthetic-admin-key"}
			accounts, err := service.ListSub2APIAdminAccountsForStatusCheck(t.Context(), session)
			if tc.wantOK {
				if err != nil || len(accounts) != 1 || accounts[0].Status != "1" {
					t.Fatalf("compatible inventory rejected: accounts=%+v err=%v", accounts, err)
				}
			} else if err == nil || accounts != nil {
				t.Fatalf("invalid inventory accepted: accounts=%+v err=%v", accounts, err)
			}
		})
	}
}
