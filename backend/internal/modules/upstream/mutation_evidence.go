package upstream

import "errors"

func sub2APIMutationData(payload any) (map[string]any, bool) {
	record, ok := payload.(map[string]any)
	if !ok {
		return nil, false
	}
	if code, exists := record["code"]; exists {
		value, valid := code.(float64)
		if !valid || value != 0 {
			return nil, false
		}
	}
	if success, exists := record["success"]; exists {
		value, valid := success.(bool)
		if !valid || !value {
			return nil, false
		}
	}
	if data, exists := record["data"]; exists {
		value, valid := data.(map[string]any)
		return value, valid
	}
	return record, true
}

// These categories are established before writes by Sub2API 0.2.13's admin
// middleware or the selected handler's parameter binding. Status alone proves
// nothing: schedulable may write then fail its readback, and delete may partially
// cascade before returning an error.
func classifySub2APIMutationError(err error, endpoint string) error {
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.MutationOutcome == MutationNotSent {
		return err
	}
	switch requestErr.StatusCode {
	case 401:
		switch requestErr.RemoteReason {
		case "UNAUTHORIZED", "INVALID_ADMIN_KEY", "TOKEN_EXPIRED", "INVALID_TOKEN", "USER_NOT_FOUND", "USER_INACTIVE", "TOKEN_REVOKED":
			requestErr.MutationOutcome = MutationConfirmedRejected
		}
	case 403:
		if requestErr.RemoteReason == "FORBIDDEN" {
			requestErr.MutationOutcome = MutationConfirmedRejected
		}
	case 400:
		if requestErr.RemoteCode == nil || *requestErr.RemoteCode != 400 {
			return err
		}
		switch requestErr.RemoteMessage {
		case "Invalid account ID":
			if endpoint == "schedulable" || endpoint == "delete" {
				requestErr.MutationOutcome = MutationConfirmedRejected
			}
		case "Invalid request":
			if endpoint == "schedulable" || endpoint == "bulk-update" {
				requestErr.MutationOutcome = MutationConfirmedRejected
			}
		case "No updates provided", "account_ids or filters is required":
			if endpoint == "bulk-update" {
				requestErr.MutationOutcome = MutationConfirmedRejected
			}
		}
	}
	return err
}

// Aggregate metadata, when supplied, must agree with the one verified item.
func sub2APIBulkSummaryConsistent(data map[string]any, accountID string) bool {
	for _, field := range []struct {
		key  string
		want float64
	}{{"success", 1}, {"failed", 0}} {
		if raw, exists := data[field.key]; exists {
			value, ok := raw.(float64)
			if !ok || value != field.want {
				return false
			}
		}
	}
	for _, field := range []struct {
		key  string
		want int
	}{{"success_ids", 1}, {"failed_ids", 0}} {
		if raw, exists := data[field.key]; exists {
			ids, ok := raw.([]any)
			if !ok || len(ids) != field.want {
				return false
			}
			if len(ids) == 1 {
				id, valid := strictSub2APIInventoryID(ids[0])
				if !valid || id != accountID {
					return false
				}
			}
		}
	}
	return true
}
