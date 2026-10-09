package upstream

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Sub2APIModelControlAccount retains only model-control metadata, never credentials.
type Sub2APIModelControlAccount struct{ AdminGroupAccountInfo }

var ErrSub2APIModelControlAccountMissing = errors.New("Sub2API model-control account missing")

func parseSub2APIModelMapping(record map[string]any) (map[string]string, bool) {
	credentials, ok := record["credentials"].(map[string]any)
	if !ok {
		return nil, false
	}
	mapping := map[string]string{}
	raw := credentials["model_mapping"]
	if raw == nil {
		return mapping, true
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	for key, value := range values {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(text) == "" {
			return nil, false
		}
		mapping[key] = text
	}
	return mapping, true
}
func parseSub2APIOpenAIPassthrough(record map[string]any) bool {
	extra, _ := record["extra"].(map[string]any)
	if value, ok := extra["openai_passthrough"].(bool); ok {
		return value
	}
	value, _ := extra["openai_oauth_passthrough"].(bool)
	return value
}
func (s *PlatformService) ReadSub2APIModelControlAccountContext(ctx context.Context, session Session, accountID string) (Sub2APIModelControlAccount, error) {
	var result Sub2APIModelControlAccount
	if _, valid := strictSub2APIInventoryID(accountID); !valid || session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return result, localMutationError(ErrorAuth)
	}
	response, err := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+"/api/v1/admin/accounts/"+accountID, adminAuthOptions(session))
	if err != nil {
		var request *RequestError
		if errors.As(err, &request) && request.StatusCode == http.StatusNotFound {
			return result, ErrSub2APIModelControlAccountMissing
		}
		return result, err
	}
	data, valid := strictImportSub2APIData(response.Payload)
	if !valid {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	id, valid := strictSub2APIInventoryID(data["id"])
	if !valid || id != accountID {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	for _, key := range []string{"name", "platform", "type", "status"} {
		value, ok := data[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
	}
	if _, ok := data["schedulable"].(bool); !ok {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	account := parseSub2APIAccount(data)
	if !account.ModelMappingKnown {
		return result, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	groups, err := readImportGroups(data)
	if err != nil {
		return result, err
	}
	account.GroupIDs = []string{}
	for _, group := range groups {
		account.GroupIDs = append(account.GroupIDs, group.ID)
	}
	result.AdminGroupAccountInfo = account
	return result, nil
}
func (s *PlatformService) UpdateSub2APIAdminAccountModelMappingContext(ctx context.Context, session Session, accountID string, mapping map[string]string) error {
	if len(mapping) == 0 {
		return localMutationError(ErrorInvalidFields)
	}
	for key, value := range mapping {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return localMutationError(ErrorInvalidFields)
		}
	}
	return s.bulkUpdateSub2APIAdminAccountContext(ctx, session, accountID, sub2APIAdminAccountBulkUpdate{Credentials: map[string]any{"model_mapping": mapping}})
}
