package connection_health

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const ErrorIntelligenceWeightInvalid = "admin.connectionHealth.errors.intelligenceWeightInvalid"

type AccountConfig struct {
	UserID             string
	AdminAccountID     string
	TargetID           string
	IntelligenceWeight *int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type TargetIntelligenceWeightResult struct {
	TargetID           string `json:"targetId"`
	IntelligenceWeight *int   `json:"intelligenceWeight"`
}

type NullableIntInput struct {
	Set   bool
	Value *int
}

func (input *NullableIntInput) UnmarshalJSON(data []byte) error {
	input.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		input.Value = nil
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	input.Value = &value
	return nil
}

type UpdateTargetIntelligenceWeightInput struct {
	IntelligenceWeight NullableIntInput `json:"intelligenceWeight"`
}

func (s *Service) SetTargetIntelligenceWeight(
	ctx context.Context,
	userID string,
	targetID string,
	intelligenceWeight *int,
) (TargetIntelligenceWeightResult, error) {
	if intelligenceWeight != nil && (*intelligenceWeight < 0 || *intelligenceWeight > 100) {
		return TargetIntelligenceWeightResult{}, requestError(ErrorIntelligenceWeightInvalid)
	}
	session, target, _, adminAccountID, err := s.resolveManualTarget(ctx, userID, targetID)
	if err != nil {
		return TargetIntelligenceWeightResult{}, err
	}
	if session.Platform != upstream.PlatformSub2API {
		return TargetIntelligenceWeightResult{}, requestError(ErrorProbeTargetNotFound)
	}
	config, err := s.repo.UpsertAccountIntelligenceWeight(
		ctx, userID, adminAccountID, target.TargetID, intelligenceWeight,
	)
	if err != nil {
		return TargetIntelligenceWeightResult{}, err
	}
	return TargetIntelligenceWeightResult{
		TargetID:           config.TargetID,
		IntelligenceWeight: cloneIntPointer(config.IntelligenceWeight),
	}, nil
}
