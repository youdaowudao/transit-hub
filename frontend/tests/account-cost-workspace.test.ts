import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { prepareIdempotentSubmission } from '../src/modules/admin/utils/idempotency'

const source = readFileSync(new URL('../src/modules/admin/components/dashboard/AccountCostWorkspace.vue', import.meta.url), 'utf8')

describe('account cost workspace', () => {
  it('keeps the four work views at one drawer level', () => {
    for (const label of ['今日成本', '买号资产', '成本账本', '成本规则']) expect(source).toContain(label)
    expect(source).toContain("type WorkspaceTab = 'today' | 'assets' | 'ledger' | 'rules'")
  })

  it('supports efficient batch entry and account lifecycle follow-up', () => {
    for (const field of ['platform', 'channel', 'accountType', 'purchaseUrl', 'defaultUpstreamReferenceUrl', 'quantity', 'totalAmount']) expect(source).toContain(field)
    for (const event of ['status', 'restore', 'refund', 'quota_observation', 'manual_observation', 'stats_mode_change']) expect(source).toContain(event)
    expect(source).toContain('逐号分摊预览')
    expect(source).toContain('实际平均售出倍率')
    expect(source).toContain('最终盈亏')
		expect(source.match(/<Button type="submit" :disabled="saving">/g)).toHaveLength(3)
  })

  it('uses real connection choices, explicit refund closing and responsive asset views', () => {
	expect(source).toContain('listRealConnections')
	expect(source).toContain('eligibleConnections')
	expect(source).toContain("input.eventType === 'refund' && eventForm.refundClose")
	expect(source).toContain('退款并关闭账号')
	expect(source).toContain('md:hidden')
	expect(source).toContain('ledgerGroups')
	expect(source).toContain('补充或更换上游关联')
  })

  it('previews unit or total pricing and keeps historical gaps explicit', () => {
	expect(source).toContain('unitAmount')
	expect(source).toContain('identifierPaste')
	expect(source).toContain('尾差归最后一个账号、最后一天')
	expect(source).toContain('dailyRecognitionPreview')
	expect(source).toContain('查看每天确认金额')
	expect(source).toContain('缺少单号历史快照')
	expect(source).toContain('while (accountRows.value.length < identifiers.length)')
  })

  it('returns to the asset list and keeps the newly created batch visible', () => {
    expect(source).toContain('recentBatch.value = { id: result.batch.id')
    expect(source).toContain('selectedDetail.value = null')
    expect(source).not.toContain('selectedDetail.value = await getAccountAsset(created.id)')
    expect(source).toContain('已保存批次')
  })

  it('paginates ledger source groups instead of daily rows', () => {
    expect(source).toContain('ledgerHasMore.value = result.hasMore')
    expect(source).toContain('group.records[0]?.businessDate')
  })

  it('uses the backend pagination boundary for account assets', () => {
		expect(source).toContain('assetHasMore.value = result.hasMore')
		expect(source).not.toContain('assetHasMore.value = result.items.length === 50')
	})

  it('collects auditable same-day splits and complete account history values', () => {
	for (const field of ['previousQuotaUsed', 'previousRevenue', 'replacementQuotaUsed', 'replacementRevenue']) expect(source).toContain(field)
	expect(source).toContain('recognizedCostCents')
	expect(source).toContain('eventSummary')
	expect(source).toContain('metadata_correction')
	expect(source).toContain('missingFieldText')
	expect(source).toContain("event.upstreamReferenceUrl !== undefined")
	expect(source).toContain('上游参考链接已清空')
  })

  it('labels business dates, wraps audit links and provides a live-link action for dead accounts', () => {
    for (const label of ['购买日期', '确认开始日', '关联生效日', '事件生效日']) expect(source).toContain(label)
    expect(source).toContain("router.push({ name: 'AdminUpstream' })")
    expect(source).toContain('前往上游处理关联')
    expect(source).toContain('break-all')
  })

  it('reuses an idempotency key until the same business submission succeeds', () => {
    let sequence = 0
    const generate = (prefix: string) => `${prefix}-${++sequence}`
    const first = prepareIdempotentSubmission(null, 'account-batch', { total: 1001, accounts: ['a', 'b'] }, generate)
    const retry = prepareIdempotentSubmission(first, 'account-batch', { total: 1001, accounts: ['a', 'b'] }, generate)
    const changed = prepareIdempotentSubmission(retry, 'account-batch', { total: 1002, accounts: ['a', 'b'] }, generate)
    expect(retry.key).toBe(first.key)
    expect(changed.key).not.toBe(first.key)
    for (const attempt of ['batchAttempt', 'eventAttempt', 'linkAttempt', 'statsModeAttempt']) {
      expect(source).toContain(attempt)
    }
  })

  it('keeps the idempotency key when the write succeeds but the follow-up refresh fails', () => {
    const submitBatch = source.slice(source.indexOf('const submitBatch'), source.indexOf('const openAsset'))
    const submitEvent = source.slice(source.indexOf('const submitEvent'), source.indexOf('const submitLink'))
    const submitLink = source.slice(source.indexOf('const submitLink'), source.indexOf('const submitStatsMode'))
    const submitStatsMode = source.slice(source.indexOf('const submitStatsMode'), source.indexOf('const submitCost'))
    expect(submitBatch.indexOf('batchAttempt.value = null')).toBeGreaterThan(submitBatch.indexOf('await loadAssets()'))
    expect(submitEvent.indexOf('eventAttempt.value = null')).toBeGreaterThan(submitEvent.indexOf('await loadAssets()'))
    expect(submitLink.indexOf('linkAttempt.value = null')).toBeGreaterThan(submitLink.indexOf('await loadAssets()'))
    expect(submitStatsMode.indexOf('statsModeAttempt.value = null')).toBeGreaterThan(submitStatsMode.indexOf('await loadAssets()'))
  })
})
