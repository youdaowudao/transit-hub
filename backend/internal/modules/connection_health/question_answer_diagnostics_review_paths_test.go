package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestQuestionAnswerReviewAdditionalHeaderRepresentationsAreSafe(t *testing.T) {
	marker := "qa-additional-credential-marker"
	for _, text := range []string{
		"Proxy-Authorization: Bearer " + marker,
		`{"\u0041uthorization":"Basic ` + marker + `"}`,
		`malformed header "X-Api-Key: ` + marker + `"`,
		"Set-Cookie: session=" + marker,
		"Cookie: session=" + marker + "; other=value",
		"transport rejected Basic " + marker,
		`{"header":"authorization: Digest username=qa-user,response=` + marker + `"}`,
		`transport Bear\u0065r\u0020` + marker,
		`transport Basi\u0063\u0020` + marker,
		`token\u003d` + marker,
		`password\u003a\u0020` + marker,
		`https\u003a\u002f\u002fuser:` + marker + `@unit.invalid/path?token=value`,
	} {
		safe := questionAnswerSafeCause(errors.New(text))
		if strings.Contains(safe, marker) || safe == "" {
			t.Fatal("header error exposed a credential or lost its safe fallback")
		}
	}
}

func TestQuestionAnswerReviewStartHTTPPreparationMatrixDoesNotCreate(t *testing.T) {
	for _, item := range []struct {
		name, key, word, reason, code, modelBody string
		modelErr                                 bool
		configuration                            string
	}{
		{name: "credential", key: ErrorCredentialUnavailable, word: "凭据", reason: upstream.ReasonCredentialUnavailable},
		{name: "verification", key: ErrorSecureVerificationRequired, word: "验证", reason: upstream.ReasonSecureVerificationRequired},
		{name: "base-url", key: ErrorBaseURLUnavailable, word: "地址", reason: upstream.ReasonBaseURLUnavailable},
		{name: "model", key: ErrorModelUnavailable, word: "模型", reason: upstream.ReasonModelUnavailable},
		{name: "export", key: ErrorExportUnavailable, word: "导出", reason: upstream.ReasonExportUnavailable},
		{name: "redacted", key: ErrorCredentialsRedacted, word: "凭据已被隐藏", reason: upstream.ReasonCredentialsRedacted},
		{name: "expired", key: upstream.ErrorUpstreamKeyExpired, word: "过期", code: "API_KEY_EXPIRED"},
		{name: "quota", key: upstream.ErrorUpstreamKeyQuotaExhausted, word: "额度", code: "API_KEY_QUOTA_EXHAUSTED"},
		{name: "quota-alias", key: upstream.ErrorUpstreamKeyQuotaExhausted, word: "额度", code: "INSUFFICIENT_QUOTA"},
		{name: "balance", key: upstream.ErrorUpstreamInsufficientBalance, word: "余额", code: "INSUFFICIENT_BALANCE"},
		{name: "announcement", key: upstream.ErrorAnnouncementAckRequired, word: "公告", code: "ANNOUNCEMENT_ACK_REQUIRED"},
		{name: "models-unavailable", key: ErrorModelListUnavailable, word: "模型列表", modelErr: true},
		{name: "models-invalid", key: ErrorModelListInvalid, word: "格式", modelBody: "{invalid"},
		{name: "models-403-unknown", key: ErrorModelListUnavailable, word: "模型列表", code: "UNKNOWN_REFUSAL"},
		{name: "configuration-unavailable", key: ErrorTestConfigurationUnavailable, word: "测试配置", configuration: "unavailable"},
		{name: "configuration-conflict", key: ErrorTestConfigurationConflict, word: "冲突", configuration: "conflict"},
		{name: "account-list", key: ErrorAccountsFetch, word: "账号列表", configuration: "account-list"},
	} {
		t.Run(item.name, func(t *testing.T) {
			s := panicPathsScheduleService(t, nil)
			repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "q"})
			s.questionAnswers = repo
			reader := s.platformGroups.(*c2ScheduleInventoryReader)
			if item.reason != "" {
				reader.credErr = map[string]error{"a": &upstream.ProbeCredentialError{Reason: item.reason}}
			}
			health := s.repo.(*fakeRepository)
			if item.configuration == "unavailable" {
				health.testConfigurationErr = errors.New("isolated config read failure")
			}
			if item.configuration == "account-list" {
				reader.errByGrp = map[string]error{"selected": errors.New("isolated account list read failure")}
			}
			if item.configuration == "conflict" {
				reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "conflict", Name: "Conflict"})
				reader.accountsByGrp["conflict"] = []upstream.AdminGroupAccountInfo{{ID: "a", Name: "A", Models: "available"}}
				health.testConfigurations = []GroupTestConfig{
					{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "selected", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 20},
					{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "conflict", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 20},
				}
			}
			asks := 0
			s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
				asks++
				return nil, errors.New("question must not be sent")
			})
			s.modelDiscovery.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
				if item.modelErr {
					return nil, errors.New("isolated transport failure")
				}
				body, status := item.modelBody, 200
				if item.code != "" {
					status = 403
					body = `{"code":"` + item.code + `"}`
				}
				if body == "" {
					body = `{"data":[{"id":"available"}]}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			mux := http.NewServeMux()
			RegisterRoutes(mux, s)
			response := internalHTTPQuestionAnswerRequest(mux, http.MethodPost, "", `{"models":["available"],"questionIds":["question"],"repeatCount":1}`)
			var payload map[string]any
			if json.Unmarshal(response.Body.Bytes(), &payload) != nil || response.Code != 400 || payload["message"] != item.key || payload["operationResult"] != "not_started" {
				t.Fatalf("preparation classification changed: status=%d key=%v result=%v", response.Code, payload["message"], payload["operationResult"])
			}
			detail, _ := payload["detail"].(string)
			if !strings.Contains(detail, item.word) || strings.Contains(detail, "admin.") || strings.Contains(detail, item.reason) && item.reason != "" {
				t.Fatalf("known safe reason was swallowed: %s", detail)
			}
			if !strings.Contains(detail, "本次尚未创建题目") || !strings.Contains(detail, "重新读取") && !strings.Contains(detail, "检查") {
				t.Fatalf("missing action/result guidance: %s", detail)
			}
			if item.name == "models-403-unknown" {
				for _, guess := range []string{"余额不足", "Key已过期", "额度已用完", "权限不足"} {
					if strings.Contains(detail, guess) {
						t.Fatal("unknown403 acquired a guessed cause")
					}
				}
			}
			calls, _, records := repo.createMetrics()
			if calls != 0 || records != 0 || asks != 0 {
				t.Fatal("preparation refusal created or sent a question")
			}
		})
	}
}

func TestQuestionAnswerReviewWriteRecoveryUsesFinalDirectCause(t *testing.T) {
	for _, item := range []struct {
		name, body, contentType, step, cause string
		protocol                             TestProtocol
		doFailure, readFailure               bool
	}{
		{name: "final-do", step: "request", cause: "final connection refused", doFailure: true},
		{name: "final-body-read", step: "receive", cause: "unexpected EOF", readFailure: true},
		{name: "responses-read-before-generic", protocol: TestProtocolResponses, body: "data: {invalid", step: "receive", cause: "unexpected EOF", readFailure: true},
		{name: "responses-reported-failure", protocol: TestProtocolResponses, body: `{"status":"failed","error":{"message":"specific peer failure"}}`, step: "parse"},
		{name: "messages-event-format", protocol: TestProtocolMessages, contentType: "text/event-stream", body: "data: {broken}\n\n", step: "parse", cause: "invalid character"},
	} {
		t.Run(item.name, func(t *testing.T) {
			p := newQuestionAnswerProgress(time.Now())
			p.startRequest(time.Second, time.Now())
			runner := &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(request.Context())
				trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("temporary write marker")})
				if item.doFailure {
					return nil, errors.New("final connection refused")
				}
				trace.WroteRequest(httptrace.WroteRequestInfo{})
				var body io.Reader = strings.NewReader(item.body)
				if item.readFailure {
					body = io.MultiReader(body, diagnosticsREDReadFailure{})
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{item.contentType}}, Body: io.NopCloser(body)}, nil
			})}}
			protocol := item.protocol
			if protocol == "" {
				protocol = TestProtocolChatCompletions
			}
			result := runner.AskDetailedWithProgress(context.Background(), upstream.ProbeCredential{BaseURL: "https://unit.invalid", AnthropicAuth: "x_api_key"}, "model", "q", QuestionAnswerReasoningEffortMedium, p, protocol)
			if result.ErrorType == "" || result.Diagnostics == nil || result.Diagnostics.FailureStep != item.step || !strings.Contains(result.Diagnostics.LocalError, item.cause) || strings.Contains(result.Diagnostics.LocalError, "temporary write marker") {
				t.Fatalf("temporary attempt masked final result: type=%s diagnostics=%+v", result.ErrorType, result.Diagnostics)
			}
			if item.name == "responses-reported-failure" && !strings.Contains(result.UpstreamExcerpt, "specific peer failure") {
				t.Fatal("peer failure explanation was lost")
			}
			if item.doFailure && result.Diagnostics.RequestWrittenAt != nil {
				t.Fatal("failed write was marked complete")
			}
		})
	}
}
