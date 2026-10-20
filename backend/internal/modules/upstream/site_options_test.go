package upstream

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSiteOptionsURLB1(t *testing.T) {
	cache := &scopedOptionCountingCache{syncTestCache: newSyncTestCache(
		&Site{ID: "a", UserID: "owner", AdminAccountID: "workspace", Name: "A", BaseURL: "https://up.example.test/v1"},
		&Site{ID: "b", UserID: "owner", AdminAccountID: "other", Name: "B", BaseURL: "https://other.example.test"},
	)}
	got, err := NewService(nil, nil, nil, cache).ListOptions(context.Background(), "owner", "workspace")
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != "a" || cache.lists != 1 {
		t.Fatalf("unexpected selection or reads: %#v %v reads=%d", got, err, cache.lists)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0]["baseUrl"] != "https://up.example.test/v1" {
		t.Fatalf("baseUrl missing or changed: %s", raw)
	}
	for _, field := range []string{"metrics", "platform", "session"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("unexpected full site field %s", field)
		}
	}
}

func TestSiteOptionsURLB2(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://user:pass@up.example.test/v1", "https://up.example.test/v1"},
		{"javascript:alert(1)", ""}, {"ftp://up.example.test", ""}, {"not a url", ""}, {"https://", ""}, {"", ""},
		{"  http://up.example.test:8080/  ", "http://up.example.test:8080/"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := optionBaseURL(tc.input); got != tc.want {
				t.Fatalf("sanitized URL=%q want %q", got, tc.want)
			}
			cache := newSyncTestCache(&Site{ID: "a", UserID: "owner", AdminAccountID: "workspace", BaseURL: tc.input})
			result, err := NewService(nil, nil, nil, cache).ListOptions(context.Background(), "owner", "workspace")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(result)
			var payload struct {
				Items []map[string]any `json:"items"`
			}
			_ = json.Unmarshal(raw, &payload)
			if payload.Items[0]["baseUrl"] != tc.want {
				t.Fatalf("returned URL differs: %s", raw)
			}
			if strings.Contains(string(raw), "user") || strings.Contains(string(raw), "pass") {
				t.Fatal("URL credentials leaked")
			}
		})
	}
}
