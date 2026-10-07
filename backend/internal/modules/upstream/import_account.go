package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ImportCredentialNotSent   = "not_sent"
	ImportCredentialRejected  = "confirmed_rejected"
	ImportCredentialKnownID   = "created_known_id"
	ImportCredentialUnknownID = "created_unknown_id"
	ImportCredentialUncertain = "uncertain"
	ImportCleanupConfirmed    = "confirmed"
	ImportCleanupRetained     = "retained"
	ImportCleanupPending      = "pending"
)

// ImportCredential retains every confirmed stage, including an ID whose secret
// could not be retrieved. It must never cross the browser boundary.
type ImportCredential struct {
	ID      string
	Key     string
	Name    string
	Outcome string
}

func importMutationIDEvidence(record map[string]any) any {
	data, _ := record["data"].(map[string]any)
	id, valid := strictSub2APIInventoryID(data["id"])
	if !valid {
		return nil
	}
	return map[string]any{"data": map[string]any{"id": id}}
}

func strictImportSub2APIData(payload any) (map[string]any, bool) {
	record, ok := payload.(map[string]any)
	if !ok {
		return nil, false
	}
	code, ok := strictInventoryNonnegative(record["code"])
	if !ok || code != 0 {
		return nil, false
	}
	if value, exists := record["success"]; exists {
		if success, valid := value.(bool); !valid || !success {
			return nil, false
		}
	}
	data, ok := record["data"].(map[string]any)
	return data, ok
}

func strictImportNewAPISuccess(payload any) bool {
	record, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	success, ok := record["success"].(bool)
	if !ok || !success {
		return false
	}
	if value, exists := record["code"]; exists {
		code, valid := strictInventoryNonnegative(value)
		if !valid || code != 0 {
			return false
		}
	}
	return true
}

func importCredentialFailureOutcome(err error) string {
	switch RemoteMutationOutcome(err) {
	case MutationNotSent:
		return ImportCredentialNotSent
	case MutationConfirmedRejected:
		return ImportCredentialRejected
	default:
		return ImportCredentialUncertain
	}
}

// A failed creation receipt that still identifies a resource contradicts any
// claim that nothing was created. Keep that evidence uncertain, without changing
// the shared rejection classifier used by older mutation callers.
func classifyImportCreationError(err error, platform Platform, endpoint, observedID string) error {
	if observedID != "" {
		var requestErr *RequestError
		if errors.As(err, &requestErr) {
			result := *requestErr
			result.MutationOutcome = MutationUncertain
			return &result
		}
		return &RequestError{MessageKey: ErrorRequest, Platform: platform, MutationOutcome: MutationUncertain, Cause: err}
	}
	if platform == PlatformSub2API {
		return classifySub2APIMutationError(err, endpoint)
	}
	return err
}

func (s *PlatformService) CreateImportUpstreamCredentialContext(ctx context.Context, session Session, name, groupID string) (ImportCredential, error) {
	result := ImportCredential{Name: name, Outcome: ImportCredentialNotSent}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var options requestOptions
	var endpoint string
	switch session.Platform {
	case PlatformSub2API:
		id, ok := strictSub2APIInventoryID(groupID)
		if !ok || strings.TrimSpace(session.AccessToken) == "" {
			return result, localMutationError(ErrorAuth)
		}
		numericID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return result, localMutationError(ErrorInvalidResponse)
		}
		options = sub2APIUserAuthOptions(session)
		options.Body = map[string]any{"name": name, "group_id": numericID}
		endpoint = session.BaseURL + "/api/v1/keys"
	case PlatformNewAPI:
		if !session.IsAuthenticated() {
			return result, localMutationError(ErrorAuth)
		}
		options = newAPIAuthOptions(session)
		options.Body = map[string]any{"name": name, "remain_quota": 0, "unlimited_quota": true, "expired_time": -1, "model_limits_enabled": false, "model_limits": "", "allow_ips": "", "group": groupID, "cross_group_retry": false}
		endpoint = session.BaseURL + "/api/token/"
	default:
		return result, localMutationError(ErrorAuth)
	}
	options.Method = http.MethodPost
	options.PreserveImportMutationID = true
	response, err := s.httpClient.requestJSONWithContext(ctx, endpoint, options)
	if err != nil {
		if record, ok := response.Payload.(map[string]any); ok {
			data, _ := record["data"].(map[string]any)
			result.ID, _ = strictSub2APIInventoryID(data["id"])
		}
		err = classifyImportCreationError(err, session.Platform, "create-key", result.ID)
		result.Outcome = importCredentialFailureOutcome(err)
		return result, err
	}
	result.Outcome = ImportCredentialUncertain
	if session.Platform == PlatformSub2API {
		// Preserve an observed ID even when the success envelope or Key is invalid.
		record, _ := response.Payload.(map[string]any)
		observed, _ := record["data"].(map[string]any)
		if id, valid := strictSub2APIInventoryID(observed["id"]); valid {
			result.ID = id
		}
		data, valid := strictImportSub2APIData(response.Payload)
		if !valid {
			return result, newRequestError(ErrorInvalidResponse, session.Platform)
		}
		result.Outcome = ImportCredentialUnknownID
		id, valid := strictSub2APIInventoryID(data["id"])
		if !valid {
			return result, newRequestError(ErrorInvalidResponse, session.Platform)
		}
		result.ID, result.Outcome = id, ImportCredentialKnownID
		key, valid := data["key"].(string)
		if !valid || strings.TrimSpace(key) == "" {
			return result, newRequestError(ErrorInvalidResponse, session.Platform)
		}
		result.Key = key
		return result, nil
	}
	if !strictImportNewAPISuccess(response.Payload) {
		return result, newRequestError(ErrorInvalidResponse, session.Platform)
	}
	result.Outcome = ImportCredentialUnknownID
	id, err := s.findImportNewAPITokenContext(ctx, session, name)
	if err != nil {
		return result, err
	}
	result.ID, result.Outcome = id, ImportCredentialKnownID
	options = newAPIAuthOptions(session)
	options.Method = http.MethodPost
	response, err = s.httpClient.requestJSONWithContext(ctx, session.BaseURL+"/api/token/"+id+"/key", options)
	if err != nil {
		return result, err
	}
	if !strictImportNewAPISuccess(response.Payload) {
		return result, newRequestError(ErrorInvalidResponse, session.Platform)
	}
	record, _ := response.Payload.(map[string]any)
	data, ok := record["data"].(map[string]any)
	if !ok {
		return result, newRequestError(ErrorInvalidResponse, session.Platform)
	}
	key, valid := data["key"].(string)
	if !valid || strings.TrimSpace(key) == "" {
		return result, newRequestError(ErrorInvalidResponse, session.Platform)
	}
	result.Key = key
	return result, nil
}

func (s *PlatformService) findImportNewAPITokenContext(ctx context.Context, session Session, name string) (string, error) {
	total, fetched, matches := -1, 0, 0
	matchedID := ""
	seen := map[string]bool{}
	for page := 1; page <= 10; page++ {
		response, err := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+fmt.Sprintf("/api/token/?p=%d&page_size=100", page), newAPIAuthOptions(session))
		if err != nil {
			return "", err
		}
		if !strictImportNewAPISuccess(response.Payload) {
			return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
		}
		record, _ := response.Payload.(map[string]any)
		data, valid := record["data"].(map[string]any)
		if !valid {
			return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
		}
		pageValue, pOK := strictInventoryNonnegative(data["page"])
		pageSize, sOK := strictInventoryNonnegative(data["page_size"])
		pageTotal, tOK := strictInventoryNonnegative(data["total"])
		items, iOK := data["items"].([]any)
		if !pOK || !sOK || !tOK || !iOK || pageValue != page || pageSize != 100 || pageTotal > 1000 || total >= 0 && pageTotal != total {
			return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
		}
		total = pageTotal
		expected := min(100, total-fetched)
		if expected < 0 || len(items) != expected {
			return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
		}
		for _, item := range items {
			row, valid := item.(map[string]any)
			if !valid {
				return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
			}
			id, valid := strictSub2APIInventoryID(row["id"])
			itemName, nameValid := row["name"].(string)
			if !valid || !nameValid || seen[id] {
				return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
			}
			seen[id] = true
			if itemName == name {
				matches++
				matchedID = id
			}
		}
		fetched += len(items)
		if fetched == total {
			if matches == 1 {
				return matchedID, nil
			}
			return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
		}
	}
	return "", newRequestError(ErrorInvalidResponse, PlatformNewAPI)
}

func (s *PlatformService) DeleteImportUpstreamCredentialContext(ctx context.Context, session Session, id string) (string, error) {
	if _, valid := strictSub2APIInventoryID(id); !valid {
		return ImportCleanupRetained, localMutationError(ErrorInvalidResponse)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var endpoint string
	var options requestOptions
	if session.Platform == PlatformSub2API {
		if strings.TrimSpace(session.AccessToken) == "" {
			return ImportCleanupRetained, localMutationError(ErrorAuth)
		}
		endpoint, options = session.BaseURL+"/api/v1/keys/"+id, sub2APIUserAuthOptions(session)
	} else if session.Platform == PlatformNewAPI {
		endpoint, options = session.BaseURL+"/api/token/"+id, newAPIAuthOptions(session)
	} else {
		return ImportCleanupRetained, localMutationError(ErrorAuth)
	}
	options.Method = http.MethodDelete
	response, err := s.httpClient.requestJSONWithContext(ctx, endpoint, options)
	if err != nil {
		if session.Platform == PlatformSub2API {
			err = classifySub2APIMutationError(err, "delete-key")
		}
		if outcome := RemoteMutationOutcome(err); outcome == MutationNotSent || outcome == MutationConfirmedRejected {
			return ImportCleanupRetained, err
		}
		return ImportCleanupPending, err
	}
	confirmed := false
	if session.Platform == PlatformSub2API {
		data, valid := strictImportSub2APIData(response.Payload)
		_, messageValid := data["message"].(string)
		confirmed = valid && messageValid
	} else {
		confirmed = strictImportNewAPISuccess(response.Payload)
	}
	if !confirmed {
		return ImportCleanupPending, newRequestError(ErrorInvalidResponse, session.Platform)
	}
	return ImportCleanupConfirmed, nil
}

func (s *PlatformService) PreviewSub2APIImportModelsContext(ctx context.Context, session Session, platform, baseURL, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return nil, localMutationError(ErrorAuth)
	}
	options := adminAuthOptions(session)
	options.Method = http.MethodPost
	options.Body = map[string]any{"platform": platform, "type": "apikey", "base_url": baseURL, "api_key": key}
	response, err := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+"/api/v1/admin/accounts/models/sync-upstream-preview", options)
	if err != nil {
		return nil, err
	}
	data, valid := strictImportSub2APIData(response.Payload)
	if !valid {
		return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	items, valid := data["models"].([]any)
	if !valid || len(items) == 0 {
		return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(items))
	for _, item := range items {
		id, valid := item.(string)
		id = strings.TrimSpace(id)
		if !valid || id == "" || strings.Contains(id, "*") {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		if !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models, nil
}

func (s *PlatformService) CreateSub2APIImportAccountContext(ctx context.Context, session Session, payload map[string]any) (string, error) {
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return "", localMutationError(ErrorAuth)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	options := adminAuthOptions(session)
	options.Method, options.Body = http.MethodPost, payload
	options.PreserveImportMutationID = true
	response, err := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+"/api/v1/admin/accounts", options)
	if err != nil {
		record, _ := response.Payload.(map[string]any)
		data, _ := record["data"].(map[string]any)
		id, _ := strictSub2APIInventoryID(data["id"])
		return id, classifyImportCreationError(err, session.Platform, "create", id)
	}
	record, _ := response.Payload.(map[string]any)
	observed, _ := record["data"].(map[string]any)
	observedID, _ := strictSub2APIInventoryID(observed["id"])
	data, valid := strictImportSub2APIData(response.Payload)
	if !valid {
		return observedID, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	id, valid := strictSub2APIInventoryID(data["id"])
	if !valid {
		return "", newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	return id, nil
}

// Sub2APIImportConfiguration is a safe internal projection: no API keys, base
// URLs, credentials objects or extra objects leave this function.
type Sub2APIImportConfiguration struct {
	ID, Name, Platform, Type                                                           string
	Priority, Concurrency                                                              int
	Passthrough, PoolMode, UpstreamBillingProbeEnabled, UpstreamBillingRateSyncEnabled bool
	Groups                                                                             []Sub2APIImportGroup
	ModelMapping                                                                       map[string]string
}
type Sub2APIImportGroup struct{ ID, Name string }

func importBoolean(record map[string]any, key string) (bool, bool) {
	value, exists := record[key]
	if !exists {
		return false, true
	}
	result, valid := value.(bool)
	return result, valid
}

func (s *PlatformService) ReadSub2APIImportConfigurationContext(ctx context.Context, session Session, accountID string) (Sub2APIImportConfiguration, error) {
	var result Sub2APIImportConfiguration
	if _, valid := strictSub2APIInventoryID(accountID); !valid || session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return result, localMutationError(ErrorAuth)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+"/api/v1/admin/accounts/"+accountID, adminAuthOptions(session))
	if err != nil {
		return result, err
	}
	data, valid := strictImportSub2APIData(response.Payload)
	if !valid {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result.ID, valid = strictSub2APIInventoryID(data["id"])
	if !valid || result.ID != accountID {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result.Name, valid = data["name"].(string)
	if !valid || strings.TrimSpace(result.Name) == "" {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result.Platform, valid = data["platform"].(string)
	if !valid || strings.TrimSpace(result.Platform) == "" {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result.Type, valid = data["type"].(string)
	if !valid || strings.TrimSpace(result.Type) == "" {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result.Priority, valid = strictInventoryNonnegative(data["priority"])
	if !valid || result.Priority > 2147483647 {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result.Concurrency, valid = strictInventoryNonnegative(data["concurrency"])
	if !valid || result.Concurrency < 1 || result.Concurrency > 2147483647 {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	credentials, valid := data["credentials"].(map[string]any)
	if !valid {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	extra := map[string]any{}
	if value, exists := data["extra"]; exists && value != nil {
		extra, valid = value.(map[string]any)
		if !valid {
			return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
	}
	result.PoolMode, valid = importBoolean(credentials, "pool_mode")
	if !valid {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	for _, entry := range []struct {
		key    string
		target *bool
	}{{"upstream_billing_probe_enabled", &result.UpstreamBillingProbeEnabled}, {"upstream_billing_rate_sync_enabled", &result.UpstreamBillingRateSyncEnabled}} {
		*entry.target, valid = importBoolean(extra, entry.key)
		if !valid {
			return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
	}
	if result.Platform == "openai" || result.Platform == "anthropic" {
		result.Passthrough, valid = importBoolean(extra, result.Platform+"_passthrough")
		if !valid {
			return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
	}
	result.ModelMapping = map[string]string{}
	if value, exists := credentials["model_mapping"]; exists {
		mapping, valid := value.(map[string]any)
		if !valid {
			return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		for k, v := range mapping {
			mapped, valid := v.(string)
			if strings.TrimSpace(k) == "" || !valid || strings.TrimSpace(mapped) == "" {
				return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			result.ModelMapping[k] = mapped
		}
	}
	groups, err := readImportGroups(data)
	if err != nil {
		return result, err
	}
	result.Groups = groups
	return result, nil
}

func readImportGroups(data map[string]any) ([]Sub2APIImportGroup, error) {
	ids, names := map[string]bool{}, map[string]string{}
	groupIDs, hasIDs := data["group_ids"]
	if hasIDs {
		items, valid := groupIDs.([]any)
		if !valid {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		for _, item := range items {
			id, valid := strictSub2APIInventoryID(item)
			if !valid {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			ids[id] = true
		}
	}
	if value, exists := data["groups"]; exists {
		items, valid := value.([]any)
		if !valid {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		groupSet := map[string]bool{}
		for _, item := range items {
			record, valid := item.(map[string]any)
			if !valid {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			id, valid := strictSub2APIInventoryID(record["id"])
			if !valid || groupSet[id] {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			groupSet[id] = true
			name, valid := record["name"].(string)
			if !valid || strings.TrimSpace(name) == "" {
				name = "名称不可读"
			}
			names[id] = name
		}
		if hasIDs {
			if len(ids) != len(groupSet) {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			for id := range groupSet {
				if !ids[id] {
					return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
				}
			}
		} else {
			ids = groupSet
		}
	} else if !hasIDs {
		return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	result := make([]Sub2APIImportGroup, 0, len(ids))
	for id := range ids {
		name := names[id]
		if name == "" {
			name = "名称不可读"
		}
		result = append(result, Sub2APIImportGroup{ID: id, Name: name})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
