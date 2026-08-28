// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'

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

describe('connection health refresh progress behavior', () => {
  it('passes the complete workspace group snapshot to the setup drawer', async () => {
    harness.activeWorkspaceScope = 'ws1'
    harness.refs.adminGroups.value = [
      { id: 'group-visible', name: '当前可见分组', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [] },
      { id: 'group-hidden', name: '仍需参与来源判断的分组', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [] },
    ]
    const SetupGroupsProbe = defineComponent({
      name: 'GroupHealthSetupDrawer',
      props: {
        allGroups: { type: Array, default: () => [] },
      },
      setup(props) {
        return () => h('div', { 'data-test': 'setup-all-groups' }, (props.allGroups as Array<{ name: string }>).map(group => group.name).join(','))
      },
    })

    const wrapper = await mountView({ GroupHealthSetupDrawer: SetupGroupsProbe })

    expect(wrapper.get('[data-test="setup-all-groups"]').text()).toBe('当前可见分组,仍需参与来源判断的分组')
  })

  it('keeps the setup group and complete group list on the same opening snapshot during a refresh', async () => {
    harness.activeWorkspaceScope = 'ws1'
    harness.refs.adminGroups.value = [
      { id: 'group-current', name: '打开时当前分组', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [] },
      { id: 'group-remaining', name: '打开时剩余分组', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [] },
    ]
    const GroupDetailProbe = defineComponent({
      name: 'AdminGroupHealthDetail',
      props: { group: { type: Object, required: true } },
      emits: ['setup'],
      setup(props, { emit }) {
        return () => h('button', {
          'data-test': 'open-setup',
          onClick: () => emit('setup', props.group),
        }, (props.group as { name: string }).name)
      },
    })
    const SetupSnapshotProbe = defineComponent({
      name: 'GroupHealthSetupDrawer',
      props: {
        open: Boolean,
        group: { type: Object, default: null },
        allGroups: { type: Array, default: () => [] },
      },
      setup(props) {
        return () => props.open
          ? h('div', { 'data-test': 'setup-snapshot' }, [
              (props.group as { name?: string } | null)?.name ?? '',
              '|',
              (props.allGroups as Array<{ name: string }>).map(group => group.name).join(','),
            ])
          : null
      },
    })
    const wrapper = await mountView({
      AdminGroupHealthDetail: GroupDetailProbe,
      GroupHealthSetupDrawer: SetupSnapshotProbe,
    })

    await wrapper.get('[data-test="open-setup"]').trigger('click')
    const openedGroupName = wrapper.get('[data-test="open-setup"]').text()
    expect(wrapper.get('[data-test="setup-snapshot"]').text()).toBe(
      `${openedGroupName}|打开时当前分组,打开时剩余分组`,
    )

    harness.refs.adminGroups.value = [
      { id: 'group-current', name: '刷新后当前分组', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [] },
      { id: 'group-remaining', name: '刷新后剩余分组', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [] },
    ]
    await nextTick()

    expect(wrapper.get('[data-test="open-setup"]').text()).toBe(openedGroupName.replace('打开时', '刷新后'))
    expect(wrapper.get('[data-test="setup-snapshot"]').text()).toBe(
      `${openedGroupName}|打开时当前分组,打开时剩余分组`,
    )
  })

  it('reloads the main group list after a successful setup save and renders the refreshed result', async () => {
    harness.activeWorkspaceScope = 'ws1'
    harness.refs.adminGroups.value = [{
      id: 'group-a', name: '保存前局部结果', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [],
    }]
    harness.loadAdminGroups.mockImplementationOnce(async () => {
      harness.refs.adminGroups.value = [{
        id: 'group-a', name: '后端重新读取结果', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [],
      }]
      return true
    })
    const GroupDetailProbe = defineComponent({
      name: 'AdminGroupHealthDetail',
      props: { group: { type: Object, required: true } },
      emits: ['setup'],
      setup(props, { emit }) {
        return () => h('button', {
          'data-test': 'open-setup',
          onClick: () => emit('setup', props.group),
        }, (props.group as { name: string }).name)
      },
    })
    const SetupSaveProbe = defineComponent({
      name: 'GroupHealthSetupDrawer',
      props: { open: Boolean },
      emits: ['saved'],
      setup(props, { emit }) {
        return () => props.open
          ? h('button', {
              'data-test': 'save-setup',
              onClick: () => emit('saved', {
                adminGroupId: 'group-a', adminGroupName: '保存前局部结果',
                policyIds: [], policies: [], excludedTargetIds: [], prioritySyncStatus: 'success',
              }),
            }, '保存配置')
          : null
      },
    })
    const wrapper = await mountView({
      AdminGroupHealthDetail: GroupDetailProbe,
      GroupHealthSetupDrawer: SetupSaveProbe,
    })

    expect(wrapper.get('[data-test="open-setup"]').text()).toBe('保存前局部结果')
    await wrapper.get('[data-test="open-setup"]').trigger('click')
    await wrapper.get('[data-test="save-setup"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="open-setup"]').text()).toBe('后端重新读取结果')
  })

  it('retries the authoritative group reload after an overlapping terminal refresh finishes', async () => {
    harness.activeWorkspaceScope = 'ws1'
    harness.refs.adminGroups.value = [{
      id: 'group-a', name: '保存前局部结果', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [],
    }]
    const refresh = deferred<boolean>()
    let refreshActive = true
    harness.refreshAdminGroups.mockReset().mockReturnValueOnce(refresh.promise)
    harness.loadAdminGroups.mockImplementation(async () => {
      if (refreshActive) return false
      harness.refs.adminGroups.value = [{
        id: 'group-a', name: '刷新收口后的权威结果', platform: 'sub2api', type: 'public', accountCount: 0, accounts: [],
      }]
      return true
    })
    const GroupDetailProbe = defineComponent({
      name: 'AdminGroupHealthDetail',
      props: { group: { type: Object, required: true } },
      emits: ['setup'],
      setup(props, { emit }) {
        return () => h('button', {
          'data-test': 'open-setup',
          onClick: () => emit('setup', props.group),
        }, (props.group as { name: string }).name)
      },
    })
    const SetupSaveProbe = defineComponent({
      name: 'GroupHealthSetupDrawer',
      props: { open: Boolean },
      emits: ['saved'],
      setup(props, { emit }) {
        return () => props.open
          ? h('button', {
              'data-test': 'save-setup',
              onClick: () => emit('saved', {
                adminGroupId: 'group-a', adminGroupName: '保存前局部结果',
                policyIds: [], policies: [], excludedTargetIds: [], prioritySyncStatus: 'success',
              }),
            }, '保存配置')
          : null
      },
    })
    const wrapper = await mountView({
      AdminGroupHealthDetail: GroupDetailProbe,
      GroupHealthSetupDrawer: SetupSaveProbe,
    })

    await findButton(wrapper, '刷新').trigger('click')
    await wrapper.get('[data-test="open-setup"]').trigger('click')
    await wrapper.get('[data-test="save-setup"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="open-setup"]').text()).toBe('保存前局部结果')

    refreshActive = false
    refresh.resolve(true)
    await flushPromises()

    expect(wrapper.get('[data-test="open-setup"]').text()).toBe('刷新收口后的权威结果')
  })

  it('ends the primary spinner when terminal arrives even if every auxiliary promise remains pending', async () => {
    const refresh = deferred<boolean>()
    const never = new Promise<boolean>(() => undefined)
    const wrapper = await mountView()
    harness.refreshAdminGroups.mockReset().mockReturnValueOnce(refresh.promise)
    harness.loadPolicies.mockReset().mockReturnValue(never)
    harness.loadEvents.mockReset().mockReturnValue(never)
    harness.getPrioritySyncStatus.mockReset().mockReturnValue(never)

    const refreshButton = findButton(wrapper, '刷新')
    harness.refs.isLoading.value = true
    await nextTick()
    expect(refreshButton.attributes('disabled')).toBeUndefined()

    await refreshButton.trigger('click')
    await nextTick()
    expect(refreshButton.attributes('disabled')).toBeDefined()
    expect(findButton(wrapper, '策略').attributes('disabled')).toBeUndefined()
    expect(findButton(wrapper, '事件').attributes('disabled')).toBeUndefined()

    refresh.resolve(true)
    await flushPromises()

    expect(refreshButton.attributes('disabled')).toBeUndefined()
    expect(findButton(wrapper, '策略').attributes('disabled')).toBeUndefined()
    expect(findButton(wrapper, '事件').attributes('disabled')).toBeUndefined()
  })

  it('shows stage counts, accumulated issues and current waiters while preserving the existing list area', async () => {
    const refresh = deferred<boolean>()
    const wrapper = await mountView()
    harness.refreshAdminGroups.mockReset().mockReturnValueOnce(refresh.promise)
    await findButton(wrapper, '刷新').trigger('click')
    harness.refs.refreshRunSnapshot.value = {
      runId: 'run-progress-1', revision: 6, runState: 'running', stage: 'multiplier_refresh',
      stageCompletedSites: 2, stageTotalSites: 3,
      waiting: [{ siteId: 'site-wait', siteName: '阻塞站点', phase: 'multiplier_refresh', elapsedSeconds: 41 }],
      issues: [{ siteId: 'site-failed', siteName: '失败站点', phase: 'site_sync', status: 'failed', errorKey: 'site_sync_network' }],
    }
    await nextTick()

    expect(wrapper.text()).toContain('倍率')
    expect(wrapper.text()).toContain('2 / 3')
    expect(wrapper.text()).toContain('阻塞站点')
    expect(wrapper.text()).toContain('41')
    expect(wrapper.text()).toContain('失败站点')
    expect(wrapper.text()).toContain('网络')
    expect(wrapper.find('[data-test="connection-health-list"]').exists() || wrapper.text().includes('暂无')).toBe(true)
    refresh.resolve(true)
  })

  it('shows the confirmed manual-against-automatic notice without disabling unrelated entries', async () => {
    const wrapper = await mountView()
    harness.refs.refreshConflictNotice.value = '自动刷新正在进行，本次未执行强制刷新'
    harness.refs.refreshRunSnapshot.value = {
      runId: 'run-auto-1', revision: 2, runState: 'running', stage: 'site_sync',
      stageCompletedSites: 0, stageTotalSites: 1, waiting: [], issues: [],
    }
    await nextTick()

    expect(wrapper.text()).toContain('自动刷新正在进行，本次未执行强制刷新')
    expect(findButton(wrapper, '策略').attributes('disabled')).toBeUndefined()
    expect(findButton(wrapper, '事件').attributes('disabled')).toBeUndefined()
  })

  it('shows a failed main-groups terminal instead of the previous successful refresh state', async () => {
    const wrapper = await mountView()
    harness.refs.terminalRefreshSummary.value = {
      state: 'failure',
      errorKey: 'main_groups_unavailable',
      sites: [],
    }
    await nextTick()

    expect(wrapper.text()).toContain('本轮刷新失败，已保留旧数据')
    expect(wrapper.text()).toContain('主站分组读取')
    expect(wrapper.text()).not.toContain('本轮刷新全部成功')
  })

  it('keeps the fatal terminal reason visible beside failed and disabled site summaries', async () => {
    const wrapper = await mountView()
    harness.refs.terminalRefreshSummary.value = {
      state: 'failure',
      errorKey: 'main_groups_unavailable',
      sites: [
        { siteId: 'site-failed', status: 'unavailable', errorKey: 'site_sync_network' },
        { siteId: 'site-wait', status: 'disabled', errorKey: 'site_sync_disabled' },
      ],
    }
    await nextTick()

    expect(wrapper.text()).toContain('失败站点')
    expect(wrapper.text()).toContain('失败站点: 上游站点同步：网络不可达')
    expect(wrapper.text()).toContain('未参与本轮')
    expect(wrapper.text()).toContain('阻塞站点: 上游站点同步：站点已禁用')
    expect(wrapper.text()).toContain('本轮刷新失败，已保留旧数据')
    expect(wrapper.text()).toContain('主站分组读取')
    expect(wrapper.text()).not.toContain('本轮刷新全部成功')
  })

  it('uses different safe messages for queue timeout and started request timeout', async () => {
    const refresh = deferred<boolean>()
    const wrapper = await mountView()
    harness.refreshAdminGroups.mockReset().mockReturnValueOnce(refresh.promise)
    await findButton(wrapper, '刷新').trigger('click')
    harness.refs.refreshRunSnapshot.value = {
      runId: 'run-timeouts', revision: 4, runState: 'running', stage: 'multiplier_refresh',
      stageCompletedSites: 2, stageTotalSites: 3,
      waiting: [],
      issues: [
        { siteId: 'site-queue', siteName: '排队站点', phase: 'multiplier_refresh', status: 'timeout', errorKey: 'multiplier_queue_timeout' },
        { siteId: 'site-request', siteName: '请求站点', phase: 'multiplier_refresh', status: 'timeout', errorKey: 'multiplier_request_timeout' },
      ],
    }
    await nextTick()

    expect(wrapper.text()).toContain('排队超时（未发起上游请求）')
    expect(wrapper.text()).toContain('上游请求超时')
    refresh.resolve(true)
  })

  it('shows that the progress connection is interrupted while bounded reconnect is running', async () => {
    const wrapper = await mountView()
    harness.refs.refreshConnectionState.value = 'reconnecting'
    await nextTick()

    expect(wrapper.text()).toContain('进度连接中断，正在重新连接')
  })

  it('cancels the page subscription on unmount and does not render workspace A groups after entering workspace B', async () => {
    const refreshA = deferred<boolean>()
    harness.activeWorkspaceScope = 'workspace-a'
    harness.currentAccount.value = { id: 'workspace-a', displayName: '工作区 A' }
    harness.refs.adminGroups.value = [{
      id: 'group-a', name: '仅属于工作区 A', platform: 'sub2api', type: 'public',
      accountCount: 0, monitoredAccountCount: 0, accounts: [],
    }]
    harness.refreshAdminGroupsAutomatically.mockReset().mockReturnValueOnce(refreshA.promise)
    const wrapperA = await mountView()
    expect(wrapperA.text()).toContain('仅属于工作区 A')

    wrapperA.unmount()
    mountedWrappers.splice(mountedWrappers.indexOf(wrapperA), 1)
    harness.currentAccount.value = { id: 'workspace-b', displayName: '工作区 B' }
    harness.refreshAdminGroupsAutomatically.mockResolvedValueOnce(true)
    const wrapperB = await mountView()

    expect(harness.cancelAdminGroupsRefresh).toHaveBeenCalledTimes(1)
    expect(harness.setAdminGroupsWorkspace).toHaveBeenCalledWith('workspace-b')
    expect(wrapperB.text()).not.toContain('仅属于工作区 A')
    expect(wrapperB.text()).toContain('暂无')

    harness.refs.adminGroups.value = [{
      id: 'group-b', name: '仅属于工作区 B', platform: 'sub2api', type: 'public',
      accountCount: 0, monitoredAccountCount: 0, accounts: [],
    }]
    await nextTick()
    expect(wrapperB.text()).toContain('仅属于工作区 B')
    expect(wrapperB.text()).not.toContain('仅属于工作区 A')
    refreshA.resolve(false)
  })
})
