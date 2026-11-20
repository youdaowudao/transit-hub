package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAbsorbReview1StrictUsersConsumesValidatedArray(t *testing.T) {
	for _, tc := range []struct {
		name, extra  string
		strict, good bool
		wantID       string
	}{
		{"strict canonical", "", true, true, "1"},
		{"strict matching root", `"items":[{"id":1}],`, true, true, "1"},
		{"strict conflicting root same size", `"items":[{"id":2}],`, true, false, ""},
		{"strict conflicting alias same size", `"items":[{"id":1}],`, true, false, ""},
		{"legacy root priority", `"items":[{"id":2}],`, false, true, "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alias := ""
			if tc.name == "strict conflicting alias same size" {
				alias = `,"list":[{"id":2}]`
			}
			body := `{` + tc.extra + `"data":{"items":[{"id":1}],"total":1,"page":1,"page_size":100,"pages":1` + alias + `}}`
			s := NewPlatformService(NewHTTPClient(&http.Client{Transport: absorbProbeTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}))
			page, err := s.FetchSub2APIAdminUsersPageWithContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}, Sub2APIAdminUsersQuery{Page: 1, PageSize: 100, StrictPagination: tc.strict})
			if (err == nil) != tc.good {
				t.Fatalf("validated array contract: page=%+v error=%v", page, err)
			}
			if tc.good && (len(page.Items) != 1 || page.Items[0].ID != tc.wantID) {
				t.Fatalf("consumed unexpected array: %+v", page)
			}
		})
	}
}
