// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { storeAccessToken } from '@/modules/auth/api/auth'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  workspace: null as any,
  visibility: null as any,
  automatic: vi.fn(),
  manual: vi.fn(),
  loadAdminGroups: vi.fn(),
  listUpstreamSites: vi.fn(),
  listRealConnections: vi.fn(),
  listHiddenAccounts: vi.fn(),
  getPriorityStatus: vi.fn(),
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
    useIntervalFn: () => ({ pause: vi.fn(), resume: vi.fn() }),
  }
})

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.workspace = ref(null)
  return { useAdminAccounts: () => ({ currentAccount: harness.workspace }) }
})

vi.mock('@/modules/admin/composables/useConnectionHealth', async importOriginal => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  const { ref } = await import('vue')
  harness.refs = {
    overview: ref(null),
    groups: ref([]),
    adminGroups: ref([]),
    events: ref([]),
    policies: ref([]),
    adminGroupsLoaded: ref(true),
    isLoading: ref(false),
    isActionLoading: ref(false),
    errorKey: ref(''),
    terminalRefreshSummary: ref(null),
    refreshRunSnapshot: ref(null),
    refreshConflictNotice: ref(''),
    refreshConnectionState: ref('connected'),
  }
  return {
    ...actual,
    useConnectionHealth: () => ({
      ...harness.refs,
      loadGroups: vi.fn(async () => true),
      loadAdminGroups: harness.loadAdminGroups,
      refreshAdminGroups: harness.manual,
      refreshAdminGroupsAutomatically: harness.automatic,
      loadEvents: vi.fn(async () => true),
      loadPolicies: vi.fn(async () => true),
      cancelAdminGroupsRefresh: vi.fn(),
      setAdminGroupsWorkspace: vi.fn(),
    }),
  }
})

vi.mock('@/modules/admin/api/connectionHealth', async importOriginal => ({
  ...(await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>()),
  getPrioritySyncStatus: harness.getPriorityStatus,
  listHiddenAccounts: harness.listHiddenAccounts,
}))

vi.mock('@/modules/admin/api/upstream', async importOriginal => ({
  ...(await importOriginal<typeof import('@/modules/admin/api/upstream')>()),
  listUpstreamSites: harness.listUpstreamSites,
}))
vi.mock('@/modules/admin/api/mySites', async importOriginal => ({
  ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()),
  listRealConnections: harness.listRealConnections,
}))

const refreshLabel = '更新这个账号的倍率。先同步它对接的启用中上游站点，再在后台读取倍率，读完后的下一次自动刷新会显示新值。主站账号不会被修改，已开启自动调价的主站分组可能调整倍率。'
const manualDisabledLabel = '整页刷新进行中，完成后再试'
const disabledRowText = '上游站点已禁用，主站“可调度”开关仍开着'
const successText = '已同步上游站点，倍率会在之后的自动刷新中更新；若仍未变化可稍后再点'
const noBindingText = '这个账号没有对接启用中的上游站点，无法更新倍率'

const account = (id: string, overrides: Partial<AdminGroupAccount> = {}): AdminGroupAccount => ({
  id, name: `测试账号${id}`, targetId: `sub2api:ws1:${id}`, platform: 'openai', type: 'api',
  status: 'active', schedulable: true, probeAvailable: true, modelHealth: [], effectivePolicies: [],
  multiplierResolutionStatus: 'disabled', multiplierSource: 'none', upstreamSiteId: 'site-disabled',
  ...overrides,
})

const group = (id: string, name: string, accounts: AdminGroupAccount[]): AdminGroupHealth => ({
  id, name, accounts, platform: 'openai', status: 'active', type: 'standard', isExclusive: false,
  subscriptionType: '', multiplier: 1, multiplierDisplay: '1x', accountCount: accounts.length,
  monitoredAccountCount: 0, healthSummary: {
    totalAccounts: accounts.length, probeableAccounts: accounts.length, unprobeableAccounts: 0,
    healthyModels: 0, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0,
    lastProbeAt: null,
  },
})

const policy = (strategyMode: 'multiplier_only' | 'health_probe'): NonNullable<AdminGroupAccount['effectivePolicies']> => [
  { policyId: strategyMode, policyName: strategyMode, enabled: true, priorityMode: 'multiplier', strategyMode },
]

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const fetchMock = vi.fn<typeof fetch>()
const wrappers: VueWrapper[] = []
const open = async (groups: AdminGroupHealth[]) => {
  harness.refs.adminGroups.value = groups
  const wrapper = mount(ConnectionHealthView, {
    attachTo: document.body,
    global: { stubs: {
      ConnectionHealthEventsDialog: true,
      GroupHealthSetupDrawer: true,
      ManualOneTimeProbeDialog: true,
      PolicyConfigDrawer: true,
      ProbePolicyListDialog: true,
      TargetPolicyAssignmentDialog: true,
      QuestionAnswerBatchDrawer: true,
      QuestionAnswerScheduleDrawer: true,
      QuestionAnswerModelControlDrawer: true,
      QuestionAnswerScheduleBatchDialog: true,
      RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
    } },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
const notice = (wrapper: VueWrapper) => wrapper.find('[data-testid="disabled-upstream-schedulable-notice"]')
const row = (wrapper: VueWrapper, name: string) => {
  const target = wrapper.findAll('tbody tr').find(item => item.text().includes(name))
  if (!target) throw new Error(`Missing account row ${name}`)
  return target
}
const icon = (wrapper: VueWrapper, name: string) => row(wrapper, name).get(`button[aria-label="${refreshLabel}"]`)
const selectGroup = async (wrapper: VueWrapper, name: string) => {
  const entry = wrapper.findAll('nav button').find(item => item.text().includes(name))
  if (!entry) throw new Error(`Missing group ${name}`)
  await entry.trigger('click')
}
const tooltip = async (button: ReturnType<typeof icon>) => {
  const trigger = button.element.parentElement!
  trigger.dispatchEvent(new MouseEvent('mouseenter'))
  await vi.advanceTimersByTimeAsync(150)
  const text = document.body.querySelector('[role="tooltip"]')?.textContent?.trim()
  trigger.dispatchEvent(new MouseEvent('mouseleave'))
  await nextTick()
  return text
}

beforeEach(() => {
  harness.workspace.value = { id: 'ws1', displayName: '工作区', platform: 'sub2api', baseUrl: 'https://main.example/' }
  harness.visibility.value = 'hidden'
  harness.refs.refreshConflictNotice.value = ''
  harness.refs.terminalRefreshSummary.value = null
  harness.refs.refreshRunSnapshot.value = null
  harness.automatic.mockReset().mockResolvedValue(true)
  harness.manual.mockReset().mockResolvedValue(true)
  harness.loadAdminGroups.mockReset().mockResolvedValue(true)
  harness.listUpstreamSites.mockReset().mockResolvedValue([{ id: 'site-disabled', name: '旧上游', metrics: {} }])
  harness.listRealConnections.mockReset().mockResolvedValue([])
  harness.listHiddenAccounts.mockReset().mockImplementation(async workspaceId => ({ workspaceId, items: [] }))
  harness.getPriorityStatus.mockReset().mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0 })
  fetchMock.mockReset().mockImplementation(async () => { throw new Error('Unexpected request') })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('disabled upstream with scheduling enabled', () => {
  it('reminds on all strategy paths, merges groups, and keeps an excluded account out of the details', async () => {
    const active = account('101')
    const inactive = account('102', { status: 'inactive', effectivePolicies: policy('multiplier_only') })
    const other = account('103', { status: 'error', effectivePolicies: policy('health_probe') })
    const hidden = account('104')
    harness.listHiddenAccounts.mockResolvedValue({ workspaceId: 'ws1', items: [{ targetId: hidden.targetId, accountName: hidden.name }] })
    const wrapper = await open([group('g1', '主力分组', [active, inactive, other, hidden]), group('g2', '后备分组', [active])])

    const warning = notice(wrapper)
    expect(warning.text()).toContain('以下账号对接的上游站点已在调度站禁用，但主站的“可调度”开关仍开着，请到主站处理')
    expect(warning.findAll('li')).toHaveLength(4)
    expect(warning.findAll('li').filter(item => item.text().includes(active.name))).toHaveLength(1)
    expect(warning.text()).toContain('「测试账号101」（主站账号 ID 101）· 上游站点「旧上游」· 主站分组：主力分组、后备分组')
    expect(warning.text()).toContain('主站账号是启用状态，“可调度”开关开着。')
    expect(warning.text()).toContain('主站账号已停用，“可调度”开关仍开着；账号被重新启用后就可能被调度。')
    expect(warning.text()).toContain('主站账号状态是「error」，“可调度”开关开着。')
    expect(warning.text()).toContain('关闭“可调度”或把它移出这些分组')
    expect(warning.text()).toContain('到调度站“上游站点”重新启用它')
    expect(warning.text()).toContain('处理后本提醒在下一次自动刷新时消失')
    const link = warning.get('a')
    expect(link.text()).toBe('打开主站账号列表')
    expect(link.attributes()).toMatchObject({ href: 'https://main.example/admin/accounts', target: '_blank', rel: 'noopener noreferrer' })
    expect(warning.text()).toContain(hidden.name)
    for (const item of [active, inactive, other]) expect(row(wrapper, item.name).text()).toContain(disabledRowText)
    expect(wrapper.findAll('tbody tr').some(item => item.text().includes(hidden.name))).toBe(false)
  })

  it('shows the site ID until both reads succeed, and still uses the ID after the binding read fails', async () => {
    const connections = deferred<unknown[]>()
    harness.listRealConnections.mockReturnValueOnce(connections.promise)
    const wrapper = await open([group('g1', '测试组', [account('101')])])
    expect(notice(wrapper).text()).toContain('上游站点「site-disabled」')
    expect(notice(wrapper).text()).not.toContain('旧上游')
    connections.resolve([])
    await flushPromises()
    expect(notice(wrapper).text()).toContain('上游站点「旧上游」')

    harness.listRealConnections.mockRejectedValueOnce(new Error('admin.mySites.errors.network'))
    await wrapper.findAll('button').find(item => item.text() === '刷新')!.trigger('click')
    await flushPromises()
    expect(notice(wrapper).text()).toContain('上游站点「site-disabled」')
    expect(notice(wrapper).text()).not.toContain('旧上游')
  })

  it('drops the reminder on the next automatic update when scheduling is closed', async () => {
    const item = account('101', { effectivePolicies: policy('health_probe') })
    const wrapper = await open([group('g1', '测试组', [item])])
    expect(notice(wrapper).exists()).toBe(true)
    harness.automatic.mockImplementationOnce(async () => {
      harness.refs.adminGroups.value = [group('g1', '测试组', [{ ...item, schedulable: false }])]
      return true
    })
    harness.visibility.value = 'visible'
    await flushPromises()

    expect(notice(wrapper).exists()).toBe(false)
    const body = row(wrapper, item.name)
    expect(body.text()).toContain('上游站点已禁用')
    expect(body.text()).not.toContain(disabledRowText)
    expect(body.text()).not.toContain('未参与本轮倍率读取')
  })

  it.each([
    ['sub2api', 'resolved'], ['sub2api', 'missing'], ['sub2api', 'unavailable'], ['newapi', 'disabled'],
  ])('does not show a warning for %s / %s', async (platform, multiplierResolutionStatus) => {
    harness.workspace.value.platform = platform
    const item = account('101', { multiplierResolutionStatus, effectivePolicies: policy('health_probe') })
    const wrapper = await open([group('g1', '测试组', [item])])
    expect(notice(wrapper).exists()).toBe(false)
    expect(row(wrapper, item.name).text()).not.toContain(disabledRowText)
    if (platform === 'newapi') expect(wrapper.find(`button[aria-label="${refreshLabel}"]`).exists()).toBe(false)
  })
})

describe('account multiplier refresh', () => {
  it('sends authenticated scoped POSTs per account and only asks the existing automatic refresh to reload', async () => {
    const sync1 = deferred<Response>()
    const sync2 = deferred<Response>()
    const automatic = deferred<boolean>()
    fetchMock.mockReturnValueOnce(sync1.promise).mockReturnValueOnce(sync2.promise)
    const wrapper = await open([group('g1', '测试组', [account('101'), account('102')])])
    const first = icon(wrapper, '测试账号101')
    const second = icon(wrapper, '测试账号102')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    expect(first.text()).toBe('')
    expect(await tooltip(first)).toBe(refreshLabel)
    vi.useRealTimers()
    storeAccessToken('multiplier-action-test-token')
    await first.trigger('click')
    await first.trigger('click')
    expect(first.attributes('disabled')).toBeDefined()
    expect(first.find('svg').classes()).toEqual(expect.arrayContaining(['h-3', 'w-3', 'animate-spin']))
    expect(second.attributes('disabled')).toBeUndefined()
    await second.trigger('click')

    expect(fetchMock.mock.calls.map(([url, options]) => [url, options?.method, JSON.parse(String(options?.body))])).toEqual([
      ['/api/connection-health/targets/sub2api%3Aws1%3A101/multiplier-refresh', 'POST', { workspaceId: 'ws1' }],
      ['/api/connection-health/targets/sub2api%3Aws1%3A102/multiplier-refresh', 'POST', { workspaceId: 'ws1' }],
    ])
    expect(new Headers(fetchMock.mock.calls[0]![1]?.headers).get('Authorization')).toBe('Bearer multiplier-action-test-token')
    harness.automatic.mockReturnValueOnce(automatic.promise)
    sync1.resolve(json({ noBinding: false, sites: [{ siteId: 's1', siteName: '站点一', status: 'success' }] }))
    await flushPromises()
    expect(row(wrapper, '测试账号101').text()).toContain(successText)
    expect(row(wrapper, '测试账号102').text()).not.toContain(successText)
    expect(first.attributes('disabled')).toBeUndefined()
    expect(harness.automatic).toHaveBeenCalledTimes(2)
    expect(harness.listRealConnections).toHaveBeenCalledTimes(2)
    expect(harness.listUpstreamSites).toHaveBeenCalledTimes(3)

    sync2.resolve(json({ noBinding: true, sites: [] }))
    await flushPromises()
    expect(row(wrapper, '测试账号102').text()).toContain(noBindingText)
    expect(harness.automatic).toHaveBeenCalledTimes(2)
    expect(harness.manual).not.toHaveBeenCalled()
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()
    automatic.resolve(true)
  })

  it('renders each site result and only one common success line', async () => {
    fetchMock.mockResolvedValueOnce(json({ noBinding: false, sites: [
      { siteId: 'ok', siteName: '正常站', status: 'success' },
      { siteId: 'warning', siteName: '告警站', status: 'success', groupRatesWarning: { message: '问题', detail: '原因', next: '怎么处理' } },
      { siteId: 'failed', siteName: '失败站', status: 'failed', errorKey: 'site_sync_network', errorDetail: '连接中断，token=secret-relative-value' },
      { siteId: 'timeout', siteName: '超时站', status: 'failed', errorKey: 'site_sync_timeout' },
      { siteId: 'cancelled', siteName: '取消站', status: 'failed', errorKey: 'site_sync_cancelled' },
      { siteId: 'not-readable', status: 'site_read_failed' },
    ] }))
    const wrapper = await open([group('g1', '测试组', [account('101')])])
    await icon(wrapper, '测试账号101').trigger('click')
    await flushPromises()
    const result = row(wrapper, '测试账号101').text()
    expect(result.split(successText)).toHaveLength(2)
    expect(result).toContain('上游站点「告警站」已同步：问题；原因；怎么处理')
    expect(result).toContain('上游站点「失败站」同步失败：上游站点同步：网络不可达；连接中断')
    expect(result).not.toContain('secret-relative-value')
    expect(result).toContain('本次未确认上游站点「超时站」的同步结果，请到上游站点页查看后再试')
    expect(result).toContain('本次未确认上游站点「取消站」的同步结果，请到上游站点页查看后再试')
    expect(result).toContain('有一个上游站点的资料读取失败，可以再点一次')
    expect(harness.automatic).toHaveBeenCalledTimes(2)
  })

  it.each([
    [409, 'workspace_changed', '当前工作区已切换'],
    [400, 'unsupported_platform', '当前工作区不是 Sub2API'],
    [404, 'target_not_found', '这个账号不属于当前工作区'],
  ])('renders a refused request without implying synchronization began for %s', async (status, key, reason) => {
    fetchMock.mockResolvedValueOnce(json({ message: key }, status))
    const wrapper = await open([group('g1', '测试组', [account('101')])])
    await icon(wrapper, '测试账号101').trigger('click')
    await flushPromises()
    expect(row(wrapper, '测试账号101').text()).toContain(`没有开始更新：${reason}`)
    expect(row(wrapper, '测试账号101').text()).not.toContain('站点可能已开始同步')
    expect(harness.automatic).toHaveBeenCalledTimes(2)
  })

  it('reports a dropped network request without claiming whether it ran and clears the result on retry', async () => {
    const next = deferred<Response>()
    fetchMock.mockRejectedValueOnce(new Error('connection lost')).mockReturnValueOnce(next.promise)
    const wrapper = await open([group('g1', '测试组', [account('101')])])
    const button = icon(wrapper, '测试账号101')
    await button.trigger('click')
    await flushPromises()
    const result = row(wrapper, '测试账号101')
    expect(result.text()).toContain('更新请求没有完成：')
    expect(result.text()).toContain('站点可能已开始同步，可以再点一次')
    expect(result.text()).not.toContain(successText)
    await button.trigger('click')
    expect(result.text()).not.toContain('更新请求没有完成')
    next.resolve(json({ noBinding: true, sites: [] }))
    await flushPromises()
    expect(result.text()).toContain(noBindingText)
  })

  it('disables the icon and changes its tooltip during whole-page manual refresh', async () => {
    const manual = deferred<boolean>()
    harness.manual.mockReturnValueOnce(manual.promise)
    const wrapper = await open([group('g1', '测试组', [account('101')])])
    await wrapper.findAll('button').find(item => item.text() === '刷新')!.trigger('click')
    const button = icon(wrapper, '测试账号101')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    expect(button.attributes('disabled')).toBeDefined()
    expect(await tooltip(button)).toBe(manualDisabledLabel)
    await button.trigger('click')
    expect(fetchMock).not.toHaveBeenCalled()
    vi.useRealTimers()
    manual.resolve(true)
    await flushPromises()
    expect(button.attributes('disabled')).toBeUndefined()
  })

  it('discards late per-account results after visiting another group or workspace', async () => {
    const first = deferred<Response>()
    const second = deferred<Response>()
    const item = account('101')
    fetchMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const wrapper = await open([group('g1', '第一组', [item]), group('g2', '第二组', [item])])

    await icon(wrapper, item.name).trigger('click')
    await selectGroup(wrapper, '第二组')
    await selectGroup(wrapper, '第一组')
    first.resolve(json({ noBinding: true, sites: [] }))
    await flushPromises()
    expect(row(wrapper, item.name).text()).not.toContain(noBindingText)
    expect(harness.automatic).toHaveBeenCalledTimes(2)

    await icon(wrapper, item.name).trigger('click')
    harness.workspace.value = { ...harness.workspace.value, id: 'ws2' }
    await nextTick()
    harness.workspace.value = { ...harness.workspace.value, id: 'ws1' }
    await nextTick()
    second.resolve(json({ noBinding: true, sites: [] }))
    await flushPromises()
    expect(row(wrapper, item.name).text()).not.toContain(noBindingText)
    expect(harness.automatic).toHaveBeenCalledTimes(2)
  })
})

const bound = (id: string, site: string, accountId: string, extra: Record<string, string> = {}) => ({
  id, upstreamSiteId: site, adminAccountId: accountId, adminAccountName: `绑定账号${accountId}`, ...extra,
})
const diagnosis = (accountId: string, action = 'status', reason = 'uncertain') => ({
  accountId, accountName: `诊断账号${accountId}`, targetId: `sub2api:ws1:${accountId}`, action, phase: 'uncertain', reason,
})
const priorityFailure = (overrides: Record<string, unknown> = {}) => {
  harness.getPriorityStatus.mockResolvedValue({ workspaceId: 'ws1', status: 'failed', failedCount: 1, ...overrides })
}
const pickButton = (label: string, scope: ParentNode = document.body): HTMLButtonElement => {
  const found = [...scope.querySelectorAll('button')].find(button => button.textContent?.trim() === label)
  if (!found) throw new Error(`Missing button ${label}`)
  return found
}
const dialog = () => document.body.querySelector('[role="dialog"]')!
const actionPreview = {
  canConfirm: true, dispatchId: 'dispatch-tested', pendingValue: 'active', currentValue: 'inactive',
  observedAt: '2031-02-03T04:05:06Z', recordedAt: '2030-10-11T12:13:14Z', willMarkAs: 'rejected', source: 'health_probe',
}

describe('upstream site and binding problem guidance', () => {
  it('groups affected accounts, retains the original failures and links to fixes', async () => {
    harness.listRealConnections.mockResolvedValue([
      bound('1', 's1', '101'), bound('2', 's1', '101'), bound('3', 's1', '999', { adminPlatform: 'newapi' }),
      bound('4', 's1', '404', { status: 'missing' }), bound('5', 's1', '405', { status: 'missing', adminPlatform: 'newapi' }),
      bound('6', 'disabled', '456'), bound('7', 'normal', '457'),
      ...['102', '103', '104', '105', '106'].map(id => bound(id, 's1', id)),
    ])
    harness.listUpstreamSites.mockResolvedValue([
      { id: 's1', name: '出错站点', status: 'error', errorKey: 'admin.upstream.errors.networkUnreachable', websiteUrl: 'javascript:bad', baseUrl: 'https://upstream.example/', metrics: {} },
      { id: 'disabled', name: '禁用错误站', enabled: false, status: 'error', metrics: {} },
      { id: 'normal', name: '正常站', status: 'connected', metrics: {} },
      { id: 'unbound', name: '没有对接的站', status: 'error', metrics: {} },
    ])
    priorityFailure()
    const item = account('101', { multiplierResolutionStatus: 'unavailable', upstreamSiteId: 's1',
      prioritySyncBlocked: true, prioritySyncBlockReason: 'site_unavailable' })
    const keyItem = account('102', { multiplierResolutionStatus: 'missing', prioritySyncBlocked: true, prioritySyncBlockReason: 'key_deleted', upstreamSiteId: 's1' })
    const wrapper = await open([group('g1', '主力分组', [item, keyItem]), group('g2', '后备分组', [item])])
    harness.refs.terminalRefreshSummary.value = { state: 'partial', sites: [{ siteId: 's1', status: 'unavailable', errorKey: 'site_sync_network' }] }
    await nextTick()
    const box = wrapper.get('[data-testid="upstream-site-issues"]')
    expect(box.findAll('li')).toHaveLength(1)
    expect(box.text()).toContain('影响 6 个账号')
    expect(box.text()).toContain('测试账号101（主站分组 主力分组、后备分组）')
    expect(box.text()).toContain('等 6 个')
    expect(box.text()).not.toContain('绑定账号106')
    expect(box.text()).not.toContain('绑定账号999')
    expect(box.text()).toContain('先打开上游网站确认是否还能访问')
    expect(box.findAll('a').map(a => [a.text(), a.attributes('href')])).toEqual([
      ['去上游站点页', '/admin/upstream'], ['打开上游网站', 'https://upstream.example/'],
    ])
    expect(wrapper.text()).toContain('出错站点: 上游站点同步：网络不可达')
    expect(wrapper.text().match(/站点问题见上方/g)).toHaveLength(1)
    expect(wrapper.text()).toContain('上游找不到这个 Key（可能已删除）')
    expect(wrapper.get('a[href="/admin/group-rates?cleanup=stale"]').text()).toContain('有 1 条对接已被标记为主站账号不存在')
  })

  it('explains the missing group and lists replacements while retaining scoped links', async () => {
    harness.listUpstreamSites.mockResolvedValue([{
      id: 's1', name: '魔力坊', status: 'error', errorKey: 'admin.upstream.errors.refreshTokenRejected',
      websiteUrl: 'https://upstream.example/', metrics: { groups: [
        { id: 'b', name: 'B组', multiplier: null }, { id: 'a', name: 'A组', multiplier: 0.7 },
      ] },
    }])
    harness.listRealConnections.mockResolvedValueOnce([bound('1', 's1', '456')])
    priorityFailure()
    const builder = (id: string, why: string, siteId: string) => account(id, { multiplierResolutionStatus: 'missing', upstreamSiteId: siteId, prioritySyncBlocked: true, prioritySyncBlockReason: why })
    const wrapper = await open([group('g1', '分组', [
      { ...builder('456', 'group_missing', 's1'), upstreamKeyGroupName: 'ChatGPT-Plus【特惠】', upstreamKeyGroupId: '22' },
      builder('401', 'binding_missing', 's1'), builder('402', 'binding_missing', ''),
    ])])
    expect(wrapper.text()).toContain('到上游站点页重新登录这个站点')
    expect(wrapper.text()).toContain('Key 所在分组「ChatGPT-Plus【特惠】」（ID 22）在站点资料里已找不到')
    expect(wrapper.text()).toContain('站点现有分组：A组 0.7x、B组 倍率未知')
    expect(wrapper.text()).toContain('改好后点这个账号倍率旁的刷新图标')
    expect(wrapper.findAll('a[href="/admin/group-rates?siteId=s1"]')).toHaveLength(2)
    expect(wrapper.findAll('a[href="/admin/group-rates"]')).toHaveLength(1)
    expect(wrapper.text().match(/这条对接缺少站点或 Key 资料/g)).toHaveLength(2)
    expect(wrapper.text()).not.toContain('站点问题见上方')
    harness.listRealConnections.mockRejectedValueOnce(new Error('admin.mySites.errors.network'))
    harness.visibility.value = 'visible'
    await flushPromises()
    expect(wrapper.find('[data-testid="upstream-site-issues"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('站点现有分组暂时没读到')
    expect(wrapper.text()).toContain('暂时无法判断哪些已对接站点的分组倍率没确认')
  })

  it('uses general advice for request errors and omits invalid website links and unrelated site references', async () => {
    harness.listUpstreamSites.mockResolvedValue([
      { id: 's1', name: '请求失败站', status: 'error', errorKey: 'admin.upstream.errors.upstreamServer', websiteUrl: 'invalid', baseUrl: 'invalid', metrics: {} },
      { id: 's2', name: '其他平台站', status: 'error', metrics: {} },
    ])
    harness.listRealConnections.mockResolvedValue([
      bound('1', 's1', '101'), bound('2', 's2', '102', { adminPlatform: 'newapi' }),
    ])
    priorityFailure()
    const item = account('101', { multiplierResolutionStatus: 'unavailable', upstreamSiteId: 's1', prioritySyncBlocked: true, prioritySyncBlockReason: 'key_unavailable' })
    const wrapper = await open([group('g1', '主力分组', [item])])
    const box = wrapper.get('[data-testid="upstream-site-issues"]')
    expect(box.text()).toContain('到上游站点页查看这个站点的详细错误后重新同步')
    expect(box.text()).not.toContain('其他平台站')
    expect(box.findAll('a').map(a => a.attributes('href'))).toEqual(['/admin/upstream'])
    expect(wrapper.text()).not.toContain('站点问题见上方')
    expect(wrapper.find('a[href="/admin/group-rates?cleanup=stale"]').exists()).toBe(false)
  })
})

describe('reconciling an unconfirmed account state change', () => {
  it('reads the pending and current values before committing, then refreshes both read paths', async () => {
    priorityFailure({
      errorKey: 'admin.connectionHealth.errors.priorityWaitingTimeout',
      actionDiagnostics: [diagnosis('468')], failedTargets: [{ accountId: '468', reason: 'waiting_timeout' }],
    })
    fetchMock.mockResolvedValueOnce(json(actionPreview)).mockResolvedValueOnce(json({}))
    const wrapper = await open([])
    expect(wrapper.text()).toContain('有账号超过 5 分钟仍未能写回 Priority')
    expect(wrapper.text()).toContain('（该账号另有待确认修改，见上方）')
    pickButton('核对并结束').click()
    await flushPromises()
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/connection-health/accounts/468/remote-action/current')
    expect(fetchMock.mock.calls[0]?.[1]?.method ?? 'GET').toBe('GET')
    expect(dialog().textContent).toContain('改成「启用」')
    expect(dialog().textContent).toContain('主站现在是「停用」')
    expect(dialog().textContent).toContain('最后记录时间')
    expect(dialog().textContent).toMatch(/203[01]/)
    expect(dialog().textContent).toContain('健康恢复后可能被自动改回「启用」')
    expect(dialog().textContent).toContain('本操作不会修改主站')
    pickButton('确认结束', dialog()).click()
    await flushPromises()
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/connection-health/accounts/468/remote-action/confirm-current')
    expect(fetchMock.mock.calls[1]?.[1]?.method).toBe('POST')
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual({ dispatchId: 'dispatch-tested', currentValue: 'inactive' })
    expect(wrapper.text()).toContain('已结束这条待确认记录。下一轮同步会重新检查这个账号。')
    expect(harness.getPriorityStatus).toHaveBeenCalledTimes(2)
    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)
  })

  it('omits the automatic management warning for a manual status change', async () => {
    priorityFailure({ actionDiagnostics: [diagnosis('468')] })
    fetchMock.mockResolvedValueOnce(json({ ...actionPreview, source: 'manual' }))
    await open([])
    pickButton('核对并结束').click()
    await flushPromises()
    expect(pickButton('确认结束', dialog())).toBeTruthy()
    expect(dialog().textContent).not.toContain('健康恢复后可能被自动改回')
  })

  it('leaves a legacy record read-only and omits controls for other actions and reasons', async () => {
    priorityFailure({ actionDiagnostics: [diagnosis('468'), diagnosis('469', 'schedulable'), diagnosis('470', 'priority'), diagnosis('471', 'status', 'conflict'), diagnosis('472', 'status', 'not_visible')],
      failedTargets: [{ accountId: '472', reason: 'waiting_timeout' }] })
    fetchMock.mockResolvedValueOnce(json({ canConfirm: false, blockedReason: 'legacy_alias' }))
    const wrapper = await open([])
    expect(wrapper.findAll('button').filter(button => button.text() === '核对并结束')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('（该账号另有待确认修改，见上方）')
    pickButton('核对并结束').click()
    await flushPromises()
    expect(dialog().textContent).toContain('这条记录使用旧格式保存，本次没有修改')
    expect(dialog().textContent).not.toContain('确认结束')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['none', '这条记录已经结束，刷新页面即可'],
    ['not_uncertain', '这条记录不是“结果未知”状态'],
    ['unsupported_action', '这类修改暂不支持在这里结束'],
    ['multiple_pending', '这个账号有多条待确认记录'],
    ['original_running', '原来的修改可能还在进行'],
    ['account_not_found', '主站账号列表里找不到这个账号'],
    ['main_site_status_unknown', '主站返回的账号状态是「0.5」'],
    ['session_unavailable', '主站登录资料不可用'],
    ['main_site_unavailable', '读不到主站账号列表：上游请求超时'],
  ])('previews %s as a read-only explanation', async (blockedReason, expected) => {
    priorityFailure({ actionDiagnostics: [diagnosis('468')] })
    fetchMock.mockResolvedValueOnce(json({ canConfirm: false, blockedReason, currentValue: '0.5', detail: '上游请求超时' }))
    await open([])
    pickButton('核对并结束').click()
    await flushPromises()
    expect(dialog().textContent).toContain(expected)
    expect(dialog().textContent).not.toContain('确认结束')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('drops a late preview when the workspace changes and suppresses confirmation in NewAPI', async () => {
    priorityFailure({ actionDiagnostics: [diagnosis('468')] })
    const request = deferred<Response>()
    fetchMock.mockReturnValueOnce(request.promise)
    const wrapper = await open([])
    pickButton('核对并结束').click()
    await nextTick()
    expect(dialog()).not.toBeNull()
    harness.workspace.value = { ...harness.workspace.value, id: 'ws2', platform: 'newapi' }
    await nextTick()
    request.resolve(json(actionPreview))
    await flushPromises()
    expect(dialog()).toBeNull()
    expect(wrapper.text()).not.toContain('核对并结束')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('shows the safe backend reason when the account action could not be read', async () => {
    priorityFailure({ actionDiagnostics: [diagnosis('468')] })
    fetchMock.mockResolvedValueOnce(json({ blockedReason: 'read_failed', detail: '未能读取这个账号的 Priority 待确认记录，本次没有做任何修改。' }, 500))
    await open([])
    pickButton('核对并结束').click()
    await flushPromises()
    expect(dialog().textContent).toContain('未能读取这个账号的 Priority 待确认记录，本次没有做任何修改。')
    expect(dialog().textContent).not.toContain('确认结束')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each([
    [409, 'changed', '未能核对这条记录的最新状态，本次没有做任何修改'],
    [409, 'current_value_changed', '主站当前状态已变化'],
    [409, 'original_running', '原来的修改可能还在进行'],
    [409, 'multiple_pending', '这个账号有多条待确认记录'],
    [409, 'reconcile_failed', '没有核对到一致的结果'],
    [409, 'main_site_status_unknown', '主站返回的账号状态是「error」'],
    [409, 'main_site_unavailable', '读不到主站账号列表：上游请求超时'],
    [500, 'read_failed', '未能查看原操作，本次没有做任何修改'],
    [500, 'prepare_failed', '未能查看原操作，本次没有做任何修改'],
    [500, 'result_unknown', '结果没有确认，请刷新页面查看这条记录是否已经结束'],
    [500, 'unexpected', '结果没有确认，请刷新页面查看这条记录是否已经结束'],
  ])('shows a specific result for HTTP %s %s without enabling another blind confirmation', async (status, blockedReason, expected) => {
    priorityFailure({ actionDiagnostics: [diagnosis('468')] })
    const detail = blockedReason === 'read_failed' || blockedReason === 'prepare_failed'
      ? '未能查看原操作，本次没有做任何修改。'
      : '上游请求超时'
    fetchMock.mockResolvedValueOnce(json(actionPreview))
      .mockResolvedValueOnce(json({ message: blockedReason, blockedReason, currentValue: 'error', detail }, status))
    await open([])
    pickButton('核对并结束').click()
    await flushPromises()
    pickButton('确认结束', dialog()).click()
    await flushPromises()
    expect(dialog().textContent).toContain(expected)
    if (status === 409) expect(dialog().textContent).toContain('本次没有做任何修改')
    expect([...dialog().querySelectorAll('button')].some(button => button.textContent?.trim() === '确认结束')).toBe(false)
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()
  })
})
