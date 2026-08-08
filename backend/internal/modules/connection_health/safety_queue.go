package connection_health

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const (
	RemoteActionSafetyConfirmationQueued = "safety_confirmation_queued"
	RemoteActionSafetyGuardHeld          = "safety_guard_held"
	RemoteActionSafetyGateUnavailable    = "safety_gate_unavailable"
	RemoteActionSafetyCircuitOpen        = "safety_circuit_open"
	RemoteActionSafetyStaleEpoch         = "safety_stale_epoch"
)

type automaticProbeGuard struct {
	queueEpoch       int64
	normalGeneration int64
	observationEpoch int64
}

func (s *Service) automaticProbeAllowed(ctx context.Context, userID, workspaceID, targetID, faultDomain string, expectedEpoch int64) (bool, error) {
	if s.safetyRepo == nil {
		return false, nil
	}
	currentEpoch, err := s.safetyRepo.GetAbnormalQueueEpoch(ctx, userID, workspaceID)
	if err != nil || currentEpoch != expectedEpoch {
		return false, err
	}
	open, err := s.safetyRepo.TargetCircuitOpen(ctx, userID, workspaceID, targetID)
	if err != nil || open {
		return false, err
	}
	if faultDomain == "" {
		endpoint, endpointErr := s.safetyRepo.GetTargetFaultEndpoint(ctx, userID, workspaceID, targetID)
		if endpointErr != nil {
			return false, endpointErr
		}
		if endpoint == "" {
			anyOpen, circuitErr := s.safetyRepo.AnyIncidentCircuitOpen(ctx, userID, workspaceID)
			if circuitErr != nil || anyOpen {
				return false, circuitErr
			}
		} else {
			for _, family := range []string{"server", "transport"} {
				incident, circuitErr := s.safetyRepo.GetIncidentCircuit(ctx, userID, workspaceID, endpoint+":"+family)
				if circuitErr != nil {
					return false, circuitErr
				}
				if incident != nil && incident.State != CircuitClosed {
					return false, nil
				}
			}
		}
	}
	if faultDomain != "" {
		incident, err := s.safetyRepo.GetIncidentCircuit(ctx, userID, workspaceID, faultDomain)
		if err != nil {
			return false, err
		}
		if incident != nil && incident.State != CircuitClosed {
			return false, nil
		}
	}
	return true, nil
}

func isDestructiveConfirmationResult(result ResultKey) bool {
	switch result {
	case ResultServerError, ResultAuth, ResultModelNotFound, ResultNetworkFluctuation, ResultInvalidResponse:
		return true
	default:
		return false
	}
}

func confirmationFaultDomain(baseURL string, target AdminProbeTarget, result ResultKey) string {
	// Transport/server failures share the normalized endpoint and error family.
	// Credential/model errors remain target-scoped and cannot open a broad circuit.
	switch result {
	case ResultServerError, ResultNetworkFluctuation:
		if endpoint := confirmationFaultEndpoint(baseURL); endpoint != "" {
			family := "server"
			if result == ResultNetworkFluctuation {
				family = "transport"
			}
			return endpoint + ":" + family
		}
	}
	return target.TargetID + ":" + string(result)
}

func confirmationFaultEndpoint(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	endpointPath := path.Join("/", parsed.Path, "/v1/chat/completions")
	return strings.ToLower(parsed.Host) + endpointPath
}

func (s *Service) enqueueHealthIncidentConfirmation(
	ctx context.Context,
	userID string,
	workspaceID string,
	target AdminProbeTarget,
	cred upstream.ProbeCredential,
	spec probeModelSpec,
	outcome ProbeOutcome,
	guard automaticProbeGuard,
	now time.Time,
) (string, error) {
	if !policyRemoteActionEnabled(spec.policy) || !isDestructiveConfirmationResult(outcome.Result) {
		return "", nil
	}
	// A formal/manual probe is an observation only. The scheduler is the sole
	// producer of health-incident confirmation work.
	if s.safetyRepo == nil {
		return RemoteActionSafetyGateUnavailable, nil
	}
	repo := s.safetyRepo
	settings, err := repo.GetSafetySettings(ctx, userID, workspaceID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	queueEpoch, err := repo.GetAbnormalQueueEpoch(ctx, userID, workspaceID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	generation, err := repo.MutationGeneration(ctx, userID, workspaceID, target.AccountID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	item := AbnormalQueueItem{
		UserID:             userID,
		WorkspaceID:        workspaceID,
		TargetID:           target.TargetID,
		AccountID:          target.AccountID,
		ModelName:          spec.modelName,
		ProviderFamily:     spec.providerFamily,
		ProbePrompt:        spec.probePrompt,
		MaxProbeTokens:     spec.maxProbeTokens,
		Kind:               QueueKindConfirmation,
		Source:             SafetySourceHealthIncident,
		IncidentID:         "target:" + target.TargetID + ":" + spec.modelName,
		FaultDomain:        confirmationFaultDomain(cred.BaseURL, target, outcome.Result),
		ObservationEpoch:   guard.observationEpoch,
		NormalGeneration:   guard.normalGeneration,
		QueueEpoch:         guard.queueEpoch,
		Attempt:            1,
		RequiredAttempts:   settings.ConfirmationObservationCount,
		ConfirmationDelays: append([]int(nil), settings.ConfirmationDelaysSeconds...),
		ConfirmationJitter: settings.ConfirmationJitterSeconds,
		NextAttemptAt:      now.Add(confirmationDelay(settings, 1, nil)),
		ActionKey: fmt.Sprintf(
			"confirmation:%s:%s:%d:%d",
			target.TargetID,
			spec.modelName,
			guard.queueEpoch,
			generation,
		),
		MutationGeneration: generation,
		State:              QueueStateQueued,
		ExpectedResult:     string(outcome.Result),
		LastResult:         string(outcome.Result),
	}
	if queueEpoch != guard.queueEpoch {
		return RemoteActionSafetyStaleEpoch, nil
	}
	// Record the shared-fault observation before queue admission. A full isolated
	// confirmation queue must not prevent the second affected account from opening
	// the common circuit and cancelling the existing fan-out.
	_, opened, err := repo.ObserveIncidentFailure(ctx, item, target.TargetID)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	if opened {
		return RemoteActionSafetyCircuitOpen, nil
	}
	queued, _, err := repo.EnqueueAbnormalQueueItem(ctx, item, settings.AbnormalQueueCapacity)
	if err != nil {
		return RemoteActionSafetyGateUnavailable, err
	}
	if queued.State == QueueStateGuardHeld {
		return RemoteActionSafetyGuardHeld, nil
	}
	if queued.State == QueueStateCancelled {
		return RemoteActionSafetyStaleEpoch, nil
	}
	return RemoteActionSafetyConfirmationQueued, nil
}
