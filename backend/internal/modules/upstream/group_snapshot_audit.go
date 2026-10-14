package upstream

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

func rejectedGroupAudit(issue string) *GroupSnapshotAudit { return &GroupSnapshotAudit{Issue: issue} }
func snapshotBusinessOK(record map[string]any) error {
	if code, exists := record["code"]; exists {
		value, ok := strictInventoryNonnegative(code)
		if !ok || value != 0 {
			return errors.New("分组接口业务结果code不是整数0")
		}
	}
	if value, exists := record["success"]; exists {
		success, ok := value.(bool)
		if !ok || !success {
			return errors.New("分组接口业务结果success不是true")
		}
	}
	return nil
}
func auditFixedNumber(value any) (*float64, error) {
	n := readNumber(value)
	if n == nil || math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0 {
		return nil, errors.New("固定倍率缺失或不是有效的非负数值")
	}
	return n, nil
}
func auditGroupName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return errors.New("分组名称为空或包含首尾空格")
	}
	return nil
}
func auditGroupID(record map[string]any) (string, error) {
	id := ""
	present := false
	for _, key := range []string{"id", "group_id", "groupId"} {
		value, exists := record[key]
		if !exists {
			continue
		}
		parsed, ok := strictSub2APIInventoryID(value)
		if !ok {
			return "", errors.New("分组编号不是规范的正整数")
		}
		if present && id != parsed {
			return "", errors.New("同一分组的编号字段互相矛盾")
		}
		present = true
		id = parsed
	}
	return id, nil
}
func auditAliases(record map[string]any, keys []string) (*float64, error) {
	var ratio *float64
	for _, key := range keys {
		value, exists := record[key]
		if !exists {
			continue
		}
		parsed, err := auditFixedNumber(value)
		if err != nil {
			return nil, err
		}
		if ratio != nil && *ratio != *parsed {
			return nil, errors.New("同一分组的倍率字段互相矛盾")
		}
		ratio = parsed
	}
	return ratio, nil
}
func strictAvailableItems(payload any) ([]any, error) {
	record, ok := payload.(map[string]any)
	if !ok {
		return nil, errors.New("可用分组返回不是对象")
	}
	if err := snapshotBusinessOK(record); err != nil {
		return nil, err
	}
	candidates := [][]any{}
	add := func(value any) error {
		items, ok := value.([]any)
		if !ok {
			return errors.New("可用分组列表不是明确数组")
		}
		candidates = append(candidates, items)
		return nil
	}
	if data, exists := record["data"]; exists {
		switch v := data.(type) {
		case []any:
			candidates = append(candidates, v)
		case map[string]any:
			if err := snapshotBusinessOK(v); err != nil {
				return nil, err
			}
			for _, key := range []string{"items", "list", "records", "groups"} {
				if value, exists := v[key]; exists {
					if err := add(value); err != nil {
						return nil, err
					}
				}
			}
		default:
			return nil, errors.New("可用分组data字段不是列表或列表对象")
		}
	}
	if value, exists := record["items"]; exists {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("可用分组没有可识别的列表字段")
	}
	for _, items := range candidates[1:] {
		if !reflect.DeepEqual(items, candidates[0]) {
			return nil, errors.New("可用分组多个列表字段互相矛盾")
		}
	}
	items := candidates[0]
	wrappers := []map[string]any{record}
	if data, ok := record["data"].(map[string]any); ok {
		wrappers = append(wrappers, data)
	}
	for index := 0; index < len(wrappers); index++ {
		wrapper := wrappers[index]
		if page, exists := wrapper["pagination"]; exists {
			v, ok := page.(map[string]any)
			if !ok {
				return nil, errors.New("可用分组分页资料无法确认")
			}
			wrappers = append(wrappers, v)
		}
		if value, exists := wrapper["total"]; exists {
			total, ok := strictInventoryNonnegative(value)
			if !ok || total != len(items) {
				return nil, errors.New("可用分组总数与实际列表不一致")
			}
		}
		if value, exists := wrapper["has_more"]; exists {
			more, ok := value.(bool)
			if !ok || more {
				return nil, errors.New("可用分组仍有后续页或后续页信息无法确认")
			}
		}
		for _, key := range []string{"page", "total_pages"} {
			if value, exists := wrapper[key]; exists {
				number, ok := strictInventoryNonnegative(value)
				if !ok || number > 1 {
					return nil, errors.New("可用分组不是完整单页目录")
				}
			}
		}
		for _, key := range []string{"next", "next_cursor", "cursor"} {
			if value, exists := wrapper[key]; exists && value != nil && value != "" {
				return nil, errors.New("可用分组后续目录无法确认")
			}
		}
	}
	return items, nil
}
func auditedRateOverrides(payload any) (map[string]float64, error) {
	data := payload
	if record, ok := payload.(map[string]any); ok {
		if err := snapshotBusinessOK(record); err != nil {
			return nil, err
		}
		if value, exists := record["data"]; exists {
			data = value
		}
	}
	result := map[string]float64{}
	switch v := data.(type) {
	case map[string]any:
		for id, value := range v {
			parsed, ok := strictSub2APIInventoryID(id)
			if !ok {
				return nil, errors.New("专属倍率的分组编号无效")
			}
			rate, err := auditFixedNumber(value)
			if err != nil {
				return nil, err
			}
			result[parsed] = *rate
		}
	case []any:
		for _, item := range v {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("专属倍率条目不是对象")
			}
			id, err := auditGroupID(record)
			if err != nil || id == "" {
				return nil, errors.New("专属倍率条目的分组编号缺失或无效")
			}
			if _, exists := result[id]; exists {
				return nil, errors.New("专属倍率分组编号重复")
			}
			rate, err := auditAliases(record, []string{"rate_multiplier", "rateMultiplier", "multiplier", "rate"})
			if err != nil || rate == nil {
				return nil, errors.New("专属倍率条目的固定倍率缺失或无效")
			}
			result[id] = *rate
		}
	default:
		return nil, errors.New("专属倍率返回不是编号对象或条目数组")
	}
	return result, nil
}
func auditSub2APIGroups(available, rates any, ratesErr error, normalized []GroupInfo, options ...requestOptions) (audit *GroupSnapshotAudit) {
	defer func() { audit = withGroupAuditBusinessHint(audit, options, available, rates) }()
	items, err := strictAvailableItems(available)
	if err != nil {
		return rejectedGroupAudit("读取可用分组失败：" + err.Error())
	}
	overrides := map[string]float64{}
	if ratesErr != nil {
		var detail *RequestError
		if errors.As(ratesErr, &detail) && ((detail.StatusCode >= 200 && detail.StatusCode < 300) || detail.MessageKey == ErrorInvalidResponse) {
			return rejectedGroupAudit("读取专属倍率失败：" + costFailureCause(ratesErr))
		}
	}
	if ratesErr == nil {
		overrides, err = auditedRateOverrides(rates)
		if err != nil {
			return rejectedGroupAudit("读取专属倍率失败：" + err.Error())
		}
	}
	expected := make([]GroupInfo, 0, len(items))
	ids, names := map[string]bool{}, map[string]int{}
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return rejectedGroupAudit("读取可用分组失败：列表条目不是对象")
		}
		name, ok := record["name"].(string)
		if !ok || auditGroupName(name) != nil {
			return rejectedGroupAudit("读取可用分组失败：分组名称缺失或不规范")
		}
		id, idErr := auditGroupID(record)
		if idErr != nil {
			return rejectedGroupAudit("读取可用分组失败：" + idErr.Error())
		}
		if id != "" && ids[id] {
			return rejectedGroupAudit("读取可用分组失败：分组编号重复")
		}
		ids[id] = true
		names[name]++
		rate, rateErr := auditFixedNumber(record["rate_multiplier"])
		if rateErr != nil {
			return rejectedGroupAudit("读取可用分组失败：" + rateErr.Error())
		}
		if dedicated, exists := overrides[id]; exists && id != "" {
			value := dedicated
			rate = &value
		}
		expected = append(expected, GroupInfo{ID: id, Name: name, Multiplier: rate})
	}
	for _, group := range expected {
		if group.ID == "" && names[group.Name] != 1 {
			return rejectedGroupAudit("读取可用分组失败：缺少编号的同名分组无法唯一对应")
		}
	}
	if !sameAuditedGroups(expected, normalized) {
		return rejectedGroupAudit("分组转换核对失败：原始分组身份或最终倍率未被完整保留")
	}
	return &GroupSnapshotAudit{AllowUpdate: true, AllowCleanup: true}
}
func sameAuditedGroups(expected, actual []GroupInfo) bool {
	a, b := canonicalGroups(expected), canonicalGroups(actual)
	// Classification does not determine whether a multiplier directory is complete.
	for i := range a {
		a[i].Platform = ""
	}
	for i := range b {
		b[i].Platform = ""
	}
	return reflect.DeepEqual(a, b)
}
func hasSnapshotPagination(value any) bool {
	record, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, item := range record {
		switch key {
		case "total", "has_more", "page", "page_size", "total_pages", "next", "next_cursor", "cursor", "pagination":
			return true
		}
		// Only inspect objects: model arrays are not pagination envelopes.
		if _, ok := item.(map[string]any); ok && hasSnapshotPagination(item) {
			return true
		}
	}
	return false
}
func groupEnvelopeField(key string) bool {
	switch key {
	case "code", "success", "message", "msg", "total", "has_more", "page", "page_size", "total_pages", "next", "next_cursor", "cursor", "pagination":
		return true
	}
	return false
}
func parseNewAPIGroupDirectory(payload any) (map[string]*float64, bool, error) {
	record, ok := payload.(map[string]any)
	if !ok {
		return nil, false, errors.New("组目录返回不是对象")
	}
	if err := snapshotBusinessOK(record); err != nil {
		return nil, false, err
	}
	source := record
	complete := false
	if data, exists := record["data"]; exists {
		switch value := data.(type) {
		case map[string]any:
			source = value
			complete = true
		case []any:
			if len(value) != 0 {
				return nil, false, errors.New("组目录的非空数组没有可确认的分组身份")
			}
			return map[string]*float64{}, true, nil
		default:
			return nil, false, errors.New("组目录data不是对象或明确空数组")
		}
	}
	result := map[string]*float64{}
	for name, item := range source {
		if !complete && groupEnvelopeField(name) {
			continue
		}
		if complete && groupPaginationField(name) {
			// Directory wrappers may carry model/page metadata. It only removes
			// cleanup permission; an actual ratio object still remains a group.
			object, isObject := item.(map[string]any)
			hasRatio := false
			for _, key := range []string{"ratio", "rate", "multiplier"} {
				if _, exists := object[key]; exists {
					hasRatio = true
				}
			}
			if !isObject || !hasRatio {
				continue
			}
		}
		if err := auditGroupName(name); err != nil {
			return nil, false, err
		}
		var rate *float64
		var err error
		if object, ok := item.(map[string]any); ok {
			rate, err = auditAliases(object, []string{"ratio", "rate", "multiplier"})
		} else {
			rate, err = auditFixedNumber(item)
		}
		if err != nil {
			return nil, false, fmt.Errorf("组目录包含无效倍率：%w", err)
		}
		if rate == nil && !strings.EqualFold(name, "auto") {
			return nil, false, errors.New("组目录固定倍率字段缺失")
		}
		result[name] = rate
	}
	if !complete && len(result) == 0 {
		return nil, false, errors.New("旧版组返回没有明确目录，不能作为有效空目录")
	}
	return result, complete, nil
}

type newAPIPricingDirectory struct {
	ratios   map[string]*float64
	usable   map[string]string
	complete bool
}

func readPricingDirectories(record map[string]any) (newAPIPricingDirectory, error) {
	result := newAPIPricingDirectory{ratios: map[string]*float64{}, usable: map[string]string{}}
	ratiosValue, hasRatios := record["group_ratio"]
	usableValue, hasUsable := record["usable_group"]
	if hasRatios {
		ratios, ok := ratiosValue.(map[string]any)
		if !ok {
			return result, errors.New("价格group_ratio不是对象")
		}
		for name, value := range ratios {
			if err := auditGroupName(name); err != nil {
				return result, err
			}
			rate, err := auditFixedNumber(value)
			if err != nil {
				return result, err
			}
			result.ratios[name] = rate
		}
	}
	if hasUsable {
		usable, ok := usableValue.(map[string]any)
		if !ok {
			return result, errors.New("价格usable_group不是对象")
		}
		for name, value := range usable {
			if err := auditGroupName(name); err != nil {
				return result, err
			}
			description, ok := value.(string)
			if !ok {
				return result, errors.New("价格可用组说明不是字符串")
			}
			result.usable[name] = description
		}
	}
	result.complete = hasRatios && hasUsable
	return result, nil
}
func parseNewAPIPricingDirectory(payload any) (newAPIPricingDirectory, error) {
	record, ok := payload.(map[string]any)
	if !ok {
		return newAPIPricingDirectory{}, errors.New("价格返回不是对象")
	}
	if err := snapshotBusinessOK(record); err != nil {
		return newAPIPricingDirectory{}, err
	}
	top, err := readPricingDirectories(record)
	if err != nil {
		return top, err
	}
	data, hasData := record["data"]
	if nested, ok := data.(map[string]any); ok {
		if err := snapshotBusinessOK(nested); err != nil {
			return top, err
		}
		inner, err := readPricingDirectories(nested)
		if err != nil {
			return inner, err
		}
		if _, exists := record["group_ratio"]; exists {
			if _, innerExists := nested["group_ratio"]; !innerExists || !reflect.DeepEqual(top.ratios, inner.ratios) {
				return inner, errors.New("价格内外group_ratio目录互相矛盾")
			}
		}
		if _, exists := record["usable_group"]; exists {
			if _, innerExists := nested["usable_group"]; !innerExists || !reflect.DeepEqual(top.usable, inner.usable) {
				return inner, errors.New("价格内外usable_group目录互相矛盾")
			}
		}
		if _, a := nested["group_ratio"]; !a {
			if _, b := nested["usable_group"]; !b {
				return inner, errors.New("价格data对象没有可识别的分组目录")
			}
		}
		return inner, nil
	}
	if hasData {
		models, ok := data.([]any)
		if !ok {
			return top, errors.New("价格data字段不是模型数组或目录对象")
		}
		for _, model := range models {
			if _, ok := model.(map[string]any); !ok {
				return top, errors.New("价格模型数组条目不是对象")
			}
		}
		return top, nil
	}
	if _, a := record["group_ratio"]; !a {
		if _, b := record["usable_group"]; !b {
			return top, errors.New("价格返回没有可识别的目录或模型数组")
		}
	}
	return top, nil
}
func auditNewAPIGroups(groupsPayload, pricingPayload any, groupsErr, pricingErr error, normalized []GroupInfo, options ...requestOptions) (audit *GroupSnapshotAudit) {
	defer func() { audit = withGroupAuditBusinessHint(audit, options, groupsPayload, pricingPayload) }()
	if groupsErr != nil {
		return rejectedGroupAudit("读取组目录失败：" + costFailureCause(groupsErr))
	}
	if pricingErr != nil {
		return rejectedGroupAudit("读取价格目录失败：" + costFailureCause(pricingErr))
	}
	base, groupsComplete, err := parseNewAPIGroupDirectory(groupsPayload)
	if err != nil {
		return rejectedGroupAudit("核查组目录失败：" + err.Error())
	}
	pricing, err := parseNewAPIPricingDirectory(pricingPayload)
	if err != nil {
		return rejectedGroupAudit("核查价格目录失败：" + err.Error())
	}
	for name, rate := range pricing.ratios {
		base[name] = rate
	}
	for name := range pricing.usable {
		if _, exists := base[name]; !exists {
			if !strings.EqualFold(name, "auto") {
				return rejectedGroupAudit("核查价格目录失败：可用固定分组缺少倍率")
			}
			base[name] = nil
		}
	}
	expected := make([]GroupInfo, 0, len(base))
	names := make([]string, 0, len(base))
	for name := range base {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := "fixed"
		if base[name] == nil {
			mode = "auto"
		}
		expected = append(expected, GroupInfo{ID: name, Name: name, Multiplier: base[name], MultiplierMode: mode})
	}
	if !sameAuditedGroups(expected, normalized) {
		return rejectedGroupAudit("分组转换核对失败：原始分组身份或最终倍率未被完整保留")
	}
	cleanup := groupsComplete && pricing.complete && !hasSnapshotPagination(groupsPayload) && !hasSnapshotPagination(pricingPayload)
	return &GroupSnapshotAudit{AllowUpdate: true, AllowCleanup: cleanup, Issue: func() string {
		if !cleanup {
			return "价格分组目录尚未完整确认，本轮未清理旧倍率"
		}
		return ""
	}()}
}

func groupPaginationField(key string) bool {
	switch key {
	case "total", "has_more", "page", "page_size", "total_pages", "next", "next_cursor", "cursor", "pagination":
		return true
	}
	return false
}
func withGroupAuditBusinessHint(audit *GroupSnapshotAudit, options []requestOptions, payloads ...any) *GroupSnapshotAudit {
	if audit == nil || audit.AllowUpdate {
		return audit
	}
	var auth requestOptions
	if len(options) > 0 {
		auth = options[0]
	}
	for _, payload := range payloads {
		record, ok := payload.(map[string]any)
		if !ok || snapshotBusinessOK(record) == nil {
			continue
		}
		for _, field := range []string{"message", "msg"} {
			if message, ok := record[field].(string); ok {
				if safe := safeRequestMessage(message, auth); safe != "" {
					audit.Issue += "；上游说明：" + safe
					break
				}
			}
		}
	}
	return audit
}
