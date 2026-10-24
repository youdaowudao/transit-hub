package upstream

import (
	"fmt"
	"math"
	"strconv"
)

// Only risk polling opts in. Legacy query pages retain their tolerant parser.
func validateSignupRiskUsersPage(payload any, query Sub2APIAdminUsersQuery) error {
	invalid := func(format string, args ...any) error {
		err := newRequestError(ErrorInvalidResponse, PlatformSub2API)
		// Describe fields and counts only; never echo a raw response value.
		err.UpstreamMessage = fmt.Sprintf(format, args...)
		return err
	}
	outer, ok := payload.(map[string]any)
	if !ok {
		return invalid("用户列表响应不是JSON对象")
	}
	data, ok := outer["data"].(map[string]any)
	if !ok {
		return invalid("用户列表响应缺少完整的数据对象")
	}
	items, ok := data["items"].([]any)
	if !ok {
		return invalid("用户列表条目不是数组或已经缺失")
	}
	if len(items) > query.PageSize {
		return invalid("本页最多可返回%d条用户，实际返回%d条", query.PageSize, len(items))
	}
	number := func(key, label string) (int, error) {
		if _, present := data[key]; !present {
			return 0, invalid("分页缺少%s", label)
		}
		n, ok := data[key].(float64)
		if !ok {
			return 0, invalid("分页的%s不是数字", label)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 9007199254740991 || n != math.Trunc(n) {
			return 0, invalid("分页的%s不是可完整读取的非负整数", label)
		}
		return int(n), nil
	}
	page, err := number("page", "页号")
	if err != nil {
		return err
	}
	size, err := number("page_size", "每页数量")
	if err != nil {
		return err
	}
	total, err := number("total", "用户总数")
	if err != nil {
		return err
	}
	pages, err := number("pages", "总页数")
	if err != nil {
		return err
	}
	if page != query.Page {
		return invalid("本次请求第%d页，站点实际返回第%d页", query.Page, page)
	}
	if size != query.PageSize {
		return invalid("本次每页请求%d条用户，站点的每页数量实际为%d", query.PageSize, size)
	}
	if pages < 1 {
		return invalid("分页的总页数必须至少为1，实际为%d", pages)
	}
	if total < len(items) {
		return invalid("用户总数为%d，小于本页实际返回的%d条用户", total, len(items))
	}
	expectedPages := max(1, (total+size-1)/size)
	expectedItems := min(size, max(0, total-(page-1)*size))
	if pages != expectedPages {
		return invalid("按用户总数应有%d页，站点实际返回的总页数为%d", expectedPages, pages)
	}
	if len(items) != expectedItems {
		return invalid("第%d页应返回%d条用户，实际返回%d条", page, expectedItems, len(items))
	}
	for index, value := range items {
		record, ok := value.(map[string]any)
		if !ok {
			return invalid("第%d条用户不是完整的用户对象", index+1)
		}
		if _, present := record["id"]; !present {
			return invalid("第%d条用户缺少编号", index+1)
		}
		var id string
		switch v := record["id"].(type) {
		case string:
			id = v
		case float64:
			if v <= 0 || v > 9007199254740991 || v != math.Trunc(v) {
				return invalid("第%d条用户的编号不是可完整读取的正整数", index+1)
			}
			id = strconv.FormatInt(int64(v), 10)
		default:
			return invalid("第%d条用户的编号不是完整的正整数", index+1)
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
			return invalid("第%d条用户的编号不是完整的正整数", index+1)
		}
	}
	return nil
}
