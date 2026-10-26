// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import TargetPolicyAssignmentDialog from '@/modules/admin/components/dashboard/TargetPolicyAssignmentDialog.vue'
// 20261016/4：分组健康页新增已对接站点读取；本用例与它无关，隔离为空列表，不改原断言。
vi.mock('@/modules/admin/api/mySites', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()), listRealConnections: vi.fn(async () => harness.connections) }))

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  getPrioritySyncStatus: vi.fn(),
  connections: [] as Record<string, unknown>[],
  currentAccount: null as any,
  loadTargetPolicyAssignments: vi.fn(),
  saveTargetPolicyAssignments: vi.fn(),
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
  listUpstreamSites: async () => [{ id: 'site-1', name: '原上游站点' }],
}))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'ws1', displayName: '测试工作区', platform: 'sub2api' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})
vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  const { ref } = await import('vue')
  harness.refs = {
    overview: ref(null), groups: ref([]), adminGroups: ref([]), adminGroupsLoaded: ref(true),
    events: ref([]), policies: ref([]), isLoading: ref(false), isActionLoading: ref(false), errorKey: ref(''),
    terminalRefreshSummary: ref(null), refreshRunSnapshot: ref(null), refreshConflictNotice: ref(''),
    refreshConnectionState: ref('connected'),
  }
  return {
    ...actual,
    useConnectionHealth: () => ({
      ...harness.refs,
      loadAll: async () => undefined,
      loadGroups: async () => true,
      loadAdminGroups: async () => true,
      refreshAdminGroups: async () => true,
      refreshAdminGroupsAutomatically: async () => true,
      loadEvents: async () => true,
      loadPolicies: async () => true,
      cancelAdminGroupsRefresh: vi.fn(),
      setAdminGroupsWorkspace: vi.fn(),
      invalidateAdminGroupsReads: vi.fn(),
      removePolicy: async () => true,
      savePolicy: async () => true,
      updateTargetSchedulable: async () => true,
      loadTargetPolicyAssignments: harness.loadTargetPolicyAssignments,
      saveTargetPolicyAssignments: harness.saveTargetPolicyAssignments,
    }),
  }
})

const wrappers: VueWrapper[] = []
const mountView = async (status: Record<string, unknown>) => {
  harness.getPrioritySyncStatus.mockResolvedValue({
    workspaceId: 'ws1', status: 'failed', failedCount: 1,
    lastFailureAt: '2031-02-03T04:05:06Z', ...status,
  })
  const wrapper = mount(ConnectionHealthView, {
    global: { stubs: {
      Button: { template: '<button v-bind="$attrs"><slot /></button>' },
      AdminGroupHealthDetail: true,
      ConnectionHealthEventsDialog: true, GroupHealthSetupDrawer: true,
      ManualOneTimeProbeDialog: true, QuestionAnswerBatchDrawer: true,
      PolicyConfigDrawer: true, ProbePolicyListDialog: true, TargetPolicyAssignmentDialog: true,
    } },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2031-02-03T04:05:06Z'))
  harness.getPrioritySyncStatus.mockReset()
  harness.connections = []
  harness.currentAccount.value = { id: 'ws1', displayName: '测试工作区', platform: 'sub2api' }
  harness.loadTargetPolicyAssignments.mockReset().mockResolvedValue({ assignments: { policyIds: ['policy1'] } })
  harness.saveTargetPolicyAssignments.mockReset().mockResolvedValue({ assignments: { policyIds: ['policy1'] } })
  harness.refs.adminGroups.value = []
  localStorage.clear()
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.useRealTimers()
  localStorage.clear()
  document.body.innerHTML = ''
})

const UNKNOWN_REASON = '原因未分类，请查看服务器日志中的 [connection-health] priority sync 记录。'
const REASONS = [
  ['priorityTargetNotVisible', 'not_visible', '有账号已不在任何分组，无法恢复它的原 Priority；请把账号加回分组，或在主站删除它。', '当前分组列表中找不到它'],
  ['priorityWriteFailed', 'write_failed', '写入主站 Priority 失败，系统将在后台重试。', 'Priority 写入失败'],
  ['priorityWaitingTimeout', 'waiting_timeout', '有账号超过 5 分钟仍未能写回 Priority，系统将继续重试。', '超过5分钟仍未确认 Priority 写回'],
  ['priorityTargetFailed', 'other', '部分账号的 Priority 未能写回，系统将在后台重试；原因见服务器日志。', 'Priority 处理未完成'],
] as const

describe('Priority failure reasons in the real status banner', () => {
  it('keeps the named account in policy loading and saving errors, saves original IDs, and cancels without a save', async () => {
    const targetId = 'sub2api:ws1:229'
    const policy = { id: 'policy1', name: '既有策略', enabled: true, modelTargets: [], probeIntervalSeconds: 60 } as any
    harness.loadTargetPolicyAssignments.mockResolvedValueOnce({ errorKey: 'admin.connectionHealth.errors.network' })
    const wrapper = mount(TargetPolicyAssignmentDialog, { props: {
      open: false, targetId, accountName: '', accountLabel: { name: '策略分配对应账号', siteNames: ['策略网站'] }, policies: [policy],
    }, global: { stubs: { Teleport: true } } }); wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises()
    expect(wrapper.text()).toContain('账号「策略分配对应账号」（网站「策略网站」）')
    expect(wrapper.text()).not.toContain(targetId)
    expect(harness.loadTargetPolicyAssignments).toHaveBeenCalledWith(targetId)
    await wrapper.setProps({ open: false }); await wrapper.setProps({ open: true }); await flushPromises()
    harness.saveTargetPolicyAssignments.mockResolvedValueOnce({ errorKey: 'admin.connectionHealth.errors.network' })
    const save = wrapper.findAll('button').find(button => button.text() === '保存')!
    await save.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('策略分配对应账号')
    expect(harness.saveTargetPolicyAssignments).toHaveBeenCalledWith(targetId, ['policy1'])
    const calls = harness.saveTargetPolicyAssignments.mock.calls.length
    await wrapper.findAll('button').find(button => button.text() === '取消')!.trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(harness.saveTargetPolicyAssignments).toHaveBeenCalledTimes(calls)
  })
  it('identifies an ungrouped account from the existing connection name and website instead of its number', async () => {
    harness.connections = [
      { adminPlatform: 'newapi', adminAccountId: '229', adminAccountName: '其他平台不得使用', upstreamSiteId: 'site-1' },
      { adminPlatform: 'sub2api', adminAccountId: '229', adminAccountName: '完整账号229保留名称中的数字', upstreamSiteId: 'site-1' },
    ]
    const wrapper = await mountView({ failedTargets: [{ accountId: '229', reason: 'not_visible' }] })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain('账号「完整账号229保留名称中的数字」（网站「原上游站点」）')
    expect(banner.text()).toContain('请先到主站账号列表核对')
    expect(banner.text()).not.toContain('#229')
    expect(banner.text()).not.toContain('其他平台不得使用')
  })

  it('rejects a previous workspace status even when the account number is the same', async () => {
    const wrapper = await mountView({ workspaceId: 'ws2', failedTargets: [{ accountId: '229', accountName: '其他工作区不得显示', reason: 'not_visible' }] })
    expect(wrapper.text()).not.toContain('其他工作区不得显示')
  })

  it('ignores a late status after switching away and back to the same workspace', async () => {
    let resolve!: (value: any) => void
    harness.getPrioritySyncStatus.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = await mountView({ failedTargets: [] })
    harness.currentAccount.value = { id: 'ws2', displayName: '另一个工作区', platform: 'sub2api' }; await flushPromises()
    harness.currentAccount.value = { id: 'ws1', displayName: '测试工作区', platform: 'sub2api' }; await flushPromises()
    resolve({ workspaceId: 'ws1', status: 'failed', failedCount: 1, failedTargets: [{ accountId: '229', accountName: '已经失效的读取', reason: 'not_visible' }] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('已经失效的读取')
  })

  it('shows the known live name before a stale name in a status response', async () => {
    harness.refs.adminGroups.value = [{ id: 'g1', name: '分组', accounts: [{ id: '229', targetId: 'sub2api:ws1:229', name: '主站当前完整名称' }] }]
    const wrapper = await mountView({ failedTargets: [{ accountId: '229', accountName: '之前的名称', reason: 'write_failed' }] })
    expect(wrapper.get('[role="alert"]').text()).toContain('主站当前完整名称')
    expect(wrapper.get('[role="alert"]').text()).not.toContain('之前的名称')
  })

  it.each(REASONS)('shows %s and the failed account without a group-health reading error', async (key, reason, message, label) => {
    const wrapper = await mountView({
      errorKey: `admin.connectionHealth.errors.${key}`,
      failedTargets: [{ accountId: '242', accountName: '失败账号', reason }],
    })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain(message)
    expect(banner.text()).toContain(`账号「失败账号」：${label}`)
    expect(banner.text()).not.toContain('暂时无法读取分组健康数据')
    expect(banner.text()).not.toContain(UNKNOWN_REASON)
    expect(banner.findAll('li')).toHaveLength(1)
  })

  it.each(['admin.connectionHealth.errors.unknown', 'unrecognized_priority_reason', ''])('uses the Priority fallback for %s', async (errorKey) => {
    const wrapper = await mountView({ errorKey })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain(UNKNOWN_REASON)
    expect(banner.text()).not.toContain('暂时无法读取分组健康数据')
    expect(banner.text()).not.toContain('unrecognized_priority_reason')
    expect(banner.find('ul').exists()).toBe(false)
  })

  it('shows ten returned accounts and the remaining count, with missing-name and unknown-reason fallbacks', async () => {
    const failedTargets = Array.from({ length: 10 }, (_, index) => ({
      accountId: String(100 + index),
      ...(index === 0 ? {} : { accountName: `失败账号${index}` }),
      reason: index === 0 ? 'future_reason' : 'write_failed',
    }))
    const wrapper = await mountView({
      errorKey: 'admin.connectionHealth.errors.priorityWriteFailed', failedCount: 13, failedTargets,
    })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain('账号（名称暂未读到）：Priority 处理未完成')
    expect(banner.text()).toContain('账号「失败账号9」：Priority 写入失败')
    expect(banner.text()).toContain('另有 3 个账号')
    expect(banner.text()).not.toContain('future_reason')
    expect(banner.findAll('li')).toHaveLength(11)
  })

  it('does not invent a failure list after a restart when failedTargets is omitted', async () => {
    const wrapper = await mountView({ errorKey: 'admin.connectionHealth.errors.priorityWriteFailed', failedCount: 13 })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain(REASONS[1][2])
    expect(banner.find('ul').exists()).toBe(false)
    expect(banner.text()).not.toContain('另有')
  })

  it('keeps the partial banner, multiplier blockers and changed-details notice without showing failedTargets', async () => {
    harness.refs.adminGroups.value = [{
      id: 'group-1', name: '原分组', platform: 'sub2api', type: 'public', accountCount: 1,
      accounts: [{ id: '101', name: '倍率异常账号', targetId: 'sub2api:ws1:101',
        upstreamSiteId: 'site-1', prioritySyncBlocked: true, prioritySyncBlockReason: 'binding_missing' }],
    }]
    const wrapper = await mountView({
      status: 'partial', errorKey: 'admin.connectionHealth.errors.priorityMetadataUnavailable', failedCount: 2,
      failedTargets: [{ accountId: '242', accountName: '不得展示的失败账号', reason: 'write_failed' }],
    })
    const banner = wrapper.findAll('[role="status"]').find(item => item.text().includes('Priority 本轮部分完成'))!
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('2 项倍率资料异常')
    expect(banner.text()).toContain('其他账号已正常调度，系统将在后台重试。')
    expect(banner.text()).toContain('倍率异常账号 · 原上游站点：上游站点或 Key 绑定不完整')
    expect(banner.text()).toContain('当前账号资料已发生变化，后台状态将在下一轮重试后更新。')
    expect(banner.text()).not.toContain('不得展示的失败账号')
    expect(banner.text()).not.toContain('后台同步失败')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('keeps multiplier blockers visible alongside a failed Priority round', async () => {
    harness.refs.adminGroups.value = [{
      id: 'group-1', name: '原分组', platform: 'sub2api', type: 'public', accountCount: 1,
      accounts: [{ id: '101', name: '倍率异常账号', targetId: 'sub2api:ws1:101',
        upstreamSiteId: 'site-1', prioritySyncBlocked: true, prioritySyncBlockReason: 'binding_missing' }],
    }]
    const wrapper = await mountView({
      errorKey: 'admin.connectionHealth.errors.priorityWriteFailed',
      failedTargets: [{ accountId: '242', accountName: '失败账号', reason: 'write_failed' }],
    })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain('账号「失败账号」：Priority 写入失败')
    expect(banner.text()).toContain('倍率异常账号 · 原上游站点：上游站点或 Key 绑定不完整')
    expect(banner.text()).not.toContain('当前账号资料已发生变化')
  })
})
