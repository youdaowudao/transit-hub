package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestModelControlInventoryMappingAndPassthroughParsing(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials any
		known       bool
		want        map[string]string
	}{{"missing", nil, false, nil}, {"empty", map[string]any{}, true, map[string]string{}}, {"null", map[string]any{"model_mapping": nil}, true, map[string]string{}}, {"mapping", map[string]any{"model_mapping": map[string]any{"a": "A"}}, true, map[string]string{"a": "A"}}, {"array", map[string]any{"model_mapping": []any{"a"}}, false, nil}, {"value", map[string]any{"model_mapping": map[string]any{"a": 1}}, false, nil}, {"empty_key", map[string]any{"model_mapping": map[string]any{"": "A"}}, false, nil}, {"empty_value", map[string]any{"model_mapping": map[string]any{"a": ""}}, false, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			record := map[string]any{}
			if tc.credentials != nil {
				record["credentials"] = tc.credentials
			}
			a := parseSub2APIAccount(record)
			if a.ModelMappingKnown != tc.known || !reflect.DeepEqual(a.ModelMapping, tc.want) {
				t.Fatalf("mapping known=%t mapping=%v", a.ModelMappingKnown, a.ModelMapping)
			}
			encoded, _ := json.Marshal(a)
			var body map[string]any
			_ = json.Unmarshal(encoded, &body)
			for _, k := range []string{"ModelMapping", "ModelMappingKnown", "OpenAIPassthrough", "credentials"} {
				if _, ok := body[k]; ok {
					t.Fatalf("internal field %s leaked", k)
				}
			}
		})
	}
	for _, tc := range []struct {
		extra map[string]any
		want  bool
	}{{map[string]any{"openai_passthrough": false, "openai_oauth_passthrough": true}, false}, {map[string]any{"openai_passthrough": "bad", "openai_oauth_passthrough": true}, true}, {map[string]any{"openai_passthrough": true}, true}, {map[string]any{"openai_passthrough": "bad", "openai_oauth_passthrough": "bad"}, false}} {
		if got := parseSub2APIOpenAIPassthrough(map[string]any{"extra": tc.extra}); got != tc.want {
			t.Fatalf("passthrough precedence=%t want=%t", got, tc.want)
		}
	}
}
func TestModelControlDetailAndNarrowMutation(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("explicit integration opt-in required for httptest")
	}
	detail := map[string]any{"id": 1, "name": "C3 fixture", "platform": "openai", "type": "apikey", "status": "active", "schedulable": true, "credentials": map[string]any{"model_mapping": map[string]string{"a": "A", "b": "B"}}, "group_ids": []int{1}}
	mode := "valid"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			calls++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			want := map[string]any{"account_ids": []any{float64(1)}, "credentials": map[string]any{"model_mapping": map[string]any{"b": "B"}}}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("unsafe mutation fields=%v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"results": []map[string]any{{"account_id": 1, "success": true}}}})
			return
		}
		if mode == "404" {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 404})
			return
		}
		record := map[string]any{}
		for k, v := range detail {
			record[k] = v
		}
		if mode == "missing" {
			delete(record, "credentials")
		}
		if mode == "invalid" {
			record["credentials"] = map[string]any{"model_mapping": 42}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": record})
	}))
	defer server.Close()
	service := NewPlatformService(NewHTTPClient(server.Client()))
	session := Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	got, err := service.ReadSub2APIModelControlAccountContext(context.Background(), session, "1")
	if err != nil || got.ModelMapping["a"] != "A" || len(got.GroupIDs) != 1 {
		t.Fatalf("strict detail=%+v err=%v", got, err)
	}
	for _, value := range []string{"missing", "invalid", "404"} {
		mode = value
		_, err := service.ReadSub2APIModelControlAccountContext(context.Background(), session, "1")
		if err == nil || value == "404" && !errors.Is(err, ErrSub2APIModelControlAccountMissing) {
			t.Fatalf("invalid detail %s was accepted", value)
		}
	}
	if err := service.UpdateSub2APIAdminAccountModelMappingContext(context.Background(), session, "1", map[string]string{}); err == nil || calls != 0 {
		t.Fatal("empty mapping sent")
	}
	if err := service.UpdateSub2APIAdminAccountModelMappingContext(context.Background(), session, "1", map[string]string{"b": "B"}); err != nil || calls != 1 {
		t.Fatalf("narrow mapping update failed: %v", err)
	}
}
