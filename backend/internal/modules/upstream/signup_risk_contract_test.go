package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type signupRiskContractTransport func(*http.Request) (*http.Response, error)

func (f signupRiskContractTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The JSON query lets these business assertions compile and fail on the old
// client, before it has the optional read-only risk query fields.
func signupRiskContractQuery(t *testing.T) Sub2APIAdminUsersQuery {
	t.Helper()
	var q Sub2APIAdminUsersQuery
	if err := json.Unmarshal([]byte(`{"Page":1,"PageSize":50,"Search":"[注册风险:高]","IncludeSubscriptions":false,"StrictPagination":true}`), &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestSignupRiskContractReadsNotesWithoutSubscriptions(t *testing.T) {
	transport := signupRiskContractTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/users" || q.Get("search") != "[注册风险:高]" || q.Get("page") != "1" || q.Get("page_size") != "50" {
			t.Error("risk poll must use the existing read-only administrator search contract")
		}
		if q.Get("include_subscriptions") != "false" {
			t.Error("risk poll must omit subscription expansion")
		}
		if r.Header.Get("x-api-key") == "" || r.Header.Get("Authorization") != "" {
			t.Error("risk poll must reuse administrator key authentication")
		}
		body := `{"data":{"items":[{"id":42,"email":"alice+1@example.com","notes":"[注册风险:高] 含+别名","created_at":"2031-01-01T00:00:00Z"}],"total":1,"page":1,"page_size":50,"pages":1}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	service := NewPlatformService(NewHTTPClient(&http.Client{Transport: transport}))
	page, err := service.FetchSub2APIAdminUsersPageWithContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "isolated-fixture"}, signupRiskContractQuery(t))
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("cannot read complete risk user: %v", err)
	}
	raw, _ := json.Marshal(page.Items[0])
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["Notes"] != "[注册风险:高] 含+别名" || page.Items[0].ID != "42" || page.Items[0].CreatedAt == nil {
		t.Error("administrator notes and registration identity must survive parsing for secondary verification")
	}
}

func TestSignupRiskContractRejectsBrokenPaginationBeforeInitialization(t *testing.T) {
	for _, body := range []string{
		`{"data":{"items":"broken","total":0,"page":1,"page_size":50,"pages":1}}`,
		`{"data":{"total":0,"page":1,"page_size":50,"pages":1}}`,
		`{"data":{"items":[],"total":0.5,"page":1,"page_size":50,"pages":1}}`,
		`{"data":{"items":[],"total":0,"page":1,"page_size":"50","pages":1}}`,
		`{"data":{"items":[{"email":"broken@example.com"}],"total":1,"page":1,"page_size":50,"pages":1}}`,
	} {
		t.Run(body, func(t *testing.T) {
			transport := signupRiskContractTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			service := NewPlatformService(NewHTTPClient(&http.Client{Transport: transport}))
			if _, err := service.FetchSub2APIAdminUsersPageWithContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "isolated-fixture"}, signupRiskContractQuery(t)); err == nil {
				t.Fatal("malformed page must not silently initialize or advance processed users")
			}
		})
	}
}

func TestSignupRiskContractRejectsIncompletePageCounts(t *testing.T) {
	for _, body := range []string{
		`{"data":{"items":[],"total":10,"page":1,"page_size":50,"pages":1}}`,
		`{"data":{"items":[],"total":100,"page":1,"page_size":50,"pages":1}}`,
	} {
		transport := signupRiskContractTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		client := NewPlatformService(NewHTTPClient(&http.Client{Transport: transport}))
		if _, err := client.FetchSub2APIAdminUsersPageWithContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "isolated-fixture"}, signupRiskContractQuery(t)); err == nil {
			t.Error("nonzero totals cannot be accepted as a complete empty baseline")
		}
	}
}

func TestSignupRiskContractExplainsBrokenResponseWithoutRawValues(t *testing.T) {
	for _, tc := range []struct{ body, reason string }{
		{`{"data":{"items":[],"total":0,"page":1,"page_size":"50","pages":1}}`, "每页数量不是数字"},
		{`{"data":{"items":[],"total":10,"page":1,"page_size":50,"pages":1}}`, "应返回10条用户，实际返回0条"},
		{`{"data":{"items":[{"id":"password=fixture-private-value"}],"total":1,"page":1,"page_size":50,"pages":1}}`, "第1条用户的编号不是完整的正整数"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			transport := signupRiskContractTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			client := NewPlatformService(NewHTTPClient(&http.Client{Transport: transport}))
			_, err := client.FetchSub2APIAdminUsersPageWithContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "isolated-fixture"}, signupRiskContractQuery(t))
			var failure *RequestError
			if !errors.As(err, &failure) || failure.MessageKey != ErrorInvalidResponse || !strings.Contains(failure.UpstreamMessage, tc.reason) {
				t.Errorf("response rejection omitted its known field and reason: %v", err)
			} else if strings.Contains(failure.UpstreamMessage, "fixture-private-value") {
				t.Fatal("response diagnosis echoed an untrusted value")
			}
		})
	}
}
