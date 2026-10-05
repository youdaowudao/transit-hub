// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  getPrioritySyncStatus: vi.fn(),
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
  return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws1', displayName: '测试工作区' }) }) }
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
      invalidatePriorityCandidatePlanNow: vi.fn(),
      removePolicy: async () => true,
      savePolicy: async () => true,
      updateTargetSchedulable: async () => true,
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
      AdminGroupHealthDetail: true, PriorityCandidatePreview: true,
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
  ['priorityTargetNotVisible', 'not_visible', '有账号已不在任何分组，无法恢复它的原 Priority；请把账号加回分组，或在主站删除它。', '已不在任何分组'],
  ['priorityWriteFailed', 'write_failed', '写入主站 Priority 失败，系统将在后台重试。', '写入主站失败'],
  ['priorityWaitingTimeout', 'waiting_timeout', '有账号超过 5 分钟仍未能写回 Priority，系统将继续重试。', '超过 5 分钟未能写回'],
  ['priorityTargetFailed', 'other', '部分账号的 Priority 未能写回，系统将在后台重试；原因见服务器日志。', '处理失败，原因见服务器日志'],
] as const

describe('Priority failure reasons in the real status banner', () => {
  it.each(REASONS)('shows %s and the failed account without a group-health reading error', async (key, reason, message, label) => {
    const wrapper = await mountView({
      errorKey: `admin.connectionHealth.errors.${key}`,
      failedTargets: [{ accountId: '242', accountName: '失败账号', reason }],
    })
    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain(message)
    expect(banner.text()).toContain(`失败账号（#242）：${label}`)
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
    expect(banner.text()).toContain('主站账号 #100：处理失败，原因见服务器日志')
    expect(banner.text()).toContain('失败账号9（#109）：写入主站失败')
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
    expect(banner.text()).toContain('失败账号（#242）：写入主站失败')
    expect(banner.text()).toContain('倍率异常账号 · 原上游站点：上游站点或 Key 绑定不完整')
    expect(banner.text()).not.toContain('当前账号资料已发生变化')
  })
})
