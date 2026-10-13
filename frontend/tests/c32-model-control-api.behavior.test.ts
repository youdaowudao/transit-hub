// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getModelControlAutoSettings, saveModelControlAutoSettings, getModelControlTarget, listModelControlItems, addModelControlManaged, previewModelControl, executeModelControl, verifyModelControl, listModelControlEvents, getConnectionHealthAdminGroups } from '@/modules/admin/api/connectionHealth'
import { managed, now, targetId } from './fixtures/v2911ModelControl'
import { c32Groups } from './fixtures/c32ModelControl'
import { realConnect } from '@/modules/admin/api/mySites'
const settings = { autoCloseEnabled: false, autoCloseEnabledAt: null, autoRestoreEnabled: false, autoRestoreEnabledAt: null, healthCoordinationEnabled: false, verifyIntervalMinutes: 10, retryIntervalMinutes: 5, version: 0, lastScanStartedAt: null, lastScanFinishedAt: null, lastScanErrorKey: '' }
let response: unknown, status: number, calls: Array<{ url: string; init: RequestInit }> = []
beforeEach(() => { response = settings; status = 200; calls = []; vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => { calls.push({ url, init }); return new Response(JSON.stringify(response), { status }) })) })
afterEach(() => vi.unstubAllGlobals())
describe('C3.2 new settings and optional fields have strict response parsing', () => {
 it('reads defaults and saves exactly five fields plus expected version', async () => {
  await expect(getModelControlAutoSettings()).resolves.toEqual(settings); await saveModelControlAutoSettings({ ...settings, autoCloseEnabled: true, version: 7 }, 7)
  expect(calls[1]!.url).toContain('/model-control/auto-settings'); expect(JSON.parse(String(calls[1]!.init.body))).toEqual({ autoCloseEnabled: true, autoRestoreEnabled: false, healthCoordinationEnabled: false, verifyIntervalMinutes: 10, retryIntervalMinutes: 5, expectedVersion: 7 })
 })
 it.each([['autoCloseEnabled', 'false'], ['autoRestoreEnabled', 0], ['healthCoordinationEnabled', null], ['autoCloseEnabledAt', 'bad-date'], ['autoRestoreEnabledAt', 1], ['verifyIntervalMinutes', 4], ['verifyIntervalMinutes', 60.1], ['retryIntervalMinutes', 0], ['retryIntervalMinutes', 61], ['version', -1], ['lastScanStartedAt', ''], ['lastScanFinishedAt', undefined], ['lastScanErrorKey', null]])('rejects malformed %s=%s', async (field, value) => {
  response = { ...settings, [field]: value }; await expect(getModelControlAutoSettings()).rejects.toThrow('admin.connectionHealth.errors.request')
 })
 it('accepts absent flags from old items, while validating every item-returning endpoint', async () => {
  const old = managed(), bad = { ...managed(), auto: { state: 'waiting', operation: 'close', nextAt: null, reasonKey: '', changedAt: 1 } }
  response = { targetId, candidates: [], items: [old] }; await expect(getModelControlTarget(targetId)).resolves.toHaveProperty('items')
  for (const load of [() => getModelControlTarget(targetId), () => listModelControlItems('all', '', 1), () => verifyModelControl([targetId])]) { response = { items: [bad], candidates: [], targetId, errors: [], page: 1, totalPages: 1, counts: {} }; await expect(load()).rejects.toThrow('对象不匹配') }
  response = bad; await expect(addModelControlManaged(targetId, 'M')).rejects.toThrow('对象不匹配')
  response = { item: bad }; await expect(previewModelControl(targetId, 'M', 'close', old.basis)).rejects.toThrow('对象不匹配'); await expect(executeModelControl(targetId, 'M', 'close', old.basis, 'fp', false)).rejects.toThrow('对象不匹配')
 })
 it.each([{ state: 'unknown' }, { operation: 'add' }, { nextAt: 1 }, { reasonKey: false }, { changedAt: null }])('rejects malformed auto projection %j', async patch => {
  response = { items: [{ ...managed(), auto: { state: 'retry', operation: 'restore', nextAt: now, reasonKey: '', changedAt: now, ...patch } }], targetId, candidates: [] }; await expect(getModelControlTarget(targetId)).rejects.toThrow('对象不匹配')
 })
 it('accepts valid optional admin markers and rejects malformed booleans or source rows', async () => {
  response = c32Groups(); await expect(getConnectionHealthAdminGroups()).resolves.toHaveLength(1)
  for (const field of ['modelControlNoActiveProbe', 'modelControlClosed', 'modelFloorLastSources']) {
   const groups = c32Groups(), a = groups[0]!.accounts[0]! as unknown as Record<string, unknown>
   if (field === 'modelControlClosed') groups[0]!.accounts[0]!.modelHealth[0]!.modelControlClosed = 'true' as unknown as boolean
   else a[field] = field === 'modelFloorLastSources' ? [{ groupId: 1, groupName: '', modelKey: '' }] : 'true'
   response = groups; await expect(getConnectionHealthAdminGroups()).rejects.toThrow('admin.connectionHealth.errors.request')
  }
  const groups = c32Groups(); groups[0]!.accounts[0]!.unprobedModels![0]!.modelControlClosed = 1 as unknown as boolean; response = groups; await expect(getConnectionHealthAdminGroups()).rejects.toThrow('admin.connectionHealth.errors.request')
 })
 it('retains empty automatic actor and absent old actor, rejects wrong actor type', async () => {
  response = { items: [{ actorUserId: '' }, {}], page: 1, totalPages: 1 }; await expect(listModelControlEvents('', '', 1)).resolves.toHaveProperty('items')
  response = { items: [{ actorUserId: null }], page: 1, totalPages: 1 }; await expect(listModelControlEvents('', '', 1)).rejects.toThrow('admin.connectionHealth.errors.request')
 })
 it('retains the approved same-model safety reason in a real import failure and rejects unknown reasons', async () => {
  const request = { upstreamSiteId: 'upstream', upstreamGroupId: '1', upstreamGroupName: '上游组', groupType: 'subscription', ownGroupIds: ['1'], accountSettings: { priorityMode: 'automatic' as const, priority: 0, concurrency: 1, passthrough: false, poolMode: false, upstreamBillingProbeEnabled: false } }
  status = 409; response = { message: 'admin.mySites.errors.compensationPendingVerification', stage: 'account_create', cleanup: 'retained', retryAllowed: false, adminResourceId: '101', reason: 'admin.connectionHealth.errors.sub2apiGroupLastModelSource', groupId: '1', groupName: '保留组' }
  await expect(realConnect(request, 'ws1')).rejects.toMatchObject({ trusted: true, reason: 'admin.connectionHealth.errors.sub2apiGroupLastModelSource', groupName: '保留组' })
  response = { ...response as object, reason: 'unknown' }; await expect(realConnect(request, 'ws1')).rejects.toMatchObject({ trusted: true, reason: undefined })
 })
})
