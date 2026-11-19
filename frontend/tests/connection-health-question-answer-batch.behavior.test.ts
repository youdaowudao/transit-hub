// @vitest-environment jsdom
import { c32AutoSettings } from './fixtures/c32ModelControl'

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { upgradeQuestionAnswerHistoryFixture } from './fixtures/c1QuestionAnswerHistory'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ManualOneTimeProbeDialog, {
  type ManualProbeTargetSummary,
} from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import * as queuePolling from '@/modules/admin/composables/useQuestionAnswerQueue'
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
// 20261016/4：分组健康页新增已对接站点读取；本用例与它无关，隔离为空列表，不改原断言。
vi.mock('@/modules/admin/api/mySites', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()), listRealConnections: vi.fn(async () => []) }))

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
  listHiddenAccounts: vi.fn(),
  hideAccount: vi.fn(),
  unhideAccount: vi.fn(),
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

vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
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
    ...actual,
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

vi.mock('@/modules/admin/api/connectionHealth', async original => ({
  ...await original<typeof import('@/modules/admin/api/connectionHealth')>(),
  getModelControlTarget: async (targetId: string) => ({ targetId, items: [], candidates: [] }),
  getModelControlAutoSettings: async () => c32AutoSettings(),
  getModelControlSettings: async () => ({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 0 }),
  verifyModelControl: async () => ({ items: [], errors: [] }),
  cancelQuestionAnswerBatch: harness.cancelQuestionAnswerBatch,
  getLatestQuestionAnswerBatch: harness.getLatestQuestionAnswerBatch,
  getQuestionAnswerBatch: harness.getQuestionAnswerBatch,
  getQuestionAnswerHistory: async (...args: unknown[]) => upgradeQuestionAnswerHistoryFixture(await harness.getQuestionAnswerHistory(...args)),
  listTestQuestions: harness.listTestQuestions,
  discoverTargetModels: harness.discoverTargetModels,
  getPrioritySyncStatus: harness.getPrioritySyncStatus,
  probeTargetWithProgress: harness.probeTargetWithProgress,
  setQuestionAnswerJudgment: harness.setQuestionAnswerJudgment,
  startQuestionAnswerBatch: harness.startQuestionAnswerBatch,
  listHiddenAccounts: harness.listHiddenAccounts,
  hideAccount: harness.hideAccount,
  unhideAccount: harness.unhideAccount,
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
  vi.spyOn(window, 'confirm').mockReturnValue(true)
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
  harness.listHiddenAccounts.mockReset().mockResolvedValue({ workspaceId: 'ws1', items: [] })
  harness.hideAccount.mockReset().mockResolvedValue({ kind: 'saved', targetId: '', hidden: true })
  harness.unhideAccount.mockReset().mockResolvedValue({ kind: 'saved', targetId: '', hidden: false })
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
  vi.restoreAllMocks()
  vi.useRealTimers()
  vi.unstubAllEnvs()
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
} as AdminGroupAccount)

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
  realAnswers = false,
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
        AdminGroupHealthDetail: !realAnswers,
        ConnectionHealthEventsDialog: true,
        GroupHealthSetupDrawer: true,
        ManualOneTimeProbeDialog: !realAnswers,
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

describe('model error parent entry preservation', () => {
  it('refreshes the shared queue on original current-workspace task notifications and manual refresh only', async () => {
    const refreshQueue = vi.spyOn(queuePolling, 'refreshQuestionAnswerQueue')
    const current = 'sub2api:ws1:queue-notification'
    const wrapper = await mountView([adminGroup('notification-parent', [account(current)])], storedPreferences())
    refreshQueue.mockClear()
    wrapper.getComponent(ManualOneTimeProbeDialog).vm.$emit('question-answer-started', current); await flushPromises()
    expect(refreshQueue).toHaveBeenCalledWith('ws1')
    refreshQueue.mockClear()
    wrapper.getComponent(ManualOneTimeProbeDialog).vm.$emit('question-answer-stats-dirty', 'sub2api:other:foreign'); await flushPromises()
    expect(refreshQueue).not.toHaveBeenCalled()
    const refreshButton = wrapper.findAll('button').find(button => button.text() === '刷新')!
    await refreshButton.trigger('click'); await flushPromises()
    expect(refreshQueue).toHaveBeenCalledWith('ws1')
  })
  it('opens an absent queue target read-only then removes that overlay when a current target opens manually', async () => {
    const current = 'sub2api:ws1:current-queue-target'
    const wrapper = await mountView([adminGroup('queue-parent', [account(current)])], storedPreferences())
    wrapper.getComponent(ManualOneTimeProbeDialog).vm.$emit('question-answer-view', { targetId: 'sub2api:ws1:missing', batchId: 'missing-batch' })
    await flushPromises()
    const fallback = wrapper.getComponent(QuestionAnswerScheduleBatchDialog)
    expect(fallback.props('batchId')).toBe('missing-batch')
    fallback.vm.$emit('question-answer-view', { targetId: current, batchId: 'current-batch' }); await flushPromises()
    expect(wrapper.findComponent(QuestionAnswerScheduleBatchDialog).exists()).toBe(false)
    expect(wrapper.getComponent(ManualOneTimeProbeDialog).props('target').targetId).toBe(current)
    expect(wrapper.getComponent(ManualOneTimeProbeDialog).props('initialQuestionAnswerBatchId')).toBe('current-batch')
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
  })
  it.each(['account', 'recent', 'management'] as const)('T9 opens the real %s entry with original target and exact batch', async entry => {
    const id = 'sub2api:ws1:model-error-entry'
    const batchId = 'model-error-exact-entry'
    harness.discoverModels.mockResolvedValue({ errorKey: 'admin.upstream.errors.upstreamInsufficientBalance' })
    const result = emptyBatch({ batchId, records: [record({ targetId: id, batchId, status: 'succeeded', answerBody: 'Preserved entry answer', answerJudgment: 'unreviewed' })] })
    harness.getQuestionAnswerBatch.mockResolvedValue(result)
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(result)
    const source = account(id, {
      hasEnabledProbePolicy: true,
      recentQuestionAnswer: { batchId, source: 'manual', createdAt: '2026-08-31T00:00:00Z', requests: { ...stats().requests, submitted: 1, succeeded: 1 }, reviews: { correct: 1, incorrect: 0, unreviewed: 0 }, byModel: [] } as any,
      modelControl: { open: 1, closed: 0, attention: 0, models: [] },
    })
    const wrapper = await mountView([adminGroup('entry-group', [source])], storedPreferences(), true)
    const detail = wrapper.get('section[aria-label="Group entry-group"]')
    const button = entry === 'management' ? detail.get('[data-testid="model-control-summary"]')
      : entry === 'account' ? detail.findAll('button').find(b => ['手动探活', '有未查看的问答测试'].includes(b.attributes('aria-label') || ''))
        : detail.findAll('button').find(b => b.text().trim() === '100%')
    expect(button).toBeDefined()
    await button!.trigger('click'); await flushPromises()
    const dialog = wrapper.getComponent(ManualOneTimeProbeDialog)
    expect(dialog.props('target')?.targetId).toBe(id)
    expect(dialog.props('initialQuestionAnswerBatchId')).toBe(entry === 'recent' ? batchId : null)
    expect(dialog.get('[data-testid="question-answer-pending"]').text()).toContain('Preserved entry answer')
    expect(dialog.text()).toContain('上游账户余额不足')
    expect(startButton(dialog).attributes('disabled')).toBeDefined()
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
  })
})

describe('connection health two-column layout', () => {
  it('renders every visible group without a bounded scrolling sidebar', async () => {
    const groups = Array.from(
      { length: 30 },
      (_, index) => adminGroup(`group-${index + 1}`, []),
    )
    const wrapper = await mountView(groups)

    const nav = wrapper.get('nav[aria-label="上游分组列表"]')
    const aside = nav.element.closest('aside')

    expect(aside).not.toBeNull()
    const layoutClasses = [...Array.from(aside!.classList), ...nav.classes()]
    expect(layoutClasses.some(value => /(^|:)max-h-(?!none$)/.test(value))).toBe(false)
    expect(nav.classes().some(value => /(^|:)overflow-y-(auto|scroll)$/.test(value))).toBe(false)
    const buttons = nav.findAll('button')
    expect(buttons).toHaveLength(groups.length)
    expect(new Set(buttons.map(button => button.get('span.font-medium').text())))
      .toEqual(new Set(groups.map(group => group.name)))
    const lastGroupName = buttons.at(-1)!.get('span.font-medium').text()
    await buttons.at(-1)!.trigger('click')
    await flushPromises()
    expect(wrapper.findComponent({ name: 'AdminGroupHealthDetail' }).props('group').id)
      .toBe(groups.find(group => group.name === lastGroupName)!.id)
  })
})

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
    expect(wrapper.find('[data-testid="question-answer-batch-line"]').exists()).toBe(true)
    const retry = wrapper.findAll('button').find(button => button.text().trim() === '重新读取模型')
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

const checkGroup = async (drawer: ReturnType<VueWrapper['find']>, groupId: string) => {
  await drawer.get(`input[type="checkbox"][data-group-id="${groupId}"]`).trigger('change')
  await flushPromises()
}
const startButtonOf = (drawer: ReturnType<VueWrapper['find']>) => drawer.get('[data-testid="question-answer-batch-start"]')
const blockReasonOf = (drawer: ReturnType<VueWrapper['find']>) => drawer.find('[data-testid="question-answer-batch-block-reason"]')
const simplePreferences = (overrides: Partial<QuestionAnswerPreferences> = {}) => storedPreferences({ modelIds: ['model-a'], questionIds: ['q1'], repeatCount: 1, ...overrides })

describe('batch drawer: pick questions, groups and accounts, models, then start', () => {
  it('opens with every group unchecked, expands a checked group with all accounts selected, and submits only the kept accounts in display order', async () => {
    const wrapper = await mountView([adminGroup('g1', [account('t1'), account('t2'), account('t3')]), adminGroup('g2', [account('t4')])], simplePreferences({ batchTargetIds: ['t1', 't2'] }))
    const drawer = await openBatchDrawer(wrapper)
    expect(drawer.findAll('input[data-group-id]').every(input => !(input.element as HTMLInputElement).checked)).toBe(true)
    expect(batchTargetCheckbox(drawer as unknown as VueWrapper, 't1').exists()).toBe(false)
    expect(blockReasonOf(drawer).text()).toBe('先勾选分组或账号')
    expect(harness.discoverTargetModels).not.toHaveBeenCalled()

    await checkGroup(drawer, 'g1')
    for (const id of ['t1', 't2', 't3']) expect((batchTargetCheckbox(drawer as unknown as VueWrapper, id).element as HTMLInputElement).checked).toBe(true)
    expect(batchTargetCheckbox(drawer as unknown as VueWrapper, 't4').exists()).toBe(false)
    await batchTargetCheckbox(drawer as unknown as VueWrapper, 't2').trigger('change')
    await flushPromises()
    expect(drawer.get('[data-testid="question-answer-batch-selection-summary"]').text()).toContain('已选 1 个分组、2 个账号')
    await vi.waitFor(() => expect(startButtonOf(drawer).attributes('disabled')).toBeUndefined())
    await startButtonOf(drawer).trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(2))
    expect(harness.startQuestionAnswerBatch.mock.calls.map(call => call.slice(0, 5))).toEqual([
      ['t1', ['model-a'], ['q1'], 'high', 1],
      ['t3', ['model-a'], ['q1'], 'high', 1],
    ])
  })

  it('keeps one selection per shared account, keeps it when another checked group still covers it, and reselects all on recheck', async () => {
    const shared = account('shared')
    const wrapper = await mountView([adminGroup('g1', [shared, account('a1')]), adminGroup('g2', [shared, account('b1')])], simplePreferences())
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await checkGroup(drawer, 'g2')
    const sharedBoxes = () => drawer.findAll('input[data-target-id="shared"]')
    expect(sharedBoxes()).toHaveLength(2)
    expect(drawer.text()).toContain('也在：Group g2')
    await sharedBoxes()[0].trigger('change')
    await flushPromises()
    expect(sharedBoxes().every(box => !(box.element as HTMLInputElement).checked)).toBe(true)
    await checkGroup(drawer, 'g1')
    expect(sharedBoxes()).toHaveLength(1)
    expect((sharedBoxes()[0].element as HTMLInputElement).checked).toBe(false)
    await checkGroup(drawer, 'g1')
    expect(sharedBoxes().every(box => (box.element as HTMLInputElement).checked)).toBe(true)
  })

  it('edits questions, effort, repeat and models in the drawer, writes the shared preference, keeps unseen saved models and submits the new choice', async () => {
    harness.discoverTargetModels.mockResolvedValue([{ id: 'model-a', name: 'A' }, { id: 'model-b', name: 'B' }])
    const wrapper = await mountView([adminGroup('g1', [account('t1')])], simplePreferences({ modelIds: ['model-a', 'unseen-model'] }))
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await vi.waitFor(() => expect(drawer.find('input[data-model-id="model-b"]').exists()).toBe(true))
    await drawer.get('input[data-question-id="q2"]').trigger('change')
    await drawer.get('[data-testid="question-answer-batch-effort"]').setValue('low')
    await drawer.get('[data-testid="question-answer-batch-repeat"]').setValue('3')
    await drawer.get('input[data-model-id="model-b"]').trigger('change')
    await flushPromises()
    const saved = readConnectionHealthPreferences('ws1').questionAnswer
    expect(saved.questionIds).toEqual(['q1', 'q2'])
    expect(saved.reasoningEffort).toBe('low')
    expect(saved.repeatCount).toBe(3)
    expect(saved.modelIds).toEqual(['unseen-model', 'model-a', 'model-b'])
    expect(drawer.text()).toContain('1/1 个账号有')
    await startButtonOf(drawer).trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledOnce())
    expect(harness.startQuestionAnswerBatch.mock.calls[0]?.slice(0, 5)).toEqual(['t1', ['model-a', 'model-b'], ['q1', 'q2'], 'low', 3])
  })

  it('shows default question and model without writing them, and does not refill after the user clears every choice', async () => {
    harness.discoverTargetModels.mockResolvedValue([{ id: 'other', name: 'Other' }, { id: 'gpt-5.6-sol', name: 'Sol' }])
    const wrapper = await mountView([adminGroup('g1', [account('t1')])], storedPreferences({ modelIds: ['missing'], questionIds: ['gone'] }))
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await vi.waitFor(() => expect(drawer.find('input[data-model-id="gpt-5.6-sol"]').exists()).toBe(true))
    expect((drawer.get('input[data-question-id="q1"]').element as HTMLInputElement).checked).toBe(true)
    expect((drawer.get('input[data-model-id="gpt-5.6-sol"]').element as HTMLInputElement).checked).toBe(true)
    expect(readConnectionHealthPreferences('ws1').questionAnswer.questionIds).toEqual(['gone'])
    expect(readConnectionHealthPreferences('ws1').questionAnswer.modelIds).toEqual(['missing'])
    await drawer.get('input[data-question-id="q1"]').trigger('change')
    await flushPromises()
    expect(drawer.findAll('input[data-question-id]').every(box => !(box.element as HTMLInputElement).checked)).toBe(true)
    expect(blockReasonOf(drawer).text()).toBe('先勾选题目')
    expect(startButtonOf(drawer).attributes('disabled')).toBeDefined()
  })

  it('reads models with at most four requests at once, drops queued accounts that were unchecked, and does not reread cached accounts', async () => {
    const pending = new Map<string, ReturnType<typeof deferred<Array<{ id: string; name: string }>>>>()
    let inFlight = 0
    let maxInFlight = 0
    harness.discoverTargetModels.mockImplementation((targetId: string) => {
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      const waiter = deferred<Array<{ id: string; name: string }>>()
      pending.set(targetId, waiter)
      return waiter.promise.finally(() => { inFlight-- })
    })
    const ids = ['t1', 't2', 't3', 't4', 't5', 't6']
    const wrapper = await mountView([adminGroup('g1', ids.map(id => account(id)))], simplePreferences())
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    expect(harness.discoverTargetModels.mock.calls.map(call => call[0])).toEqual(['t1', 't2', 't3', 't4'])
    expect(blockReasonOf(drawer).text()).toBe('正在读取账号模型，请稍等')
    await batchTargetCheckbox(drawer as unknown as VueWrapper, 't6').trigger('change')
    await flushPromises()
    for (const id of ['t1', 't2', 't3', 't4']) pending.get(id)!.resolve([{ id: 'model-a', name: 'A' }])
    await flushPromises()
    await vi.waitFor(() => expect(harness.discoverTargetModels.mock.calls.map(call => call[0])).toEqual(['t1', 't2', 't3', 't4', 't5']))
    pending.get('t5')!.resolve([{ id: 'model-a', name: 'A' }])
    await flushPromises()
    expect(maxInFlight).toBeLessThanOrEqual(4)
    await batchTargetCheckbox(drawer as unknown as VueWrapper, 't1').trigger('change')
    await batchTargetCheckbox(drawer as unknown as VueWrapper, 't1').trigger('change')
    await flushPromises()
    expect(harness.discoverTargetModels.mock.calls.filter(call => call[0] === 't1')).toHaveLength(1)
    expect(harness.discoverTargetModels.mock.calls.some(call => call[0] === 't6')).toBe(false)
  })

  it('keeps the existing whole-drawer stop when any group account list is incomplete', async () => {
    const wrapper = await mountView([adminGroup('g1', [account('t1')]), adminGroup('broken', [], { accountsError: 'admin.connectionHealth.errors.accountsFetch' })], simplePreferences())
    const drawer = await openBatchDrawer(wrapper)
    expect(drawer.text()).toContain('账号列表尚未完整')
    expect(drawer.find('input[data-group-id="g1"]').exists()).toBe(false)
    expect(startButtonOf(drawer).attributes('disabled')).toBeDefined()
    expect(harness.discoverTargetModels).not.toHaveBeenCalled()
  })

  it('explains empty model lists and blocked configurations per account and never requests models for blocked accounts', async () => {
    harness.discoverTargetModels.mockImplementation(async (targetId: string) => targetId === 'empty' ? [] : [{ id: 'model-a', name: 'A' }])
    const blocked = account('blocked', { testConfiguration: { status: 'conflict', protocol: '', probeTimeoutSeconds: 0, sourceGroups: [], blockedReason: 'admin.connectionHealth.errors.testConfigurationConflict' } as unknown as AdminGroupAccount['testConfiguration'] })
    const wrapper = await mountView([adminGroup('g1', [account('ok'), account('empty'), blocked])], simplePreferences())
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await vi.waitFor(() => expect(drawer.get('[data-testid="question-answer-batch-preview-empty"]').text()).toContain('没有读到任何模型'))
    expect(drawer.get('[data-testid="question-answer-batch-preview-blocked"]').text()).toContain('测试配置不可用')
    expect(drawer.get('[data-testid="question-answer-batch-preview-ok"]').text()).toContain('1 次')
    expect(harness.discoverTargetModels.mock.calls.some(call => call[0] === 'blocked')).toBe(false)
    expect(drawer.get('[data-testid="question-answer-batch-total"]').text()).toContain('共 1 个账号、1 次请求')
  })

  it('blocks start with the first applicable reason, including an account over 50 requests', async () => {
    harness.discoverTargetModels.mockResolvedValue(Array.from({ length: 6 }, (_, index) => ({ id: `m${index}`, name: `M${index}` })))
    const wrapper = await mountView([adminGroup('g1', [account('t1')])], storedPreferences({ modelIds: ['m0', 'm1', 'm2', 'm3', 'm4', 'm5'], questionIds: ['q1', 'q2'], repeatCount: 5 }))
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await vi.waitFor(() => expect(blockReasonOf(drawer).text()).toContain('超过单个账号 50 次请求'))
    expect(startButtonOf(drawer).attributes('disabled')).toBeDefined()
  })

  it('hides accounts in the hidden list from the drawer and never submits them', async () => {
    const preferences = simplePreferences().questionAnswer
    const wrapper = mount(QuestionAnswerBatchDrawer, {
      attachTo: document.body,
      props: { groups: [adminGroup('g1', [account('image'), account('text')])], hiddenTargetIds: new Set(['image']), preferenceScope: 'ws1', preferences },
    })
    mountedWrappers.push(wrapper)
    const drawer = await openBatchDrawer(wrapper)
    expect(drawer.text()).toContain('Group g1 · 1 个账号 · 另有 1 个已屏蔽')
    await checkGroup(drawer, 'g1')
    expect(batchTargetCheckbox(wrapper, 'image').exists()).toBe(false)
    await vi.waitFor(() => expect(startButtonOf(drawer).attributes('disabled')).toBeUndefined())
    await startButtonOf(drawer).trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledOnce())
    expect(harness.startQuestionAnswerBatch.mock.calls[0]?.[0]).toBe('text')
    expect(harness.discoverTargetModels.mock.calls.some(call => call[0] === 'image')).toBe(false)
  })
})

describe('hidden account page notice', () => {
  it('clears the page notice that asks for a reread once a manual reread succeeds, but not after an automatic read', async () => {
    harness.currentAccount!.value = { id: 'ws1', displayName: 'Workspace One', platform: 'sub2api' } as { id: string; displayName: string }
    harness.hideAccount.mockResolvedValue({ kind: 'unknown', reason: 'network' })
    const wrapper = await mountView([adminGroup('g1', [account('sub2api:ws1:t1')])], simplePreferences(), true)
    await flushPromises()
    await wrapper.get('[data-testid="hide-account-button"]').trigger('click')
    await flushPromises()
    const message = wrapper.get('[data-testid="hidden-accounts-notice"]').text()
    expect(message).toContain('没有确认（网络中断或超时）')
    expect(message).toContain('可能已经保存，后台也可能开始处理')
    expect(message).toContain('请先重新读取清单核对')
    expect(message).not.toContain('没有保存')
    const panelButton = wrapper.findAll('button').find(item => item.text().includes('管理分组显示'))
    await panelButton!.trigger('click')
    await flushPromises()
    harness.listHiddenAccounts.mockRejectedValueOnce(new Error('offline'))
    const reread = () => wrapper.get('[data-testid="hidden-accounts-panel"]').findAll('button').find(item => item.text() === '重新读取')!
    await reread().trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="hidden-accounts-notice"]').exists()).toBe(true)
    await reread().trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="hidden-accounts-notice"]').exists()).toBe(false)
  })
})

describe('hidden account page notice when a manual reread finishes first', () => {
  it('does not show the unconfirmed page notice after a manual reread already resolved it during the automatic reread', async () => {
    harness.currentAccount!.value = { id: 'ws1', displayName: 'Workspace One', platform: 'sub2api' } as { id: string; displayName: string }
    const automatic = deferred<{ workspaceId: string; items: [] }>()
    harness.hideAccount.mockImplementation(async () => {
      harness.listHiddenAccounts.mockReturnValueOnce(automatic.promise)
      return { kind: 'unknown', reason: 'network' }
    })
    const wrapper = await mountView([adminGroup('g1', [account('sub2api:ws1:t1')])], simplePreferences(), true)
    await flushPromises()
    await wrapper.findAll('button').find(item => item.text().includes('管理分组显示'))!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="hide-account-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="hidden-accounts-panel"]').text()).toContain('有一次屏蔽设置的结果没有确认')
    await wrapper.get('[data-testid="hidden-accounts-panel"]').findAll('button').find(item => item.text() === '重新读取')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="hidden-accounts-panel"]').text()).not.toContain('有一次屏蔽设置的结果没有确认')
    automatic.resolve({ workspaceId: 'ws1', items: [] })
    await flushPromises()
    expect(wrapper.find('[data-testid="hidden-accounts-notice"]').exists()).toBe(false)
  })
})

describe('batch drawer review fixes', () => {
  it('rereads models when an account model list changes and ignores the late answer for the old list', async () => {
    const first = deferred<Array<{ id: string; name: string }>>()
    harness.discoverTargetModels.mockReturnValueOnce(first.promise).mockResolvedValueOnce([{ id: 'model-new', name: 'New' }])
    const groups = [adminGroup('g1', [account('t1', { models: 'model-old' })])]
    const preferences = simplePreferences().questionAnswer
    const wrapper = mount(QuestionAnswerBatchDrawer, { attachTo: document.body, props: { groups, preferenceScope: 'ws1', preferences } })
    mountedWrappers.push(wrapper)
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await wrapper.setProps({ groups: [adminGroup('g1', [account('t1', { models: 'model-new' })])] })
    await flushPromises()
    expect(harness.discoverTargetModels).toHaveBeenCalledTimes(2)
    first.resolve([{ id: 'model-old', name: 'Old' }])
    await flushPromises()
    expect(drawer.find('input[data-model-id="model-old"]').exists()).toBe(false)
    expect(drawer.find('input[data-model-id="model-new"]').exists()).toBe(true)
  })

  it('keeps the running progress and the finished results visible when a group account list later fails, while still blocking a new round', async () => {
    const pending = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch.mockReturnValue(pending.promise)
    const preferences = simplePreferences().questionAnswer
    const wrapper = mount(QuestionAnswerBatchDrawer, { attachTo: document.body, props: { groups: [adminGroup('g1', [account('t1')])], preferenceScope: 'ws1', preferences } })
    mountedWrappers.push(wrapper)
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await vi.waitFor(() => expect(startButtonOf(drawer).attributes('disabled')).toBeUndefined())
    await startButtonOf(drawer).trigger('click')
    await flushPromises()
    const broken = [adminGroup('g1', [account('t1')]), adminGroup('broken', [], { accountsError: 'admin.connectionHealth.errors.accountsFetch' })]
    await wrapper.setProps({ groups: broken })
    await flushPromises()
    expect(drawer.text()).toContain('账号列表尚未完整')
    expect(drawer.find('[data-testid="question-answer-batch-run-summary"]').exists()).toBe(true)
    expect(drawer.find('[data-testid="question-answer-batch-submitting"]').exists()).toBe(true)
    pending.resolve(emptyBatch({ batchId: 'b1', submittedCount: 1 }))
    await flushPromises()
    await vi.waitFor(() => expect(drawer.find('[data-testid="question-answer-batch-outcome-t1"]').exists()).toBe(true))
    expect(drawer.find('[data-testid="question-answer-batch-run-summary"]').exists()).toBe(true)
    expect(startButtonOf(drawer).attributes('disabled')).toBeDefined()
  })

  it('does not allow starting on reopen until this session reads the questions again', async () => {
    const wrapper = await mountView([adminGroup('g1', [account('t1')])], simplePreferences())
    let drawer = await openBatchDrawer(wrapper)
    await drawer.get('[data-testid="question-answer-batch-close"]').trigger('click')
    const slow = deferred<TestQuestion[]>()
    harness.listTestQuestions.mockReturnValueOnce(slow.promise)
    drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    expect(drawer.findAll('input[data-question-id]')).toHaveLength(0)
    expect(blockReasonOf(drawer).text()).toBe('正在读取测试题，请稍等')
    expect(startButtonOf(drawer).attributes('disabled')).toBeDefined()
    slow.resolve([question('q1', true)])
    await flushPromises()
    await vi.waitFor(() => expect(startButtonOf(drawer).attributes('disabled')).toBeUndefined())
  })

  it('hides the live selection steps while a run is submitting from its frozen plan', async () => {
    const pending = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch.mockReturnValue(pending.promise)
    const wrapper = await mountView([adminGroup('g1', [account('t1')])], simplePreferences())
    const drawer = await openBatchDrawer(wrapper)
    await checkGroup(drawer, 'g1')
    await vi.waitFor(() => expect(startButtonOf(drawer).attributes('disabled')).toBeUndefined())
    await startButtonOf(drawer).trigger('click')
    await flushPromises()
    expect(drawer.find('[data-testid="question-answer-batch-step-groups"]').exists()).toBe(false)
    expect(drawer.find('[data-testid="question-answer-batch-step-confirm"]').exists()).toBe(false)
    expect(drawer.text()).toContain('本轮开始时确定的账号和进度')
    pending.resolve(emptyBatch({ batchId: 'b1', submittedCount: 1 }))
    await flushPromises()
    expect(drawer.find('[data-testid="question-answer-batch-step-groups"]').exists()).toBe(true)
  })
})

describe('batch sequential submission (kept behaviors)', () => {
  const prepared = async (groups: AdminGroupHealth[], preferences = simplePreferences()) => {
    const wrapper = await mountView(groups, preferences)
    const drawer = await openBatchDrawer(wrapper)
    for (const group of groups) await checkGroup(drawer, group.id)
    await vi.waitFor(() => expect(blockReasonOf(drawer).exists() && blockReasonOf(drawer).text() === '正在读取账号模型，请稍等').toBe(false))
    return { wrapper, drawer }
  }

  it.each(['Asia/Tokyo', 'America/New_York'])('shows the started batch summary in Singapore from %s without changing submitted requests', async timezone => {
    vi.stubEnv('TZ', timezone)
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-03T00:00:00Z'))
    const pending = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch.mockReturnValue(pending.promise)
    const { wrapper, drawer } = await prepared([adminGroup('sg-group', [account('sg-target')])])
    try {
      await startButtonOf(drawer).trigger('click')
      await flushPromises()
      expect(harness.startQuestionAnswerBatch.mock.calls[0]?.slice(0, 5)).toEqual(['sg-target', ['model-a'], ['q1'], 'high', 1])
      const summary = wrapper.get('[data-testid="question-answer-batch-entry-summary"]')
      expect(summary.text()).toContain('08:00')
      expect(summary.text()).not.toContain(timezone === 'Asia/Tokyo' ? '09:00' : '20:00')
    } finally {
      pending.resolve(emptyBatch({ batchId: 'sg-started', submittedCount: 1 }))
      await flushPromises()
    }
  })

  it('starts compatible targets serially in display order and reconciles mixed outcomes and accepted totals', async () => {
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
      return emptyBatch({ batchId: `batch-${targetId}`, submittedCount: targetId === 't1' ? 8 : 7 })
    })
    const { drawer } = await prepared(
      [adminGroup('g1', ['t1', 't2', 't3', 't4', 't5', 't6'].map(targetId => account(targetId)))],
      storedPreferences({ modelIds: ['model-a', 'model-b'], questionIds: ['q1', 'q2'], reasoningEffort: 'high', repeatCount: 2 }),
    )
    await vi.waitFor(() => expect(drawer.text()).toContain('预览总量 32'))
    await startButtonOf(drawer).trigger('click')
    await vi.waitFor(() => expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(4))
    await flushPromises()
    expect(maxInFlight).toBe(1)
    expect(harness.startQuestionAnswerBatch.mock.calls.map(call => call[0])).toEqual(['t1', 't2', 't5', 't6'])
    expect(drawer.text()).toContain('已处理 6/6')
    expect(drawer.text()).toContain('已启动 2')
    expect(drawer.text()).toContain('已跳过 2')
    expect(drawer.text()).toContain('失败 1')
    expect(drawer.text()).toContain('实际接受总量尚未确认')
    expect(drawer.text()).toContain('已确认受理 15 条')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t2"]').text()).toContain('已有活动批次')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t3"]').text()).toContain('无兼容模型')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t4"]').text()).toContain('模型发现失败')
    expect(drawer.get('[data-testid="question-answer-batch-outcome-t5"]').text()).toContain('不会自动重发')
    expect(readConnectionHealthPreferences('ws1').questionAnswerUnreadTargetIds).toEqual(['t1', 't6'])
  })

  it('ignores a double start, keeps the same run across close and reopen, and resets the choices after the run ends', async () => {
    const response = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch.mockReturnValue(response.promise)
    const { wrapper, drawer } = await prepared([adminGroup('g1', [account('t1')])])
    const start = startButtonOf(drawer)
    await Promise.all([start.trigger('click'), start.trigger('click')])
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)
    await drawer.get('[data-testid="question-answer-batch-close"]').trigger('click')
    const reopened = await openBatchDrawer(wrapper)
    expect(reopened.text()).toContain('已处理 0/1')
    expect(reopened.text()).toContain('本轮正在提交')
    expect(startButtonOf(reopened).attributes('disabled')).toBeDefined()
    response.resolve(emptyBatch({ batchId: 'batch-t1', submittedCount: 1 }))
    await flushPromises()
    expect(reopened.text()).toContain('已处理 1/1')
    expect(reopened.text()).toContain('实际接受总量 1')
    await reopened.get('[data-testid="question-answer-batch-close"]').trigger('click')
    const third = await openBatchDrawer(wrapper)
    expect(third.text()).toContain('已处理 1/1')
    expect((third.get('input[data-group-id="g1"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-testid="question-answer-batch-entry-summary"]').text()).toContain('已处理 1/1')
  })

  it('aborts the pending request on scope change and rejects its late unread write', async () => {
    const secondResponse = deferred<QuestionAnswerBatch>()
    harness.startQuestionAnswerBatch
      .mockResolvedValueOnce(emptyBatch({ batchId: 'batch-t1', submittedCount: 1 }))
      .mockReturnValueOnce(secondResponse.promise)
    const { drawer } = await prepared([adminGroup('g1', [account('t1'), account('t2')])])
    await startButtonOf(drawer).trigger('click')
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
})
