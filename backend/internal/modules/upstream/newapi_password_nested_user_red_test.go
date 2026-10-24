package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 主代理冻结的关键业务回归。全部资料合成，内存传输，不监听端口或访问上游。
func newAPIPasswordFixturePlatform(t *testing.T, fixture *switchFixture, loginData map[string]any, hasCookie bool) *PlatformService {
	t.Helper()
	platform := fixture.platform(t)
	original := platform.httpClient.client.Transport
	platform.httpClient.client.Transport = keyUsageRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Cookie") == "fixture-session=new" && (request.URL.Path == "/api/user/self" || request.URL.Path == "/api/log/self/stat") {
			if request.Header.Get("Authorization") != "" || request.Header.Get("New-Api-User") != fmt.Sprint(fixture.newID) {
				t.Errorf("密码会话的本人/费用请求未沿用Cookie及核实编号")
			}
		}
		response, err := original.RoundTrip(request)
		if err != nil || request.URL.Path != "/api/user/login" {
			return response, err
		}
		response.Body.Close()
		body, err := json.Marshal(map[string]any{"success": true, "data": loginData})
		if err != nil {
			t.Fatal(err)
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		if !hasCookie {
			response.Header.Del("Set-Cookie")
		}
		return response, nil
	})
	return platform
}

func TestNewAPIPasswordNestedUserLogin(t *testing.T) {
	fixture := newSwitchFixture()
	platform := newAPIPasswordFixturePlatform(t, fixture, map[string]any{
		"user":  map[string]any{"id": 7},
		"token": "fixture-token-must-not-be-used",
	}, true)
	result, err := platform.Login("http://fixture.invalid", PlatformNewAPI, "fixture-account", "fixture-password")
	if err != nil {
		t.Fatalf("上游成功并返回Cookie及嵌套用户编号，应能完成密码登录：%v", err)
	}
	if result.Session.AuthMode != AuthModePassword || result.Session.UserID != "7" || !result.Session.UserIDConfirmed || result.Session.Cookie != "fixture-session=new" {
		t.Fatal("没有形成已核实编号的原Cookie密码会话")
	}
	if result.Session.AccessToken != "" || result.Session.RefreshToken != "" {
		t.Fatal("不应将登录返回的Token替代Cookie会话")
	}
	if result.Metrics.TodayConsumeStatus != "ok" || result.Metrics.TodayConsume.Value == nil || *result.Metrics.TodayConsume.Value != 0 {
		t.Fatal("密码登录后没有读取到确定的今日费用")
	}
	if fixture.logins != 1 || fixture.paths["/api/user/self"] != 1 || fixture.paths["/api/log/self/stat"] != 1 {
		t.Fatalf("不能新增登录或本人补查请求：login=%d self=%d cost=%d", fixture.logins, fixture.paths["/api/user/self"], fixture.paths["/api/log/self/stat"])
	}
}

func TestNewAPIPasswordNestedUserUpdateIdentity(t *testing.T) {
	for _, newID := range []int{7, 8} {
		t.Run(fmt.Sprintf("new_user_%d", newID), func(t *testing.T) {
			fixture := newSwitchFixture()
			fixture.newID = newID
			service, cache, repository, original := switchSetup(t, fixture, PlatformNewAPI, AuthModeUserKey, true)
			service.platformService = newAPIPasswordFixturePlatform(t, fixture, map[string]any{"user": map[string]any{"id": newID}, "token": "fixture-unused-token"}, true)
			before := cloneSite(original)
			dto := switchDTO(original, AuthModePassword)
			dto.Account = "owner@example.com"
			response, err := service.Update(context.Background(), "fixture-user", original.ID, dto)
			if newID == 7 {
				if err != nil {
					t.Fatalf("同一个账号应能从Key更新为嵌套返回的密码会话：%v", err)
				}
				if response.ID != original.ID || response.AuthMode != AuthModePassword {
					t.Fatal("更新改变了站点编号或没有保存密码方式")
				}
				switchSuccess(t, cache, repository, original, AuthModePassword, "7", dto.Account)
				return
			}
			switchError(t, err, "另一个账号")
			switchUnchanged(t, cache, repository, &before)
		})
	}
}

func TestNewAPIPasswordExtraVerificationStopsCompleteSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag any
		want string
	}{
		{"explicit_two_factor", true, "明确要求两步验证"},
		{"unknown_string", "false", "额外验证"},
		{"unknown_null", nil, "额外验证"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSwitchFixture()
			platform := newAPIPasswordFixturePlatform(t, fixture, map[string]any{"id": 7, "require_2fa": tc.flag}, true)
			_, err := platform.Login("http://fixture.invalid", PlatformNewAPI, "fixture-account", "fixture-password")
			var failure *RequestError
			if !errors.As(err, &failure) || failure.Reason != ErrorLoginIncomplete || failure.Stage != "login" || failure.StatusCode != 200 || !strings.Contains(failure.UpstreamMessage, tc.want) {
				t.Fatalf("额外验证未完成时应明确拒绝密码登录：%v", err)
			}
			if fixture.paths["/api/user/self"] != 0 || fixture.paths["/api/log/self/stat"] != 0 || fixture.paths["/api/status"] != 0 {
				t.Fatal("额外验证未完成时仍继续读取账号或费用")
			}
		})
	}
}
