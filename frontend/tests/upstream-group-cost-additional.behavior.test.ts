// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h as element, KeepAlive, ref, type Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import SiteSettingsModal from '@/modules/admin/components/upstream/SiteSettingsModal.vue'
import { useUpstreamSites } from '@/modules/admin/composables/useUpstreamSites'
import { groupCostForDisplay, nextGroupCostDisplayBoundary } from '@/modules/admin/utils/upstream'
import type { UpstreamGroupInfo, UpstreamSite, UpstreamSiteResponse, SyncStreamEvent } from '@/modules/admin/types/upstream'
const api = vi.hoisted(() => ({ list: vi.fn(), stream: vi.fn(), sync: vi.fn(), syncAll: vi.fn(), edit: vi.fn(), settings: vi.fn(), enabled: vi.fn(), remove: vi.fn(), create: vi.fn(), account: undefined as unknown as Ref<{ id: string } | null> }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', () => ({ useAdminAccounts: () => ({ currentAccount: api.account }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 60 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: api.list, streamSyncAllUpstreamSites: api.stream, syncUpstreamSite: api.sync, syncAllUpstreamSites: api.syncAll, updateUpstreamSite: api.edit, updateSiteSettings: api.settings, updateUpstreamSiteEnabled: api.enabled, removeUpstreamSite: api.remove, createUpstreamSite: api.create }))
const observed = '2031-02-03T04:00:00Z'
const group = (amount: number | null = 3): UpstreamGroupInfo => ({ id: '1', name: '费用分组', platform: 'openai', multiplier: 1, multiplierDisplay: '1', todayCost: amount, costMode: amount == null ? 'unknown' : 'exact', costObservedAt: amount == null ? null : observed, costSource: 'group_summary', costComplete: true })
const site = (id = 'a', amount: number | null = 3): UpstreamSiteResponse => ({ id, name: `站点${id}`, baseUrl: 'https://fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', account: 'fixture', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, settings: { balanceThreshold: 5 }, lastSyncedAt: Date.parse(observed), metrics: { balance: { value: 10, display: '10' }, todayConsume: { value: 3, display: '3' }, historyRecharge: { value: 10, display: '10' }, group: group(amount), groups: [group(amount)], todayConsumeDate: '2031-02-03', todayConsumeTimezone: 'Asia/Singapore', todayConsumeStatus: 'ok' } })
const deferred = <T,>() => { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((r, j) => { resolve = r; reject = j }); return { promise, resolve, reject } }
const wrappers: VueWrapper[] = []
const stubs = { Teleport: true, Tooltip: { template: '<span><slot /></span>' } }
const view = async () => { const w = mount(UpstreamView, { global: { stubs } }); wrappers.push(w); await flushPromises(); return w }
const open = async (w: VueWrapper, index = 0) => { await w.findAll('button').filter(b => b.text().includes('可用分组'))[index]!.trigger('click'); await flushPromises() }
const dialog = (w: VueWrapper) => w.get('[role="dialog"]')
const close = async (w: VueWrapper) => { await dialog(w).findAll('button').find(b => b.text() === '关闭')!.trigger('click'); await flushPromises() }
const service = async () => { let s!: ReturnType<typeof useUpstreamSites>; const w = mount(defineComponent({ setup() { s = useUpstreamSites(); return () => element('div') } })); wrappers.push(w); await flushPromises(); return s }
const form = { name: '编辑后', siteUrl: 'https://fixture.invalid', platform: 'sub2api' as const, authMode: 'password' as const, account: 'fixture', password: '', accessToken: '', refreshToken: '', tokenType: 'Bearer', userId: '', rechargeRate: 1, remark: '' }
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2031-02-03T04:05:00Z')); vi.resetAllMocks(); api.account = ref({ id: 'workspace-a' }); api.list.mockResolvedValue([site()]); api.stream.mockResolvedValue(undefined); api.settings.mockResolvedValue(site()); localStorage.clear(); Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.clearAllTimers(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = ''; Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })

describe('费用缓存读取的归属和旧入口保护', () => {
  it('补读只合并本站并保留倍率警告；真实同步无警告继续清除', async () => {
    const original = site(); original.groupRatesWarning = { message: '倍率保存未确认', detail: '连接中断', next: '请检查数据库' }
    api.list.mockResolvedValueOnce([original, site('b')]).mockResolvedValue([site('a', 9)])
    const s = await service(); await s.readSiteSnapshot('a')
    expect(s.sites.value.map(item => item.id)).toEqual(['a', 'b'])
    expect(s.sites.value[0]!.metrics.groups[0]!.todayCost).toBe(9)
    expect(s.sites.value[0]!.groupRatesWarning?.detail).toBe('连接中断')
    api.sync.mockResolvedValue(site('a', 10)); await s.syncSite('a')
    expect(s.sites.value[0]!.groupRatesWarning).toBeUndefined()
  })
  it.each(['edit', 'enabled', 'remove', 'settings'] as const)('在 %s 开始前发出的费用读迟到不能回写', async kind => {
    const s = await service(); const late = deferred<UpstreamSiteResponse[]>(); api.list.mockReturnValueOnce(late.promise)
    const read = s.readSiteSnapshot('a')
    if (kind === 'edit') { api.edit.mockResolvedValue({ ...site(), name: '编辑后' }); await s.updateSite('a', form) }
    if (kind === 'enabled') { api.enabled.mockResolvedValue({ ...site(), enabled: false }); await s.setSiteEnabled('a', false) }
    if (kind === 'remove') { api.remove.mockResolvedValue(undefined); await s.deleteSite('a') }
    if (kind === 'settings') await s.saveSiteSettings('a', { balanceThreshold: 22 })
    late.resolve([site('a', 99)]); expect(await read).toBe(false)
    expect(s.sites.value[0]?.metrics.groups[0]?.todayCost).not.toBe(99)
    if (kind === 'edit') expect(s.sites.value[0]!.name).toBe('编辑后')
    if (kind === 'enabled') expect(s.sites.value[0]!.enabled).toBe(false)
    if (kind === 'remove') expect(s.sites.value).toHaveLength(0)
    if (kind === 'settings') expect(s.sites.value[0]!.settings.balanceThreshold).toBe(22)
  })
  it('操作失败也使旧补读失效，并保留原设置及金额', async () => {
    const s = await service(); const late = deferred<UpstreamSiteResponse[]>(); api.list.mockReturnValueOnce(late.promise)
    const read = s.readSiteSnapshot('a'); api.settings.mockRejectedValueOnce(new Error('保存连接中断'))
    await expect(s.saveSiteSettings('a', { balanceThreshold: 22 })).rejects.toThrow('保存连接中断')
    late.resolve([site('a', 99)]); expect(await read).toBe(false)
    expect(s.sites.value[0]!.settings.balanceThreshold).toBe(5)
    expect(s.sites.value[0]!.metrics.groups[0]!.todayCost).toBe(3)
  })
  it('操作中不启动费用读，结束后已完成的费用读不会改变其他字段', async () => {
    const s = await service(); const patch = deferred<UpstreamSiteResponse>(); api.settings.mockReturnValueOnce(patch.promise)
    const save = s.saveSiteSettings('a', { balanceThreshold: 22 }); const calls = api.list.mock.calls.length
    expect(await s.readSiteSnapshot('a')).toBe(false); expect(api.list).toHaveBeenCalledTimes(calls)
    patch.resolve({ ...site('a', 99), name: '旧整站响应' }); await save
    expect(s.sites.value[0]!.name).toBe('站点a'); expect(s.sites.value[0]!.metrics.groups[0]!.todayCost).toBe(3)
    expect(s.sites.value[0]!.settings.balanceThreshold).toBe(22)
  })
  it('较早操作的 finally 不会作废较新操作结果', async () => {
    const s = await service(); const old = deferred<UpstreamSiteResponse>(); const newer = deferred<UpstreamSiteResponse>()
    api.settings.mockReturnValueOnce(old.promise); api.edit.mockReturnValueOnce(newer.promise)
    const save = s.saveSiteSettings('a', { balanceThreshold: 22 }); const edit = s.updateSite('a', form)
    old.resolve(site()); await save
    newer.resolve({ ...site(), name: '最新编辑' }); expect(await edit).toBe(true)
    expect(s.sites.value[0]!.name).toBe('最新编辑'); expect(s.pendingSiteOperations.value.size).toBe(0)
  })
  // 主代理冻结第2轮关键回归：独立字段成功均须保留，终态只释放自己一次。
  it.each((['edit', 'enabled'] as const).flatMap(kind => (['settings-first', 'action-first'] as const).flatMap(start =>
    (['settings-first', 'action-first'] as const).map(finish => ({ kind, start, finish })),
  )))('设置与 $kind：$start 发起、$finish 完成都保留各自成功字段', async ({ kind, start, finish }) => {
    const s = await service(); const settings = deferred<UpstreamSiteResponse>(); const action = deferred<UpstreamSiteResponse>()
    api.settings.mockReturnValueOnce(settings.promise); api.edit.mockReturnValueOnce(action.promise); api.enabled.mockReturnValueOnce(action.promise)
    let saving!: Promise<void>; let acting!: Promise<unknown>
    const startSettings = () => { saving = s.saveSiteSettings('a', { balanceThreshold: 22 }) }
    const startAction = () => { acting = kind === 'edit' ? s.updateSite('a', form) : s.setSiteEnabled('a', false) }
    if (start === 'settings-first') { startSettings(); startAction() } else { startAction(); startSettings() }
    const endSettings = async () => { settings.resolve(site()); await saving }
    const endAction = async () => { action.resolve({ ...site(), name: kind === 'edit' ? '独立编辑成功' : '站点a', enabled: kind !== 'enabled' }); await acting }
    if (finish === 'settings-first') { await endSettings(); await endAction() } else { await endAction(); await endSettings() }
    expect(s.sites.value[0]!.settings.balanceThreshold).toBe(22)
    expect(s.sites.value[0]!.name).toBe(kind === 'edit' ? '独立编辑成功' : '站点a')
    expect(s.sites.value[0]!.enabled).toBe(kind !== 'enabled'); expect(s.pendingSiteOperations.value.size).toBe(0)
    expect(api.settings).toHaveBeenCalledOnce(); expect(kind === 'edit' ? api.edit : api.enabled).toHaveBeenCalledOnce()
  })
  it.each((['edit-first', 'enabled-first'] as const).flatMap(start => (['edit-first', 'enabled-first'] as const).map(finish => ({ start, finish }))))('编辑与启停：$start 发起、$finish 完成都保留各自字段', async ({ start, finish }) => {
    const s = await service(); const editResult = deferred<UpstreamSiteResponse>(); const enabledResult = deferred<UpstreamSiteResponse>()
    api.edit.mockReturnValueOnce(editResult.promise); api.enabled.mockReturnValueOnce(enabledResult.promise)
    let editing!: Promise<boolean>; let enabling!: Promise<void>
    const startEdit = () => { editing = s.updateSite('a', form) }; const startEnabled = () => { enabling = s.setSiteEnabled('a', false) }
    if (start === 'edit-first') { startEdit(); startEnabled() } else { startEnabled(); startEdit() }
    const endEdit = async () => { editResult.resolve({ ...site(), name: '独立编辑成功' }); expect(await editing).toBe(true) }
    const endEnabled = async () => { enabledResult.resolve({ ...site(), enabled: false }); await enabling }
    if (finish === 'edit-first') { await endEdit(); await endEnabled() } else { await endEnabled(); await endEdit() }
    expect(s.sites.value[0]!.name).toBe('独立编辑成功'); expect(s.sites.value[0]!.enabled).toBe(false)
    expect(s.sites.value[0]!.settings.balanceThreshold).toBe(5); expect(s.pendingSiteOperations.value.size).toBe(0)
  })
  it.each((['edit', 'settings', 'enabled', 'remove'] as const).flatMap(kind => (['done', 'error'] as const).map(event => ({ kind, event }))))('SSE先发后 $kind 完成，旧 $event 立即释放本站而其他站仍在同步', async ({ kind, event }) => {
    api.list.mockResolvedValueOnce([site(), site('b')]); const s = await service(); const stream = deferred<void>(); let emit!: (event: SyncStreamEvent) => void
    api.stream.mockImplementationOnce((callback: typeof emit) => { emit = callback; return stream.promise }); const syncing = s.streamRefreshSites()
    try {
      emit({ event: 'syncing', siteId: 'a' }); expect(s.siteSyncStates.value.get('a')?.phase).toBe('syncing')
      if (kind === 'edit') { api.edit.mockResolvedValueOnce({ ...site(), name: '独立编辑成功' }); await s.updateSite('a', form) }
      if (kind === 'settings') await s.saveSiteSettings('a', { balanceThreshold: 22 })
      if (kind === 'enabled') { api.enabled.mockResolvedValueOnce({ ...site(), enabled: false }); await s.setSiteEnabled('a', false) }
      if (kind === 'remove') { api.remove.mockResolvedValueOnce(undefined); await s.deleteSite('a') }
      expect(s.pendingSiteOperations.value.has('a')).toBe(true)
      emit({ event, siteId: 'a', site: { ...site('a', 99), name: '旧SSE对象' } })
      expect(s.pendingSiteOperations.value.has('a')).toBe(false); expect(s.pendingSiteOperations.value.has('b')).toBe(true)
      expect(s.siteSyncStates.value.get('a')?.phase).not.toBe('syncing')
      expect(s.sites.value.find(item => item.id === 'a')?.name).not.toBe('旧SSE对象')
      const current = s.sites.value.find(item => item.id === 'a')
      if (kind === 'remove') expect(current).toBeUndefined()
      else {
        if (kind === 'edit') expect(current!.name).toBe('独立编辑成功')
        if (kind === 'settings') expect(current!.settings.balanceThreshold).toBe(22)
        if (kind === 'enabled') expect(current!.enabled).toBe(false)
        api.list.mockResolvedValueOnce([{ ...current!, metrics: { ...current!.metrics, groups: [group(8)] } }]); expect(await s.readSiteSnapshot('a')).toBe(true)
        expect(s.sites.value.find(item => item.id === 'a')!.metrics.groups[0]!.todayCost).toBe(8)
      }
      const next = deferred<UpstreamSiteResponse>(); api.settings.mockReturnValueOnce(next.promise); const newer = s.saveSiteSettings('b', { balanceThreshold: 33 })
      emit({ event, siteId: 'a', site: site('a', 99) }); emit({ event: 'complete', siteId: '' })
      expect(s.pendingSiteOperations.value.has('b')).toBe(true)
      next.resolve(site('b')); await newer; expect(s.sites.value.find(item => item.id === 'b')!.settings.balanceThreshold).toBe(33)
      expect(s.pendingSiteOperations.value.size).toBe(0)
    } finally { stream.resolve(); await syncing }
  })
  it.each(['edit', 'settings', 'enabled'] as const)('单站同步先发后 %s 成功，旧失败不贴回当前站点', async kind => {
    const s = await service(); const old = deferred<UpstreamSiteResponse>(); api.sync.mockReturnValueOnce(old.promise)
    const syncing = s.refreshSingleSite('a')
    if (kind === 'edit') { api.edit.mockResolvedValueOnce({ ...site(), name: '独立编辑成功' }); await s.updateSite('a', form) }
    if (kind === 'settings') await s.saveSiteSettings('a', { balanceThreshold: 22 })
    if (kind === 'enabled') { api.enabled.mockResolvedValueOnce({ ...site(), enabled: false }); await s.setSiteEnabled('a', false) }
    old.reject(Object.assign(new Error('request'), { failure: { errorDetail: '旧同步连接中断' } })); await syncing
    expect(s.siteSyncStates.value.get('a')?.phase).not.toBe('error')
    expect(s.siteSyncStates.value.get('a')?.errorDetail).not.toBe('旧同步连接中断')
    if (kind === 'edit') expect(s.sites.value[0]!.name).toBe('独立编辑成功')
    if (kind === 'settings') expect(s.sites.value[0]!.settings.balanceThreshold).toBe(22)
    if (kind === 'enabled') expect(s.sites.value[0]!.enabled).toBe(false)
    expect(s.pendingSiteOperations.value.size).toBe(0); expect(s.syncingSiteIds.value.size).toBe(0)
  })
  it('当前单站同步失败仍显示具体原因并释放占用', async () => {
    const s = await service(); api.sync.mockRejectedValueOnce(Object.assign(new Error('request'), { failure: { errorDetail: '本站同步连接中断' } }))
    await s.refreshSingleSite('a')
    expect(s.siteSyncStates.value.get('a')?.phase).toBe('error'); expect(s.siteSyncStates.value.get('a')?.errorDetail).toBe('本站同步连接中断')
    expect(s.pendingSiteOperations.value.size).toBe(0); expect(s.syncingSiteIds.value.size).toBe(0)
  })
  it('迟到初始列表不能覆盖期间已经完成的 SSE 费用或恢复删除对象', async () => {
    const initial = deferred<UpstreamSiteResponse[]>(); api.list.mockReturnValueOnce(initial.promise)
    let s!: ReturnType<typeof useUpstreamSites>; const w = mount(defineComponent({ setup() { s = useUpstreamSites(); return () => element('div') } })); wrappers.push(w)
    api.stream.mockImplementationOnce(async (emit: (e: SyncStreamEvent) => void) => emit({ event: 'done', siteId: 'a', site: site('a', 8) }))
    await s.streamRefreshSites(); initial.resolve([site('a', 1)]); await flushPromises()
    expect(s.sites.value[0]!.metrics.groups[0]!.todayCost).toBe(8)
    const reload = deferred<UpstreamSiteResponse[]>(); api.list.mockReturnValueOnce(reload.promise)
    const loading = s.loadSites(); api.remove.mockResolvedValue(undefined); await s.deleteSite('a')
    reload.resolve([site('a', 1)]); await loading; expect(s.sites.value).toHaveLength(0)
  })
  it('SSE complete 保持原立即完成行为，旧流 finally 不清新流刷新状态', async () => {
    const s = await service(); const oldStream = deferred<void>(); const newStream = deferred<void>()
    let finish!: (event: SyncStreamEvent) => void
    api.stream.mockImplementationOnce((emit: typeof finish) => { finish = emit; return oldStream.promise })
    const old = s.streamRefreshSites(); expect(s.pendingSiteOperations.value.has('a')).toBe(true)
    finish({ event: 'complete', siteId: '' })
    expect(s.isRefreshing.value).toBe(false); expect(s.pendingSiteOperations.value.size).toBe(0)
    api.stream.mockReturnValueOnce(newStream.promise); const next = s.streamRefreshSites()
    oldStream.resolve(); await old
    expect(s.isRefreshing.value).toBe(true); expect(s.pendingSiteOperations.value.has('a')).toBe(true)
    newStream.resolve(); await next
    expect(s.isRefreshing.value).toBe(false); expect(s.pendingSiteOperations.value.size).toBe(0)
  })
  it('迟到全量同步不能覆盖随后编辑', async () => {
    const s = await service(); const all = deferred<UpstreamSiteResponse[]>(); api.syncAll.mockReturnValueOnce(all.promise)
    const sync = s.refreshSites(); api.edit.mockResolvedValue({ ...site(), name: '新编辑' }); await s.updateSite('a', form)
    all.resolve([{ ...site('a', 99), name: '旧全量' }]); await sync
    expect(s.sites.value[0]!.name).toBe('新编辑'); expect(s.sites.value[0]!.metrics.groups[0]!.todayCost).toBe(3)
  })
  it.each((['edit', 'enabled', 'remove', 'settings'] as const).flatMap(kind => (['direct', 'button'] as const).map(sync => ({ kind, sync }))))('独立 $kind 进行时，$sync 单站同步不抢所有权或清掉旧反馈', async ({ kind, sync }) => {
    const s = await service()
    const mutation = deferred<UpstreamSiteResponse>()
    api.edit.mockReturnValueOnce(mutation.promise); api.enabled.mockReturnValueOnce(mutation.promise)
    api.remove.mockReturnValueOnce(mutation.promise); api.settings.mockReturnValueOnce(mutation.promise)
    const modifying = kind === 'edit' ? s.updateSite('a', form) : kind === 'enabled' ? s.setSiteEnabled('a', false) : kind === 'remove' ? s.deleteSite('a') : s.saveSiteSettings('a', { balanceThreshold: 22 })
    s.siteSyncStates.value.set('a', { phase: 'error', errorDetail: '既有读取说明' })
    if (sync === 'direct') await s.syncSite('a'); else await s.refreshSingleSite('a')
    expect(api.sync).not.toHaveBeenCalled()
    expect(s.siteSyncStates.value.get('a')?.errorDetail).toBe('既有读取说明')
    mutation.resolve({ ...site(), name: '编辑后', enabled: false }); await modifying
    if (kind === 'edit') expect(s.sites.value[0]!.name).toBe('编辑后')
    if (kind === 'enabled') expect(s.sites.value[0]!.enabled).toBe(false)
    if (kind === 'remove') expect(s.sites.value).toHaveLength(0)
    if (kind === 'settings') expect(s.sites.value[0]!.settings.balanceThreshold).toBe(22)
    expect(s.pendingSiteOperations.value.size).toBe(0)
  })
  it.each((['settings', 'enabled', 'edit', 'remove'] as const).flatMap(kind => (['done', 'error'] as const).map(event => ({ kind, event }))))('初始列表未到时启动流，补读和 $kind 完成后，晚到 $event 不首次认领旧对象', async ({ kind, event }) => {
    const initial = deferred<UpstreamSiteResponse[]>(); api.list.mockReturnValueOnce(initial.promise)
    const s = await service(); expect(s.sites.value).toHaveLength(0)
    const stream = deferred<void>(); let emit!: (event: SyncStreamEvent) => void
    api.stream.mockImplementationOnce((callback: typeof emit) => { emit = callback; return stream.promise })
    const syncing = s.streamRefreshSites()
    try {
      initial.resolve([site()]); await flushPromises()
      api.list.mockResolvedValueOnce([site('a', 4)]); expect(await s.readSiteSnapshot('a')).toBe(true)
      if (kind === 'settings') await s.saveSiteSettings('a', { balanceThreshold: 22 })
      if (kind === 'enabled') { api.enabled.mockResolvedValueOnce({ ...site(), enabled: false }); await s.setSiteEnabled('a', false) }
      if (kind === 'edit') { api.edit.mockResolvedValueOnce({ ...site(), name: '独立编辑成功' }); expect(await s.updateSite('a', form)).toBe(true) }
      if (kind === 'remove') { api.remove.mockResolvedValueOnce(undefined); await s.deleteSite('a') }
      emit({ event, siteId: 'a', site: { ...site('a', 99), name: '旧初始流对象' } })
      const current = s.sites.value.find(item => item.id === 'a')
      expect(current?.name).not.toBe('旧初始流对象'); expect(current?.metrics.groups[0]?.todayCost).not.toBe(99)
      if (kind === 'settings') expect(current!.settings.balanceThreshold).toBe(22)
      if (kind === 'enabled') expect(current!.enabled).toBe(false)
      if (kind === 'edit') expect(current!.name).toBe('独立编辑成功')
      if (kind === 'remove') expect(current).toBeUndefined()
      expect(s.pendingSiteOperations.value.has('a')).toBe(false)
    } finally {
      emit({ event: 'complete', siteId: '' }); stream.resolve(); await syncing
    }
  })
  // 主代理冻结 Review 并发业务期望；实现不得放宽这些断言。
  it.each((['edit', 'enabled', 'remove', 'settings'] as const).flatMap(kind =>
    (['all', 'stream'] as const).flatMap(sync => (['mutation-first', 'sync-first'] as const).map(order => ({ kind, sync, order }))),
  ))('$kind 先开始后 $sync 同步，$order 完成顺序都保留实际修改', async ({ kind, sync, order }) => {
    const s = await service(); const mutation = deferred<UpstreamSiteResponse>(); const all = deferred<UpstreamSiteResponse[]>(); const stream = deferred<void>()
    let emit!: (event: SyncStreamEvent) => void
    api.edit.mockReturnValueOnce(mutation.promise); api.enabled.mockReturnValueOnce(mutation.promise)
    api.remove.mockReturnValueOnce(mutation.promise); api.settings.mockReturnValueOnce(mutation.promise)
    const operation = kind === 'edit' ? s.updateSite('a', form) : kind === 'enabled' ? s.setSiteEnabled('a', false) : kind === 'remove' ? s.deleteSite('a') : s.saveSiteSettings('a', { balanceThreshold: 22 })
    api.syncAll.mockReturnValueOnce(all.promise); api.stream.mockImplementationOnce((callback: typeof emit) => { emit = callback; return stream.promise })
    const syncing = sync === 'all' ? s.refreshSites() : s.streamRefreshSites()
    const finishSync = async () => {
      if (sync === 'all') all.resolve([{ ...site('a', 99), name: '旧同步对象' }])
      else { emit({ event: 'done', siteId: 'a', site: { ...site('a', 99), name: '旧同步对象' } }); emit({ event: 'complete', siteId: '' }); stream.resolve() }
      await syncing
    }
    const finishMutation = async () => { mutation.resolve({ ...site(), name: '编辑后', enabled: kind !== 'enabled' }); await operation }
    if (order === 'mutation-first') { await finishMutation(); await finishSync() } else { await finishSync(); await finishMutation() }
    if (kind === 'remove') expect(s.sites.value).toHaveLength(0)
    else {
      expect(s.sites.value[0]!.metrics.groups[0]!.todayCost).toBe(3); expect(s.sites.value[0]!.name).not.toBe('旧同步对象')
      if (kind === 'edit') expect(s.sites.value[0]!.name).toBe('编辑后')
      if (kind === 'enabled') expect(s.sites.value[0]!.enabled).toBe(false)
      if (kind === 'settings') expect(s.sites.value[0]!.settings.balanceThreshold).toBe(22)
    }
    expect(s.pendingSiteOperations.value.size).toBe(0)
  })
  it.each(['all', 'stream', 'edit'] as const)('删除先发，后发 %s 失败不丢弃已成功删除', async kind => {
    const s = await service(); const deletion = deferred<void>(); api.remove.mockReturnValueOnce(deletion.promise)
    const deleting = s.deleteSite('a')
    if (kind === 'all') { api.syncAll.mockRejectedValueOnce(new Error('同步中断')); await expect(s.refreshSites()).rejects.toThrow('同步中断') }
    if (kind === 'stream') { api.stream.mockRejectedValueOnce(new Error('同步中断')); await s.streamRefreshSites() }
    if (kind === 'edit') { api.edit.mockRejectedValueOnce(new Error('保存中断')); expect(await s.updateSite('a', form)).toBe(false) }
    deletion.resolve(); await deleting
    expect(s.sites.value).toHaveLength(0); expect(s.pendingSiteOperations.value.size).toBe(0)
    api.list.mockResolvedValueOnce([site('a', 99)]); await s.loadSites()
    expect(s.sites.value).toHaveLength(0)
  })
  it('切换工作区清掉旧读与旧保存的本地应用，旧 finally 不释放新工作区操作', async () => {
    const s = await service(); const oldSave = deferred<UpstreamSiteResponse>(); api.settings.mockReturnValueOnce(oldSave.promise)
    const savingOld = s.saveSiteSettings('a', { balanceThreshold: 22 })
    api.list.mockResolvedValue([{ ...site('a', 8), name: '新工作区' }]); api.account.value = { id: 'workspace-b' }; await flushPromises()
    const nextSave = deferred<UpstreamSiteResponse>(); api.settings.mockReturnValueOnce(nextSave.promise)
    const savingNext = s.saveSiteSettings('a', { balanceThreshold: 33 })
    oldSave.resolve(site()); await savingOld
    expect(s.pendingSiteOperations.value.has('a')).toBe(true)
    expect(s.sites.value[0]!.name).toBe('新工作区'); expect(s.sites.value[0]!.settings.balanceThreshold).toBe(5)
    nextSave.resolve(site()); await savingNext
    expect(s.sites.value[0]!.settings.balanceThreshold).toBe(33); expect(s.pendingSiteOperations.value.size).toBe(0)
  })
})

describe('弹窗补读、原因和生命周期', () => {
  it('读取未完成时不会重叠，关闭取消且不再请求', async () => {
    const late = deferred<UpstreamSiteResponse[]>(); api.list.mockResolvedValueOnce([site()]).mockReturnValueOnce(late.promise)
    const w = await view(); await open(w); const controllerSignal = api.list.mock.calls[1]![1] as AbortSignal
    await vi.advanceTimersByTimeAsync(20_000); expect(api.list).toHaveBeenCalledTimes(2)
    await close(w); expect(controllerSignal.aborted).toBe(true)
    late.resolve([site('a', 99)]); await flushPromises(); await vi.advanceTimersByTimeAsync(40_000)
    expect(api.list).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
  })
  it('隐藏时停止，恢复可见立即补读，卸载回收计时器', async () => {
    const w = await view(); await open(w); expect(api.list).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    await vi.advanceTimersByTimeAsync(20_000); expect(api.list).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }); document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(api.list).toHaveBeenCalledTimes(3); w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); expect(vi.getTimerCount()).toBe(0)
  })
  it('KeepAlive 失活停止，恢复立即补读', async () => {
    const show = ref(true)
    const w = mount(defineComponent({ setup: () => () => element(KeepAlive, null, { default: () => show.value ? element(UpstreamView) : element('p') }) }), { global: { stubs } }); wrappers.push(w)
    await flushPromises(); await open(w); expect(api.list).toHaveBeenCalledTimes(2)
    show.value = false; await flushPromises(); await vi.advanceTimersByTimeAsync(20_000); expect(api.list).toHaveBeenCalledTimes(2)
    show.value = true; await flushPromises(); expect(api.list).toHaveBeenCalledTimes(3)
  })
  it('失败以十、二十、三十秒退避，成功恢复五秒；具体原因可见且已脱敏', async () => {
    const failure = Object.assign(new Error('admin.upstream.errors.request'), { failure: { errorDetail: '缓存连接中断 token=fixture-secret-value' } })
    api.list.mockResolvedValueOnce([site()]).mockRejectedValueOnce(failure).mockRejectedValueOnce(failure).mockRejectedValueOnce(failure).mockResolvedValue([site('a', 6)])
    const w = await view(); await open(w)
    expect(dialog(w).text()).toContain('缓存连接中断'); expect(dialog(w).text()).not.toContain('fixture-secret-value'); expect(dialog(w).text()).toContain('今日费用 3.00')
    await vi.advanceTimersByTimeAsync(9_999); expect(api.list).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1); expect(api.list).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(19_999); expect(api.list).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(1); expect(api.list).toHaveBeenCalledTimes(4)
    await vi.advanceTimersByTimeAsync(29_999); expect(api.list).toHaveBeenCalledTimes(4)
    await vi.advanceTimersByTimeAsync(1); await flushPromises(); expect(api.list).toHaveBeenCalledTimes(5); expect(dialog(w).text()).not.toContain('缓存连接中断')
    await vi.advanceTimersByTimeAsync(5_000); expect(api.list).toHaveBeenCalledTimes(6)
  })
  it('共同原因只显示一次，个别组原因直接可见且不覆盖已有上游原因', async () => {
    const original = site('a', null)
    original.metrics.groups = [1, 2, 3].map(id => ({ ...group(null), id: String(id), name: `分组${id}`, costReason: id === 3 ? '读取分组费用记录失败：该组缓存格式损坏' : '上游费用读取失败：费用返回缺少日期' }))
    api.list.mockResolvedValue([original]); const w = await view(); await open(w)
    const visibleText = dialog(w).text()
    expect(visibleText.split('上游费用读取失败：费用返回缺少日期')).toHaveLength(2)
    expect(visibleText).toContain('该组缓存格式损坏')
    expect(dialog(w).findAll('button').find(b => b.text().includes('分组3'))!.text()).toContain('该组缓存格式损坏')
  })
  it('工作区切换关闭弹窗、清错误并只加载缓存，不额外同步全站', async () => {
    const late = deferred<UpstreamSiteResponse[]>(); api.list.mockResolvedValueOnce([site()]).mockReturnValueOnce(late.promise)
    const w = await view(); await open(w)
    api.list.mockResolvedValue([site('b', 8)]); api.account.value = { id: 'workspace-b' }; await flushPromises()
    late.resolve([site('a', 99)]); await flushPromises()
    expect(w.find('[role="dialog"]').exists()).toBe(false); expect(w.text()).toContain('站点b'); expect(w.text()).not.toContain('站点a')
    expect(api.stream).toHaveBeenCalledTimes(1)
  })
  it('删除当前站点时关闭弹窗并显示具体说明', async () => {
    const w = await view(); await open(w)
    // The same list object used by the modal is removed via the real composable.
    api.remove.mockResolvedValue(undefined)
    const card = w.get('div.group.bg-card'); await card.findAll('button').find(b => b.find('svg.lucide-trash-2').exists())!.trigger('click'); await flushPromises()
    const confirm = w.findAll('button').find(b => b.text() === '确认删除')!
    expect(confirm).toBeDefined(); await confirm.trigger('click'); await flushPromises()
    expect(w.text()).toContain('这个站点已删除'); expect(w.text()).not.toContain('今日费用 3.00')
  })
  it.each((['ninety', 'midnight'] as const).flatMap(boundary => (['retained', 'running'] as const).map(reason => ({ boundary, reason }))))('补读失败越过 $boundary 时 $reason 旧说明明确标为历史且不与当前未知冲突', async ({ boundary, reason }) => {
    const original = site(); const g = original.metrics.groups[0]!
    g.costReason = reason === 'retained' ? '沿用截至 2031-02-03 12:00:00 的费用。上游拒绝读取：请先确认公告；请到上游处理后同步这个站点。' : '当前显示截至 2031-02-03 12:00:00 的费用，系统正在读取新的费用。'
    if (reason === 'retained') g.costMode = 'retained'
    if (boundary === 'ninety') vi.setSystemTime(new Date('2031-02-03T05:29:59Z'))
    else { vi.setSystemTime(new Date('2031-02-03T15:59:59Z')); g.costObservedAt = '2031-02-03T15:59:00Z' }
    api.list.mockResolvedValueOnce([original]).mockRejectedValue(Object.assign(new Error('request'), { failure: { errorDetail: '缓存连接中断' } }))
    const w = await view(); await open(w); expect(dialog(w).text()).toContain('今日费用 3.00')
    await vi.advanceTimersByTimeAsync(1_002); await flushPromises()
    const text = dialog(w).text(); expect(text).toContain('今日费用暂无法读取'); expect(text).not.toContain('今日费用 3.00')
    expect(text).toContain('历史说明：'); expect(text.split('历史说明：')[0]).not.toMatch(/沿用截至|当前显示截至/)
    expect(text).toContain(g.costReason)
    expect(text).toContain(boundary === 'ninety' ? '超过九十分钟' : '不属于新加坡今天')
    if (reason === 'retained') expect(text).toContain('请先确认公告')
    expect(api.list).toHaveBeenCalledTimes(2)
  })
  it.each(['fifteen', 'ninety', 'midnight'] as const)('补读持续失败时 %s 边界会纯显示降级或撤下旧值及未归属提示', async boundary => {
    const original = site(); const g = original.metrics.groups[0]!
    g.costComplete = false; g.unattributedCost = 0.4; g.siteReportedCost = 3.4; g.groupAttributedCost = 3
    if (boundary === 'fifteen') vi.setSystemTime(new Date('2031-02-03T04:14:59Z'))
    if (boundary === 'ninety') vi.setSystemTime(new Date('2031-02-03T05:29:59Z'))
    if (boundary === 'midnight') { vi.setSystemTime(new Date('2031-02-03T15:59:59Z')); g.costObservedAt = '2031-02-03T15:59:00Z' }
    api.list.mockResolvedValueOnce([original]).mockRejectedValue(Object.assign(new Error('request'), { failure: { errorDetail: '缓存连接中断' } }))
    const w = await view(); await open(w); expect(dialog(w).text()).toContain('今日费用 3.00')
    await vi.advanceTimersByTimeAsync(1_002); await flushPromises()
    const text = dialog(w).text()
    expect(text).toContain('缓存连接中断')
    if (boundary === 'fifteen') { expect(text).toContain('(沿用)'); expect(text).toContain('今日费用 3.00') }
    else { expect(text).toContain('今日费用暂无法读取'); expect(text).not.toContain('今日费用 3.00'); expect(text).not.toContain('未完全归属'); expect(text).not.toContain('无法对应当前可用分组'); expect(text).toContain(boundary === 'ninety' ? '超过九十分钟' : '不属于新加坡今天') }
    expect(api.list).toHaveBeenCalledTimes(2)
  })
})

describe('设置弹窗保存对象与显示反馈', () => {
  it.each(['success', 'failure'] as const)('A 保存迟到 %s 不标记之后打开的B，捕获原ID和输入', async outcome => {
    const saving = deferred<void>(); const save = vi.fn(() => saving.promise)
    const w = mount(SiteSettingsModal, { props: { open: true, site: { ...site(), logo: 'A', logoBg: '' } as UpstreamSite, saveSettings: save }, global: { stubs } }); wrappers.push(w)
    await w.findAll('button').find(b => b.text() === '保存')!.trigger('click')
    expect(save).toHaveBeenCalledWith('a', { balanceThreshold: 5 })
    await w.setProps({ open: false }); await w.setProps({ open: true, site: { ...site('b'), logo: 'B', logoBg: '' } as UpstreamSite })
    if (outcome === 'success') saving.resolve(); else saving.reject(new Error('A站保存失败'))
    await flushPromises()
    expect(w.text()).toContain('站点b'); expect(w.text()).not.toContain('已保存'); expect(w.text()).not.toContain('A站保存失败')
    if (outcome === 'success') expect(w.emitted('saved')?.[0]).toEqual(['a', { balanceThreshold: 5 }])
    expect(vi.getTimerCount()).toBe(0)
  })
  it('同站费用对象更新不重置尚未保存的设置草稿，失败保留输入', async () => {
    const save = vi.fn().mockRejectedValue(new Error('保存连接中断'))
    const w = mount(SiteSettingsModal, { props: { open: true, site: { ...site(), logo: 'A', logoBg: '' } as UpstreamSite, saveSettings: save }, global: { stubs } }); wrappers.push(w)
    await w.get('input[type="number"]').setValue('17')
    await w.setProps({ site: { ...site('a', 9), settings: { balanceThreshold: 99 }, logo: 'A', logoBg: '' } as UpstreamSite })
    expect((w.get('input[type="number"]').element as HTMLInputElement).value).toBe('17')
    await w.findAll('button').find(b => b.text() === '保存')!.trigger('click'); await flushPromises()
    expect(save).toHaveBeenCalledWith('a', { balanceThreshold: 17 }); expect(w.text()).toContain('保存连接中断')
    expect((w.get('input[type="number"]').element as HTMLInputElement).value).toBe('17')
  })
})

describe('新加坡当天费用显示门禁', () => {
  it('十五与九十分钟沿用包含边界，原 retained/unknown 不升级，确认零可读且不写原对象', () => {
    const now = Date.parse(observed)
    expect(groupCostForDisplay(group(0), now).todayCost).toBe(0)
    expect(groupCostForDisplay(group(), now + 15 * 60_000).costMode).toBe('exact')
    expect(groupCostForDisplay(group(), now + 15 * 60_000 + 1).costMode).toBe('retained')
    expect(groupCostForDisplay(group(), now + 90 * 60_000).todayCost).toBe(3)
    expect(groupCostForDisplay(group(), now + 90 * 60_000 + 1).todayCost).toBeNull()
    expect(groupCostForDisplay({ ...group(), costMode: 'retained' }, now).costMode).toBe('retained')
    expect(groupCostForDisplay({ ...group(), costMode: 'unknown' }, now).todayCost).toBeNull()
    const original = group(); groupCostForDisplay(original, now + 100 * 60_000); expect(original.todayCost).toBe(3)
    expect(nextGroupCostDisplayBoundary([group()], now)).toBe(now + 15 * 60_000 + 1)
  })
  it.each([null, NaN, Infinity, -1])('无可靠非负金额 %s 不显示零', amount => {
    expect(groupCostForDisplay(group(amount), Date.parse(observed)).todayCost).toBeNull()
  })
  it.each([null, 'invalid', '2031-02-03T04:00:01Z', '2031-02-02T04:00:00Z'])('缺少、无效、未来或跨日时间 %s 不显示金额', time => {
    const result = groupCostForDisplay({ ...group(), costObservedAt: time }, Date.parse(observed))
    expect(result.todayCost).toBeNull(); expect(result.costReason).toContain('不会修改主站')
  })
})
