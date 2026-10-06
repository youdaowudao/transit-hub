package connection_health

import "time"

const (
	HealthEvidenceLegacy  = "legacy"
	HealthEvidenceValid   = "valid"
	HealthEvidenceInvalid = "invalid"
)

func protocolPointer(protocol TestProtocol) *TestProtocol { return &protocol }

func healthEvidenceMatches(state ConnectionHealthState, protocol TestProtocol) bool {
	if !validTestProtocol(protocol) {
		return false
	}
	switch state.HealthEvidenceStatus {
	case HealthEvidenceLegacy, "":
		// Empty is the pre-migration in-memory representation used by legacy
		// readers; durable new rows always carry invalid explicitly.
		return protocol == TestProtocolChatCompletions && state.HealthEvidenceProtocol == nil
	case HealthEvidenceValid:
		return state.HealthEvidenceProtocol != nil && *state.HealthEvidenceProtocol == protocol
	default:
		return false
	}
}

func successLatencyForProtocol(state ConnectionHealthState, protocol TestProtocol) *int {
	if !healthEvidenceMatches(state, protocol) {
		return nil
	}
	if state.LastSuccessProtocol == nil {
		if protocol != TestProtocolChatCompletions {
			return nil
		}
	} else if *state.LastSuccessProtocol != protocol {
		return nil
	}
	return state.LastSuccessLatencyMs
}

type CurrentHealthResult struct {
	Status      string       `json:"status"`
	At          *time.Time   `json:"at,omitempty"`
	Protocol    TestProtocol `json:"protocol,omitempty"`
	ErrorKey    string       `json:"errorKey,omitempty"`
	ErrorDetail string       `json:"errorDetail,omitempty"`
}

type LastTestAttempt struct {
	At                  *time.Time    `json:"at,omitempty"`
	Result              string        `json:"result,omitempty"`
	Disposition         string        `json:"disposition,omitempty"`
	Protocol            *TestProtocol `json:"protocol"`
	ProbeTimeoutSeconds *int          `json:"probeTimeoutSeconds"`
	ErrorKey            string        `json:"errorKey,omitempty"`
	ErrorDetail         string        `json:"errorDetail,omitempty"`
}

func projectCurrentHealth(state ConnectionHealthState, configuration EffectiveTestConfiguration) CurrentHealthResult {
	result := CurrentHealthResult{Status: "unverified", Protocol: configuration.Protocol}
	if !configuration.usable() || !healthEvidenceMatches(state, configuration.Protocol) {
		return result
	}
	if state.LastAppliedProbeAt != nil && state.LastAppliedProbeResult != nil {
		if state.LastAppliedProbeProtocol == nil || *state.LastAppliedProbeProtocol != configuration.Protocol {
			return result
		}
		result.At = utcTimePointer(state.LastAppliedProbeAt)
		if *state.LastAppliedProbeResult == string(ResultOK) || *state.LastAppliedProbeResult == string(ResultSlowResponse) {
			result.Status = "success"
		} else {
			result.Status = "failure"
			result.ErrorKey = state.LastErrorKey
			result.ErrorDetail = state.LastErrorDetail
		}
		return result
	}
	if configuration.Protocol != TestProtocolChatCompletions {
		return result
	}
	if state.LastFailureAt != nil && (state.LastSuccessAt == nil || !state.LastFailureAt.Before(*state.LastSuccessAt)) {
		result.Status = "failure"
		result.At = utcTimePointer(state.LastFailureAt)
		result.ErrorKey = state.LastErrorKey
		result.ErrorDetail = state.LastErrorDetail
	} else if state.LastSuccessAt != nil {
		result.Status = "success"
		result.At = utcTimePointer(state.LastSuccessAt)
	}
	return result
}

func applyCurrentHealthProjection(model *ModelHealth, state ConnectionHealthState, configuration EffectiveTestConfiguration) {
	projection := projectCurrentHealth(state, configuration)
	model.CurrentHealthResult = &projection
	model.LastAttempt = &LastTestAttempt{At: utcTimePointer(state.LastProbeAt), Protocol: state.LastProbeProtocol, ProbeTimeoutSeconds: state.LastProbeTimeoutSeconds}
	if state.LastAppliedProbeAt != nil && state.LastProbeAt != nil && state.LastAppliedProbeAt.Equal(*state.LastProbeAt) && state.LastAppliedProbeResult != nil {
		model.LastAttempt.Result = *state.LastAppliedProbeResult
		model.LastAttempt.Disposition = "applied"
		if model.LastAttempt.Result != string(ResultOK) && model.LastAttempt.Result != string(ResultSlowResponse) {
			model.LastAttempt.ErrorKey, model.LastAttempt.ErrorDetail = state.LastErrorKey, state.LastErrorDetail
		}
	}
	model.LastSuccessLatencyMs = successLatencyForProtocol(state, configuration.Protocol)
	model.FirstTokenMs, model.FirstEventMs = nil, nil
	if model.LastSuccessLatencyMs != nil {
		model.FirstTokenMs, model.FirstEventMs = state.LastFirstTokenMs, state.LastFirstEventMs
	}
}

func applyLatestAttemptDetails(models []ModelHealth, events []ConnectionHealthEvent, targetID string) {
	for index := range models {
		attempt := models[index].LastAttempt
		if attempt == nil || attempt.At == nil {
			continue
		}
		var latest *ConnectionHealthEvent
		for eventIndex := range events {
			event := &events[eventIndex]
			if event.ConnectionID != targetID || event.ModelName != models[index].ModelName || event.ProbeDisposition == "stale" || event.ProbeDisposition == "" || event.CreatedAt.Before(*attempt.At) {
				continue
			}
			if latest == nil || event.CreatedAt.After(latest.CreatedAt) {
				latest = event
			}
		}
		if latest != nil {
			attempt.Result, attempt.Disposition = latest.Result, latest.ProbeDisposition
			attempt.Protocol, attempt.ProbeTimeoutSeconds = latest.RequestProtocol, latest.RequestTimeoutSeconds
			attempt.ErrorKey, attempt.ErrorDetail = latest.ErrorKey, latest.ErrorDetail
		}
	}
}
