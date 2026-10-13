package upstream

import (
	"context"
	"fmt"
	"net/url"
)

// ReadSub2APIAdminAccountExistenceContext validates only the success envelope
// and account identity. A 404 remains a request error, never deletion evidence.
func (s *PlatformService) ReadSub2APIAdminAccountExistenceContext(ctx context.Context, session Session, accountID string) (bool, error) {
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return false, newRequestError(ErrorAuth, PlatformSub2API)
	}
	if _, valid := strictSub2APIInventoryID(accountID); !valid {
		return false, newRequestError(ErrorInvalidResponse, PlatformSub2API)
	}
	options := adminAuthOptions(session)
	response, err := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+"/api/v1/admin/accounts/"+url.PathEscape(accountID), options)
	if err != nil {
		return false, err
	}
	data, valid := strictImportSub2APIData(response.Payload)
	if !valid {
		return false, scopedAccountEnvelopeError(response, options, "账号详情没有返回有效的成功结果")
	}
	id, valid := strictSub2APIInventoryID(data["id"])
	if !valid || id != accountID {
		return false, scopedAccountEnvelopeError(response, options, "账号详情返回的ID与本次账号不一致")
	}
	return true, nil
}

func scopedAccountEnvelopeError(response jsonResponse, options requestOptions, reason string) error {
	err := newRequestErrorWithStatus(ErrorInvalidResponse, PlatformSub2API, response.StatusCode)
	if record, ok := response.Payload.(map[string]any); ok {
		if message, ok := record["message"].(string); ok && message != "" {
			reason += "：" + safeRequestMessage(message, options)
		}
	}
	err.UpstreamMessage = reason
	return err
}

// ListSub2APIAdminAccountIDsStrictContext is separate from the shared display
// inventory: deletion requires original IDs and every page's strict code=0.
func (s *PlatformService) ListSub2APIAdminAccountIDsStrictContext(ctx context.Context, session Session) (map[string]struct{}, error) {
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return nil, newRequestError(ErrorAuth, PlatformSub2API)
	}
	const pageSize = 100
	const maxPages = 100
	options := adminAuthOptions(session)
	ids := make(map[string]struct{})
	expectedTotal := -1
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := s.httpClient.requestJSONWithContext(ctx, fmt.Sprintf("%s/api/v1/admin/accounts?page=%d&page_size=%d", session.BaseURL, page, pageSize), options)
		if err != nil {
			return nil, err
		}
		root, valid := response.Payload.(map[string]any)
		if !valid {
			return nil, scopedAccountEnvelopeError(response, options, "完整账号列表返回格式不正确")
		}
		code, valid := strictInventoryNonnegative(root["code"])
		if !valid || code != 0 {
			return nil, scopedAccountEnvelopeError(response, options, "完整账号列表未返回成功结果")
		}
		if value, exists := root["success"]; exists {
			success, valid := value.(bool)
			if !valid || !success {
				return nil, scopedAccountEnvelopeError(response, options, "完整账号列表未返回成功结果")
			}
		}
		items, valid := sub2APIAccountPageItems(response.Payload)
		total, hasTotal, validTotal := sub2APIAccountPaginationTotal(response.Payload)
		metadata, validMetadata := sub2APIInventoryMetadata(response.Payload, page, pageSize)
		if !valid || !hasTotal || !validTotal || !validMetadata || !metadata.hasTotal || metadata.total != total || total > pageSize*maxPages || len(items) > pageSize {
			return nil, scopedAccountEnvelopeError(response, options, "完整账号列表的分页资料不完整或超出可确认范围")
		}
		if expectedTotal < 0 {
			expectedTotal = total
		} else if expectedTotal != total {
			return nil, scopedAccountEnvelopeError(response, options, "完整账号列表读取期间总数发生变化")
		}
		for _, item := range items {
			record, valid := item.(map[string]any)
			if !valid {
				return nil, scopedAccountEnvelopeError(response, options, "完整账号列表包含无法确认的账号资料")
			}
			id, valid := strictSub2APIInventoryID(record["id"])
			if !valid {
				return nil, scopedAccountEnvelopeError(response, options, "完整账号列表包含格式不正确的账号ID")
			}
			if _, duplicate := ids[id]; duplicate {
				return nil, scopedAccountEnvelopeError(response, options, "完整账号列表包含重复账号，无法确认删除")
			}
			ids[id] = struct{}{}
		}
		if len(ids) > expectedTotal {
			return nil, scopedAccountEnvelopeError(response, options, "完整账号列表条数超过返回总数")
		}
		finished, validPage := sub2APIInventoryPageFinished(response.Payload, page, pageSize, len(items), len(ids))
		if !validPage {
			return nil, scopedAccountEnvelopeError(response, options, "完整账号列表的页码、条数或继续分页说明互相矛盾，无法确认删除")
		}
		if finished {
			return ids, nil
		}
	}
	return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
}
