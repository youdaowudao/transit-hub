// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { t, te } from '@/locales'
import { questionAnswerErrorMessageKey } from '@/modules/admin/utils/questionAnswers'
import { questionAnswerScheduleReason } from '@/modules/admin/utils/questionAnswerSchedules'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import QuestionAnswerScheduleExecutionDetail from '@/modules/admin/components/dashboard/QuestionAnswerScheduleExecutionDetail.vue'
import { c2Execution, c2Stats } from './fixtures/c2QuestionAnswerSchedules'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
// 20261016/4：分组健康页新增已对接站点读取；本用例与它无关，隔离为空列表，不改原断言。
vi.mock('@/modules/admin/api/mySites', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()), listRealConnections: vi.fn(async () => []) }))

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  getPrioritySyncStatus: vi.fn(),
  getQuestionAnswerBatch: vi.fn(),
  getQuestionAnswerScheduleExecution: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn(async () => undefined) }),
}))
vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  return {
    useDocumentVisibility: () => ref('visible'),
    useIntervalFn: () => ({ pause: vi.fn(), resume: vi.fn() }),
  }
})
vi.mock('@/modules/admin/api/connectionHealth', async importOriginal => ({
  ...(await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>()),
  getPrioritySyncStatus: harness.getPrioritySyncStatus,
  getQuestionAnswerBatch: harness.getQuestionAnswerBatch,
  getQuestionAnswerScheduleExecution: harness.getQuestionAnswerScheduleExecution,
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
      invalidateAdminGroupsReads: vi.fn(),
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
  harness.refs.adminGroups.value = []
  localStorage.clear()
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.useRealTimers()
  localStorage.clear()
  document.body.innerHTML = ''
})

describe('background internal failures explain recovery', () => {
  it('renders refresh terminal internal failure while retaining the previous list', async () => {
    harness.refs.terminalRefreshSummary.value = { state: 'failed', sites: [], errorKey: 'refresh_internal_error', failedStage: 'site_sync' }
    const wrapper = await mountView({ status: 'idle', failedCount: 0 })
    await nextTick()
    expect(wrapper.text()).toContain('刷新流程：程序内部出错，本轮已结束并保留原列表，没有修改主站；请再点“刷新”，仍失败请查看服务日志')
    expect(wrapper.text()).not.toContain('原因未提供')
    expect(wrapper.text()).not.toContain('刷新阶段未知')
  })

  it('renders the question-answer failure through the same translation path used by records', () => {
    const wrapper = mount({ setup() { return { message: t(questionAnswerErrorMessageKey('internal_error', te)) } }, template: '<p role="alert">{{ message }}</p>' })
    wrappers.push(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('测试程序内部出错，本批次已停止，没有修改主站；可以重新发起测试。')
    expect(wrapper.text()).not.toContain('原因未提供')
  })

  it('renders scheduled internal failure and the immediate retry instruction', () => {
    const wrapper = mount({ setup() { return { message: questionAnswerScheduleReason('internal_error') } }, template: '<p role="alert">{{ message }}</p>' })
    wrappers.push(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('测试程序内部出错，这个账号本次没有测试，没有修改主站；可以点“立即执行”重试。')
    expect(wrapper.text()).not.toContain('原因未提供')
  })
  it('displays the internal reason inside the actual question-answer record dialog', async () => {
    harness.getQuestionAnswerBatch.mockResolvedValue({ batchId: 'internal-fixture', targetId: 'sub2api:ws1:a', active: false, stats: c2Stats(0, 0, 0, 1), records: [{ id: 'r1', modelName: 'm1', questionName: '隔离题', status: 'failed', errorType: 'internal_error' }] })
    const wrapper = mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: 'sub2api:ws1:a', batchId: 'internal-fixture', accountName: '隔离账号', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }); wrappers.push(wrapper); await flushPromises()
    expect(wrapper.get('article').text()).toContain('测试程序内部出错，本批次已停止，没有修改主站；可以重新发起测试。')
    expect(wrapper.get('article').text()).not.toContain('问答请求失败。')
    expect(wrapper.get('article').text()).toContain('账号 当时未记录')
  })

  it('displays the internal reason for the actual scheduled target', async () => {
    const execution = c2Execution({ status: 'failed', active: false, statusReason: 'internal_error' })
    execution.targets[0]!.status = 'failed'; execution.targets[0]!.statusReason = 'internal_error'
    harness.getQuestionAnswerScheduleExecution.mockResolvedValue(execution)
    const wrapper = mount(QuestionAnswerScheduleExecutionDetail, { props: { executionId: execution.id, workspace: 'ws1', scheduleName: '隔离计划' } }); wrappers.push(wrapper); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-target"]').text()).toContain('测试程序内部出错，这个账号本次没有测试，没有修改主站；可以点“立即执行”重试。')
    expect(wrapper.text()).not.toContain('运行结果待处理')
  })

})
