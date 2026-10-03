package upstream

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// IDs must be checked before the permissive display normalizers can truncate
// numbers. JSON numbers beyond the exact integer range cannot prove identity.
func strictSub2APIInventoryID(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		return v, err == nil && n > 0 && strconv.FormatUint(n, 10) == v
	case float64:
		if v <= 0 || v > 9007199254740991 || math.Trunc(v) != v {
			return "", false
		}
		return strconv.FormatFloat(v, 'f', 0, 64), true
	case json.Number:
		n, err := strconv.ParseUint(string(v), 10, 64)
		return string(v), err == nil && n > 0 && n <= 9007199254740991 && strconv.FormatUint(n, 10) == string(v)
	case int:
		if v > 0 {
			return strconv.Itoa(v), true
		}
	case int64:
		if v > 0 && v <= 9007199254740991 {
			return strconv.FormatInt(v, 10), true
		}
	}
	return "", false
}

func strictInventoryNonnegative(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v >= 0 && v <= 9007199254740991 && math.Trunc(v) == v {
			return int(v), true
		}
	case int:
		return v, v >= 0
	case json.Number:
		n, err := strconv.ParseUint(string(v), 10, 53)
		return int(n), err == nil
	}
	return 0, false
}

type inventoryPageMetadata struct {
	total         int
	hasTotal      bool
	more          bool
	hasMore       bool
	hasPagination bool
	pageSize      int
}

func sub2APIInventoryMetadata(value any, page, pageSize int) (inventoryPageMetadata, bool) {
	meta := inventoryPageMetadata{}
	root, ok := value.(map[string]any)
	if !ok {
		_, array := value.([]any)
		return meta, array
	}
	scopes := []map[string]any{root}
	if data, ok := root["data"].(map[string]any); ok {
		scopes = append(scopes, data)
	}
	for _, scope := range append([]map[string]any{}, scopes...) {
		for _, key := range []string{"pagination", "meta"} {
			if raw, exists := scope[key]; exists {
				nested, ok := raw.(map[string]any)
				if !ok {
					return meta, false
				}
				scopes = append(scopes, nested)
				meta.hasPagination = true
			}
		}
	}
	setMore := func(more bool) bool {
		if meta.hasMore && meta.more != more {
			return false
		}
		meta.more, meta.hasMore = more, true
		return true
	}
	for _, scope := range scopes {
		for key := range scope {
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "next") && key != "next" && key != "next_page" && key != "nextPage" && key != "next_page_url" {
				return meta, false
			}
		}
		for _, key := range []string{"total", "count"} {
			if raw, exists := scope[key]; exists {
				n, ok := strictInventoryNonnegative(raw)
				if !ok || (meta.hasTotal && meta.total != n) {
					return meta, false
				}
				meta.total, meta.hasTotal, meta.hasPagination = n, true, true
			}
		}
		for _, key := range []string{"page", "current_page", "currentPage"} {
			if raw, exists := scope[key]; exists {
				n, ok := strictInventoryNonnegative(raw)
				if !ok || n != page {
					return meta, false
				}
				meta.hasPagination = true
			}
		}
		for _, key := range []string{"page_size", "pageSize", "per_page", "perPage"} {
			if raw, exists := scope[key]; exists {
				n, ok := strictInventoryNonnegative(raw)
				if !ok || n <= 0 || (pageSize > 0 && n != pageSize) || (meta.pageSize > 0 && meta.pageSize != n) {
					return meta, false
				}
				meta.hasPagination = true
				meta.pageSize = n
			}
		}
		for _, key := range []string{"has_more", "hasMore", "has_next", "hasNext"} {
			if raw, exists := scope[key]; exists {
				more, ok := raw.(bool)
				if !ok || !setMore(more) {
					return meta, false
				}
				meta.hasPagination = true
			}
		}
		for _, key := range []string{"next", "next_page", "nextPage", "next_page_url"} {
			if raw, exists := scope[key]; exists {
				more := false
				if raw != nil && raw != "" {
					n, ok := strictInventoryNonnegative(raw)
					if !ok || (n != 0 && n != page+1) {
						return meta, false
					}
					more = n != 0
				}
				if !setMore(more) {
					return meta, false
				}
				meta.hasPagination = true
			}
		}
		for _, key := range []string{"pages", "total_pages", "totalPages", "last_page", "lastPage"} {
			if raw, exists := scope[key]; exists {
				n, ok := strictInventoryNonnegative(raw)
				if !ok || (n < page && !(n == 0 && page == 1 && meta.hasTotal && meta.total == 0)) || !setMore(n > page) {
					return meta, false
				}
				meta.hasPagination = true
			}
		}
	}
	return meta, true
}

func sub2APIGroupPageItems(value any) ([]any, bool, bool) {
	var items []any
	arrayContract := false
	if raw, ok := value.([]any); ok {
		items = raw
		arrayContract = true
	} else {
		var ok bool
		items, ok = sub2APIAccountPageItems(value)
		if !ok {
			return nil, false, false
		}
		if root, ok := value.(map[string]any); ok {
			_, arrayContract = root["data"].([]any)
		}
	}
	return items, arrayContract, true
}

func strictSub2APIGroupItems(value any) ([]map[string]any, bool) {
	items, arrayContract, validItems := sub2APIGroupPageItems(value)
	if !validItems {
		return nil, false
	}
	meta, ok := sub2APIInventoryMetadata(value, 1, 0)
	if !ok || (meta.hasTotal && meta.total != len(items)) || (meta.hasMore && meta.more) || (meta.pageSize > 0 && len(items) > meta.pageSize) {
		return nil, false
	}
	if !meta.hasTotal && !meta.hasMore && (!arrayContract || meta.hasPagination) {
		return nil, false
	}
	seen := make(map[string]bool)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		id, ok := strictSub2APIInventoryID(record["id"])
		if !ok || seen[id] {
			return nil, false
		}
		seen[id] = true
		result = append(result, record)
	}
	return result, true
}

// Verify the terminal proof against the cumulative count, considering every
// compatible metadata field together instead of choosing a convenient one.
func sub2APIInventoryPageFinished(value any, page, pageSize, count, cumulative int) (bool, bool) {
	meta, ok := sub2APIInventoryMetadata(value, page, pageSize)
	if !ok || count > pageSize {
		return false, false
	}
	if meta.hasTotal {
		if cumulative > meta.total {
			return false, false
		}
		finished := cumulative == meta.total
		if meta.hasMore && meta.more == finished {
			return false, false
		}
		if !finished && count < pageSize {
			return false, false
		}
		return finished, true
	}
	if meta.hasMore {
		if meta.more && count < pageSize {
			return false, false
		}
		return !meta.more, true
	}
	// The accounts endpoint's established array envelope uses short-page EOF;
	// object pagination envelopes without a total/terminal flag are ambiguous.
	root, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	_, array := root["data"].([]any)
	if !array || meta.hasPagination {
		return false, false
	}
	return count < pageSize, true
}
