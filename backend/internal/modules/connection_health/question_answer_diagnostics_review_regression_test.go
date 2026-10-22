package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestQuestionAnswerReviewAuthHeadersCannotLeaveAnotherCredential(t *testing.T) {
	for _, input := range []string{
		"Authorization: Bearer qa-other-auth-marker",
		"Authorization: Basic cWEtb3RoZXItYXV0aA==",
		`malformed HTTP header "Authorization: Bearer qa-other-auth-marker"`,
		`{"Authorization":"Basic cWEtb3RoZXItYXV0aA=="}`,
		`{"header":"Authorization: Basic cWEtb3RoZXItYXV0aA=="}`,
		`{\"Authorization\":\"Basic cWEtb3RoZXItYXV0aA==\"}`,
	} {
		cause := questionAnswerSafeCause(errors.New(input), "a-different-account-key")
		if strings.Contains(cause, "qa-other-auth-marker") || strings.Contains(cause, "cWEtb3RoZXItYXV0aA") {
			t.Error("authentication header sanitation left a credential marker")
		}
	}
}

func TestQuestionAnswerReviewKnownPreparationCausesRemainReadable(t *testing.T) {
	for _, item := range []struct {
		err       error
		key, word string
	}{
		{errors.Join(requestError(ErrorBaseURLUnavailable), &upstream.ProbeCredentialError{Reason: upstream.ReasonBaseURLUnavailable}), ErrorBaseURLUnavailable, "地址"},
		{errors.Join(requestError(ErrorCredentialsRedacted), &upstream.ProbeCredentialError{Reason: upstream.ReasonCredentialsRedacted}), ErrorCredentialsRedacted, "凭据"},
		{requestError(upstream.ErrorUpstreamKeyExpired), upstream.ErrorUpstreamKeyExpired, "过期"},
		{requestError(upstream.ErrorUpstreamKeyQuotaExhausted), upstream.ErrorUpstreamKeyQuotaExhausted, "额度"},
		{requestError(upstream.ErrorUpstreamInsufficientBalance), upstream.ErrorUpstreamInsufficientBalance, "余额"},
		{requestError(upstream.ErrorAnnouncementAckRequired), upstream.ErrorAnnouncementAckRequired, "公告"},
	} {
		response := httptest.NewRecorder()
		writeQuestionAnswerError(response, questionAnswerOperationError(item.err, "preparing", "not_started", ""), "preparing", "not_started", "")
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		detail, _ := body["detail"].(string)
		if body["message"] != item.key || !strings.Contains(detail, item.word) || strings.Contains(detail, "base_url_unavailable") || strings.Contains(detail, "credentials_redacted") || strings.Contains(detail, "接口未提供更具体原因") {
			t.Errorf("known preparation classification was lost: key=%s detail=%s", item.key, detail)
		}
	}
}

func TestQuestionAnswerReviewRecoveredWriteCannotMaskFinalResult(t *testing.T) {
	for _, item := range []struct {
		name, body string
		success    bool
	}{
		{"complete", `{"choices":[{"message":{"content":"complete answer"}}]}`, true},
		{"invalid-json", `{ broken`, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			p := newQuestionAnswerProgress(time.Now())
			p.startRequest(time.Second, time.Now())
			r := &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(request.Context())
				trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("temporary write marker")})
				trace.WroteRequest(httptrace.WroteRequestInfo{})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(item.body))}, nil
			})}}
			result := r.AskDetailedWithProgress(context.Background(), upstream.ProbeCredential{BaseURL: "https://unit.invalid"}, "model", "q", QuestionAnswerReasoningEffortMedium, p)
			if result.Diagnostics == nil || result.Diagnostics.RequestWrittenAt == nil {
				t.Fatal("successful write observation was lost")
			}
			d := result.Diagnostics
			if item.success {
				if result.ErrorType != "" || d.FailureStep != "" || d.LocalError != "" {
					t.Fatal("a recovered write left failure evidence on a complete answer")
				}
			} else if result.ErrorType == "" || d.FailureStep != "parse" || !strings.Contains(d.LocalError, "invalid character") || strings.Contains(d.LocalError, "temporary write marker") {
				t.Fatal("a recovered write masked the final parsing failure")
			}
		})
	}
}
