// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, type Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useUpstreamSites } from '@/modules/admin/composables/useUpstreamSites'
import type { UpstreamSiteForm, UpstreamSiteResponse } from '@/modules/admin/types/upstream'

const api = vi.hoisted(() => ({ list: vi.fn(), sync: vi.fn(), update: vi.fn(), stream: vi.fn(), remove: vi.fn(), account: undefined as Ref<{ id: string }> | undefined }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  api.account = ref({ id: 'workspace-a' })
  return { useAdminAccounts: () => ({ currentAccount: api.account }) }
})
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: api.list, syncUpstreamSite: api.sync, updateUpstreamSite: api.update, streamSyncAllUpstreamSites: api.stream, removeUpstreamSite: api.remove, syncAllUpstreamSites: vi.fn(), createUpstreamSite: vi.fn(), updateUpstreamSiteEnabled: vi.fn(), updateUpstreamSitePinned: vi.fn(), updateSiteSettings: vi.fn() }))

const rejectedKey = 'admin.upstream.errors.refreshTokenRejected'
const rejection = () => Object.assign(new Error(rejectedKey), { failure: { stage: 'refresh', httpStatus: 401, errorDetail: '已暂停自动访问，请用原账号重新登录。' } })
const fixture = (id = 'site'): UpstreamSiteResponse => ({ id, name: id, baseUrl: 'https://fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', authMode: 'token', account: 'fixture', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, metrics: { balance: { value: 7, display: '7' }, todayConsume: { value: 1, display: '1' }, historyRecharge: { value: 10, display: '10' }, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
const paused = (): UpstreamSiteResponse => ({ ...fixture(), status: 'error', errorKey: rejectedKey })
const form = (credential = ''): UpstreamSiteForm => ({ name: 'site', siteUrl: 'https://fixture.invalid', platform: 'sub2api', authMode: 'token', account: 'fixture', accessToken: credential, refreshToken: '', password: '', tokenType: '', userId: '', rechargeRate: 1, remark: '' })
const deferred = <T,>() => { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail }); return { promise, resolve, reject } }
let wrapper: VueWrapper | undefined
const start = async () => {
  let state!: ReturnType<typeof useUpstreamSites>
  wrapper = mount(defineComponent({ setup() { state = useUpstreamSites(); return () => h('div') } }))
  await flushPromises()
  return state
}
beforeEach(() => { vi.resetAllMocks(); api.account!.value = { id: 'workspace-a' }; api.list.mockResolvedValue([fixture()]); api.sync.mockRejectedValue(rejection()); api.remove.mockResolvedValue(undefined); api.stream.mockResolvedValue(undefined) })
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('认证状态重取和恢复的并发保护', () => {
  it('reads only after the mutation finishes and accepts connected from another login', async () => {
    const state = await start()
    api.list.mockImplementation(async () => {
      expect(state.pendingSiteOperations.value.has('site')).toBe(false)
      return [fixture()]
    })
    await state.refreshSingleSite('site')
    expect(api.list).toHaveBeenCalledTimes(2)
    expect(api.sync).toHaveBeenCalledTimes(1)
    expect(state.siteSyncStates.value.has('site')).toBe(false)
    expect(state.connectedCount.value).toBe(1)
  })

  it('retains the known failure on failed reads and clears it on an accepted later read', async () => {
    const state = await start()
    api.list.mockRejectedValueOnce(new Error('fixture cache failure'))
    await state.refreshSingleSite('site')
    expect(state.siteSyncStates.value.get('site')?.errorDetail).toContain('页面尚未核实当前状态')
    expect(state.connectedCount.value).toBe(0)
    api.list.mockResolvedValue([fixture()])
    expect(await state.readSiteSnapshot('site')).toBe(true)
    expect(state.siteSyncStates.value.has('site')).toBe(false)
    expect(state.connectedCount.value).toBe(1)
  })

  it('does not apply a late pause over a newer successful login', async () => {
    const state = await start()
    const late = deferred<UpstreamSiteResponse[]>()
    api.list.mockImplementationOnce(() => late.promise)
    const refresh = state.refreshSingleSite('site'); await flushPromises()
    api.update.mockResolvedValue(fixture())
    expect(await state.updateSite('site', form('fixture-new'))).toBe(true)
    late.resolve([paused()]); await refresh
    expect(state.sites.value[0]?.status).toBe('connected')
    expect(state.siteSyncStates.value.has('site')).toBe(false)
    expect(state.connectedCount.value).toBe(1)
  })

  it.each(['workspace', 'delete'] as const)('does not resurrect old authentication state after %s changes', async change => {
    const state = await start()
    const late = deferred<UpstreamSiteResponse[]>()
    api.list.mockImplementationOnce(() => late.promise)
    const refresh = state.refreshSingleSite('site'); await flushPromises()
    const oldError = state.siteSyncStates.value.get('site')
    if (change === 'workspace') { api.list.mockResolvedValue([fixture('other')]); api.account!.value = { id: 'workspace-b' }; await flushPromises() }
    else await state.deleteSite('site')
    late.resolve([paused()]); await refresh
    expect(state.sites.value.some(site => site.id === 'site')).toBe(false)
    if (change === 'workspace') expect(state.siteSyncStates.value.has('site')).toBe(false)
    else {
      // Existing deletion keeps this unrendered map; a late read must not mutate it.
      expect(state.siteSyncStates.value.get('site')).toBe(oldError)
      expect(state.connectedCount.value).toBe(0)
    }
  })

  it.each(['metadata', 'failed', 'skipped'] as const)('does not clear the saved/temporary refusal on %s operations', async action => {
    api.list.mockResolvedValue([paused()])
    const state = await start()
    state.siteSyncStates.value.set('site', { phase: 'error', errorKey: rejectedKey, errorDetail: '原认证提示' })
    if (action === 'failed') {
      api.update.mockRejectedValue(new Error('admin.upstream.errors.loginRejected'))
      expect(await state.updateSite('site', form('fixture-failed'))).toBe(false)
    } else if (action === 'metadata') {
      api.update.mockResolvedValue(paused())
      await state.updateSite('site', form())
    } else {
      const pending = deferred<UpstreamSiteResponse>()
      api.update.mockImplementation(() => pending.promise)
      const update = state.updateSite('site', form())
      expect(await state.readSiteSnapshot('site')).toBe(false)
      pending.resolve(paused()); await update
    }
    expect(state.siteSyncStates.value.get('site')?.errorKey).toBe(rejectedKey)
    expect(state.sites.value[0]?.errorKey).toBe(rejectedKey)
  })
})
