import { expect, it } from 'vitest'
import type { KnowledgeRow, KnowledgeVerdict } from '../../chanshui/web/lib/api'
import { knowledgeDateComparison } from '../../chanshui/web/lib/knowledge-dates'

function event(happened_at: string, kind: KnowledgeRow['kind'], error?: string): KnowledgeRow {
  return { id: happened_at, happened_at, kind, quarter: '', event: '', answer: '', ok: kind === 'know', unsure: kind === 'unknown', error }
}

function observations(...rows: KnowledgeRow[]): KnowledgeVerdict {
  return { status: 'estimated', reason: '', protocol: 'chat', rows }
}

it('同一组一月到二月观测只匹配报告模型所属厂商', () => {
  const result = observations(event('2026-01-01', 'know'), event('2026-02-22', 'unknown'))
  for (const model of ['chat-latest']) {
    const gpt = knowledgeDateComparison(model, result)
    expect(gpt).toBe('本轮知识表现接近 GPT-5.6 Sol、GPT-5.6 Terra、GPT-5.6 Luna，暂无法区分。')
    expect(gpt).not.toContain('Claude')
  }
  const claude = knowledgeDateComparison('anthropic/claude-opus-5-5', result)
  expect(claude).toContain('Claude Opus 4.8、Claude Opus 4.7、Claude Sonnet 5 等 5 款模型')
  expect(claude).not.toContain('GPT')
  expect(knowledgeDateComparison('gemini-test', result)).toBeUndefined()
})

it('对照只使用明确答对和明确不知道，并保留两者的先后关系', () => {
  const result = observations(event('2026-01-01', 'know'), event('2026-02-22', 'wrong'), event('2026-04-20', 'unknown', '请求失败'))
  expect(knowledgeDateComparison('gpt-5.6-sol', result)).toContain('证据不足')
  result.rows!.push(event('2025-11-04', 'unknown'))
  expect(knowledgeDateComparison('gpt-5.6-sol', result)).toContain('日期边界互相冲突')
  result.status = 'invalid'
  expect(knowledgeDateComparison('gpt-5.6-sol', result)).toContain('无效回答')
})

it('月精度命中跨月区间并区分唯一候选的证据强弱', () => {
  const result = observations(event('2026-05-28', 'know'), event('2026-05-31', 'unknown'))
  expect(knowledgeDateComparison('claude-sonnet-5', result)).toContain('只有 Claude Opus 5')
  expect(knowledgeDateComparison('claude-sonnet-5', result)).toContain('仍需更多题确认')
  result.rows!.push(event('2026-05-29', 'know'), event('2026-06-01', 'unknown'))
  expect(knowledgeDateComparison('claude-sonnet-5', result)).toBe('本轮知识表现最接近 Claude Opus 5 的公开知识日期。')
  expect(knowledgeDateComparison('gpt-5.6-sol', result)).toContain('没有 GPT 型号')
})


it('未选择或没有适用题目时没有对照结论', () => {
  for (const status of ['not_selected', 'not_applicable']) {
    expect(knowledgeDateComparison('gpt-6.1-sol', { ...observations(), status })).toBeUndefined()
  }
})

it('完整新版只显示实际型号公开日期，旧路径与部分选题无新增提醒', () => {
  const result = observations(event('2026-05-20', 'know'), event('2026-05-30', 'know'))
  expect(knowledgeDateComparison('gpt-6-astra', result)).toBeUndefined()
  result.boundary_review = {status:'not_needed',reason:'no_anchor'}
  expect(knowledgeDateComparison('gpt-6-astra',result)).toContain('公开知识截止日期为 2026-04-30')
  expect(knowledgeDateComparison('gpt-6-astra',result)).toContain('答对了多道')
  result.web_suspect = '答对了近期事件'
  expect(knowledgeDateComparison('gpt-6-astra',result)).not.toContain('答对了多道')
  delete result.web_suspect
  result.boundary_review = {status:'unconfirmed',reason:'retry_pending'}
  expect(knowledgeDateComparison('gpt-6-astra',result)).not.toContain('答对了多道')
})

it('跨年报告未确认时只给声明型号日期，确认后使用后端复核的范围', () => {
  const result = observations(
    event('2025-11-04', 'unknown'), event('2026-01-31', 'know'),
    event('2026-04-12', 'know'), event('2026-04-26', 'unknown'), event('2026-05-16', 'unknown'),
  )
  result.status = 'unknown'
  result.boundary_review = { method: 'cross_year', status: 'unconfirmed', reason: 'conflict' }
  expect(knowledgeDateComparison('chat-latest', result)).toBe('Chat Latest（chat-latest） 的公开知识截止日期为 2026-04-20。')
  expect(knowledgeDateComparison('gpt-unlisted', result)).toBeUndefined()
  result.status = 'estimated'
  result.boundary_review = { method: 'cross_year', status: 'two_unknown', reason: 'two_unknown', from: '2026-04-12', to: '2026-04-26' }
  expect(knowledgeDateComparison('chat-latest', result)).toContain('本轮知识表现接近 GPT-6 Sol、Chat Latest')
  expect(knowledgeDateComparison('chat-latest', result)).not.toContain('互相冲突')
  result.boundary_review.to = '2025-11-04'
  expect(knowledgeDateComparison('chat-latest', result)).not.toContain('本轮知识表现')
})

it('跨年报告只有独立的较晚正答可提示核对，早期未知或失败抑制提示', () => {
  const result = observations(event('2026-05-16', 'know'), event('2026-05-20', 'know'))
  result.status = 'unknown'
  result.boundary_review = { method: 'cross_year', status: 'unconfirmed', reason: 'no_anchor' }
  expect(knowledgeDateComparison('gpt-6.1-sol', result)).toContain('答对了多道')
  result.rows!.push(event('2025-11-04', 'unknown'))
  expect(knowledgeDateComparison('gpt-6.1-sol', result)).not.toContain('答对了多道')
  result.rows!.pop()
  result.boundary_review.reason = 'main_error'
  expect(knowledgeDateComparison('gpt-6.1-sol', result)).not.toContain('答对了多道')
})
