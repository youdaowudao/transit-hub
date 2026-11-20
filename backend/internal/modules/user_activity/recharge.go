package user_activity

import (
	"context"
	"errors"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type redeemStream struct {
	rows                  []upstream.Sub2APIRedeemRecord
	seen                  map[string]bool
	page, requests, total int
	known, done           bool
	last                  *time.Time
}
type rechargeCollector struct {
	s                 *Service
	p                 *pacedReader
	session           upstream.Session
	streams           map[string]*redeemStream
	affiliateRequests int
}

func newRechargeCollector(s *Service, p *pacedReader, session upstream.Session) *rechargeCollector {
	return &rechargeCollector{s: s, p: p, session: session, streams: map[string]*redeemStream{}}
}
func (c *rechargeCollector) redeems(ctx context.Context, kind, from, to string) ([]Recharge, error) {
	stream := c.streams[kind]
	if stream == nil {
		stream = &redeemStream{seen: map[string]bool{}, page: 1}
		c.streams[kind] = stream
	}
	start, _, _ := businesstime.Bounds(from)
	for !stream.done && (stream.last == nil || !stream.last.Before(start)) {
		if stream.requests >= 400 {
			return nil, fail(kind + "入账读取已达到本轮400次上限，不能确认窗口完整")
		}
		page := stream.page
		stream.requests++
		result, e := call(ctx, c.p, func(r context.Context) (upstream.Sub2APIRedeemRecordsPage, error) {
			return c.s.platform.FetchSub2APIUsedRedeemRecordsContext(r, c.session, kind, page, 500)
		})
		if e != nil {
			return nil, e
		}
		if result.TotalKnown {
			if stream.known && stream.total != result.Total {
				return nil, fail(kind + "入账总数在分页期间变化")
			}
			stream.total = result.Total
			stream.known = true
		}
		if len(result.Items) == 0 {
			if stream.known && len(stream.rows) != stream.total {
				return nil, fail(kind + "入账在总数收齐前返回空页")
			}
			stream.done = true
			break
		}
		for _, r := range result.Items {
			if r.UsedAt == nil || stream.seen[r.ID] {
				return nil, fail(kind + "入账包含缺失时间或重复编号")
			}
			if stream.last != nil && r.UsedAt.After(*stream.last) {
				return nil, fail(kind + "入账跨页使用时间不是降序")
			}
			stream.seen[r.ID] = true
			stream.last = r.UsedAt
			stream.rows = append(stream.rows, r)
		}
		if stream.known {
			if len(stream.rows) > stream.total {
				return nil, fail(kind + "入账条数超过总数")
			}
			if len(stream.rows) == stream.total {
				stream.done = true
			}
		}
		stream.page++
	}
	out := []Recharge{}
	for _, r := range stream.rows {
		day := dayAt(*r.UsedAt)
		if day < from || day >= to {
			continue
		}
		channel := "redeem"
		if kind == "admin_balance" {
			channel = "admin_add"
			if r.Value < 0 {
				channel = "admin_deduct"
			}
		} else if strings.HasPrefix(r.Code, "PAY-") {
			channel = "payment"
		}
		if kind == "balance" && r.Value < 0 {
			return nil, fail("普通充值或兑换记录出现负数金额，未替换窗口")
		}
		out = append(out, Recharge{r.UsedBy, day, channel, r.Value, 1})
	}
	return out, nil
}
func (c *rechargeCollector) affiliates(ctx context.Context, from, to string) ([]Recharge, error) {
	seen := map[string]bool{}
	out := []Recharge{}
	total := -1
	count := 0
	var last *time.Time
	start, _, _ := businesstime.Bounds(from)
	for page := 1; ; page++ {
		if c.affiliateRequests >= 200 {
			return nil, fail("返利入账已达到本轮200次上限，不能确认窗口完整")
		}
		c.affiliateRequests++
		result, e := call(ctx, c.p, func(r context.Context) (upstream.Sub2APIAffiliateTransfersPage, error) {
			return c.s.platform.FetchSub2APIAffiliateTransfersContext(r, c.session, from, shift(to, -1), page, 100)
		})
		if e != nil {
			return nil, e
		}
		if result.TotalKnown {
			if total >= 0 && total != result.Total {
				return nil, fail("返利总数在分页期间变化")
			}
			total = result.Total
		}
		if len(result.Items) == 0 {
			if total >= 0 && count != total {
				return nil, fail("返利记录在总数收齐前返回空页")
			}
			return out, nil
		}
		for _, r := range result.Items {
			if r.CreatedAt == nil || seen[r.ID] {
				return nil, fail("返利记录缺失时间或编号重复")
			}
			if last != nil && r.CreatedAt.After(*last) {
				return nil, fail("返利时间跨页不是降序")
			}
			seen[r.ID] = true
			last = r.CreatedAt
			count++
			day := dayAt(*r.CreatedAt)
			if r.Action == "transfer" && day >= from && day < to {
				out = append(out, Recharge{r.UserID, day, "affiliate", r.Amount, 1})
			}
		}
		if total >= 0 {
			if count > total {
				return nil, fail("返利记录条数超过总数")
			}
			if count == total {
				return out, nil
			}
		}
		if last != nil && last.Before(start) {
			return out, nil
		}
	}
}
func (c *rechargeCollector) window(ctx context.Context, from, to string) ([]Recharge, []string, bool, string, error) {
	rows := []Recharge{}
	channels := []string{}
	all := true
	warning := ""
	var failures []error
	for _, kind := range []string{"balance", "admin_balance"} {
		part, e := c.redeems(ctx, kind, from, to)
		if e != nil {
			all = false
			failures = append(failures, fail(kind+"："+safeReason(e)))
			continue
		}
		rows = append(rows, part...)
		if kind == "balance" {
			channels = append(channels, "payment", "redeem")
		} else {
			channels = append(channels, "admin_add", "admin_deduct")
		}
	}
	affiliates, e := c.affiliates(ctx, from, to)
	if e != nil {
		all = false
		var requestErr *upstream.RequestError
		if errors.As(e, &requestErr) && requestErr.StatusCode == 404 {
			warning = "当前主站未提供返利转入记录，入账仅显示已读取的部分，完整期间比较与首次入账判断暂不可用。"
		} else {
			failures = append(failures, fail("返利："+safeReason(e)))
		}
	} else {
		channels = append(channels, "affiliate")
		rows = append(rows, affiliates...)
	}
	// Aggregate one row per user/day/channel before publishing a window.
	merged := map[string]Recharge{}
	for _, row := range rows {
		key := row.SiteUserID + "/" + row.Day + "/" + row.Channel
		old := merged[key]
		old.SiteUserID, old.Day, old.Channel = row.SiteUserID, row.Day, row.Channel
		old.Amount += row.Amount
		old.Events += row.Events
		merged[key] = old
	}
	rows = rows[:0]
	for _, row := range merged {
		rows = append(rows, row)
	}
	return rows, channels, all, warning, errors.Join(failures...)
}
