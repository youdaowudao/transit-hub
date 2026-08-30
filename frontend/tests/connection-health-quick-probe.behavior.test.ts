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

type QuickProbePhase = 'starting' | 'queued' | 'running' | ''

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
  quickProbeTargetId = '',
  quickProbePhase: QuickProbePhase = '',
  quickProbeErrors: Record<string, string> = {},
  unreadTargetIds: string[] = [],
) => {
  const wrapper = mount(AdminGroupHealthDetail, {
    props: {
      group: makeGroup(accounts),
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: unreadTargetIds,
      actionLoading: false,
      quickProbeTargetId,
      quickProbePhase,
      quickProbeErrors,
    },
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
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('AdminGroupHealthDetail quick formal probe behavior', () => {
  it('keeps the old unread Zap event and gives the new Bolt its own event without sharing unread styling', async () => {
    const account = makeAccount()
    const wrapper = mountDetail([account], '', '', {}, [account.targetId])
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

  it('maps starting, queued, and running only onto the active quick button while old actions stay enabled', async () => {
    const first = makeAccount({ id: 'first', name: '运行账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '旁路账号', targetId: 'sub2api:ws1:second' })
    const wrapper = mountDetail([first, second], first.targetId, 'starting')

    const expectedLabels: Array<[QuickProbePhase, string]> = [
      ['starting', '正在提交正式探活：gpt-5.6-sol'],
      ['queued', '正式探活排队中：gpt-5.6-sol'],
      ['running', '正式探活进行中：gpt-5.6-sol'],
    ]
    for (const [phase, label] of expectedLabels) {
      await wrapper.setProps({ quickProbePhase: phase })
      expect(buttonByAria(rowFor(wrapper, first.name), label).attributes('disabled')).toBeDefined()
    }

    expect(buttonByAriaFragment(rowFor(wrapper, second.name), '一键正式探活').attributes('disabled')).toBeDefined()
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
    const wrapper = mountDetail([withHistory, withoutHistory], '', '', { [withHistory.targetId]: longError })

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

  it('renders stream stages, disables only quick buttons, and leaves the automatic refresh and other operations usable', async () => {
    const pending = deferred<ModelHealth[]>()
    let onPhase: ((phase: 'queued' | 'running') => void) | undefined
    harness.probeTargetWithProgress.mockImplementation((_targetId, _models, phaseCallback) => {
      onPhase = phaseCallback
      return pending.promise
    })
    const first = makeAccount({ id: 'first', name: '运行账号', targetId: 'sub2api:ws1:first' })
    const second = makeAccount({ id: 'second', name: '旁路账号', targetId: 'sub2api:ws1:second' })
    const wrapper = await mountView([makeGroup([first, second])])
    harness.refreshAdminGroupsAutomatically.mockClear()

    await buttonByAria(rowFor(wrapper, first.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    expect(buttonByAria(rowFor(wrapper, first.name), '正在提交正式探活：gpt-5.6-sol').attributes('disabled')).toBeDefined()
    onPhase?.('queued')
    await nextTick()
    expect(buttonByAria(rowFor(wrapper, first.name), '正式探活排队中：gpt-5.6-sol').attributes('disabled')).toBeDefined()
    onPhase?.('running')
    await nextTick()
    expect(buttonByAria(rowFor(wrapper, first.name), '正式探活进行中：gpt-5.6-sol').attributes('disabled')).toBeDefined()
    expect(buttonByAriaFragment(rowFor(wrapper, second.name), '一键正式探活').attributes('disabled')).toBeDefined()

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

    pending.resolve([successResult(180)])
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

  it('treats an empty result as an explicit error without inventing 0 ms', async () => {
    const account = makeAccount({ modelHealth: [], unprobedModels: [{ modelName: 'gpt-5.6-sol', providerFamily: 'openai' }] })
    harness.probeTargetWithProgress.mockResolvedValue([])
    const wrapper = await mountView([makeGroup([account])])

    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('正式探活已完成，但没有返回模型结果')
    expect(rowFor(wrapper, account.name).text()).not.toContain('0 ms')
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

  it('invalidates and aborts the active quick request on unmount before late completion', async () => {
    const account = makeAccount()
    const pending = deferred<ModelHealth[]>()
    let requestSignal: AbortSignal | undefined
    harness.probeTargetWithProgress.mockImplementation((_targetId, _models, _onPhase, signal) => {
      requestSignal = signal
      return pending.promise
    })
    const wrapper = await mountView([makeGroup([account])])
    harness.loadAdminGroups.mockClear()
    await buttonByAria(rowFor(wrapper, account.name), '一键正式探活：gpt-5.6-sol').trigger('click')

    wrapper.unmount()
    removeMountedWrapper(wrapper)
    expect(requestSignal?.aborted).toBe(true)
    pending.resolve([successResult(777)])
    await flushPromises()
    expect(harness.loadAdminGroups).not.toHaveBeenCalled()
    expect((harness.refs.adminGroups.value as AdminGroupHealth[])[0].accounts[0].modelHealth[0].lastSuccessLatencyMs).toBe(120)
  })
})
