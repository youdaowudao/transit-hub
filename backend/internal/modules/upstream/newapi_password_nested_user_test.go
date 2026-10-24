package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

func TestNewAPIPasswordUserIDShapes(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, key := range []string{"id", "user_id", "userId"} {
			for _, value := range []any{float64(7), "7", " 7 ", "7.0"} {
				t.Run(fmt.Sprintf("nested_%t_%s_%v", nested, key, value), func(t *testing.T) {
					record := map[string]any{key: value}
					if nested {
						record = map[string]any{"user": record}
					}
					if got := newAPIUserID(record); got != "7" {
						t.Fatalf("合法编号未被规范读取：%q", got)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name string
		data map[string]any
		want string
	}{
		{"direct_container_first", map[string]any{"userId": "7", "user": map[string]any{"id": float64(8)}}, "7"},
		{"direct_alias_order", map[string]any{"id": "7", "user_id": "8", "userId": "9"}, "7"},
		{"nested_alias_order", map[string]any{"user": map[string]any{"id": "7", "user_id": "8"}}, "7"},
		{"invalid_direct_then_valid_alias", map[string]any{"id": float64(0), "user_id": "7"}, "7"},
		{"invalid_direct_then_nested", map[string]any{"id": float64(-1), "user": map[string]any{"id": "7"}}, "7"},
		{"direct_survives_unrelated_user", map[string]any{"id": "7", "user": []any{"ignored"}}, "7"},
		{"no_unknown_nesting", map[string]any{"account": map[string]any{"id": float64(7)}}, ""},
		{"nil", nil, ""},
		{"empty", map[string]any{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newAPIUserID(tc.data); got != tc.want {
				t.Fatalf("候选顺序或支持范围错误：got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestNewAPIPasswordInvalidUserIDIsNotConfirmed(t *testing.T) {
	invalid := []any{nil, false, true, float64(0), float64(-1), 1.5, math.NaN(), math.Inf(1), math.Inf(-1), float64(9007199254740992), "", " ", "0", "-1", "1.5", "NaN", "Inf", "9007199254740992", "invalid", []any{7}, map[string]any{"id": 7}}
	for index, value := range invalid {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("case_%d_nested_%t", index, nested), func(t *testing.T) {
				record := map[string]any{"id": value}
				if nested {
					record = map[string]any{"user": record}
				}
				if got := newAPIUserID(record); got != "" {
					t.Fatalf("非法编号被转换为确认编号：%q", got)
				}
			})
		}
	}
	if got := newAPIUserID(map[string]any{"user": map[string]any{"id": float64(9007199254740991)}}); got != "9007199254740991" {
		t.Fatalf("安全整数上界应保留：%q", got)
	}
}

func TestNewAPIPasswordMissingLoginPartsAreSpecific(t *testing.T) {
	for _, tc := range []struct {
		name      string
		data      map[string]any
		hasCookie bool
		want      string
	}{
		{"missing_cookie", map[string]any{"user": map[string]any{"id": 7}}, false, "没有返回登录会话"},
		{"missing_id", map[string]any{"user": map[string]any{}}, true, "没有合法的用户编号"},
		{"fractional_direct_id", map[string]any{"id": 7.5}, true, "没有合法的用户编号"},
		{"invalid_nested_id", map[string]any{"user": map[string]any{"id": "NaN"}}, true, "没有合法的用户编号"},
		{"unsupported_user_shape", map[string]any{"user": []any{7}}, true, "没有合法的用户编号"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSwitchFixture()
			tc.data["token"] = "fixture-sensitive-token"
			platform := newAPIPasswordFixturePlatform(t, fixture, tc.data, tc.hasCookie)
			_, err := platform.Login("http://fixture.invalid", PlatformNewAPI, "fixture-account", "fixture-sensitive-password")
			var failure *RequestError
			if !errors.As(err, &failure) || failure.Reason != ErrorLoginIncomplete || failure.Stage != "login" || failure.StatusCode != 200 || !strings.Contains(failure.UpstreamMessage, tc.want) {
				t.Fatalf("登录缺项没有保留准确原因、步骤或状态：%v", err)
			}
			for _, secret := range []string{"fixture-sensitive-token", "fixture-sensitive-password", "fixture-session=new"} {
				if strings.Contains(failure.UpstreamMessage, secret) || strings.Contains(err.Error(), secret) {
					t.Fatal("登录缺项错误泄漏了合成凭据")
				}
			}
			for _, wrong := range []string{"可能开启", "改用 Key", "改用 Token"} {
				if strings.Contains(failure.UpstreamMessage, wrong) {
					t.Fatal("缺项错误仍含无依据的验证或连接方式建议")
				}
			}
			if fixture.paths["/api/user/self"] != 0 || fixture.paths["/api/status"] != 0 || fixture.paths["/api/log/self/stat"] != 0 {
				t.Fatal("缺少必要登录信息时仍继续读取")
			}
		})
	}
}

func TestNewAPIPasswordUnknownVerificationTypesStopReads(t *testing.T) {
	for index, value := range []any{float64(0), float64(1), []any{}, map[string]any{}, "true", ""} {
		t.Run(fmt.Sprintf("unknown_%d", index), func(t *testing.T) {
			fixture := newSwitchFixture()
			platform := newAPIPasswordFixturePlatform(t, fixture, map[string]any{"user": map[string]any{"id": 7}, "require_2fa": value}, true)
			_, err := platform.Login("http://fixture.invalid", PlatformNewAPI, "fixture-account", "fixture-password")
			var failure *RequestError
			if !errors.As(err, &failure) || !strings.Contains(failure.UpstreamMessage, "额外验证") {
				t.Fatalf("未知验证状态应明确拒绝：%v", err)
			}
			if fixture.paths["/api/user/self"] != 0 || fixture.paths["/api/status"] != 0 || fixture.paths["/api/log/self/stat"] != 0 {
				t.Fatal("未知验证状态继续读取了账号或费用")
			}
		})
	}
}

func TestNewAPIPasswordLegacyAndNestedKeepOriginalReads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		data      map[string]any
		outerFlag any
	}{
		{"legacy_direct", map[string]any{"id": 7}, true},
		{"legacy_alias_string", map[string]any{"user_id": "7", "require_2fa": false}, true},
		{"nested_alias_string", map[string]any{"user": map[string]any{"userId": "7"}, "require_2fa": false}, true},
		{"legacy_missing_success", map[string]any{"id": 7}, nil},
		{"legacy_non_boolean_success", map[string]any{"id": 7}, "legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSwitchFixture()
			platform := newAPIPasswordFixturePlatform(t, fixture, tc.data, true)
			if tc.outerFlag != true {
				original := platform.httpClient.client.Transport
				platform.httpClient.client.Transport = keyUsageRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					response, err := original.RoundTrip(request)
					if err != nil || request.URL.Path != "/api/user/login" {
						return response, err
					}
					response.Body.Close()
					payload := map[string]any{"data": tc.data}
					if tc.outerFlag != nil {
						payload["success"] = tc.outerFlag
					}
					body, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					response.Body = io.NopCloser(bytes.NewReader(body))
					return response, nil
				})
			}
			result, err := platform.Login("http://fixture.invalid", PlatformNewAPI, "fixture-account", "fixture-password")
			if err != nil || result.Session.UserID != "7" || result.Session.AuthMode != AuthModePassword {
				t.Fatalf("完整合法旧形状或嵌套形状未保持登录：%v", err)
			}
			if fixture.logins != 1 || fixture.paths["/api/user/self"] != 1 || fixture.paths["/api/log/self/stat"] != 1 {
				t.Fatal("编号兼容额外增加了登录、本人或费用读取")
			}
		})
	}
}

func TestNewAPIPasswordAutoDetectionAcceptsNestedUser(t *testing.T) {
	fixture := newSwitchFixture()
	platform := newAPIPasswordFixturePlatform(t, fixture, map[string]any{"user": map[string]any{"id": 7}}, true)
	result, err := platform.Login("http://fixture.invalid", PlatformAuto, "fixture-account", "fixture-password")
	if err != nil || result.Platform != PlatformNewAPI || result.Session.UserID != "7" {
		t.Fatalf("自动识别没有接受合法NewAPI嵌套用户：%v", err)
	}
	if fixture.paths["/api/v1/auth/login"] != 0 {
		t.Fatal("NewAPI登录已成功却继续尝试其他平台")
	}
}

func TestNewAPIPasswordNestedAdminKeepsRoleCheck(t *testing.T) {
	for _, role := range []int{1, 10} {
		t.Run(fmt.Sprintf("role_%d", role), func(t *testing.T) {
			fixture := newSwitchFixture()
			platform := newAPIPasswordFixturePlatform(t, fixture, map[string]any{"user": map[string]any{"id": 7}}, true)
			original := platform.httpClient.client.Transport
			platform.httpClient.client.Transport = keyUsageRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := original.RoundTrip(request)
				if err != nil || request.URL.Path != "/api/user/self" {
					return response, err
				}
				response.Body.Close()
				body, err := json.Marshal(map[string]any{"success": true, "data": map[string]any{"id": 7, "quota": 100, "role": role}})
				if err != nil {
					t.Fatal(err)
				}
				response.Body = io.NopCloser(bytes.NewReader(body))
				return response, nil
			})
			session, err := platform.LoginNewAPIAdmin("http://fixture.invalid", "fixture-account", "fixture-password")
			if role == 10 {
				if err != nil || session.UserID != "7" || session.AuthMode != AuthModePassword {
					t.Fatalf("嵌套编号的管理员应正常通过：%v", err)
				}
				return
			}
			if err == nil || session.UserID != "" {
				t.Fatal("普通用户因嵌套编号兼容而获得了管理员会话")
			}
		})
	}
}
