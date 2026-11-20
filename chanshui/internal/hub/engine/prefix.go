package engine

import (
	"context"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/hub/prefixcheck"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

// Test cases substitute a counter to exercise cancellation and failures.
var prefixCount prefixcheck.CountFunc = prefixcheck.Count

func prefixSummary(runCtx context.Context, s *store.Store, owner store.Owner, id string) prefixcheck.Analysis {
	if runCtx.Err() != nil {
		return prefixcheck.Unavailable("检测被停止或已达到总时长，未做逐题核对")
	}
	if deadline, ok := runCtx.Deadline(); ok && time.Until(deadline) < 15*time.Second {
		return prefixcheck.Unavailable("检测接近总时长，为保证结果能保存，未做逐题核对")
	}
	ctx, cancel := context.WithTimeout(runCtx, 3*time.Second)
	defer cancel()
	var candidates []prefixcheck.Candidate
	var injections []prefixcheck.InjectionItem
	skipped, cursor := 0, ""
	for {
		page, err := s.ListItems(ctx, owner, id, store.Filters{Cursor: cursor, Limit: 5})
		if err != nil {
			return giveUp(ctx, runCtx, "读取已保存题目失败，未做逐题核对："+err.Error())
		}
		for _, item := range page.Items {
			candidate, injection, skip := prefixcheck.Extract(toStored(item))
			switch {
			case injection != nil:
				injections = append(injections, *injection)
			case candidate != nil:
				candidates = append(candidates, *candidate)
			case skip:
				skipped++
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	result, err := prefixcheck.Analyze(ctx, candidates, injections, skipped, api.IQSystemPrompt, prefixCount)
	if err != nil {
		return giveUp(ctx, runCtx, "分词器加载失败，未做逐题核对："+err.Error())
	}
	result.ComputedAtMs = time.Now().UnixMilli()
	return result
}

func giveUp(ctx, runCtx context.Context, other string) prefixcheck.Analysis {
	switch {
	case runCtx.Err() != nil:
		return prefixcheck.Unavailable("检测被停止，逐题核对已放弃")
	case ctx.Err() != nil:
		return prefixcheck.Unavailable("逐题核对超过 3 秒，已放弃，检测结果照常保存")
	default:
		return prefixcheck.Unavailable(other)
	}
}

func toStored(item store.Item) prefixcheck.StoredItem {
	return prefixcheck.StoredItem{
		ItemID: item.ItemID, OrderIndex: item.OrderIndex, Kind: item.Definition.Kind,
		JudgeType: item.Definition.Judge.Type, Template: item.Definition.Template,
		Prompt: item.Prompt, ImageAvailable: item.ImageAvailable, Result: item.Result,
	}
}
