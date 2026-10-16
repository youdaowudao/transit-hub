// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
// 20261016/4：分组健康页新增已对接站点读取；本用例与它无关，隔离为空列表，不改原断言。
vi.mock('@/modules/admin/api/mySites', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()), listRealConnections: vi.fn(async () => []) }))

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  currentAccount: null as any,
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
  cancelAdminGroupsRefresh: vi.fn(),
  setAdminGroupsWorkspace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn(async () => undefined) }),
}))

vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  return {
    useDocumentVisibility: () => ref('hidden'),
    useIntervalFn: () => ({ pause: vi.fn(), resume: vi.fn() }),
  }
})

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  getPrioritySyncStatus: harness.getPrioritySyncStatus,
}))

vi.mock('@/modules/admin/api/upstream', () => ({
  listUpstreamSites: harness.listUpstreamSites,
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

const mountedWrappers: VueWrapper[] = []

const mountView = async (componentStubs: Record<string, unknown> = {}) => {
  const wrapper = mount(ConnectionHealthView, {
    global: {
      stubs: {
        Button: { template: '<button v-bind="$attrs"><slot /></button>' },
        AdminGroupHealthDetail: true,
        ConnectionHealthEventsDialog: true,
        GroupHealthSetupDrawer: true,
        ManualOneTimeProbeDialog: true,
        PolicyConfigDrawer: true,
        ProbePolicyListDialog: true,
        TargetPolicyAssignmentDialog: true,
        ...componentStubs,
      },
    },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const findButton = (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => item.text().trim() === label || item.text().includes(label))
  if (!button) throw new Error(`missing button: ${label}`)
  return button
}

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

beforeEach(() => {
  for (const refValue of Object.values(harness.refs)) refValue.value = Array.isArray(refValue.value) ? [] : null
  if (harness.refs.isLoading) harness.refs.isLoading.value = false
  if (harness.refs.isActionLoading) harness.refs.isActionLoading.value = false
  if (harness.refs.errorKey) harness.refs.errorKey.value = ''
  if (harness.refs.refreshConflictNotice) harness.refs.refreshConflictNotice.value = ''
  if (harness.refs.refreshConnectionState) harness.refs.refreshConnectionState.value = 'connected'
  harness.loadAll.mockReset().mockResolvedValue(undefined)
  harness.loadGroups.mockReset().mockResolvedValue(true)
  harness.loadAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroupsAutomatically.mockReset().mockResolvedValue(true)
  harness.loadEvents.mockReset().mockResolvedValue(true)
  harness.loadPolicies.mockReset().mockResolvedValue(true)
  harness.getPrioritySyncStatus.mockReset().mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0 })
  harness.listUpstreamSites.mockReset().mockResolvedValue([{ id: 'site-wait', name: '阻塞站点' }, { id: 'site-failed', name: '失败站点' }])
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
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('healthy refresh preserves precise group rates warning', () => {
  it('shows the warning during refresh and after successful health completion without marking all modules failed', async () => {
    const waiting = deferred<boolean>()
    harness.refreshAdminGroups.mockReturnValueOnce(waiting.promise)
    const wrapper = await mountView()
    await findButton(wrapper, '刷新').trigger('click')
    const warning = { message: '倍率保存结果尚未确认', detail: '提交连接中断，不能确认哪些倍率已更新或清理', next: '请核查数据库提交结果后再同步；主站账号未修改' }
    harness.refs.refreshRunSnapshot.value = { runId: 'warning-run', revision: 2, runState: 'running', stage: 'site_sync', stageCompletedSites: 1, stageTotalSites: 1, waiting: [], issues: [{ siteId: 'site-failed', siteName: '测试网站', phase: 'group_rates', status: 'success', errorKey: '', groupRatesWarning: warning }] }
    await nextTick()
    expect(wrapper.text()).toContain(warning.detail)
    harness.refs.terminalRefreshSummary.value = { state: 'success', sites: [] }
    waiting.resolve(true)
    await flushPromises()
    expect(wrapper.get('[data-group-rates-warning]').text()).toContain(warning.message)
    expect(wrapper.get('[data-group-rates-warning]').text()).toContain(warning.next)
    expect(wrapper.get('[data-group-rates-warning]').text()).not.toContain('成本都失败')
    expect(wrapper.get('[data-group-rates-warning]').text()).not.toContain('全部回滚')
  })
})
