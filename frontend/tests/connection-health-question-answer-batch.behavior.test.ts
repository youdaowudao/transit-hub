// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ManualOneTimeProbeDialog, {
  type ManualProbeTargetSummary,
} from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
  QuestionAnswerBatch,
  QuestionAnswerHistory,
  QuestionAnswerRecord,
  QuestionAnswerStats,
  TestQuestion,
} from '@/modules/admin/types/connectionHealth'
import {
  connectionHealthPreferencesStorageKey,
  createDefaultConnectionHealthPreferences,
  readConnectionHealthPreferences,
  type QuestionAnswerPreferences,
  type QuestionAnswerSelectionPreferences,
} from '@/modules/admin/utils/connectionHealthPreferences'

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  currentAccount: null as { value: { id: string; displayName: string } | null } | null,
  discoverModels: vi.fn(),
  discoverTargetModels: vi.fn(),
  listTestQuestions: vi.fn(),
  getQuestionAnswerHistory: vi.fn(),
  getLatestQuestionAnswerBatch: vi.fn(),
  getQuestionAnswerBatch: vi.fn(),
  cancelQuestionAnswerBatch: vi.fn(),
  startQuestionAnswerBatch: vi.fn(),
  setQuestionAnswerJudgment: vi.fn(),
  getPrioritySyncStatus: vi.fn(),
  probeTargetWithProgress: vi.fn(),
  listUpstreamSites: vi.fn(),
  loadAll: vi.fn(),
  loadGroups: vi.fn(),
  loadAdminGroups: vi.fn(),
  refreshAdminGroups: vi.fn(),
  refreshAdminGroupsAutomatically: vi.fn(),
  loadEvents: vi.fn(),
  loadPolicies: vi.fn(),
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

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'ws1', displayName: 'Workspace One' })
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
    connectionHealthRecordColorClass: () => '',
    formatConnectionHealthTime: (value: string) => value,
    useConnectionHealth: () => ({
      ...harness.refs,
      discoverModels: harness.discoverModels,
      runManualProbeOnce: vi.fn(),
      manualProbeTarget: vi.fn(),
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

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  cancelQuestionAnswerBatch: harness.cancelQuestionAnswerBatch,
  getLatestQuestionAnswerBatch: harness.getLatestQuestionAnswerBatch,
  getQuestionAnswerBatch: harness.getQuestionAnswerBatch,
  getQuestionAnswerHistory: harness.getQuestionAnswerHistory,
  listTestQuestions: harness.listTestQuestions,
  discoverTargetModels: harness.discoverTargetModels,
  getPrioritySyncStatus: harness.getPrioritySyncStatus,
  probeTargetWithProgress: harness.probeTargetWithProgress,
  setQuestionAnswerJudgment: harness.setQuestionAnswerJudgment,
  startQuestionAnswerBatch: harness.startQuestionAnswerBatch,
}))

vi.mock('@/modules/admin/api/upstream', () => ({
  listUpstreamSites: harness.listUpstreamSites,
}))

const stats = (): QuestionAnswerStats => ({
  requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
  reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
  byModel: [],
})

const emptyBatch = (overrides: Partial<QuestionAnswerBatch> = {}): QuestionAnswerBatch => ({
  batchId: '',
  records: [],
  reasoningEffort: null,
  repeatCount: 1,
  submittedCount: 0,
  completedCount: 0,
  runningCount: 0,
  active: false,
  currentModel: '',
  currentQuestion: '',
  stats: stats(),
  ...overrides,
})

const history = (): QuestionAnswerHistory => ({
  records: [],
  page: 1,
  pageSize: 20,
  totalItems: 0,
  totalPages: 0,
  stats: stats(),
  todayStats: stats(),
})

const question = (id: string, isDefault = false): TestQuestion => ({
  id,
  name: `Question ${id}`,
  body: `Body ${id}`,
  keywords: [],
  enabled: true,
  isDefault,
  createdAt: '2026-08-31T00:00:00Z',
  updatedAt: '2026-08-31T00:00:00Z',
})

const record = (overrides: Partial<QuestionAnswerRecord> = {}): QuestionAnswerRecord => ({
  id: 'record-active',
  targetId: 'target-a',
  batchId: 'batch-active',
  modelName: 'model-b',
  questionId: 'q2',
  questionName: 'Question q2',
  questionBody: 'Body q2',
  questionKeywordSnapshot: [],
  reasoningEffort: 'low',
  answerBody: '',
  status: 'pending',
  errorType: '',
  answerJudgment: null,
  manualError: false,
  createdAt: '2026-08-31T00:00:00Z',
  startedAt: null,
  completedAt: null,
  updatedAt: '2026-08-31T00:00:00Z',
  ...overrides,
})

const target = (targetId: string): ManualProbeTargetSummary => ({
  targetId,
  accountName: `Account ${targetId}`,
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  groupName: 'Group A',
  formalModels: [],
})

const savedSelection = (overrides: Partial<QuestionAnswerSelectionPreferences> = {}): QuestionAnswerSelectionPreferences => ({
  modelIds: ['model-a', 'model-b'],
  questionIds: ['q1', 'q2'],
  reasoningEffort: 'high',
  repeatCount: 4,
  ...overrides,
})

const mountedWrappers: VueWrapper[] = []

beforeEach(() => {
  window.localStorage.clear()
  for (const [name, refValue] of Object.entries(harness.refs)) {
    if (['groups', 'adminGroups', 'events', 'policies'].includes(name)) refValue.value = []
    else if (['isLoading', 'isActionLoading'].includes(name)) refValue.value = false
    else if (['errorKey', 'refreshConflictNotice'].includes(name)) refValue.value = ''
    else if (name === 'refreshConnectionState') refValue.value = 'connected'
    else refValue.value = null
  }
  if (harness.currentAccount) harness.currentAccount.value = { id: 'ws1', displayName: 'Workspace One' }
  harness.discoverModels.mockReset().mockImplementation(async (targetId: string) => ({
    models: targetId === 'target-b'
      ? [{ id: 'model-b', name: 'Model B' }]
      : [{ id: 'model-a', name: 'Model A' }, { id: 'model-b', name: 'Model B' }],
  }))
  harness.listTestQuestions.mockReset().mockResolvedValue([question('q1', true), question('q2')])
  harness.getQuestionAnswerHistory.mockReset().mockResolvedValue(history())
  harness.getLatestQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch())
  harness.getQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch())
  harness.cancelQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch())
  harness.startQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch({ batchId: 'batch-started' }))
  harness.setQuestionAnswerJudgment.mockReset()
  harness.discoverTargetModels.mockReset().mockResolvedValue([
    { id: 'model-a', name: 'Model A' },
    { id: 'model-b', name: 'Model B' },
  ])
  harness.getPrioritySyncStatus.mockReset().mockResolvedValue({
    workspaceId: 'ws1', status: 'success', failedCount: 0,
  })
  harness.probeTargetWithProgress.mockReset()
  harness.listUpstreamSites.mockReset().mockResolvedValue([])
  harness.loadAll.mockReset().mockResolvedValue(true)
  harness.loadGroups.mockReset().mockResolvedValue(true)
  harness.loadAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroupsAutomatically.mockReset().mockResolvedValue(true)
  harness.loadEvents.mockReset().mockResolvedValue(true)
  harness.loadPolicies.mockReset().mockResolvedValue(true)
  harness.cancelAdminGroupsRefresh.mockReset()
  harness.setAdminGroupsWorkspace.mockReset()
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  window.localStorage.clear()
})

const mountDialog = async (
  preferences: QuestionAnswerSelectionPreferences,
  initialTarget = target('target-a'),
) => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: {
      open: false,
      target: initialTarget,
      questionAnswerPreferences: preferences,
    },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

const openConfiguration = async (wrapper: VueWrapper) => {
  const configuration = wrapper.find('[data-testid="question-answer-configuration"]')
  if (configuration.find('[data-testid="question-answer-models"]').exists()) return
  const modify = configuration.findAll('button')
    .find(button => button.text().trim() === '修改')
  if (!modify) throw new Error('missing editable question-answer configuration')
  await modify.trigger('click')
  await flushPromises()
}

const checkedLabels = (wrapper: VueWrapper, testId: string): string[] => wrapper
  .find(`[data-testid="${testId}"]`)
  .findAll('label')
  .filter(label => (label.find('input[type="checkbox"]').element as HTMLInputElement).checked)
  .map(label => label.text().trim())

const repeatSelect = (wrapper: VueWrapper) => {
  const select = wrapper.find('#question-answer-repeat-count')
  if (!select.exists()) throw new Error('missing repeat count select')
  return select
}

const toggleLabeledCheckbox = async (wrapper: VueWrapper, testId: string, text: string) => {
  const label = wrapper.find(`[data-testid="${testId}"]`).findAll('label')
    .find(candidate => candidate.text().includes(text))
  if (!label) throw new Error(`missing checkbox ${text}`)
  await label.find('input[type="checkbox"]').trigger('change')
}

const startButton = (wrapper: VueWrapper) => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().trim() === '开始回答')
  if (!button) throw new Error('missing start question-answer button')
  return button
}

const lastPreferenceEvent = (wrapper: VueWrapper): QuestionAnswerSelectionPreferences | undefined => {
  const events = wrapper.emitted('question-answer-preferences-changed')
  return events?.at(-1)?.[0] as QuestionAnswerSelectionPreferences | undefined
}

const account = (
  targetId: string,
  overrides: Partial<AdminGroupAccount> = {},
): AdminGroupAccount => ({
  id: `account-${targetId}`,
  name: `Account ${targetId}`,
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  targetId,
  probeAvailable: true,
  modelHealth: [],
  ...overrides,
})

const adminGroup = (
  id: string,
  accounts: AdminGroupAccount[],
  overrides: Partial<AdminGroupHealth> = {},
): AdminGroupHealth => ({
  id,
  name: `Group ${id}`,
  platform: 'sub2api',
  status: 'active',
  type: 'subscription',
  isExclusive: false,
  subscriptionType: 'standard',
  multiplier: 1,
  multiplierDisplay: '1.00',
  accountCount: accounts.length,
  healthSummary: {
    totalAccounts: accounts.length,
    probeableAccounts: accounts.length,
    unprobeableAccounts: 0,
    healthyModels: 0,
    degradedModels: 0,
    suspendedModels: 0,
    disabledModels: 0,
    unconfiguredModels: 0,
    lastProbeAt: null,
  },
  accounts,
  ...overrides,
})

const storedPreferences = (
  questionAnswer: Partial<QuestionAnswerPreferences> = {},
  overrides: Partial<ReturnType<typeof createDefaultConnectionHealthPreferences>> = {},
) => ({
  ...createDefaultConnectionHealthPreferences(),
  ...overrides,
  questionAnswer: {
    ...createDefaultConnectionHealthPreferences().questionAnswer,
    modelIds: ['model-a', 'model-b'],
    questionIds: ['q1', 'q2'],
    reasoningEffort: 'high' as const,
    repeatCount: 2,
    ...questionAnswer,
  },
})

const mountView = async (
  groups: AdminGroupHealth[],
  preferences = storedPreferences(),
) => {
  window.localStorage.setItem(
    connectionHealthPreferencesStorageKey('ws1'),
    JSON.stringify(preferences),
  )
  harness.refs.adminGroups.value = groups
  const wrapper = mount(ConnectionHealthView, {
    attachTo: document.body,
    global: {
      stubs: {
        Teleport: true,
        Transition: false,
        AdminGroupHealthDetail: true,
        ConnectionHealthEventsDialog: true,
        GroupHealthSetupDrawer: true,
        ManualOneTimeProbeDialog: true,
        PolicyConfigDrawer: true,
        ProbePolicyListDialog: true,
        TargetPolicyAssignmentDialog: true,
      },
    },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const openBatchDrawer = async (wrapper: VueWrapper) => {
  const button = wrapper.find('[data-testid="question-answer-batch-open"]')
  expect(button.exists()).toBe(true)
  await button.trigger('click')
  await flushPromises()
  return wrapper.find('[data-testid="question-answer-batch-drawer"]')
}

const batchTargetCheckbox = (wrapper: VueWrapper, targetId: string) =>
  wrapper.find(`input[type="checkbox"][data-target-id="${targetId}"]`)

const deferred = <T>() => {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const openPreparedBatch = async (
  groups: AdminGroupHealth[],
  preferences: ReturnType<typeof storedPreferences>,
) => {
  const wrapper = await mountView(groups, preferences)
  const drawer = await openBatchDrawer(wrapper)
  await vi.waitFor(() => expect(drawer.get('[data-testid="question-answer-batch-start"]').exists()).toBe(true))
  return { wrapper, drawer }
}

describe('single-account preferences', () => {
  it('restores legal saved choices after async data loads and does not persist target filtering', async () => {
    const wrapper = await mountDialog(savedSelection())
    await openConfiguration(wrapper)

    expect(checkedLabels(wrapper, 'question-answer-models')).toEqual(['Model A', 'Model B'])
    const restoredQuestions = checkedLabels(wrapper, 'question-answer-questions')
    expect(restoredQuestions).toHaveLength(2)
    expect(restoredQuestions.some(text => text.includes('Question q1'))).toBe(true)
    expect(restoredQuestions.some(text => text.includes('Question q2'))).toBe(true)
    expect((wrapper.find('input[name="question-answer-reasoning-effort"][value="high"]').element as HTMLInputElement).checked).toBe(true)
    expect((repeatSelect(wrapper).element as HTMLSelectElement).value).toBe('4')
    expect(wrapper.emitted('question-answer-preferences-changed')).toBeUndefined()

    await wrapper.setProps({ target: target('target-b') })
    await flushPromises()
    await openConfiguration(wrapper)
    expect(checkedLabels(wrapper, 'question-answer-models')).toEqual(['Model B'])
    expect(wrapper.emitted('question-answer-preferences-changed')).toBeUndefined()

    await wrapper.setProps({ target: target('target-a') })
    await flushPromises()
    await openConfiguration(wrapper)
    expect(checkedLabels(wrapper, 'question-answer-models')).toEqual(['Model A', 'Model B'])
    expect(wrapper.emitted('question-answer-preferences-changed')).toBeUndefined()
  })

  it('shows an active batch authoritative selection without overwriting the next saved selection', async () => {
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch({
      batchId: 'batch-active',
      records: [record()],
      reasoningEffort: 'low',
      repeatCount: 3,
      submittedCount: 1,
      runningCount: 1,
      active: true,
      stats: {
        requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }))

    const wrapper = await mountDialog(savedSelection({
      modelIds: ['model-a'],
      questionIds: ['q1'],
      reasoningEffort: 'xhigh',
      repeatCount: 8,
    }))

    const configuration = wrapper.get('[data-testid="question-answer-configuration"]')
    expect(configuration.text()).toContain('Model B')
    expect(configuration.text()).toContain('问题 1 个 · 推理力度 低 · 每组合 3 次')
    expect(configuration.text()).toContain('当前进行中批次使用此配置')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
    expect(wrapper.emitted('question-answer-preferences-changed')).toBeUndefined()
  })

  it('emits one complete legal selection after each real user change', async () => {
    const wrapper = await mountDialog(savedSelection({
      modelIds: ['model-a'],
      questionIds: ['q1'],
      reasoningEffort: 'medium',
      repeatCount: 1,
    }))
    await openConfiguration(wrapper)

    await toggleLabeledCheckbox(wrapper, 'question-answer-models', 'Model B')
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a', 'model-b'], questionIds: ['q1'], reasoningEffort: 'medium', repeatCount: 1,
    })

    await toggleLabeledCheckbox(wrapper, 'question-answer-questions', 'Question q2')
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a', 'model-b'], questionIds: ['q1', 'q2'], reasoningEffort: 'medium', repeatCount: 1,
    })

    await wrapper.find('input[name="question-answer-reasoning-effort"][value="xhigh"]').setValue(true)
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a', 'model-b'], questionIds: ['q1', 'q2'], reasoningEffort: 'xhigh', repeatCount: 1,
    })

    await repeatSelect(wrapper).setValue('7')
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a', 'model-b'], questionIds: ['q1', 'q2'], reasoningEffort: 'xhigh', repeatCount: 7,
    })
  })

  it('preserves saved choices hidden by the current target when the user changes another visible field', async () => {
    const wrapper = await mountDialog(savedSelection(), target('target-b'))
    await openConfiguration(wrapper)
    expect(checkedLabels(wrapper, 'question-answer-models')).toEqual(['Model B'])

    await repeatSelect(wrapper).setValue('7')
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a', 'model-b'],
      questionIds: ['q1', 'q2'],
      reasoningEffort: 'high',
      repeatCount: 7,
    })

    await wrapper.find('input[name="question-answer-reasoning-effort"][value="xhigh"]').setValue(true)
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a', 'model-b'],
      questionIds: ['q1', 'q2'],
      reasoningEffort: 'xhigh',
      repeatCount: 7,
    })

    await toggleLabeledCheckbox(wrapper, 'question-answer-models', 'Model B')
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a'],
      questionIds: ['q1', 'q2'],
      reasoningEffort: 'xhigh',
      repeatCount: 7,
    })
  })

  it('does not add fallback models or questions when only another field changes after a zero-intersection restore', async () => {
    harness.listTestQuestions.mockResolvedValue([question('q1', true)])
    const wrapper = await mountDialog(savedSelection({
      modelIds: ['model-a'],
      questionIds: ['question-no-longer-enabled'],
      reasoningEffort: 'medium',
      repeatCount: 1,
    }), target('target-b'))

    expect(checkedLabels(wrapper, 'question-answer-models')).toEqual(['Model B'])
    expect(checkedLabels(wrapper, 'question-answer-questions')[0]).toContain('Question q1')

    await repeatSelect(wrapper).setValue('7')
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a'],
      questionIds: ['question-no-longer-enabled'],
      reasoningEffort: 'medium',
      repeatCount: 7,
    })

    await wrapper.find('input[name="question-answer-reasoning-effort"][value="xhigh"]').setValue(true)
    expect(lastPreferenceEvent(wrapper)).toEqual({
      modelIds: ['model-a'],
      questionIds: ['question-no-longer-enabled'],
      reasoningEffort: 'xhigh',
      repeatCount: 7,
    })
  })

  it('restores the active batch selection after model discovery retry succeeds', async () => {
    harness.discoverModels
      .mockResolvedValueOnce({ errorKey: 'admin.connectionHealth.errors.network' })
      .mockResolvedValueOnce({
        models: [{ id: 'model-a', name: 'Model A' }, { id: 'model-b', name: 'Model B' }],
      })
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch({
      batchId: 'batch-active',
      records: [record()],
      reasoningEffort: 'low',
      repeatCount: 3,
      submittedCount: 1,
      runningCount: 1,
      active: true,
      stats: {
        requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }))

    const wrapper = await mountDialog(savedSelection({
      modelIds: ['model-a'],
      questionIds: ['q1'],
      reasoningEffort: 'xhigh',
      repeatCount: 8,
    }))
    const retry = wrapper.findAll('button').find(button => button.text().trim() === '重新加载')
    expect(retry).toBeDefined()
    await retry!.trigger('click')
    await flushPromises()

    const configuration = wrapper.get('[data-testid="question-answer-configuration"]')
    expect(configuration.text()).toContain('Model B')
    expect(configuration.text()).toContain('问题 1 个 · 推理力度 低 · 每组合 3 次')
    expect(configuration.text()).toContain('当前进行中批次使用此配置')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
    expect(wrapper.emitted('question-answer-preferences-changed')).toBeUndefined()
  })

  it('keeps all 51 restored requests visible and blocks start without rewriting the preference', async () => {
    const questions = Array.from({ length: 51 }, (_, index) => question(`q${index + 1}`, index === 0))
    harness.discoverModels.mockResolvedValue({ models: [{ id: 'model-a', name: 'Model A' }] })
    harness.listTestQuestions.mockResolvedValue(questions)

    const wrapper = await mountDialog(savedSelection({
      modelIds: ['model-a'],
      questionIds: questions.map(item => item.id),
      reasoningEffort: 'medium',
      repeatCount: 1,
    }))

    expect(checkedLabels(wrapper, 'question-answer-questions')).toHaveLength(51)
    expect(wrapper.text()).toContain('共 51 次请求')
    expect(wrapper.text()).toContain('减少模型、问题或次数')
    expect(startButton(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('question-answer-preferences-changed')).toBeUndefined()
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
  })
})

describe('batch target preparation', () => {
  it('uses every ordered group, deduplicates target IDs, cleans stale saved IDs, and derives source from display order', async () => {
    const preferences = storedPreferences(
      { batchTargetIds: ['t2', 't2', 'gone', 't1'] },
      { groupOrder: ['g1', 'g2'], hiddenGroupIds: ['g2'] },
    )
    const wrapper = await mountView([
      adminGroup('g1', [account('t1'), account('t2')]),
      adminGroup('g2', [
        account('t1', { name: 'Duplicate t1' }),
        account('t3', { status: 'disabled', schedulable: false, probeAvailable: false }),
      ]),
    ], preferences)

    const drawer = await openBatchDrawer(wrapper)
    expect(drawer.exists()).toBe(true)
    expect(drawer.findAll('input[type="checkbox"][data-target-id="t1"]')).toHaveLength(1)
    expect(batchTargetCheckbox(drawer, 't1').exists()).toBe(true)
    expect(batchTargetCheckbox(drawer, 't2').exists()).toBe(true)
    expect(batchTargetCheckbox(drawer, 't3').exists()).toBe(true)
    expect((batchTargetCheckbox(drawer, 't1').element as HTMLInputElement).checked).toBe(true)
    expect((batchTargetCheckbox(drawer, 't2').element as HTMLInputElement).checked).toBe(true)
    expect((batchTargetCheckbox(drawer, 't3').element as HTMLInputElement).checked).toBe(false)
    expect(drawer.get('[data-testid="question-answer-batch-source"]').text()).toContain('Account t1')

    expect(harness.listTestQuestions).toHaveBeenCalledTimes(1)
    expect(harness.discoverTargetModels.mock.calls.map(call => call[0])).toEqual(['t1', 't2'])
    expect(readConnectionHealthPreferences('ws1').questionAnswer).toEqual({
      modelIds: ['model-a', 'model-b'],
      questionIds: ['q1', 'q2'],
      reasoningEffort: 'high',
      repeatCount: 2,
      batchTargetIds: ['t2', 't1'],
    })
    expect(drawer.text()).toContain('共 16 次请求')

    await batchTargetCheckbox(drawer, 't3').setValue(true)
    await flushPromises()
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t1', 't2', 't3'])

    await drawer.get('[data-testid="question-answer-batch-close"]').trigger('click')
    await openBatchDrawer(wrapper)
    expect((batchTargetCheckbox(wrapper, 't3').element as HTMLInputElement).checked).toBe(true)
  })

  it('blocks all preparation and preserves saved IDs when any group account list is incomplete', async () => {
    const wrapper = await mountView([
      adminGroup('g1', [], {
        name: 'Failed Group',
        accountsError: 'admin.connectionHealth.errors.request',
      }),
    ], storedPreferences({ batchTargetIds: ['target-from-failed-group'] }))

    const drawer = await openBatchDrawer(wrapper)
    expect(drawer.exists()).toBe(true)
    expect(drawer.text()).toContain('Failed Group')
    expect(drawer.text()).toContain('刷新')
    expect(harness.listTestQuestions).not.toHaveBeenCalled()
    expect(harness.discoverTargetModels).not.toHaveBeenCalled()
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
    expect(drawer.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeDefined()
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds)
      .toEqual(['target-from-failed-group'])
  })
})

describe('batch global configuration gates', () => {
  it.each([
    {
      name: 'enabled-question loading failure',
      configure: () => harness.listTestQuestions.mockRejectedValue(new Error('questions unavailable')),
      expected: '读取启用问题失败',
    },
    {
      name: 'source model discovery failure',
      configure: () => harness.discoverTargetModels.mockRejectedValue(new Error('models unavailable')),
      expected: '配置来源模型发现失败',
    },
    {
      name: 'source without a legal model',
      configure: () => harness.discoverTargetModels.mockResolvedValue([]),
      expected: '配置来源没有可用模型',
    },
    {
      name: 'no enabled questions',
      configure: () => harness.listTestQuestions.mockResolvedValue([
        { ...question('q1', true), enabled: false },
      ]),
      expected: '没有启用问题',
    },
    {
      name: 'shared configuration exceeds the per-account limit',
      configure: () => {
        const questions = Array.from({ length: 51 }, (_, index) => question(`q${index + 1}`, index === 0))
        harness.listTestQuestions.mockResolvedValue(questions)
      },
      expected: '减少模型、问题或次数',
      preferenceQuestions: Array.from({ length: 51 }, (_, index) => `q${index + 1}`),
    },
  ])('shows an actionable $name reason and creates no batch or unread marker', async ({
    configure,
    expected,
    preferenceQuestions,
  }) => {
    configure()
    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1')])],
      storedPreferences({
        modelIds: ['model-a'],
        questionIds: preferenceQuestions ?? ['q1'],
        repeatCount: 1,
        batchTargetIds: ['t1'],
      }),
    )

    await vi.waitFor(() => expect(drawer.text()).toContain(expected))
    expect(drawer.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeDefined()
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
    expect(readConnectionHealthPreferences('ws1').questionAnswerUnreadTargetIds).toEqual([])
  })
})

describe('batch sequential submission', () => {
  it('freezes display order, starts compatible targets serially, and reconciles mixed outcomes and accepted totals', async () => {
    let inFlight = 0
    let maxInFlight = 0
    harness.discoverTargetModels.mockImplementation(async (targetId: string) => {
      if (targetId === 't3') return [{ id: 'model-c', name: 'Model C' }]
      if (targetId === 't4') throw new Error('model discovery failed')
      return [{ id: 'model-a', name: 'Model A' }, { id: 'model-b', name: 'Model B' }]
    })
    harness.startQuestionAnswerBatch.mockImplementation(async (targetId: string) => {
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      await Promise.resolve()
      inFlight--
      if (targetId === 't2') throw new Error('admin.connectionHealth.errors.questionAnswerActive')
      if (targetId === 't5') throw new Error('start failed')
      return emptyBatch({
        batchId: `batch-${targetId}`,
        submittedCount: targetId === 't1' ? 8 : 7,
      })
    })

    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', ['t1', 't2', 't3', 't4', 't5', 't6'].map(targetId => account(targetId)))],
      storedPreferences({
        modelIds: ['model-a', 'model-b'],
        questionIds: ['q1', 'q2'],
        reasoningEffort: 'high',
        repeatCount: 2,
        batchTargetIds: ['t6', 't2', 't1', 't5', 't3', 't4'],
      }),
    )

    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 32'))
    expect(drawer.get('[data-testid="question-answer-batch-source"]').text()).toContain('Account t1')
    const start = drawer.get('[data-testid="question-answer-batch-start"]')
    expect(start.attributes('disabled')).toBeUndefined()
    await start.trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(4))
    await flushPromises()

    expect(maxInFlight).toBe(1)
    expect(harness.startQuestionAnswerBatch.mock.calls.map(call => call[0])).toEqual(['t1', 't2', 't5', 't6'])
    expect(harness.startQuestionAnswerBatch.mock.calls[0]?.slice(0, 5)).toEqual([
      't1', ['model-a', 'model-b'], ['q1', 'q2'], 'high', 2,
    ])
    expect(drawer.text()).toContain('已处理 6/6')
    expect(drawer.text()).toContain('已启动 2')
    expect(drawer.text()).toContain('已跳过 2')
    expect(drawer.text()).toContain('失败 2')
    expect(drawer.text()).toContain('预览总量 32')
    expect(drawer.text()).toContain('实际接受总量 15')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t2"]').text()).toContain('已有活动批次')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t3"]').text()).toContain('无兼容模型')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t4"]').text()).toContain('模型发现失败')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t5"]').text()).toContain('启动失败')
    expect(readConnectionHealthPreferences('ws1').questionAnswerUnreadTargetIds).toEqual(['t1', 't6'])
  })

  it('ignores a double start and keeps the same run alive across close and reopen', async () => {
    const response = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch.mockReturnValue(response.promise)
    const { wrapper, drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1')])],
      storedPreferences({ modelIds: ['model-a'], questionIds: ['q1'], batchTargetIds: ['t1'] }),
    )
    await vi.waitFor(() => expect(drawer.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeUndefined())

    const start = drawer.get('[data-testid="question-answer-batch-start"]')
    await Promise.all([start.trigger('click'), start.trigger('click')])
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)

    await drawer.get('[data-testid="question-answer-batch-close"]').trigger('click')
    const reopened = await openBatchDrawer(wrapper)
    expect(reopened.text()).toContain('已处理 0/1')
    expect(reopened.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeDefined()
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)

    response.resolve(emptyBatch({ batchId: 'batch-t1', submittedCount: 1 }))
    await flushPromises()
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)
    expect(reopened.text()).toContain('已处理 1/1')
    expect(reopened.text()).toContain('实际接受总量 1')
  })

  it('aborts the pending request on scope change and rejects its late unread write', async () => {
    const secondResponse = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch
      .mockResolvedValueOnce(emptyBatch({ batchId: 'batch-t1', submittedCount: 1 }))
      .mockReturnValueOnce(secondResponse.promise)
    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1'), account('t2')])],
      storedPreferences({ modelIds: ['model-a'], questionIds: ['q1'], batchTargetIds: ['t1', 't2'] }),
    )
    await vi.waitFor(() => expect(drawer.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeUndefined())
    await drawer.get('[data-testid="question-answer-batch-start"]').trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(2))

    harness.currentAccount!.value = { id: 'ws2', displayName: 'Workspace Two' }
    await nextTick()
    await flushPromises()
    const pendingSignal = harness.startQuestionAnswerBatch.mock.calls[1]?.[5] as AbortSignal
    expect(pendingSignal.aborted).toBe(true)

    secondResponse.resolve(emptyBatch({ batchId: 'batch-t2', submittedCount: 1 }))
    await flushPromises()
    expect(readConnectionHealthPreferences('ws1').questionAnswerUnreadTargetIds).toEqual(['t1'])
    expect(readConnectionHealthPreferences('ws2').questionAnswerUnreadTargetIds).toEqual([])
  })

  it('does not clear the next scope saved targets when an idle open drawer observes the workspace reset', async () => {
    window.localStorage.setItem(
      connectionHealthPreferencesStorageKey('ws2'),
      JSON.stringify(storedPreferences({ batchTargetIds: ['t2'] })),
    )
    const { wrapper } = await openPreparedBatch(
      [adminGroup('g1', [account('t1')])],
      storedPreferences({ batchTargetIds: ['t1'] }),
    )
    harness.setAdminGroupsWorkspace.mockImplementation((scope: string) => {
      if (scope === 'ws2') harness.refs.adminGroups.value = []
    })

    harness.currentAccount!.value = { id: 'ws2', displayName: 'Workspace Two' }
    await nextTick()
    await flushPromises()

    expect(wrapper.find('[data-testid="question-answer-batch-drawer"]').exists()).toBe(false)
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t1'])
    expect(readConnectionHealthPreferences('ws2').questionAnswer.batchTargetIds).toEqual(['t2'])
  })
})

describe('batch drawer lifecycle and presentation', () => {
  it('starts with no targets and lets group actions update one deduplicated target set', async () => {
    const { drawer } = await openPreparedBatch(
      [
        adminGroup('g1', [account('t1'), account('t2')]),
        adminGroup('g2', [account('t1', { name: 'Duplicate t1' }), account('t3')]),
      ],
      storedPreferences({ batchTargetIds: [] }),
    )

    expect(drawer.findAll('input[type="checkbox"][data-target-id]:checked')).toHaveLength(0)
    expect(harness.listTestQuestions).not.toHaveBeenCalled()
    expect(harness.discoverTargetModels).not.toHaveBeenCalled()

    await drawer.get('[data-testid="question-answer-batch-group-select-g1"]').trigger('click')
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t1', 't2'])
    await drawer.get('[data-testid="question-answer-batch-group-select-g2"]').trigger('click')
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t1', 't2', 't3'])
    await drawer.get('[data-testid="question-answer-batch-group-clear-g1"]').trigger('click')
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t3'])
    expect((batchTargetCheckbox(drawer, 't1').element as HTMLInputElement).checked).toBe(false)
    expect((batchTargetCheckbox(drawer, 't3').element as HTMLInputElement).checked).toBe(true)
  })

  it('discards late preparation and only renders the latest target snapshot', async () => {
    const firstDiscovery = deferred<Array<{ id: string; name: string }>>()
    let t1Calls = 0
    harness.discoverTargetModels.mockImplementation((targetId: string) => {
      if (targetId === 't1' && t1Calls++ === 0) return firstDiscovery.promise
      return Promise.resolve([{ id: 'model-a', name: 'Model A' }])
    })
    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1'), account('t2')])],
      storedPreferences({
        modelIds: ['model-a', 'model-b'],
        questionIds: ['q1'],
        repeatCount: 1,
        batchTargetIds: ['t1'],
      }),
    )
    await vi.waitFor(() => expect(harness.discoverTargetModels).toHaveBeenCalledTimes(1))

    await batchTargetCheckbox(drawer, 't2').setValue(true)
    await vi.waitFor(() => expect(harness.discoverTargetModels).toHaveBeenCalledTimes(3))
    firstDiscovery.resolve([
      { id: 'model-a', name: 'Model A' },
      { id: 'model-b', name: 'Model B' },
    ])
    await flushPromises()

    expect(drawer.text()).toContain('预览总量 2')
    expect(drawer.get('[data-testid="question-answer-batch-preview-t1"]').text()).not.toContain('model-b')
    expect(drawer.get('[data-testid="question-answer-batch-preview-t2"]').text()).toContain('model-a')
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
  })

  it('reconciles candidates and re-prepares when the shared selection changes while open', async () => {
    const { wrapper, drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1'), account('t2')])],
      storedPreferences({
        modelIds: ['model-a'],
        questionIds: ['q1'],
        repeatCount: 1,
        batchTargetIds: ['t1', 't2'],
      }),
    )
    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 2'))

    harness.refs.adminGroups.value = [adminGroup('g1', [account('t1')])]
    await nextTick()
    await flushPromises()
    await vi.waitFor(() => expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t1']))

    const manualDialog = wrapper.findComponent(ManualOneTimeProbeDialog)
    expect(manualDialog.exists()).toBe(true)
    manualDialog.vm.$emit('question-answer-preferences-changed', {
      modelIds: ['model-a', 'model-b'],
      questionIds: ['q1'],
      reasoningEffort: 'xhigh',
      repeatCount: 2,
    } satisfies QuestionAnswerSelectionPreferences)
    await nextTick()
    await flushPromises()

    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 4'))
    expect(readConnectionHealthPreferences('ws1').questionAnswer).toEqual({
      modelIds: ['model-a', 'model-b'],
      questionIds: ['q1'],
      reasoningEffort: 'xhigh',
      repeatCount: 2,
      batchTargetIds: ['t1'],
    })
  })

  it('keeps the finished run and start time beside the list entry after close and reopen', async () => {
    harness.startQuestionAnswerBatch.mockResolvedValue(emptyBatch({ batchId: 'batch-t1', submittedCount: 1 }))
    const { wrapper, drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1')])],
      storedPreferences({ modelIds: ['model-a'], questionIds: ['q1'], batchTargetIds: ['t1'] }),
    )
    expect(wrapper.find('aside [data-testid="question-answer-batch-open"]').exists()).toBe(true)
    await vi.waitFor(() => expect(drawer.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeUndefined())
    await drawer.get('[data-testid="question-answer-batch-start"]').trigger('click')
    await vi.waitFor(() => expect(drawer.text()).toContain('已处理 1/1'))
    await drawer.get('[data-testid="question-answer-batch-close"]').trigger('click')

    const entrySummary = wrapper.get('[data-testid="question-answer-batch-entry-summary"]').text()
    expect(entrySummary).toMatch(/\d{2}:\d{2} 开始/)
    expect(entrySummary).toContain('已处理 1/1')
    expect(entrySummary).toContain('已启动 1')
    expect(entrySummary).toContain('实际接受总量 1')

    const reopened = await openBatchDrawer(wrapper)
    expect(reopened.text()).toContain('已处理 1/1')
    expect(reopened.text()).toContain('实际接受总量 1')
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)
  })

  it('keeps the old outcome but re-prepares dirty targets and shared settings before a new run', async () => {
    harness.startQuestionAnswerBatch.mockResolvedValue(emptyBatch({ batchId: 'batch', submittedCount: 1 }))
    const { wrapper, drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1'), account('t2')])],
      storedPreferences({
        modelIds: ['model-a', 'model-b'],
        questionIds: ['q1'],
        reasoningEffort: 'high',
        repeatCount: 1,
        batchTargetIds: ['t1', 't2'],
      }),
    )
    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 4'))
    await drawer.get('[data-testid="question-answer-batch-start"]').trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(2))
    await drawer.get('[data-testid="question-answer-batch-close"]').trigger('click')

    harness.refs.adminGroups.value = [adminGroup('g1', [account('t1')])]
    wrapper.findComponent(ManualOneTimeProbeDialog).vm.$emit('question-answer-preferences-changed', {
      modelIds: ['model-a'],
      questionIds: ['q1'],
      reasoningEffort: 'xhigh',
      repeatCount: 3,
    } satisfies QuestionAnswerSelectionPreferences)
    await nextTick()
    await flushPromises()

    const reopened = await openBatchDrawer(wrapper)
    expect(reopened.text()).toContain('已处理 2/2')
    await vi.waitFor(() => expect(harness.discoverTargetModels).toHaveBeenCalledTimes(3))
    await vi.waitFor(() => expect(reopened.text()).toContain('预览总量 3'))
    const previousRunPreview = reopened.get('[data-testid="question-answer-batch-run-preview-total"]')
    expect(previousRunPreview.text()).toContain('上轮预览总量 4')
    expect(previousRunPreview.text()).not.toContain('预览总量 3')
    expect(reopened.get('[data-testid="question-answer-batch-run-summary"]').text()).toContain('实际接受总量 2')
    expect(readConnectionHealthPreferences('ws1').questionAnswer.batchTargetIds).toEqual(['t1'])
    const restart = reopened.get('[data-testid="question-answer-batch-start"]')
    expect(restart.attributes('disabled')).toBeUndefined()
    await restart.trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(3))
    expect(harness.startQuestionAnswerBatch.mock.calls.map(call => call[0])).toEqual(['t1', 't2', 't1'])
    expect(harness.startQuestionAnswerBatch.mock.calls[2]?.slice(0, 5)).toEqual([
      't1', ['model-a'], ['q1'], 'xhigh', 3,
    ])
  })

  it('keeps safe per-target discovery and start failure reasons distinguishable', async () => {
    harness.discoverTargetModels.mockImplementation(async (targetId: string) => {
      if (targetId === 't2') throw new Error('admin.connectionHealth.errors.network')
      return [{ id: 'model-a', name: 'Model A' }]
    })
    harness.startQuestionAnswerBatch.mockImplementation(async (targetId: string) => {
      if (targetId === 't3') throw new Error('admin.connectionHealth.errors.request')
      return emptyBatch({ batchId: `batch-${targetId}`, submittedCount: 1 })
    })
    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1'), account('t2'), account('t3')])],
      storedPreferences({ modelIds: ['model-a'], questionIds: ['q1'], batchTargetIds: ['t1', 't2', 't3'] }),
    )
    await vi.waitFor(() => expect(drawer.get('[data-testid="question-answer-batch-preview-t2"]').text())
      .toContain('网络异常，请检查连接后重试。'))
    await drawer.get('[data-testid="question-answer-batch-start"]').trigger('click')
    await vi.waitFor(() => expect(drawer.text()).toContain('已处理 3/3'))

    const discoveryOutcome = drawer.get('[data-testid="question-answer-batch-outcome-t2"]')
    const startOutcome = drawer.get('[data-testid="question-answer-batch-outcome-t3"]')
    expect(discoveryOutcome.text()).toContain('网络异常，请检查连接后重试。')
    expect(startOutcome.text()).toContain('操作失败，请稍后重试。')
    expect(discoveryOutcome.text()).not.toBe(startOutcome.text())
    expect(discoveryOutcome.find('p:last-child').classes()).toContain('break-words')
    expect(startOutcome.find('p:last-child').classes()).toContain('break-words')
  })

  it('wraps long account identities and shortens the source target ID without horizontal overflow', async () => {
    const longTargetId = `sub2api:ws1:${'x'.repeat(100)}`
    const longAccountName = `超长账号${'名'.repeat(100)}`
    harness.startQuestionAnswerBatch.mockResolvedValue(emptyBatch({ batchId: 'batch-long', submittedCount: 1 }))
    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', [account(longTargetId, { name: longAccountName })])],
      storedPreferences({
        modelIds: ['model-a'], questionIds: ['q1'], repeatCount: 1, batchTargetIds: [longTargetId],
      }),
    )
    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 1'))

    const targetName = drawer.get(`[data-testid="question-answer-batch-target-name-${longTargetId}"]`)
    expect(targetName.text()).toBe(longAccountName)
    expect(targetName.classes()).toContain('break-words')
    expect(targetName.classes()).not.toContain('truncate')
    const sourceText = drawer.get('[data-testid="question-answer-batch-source"]').text()
    expect(sourceText).toContain('sub2api:ws1:')
    expect(sourceText).toContain('…')
    expect(sourceText).not.toContain(longTargetId)
    expect(drawer.get(`[data-testid="question-answer-batch-preview-identity-${longTargetId}"]`).classes())
      .toContain('break-all')
    expect(drawer.find('section').classes()).toContain('overflow-hidden')

    await drawer.get('[data-testid="question-answer-batch-start"]').trigger('click')
    await vi.waitFor(() => expect(drawer.text()).toContain('已处理 1/1'))
    expect(drawer.get(`[data-testid="question-answer-batch-outcome-name-${longTargetId}"]`).classes())
      .toContain('break-words')
  })

  it('shows the shared configuration and per-target compatibility in a fixed responsive drawer', async () => {
    harness.discoverTargetModels.mockImplementation(async (targetId: string) => targetId === 't1'
      ? [{ id: 'model-a', name: 'Model A' }, { id: 'model-b', name: 'Model B' }]
      : [{ id: 'model-a', name: 'Model A' }])
    const { drawer } = await openPreparedBatch(
      [adminGroup('g1', [account('t1', { name: 'Primary source' }), account('t2')])],
      storedPreferences({
        modelIds: ['model-a', 'model-b'],
        questionIds: ['q1', 'q2'],
        reasoningEffort: 'high',
        repeatCount: 2,
        batchTargetIds: ['t1', 't2'],
      }),
    )
    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 12'))

    const source = drawer.get('[data-testid="question-answer-batch-source"]').text()
    expect(source).toContain('Primary source')
    expect(source).toContain('t1')
    const config = drawer.get('[data-testid="question-answer-batch-config"]').text()
    expect(config).toContain('Question q1')
    expect(config).toContain('Question q2')
    expect(config).toContain('high')
    expect(config).toContain('每组合 2 次')
    const t2Preview = drawer.get('[data-testid="question-answer-batch-preview-t2"]').text()
    expect(t2Preview).toContain('model-a')
    expect(t2Preview).toContain('model-b')
    expect(t2Preview).toContain('不兼容')
    expect(t2Preview).toContain('4 次请求')
    expect(drawer.get('[data-testid="question-answer-batch-body"]').classes()).toContain('overflow-y-auto')
    expect(drawer.get('[data-testid="question-answer-batch-footer"]').classes()).toContain('shrink-0')
    expect(drawer.text()).not.toContain('队列位置')
    expect(drawer.text()).not.toContain('预计完成')
    expect(drawer.text()).not.toContain('全部问答完成')
  })
})
