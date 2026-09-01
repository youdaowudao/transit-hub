import { afterEach, describe, expect, it, vi } from 'vitest'
import { createAccountBatch, createAccountEvent, getAdditionalCost, listAccountCostLedger, replaceAccountLink, updateAdditionalCost } from '../src/modules/admin/api/dashboardAdmin'

afterEach(() => vi.unstubAllGlobals())

describe('account cost API', () => {
  it('sends idempotency keys for batch and lifecycle writes', async () => {
	vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ batch: {}, assets: [] }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ event: {}, asset: {} }), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)

    await createAccountBatch({
      batchName: '8 月 Claude', platform: 'Claude', channel: '渠道 A', accountType: 'Team',
      purchaseDate: '2026-08-22', purchaseUrl: '', defaultUpstreamReferenceUrl: '', quantity: 2,
      totalAmountCents: 1000, identifiers: ['a', 'b'], accounts: [], accountingMode: 'replace_upstream',
      recognitionMode: 'daily', recognitionStartDate: '2026-08-22', recognitionDays: 2, statsMode: 'manual', note: '',
    }, 'batch-key')
    await createAccountEvent('asset-1', { eventType: 'refund', effectiveDate: '2026-08-22', refundCents: 100 }, 'event-key')

    expect(fetchMock.mock.calls[0][1].headers['Idempotency-Key']).toBe('batch-key')
    expect(fetchMock.mock.calls[1][1].headers['Idempotency-Key']).toBe('event-key')
  })

  it('replaces links through the scoped idempotent endpoint', async () => {
	vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
	const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ asset: {}, batch: {}, links: [], events: [], dailyStats: [], performance: {} }), { status: 200 }))
	vi.stubGlobal('fetch', fetchMock)
	await replaceAccountLink('asset/1', {
	  connectionId: 'connection-1', upstreamReferenceUrl: 'https://supplier.example/a',
	  effectiveFrom: '2026-08-22', manualSameDaySplit: true,
	  previousQuotaUsedMicros: 40_000_000, previousRevenueCents: 8000,
	  replacementQuotaUsedMicros: 60_000_000, replacementRevenueCents: 12_000,
	}, 'link-key')
	const [url, options] = fetchMock.mock.calls[0]
	expect(String(url)).toContain('/dashboard/account-assets/asset%2F1/link')
	expect(options.method).toBe('PUT')
	expect(options.headers['Idempotency-Key']).toBe('link-key')
	expect(JSON.parse(options.body)).toMatchObject({ previousQuotaUsedMicros: 40_000_000, replacementRevenueCents: 12_000 })
	expect(options.body).not.toContain('adminAccountId')
  })

  it('encodes ledger filters without leaking workspace ids', async () => {
	vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await listAccountCostLedger({ from: '2026-08-01', to: '2026-08-22', platform: 'Claude', accountAssetId: 'asset-1' })
    const url = String(fetchMock.mock.calls[0][0])
    expect(url).toContain('platform=Claude')
    expect(url).toContain('accountAssetId=asset-1')
    expect(url).not.toContain('adminAccountId')
    expect(url).not.toContain('userId')
  })

  it('reads and replaces one manual source without sending workspace fields', async () => {
	vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [{ id: 'source-1-0', sourceId: 'source-1', businessDate: '2026-08-20' }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await getAdditionalCost('source/1')
    await updateAdditionalCost('source/1', {
      type: 'fixed', name: '服务器', businessDate: '2026-08-20', amount: 100, days: 2, note: '更正',
    })

    expect(String(fetchMock.mock.calls[0][0])).toContain('/dashboard/additional-costs/source%2F1')
    expect(fetchMock.mock.calls[0][1].method).toBeUndefined()
    const [url, options] = fetchMock.mock.calls[1]
    expect(String(url)).toContain('/dashboard/additional-costs/source%2F1')
    expect(options.method).toBe('PUT')
    expect(JSON.parse(options.body)).toEqual(expect.objectContaining({ type: 'fixed', amount: 100, days: 2 }))
    expect(options.body).not.toContain('workspaceId')
    expect(options.body).not.toContain('userId')
  })
})
