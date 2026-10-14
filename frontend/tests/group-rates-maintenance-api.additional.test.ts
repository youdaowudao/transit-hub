import { afterEach, describe, expect, it, vi } from 'vitest'
import { listConnectionsNeedingAttention, realDisconnect } from '@/modules/admin/api/mySites'
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number) => status === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
const query = { workspaceAdminAccountId: 'ws', page: 1 }
const payload = () => ({ workspaceAdminAccountId: 'ws', total: 1, page: 1, pageSize: 30, totalPages: 1, items: [{ connection: { id: 'connection', upstreamSiteId: 'site', upstreamGroupId: 'gone', upstreamGroupName: '旧组', upstreamKeyId: 'key', adminAccountId: 'account', ownGroupIds: [], ownGroupNames: [], status: '' }, reason: 'source_unconfirmed', detail: '分组资料无法确认', lastSyncedAt: null }] })
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
afterEach(() => vi.unstubAllGlobals())
describe('maintenance local API confirms pagination, scope and actual outcomes', () => {
  it('sends exact workspace and optional site while retaining legacy empty-status records', async () => {
    const fetcher = vi.fn(async () => response(payload())); vi.stubGlobal('fetch', fetcher)
    const result = await listConnectionsNeedingAttention({ ...query, siteId: 'site' })
    expect(result.items[0]!.connection.status).toBe('')
    const url = new URL(String(fetcher.mock.calls[0]![0]), 'http://local')
    expect(url.searchParams.get('workspaceAdminAccountId')).toBe('ws'); expect(url.searchParams.get('siteId')).toBe('site')
  })
  it.each(['workspace', 'pagination', 'secret', 'shape'])('rejects %s damage rather than reporting zero records', async variant => {
    const value = payload()
    if (variant === 'workspace') value.workspaceAdminAccountId = 'other'
    if (variant === 'pagination') value.total = 2
    if (variant === 'secret') Object.assign(value.items[0]!.connection, { upstreamKey: 'secret' })
    if (variant === 'shape') Object.assign(value.items[0]!.connection, { ownGroupIds: null })
    vi.stubGlobal('fetch', vi.fn(async () => response(value)))
    await expect(listConnectionsNeedingAttention(query)).rejects.toThrow('资料不完整')
  })
  it('preserves a checked unlink conflict cause and next step', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ message: '已有对接资料已变化，本次未解除，请重新打开记录', detail: '账号归组已变化', next: '请重新读取并打开记录' }, 409)))
    await expect(realDisconnect({ connectionId: 'connection', mode: 'unlink', removePricingMapping: false, expectedConnection: { workspaceAdminAccountId: 'ws', id: 'connection', upstreamSiteId: 'site', upstreamGroupId: 'gone', upstreamGroupName: '旧组', upstreamKeyId: 'key', adminAccountId: 'account', adminPlatform: '', upstreamPlatform: '', provisioningMode: '', status: '', ownGroupIds: [], ownGroupNames: [], pricingMappingEnabled: false } })).rejects.toThrow('账号归组已变化；请重新读取并打开记录')
  })
})
