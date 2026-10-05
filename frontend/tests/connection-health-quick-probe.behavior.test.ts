// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
  ModelHealth,
} from '@/modules/admin/types/connectionHealth'

describe('ActionDiagnostics RED-6 user-facing action reasons', () => {
  it('renders one account and reason per diagnostic with named and fallback identities', async () => {
    harness.getPrioritySyncStatus.mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0, actionDiagnostics: [
      { accountId: '101', accountName: '验收账号', targetId: 'sub2api:adminacct_internal:101', action: 'priority', dispatchId: 'red6-secret-dispatch', phase: 'uncertain', reason: 'uncertain' },
      { accountId: '102', targetId: 'sub2api:adminacct_internal:102', action: 'target', dispatchId: 'red6-other-dispatch', phase: 'uncertain', reason: 'conflict' },
    ] })
    const wrapper = await mountView([makeGroup([makeAccount()])])
    const diagnostics = wrapper.get('[data-testid="remote-action-diagnostics"]')
    expect(diagnostics.findAll('p')).toHaveLength(2)
    expect(diagnostics.findAll('p')[0].text()).toBe('验收账号（#101）：远端操作结果未知，请在主站核对')
    expect(diagnostics.findAll('p')[1].text()).toBe('主站账号 #102：主站当前值与 TransitHub 写入的值不一致，请在主站核对')
    for (const internal of ['adminacct_', 'sub2api:', 'red6-secret-dispatch', 'red6-other-dispatch']) expect(diagnostics.text()).not.toContain(internal)
    expect(diagnostics.findAll('button')).toHaveLength(0)
  })

  it('does not render the diagnostic block when the backend filters ordinary actions', async () => {
    harness.getPrioritySyncStatus.mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0, actionDiagnostics: [] })
    const wrapper = await mountView([makeGroup([makeAccount()])])
    expect(wrapper.find('[data-testid="remote-action-diagnostics"]').exists()).toBe(false)
  })

  it('shows only the reason in account details and hides internal dispatch information', () => {
    const entry = makeAccount({ remoteActionPending: { action: 'target', dispatchId: 'red6-detail-dispatch', phase: 'uncertain', reason: 'uncertain', source: 'manual' } })
    const wrapper = mountDetail([entry])
    const row = rowFor(wrapper, entry.name)
    expect(row.text()).toContain('远端操作结果未知，请在主站核对')
    for (const old of ['阶段待确认', 'red6-detail-dispatch', '远端动作待确认', '人工操作']) expect(row.text()).not.toContain(old)
  })

  it.each(['same account', 'another account'])('explains the original safety interception and preserves scheduling: %s', async (owner) => {
    const entry = makeAccount({ schedulable: true })
    const other = makeAccount({ id: '102', name: '其他账号', targetId: 'sub2api:ws1:102', schedulable: true })
    const blocking = owner === 'same account' ? entry : other
    blocking.remoteActionPending = { action: 'priority', dispatchId: 'red6-blocker', phase: 'uncertain', reason: 'uncertain' }
    harness.updateTargetSchedulable.mockImplementation(async () => {
      harness.refs.errorKey.value = 'admin.connectionHealth.errors.remoteActionPending'
      return false
    })
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    try {
      const wrapper = await mountView([makeGroup([entry, other])])
      await buttonByAria(rowFor(wrapper, entry.name), '关闭主站调度').trigger('click')
      await flushPromises()
      expect(wrapper.text()).toContain('有远端操作尚未完成核对，当前操作未发送。请稍后再试；持续出现时请查看分组健康页顶部提示。')
      expect(wrapper.text()).not.toContain('该账号')
      expect(wrapper.text()).not.toContain('red6-blocker')
      expect(harness.updateTargetSchedulable).toHaveBeenCalledWith(entry.targetId, false)
      expect(entry.schedulable).toBe(true)
      expect(rowFor(wrapper, entry.name).text()).toContain('主站调度开启')
    } finally { confirm.mockRestore() }
  })
})

type QuickProbePhase = 'starting' | 'queued' | 'running' | ''
type ActiveQuickProbePhase = Exclude<QuickProbePhase, ''>

interface QuickProbeSuccess {
  modelName: string
  latencyMs: number
}

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  currentAccount: null as any,
  documentVisibility: null as any,
  intervalCallback: null as null | (() => void),
  activeWorkspaceScope: '',
  workspaceGroups: {} as Record<string, AdminGroupHealth[]>,
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
  invalidatePriorityCandidatePlanNow: vi.fn(),
  updateTargetSchedulable: vi.fn(),
  probeTargetWithProgress: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn(async () => undefined) }),
}))

vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  harness.documentVisibility = ref('hidden')
  return {
    useDocumentVisibility: () => harness.documentVisibility,
    useIntervalFn: (callback: () => void) => {
      harness.intervalCallback = callback
      return { pause: vi.fn(), resume: vi.fn() }
    },
  }
})

vi.mock('@/modules/admin/api/connectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>()
  return {
    ...actual,
    getPrioritySyncStatus: harness.getPrioritySyncStatus,
    probeTargetWithProgress: harness.probeTargetWithProgress,
  }
})

vi.mock('@/modules/admin/api/upstream', () => ({
  listUpstreamSites: harness.listUpstreamSites,
}))

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'ws1', displayName: '测试工作区' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})

vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  const { ref } = await import('vue')
  harness.refs = {
    overview: ref(null),
    groups: ref([]),
    adminGroups: ref([]),
    adminGroupsLoaded: ref(true),
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
    connectionHealthMessageKey: actual.connectionHealthMessageKey,
    connectionHealthStateBadgeClass: () => '',
    formatConnectionHealthTime: (value: string | null) => value ?? '-',
    formatConnectionHealthElapsed: () => '',
    hasValidConnectionHealthTime: (value: string | null | undefined) => Boolean(value),
    isConnectionHealthCurrentFailure: (model: ModelHealth) => Boolean(model.lastErrorKey) && !['ok', 'slow_response'].includes(model.probeResult ?? ''),
    remoteActionLabelKey: () => null,
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
      invalidatePriorityCandidatePlanNow: harness.invalidatePriorityCandidatePlanNow,
      removePolicy: vi.fn(async () => true),
      savePolicy: vi.fn(async () => true),
      updateTargetSchedulable: harness.updateTargetSchedulable,
    }),
  }
})

const mountedWrappers: VueWrapper[] = []

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const makeModel = (overrides: Partial<ModelHealth> = {}): ModelHealth => ({
  modelName: 'gpt-5.6-sol',
  providerFamily: 'openai',
  configured: true,
  state: 'healthy',
  currentWeight: 100,
  consecutiveFailures: 0,
  consecutiveSuccesses: 3,
  lastProbeAt: '2026-08-30T10:00:00Z',
  lastSuccessAt: '2026-08-30T10:00:00Z',
  lastFailureAt: null,
  lastLatencyMs: 120,
  lastSuccessLatencyMs: 120,
  lastErrorKey: '',
  lastErrorDetail: '',
  lastRemoteAction: '',
  probeResult: 'ok',
  nextProbeAt: '2026-08-30T10:30:00Z',
  effectiveIntervalSeconds: 1800,
  effectivePolicySources: [{
    policyId: 'policy-health',
    policyName: '正式健康策略',
    continueAutoProbe: true,
    effectiveIntervalSeconds: 1800,
  }],
  budgetPolicyId: 'policy-health',
  updatedAt: '2026-08-30T10:00:00Z',
  ...overrides,
})

const makeAccount = (overrides: Partial<AdminGroupAccount> = {}): AdminGroupAccount => ({
  id: 'account-1',
  name: '账号一',
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  schedulable: true,
  priority: 20,
  targetId: 'sub2api:ws1:account-1',
  probeAvailable: true,
  modelHealth: [makeModel()],
  unprobedModels: [],
  assignedPolicyIds: ['policy-health'],
  assignedPolicies: [{ policyId: 'policy-health', policyName: '正式健康策略', enabled: true, strategyMode: 'health_probe' }],
  hasAssignedPolicy: true,
  hasEnabledPolicy: true,
  hasEnabledProbePolicy: true,
  priorityManaged: true,
  probeModelsConfigured: true,
  ...overrides,
})

const makeGroup = (
  accounts: AdminGroupAccount[],
  overrides: Partial<AdminGroupHealth> = {},
): AdminGroupHealth => ({
  id: 'group-1',
  name: '正式探活分组',
  platform: 'sub2api',
  status: 'enabled',
  type: 'subscription',
  isExclusive: false,
  subscriptionType: '',
  multiplier: null,
  multiplierDisplay: '-',
  accountCount: accounts.length,
  monitoredAccountCount: accounts.filter(account => account.hasEnabledProbePolicy).length,
  healthSummary: {
    totalAccounts: accounts.length,
    probeableAccounts: accounts.filter(account => account.probeAvailable).length,
    unprobeableAccounts: accounts.filter(account => !account.probeAvailable).length,
    healthyModels: accounts.flatMap(account => account.modelHealth).filter(model => model.state === 'healthy').length,
    degradedModels: accounts.flatMap(account => account.modelHealth).filter(model => model.state === 'degraded').length,
    suspendedModels: accounts.flatMap(account => account.modelHealth).filter(model => model.state === 'suspended').length,
    disabledModels: 0,
    unconfiguredModels: 0,
    lastProbeAt: null,
  },
  accounts,
  ...overrides,
})

const successResult = (latency: number, probeResult: 'ok' | 'slow_response' = 'ok'): ModelHealth => makeModel({
  providerFamily: '',
  state: probeResult === 'slow_response' ? 'degraded' : 'healthy',
  probeResult,
  lastLatencyMs: latency,
  lastSuccessLatencyMs: latency,
  lastErrorKey: '',
  lastErrorDetail: '',
  updatedAt: '2026-08-30T11:00:00Z',
})

const partialSuccessResult = (latency: number, probeResult: 'ok' | 'slow_response' = 'ok'): ModelHealth => ({
  modelName: 'gpt-5.6-sol',
  providerFamily: '',
  configured: true,
  state: probeResult === 'slow_response' ? 'degraded' : 'healthy',
  currentWeight: probeResult === 'slow_response' ? 70 : 100,
  consecutiveFailures: 0,
  consecutiveSuccesses: 4,
  lastProbeAt: '2026-08-30T11:00:00Z',
  lastSuccessAt: '2026-08-30T11:00:00Z',
  lastFailureAt: null,
  lastLatencyMs: latency,
  lastSuccessLatencyMs: latency,
  lastErrorKey: '',
  lastErrorDetail: '',
  lastRemoteAction: '',
  probeResult,
  updatedAt: '2026-08-30T11:00:00Z',
})

const failureResult = (detail: string, latency = 9876, lastErrorKey = 'server_error'): ModelHealth => ({
  modelName: 'gpt-5.6-sol',
  providerFamily: '',
  configured: true,
  state: 'suspended',
  currentWeight: 0,
  consecutiveFailures: 4,
  consecutiveSuccesses: 0,
  lastProbeAt: '2026-08-30T11:00:00Z',
  lastSuccessAt: '2026-08-30T10:00:00Z',
  lastFailureAt: '2026-08-30T11:00:00Z',
  lastLatencyMs: latency,
  lastErrorKey,
  lastErrorDetail: detail,
  lastRemoteAction: '',
  probeResult: lastErrorKey,
  updatedAt: '2026-08-30T11:00:00Z',
})

const rowFor = (wrapper: VueWrapper, accountName: string): VueWrapper => {
  const row = wrapper.findAll('tbody > tr').find(candidate => candidate.text().includes(accountName))
  if (!row) throw new Error(`missing account row: ${accountName}`)
  return row
}

const buttonByAria = (container: VueWrapper, label: string): VueWrapper => {
  const button = container.findAll('button').find(candidate => candidate.attributes('aria-label') === label)
  if (!button) throw new Error(`missing button with aria-label: ${label}`)
  return button
}

const buttonByAriaFragment = (container: VueWrapper, fragment: string): VueWrapper => {
  const button = container.findAll('button').find(candidate => candidate.attributes('aria-label')?.includes(fragment))
  if (!button) throw new Error(`missing button containing aria-label: ${fragment}`)
  return button
}

const buttonByText = (wrapper: VueWrapper, text: string): VueWrapper => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().includes(text))
  if (!button) throw new Error(`missing button containing text: ${text}`)
  return button
}

const mountDetail = (
  accounts: AdminGroupAccount[],
  quickProbePhases: Record<string, ActiveQuickProbePhase> = {},
  quickProbeErrors: Record<string, string> = {},
  quickProbeSuccesses: Record<string, QuickProbeSuccess> = {},
  unreadTargetIds: string[] = [],
) => {
  const wrapper = mount(AdminGroupHealthDetail, {
    props: {
      group: makeGroup(accounts),
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: unreadTargetIds,
      actionLoading: false,
      quickProbePhases,
      quickProbeErrors,
      quickProbeSuccesses,
    } as any,
  })
  mountedWrappers.push(wrapper)
  return wrapper
}

const ManualDialogProbe = defineComponent({
  name: 'ManualOneTimeProbeDialog',
  props: {
    open: { type: Boolean, required: true },
    target: { type: Object, default: null },
  },
  emits: ['close'],
  setup(props, { emit }) {
    return () => props.open
      ? h('button', { 'data-test': 'manual-probe-dialog', onClick: () => emit('close') }, `旧手动探活弹窗：${(props.target as any)?.accountName ?? ''}`)
      : null
  },
})

const mountView = async (groups: AdminGroupHealth[]) => {
  harness.refs.adminGroups.value = groups
  harness.workspaceGroups.ws1 = groups
  const wrapper = mount(ConnectionHealthView, {
    global: {
      stubs: {
        Button: { template: '<button v-bind="$attrs"><slot /></button>' },
        ConnectionHealthEventsDialog: true,
        GroupHealthSetupDrawer: true,
        ManualOneTimeProbeDialog: ManualDialogProbe,
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

const removeMountedWrapper = (wrapper: VueWrapper) => {
  const index = mountedWrappers.indexOf(wrapper)
  if (index >= 0) mountedWrappers.splice(index, 1)
}

beforeEach(() => {
  for (const refValue of Object.values(harness.refs)) refValue.value = Array.isArray(refValue.value) ? [] : null
  harness.refs.overview.value = null
  harness.refs.adminGroupsLoaded.value = true
  harness.refs.isLoading.value = false
  harness.refs.isActionLoading.value = false
  harness.refs.errorKey.value = ''
  harness.refs.refreshConflictNotice.value = ''
  harness.refs.refreshConnectionState.value = 'connected'
  harness.currentAccount.value = { id: 'ws1', displayName: '测试工作区' }
  harness.documentVisibility.value = 'hidden'
  harness.intervalCallback = null
  harness.activeWorkspaceScope = 'ws1'
  harness.workspaceGroups = {}
  harness.loadAll.mockReset().mockResolvedValue(undefined)
  harness.loadGroups.mockReset().mockResolvedValue(true)
  harness.loadAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroupsAutomatically.mockReset().mockResolvedValue(true)
  harness.loadEvents.mockReset().mockResolvedValue(true)
  harness.loadPolicies.mockReset().mockResolvedValue(true)
  harness.getPrioritySyncStatus.mockReset().mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0 })
  harness.listUpstreamSites.mockReset().mockResolvedValue([])
  harness.cancelAdminGroupsRefresh.mockReset()
  harness.invalidatePriorityCandidatePlanNow.mockReset().mockImplementation(() => {
    harness.refs.adminGroups.value = (harness.refs.adminGroups.value as AdminGroupHealth[]).map(group => {
      const nextGroup = {
        ...group,
        accounts: group.accounts.map(account => {
          const nextAccount = { ...account }
          delete nextAccount.priorityCandidate
          return nextAccount
        }),
      }
      delete nextGroup.priorityCandidateSummary
      return nextGroup
    })
  })
  harness.updateTargetSchedulable.mockReset().mockResolvedValue(true)
  harness.probeTargetWithProgress.mockReset().mockResolvedValue([])
  harness.setAdminGroupsWorkspace.mockReset().mockImplementation((workspaceId: string) => {
    if (harness.activeWorkspaceScope === workspaceId) return
    harness.activeWorkspaceScope = workspaceId
    harness.refs.adminGroups.value = harness.workspaceGroups[workspaceId] ?? []
    harness.refs.terminalRefreshSummary.value = null
    harness.refs.refreshRunSnapshot.value = null
    harness.refs.errorKey.value = ''
  })
})

afterEach(() => {
  vi.useRealTimers()
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('AdminGroupHealthDetail quick formal probe behavior', () => {
  it('keeps the old unread Zap event and gives the new Bolt its own event without sharing unread styling', async () => {
    const account = makeAccount()
    const wrapper = mountDetail([account], {}, {}, {}, [account.targetId])
    const row = rowFor(wrapper, account.name)

    const oldProbe = buttonByAria(row, '有未查看的问答测试')
    expect(oldProbe.classes().join(' ')).toContain('amber')
    await oldProbe.trigger('click')
    expect(wrapper.emitted('probe')).toEqual([[account]])

    const quickProbe = buttonByAria(row, '一键正式探活：gpt-5.6-sol')
    expect(quickProbe.classes().join(' ')).not.toContain('amber')
    await quickProbe.trigger('click')

    expect(wrapper.emitted('quick-probe')).toEqual([[account]])
    expect(wrapper.emitted('probe')).toHaveLength(1)
  })

  it('uses separate exact gates for probe availability, enabled formal policy, and formal models', async () => {
    const knownUnavailable = makeAccount({
      id: 'known', name: '已知不可探活', targetId: 'sub2api:ws1:known', probeAvailable: false,
      probeUnavailableReason: 'credential_unavailable',
    })
    const unknownUnavailable = makeAccount({
      id: 'unknown', name: '未知不可探活', targetId: 'sub2api:ws1:unknown', probeAvailable: false,
      probeUnavailableReason: 'new_safe_reason',
    })
    const noPolicy = makeAccount({
      id: 'no-policy', name: '没有正式策略', targetId: 'sub2api:ws1:no-policy',
      assignedPolicyIds: [], assignedPolicies: [], hasAssignedPolicy: false,
      hasEnabledPolicy: false, hasEnabledProbePolicy: false,
    })
    const noModels = makeAccount({
      id: 'no-models', name: '没有正式模型', targetId: 'sub2api:ws1:no-models',
      modelHealth: [], unprobedModels: [],
    })
    const wrapper = mountDetail([knownUnavailable, unknownUnavailable, noPolicy, noModels])

    const knownRow = rowFor(wrapper, knownUnavailable.name)
    expect(buttonByAria(knownRow, '一键正式探活不可用：无法安全获取上游凭据，暂不可探活').attributes('disabled')).toBeDefined()
    expect(buttonByAria(knownRow, '手动探活').attributes('disabled')).toBeDefined()

    const unknownRow = rowFor(wrapper, unknownUnavailable.name)
    expect(buttonByAria(unknownRow, '一键正式探活不可用：当前账号暂不可正式探活').attributes('disabled')).toBeDefined()

    const noPolicyRow = rowFor(wrapper, noPolicy.name)
    const noPolicyQuick = buttonByAria(noPolicyRow, '一键正式探活不可用：未启用正式探活策略')
    expect(noPolicyQuick.attributes('disabled')).toBeDefined()
    expect(buttonByAria(noPolicyRow, '手动探活').attributes('disabled')).toBeUndefined()
    await noPolicyQuick.trigger('click')
    expect(wrapper.emitted('quick-probe')).toBeUndefined()

    const noModelsRow = rowFor(wrapper, noModels.name)
    expect(buttonByAria(noModelsRow, '一键正式探活不可用：没有正式探活模型').attributes('disabled')).toBeDefined()
    expect(buttonByAria(noModelsRow, '手动探活').attributes('disabled')).toBeUndefined()
  })

  it('maps each phase only onto its account, blocks a duplicate, and leaves another quick button clickable', async () => {
    const first = makeAccount({ id: 'first', name: '运行账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '旁路账号', targetId: 'sub2api:ws1:second' })
    const wrapper = mountDetail([first, second], { [first.targetId]: 'starting' })

    const expectedLabels: Array<[ActiveQuickProbePhase, string]> = [
      ['starting', '正在提交正式探活：gpt-5.6-sol'],
      ['queued', '正式探活排队中：gpt-5.6-sol'],
      ['running', '正式探活进行中：gpt-5.6-sol'],
    ]
    for (const [phase, label] of expectedLabels) {
      await wrapper.setProps({ quickProbePhases: { [first.targetId]: phase } } as any)
      expect(buttonByAria(rowFor(wrapper, first.name), label).attributes('disabled')).toBeDefined()
    }

    await buttonByAriaFragment(rowFor(wrapper, first.name), '正式探活进行中').trigger('click')
    expect(wrapper.emitted('quick-probe')).toBeUndefined()

    const secondQuickProbe = buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol')
    expect(secondQuickProbe.attributes('disabled')).toBeUndefined()
    await secondQuickProbe.trigger('click')
    expect(wrapper.emitted('quick-probe')).toEqual([[second]])

    expect(buttonByAria(rowFor(wrapper, first.name), '手动探活').attributes('disabled')).toBeUndefined()
    expect(buttonByAria(rowFor(wrapper, first.name), '设置账号策略').attributes('disabled')).toBeUndefined()
    expect(buttonByAria(rowFor(wrapper, first.name), '查看事件').attributes('disabled')).toBeUndefined()
    expect(buttonByAria(rowFor(wrapper, first.name), '关闭主站调度').attributes('disabled')).toBeUndefined()
  })

  it('keeps historical success latency for suspended failures and places a complete wrapping error directly under the account row', () => {
    const longError = '上游返回安全失败详情：第一行\n第二行包含很长但必须完整显示的错误说明'
    const withHistory = makeAccount({
      id: 'history', name: '有历史延迟', targetId: 'sub2api:ws1:history',
      modelHealth: [makeModel({
        state: 'suspended', probeResult: 'server_error', lastSuccessLatencyMs: 321,
        lastLatencyMs: 9876, lastErrorKey: 'server_error', lastErrorDetail: longError,
      })],
    })
    const withoutHistory = makeAccount({
      id: 'no-history', name: '无历史延迟', targetId: 'sub2api:ws1:no-history',
      modelHealth: [makeModel({
        state: 'suspended', probeResult: 'server_error', lastSuccessLatencyMs: null,
        lastLatencyMs: 7654, lastErrorKey: 'server_error', lastErrorDetail: '失败',
      })],
    })
    const wrapper = mountDetail([withHistory, withoutHistory], {}, { [withHistory.targetId]: longError })

    const historyRow = rowFor(wrapper, withHistory.name)
    expect(historyRow.text()).toContain('321 ms')
    expect(historyRow.text()).not.toContain('9876 ms')
    const rows = wrapper.findAll('tbody > tr')
    const historyIndex = rows.findIndex(row => row.text().includes(withHistory.name))
    const errorRow = rows[historyIndex + 1]
    expect(errorRow.text()).toContain('第一行')
    expect(errorRow.text()).toContain('第二行包含很长但必须完整显示的错误说明')
    expect(errorRow.classes()).toContain('quick-probe-error-row')
    expect(errorRow.find('.whitespace-pre-wrap').exists()).toBe(true)
    expect(errorRow.find('.break-words').exists()).toBe(true)
    expect(errorRow.find('.truncate').exists()).toBe(false)

    const noHistoryRow = rowFor(wrapper, withoutHistory.name)
    expect(noHistoryRow.text()).not.toContain('7654 ms')
    expect(noHistoryRow.text()).not.toMatch(/\d+ ms/)
  })
})

describe('ConnectionHealthView quick formal probe session behavior', () => {
  it('keeps the old dialog entry and sends the preferred model through the independent non-modal action', async () => {
    const account = makeAccount({
      modelHealth: [makeModel({ modelName: 'other-model' }), makeModel({ modelName: 'gpt-5.6-sol' })],
    })
    const wrapper = await mountView([makeGroup([account])])
    const row = rowFor(wrapper, account.name)

    await buttonByAria(row, '手动探活').trigger('click')
    expect(wrapper.get('[data-test="manual-probe-dialog"]').text()).toContain(account.name)
    await wrapper.get('[data-test="manual-probe-dialog"]').trigger('click')

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="manual-probe-dialog"]').exists()).toBe(false)
    expect(harness.probeTargetWithProgress).toHaveBeenCalledWith(
      account.targetId,
      ['gpt-5.6-sol'],
      expect.any(Function),
      expect.any(AbortSignal),
    )
  })

  it('falls back to the first projected formal model without discovering or selecting extra models', async () => {
    const account = makeAccount({
      modelHealth: [makeModel({ modelName: 'first-formal' }), makeModel({ modelName: 'second-formal' })],
    })
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：first-formal').trigger('click')
    await flushPromises()

    expect(harness.probeTargetWithProgress).toHaveBeenCalledWith(
      account.targetId,
      ['first-formal'],
      expect.any(Function),
      expect.any(AbortSignal),
    )
  })

  it('submits different accounts independently, renders their own stream stages, and blocks only same-account duplicates', async () => {
    const pendingByTarget = new Map<string, ReturnType<typeof deferred<ModelHealth[]>>>()
    const phaseByTarget = new Map<string, (phase: 'queued' | 'running') => void>()
    harness.probeTargetWithProgress.mockImplementation((targetId, _models, phaseCallback) => {
      const pending = deferred<ModelHealth[]>()
      pendingByTarget.set(targetId, pending)
      phaseByTarget.set(targetId, phaseCallback)
      return pending.promise
    })
    const first = makeAccount({ id: 'first', name: '运行账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '旁路账号', targetId: 'sub2api:ws1:second' })
    const third = makeAccount({ id: 'third', name: '待点击账号', targetId: 'sub2api:ws1:third' })
    const wrapper = await mountView([makeGroup([first, second, third])])
    harness.refreshAdminGroupsAutomatically.mockClear()

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    expect(buttonByAria(rowFor(wrapper, first.name), '正在提交正式探活：gpt-5.6-sol').attributes('disabled')).toBeDefined()
    expect(buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').attributes('disabled')).toBeUndefined()

    await buttonByAriaFragment(rowFor(wrapper, first.name), '正在提交正式探活').trigger('click')
    expect(harness.probeTargetWithProgress).toHaveBeenCalledTimes(1)

    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    expect(harness.probeTargetWithProgress).toHaveBeenCalledTimes(2)
    expect(buttonByAria(rowFor(wrapper, second.name), '正在提交正式探活：gpt-5.6-sol').attributes('disabled')).toBeDefined()
    expect(buttonByAria(rowFor(wrapper, third.name), '一键正式探活：gpt-5.6-sol').attributes('disabled')).toBeUndefined()

    phaseByTarget.get(first.targetId)?.('running')
    phaseByTarget.get(second.targetId)?.('queued')
    await nextTick()
    expect(buttonByAria(rowFor(wrapper, first.name), '正式探活进行中：gpt-5.6-sol').attributes('disabled')).toBeDefined()
    expect(buttonByAria(rowFor(wrapper, second.name), '正式探活排队中：gpt-5.6-sol').attributes('disabled')).toBeDefined()

    expect(buttonByAria(rowFor(wrapper, first.name), '手动探活').attributes('disabled')).toBeUndefined()
    expect(buttonByAria(rowFor(wrapper, first.name), '设置账号策略').attributes('disabled')).toBeUndefined()
    expect(buttonByAria(rowFor(wrapper, first.name), '查看事件').attributes('disabled')).toBeUndefined()
    expect(buttonByAria(rowFor(wrapper, first.name), '关闭主站调度').attributes('disabled')).toBeUndefined()
    expect(buttonByText(wrapper, '策略').attributes('disabled')).toBeUndefined()
    expect(buttonByText(wrapper, '事件').attributes('disabled')).toBeUndefined()
    expect(harness.refs.isActionLoading.value).toBe(false)

    harness.documentVisibility.value = 'visible'
    harness.intervalCallback?.()
    await flushPromises()
    expect(harness.refreshAdminGroupsAutomatically).toHaveBeenCalledTimes(1)

    pendingByTarget.get(first.targetId)?.resolve([successResult(180)])
    pendingByTarget.get(second.targetId)?.resolve([successResult(190)])
    await flushPromises()
  })

  it.each([
    ['ok', 145],
    ['slow_response', 6500],
  ] as const)('merges a %s result into every target projection, preserves metadata, removes unprobed duplicates, and shows no error', async (probeResult, latency) => {
    const sourceMetadata = {
      modelName: 'gpt-5.6-sol',
      providerFamily: 'custom-family',
      nextProbeAt: '2026-08-30T12:00:00Z',
      effectiveIntervalSeconds: 3600,
      effectivePolicySources: [{
        policyId: 'policy-meta', policyName: '元数据策略', continueAutoProbe: true, effectiveIntervalSeconds: 3600,
      }],
      budgetPolicyId: 'policy-meta',
    }
    const targetId = 'sub2api:ws1:shared'
    const accountFromHealth = makeAccount({
      id: 'shared-a', name: '已有模型投影', targetId,
      modelHealth: [makeModel({ ...sourceMetadata, lastSuccessLatencyMs: 111 })],
    })
    const accountFromUnprobed = makeAccount({
      id: 'shared-b', name: '待探活模型投影', targetId,
      modelHealth: [], unprobedModels: [{ ...sourceMetadata }],
    })
    harness.probeTargetWithProgress.mockResolvedValue([partialSuccessResult(latency, probeResult)])
    const groups = [
      makeGroup([accountFromHealth], { id: 'group-a', name: '分组 A' }),
      makeGroup([accountFromUnprobed], { id: 'group-b', name: '分组 B' }),
    ]
    const wrapper = await mountView(groups)

    await buttonByAria(rowFor(wrapper, accountFromHealth.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    for (const group of harness.refs.adminGroups.value as AdminGroupHealth[]) {
      const account = group.accounts.find(candidate => candidate.targetId === targetId)
      const model = account?.modelHealth.find(candidate => candidate.modelName === 'gpt-5.6-sol')
      expect(model).toMatchObject({
        providerFamily: 'custom-family',
        nextProbeAt: '2026-08-30T12:00:00Z',
        effectiveIntervalSeconds: 3600,
        effectivePolicySources: sourceMetadata.effectivePolicySources,
        budgetPolicyId: 'policy-meta',
        probeResult,
        lastSuccessLatencyMs: latency,
      })
      expect(account?.unprobedModels?.some(candidate => candidate.modelName === 'gpt-5.6-sol')).toBe(false)
    }
    expect(rowFor(wrapper, accountFromHealth.name).text()).toContain(`${latency} ms`)
    expect(wrapper.find('.quick-probe-error-row').exists()).toBe(false)
    expect(wrapper.find('.quick-probe-success-row').text()).toContain(`正式探活完成：gpt-5.6-sol · 本次延迟 ${latency} ms`)
  })

  it('shows the completed model and current probe latency for twenty seconds while the account latency stays the projected maximum', async () => {
    vi.useFakeTimers()
    const account = makeAccount({
      modelHealth: [
        makeModel({ modelName: 'gpt-5.6-sol', lastLatencyMs: 120, lastSuccessLatencyMs: 120 }),
        makeModel({ modelName: 'historical-slower-model', lastLatencyMs: 900, lastSuccessLatencyMs: 900 }),
      ],
    })
    harness.probeTargetWithProgress.mockResolvedValue([successResult(246)])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    expect(rowFor(wrapper, account.name).text()).toContain('900 ms')
    expect(rowFor(wrapper, account.name).text()).not.toContain('246 ms')
    const successRow = wrapper.find('.quick-probe-success-row')
    expect(successRow.exists()).toBe(true)
    expect(successRow.text()).toContain('正式探活完成：gpt-5.6-sol · 本次延迟 246 ms')
    expect(wrapper.find('.quick-probe-error-row').exists()).toBe(false)

    vi.advanceTimersByTime(19_999)
    await nextTick()
    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(true)

    vi.advanceTimersByTime(1)
    await nextTick()
    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(false)
  })

  it('clears a completed notice on explicit refresh without changing the authoritative latency semantics', async () => {
    const account = makeAccount()
    harness.probeTargetWithProgress.mockResolvedValue([successResult(246)])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.find('.quick-probe-success-row').text()).toContain('本次延迟 246 ms')

    await buttonByText(wrapper, '刷新').trigger('click')
    await flushPromises()

    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(false)
    expect(rowFor(wrapper, account.name).text()).toContain('246 ms')
  })

  it('uses probeResult for a failed result, preserves historical success latency, and never presents failed elapsed time as success latency', async () => {
    const account = makeAccount({
      modelHealth: [makeModel({ lastSuccessLatencyMs: 321, lastLatencyMs: 321 })],
    })
    harness.probeTargetWithProgress.mockResolvedValue([failureResult('本次正式探活返回安全失败详情', 9876)])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    const row = rowFor(wrapper, account.name)
    expect(row.text()).toContain('321 ms')
    expect(row.text()).not.toContain('9876 ms')
    expect(wrapper.text()).toContain('本次正式探活返回安全失败详情')
    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(false)
  })

  it.each([
    ['已知错误', 'server_error', '上游服务异常'],
    ['未知错误', 'private_upstream_failure', '暂时无法读取分组健康数据，请稍后重试。'],
  ] as const)('shows a safe Chinese category for a failed result without detail: %s', async (_label, errorKey, expectedText) => {
    const account = makeAccount()
    harness.probeTargetWithProgress.mockResolvedValue([failureResult('', 9876, errorKey)])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain(expectedText)
    expect(wrapper.text()).not.toContain(errorKey)
  })

  it.each([
    {
      label: '成功结果',
      result: successResult(246),
      expectedDetail: '',
    },
    {
      label: '失败结果',
      result: failureResult('权威补读失败前已经确认的探活错误'),
      expectedDetail: '权威补读失败前已经确认的探活错误',
    },
  ])('does not let a rejected authoritative reload replace the confirmed $label conclusion', async ({ result, expectedDetail }) => {
    const account = makeAccount({
      modelHealth: [makeModel({ lastSuccessLatencyMs: 321, lastLatencyMs: 321 })],
    })
    harness.probeTargetWithProgress.mockResolvedValue([result])
    harness.loadAdminGroups.mockRejectedValue(new Error('admin.connectionHealth.errors.network'))
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    expect(wrapper.text()).not.toContain('网络异常，请检查连接后重试。')
    if (expectedDetail) {
      expect(wrapper.text()).toContain(expectedDetail)
    } else {
      expect(rowFor(wrapper, account.name).text()).toContain('246 ms')
      expect(wrapper.find('.quick-probe-error-row').exists()).toBe(false)
    }
  })

  it('invalidates the old Priority candidate plan before merging a confirmed quick probe result and keeps it invalidated when reload fails', async () => {
    harness.currentAccount.value = { id: 'ws1', displayName: '测试工作区', platform: 'sub2api' }
    const account = makeAccount({
      modelHealth: [makeModel({ lastSuccessLatencyMs: 120, lastLatencyMs: 120 })],
      priorityCandidate: {
        state: 'candidate',
        rank: 1,
        priority: 10,
        region: 'normal',
        healthBand: 'healthy',
        successLatencyMs: 120,
        multiplier: 1,
        priorityEvidence: 'last_applied',
        blocksTakeover: false,
      },
    })
    const group = makeGroup([account], {
      priorityCandidateSummary: {
        mode: 'first_active',
        candidatePriorityReady: true,
        candidateCount: 1,
        outOfScopeCount: 0,
        blockerCount: 0,
        capacities: [],
      },
    })
    harness.probeTargetWithProgress.mockResolvedValue([successResult(246)])
    harness.loadAdminGroups.mockRejectedValue(new Error('admin.connectionHealth.errors.network'))
    const wrapper = await mountView([group])

    expect(wrapper.text()).toContain('只读候选排序')
    expect((harness.refs.adminGroups.value as AdminGroupHealth[])[0]?.accounts[0]?.modelHealth[0]?.lastLatencyMs).toBe(120)

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    const updatedGroup = (harness.refs.adminGroups.value as AdminGroupHealth[])[0]
    expect(updatedGroup?.priorityCandidateSummary).toBeUndefined()
    expect(updatedGroup?.accounts[0]?.priorityCandidate).toBeUndefined()
    expect(updatedGroup?.accounts[0]?.modelHealth[0]?.lastLatencyMs).toBe(246)
    expect(wrapper.text()).not.toContain('只读候选排序')
    expect(wrapper.text()).not.toContain('网络异常，请检查连接后重试。')
  })

  it('treats an empty result as an explicit error without inventing 0 ms', async () => {
    const account = makeAccount({ modelHealth: [], unprobedModels: [{ modelName: 'gpt-5.6-sol', providerFamily: 'openai' }] })
    harness.probeTargetWithProgress.mockResolvedValue([])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('正式探活已完成，但没有返回模型结果')
    expect(rowFor(wrapper, account.name).text()).not.toContain('0 ms')
    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(false)
  })

  it('clears only the retried account, preserves other account errors, and clears the retried account after success', async () => {
    const first = makeAccount({ id: 'first', name: '账号甲', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '账号乙', targetId: 'sub2api:ws1:second' })
    const retry = deferred<ModelHealth[]>()
    harness.probeTargetWithProgress
      .mockResolvedValueOnce([failureResult('账号甲旧错误')])
      .mockResolvedValueOnce([failureResult('账号乙错误')])
      .mockReturnValueOnce(retry.promise)
    const wrapper = await mountView([makeGroup([first, second])])

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('账号甲旧错误')

    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('账号甲旧错误')
    expect(wrapper.text()).toContain('账号乙错误')

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await nextTick()
    expect(wrapper.text()).not.toContain('账号甲旧错误')
    expect(wrapper.text()).toContain('账号乙错误')

    retry.resolve([successResult(222)])
    await flushPromises()
    expect(wrapper.text()).not.toContain('账号甲旧错误')
    expect(wrapper.text()).toContain('账号乙错误')
  })

  it('keeps temporary errors across the 30-second automatic refresh and clears all of them on explicit refresh', async () => {
    const account = makeAccount()
    harness.probeTargetWithProgress.mockResolvedValue([failureResult('自动刷新不得清除')])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('自动刷新不得清除')

    harness.documentVisibility.value = 'visible'
    harness.intervalCallback?.()
    await flushPromises()
    expect(wrapper.text()).toContain('自动刷新不得清除')

    await buttonByText(wrapper, '刷新').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('自动刷新不得清除')
  })

  it('does not cancel an in-flight quick request on explicit refresh and still shows its later failure', async () => {
    const first = makeAccount({ id: 'first', name: '已有错误账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '在途账号', targetId: 'sub2api:ws1:second' })
    const pending = deferred<ModelHealth[]>()
    let requestSignal: AbortSignal | undefined
    harness.probeTargetWithProgress
      .mockResolvedValueOnce([failureResult('刷新前已有错误')])
      .mockImplementationOnce((_targetId, _models, _onPhase, signal) => {
        requestSignal = signal
        return pending.promise
      })
    const wrapper = await mountView([makeGroup([first, second])])

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await nextTick()

    await buttonByText(wrapper, '刷新').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('刷新前已有错误')
    expect(requestSignal?.aborted).toBe(false)

    pending.reject(new Error('admin.connectionHealth.errors.network'))
    await flushPromises()
    expect(wrapper.text()).toContain('网络异常，请检查连接后重试。')
  })

  it.each([
    ['刷新先完成', true],
    ['探活先完成', false],
  ] as const)('finishes overlapping automatic refresh in the %s order and finally applies the post-probe authoritative read', async (_label, refreshFirst) => {
    const account = makeAccount()
    const probe = deferred<ModelHealth[]>()
    const refresh = deferred<boolean>()
    harness.probeTargetWithProgress.mockReturnValue(probe.promise)
    const wrapper = await mountView([makeGroup([account])])
    harness.refreshAdminGroupsAutomatically.mockReset().mockReturnValue(refresh.promise)
    harness.loadAdminGroups.mockReset().mockImplementation(async () => {
      harness.refs.adminGroups.value = [makeGroup([
        makeAccount({ modelHealth: [makeModel({ lastLatencyMs: 444, lastSuccessLatencyMs: 444 })] }),
      ])]
      return true
    })

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    harness.documentVisibility.value = 'visible'
    harness.intervalCallback?.()
    await nextTick()

    if (refreshFirst) {
      refresh.resolve(true)
      await flushPromises()
      probe.resolve([successResult(111)])
      await flushPromises()
    } else {
      probe.resolve([successResult(111)])
      await flushPromises()
      expect(harness.loadAdminGroups).not.toHaveBeenCalled()
      refresh.resolve(true)
      await flushPromises()
    }

    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)
    expect(rowFor(wrapper, account.name).text()).toContain('444 ms')
    expect(rowFor(wrapper, account.name).text()).not.toContain('111 ms')
  })

  it('preserves an earlier pending authoritative read when a later quick request fails before producing a result', async () => {
    const first = makeAccount({ id: 'first', name: '待补读账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '后续失败账号', targetId: 'sub2api:ws1:second' })
    const refresh = deferred<boolean>()
    harness.refreshAdminGroupsAutomatically.mockReset().mockReturnValue(refresh.promise)
    harness.probeTargetWithProgress
      .mockResolvedValueOnce([successResult(111)])
      .mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network'))
    harness.loadAdminGroups.mockReset().mockImplementation(async () => {
      harness.refs.adminGroups.value = [makeGroup([
        makeAccount({
          id: first.id,
          name: first.name,
          targetId: first.targetId,
          modelHealth: [makeModel({ lastLatencyMs: 444, lastSuccessLatencyMs: 444 })],
        }),
        second,
      ])]
      return true
    })
    const wrapper = await mountView([makeGroup([first, second])])

    harness.documentVisibility.value = 'visible'
    harness.intervalCallback?.()
    await nextTick()

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(rowFor(wrapper, first.name).text()).toContain('111 ms')
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()

    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('网络异常，请检查连接后重试。')
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()

    refresh.resolve(true)
    await flushPromises()

    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)
    expect(rowFor(wrapper, first.name).text()).toContain('444 ms')
    expect(rowFor(wrapper, first.name).text()).not.toContain('111 ms')
  })

  it('keeps one authoritative read in flight and drains the newer obligation when different accounts finish together', async () => {
    const first = makeAccount({ id: 'first', name: '并发账号甲', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '并发账号乙', targetId: 'sub2api:ws1:second' })
    const firstProbe = deferred<ModelHealth[]>()
    const secondProbe = deferred<ModelHealth[]>()
    const firstAuthorityRead = deferred<boolean>()
    let authorityReadsInFlight = 0
    let maxAuthorityReadsInFlight = 0
    let authorityReadSequence = 0

    harness.probeTargetWithProgress.mockImplementation((targetId) => (
      targetId === first.targetId ? firstProbe.promise : secondProbe.promise
    ))
    harness.loadAdminGroups.mockReset().mockImplementation(async () => {
      const sequence = ++authorityReadSequence
      authorityReadsInFlight++
      maxAuthorityReadsInFlight = Math.max(maxAuthorityReadsInFlight, authorityReadsInFlight)
      try {
        if (sequence === 1) await firstAuthorityRead.promise
        if (sequence === 2) {
          harness.refs.adminGroups.value = [makeGroup([
            makeAccount({
              id: first.id,
              name: first.name,
              targetId: first.targetId,
              modelHealth: [makeModel({ lastLatencyMs: 444, lastSuccessLatencyMs: 444 })],
            }),
            makeAccount({
              id: second.id,
              name: second.name,
              targetId: second.targetId,
              modelHealth: [makeModel({ lastLatencyMs: 555, lastSuccessLatencyMs: 555 })],
            }),
          ])]
        }
        return true
      } finally {
        authorityReadsInFlight--
      }
    })
    const wrapper = await mountView([makeGroup([first, second])])

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    expect(harness.probeTargetWithProgress).toHaveBeenCalledTimes(2)

    firstProbe.resolve([successResult(111)])
    await flushPromises()
    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)

    secondProbe.resolve([successResult(222)])
    await flushPromises()
    expect(rowFor(wrapper, second.name).text()).toContain('222 ms')
    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)
    expect(maxAuthorityReadsInFlight).toBe(1)

    firstAuthorityRead.resolve(true)
    await flushPromises()

    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(2)
    expect(maxAuthorityReadsInFlight).toBe(1)
    expect(rowFor(wrapper, first.name).text()).toContain('444 ms')
    expect(rowFor(wrapper, second.name).text()).toContain('555 ms')
    expect(rowFor(wrapper, second.name).text()).not.toContain('222 ms')
  })

  it('serializes authoritative reads so a later failed read cannot invalidate the earlier read or lose the latest obligation', async () => {
    const first = makeAccount({ id: 'first', name: '先完成账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '后完成账号', targetId: 'sub2api:ws1:second' })
    const refresh = deferred<boolean>()
    const secondProbe = deferred<ModelHealth[]>()
    const firstAuthorityRead = deferred<boolean>()
    let requestSequence = 0
    let authorityReadsInFlight = 0
    let maxAuthorityReadsInFlight = 0

    harness.refreshAdminGroupsAutomatically.mockReset().mockReturnValue(refresh.promise)
    harness.probeTargetWithProgress
      .mockResolvedValueOnce([successResult(111)])
      .mockReturnValueOnce(secondProbe.promise)
    harness.loadAdminGroups.mockReset().mockImplementation(async () => {
      const sequence = ++requestSequence
      authorityReadsInFlight++
      maxAuthorityReadsInFlight = Math.max(maxAuthorityReadsInFlight, authorityReadsInFlight)
      const startedConcurrently = authorityReadsInFlight > 1
      try {
        if (sequence === 1) await firstAuthorityRead.promise
        if (startedConcurrently) return false
        if (sequence !== requestSequence) return false
        const finalLatency = sequence > 1 ? 555 : 120
        harness.refs.adminGroups.value = [makeGroup([
          makeAccount({
            id: first.id,
            name: first.name,
            targetId: first.targetId,
            modelHealth: [makeModel({ lastLatencyMs: 444, lastSuccessLatencyMs: 444 })],
          }),
          makeAccount({
            id: second.id,
            name: second.name,
            targetId: second.targetId,
            modelHealth: [makeModel({ lastLatencyMs: finalLatency, lastSuccessLatencyMs: finalLatency })],
          }),
        ])]
        return true
      } finally {
        authorityReadsInFlight--
      }
    })
    const wrapper = await mountView([makeGroup([first, second])])

    harness.documentVisibility.value = 'visible'
    harness.intervalCallback?.()
    await nextTick()

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await nextTick()

    refresh.resolve(true)
    await flushPromises()
    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)

    secondProbe.resolve([successResult(222)])
    await flushPromises()
    expect(rowFor(wrapper, second.name).text()).toContain('222 ms')
    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)
    expect(maxAuthorityReadsInFlight).toBe(1)

    firstAuthorityRead.resolve(true)
    await flushPromises()

    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(2)
    expect(maxAuthorityReadsInFlight).toBe(1)
    expect(rowFor(wrapper, first.name).text()).toContain('444 ms')
    expect(rowFor(wrapper, second.name).text()).toContain('555 ms')
    expect(rowFor(wrapper, second.name).text()).not.toContain('120 ms')
    expect(rowFor(wrapper, second.name).text()).not.toContain('222 ms')
  })

  it('registers a pending authoritative read when loadAdminGroups returns false and retries it after refresh completes', async () => {
    const account = makeAccount()
    harness.probeTargetWithProgress.mockResolvedValue([successResult(111)])
    harness.loadAdminGroups.mockReset().mockResolvedValueOnce(false).mockImplementationOnce(async () => {
      harness.refs.adminGroups.value = [makeGroup([
        makeAccount({ modelHealth: [makeModel({ lastLatencyMs: 555, lastSuccessLatencyMs: 555 })] }),
      ])]
      return true
    })
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(1)
    expect(rowFor(wrapper, account.name).text()).toContain('111 ms')

    await buttonByText(wrapper, '刷新').trigger('click')
    await flushPromises()

    expect(harness.loadAdminGroups).toHaveBeenCalledTimes(2)
    expect(rowFor(wrapper, account.name).text()).toContain('555 ms')
  })

  it('aborts every workspace-A request before switching and rejects all late phases, results, notices, and reloads', async () => {
    const firstA = makeAccount({ id: 'first-a', name: '工作区 A 账号甲', targetId: 'sub2api:workspace-a:first-a' })
    const secondA = makeAccount({ id: 'second-a', name: '工作区 A 账号乙', targetId: 'sub2api:workspace-a:second-a' })
    const accountB = makeAccount({ id: 'account-b', name: '工作区 B 账号', targetId: 'sub2api:workspace-b:account-b' })
    const pendingByTarget = new Map<string, ReturnType<typeof deferred<ModelHealth[]>>>()
    const signalByTarget = new Map<string, AbortSignal>()
    const phaseByTarget = new Map<string, (phase: 'queued' | 'running') => void>()
    harness.currentAccount.value = { id: 'workspace-a', displayName: '工作区 A' }
    harness.activeWorkspaceScope = 'workspace-a'
    harness.probeTargetWithProgress.mockImplementation((targetId, _models, phaseCallback, signal) => {
      const pending = deferred<ModelHealth[]>()
      pendingByTarget.set(targetId, pending)
      signalByTarget.set(targetId, signal)
      phaseByTarget.set(targetId, phaseCallback)
      return pending.promise
    })
    const wrapper = await mountView([makeGroup([firstA, secondA], { id: 'group-a', name: '工作区 A 分组' })])
    harness.loadAdminGroups.mockClear()

    await buttonByAria(rowFor(wrapper, firstA.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await buttonByAria(rowFor(wrapper, secondA.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    expect(harness.probeTargetWithProgress).toHaveBeenCalledTimes(2)

    harness.workspaceGroups['workspace-b'] = [makeGroup([accountB], { id: 'group-b', name: '工作区 B 分组' })]
    harness.currentAccount.value = { id: 'workspace-b', displayName: '工作区 B' }
    await flushPromises()

    expect(signalByTarget.get(firstA.targetId)?.aborted).toBe(true)
    expect(signalByTarget.get(secondA.targetId)?.aborted).toBe(true)
    phaseByTarget.get(firstA.targetId)?.('running')
    phaseByTarget.get(secondA.targetId)?.('queued')
    pendingByTarget.get(firstA.targetId)?.resolve([successResult(888)])
    pendingByTarget.get(secondA.targetId)?.resolve([successResult(999)])
    await flushPromises()

    expect(wrapper.text()).toContain(accountB.name)
    expect(wrapper.text()).not.toContain(firstA.name)
    expect(wrapper.text()).not.toContain(secondA.name)
    expect(wrapper.text()).not.toContain('888 ms')
    expect(wrapper.text()).not.toContain('999 ms')
    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(false)
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()
  })

  it.each(['success', 'failure'] as const)('invalidates and aborts a workspace-A request before a late %s can write phase, result, error, reload, or final state into workspace B', async (outcome) => {
    const accountA = makeAccount({ id: 'account-a', name: '工作区 A 账号', targetId: 'sub2api:workspace-a:account-a' })
    const accountB = makeAccount({ id: 'account-b', name: '工作区 B 账号', targetId: 'sub2api:workspace-b:account-b' })
    harness.currentAccount.value = { id: 'workspace-a', displayName: '工作区 A' }
    harness.activeWorkspaceScope = 'workspace-a'
    const pending = deferred<ModelHealth[]>()
    let requestSignal: AbortSignal | undefined
    let onPhase: ((phase: 'queued' | 'running') => void) | undefined
    harness.probeTargetWithProgress.mockImplementation((_targetId, _models, phaseCallback, signal) => {
      onPhase = phaseCallback
      requestSignal = signal
      return pending.promise
    })
    const wrapper = await mountView([makeGroup([accountA], { id: 'group-a', name: '工作区 A 分组' })])
    harness.loadAdminGroups.mockClear()
    await buttonByAria(rowFor(wrapper, accountA.name), '一键正式探活：gpt-5.6-sol').trigger('click')

    harness.workspaceGroups['workspace-b'] = [makeGroup([accountB], { id: 'group-b', name: '工作区 B 分组' })]
    harness.currentAccount.value = { id: 'workspace-b', displayName: '工作区 B' }
    await flushPromises()
    expect(requestSignal?.aborted).toBe(true)
    expect(wrapper.text()).toContain('工作区 B 账号')
    expect(wrapper.text()).not.toContain('工作区 A 账号')

    onPhase?.('running')
    if (outcome === 'success') pending.resolve([successResult(999)])
    else pending.reject(new Error('工作区 A 迟到失败'))
    await flushPromises()

    expect(wrapper.text()).toContain('工作区 B 账号')
    expect(wrapper.text()).not.toContain('999 ms')
    expect(wrapper.text()).not.toContain('工作区 A 迟到失败')
    expect(wrapper.text()).not.toContain('正式探活进行中')
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()
  })

  it('clears an already displayed workspace-A error after switching to B and back to A', async () => {
    const accountA = makeAccount({ id: 'account-a', name: '工作区 A 账号', targetId: 'sub2api:workspace-a:account-a' })
    const accountB = makeAccount({ id: 'account-b', name: '工作区 B 账号', targetId: 'sub2api:workspace-b:account-b' })
    const groupA = makeGroup([accountA], { id: 'group-a', name: '工作区 A 分组' })
    const groupB = makeGroup([accountB], { id: 'group-b', name: '工作区 B 分组' })
    harness.currentAccount.value = { id: 'workspace-a', displayName: '工作区 A' }
    harness.activeWorkspaceScope = 'workspace-a'
    harness.workspaceGroups['workspace-a'] = [groupA]
    harness.workspaceGroups['workspace-b'] = [groupB]
    harness.probeTargetWithProgress.mockResolvedValue([failureResult('工作区 A 临时错误')])
    const wrapper = await mountView([groupA])

    await buttonByAria(rowFor(wrapper, accountA.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('工作区 A 临时错误')

    harness.currentAccount.value = { id: 'workspace-b', displayName: '工作区 B' }
    await flushPromises()
    expect(wrapper.text()).toContain('工作区 B 账号')
    expect(wrapper.text()).not.toContain('工作区 A 临时错误')

    harness.currentAccount.value = { id: 'workspace-a', displayName: '工作区 A' }
    await flushPromises()
    expect(wrapper.text()).toContain('工作区 A 账号')
    expect(wrapper.text()).not.toContain('工作区 A 临时错误')
  })

  it('invalidates and aborts every active quick request on unmount before late completion', async () => {
    const first = makeAccount({ id: 'first', name: '卸载账号甲', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '卸载账号乙', targetId: 'sub2api:ws1:second' })
    const pendingByTarget = new Map<string, ReturnType<typeof deferred<ModelHealth[]>>>()
    const signalByTarget = new Map<string, AbortSignal>()
    harness.probeTargetWithProgress.mockImplementation((targetId, _models, _onPhase, signal) => {
      const pending = deferred<ModelHealth[]>()
      pendingByTarget.set(targetId, pending)
      signalByTarget.set(targetId, signal)
      return pending.promise
    })
    const wrapper = await mountView([makeGroup([first, second])])
    harness.loadAdminGroups.mockClear()
    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await buttonByAria(rowFor(wrapper, second.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    expect(harness.probeTargetWithProgress).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    removeMountedWrapper(wrapper)
    expect(signalByTarget.get(first.targetId)?.aborted).toBe(true)
    expect(signalByTarget.get(second.targetId)?.aborted).toBe(true)
    pendingByTarget.get(first.targetId)?.resolve([successResult(777)])
    pendingByTarget.get(second.targetId)?.resolve([successResult(888)])
    await flushPromises()
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()
    expect((harness.refs.adminGroups.value as AdminGroupHealth[])[0].accounts[0].modelHealth[0].lastSuccessLatencyMs).toBe(120)
    expect((harness.refs.adminGroups.value as AdminGroupHealth[])[0].accounts[1].modelHealth[0].lastSuccessLatencyMs).toBe(120)
  })
})


describe('protocol and unresolved remote action safety display', () => {
  it('expands only the models belonging to the selected current-state filter in a mixed account and preserves old unconfigured accounts', async () => {
    const mixed = makeAccount({ name: 'Mixed account', modelHealth: [
      makeModel({ modelName: 'old-healthy-pending', currentHealthResult: { status: 'unverified' } }),
      makeModel({ modelName: 'current-healthy', currentHealthResult: { status: 'success' } }),
    ] })
    const oldUnconfigured = makeAccount({ id: 'old-unconfigured', targetId: 'sub2api:ws1:old-unconfigured', name: 'Old unconfigured account', probeModelsConfigured: false, modelHealth: [] })
    const wrapper = mountDetail([mixed, oldUnconfigured])
    const group = makeGroup([mixed, oldUnconfigured])
    group.healthSummary.healthyModels = 1
    group.healthSummary.unconfiguredModels = 2
    await wrapper.setProps({ group })
    const breakdown = wrapper.get('section[aria-label="当前分组探活状态"]')
    await buttonByText(breakdown, '健康').trigger('click')
    await buttonByAria(rowFor(wrapper, mixed.name), '展开模型结果').trigger('click')
    expect(wrapper.findAll('tbody > tr')[1].text()).toContain('current-healthy')
    expect(wrapper.findAll('tbody > tr')[1].text()).not.toContain('old-healthy-pending')
    expect(wrapper.text()).not.toContain(oldUnconfigured.name)
    await buttonByText(breakdown, '未配置').trigger('click')
    await buttonByAria(rowFor(wrapper, mixed.name), '展开模型结果').trigger('click')
    expect(wrapper.findAll('tbody > tr').find(row => row.text().includes('old-healthy-pending'))?.text()).not.toContain('current-healthy')
    expect(wrapper.text()).toContain('old-healthy-pending')
    expect(wrapper.text()).toContain(oldUnconfigured.name)
  })

  it('keeps the old credential-unavailable entry disabled even with a configuration conflict', async () => {
    const account = makeAccount({ probeAvailable: false, probeUnavailableReason: 'credential_unavailable', testConfiguration: { status: 'conflict', sourceGroups: [] } })
    const wrapper = await mountView([makeGroup([account])])
    const entry = buttonByAria(rowFor(wrapper, account.name), '手动探活')
    expect(entry.attributes('disabled')).toBeDefined()
    await entry.trigger('click'); await flushPromises()
    expect(wrapper.find('[data-test="manual-probe-dialog"]').exists()).toBe(false)
  })

  it.each([
    ['testConfigurationUnavailable', '成员资料或测试配置无法确认，暂不能发起新测试。'],
    ['currentProtocolUnverified', '当前协议尚未满足健康判定条件，历史状态暂不用于新的健康动作。'],
  ])('renders the server %s error in the existing result area', async (key, message) => {
    const account = makeAccount()
    harness.probeTargetWithProgress.mockRejectedValue(new Error(`admin.connectionHealth.errors.${key}`))
    const wrapper = await mountView([makeGroup([account])])
    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(message)
    expect(wrapper.text()).not.toContain(`admin.connectionHealth.errors.${key}`)
    expect(wrapper.text()).not.toContain('暂时无法读取分组健康数据')
  })

  it('filters current unverified models separately from historical healthy states', async () => {
    const pending = makeAccount({ id: 'pending', targetId: 'sub2api:ws1:pending', name: '当前协议待验证账号',
      modelHealth: [makeModel({ currentHealthResult: { status: 'unverified' } })],
    })
    const healthy = makeAccount({ id: 'current', targetId: 'sub2api:ws1:current', name: '当前协议已验证账号',
      modelHealth: [makeModel({ currentHealthResult: { status: 'success', protocol: 'responses' } })],
    })
    const wrapper = mountDetail([pending, healthy])
    const group = makeGroup([pending, healthy])
    group.healthSummary.healthyModels = 1
    group.healthSummary.unconfiguredModels = 1
    await wrapper.setProps({ group })
    const breakdown = wrapper.get('section[aria-label="当前分组探活状态"]')
    await buttonByText(breakdown, '健康').trigger('click')
    expect(wrapper.findAll('tbody > tr').map(row => row.text()).join(' ')).toContain(healthy.name)
    expect(wrapper.findAll('tbody > tr').map(row => row.text()).join(' ')).not.toContain(pending.name)
    await buttonByText(breakdown, '未配置').trigger('click')
    expect(wrapper.findAll('tbody > tr').map(row => row.text()).join(' ')).toContain(pending.name)
    expect(wrapper.findAll('tbody > tr').map(row => row.text()).join(' ')).not.toContain(healthy.name)
  })

  it.each(['conflict', 'unavailable'] as const)('keeps the existing question-answer dialog reachable when configuration is %s', async status => {
    const account = makeAccount({ probeAvailable: false, probeUnavailableReason: 'test_configuration_unavailable',
      testConfiguration: { status, sourceGroups: [], blockedReason: 'admin.connectionHealth.errors.testConfigurationUnavailable' },
    })
    const wrapper = await mountView([makeGroup([account])])
    const row = rowFor(wrapper, account.name)
    const historyEntry = buttonByAria(row, '手动探活')
    expect(historyEntry.attributes('disabled')).toBeUndefined()
    await historyEntry.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="manual-probe-dialog"]').text()).toContain(account.name)
    expect(buttonByAriaFragment(row, '一键正式探活').attributes('disabled')).toBeDefined()
    expect(harness.probeTargetWithProgress).not.toHaveBeenCalled()
  })

  it('shows the server configuration-conflict explanation without a raw key or generic fallback', async () => {
    const account = makeAccount()
    harness.probeTargetWithProgress.mockRejectedValue(new Error('admin.connectionHealth.errors.testConfigurationConflict'))
    const wrapper = await mountView([makeGroup([account])])
    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('所属分组的测试协议或超时冲突，请在分组设置中统一配置。')
    expect(wrapper.text()).not.toContain('admin.connectionHealth.errors.testConfigurationConflict')
    expect(wrapper.text()).not.toContain('暂时无法读取分组健康数据')
  })

  it.each(['same account', 'another account in the protected group'])('shows an accurate rejected scheduling-close message and preserves all account values and pending details: %s', async (pendingOwner) => {
    const confirm = vi.spyOn(window, 'confirm').mockClear().mockReturnValue(true)
    try {
      const pending = { action: 'schedulable', dispatchId: 'dispatch-blocking-close', phase: 'uncertain', reason: 'uncertain' }
      const target = makeAccount(pendingOwner === 'same account' ? { remoteActionPending: pending } : {})
      const other = makeAccount({ id: 'account-2', name: '未决账号二', targetId: 'sub2api:ws1:account-2',
        ...(pendingOwner !== 'same account' ? { remoteActionPending: pending } : {}),
      })
      harness.updateTargetSchedulable.mockImplementationOnce(async () => {
        harness.refs.errorKey.value = 'admin.connectionHealth.errors.remoteActionPending'
        return false
      })
      const wrapper = await mountView([makeGroup([target, other])])
      harness.loadAll.mockClear()
      await buttonByAria(rowFor(wrapper, target.name), '关闭主站调度').trigger('click')
      await flushPromises()
      expect(confirm).toHaveBeenCalledTimes(1)
      expect(harness.updateTargetSchedulable).toHaveBeenCalledWith(target.targetId, false)
      expect(wrapper.text()).toContain('有远端操作尚未完成核对，当前操作未发送。请稍后再试；持续出现时请查看分组健康页顶部提示。')
      expect(wrapper.text()).not.toContain('同一账号')
      expect(wrapper.text()).not.toContain('该账号')
      expect(wrapper.text()).not.toContain('最后一个可用账号')
      for (const entry of [target, other]) {
        expect(rowFor(wrapper, entry.name).text()).toContain('主站调度开启')
        expect(entry.schedulable).toBe(true)
        expect(buttonByAria(rowFor(wrapper, entry.name), '关闭主站调度').exists()).toBe(true)
      }
      expect(wrapper.text()).not.toContain(pending.dispatchId)
      expect(wrapper.text()).toContain('结果未知')
      expect(harness.loadAll).not.toHaveBeenCalled()
    } finally {
      confirm.mockRestore()
    }
  })

  it('keeps unresolved visible and invisible targets in the existing status area without a clear control', async () => {
    harness.getPrioritySyncStatus.mockResolvedValue({ workspaceId: 'ws1', status: 'success', failedCount: 0, actionDiagnostics: [
      { accountId: 'missing', targetId: 'sub2api:ws1:missing', action: 'priority', dispatchId: 'dispatch-missing', phase: 'uncertain', reason: 'target_not_visible' },
      { accountId: 'visible', accountName: '需核对账号', targetId: 'sub2api:ws1:visible', action: 'target', dispatchId: 'dispatch-visible', phase: 'uncertain', reason: 'uncertain' },
    ] })
    const account = makeAccount({ remoteActionPending: { action: 'target', dispatchId: 'dispatch-visible', phase: 'uncertain', reason: 'uncertain' } })
    const wrapper = await mountView([makeGroup([account])])
    const diagnostics = wrapper.get('[data-testid="remote-action-diagnostics"]')
    expect(diagnostics.text()).toContain('主站账号 #missing：已不在任何分组，TransitHub 已无法管理它；不再使用请在主站删除，继续使用请加回分组')
    expect(diagnostics.text()).not.toContain('dispatch-missing')
    expect(diagnostics.text()).toContain('需核对账号（#visible）：远端操作结果未知，请在主站核对')
    expect(diagnostics.findAll('button')).toHaveLength(0)
    expect(rowFor(wrapper, account.name).text()).not.toContain('dispatch-visible')
    expect(rowFor(wrapper, account.name).text()).toContain('远端操作结果未知，请在主站核对')
  })

  it('blocks a conflicted quick probe and explains its protocol sources', async () => {
    const account = makeAccount({ testConfiguration: { status: 'conflict', blockedReason: 'admin.connectionHealth.errors.testConfigurationConflict', sourceGroups: [
      { adminGroupId: 'g1', adminGroupName: 'Source One', protocol: 'responses', probeTimeoutSeconds: 30 },
      { adminGroupId: 'g2', adminGroupName: 'Source Two', protocol: 'responses', probeTimeoutSeconds: 20 },
    ] } })
    const wrapper = mountDetail([account])
    const button = buttonByAriaFragment(rowFor(wrapper, account.name), '一键正式探活')
    expect(button.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('配置冲突')
    expect(wrapper.text()).toContain('Source One: Responses / 30s')
    expect(wrapper.text()).toContain('Source Two: Responses / 20s')
    await button.trigger('click')
    expect(wrapper.emitted('quick-probe')).toBeUndefined()
  })

  it('labels stale formal completion without merging its old state or showing success', async () => {
    const account = makeAccount({ modelHealth: [makeModel({ currentWeight: 45, state: 'degraded' })] })
    harness.probeTargetWithProgress.mockResolvedValue([{ ...successResult(123), probeDisposition: 'stale', requestProtocol: 'chat_completions', requestTimeoutSeconds: 10 }])
    const wrapper = await mountView([makeGroup([account])])
    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('过期尝试')
    expect(wrapper.find('.quick-probe-success-row').exists()).toBe(false)
    expect(harness.refs.adminGroups.value[0].accounts[0].modelHealth[0].currentWeight).toBe(45)
  })
})
