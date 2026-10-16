// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { KeepAlive, defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import { UpstreamApiError } from '@/modules/admin/api/upstream'
import { locale } from '@/locales'

// 20261016/4：分组健康页常驻提示区，只列已对接站点最近一次没能确认的分组倍率读取结果。

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  currentAccount: null as any,
  visibility: null as any,
  intervalCallbacks: [] as Array<() => void>,
  activeWorkspaceScope: '',
  loadAll: vi.fn(),
  loadGroups: vi.fn(),
  loadAdminGroups: vi.fn(),
  refreshAdminGroups: vi.fn(),
  refreshAdminGroupsAutomatically: vi.fn(),
  loadEvents: vi.fn(),
  loadPolicies: vi.fn(),
  getPrioritySyncStatus: vi.fn(),
  listUpstreamSites: vi.fn(),
  listRealConnections: vi.fn(),
  cancelAdminGroupsRefresh: vi.fn(),
  setAdminGroupsWorkspace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn(async () => undefined) }),
}))

vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  harness.visibility = ref('hidden')
  return {
    useDocumentVisibility: () => harness.visibility,
    useIntervalFn: (callback: () => void) => {
      harness.intervalCallbacks.push(callback)
      return { pause: vi.fn(), resume: vi.fn() }
    },
  }
})

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  getPrioritySyncStatus: harness.getPrioritySyncStatus,
}))

vi.mock('@/modules/admin/api/upstream', async () => {
  const actual = await vi.importActual<typeof import('@/modules/admin/api/upstream')>('@/modules/admin/api/upstream')
  return { ...actual, listUpstreamSites: harness.listUpstreamSites }
})

vi.mock('@/modules/admin/api/mySites', () => ({
  listRealConnections: harness.listRealConnections,
}))

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'ws1', displayName: '测试工作区' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})

vi.mock('@/modules/admin/composables/useConnectionHealth', async () => {
  const { ref } = await import('vue')
  harness.refs = {
    overview: ref(null),
    groups: ref([]),
    adminGroups: ref([]),
    events: ref([]),
    policies: ref([]),
    isLoading: ref(false),
    isActionLoading: ref(false),
    errorKey: ref(''),
    terminalRefreshSummary: ref(null),
    refreshRunSnapshot: ref(null),
    refreshConflictNotice: ref(''),
    refreshConnectionState: ref('connected'),
  }
  return {
    connectionHealthMessageKey: (key: string) => key,
    useConnectionHealth: () => ({
      ...harness.refs,
      loadAll: harness.loadAll,
      loadGroups: harness.loadGroups,
      loadAdminGroups: harness.loadAdminGroups,
      refreshAdminGroups: harness.refreshAdminGroups,
      refreshAdminGroupsAutomatically: harness.refreshAdminGroupsAutomatically,
      loadEvents: harness.loadEvents,
      loadPolicies: harness.loadPolicies,
      cancelAdminGroupsRefresh: harness.cancelAdminGroupsRefresh,
      setAdminGroupsWorkspace: harness.setAdminGroupsWorkspace,
      removePolicy: vi.fn(async () => true),
      savePolicy: vi.fn(async () => true),
      updateTargetSchedulable: vi.fn(async () => true),
    }),
  }
})

const P1 = '已对接的上游站点中，以下站点最近一次同步没能确认分组倍率'
const P2 = '这些站点在分组倍率页的记录和调价提醒可能没有按最新倍率更新；查看不会修改主站。请按原因到上游核查，处理后到上游站点页同步该站；本区在进入本页或点“刷新”完成后更新。'
const P4 = '没有记录具体原因，请到上游站点页同步该站后再查看'
const P5 = '时间未知'
const P6_PREFIX = '暂时无法判断哪些已对接站点的分组倍率没确认：'
const P6_SUFFIX = '本次没有修改主站；请重新进入本页或点“刷新”重试，仍失败时请查看 Hub 后端日志里对应的读取记录。'
const P6A = '连不上 Hub 后端（网络中断或后端没有运行）'
const P6B = '登录状态已过期，请重新登录后继续'
const P6C = '原因未能确认'
const OBSERVED_AT = '2026-10-16T12:22:39.156Z'
const REASON = '读取可用分组失败：编号 1 的分组没有名称'

const site = (id: string, name: string, overrides: Record<string, any> = {}) => ({
  id,
  name,
  enabled: true,
  status: 'connected',
  metrics: {
    groups: [],
    rateNoticeObservation: { confirmed: false, observedAt: OBSERVED_AT, reason: REASON },
  },
  ...overrides,
})
const connection = (siteId: string, status?: string) => ({ id: `c-${siteId}`, upstreamSiteId: siteId, ...(status === undefined ? {} : { status }) })
const unconfirmedSite = () => site('s1', '站点甲')
const confirmedSite = () => site('s1', '站点甲', { metrics: { groups: [], rateNoticeObservation: { confirmed: true, observedAt: OBSERVED_AT } } })

const stubs = {
  Button: { template: '<button v-bind="$attrs"><slot /></button>' },
  AdminGroupHealthDetail: true,
  ConnectionHealthEventsDialog: true,
  GroupHealthSetupDrawer: true,
  ManualOneTimeProbeDialog: true,
  PolicyConfigDrawer: true,
  ProbePolicyListDialog: true,
  TargetPolicyAssignmentDialog: true,
}

const mountedWrappers: VueWrapper[] = []
const mountView = async () => {
  const wrapper = mount(ConnectionHealthView, { global: { stubs } })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const block = (wrapper: VueWrapper) => wrapper.find('[data-bound-site-rate-status]')

const clickRefresh = async (wrapper: VueWrapper) => {
  const button = wrapper.findAll('button').find(item => item.text().trim() === '刷新')
  if (!button) throw new Error('missing refresh button')
  await button.trigger('click')
  await flushPromises()
}

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise })
  return { promise, resolve, reject }
}

beforeEach(() => {
  for (const refValue of Object.values(harness.refs)) refValue.value = Array.isArray(refValue.value) ? [] : null
  harness.refs.isLoading.value = false
  harness.refs.isActionLoading.value = false
  harness.refs.errorKey.value = ''
  harness.refs.refreshConflictNotice.value = ''
  harness.refs.refreshConnectionState.value = 'connected'
  harness.visibility.value = 'hidden'
  harness.intervalCallbacks.splice(0)
  harness.loadAll.mockReset().mockResolvedValue(undefined)
  harness.loadGroups.mockReset().mockResolvedValue(true)
  harness.loadAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroupsAutomatically.mockReset().mockResolvedValue(true)
  harness.loadEvents.mockReset().mockResolvedValue(true)
  harness.loadPolicies.mockReset().mockResolvedValue(true)
  harness.getPrioritySyncStatus.mockReset().mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0 })
  harness.listUpstreamSites.mockReset().mockResolvedValue([unconfirmedSite()])
  harness.listRealConnections.mockReset().mockResolvedValue([connection('s1', 'active')])
  harness.currentAccount.value = { id: 'ws1', displayName: '测试工作区' }
  harness.activeWorkspaceScope = ''
  harness.cancelAdminGroupsRefresh.mockReset()
  harness.setAdminGroupsWorkspace.mockReset().mockImplementation((workspaceId: string) => {
    if (harness.activeWorkspaceScope === workspaceId) return
    harness.activeWorkspaceScope = workspaceId
    harness.refs.adminGroups.value = []
    harness.refs.terminalRefreshSummary.value = null
    harness.refs.refreshRunSnapshot.value = null
    harness.refs.errorKey.value = ''
  })
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('bound upstream site rate status on group health', () => {
  it('H1 lists a bound, enabled, connected site whose latest rate reading was not confirmed', async () => {
    const wrapper = await mountView()
    const text = block(wrapper).text()
    const time = new Date(OBSERVED_AT).toLocaleString(locale, { hour12: false, timeZone: 'Asia/Singapore' })
    expect(block(wrapper).attributes('role')).toBe('status')
    for (const part of [P1, P2, '站点甲', time, REASON]) expect(text).toContain(part)
    expect(text.indexOf(P1)).toBeLessThan(text.indexOf(P2))
    expect(text.indexOf(P2)).toBeLessThan(text.indexOf('站点甲'))
  })

  it('H2 applies every listing condition and hides the area when nothing qualifies', async () => {
    harness.listUpstreamSites.mockResolvedValue([
      unconfirmedSite(),
      site('s2', '未对接站点'),
      site('s3', '缺失对接站点'),
      site('s4', '空状态对接站点'),
      site('s5', '停用站点', { enabled: false }),
      site('s6', '异常站点', { status: 'error' }),
      site('s7', '同步中站点', { status: 'syncing' }),
      site('s8', '已确认站点', { metrics: { groups: [], rateNoticeObservation: { confirmed: true, observedAt: OBSERVED_AT } } }),
      site('s9', '无记录站点', { metrics: { groups: [] } }),
    ])
    harness.listRealConnections.mockResolvedValue([
      connection('s1', 'active'), connection('s3', 'missing'), connection('s4', ' '),
      connection('s5', 'active'), connection('s6', 'active'), connection('s7', 'active'), connection('s8', 'active'), connection('s9'),
    ])
    const wrapper = await mountView()
    const text = block(wrapper).text()
    expect(text).toContain('站点甲')
    expect(text).toContain('空状态对接站点')
    for (const name of ['未对接站点', '缺失对接站点', '停用站点', '异常站点', '同步中站点', '已确认站点', '无记录站点']) expect(text).not.toContain(name)

    harness.listUpstreamSites.mockResolvedValue([site('s2', '未对接站点'), confirmedSite()])
    await clickRefresh(wrapper)
    expect(block(wrapper).exists()).toBe(false)
  })

  it('H3 reloads both lists on re-activation and after a successful or failed manual refresh', async () => {
    const Other = defineComponent({ render: () => h('div', 'other page') })
    const Host = defineComponent({
      props: { show: { type: Boolean, default: true } },
      setup: props => () => h(KeepAlive, null, { default: () => (props.show ? h(ConnectionHealthView) : h(Other)) }),
    })
    const wrapper = mount(Host, { props: { show: true }, global: { stubs } })
    mountedWrappers.push(wrapper)
    await flushPromises()
    expect(block(wrapper).exists()).toBe(true)
    const callsAfterEntry = harness.listRealConnections.mock.calls.length
    const sitesAfterEntry = harness.listUpstreamSites.mock.calls.length

    const now = Date.now()
    vi.spyOn(Date, 'now').mockReturnValue(now + 5_000)
    await wrapper.setProps({ show: false })
    harness.listUpstreamSites.mockResolvedValue([confirmedSite()])
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(harness.listRealConnections.mock.calls.length).toBe(callsAfterEntry + 1)
    expect(harness.listUpstreamSites.mock.calls.length).toBeGreaterThan(sitesAfterEntry)
    expect(block(wrapper).exists()).toBe(false)

    harness.listUpstreamSites.mockResolvedValue([unconfirmedSite()])
    await clickRefresh(wrapper)
    expect(harness.listRealConnections.mock.calls.length).toBe(callsAfterEntry + 2)
    expect(block(wrapper).text()).toContain('站点甲')

    harness.refreshAdminGroups.mockResolvedValue(false)
    harness.listUpstreamSites.mockResolvedValue([confirmedSite()])
    await clickRefresh(wrapper)
    expect(harness.listRealConnections.mock.calls.length).toBe(callsAfterEntry + 3)
    expect(block(wrapper).exists()).toBe(false)
  })

  it('H4 does not reload on the 30-second automatic refresh', async () => {
    const wrapper = await mountView()
    harness.visibility.value = 'visible'
    const connections = harness.listRealConnections.mock.calls.length
    const sites = harness.listUpstreamSites.mock.calls.length
    const automatic = harness.refreshAdminGroupsAutomatically.mock.calls.length
    harness.intervalCallbacks[0]!()
    await flushPromises()
    expect(harness.refreshAdminGroupsAutomatically.mock.calls.length).toBe(automatic + 1)
    expect(harness.listRealConnections.mock.calls.length).toBe(connections)
    expect(harness.listUpstreamSites.mock.calls.length).toBe(sites)
    expect(block(wrapper).text()).toContain('站点甲')
  })

  it.each([
    ['network', () => Promise.reject(new Error('admin.mySites.errors.network')), P6A],
    ['login expired', () => Promise.reject(new Error('auth.errors.unauthorized')), P6B],
    ['no workspace as seen by the real API', () => Promise.reject(new Error('admin.mySites.errors.unknown')), P6C],
    ['misleading translated key', () => Promise.reject(new Error('admin.mySites.errors.request')), P6C],
    ['plain returned text', () => Promise.reject(new Error('测试原因')), P6C],
    ['not a list', () => Promise.resolve({ items: [] }), P6C],
  ])('H5 shows only P6 when the connection list fails: %s', async (_name, failure, reason) => {
    harness.listRealConnections.mockImplementation(failure)
    const wrapper = await mountView()
    const text = block(wrapper).text()
    expect(text).toContain(P6_PREFIX)
    expect(text).toContain(`读取对接资料失败（${reason}`)
    expect(text).toContain(P6_SUFFIX)
    expect(text).not.toContain(P1)
    expect(text).not.toContain('站点甲')
    for (const forbidden of ['导入参数无效', 'admin.', 'auth.errors', '测试原因', '暂时无法读取分组健康数据']) expect(text).not.toContain(forbidden)

    harness.listRealConnections.mockResolvedValue([connection('s1', 'active')])
    await clickRefresh(wrapper)
    expect(block(wrapper).text()).not.toContain(P6_PREFIX)
    expect(block(wrapper).text()).toContain('站点甲')
  })

  it('H6 names the site list failure and both failures together', async () => {
    harness.listUpstreamSites.mockRejectedValue(new Error('admin.upstream.errors.network'))
    const wrapper = await mountView()
    expect(block(wrapper).text()).toContain(`读取上游站点资料失败（${P6A}`)

    harness.listUpstreamSites.mockRejectedValue(new UpstreamApiError('admin.upstream.errors.request'))
    await clickRefresh(wrapper)
    expect(block(wrapper).text()).toContain(`读取上游站点资料失败（${P6C}`)

    harness.listRealConnections.mockRejectedValue(new Error('admin.mySites.errors.network'))
    await clickRefresh(wrapper)
    const text = block(wrapper).text()
    expect(text).toContain('读取上游站点资料失败、读取对接资料失败')
    expect(text).toContain(P6C)
    expect(text).toContain(P6A)
  })

  it('H7 keeps the newest result when an older read finishes later', async () => {
    const entry = deferred<unknown>()
    const manual = deferred<unknown>()
    harness.listRealConnections.mockReturnValueOnce(entry.promise).mockReturnValueOnce(manual.promise)
    const wrapper = await mountView()
    await clickRefresh(wrapper)
    manual.resolve([connection('s1', 'active')])
    await flushPromises()
    expect(block(wrapper).text()).toContain('站点甲')
    entry.reject(new Error('admin.mySites.errors.network'))
    await flushPromises()
    expect(block(wrapper).text()).toContain('站点甲')
    expect(block(wrapper).text()).not.toContain(P6_PREFIX)

    const olderSuccess = deferred<unknown>()
    const newerFailure = deferred<unknown>()
    harness.listRealConnections.mockReturnValueOnce(olderSuccess.promise).mockReturnValueOnce(newerFailure.promise)
    await clickRefresh(wrapper)
    await clickRefresh(wrapper)
    newerFailure.reject(new Error('admin.mySites.errors.network'))
    await flushPromises()
    expect(block(wrapper).text()).toContain(P6_PREFIX)
    olderSuccess.resolve([connection('s1', 'active')])
    await flushPromises()
    expect(block(wrapper).text()).toContain(P6_PREFIX)
    expect(block(wrapper).text()).not.toContain('站点甲')
  })

  it.each([
    ['success', (pending: ReturnType<typeof deferred<unknown>>) => pending.resolve([connection('s1', 'active')])],
    ['failure', (pending: ReturnType<typeof deferred<unknown>>) => pending.reject(new Error('admin.mySites.errors.network'))],
  ])('H8 clears the area on workspace switch, ignores a late %s and shows the new workspace result', async (_name, settle) => {
    const wrapper = await mountView()
    expect(block(wrapper).exists()).toBe(true)
    const pending = deferred<unknown>()
    const fresh = deferred<unknown>()
    harness.listRealConnections.mockReturnValueOnce(pending.promise).mockReturnValueOnce(fresh.promise)
    await clickRefresh(wrapper)
    harness.listUpstreamSites.mockResolvedValue([confirmedSite()])
    harness.currentAccount.value = { id: 'ws2', displayName: '另一工作区' }
    await nextTick()
    expect(block(wrapper).exists()).toBe(false)
    settle(pending)
    await flushPromises()
    expect(block(wrapper).exists()).toBe(false)
    fresh.resolve([connection('s1', 'active')])
    await flushPromises()
    // 新工作区的读取结果：站点已确认，提示区不出现；旧工作区迟到的结果没有被采用。
    expect(block(wrapper).exists()).toBe(false)
    expect(harness.listRealConnections.mock.calls.length).toBeGreaterThanOrEqual(3)
  })

  it('H11 reads again when the workspace resolves after the page mounted (full page load)', async () => {
    harness.currentAccount.value = null
    const wrapper = await mountView()
    harness.currentAccount.value = { id: 'ws1', displayName: '测试工作区' }
    await flushPromises()
    expect(block(wrapper).text()).toContain('站点甲')
  })

  it('H9 shows the fallback reason and unknown time', async () => {
    harness.listUpstreamSites.mockResolvedValue([site('s1', '站点甲', { metrics: { groups: [], rateNoticeObservation: { confirmed: false, observedAt: 'not-a-time', reason: '' } } })])
    const wrapper = await mountView()
    const text = block(wrapper).text()
    expect(text).toContain(P4)
    expect(text).toContain(P5)
  })

  it('H10 leaves the refresh warning block and the existing site-name failure line unchanged', async () => {
    const wrapper = await mountView()
    const warning = { message: '倍率保存结果尚未确认', detail: '提交连接中断，不能确认哪些倍率已更新或清理', next: '请核查数据库提交结果后再同步；主站账号未修改' }
    harness.refs.refreshRunSnapshot.value = { runId: 'run', revision: 2, runState: 'success', stage: 'site_sync', stageCompletedSites: 1, stageTotalSites: 1, waiting: [], issues: [{ siteId: 's1', siteName: '站点甲', phase: 'group_rates', status: 'success', errorKey: '', groupRatesWarning: warning }] }
    await nextTick()
    const refreshWarning = wrapper.get('[data-group-rates-warning]').text()
    expect(refreshWarning).toContain(warning.message)
    expect(refreshWarning).toContain(warning.next)
    expect(refreshWarning).not.toContain(P1)
    expect(block(wrapper).text()).toContain(P1)

    harness.listUpstreamSites.mockRejectedValue(new Error('admin.upstream.errors.network'))
    const failedNames = await mountView()
    expect(failedNames.text()).toContain('辅助读取失败（站点名称）')
    expect(failedNames.text()).not.toContain('辅助读取失败（已对接站点）')
    expect(block(failedNames).text()).toContain(P6_PREFIX)
  })
})
