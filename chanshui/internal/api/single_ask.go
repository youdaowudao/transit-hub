package api

import (
	"context"
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"strings"
)

type SingleAskInput struct {
	Prompt   string                  `json:"prompt"`
	Thinking *gateway.ThinkingConfig `json:"thinking"`
	Effort   string                  `json:"effort,omitempty"`
}

// These are the original ask handler's rules, shared by both entry points.
func ValidateSingleAsk(model string, input *SingleAskInput) error {
	if input.Effort != "" && input.Effort != "low" && input.Effort != "medium" && input.Effort != "high" && input.Effort != "max" {
		return fmt.Errorf("effort 档位无效")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return fmt.Errorf("请填写问题")
	}
	if !gateway.IsClaude(model) {
		input.Thinking = nil
	}
	if input.Thinking != nil {
		if input.Thinking.Type != "adaptive" && input.Thinking.Type != "enabled" && input.Thinking.Type != "disabled" {
			return fmt.Errorf("thinking 类型无效")
		}
		// updates 需要 anthropic-beta: thinking-display-updates-2026-08-18，网关会自动补上。
		if input.Thinking.Display != "" && input.Thinking.Display != "summarized" && input.Thinking.Display != "omitted" && input.Thinking.Display != "highlights" && input.Thinking.Display != "updates" {
			return fmt.Errorf("display 模式无效")
		}
		if input.Thinking.Type == "enabled" && (input.Thinking.BudgetTokens < 1024 || input.Thinking.BudgetTokens > 8192) {
			return fmt.Errorf("预算应在 1024～8192 token")
		}
	}
	return nil
}
func SingleAsk(ctx context.Context, g gateway.Client, q Request, input SingleAskInput) (gateway.Metrics, error) {
	if err := ValidateSingleAsk(q.Model, &input); err != nil {
		return gateway.Metrics{}, err
	}
	max := 4096
	if input.Thinking != nil && input.Thinking.BudgetTokens >= max {
		max = input.Thinking.BudgetTokens + 2048
	}
	return g.CallMessages(ctx, gateway.CallRequest{Model: q.Model, Prompt: input.Prompt, Thinking: input.Thinking, MaxTokens: max, Stream: true, TimeoutSeconds: q.Timeout, Effort: input.Effort})
}
