import { afterEach, describe, expect, it, vi } from 'vitest'
import { checkRealConnections, realConnect, realDisconnect, saveMySiteMapping, readConnectionMainSiteGroups, changeConnectionMainSiteGroups, syncConnectionHubGroups } from '../src/modules/admin/api/mySites'
import type { MySiteMapping, RealConnectRequest } from '../src/modules/admin/types/mySites'

const mappingWithLastRun = (): MySiteMapping => ({
  ownGroup: 'Gemini premium',
  upstreamTargets: [{ siteId: 'site-1', groupName: 'gemini' }],
  enableAutoPricing: true,
  autoPricingSource: 'primary_upstream',
  primaryUpstreamSiteId: 'site-1',
  primaryUpstreamGroupName: 'gemini',
  autoPricingStrategy: 'percentage',
  fixedIncrease: 0,
  percentageIncrease: 100,
  adjustThresholdPercent: 100,
  minMultiplier: null,
  maxMultiplier: null,
  enableAutoPricingNotify: false,
  autoPricingNotifyBotIds: [],
  autoPricingNotifyTemplate: '',
  lastAutoPricingRun: {
    status: 'skipped',
    reason: 'threshold_exceeded',
    trigger: 'manual',
    ranAt: '2026-08-25T03:25:25Z',
    oldReference: 0.1,
    newReference: 0.2,
    targetMultiplier: 0.4,
    oldOwnMultiplier: 0.2,
    newOwnMultiplier: 0.2,
  },
})

afterEach(() => vi.unstubAllGlobals())

describe('my sites mapping API', () => {
  it('checks real connections with a body-free POST request', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      checked: 2,
      active: 1,
      missing: 1,
    }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(checkRealConnections()).resolves.toEqual({ checked: 2, active: 1, missing: 1 })
    expect(fetchMock).toHaveBeenCalledWith('/api/my-sites/real-connections/check', expect.objectContaining({ method: 'POST' }))
    expect(fetchMock.mock.calls[0][1]).not.toHaveProperty('body')
  })

  it('serializes only writable fields in PATCH mapping saves', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const mapping = mappingWithLastRun()
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      authenticated: true,
      mappings: [mapping],
    }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await saveMySiteMapping(mapping, [mapping])

    const [url, options] = fetchMock.mock.calls[0]
    const requestBody = JSON.parse(String(options.body))
    const { lastAutoPricingRun: _lastRun, ...writableMapping } = mapping
    expect(url).toBe('/api/my-sites/mappings')
    expect(options.method).toBe('PATCH')
    expect(requestBody).toEqual({ mapping: writableMapping })
    expect(requestBody.mapping).not.toHaveProperty('lastAutoPricingRun')
  })

  it('serializes only writable fields in fallback PUT mapping saves', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const mapping = mappingWithLastRun()
    const otherMapping: MySiteMapping = {
      ...mappingWithLastRun(),
      ownGroup: 'Claude premium',
      upstreamTargets: [{ siteId: 'site-2', groupName: 'claude' }],
      primaryUpstreamSiteId: 'site-2',
      primaryUpstreamGroupName: 'claude',
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        message: 'admin.mySites.errors.request',
      }), { status: 400 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        authenticated: true,
        mappings: [mapping, otherMapping],
      }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await saveMySiteMapping(mapping, [mapping, otherMapping])

    const [url, options] = fetchMock.mock.calls[1]
    const requestBody = JSON.parse(String(options.body))
    const { lastAutoPricingRun: _mappingLastRun, ...writableMapping } = mapping
    const { lastAutoPricingRun: _otherLastRun, ...writableOtherMapping } = otherMapping
    expect(url).toBe('/api/my-sites/mappings')
    expect(options.method).toBe('PUT')
    expect(requestBody).toEqual({ mappings: [writableMapping, writableOtherMapping] })
    expect(requestBody.mappings).toHaveLength(2)
    expect(requestBody.mappings.every((item: MySiteMapping) => !Object.hasOwn(item, 'lastAutoPricingRun'))).toBe(true)
  })
})


describe('safe deletion API evidence', () => {
  it('retains the safe failure key from the existing message envelope', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: 'admin.connectionHealth.errors.sub2apiGroupLastUsable' }), { status: 409 })))
    await expect(realDisconnect({ connectionId: 'isolated', mode: 'full', removePricingMapping: true })).rejects.toThrow('admin.connectionHealth.errors.sub2apiGroupLastUsable')
  })
  it.each(['admin.mySites.errors.resourcesPendingVerification', 'admin.mySites.errors.accountCreationPendingVerification', 'admin.mySites.errors.compensationPendingVerification', 'admin.mySites.errors.upstreamKeyCleanupPendingVerification'])('retains resource IDs without retaining other response fields for %s', async (key) => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: key, adminResourceId: '101', upstreamKeyId: '207', ignored: 'synthetic-private-data' }), { status: 409 })))
    let captured: unknown
    try { await realDisconnect({ connectionId: 'isolated', mode: 'full', removePricingMapping: false }) } catch (error) { captured = error }
    expect(captured).toMatchObject({ message: key, adminResourceId: '101', upstreamKeyId: '207' })
    expect(captured).not.toHaveProperty('ignored')
  })
})


const grokImportRequest = (): RealConnectRequest => ({
  upstreamSiteId: 'grok-source', upstreamGroupId: '70', upstreamGroupName: 'source-group', groupType: 'grok', ownGroupIds: ['7', '8'],
  accountSettings: { priorityMode: 'manual', priority: 7, concurrency: 73, passthrough: false, poolMode: false, upstreamBillingProbeEnabled: false },
})
const grokImportResponse = () => ({
  workspaceAdminAccountId: 'grok-workspace', configurationStatus: 'confirmed',
  connection: { id: 'a'.repeat(32), upstreamSiteId: 'grok-source', upstreamGroupId: '70', upstreamGroupName: 'source-group', adminAccountId: '22', adminAccountName: 'X-source', upstreamKeyId: '11', ownGroupIds: ['7', '8'], groupType: 'grok', adminPlatform: 'sub2api', status: 'active', createdAt: '2026-10-14T00:00:00Z' },
  configuration: { observation: 'creation', adminAccountId: '22', name: 'X-source', platform: 'grok', priority: 7, concurrency: 73, passthrough: false, poolMode: false, upstreamBillingProbeEnabled: false, ownGroups: [{ id: '7', name: 'Grok A' }, { id: '8', name: 'Grok B' }], modelState: 'synced', models: ['grok-live', 'provider/custom'] },
})
const grokWarning = { stage: 'model_sync', target: 'main', message: 'admin.mySites.errors.importModelSyncWarning', reason: 'admin.upstream.errors.forbidden', httpStatus: 403, detail: '主站返回：请检查模型接口权限' }
const mockImportFetch = (payload: unknown, status = 200) => {
  vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(payload), { status }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('Grok import API contract', () => {
  it('preserves selected Grok, both target groups and explicit false settings through confirmed readback', async () => {
    const request = grokImportRequest(), payload = grokImportResponse(), fetchMock = mockImportFetch(payload)
    const result = await realConnect(request, 'grok-workspace')
    expect(result.configurationStatus).toBe('confirmed')
    expect(result.configuration).toEqual(payload.configuration)
    expect(result.connection.groupType).toBe('grok')
    expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual(request)
  })
  it('accepts a complete Grok warning without inventing model sync success', async () => {
    const payload: any = grokImportResponse()
    payload.configuration.modelState = 'not_synced'; payload.configuration.models = []; payload.diagnostics = [grokWarning]
    mockImportFetch(payload)
    const result = await realConnect(grokImportRequest(), 'grok-workspace')
    expect(result).toMatchObject({ configurationStatus: 'confirmed', configuration: { platform: 'grok', modelState: 'not_synced', models: [], passthrough: false }, diagnostics: [grokWarning] })
  })
  it.each(['current_whitelist', 'current_unrestricted'])('accepts replay %s as current configuration', async modelState => {
    const payload = grokImportResponse()
    payload.configuration = { ...payload.configuration, observation: 'current', priority: 0, concurrency: 2000, modelState, models: modelState === 'current_whitelist' ? ['current-alias-*'] : [], ownGroups: [{ id: '9', name: 'Current Grok' }] }
    mockImportFetch(payload)
    const result = await realConnect(grokImportRequest(), 'grok-workspace')
    expect(result.configuration).toEqual(payload.configuration)
    expect(result.configuration?.observation).toBe('current')
  })
  it.each(['wrong-platform', 'forbidden-passthrough', 'missing-warning', 'partial-warning-models'])('keeps completed association but refuses invalid Grok configuration: %s', async mode => {
    const payload: any = grokImportResponse()
    if (mode === 'wrong-platform') payload.configuration.platform = 'openai'
    if (mode === 'forbidden-passthrough') { payload.configuration.passthrough = true; payload.configuration.modelState = 'not_required'; payload.configuration.models = [] }
    if (mode.includes('warning')) { payload.configuration.modelState = 'not_synced'; payload.configuration.models = mode === 'partial-warning-models' ? ['invented'] : []; if (mode !== 'missing-warning') payload.diagnostics = [grokWarning] }
    mockImportFetch(payload)
    const result = await realConnect(grokImportRequest(), 'grok-workspace')
    expect(result.connection.groupType).toBe('grok')
    expect(result.configurationStatus).toBe('unavailable')
    expect(result).not.toHaveProperty('configuration')
  })
  it.each(['wrong-connection-platform', 'wrong-main', 'wrong-workspace', 'malformed-http-success'])('keeps %s pending without trustworthy creation confirmation', async mode => {
    const payload: any = grokImportResponse()
    if (mode === 'wrong-connection-platform') payload.connection.groupType = 'openai'
    if (mode === 'wrong-main') payload.connection.adminPlatform = 'grok'
    if (mode === 'wrong-workspace') payload.workspaceAdminAccountId = 'other'
    if (mode === 'malformed-http-success') delete payload.connection
    mockImportFetch(payload)
    await expect(realConnect(grokImportRequest(), 'grok-workspace')).rejects.toMatchObject({ cleanup: 'pending', retryAllowed: false })
  })
})

const groupConnection = () => ({ id: 'connection-1', upstreamSiteId: 'site-1', upstreamGroupId: '1', upstreamGroupName: '上游组', upstreamKeyId: 'key-1', adminAccountId: '101', adminAccountName: '主站账号', ownGroupIds: ['10'], ownGroupNames: ['原组'], groupType: 'openai', provisioningMode: 'managed', status: 'active', adminPlatform: 'sub2api', pricingMappingEnabled: true, createdAt: '2026-10-17T00:00:00Z' })
const groupsRead = () => ({ workspaceAdminAccountId: 'ws', connection: groupConnection(), account: { id: '101', name: '主站账号', platform: 'openai' }, currentGroups: [{ id: '10', name: '原组' }], hubGroups: [{ id: '10', name: '原组' }], groups: [{ id: '10', name: '原组', status: 'active' }], consistent: true })
const groupRequest = () => ({ workspaceAdminAccountId: 'ws', expectedConnection: { ...groupConnection(), workspaceAdminAccountId: 'ws', upstreamPlatform: '', ownGroupNames: ['原组'] }, expectedMainSiteGroupIds: ['10'], targetGroupIds: ['11'], removeOldPricingMapping: true })
const groupResult = (outcome = 'completed') => ({ workspaceAdminAccountId: 'ws', outcome, message: '修改结果', detail: '具体原因', next: '请核对', mainSiteGroups: [{ id: '11', name: '新组' }], connection: { ...groupConnection(), ownGroupIds: ['11'], ownGroupNames: ['新组'] } })
describe('main site group API contract', () => {
  it('reads current account groups using the selected connection and workspace', async () => {
    const payload = groupsRead(); payload.connection.id = 'connection/1'
    const fetchMock = mockImportFetch(payload)
    await expect(readConnectionMainSiteGroups('connection/1', 'ws')).resolves.toEqual(payload)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/my-sites/real-connections/connection%2F1/main-site-groups?workspaceAdminAccountId=ws')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'GET' })
  })
  it.each(['change', 'sync'])('sends the complete latest expectations through %s', async mode => {
    const fetchMock = mockImportFetch(groupResult())
    const request = groupRequest()
    if (mode === 'change') await changeConnectionMainSiteGroups('connection-1', request)
    else { const { targetGroupIds: _target, ...sync } = request; await syncConnectionHubGroups('connection-1', sync) }
    expect(fetchMock.mock.calls[0][0]).toBe('/api/my-sites/real-connections/connection-1/main-site-groups' + (mode === 'sync' ? '/hub-record' : ''))
    const body = JSON.parse(fetchMock.mock.calls[0][1].body)
    expect(body.expectedMainSiteGroupIds).toEqual(['10'])
    expect(body.expectedConnection.ownGroupIds).toEqual(['10'])
    expect(body.removeOldPricingMapping).toBe(true)
    if (mode === 'sync') expect(body).not.toHaveProperty('targetGroupIds')
    else expect(body.targetGroupIds).toEqual(['11'])
  })
  it.each(['read', 'change', 'sync'])('distinguishes a missing response in %s', async mode => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')))
    const pending = mode === 'read' ? readConnectionMainSiteGroups('connection-1', 'ws') : mode === 'change' ? changeConnectionMainSiteGroups('connection-1', groupRequest()) : syncConnectionHubGroups('connection-1', groupRequest())
    await expect(pending).rejects.toMatchObject({ kind: 'network' })
  })
  it('retains all three safe server explanations', async () => {
    mockImportFetch({ message: '分组不能移出', detail: '原组没有其他可用账号', next: '请保留原组' }, 409)
    await expect(changeConnectionMainSiteGroups('connection-1', groupRequest())).rejects.toThrow('分组不能移出；原组没有其他可用账号；请保留原组')
  })
  it.each(['workspace', 'account', 'connection', 'current', 'hub', 'groups', 'consistent', 'secret'])('rejects malformed read fields: %s', async mode => {
    const payload: any = groupsRead()
    if (mode === 'workspace') payload.workspaceAdminAccountId = 'other'
    if (mode === 'account') delete payload.account.platform
    if (mode === 'connection') delete payload.connection.ownGroupIds
    if (mode === 'current') payload.currentGroups = [{ id: '10' }]
    if (mode === 'hub') payload.hubGroups = '10'
    if (mode === 'groups') delete payload.groups[0].status
    if (mode === 'consistent') delete payload.consistent
    if (mode === 'secret') payload.connection.upstreamKey = 'private-response'
    mockImportFetch(payload)
    await expect(readConnectionMainSiteGroups('connection-1', 'ws')).rejects.toMatchObject({ kind: 'invalid' })
  })
  it.each(['completed', 'unchanged', 'main_changed_hub_not_saved', 'main_unknown', 'hub_unknown'])('preserves outcome %s', async outcome => {
    mockImportFetch(groupResult(outcome))
    await expect(changeConnectionMainSiteGroups('connection-1', groupRequest())).resolves.toMatchObject({ outcome, workspaceAdminAccountId: 'ws' })
  })
  it.each(['workspace', 'outcome', 'json'])('refuses unconfirmed mutation results: %s', async mode => {
    const payload: any = groupResult()
    if (mode === 'workspace') payload.workspaceAdminAccountId = 'other'
    if (mode === 'outcome') payload.outcome = 'success'
    mockImportFetch(payload)
    if (mode === 'json') vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('not json', { status: 200 })))
    await expect(changeConnectionMainSiteGroups('connection-1', groupRequest())).rejects.toMatchObject({ kind: 'invalid' })
  })
  it('expires the Hub session on 401 even without JSON', async () => {
    const removeItem = vi.fn()
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null), removeItem })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('expired', { status: 401 })))
    await expect(readConnectionMainSiteGroups('connection-1', 'ws')).rejects.toThrow('auth.errors.unauthorized')
    expect(removeItem).toHaveBeenCalled()
  })
})
