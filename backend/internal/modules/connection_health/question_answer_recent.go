package connection_health

import (
	"context"
	"strings"
	"time"
)

type QuestionAnswerRecentSummary struct {
	BatchID      string                     `json:"batchId"`
	Source       string                     `json:"source"`
	ScheduleName *string                    `json:"scheduleName"`
	CreatedAt    time.Time                  `json:"createdAt"`
	CompletedAt  *time.Time                 `json:"completedAt"`
	Partial      bool                       `json:"partial"`
	Requests     QuestionAnswerRequestStats `json:"requests"`
	Reviews      QuestionAnswerReviewStats  `json:"reviews"`
}
type QuestionAnswerRecentSummaryItem struct {
	ModelControl         *ModelControlAccountSummary  `json:"modelControl"`
	TargetID             string                       `json:"targetId"`
	RecentQuestionAnswer *QuestionAnswerRecentSummary `json:"recentQuestionAnswer"`
	ActiveNewerBatch     bool                         `json:"activeNewerBatch"`
}
type QuestionAnswerRecentSummaries struct {
	ModelControlError string                            `json:"modelControlError,omitempty"`
	Items             []QuestionAnswerRecentSummaryItem `json:"items"`
}
type questionAnswerRecentRepository interface {
	ListLatestTerminalQuestionAnswerSummaries(context.Context, string, []string) (map[string]QuestionAnswerRecentSummaryItem, error)
}

func (s *Service) ListQuestionAnswerRecentSummaries(ctx context.Context, user string, raw []string) (QuestionAnswerRecentSummaries, error) {
	result := QuestionAnswerRecentSummaries{Items: []QuestionAnswerRecentSummaryItem{}}
	if len(raw) == 0 {
		return result, requestError(ErrorRequest)
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range raw {
		if id == "" || strings.TrimSpace(id) != id {
			return result, requestError(ErrorRequest)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > 50 {
		return result, requestError(ErrorRequest)
	}
	workspace, err := s.currentAdminAccountID(ctx, user)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		parsed, ok := parseTargetID(id)
		if !ok || buildTargetID(parsed.platform, parsed.adminAccountID, parsed.accountID) != id {
			return result, requestError(ErrorRequest)
		}
		if parsed.adminAccountID != workspace || parsed.platform != "sub2api" {
			return result, requestError(ErrorNotFound)
		}
	}
	repo, ok := s.questionAnswers.(questionAnswerRecentRepository)
	if !ok {
		return result, requestError(ErrorQuestionAnswerStorage)
	}
	summaries, err := repo.ListLatestTerminalQuestionAnswerSummaries(ctx, user, ids)
	if err != nil {
		return result, requestError(ErrorQuestionAnswerStorage)
	}
	modelSummaries, modelErr := s.ListModelControlAccountSummaries(ctx, user, workspace, ids)
	if modelErr != nil {
		result.ModelControlError = modelControlError("Storage")
	}
	for _, id := range ids {
		item := summaries[id]
		if modelErr == nil {
			item.ModelControl = modelSummaries[id]
		}
		item.TargetID = id
		result.Items = append(result.Items, item)
	}
	return result, nil
}
