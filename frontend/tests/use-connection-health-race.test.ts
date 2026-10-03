import { afterEach, describe, expect, it, vi } from 'vitest'

const getConnectionHealthAdminGroupsMock = vi.hoisted(() => vi.fn())
const refreshConnectionHealthAdminGroupsMock = vi.hoisted(() => vi.fn())
const refreshConnectionHealthAdminGroupsAutomaticallyMock = vi.hoisted(() => vi.fn())
const getConnectionHealthEventsMock = vi.hoisted(() => vi.fn())
const setTargetSchedulableMock = vi.hoisted(() => vi.fn())
const setTargetPolicyAssignmentsMock = vi.hoisted(() => vi.fn())
const listConnectionHealthPoliciesMock = vi.hoisted(() => vi.fn())
const createConnectionHealthPolicyMock = vi.hoisted(() => vi.fn())
const updateConnectionHealthPolicyMock = vi.hoisted(() => vi.fn())
const setAdminGroupTestConfigurationMock = vi.hoisted(() => vi.fn())

vi.mock('../src/modules/admin/api/connectionHealth', async () => {
  const actual = await vi.importActual<typeof import('../src/modules/admin/api/connectionHealth')>('../src/modules/admin/api/connectionHealth')
  return {
    ...actual,
    getConnectionHealthAdminGroups: getConnectionHealthAdminGroupsMock,
    refreshConnectionHealthAdminGroups: refreshConnectionHealthAdminGroupsMock,
    refreshConnectionHealthAdminGroupsAutomatically: refreshConnectionHealthAdminGroupsAutomaticallyMock,
    getConnectionHealthEvents: getConnectionHealthEventsMock,
    setTargetSchedulable: setTargetSchedulableMock,
    setTargetPolicyAssignments: setTargetPolicyAssignmentsMock,
    listConnectionHealthPolicies: listConnectionHealthPoliciesMock,
    createConnectionHealthPolicy: createConnectionHealthPolicyMock,
    updateConnectionHealthPolicy: updateConnectionHealthPolicyMock,
    setAdminGroupTestConfiguration: setAdminGroupTestConfigurationMock,
  }
})
vi.mock('@/modules/admin/api/connectionHealth', async () => {
  const actual = await vi.importActual<typeof import('../src/modules/admin/api/connectionHealth')>('../src/modules/admin/api/connectionHealth')
  return {
    ...actual,
    getConnectionHealthAdminGroups: getConnectionHealthAdminGroupsMock,
    refreshConnectionHealthAdminGroups: refreshConnectionHealthAdminGroupsMock,
    refreshConnectionHealthAdminGroupsAutomatically: refreshConnectionHealthAdminGroupsAutomaticallyMock,
    getConnectionHealthEvents: getConnectionHealthEventsMock,
    setTargetSchedulable: setTargetSchedulableMock,
    setTargetPolicyAssignments: setTargetPolicyAssignmentsMock,
    listConnectionHealthPolicies: listConnectionHealthPoliciesMock,
    createConnectionHealthPolicy: createConnectionHealthPolicyMock,
    updateConnectionHealthPolicy: updateConnectionHealthPolicyMock,
    setAdminGroupTestConfiguration: setAdminGroupTestConfigurationMock,
  }
})

import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import type { ConnectionHealthPolicy, PolicyInput } from '@/modules/admin/types/connectionHealth'

afterEach(() => vi.resetAllMocks())

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

type RefreshLifecycleService = ReturnType<typeof useConnectionHealth> & {
  cancelAdminGroupsRefresh?: () => void
  setAdminGroupsWorkspace?: (workspaceId: string) => void
}

const cancelRefreshSubscription = (service: RefreshLifecycleService) => {
  service.cancelAdminGroupsRefresh?.()
}

const setRefreshWorkspace = (service: RefreshLifecycleService, workspaceId: string) => {
  service.setAdminGroupsWorkspace?.(workspaceId)
}

const group = (id: string) => ({ id, name: id, platform: 'sub2api', type: 'public', accounts: [] })

const candidateGroup = (schedulable: boolean, state: 'candidate' | 'safety_lock' = 'candidate') => ({
  id: 'candidate-group',
  name: 'candidate-group',
  platform: 'sub2api',
  type: 'public',
  priorityCandidateSummary: state === 'candidate'
    ? { mode: 'first_active', candidatePriorityReady: true, candidateCount: 1, outOfScopeCount: 0, blockerCount: 0, capacities: [] }
    : { mode: 'safety_lock', candidatePriorityReady: false, safetyReason: 'unavailable', candidateCount: 0, outOfScopeCount: 0, blockerCount: 0, capacities: [] },
  accounts: [{
    id: 'candidate-account',
    targetId: 'sub2api:workspace-candidate:candidate-account',
    name: 'Candidate account',
    schedulable,
    probeAvailable: true,
    hasAssignedPolicy: true,
    hasEnabledPolicy: true,
    hasEnabledProbePolicy: true,
    modelHealth: [],
    priorityCandidate: state === 'candidate'
      ? { state: 'candidate', rank: 1, priority: 10, region: 'normal', healthBand: 'healthy', successLatencyMs: 10, multiplier: 1, priorityEvidence: 'last_applied', blocksTakeover: false }
      : { state: 'safety_lock', reason: 'unavailable', priorityEvidence: 'last_applied', blocksTakeover: false },
  }],
})

const candidatePlanPresent = (service: ReturnType<typeof useConnectionHealth>) =>
  service.adminGroups.value.some(groupItem => groupItem.priorityCandidateSummary
    || groupItem.accounts.some(account => account.priorityCandidate))

const policyInput = (enabled: boolean, id?: string): PolicyInput => ({
  id,
  name: id ? `Policy ${id}` : 'New policy',
  enabled,
  ownGroupId: '',
  ownGroupName: '',
  autoDegradeEnabled: true,
  autoRemoteActionEnabled: false,
  priorityMode: 'multiplier',
  strategyMode: 'health_probe',
  modelTargets: [{ modelName: 'gpt-5.6-sol', providerFamily: 'openai', enabled: true }],
})

const policyResult = (input: PolicyInput): ConnectionHealthPolicy => ({
  id: input.id ?? 'policy-created',
  name: input.name,
  enabled: input.enabled,
  ownGroupId: input.ownGroupId,
  ownGroupName: input.ownGroupName,
  modelPattern: '',
  probeMode: 'responses',
  probeIntervalSeconds: 60,
  continueProbeWhenUnschedulable: false,
  unschedulableProbeIntervalMinutes: 30,
  failureThreshold: 2,
  successThreshold: 2,
  cooldownSeconds: 60,
  observationSeconds: 60,
  recoveryStepPercent: 10,
  autoDegradeEnabled: input.autoDegradeEnabled,
  autoRemoteActionEnabled: input.autoRemoteActionEnabled,
  priorityMode: input.priorityMode,
  strategyMode: input.strategyMode,
  dailyProbeBudget: 100,
  createdAt: '2026-09-06T00:00:00Z',
  updatedAt: '2026-09-06T00:00:00Z',
  modelTargets: [{
    id: 'model-target-1',
    policyId: input.id ?? 'policy-created',
    modelName: 'gpt-5.6-sol',
    providerFamily: 'openai',
    enabled: true,
    probePrompt: '',
    maxProbeTokens: 1,
    createdAt: '2026-09-06T00:00:00Z',
    updatedAt: '2026-09-06T00:00:00Z',
  }],
})

const policyMutationCases = [
  {
    name: 'policy creation',
    input: policyInput(true),
    invoke: (service: ReturnType<typeof useConnectionHealth>, input: PolicyInput) => service.savePolicy(input),
  },
  {
    name: 'policy update',
    input: policyInput(true, 'policy-update'),
    invoke: (service: ReturnType<typeof useConnectionHealth>, input: PolicyInput) => service.savePolicy(input),
  },
  {
    name: 'policy enable',
    input: policyInput(true, 'policy-enable'),
    invoke: (service: ReturnType<typeof useConnectionHealth>, input: PolicyInput) => service.savePolicy(input),
  },
  {
    name: 'policy disable',
    input: policyInput(false, 'policy-disable'),
    invoke: (service: ReturnType<typeof useConnectionHealth>, input: PolicyInput) => service.savePolicy(input),
  },
  {
    name: 'setup policy creation',
    input: policyInput(true),
    invoke: (service: ReturnType<typeof useConnectionHealth>, input: PolicyInput) => service.createPolicyForSetup(input),
  },
  {
    name: 'setup policy update',
    input: policyInput(true, 'policy-setup-update'),
    invoke: (service: ReturnType<typeof useConnectionHealth>, input: PolicyInput) => service.updatePolicyForSetup(input.id!, input),
  },
]

describe('useConnectionHealth request generations', () => {
  it('invalidates candidates after a schedulable change and rejects the older refresh terminal before a fresh reload', async () => {
    const targetId = 'sub2api:workspace-candidate:candidate-account'
    const staleTerminal = {
      status: 'success' as const,
      runId: 'candidate-stale-refresh',
      revision: 1,
      groups: [candidateGroup(true)],
      refresh: { state: 'success' as const, sites: [] },
    }
    const pendingRefresh = deferred<typeof staleTerminal>()
    let refreshOptions: any
    getConnectionHealthAdminGroupsMock
      .mockResolvedValueOnce([candidateGroup(true)])
      .mockResolvedValueOnce([candidateGroup(false, 'safety_lock')])
    refreshConnectionHealthAdminGroupsMock.mockImplementationOnce((options: any) => {
      refreshOptions = options
      return pendingRefresh.promise
    })
    setTargetSchedulableMock.mockResolvedValueOnce({
      targetId,
      schedulable: false,
      actionSource: 'user',
      actionAt: '2026-09-06T00:00:00Z',
    })

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-candidate')
    let immediateSchedulable: boolean | undefined
    let immediatePlanPresent = true
    let finalSchedulable: boolean | undefined
    let finalReady: boolean | undefined
    let finalCandidateState: string | undefined
    let reloadRequests = 0
    try {
      await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
      expect(candidatePlanPresent(service)).toBe(true)

      const refresh = service.refreshAdminGroups()
      await Promise.resolve()
      await expect(service.updateTargetSchedulable(targetId, false)).resolves.toBe(true)
      immediateSchedulable = service.adminGroups.value[0]?.accounts[0]?.schedulable
      immediatePlanPresent = candidatePlanPresent(service)

      refreshOptions.onTerminal(staleTerminal)
      pendingRefresh.resolve(staleTerminal)
      await expect(refresh).resolves.toBe(true)
      await Promise.resolve()
      await Promise.resolve()

      reloadRequests = getConnectionHealthAdminGroupsMock.mock.calls.length
      finalSchedulable = service.adminGroups.value[0]?.accounts[0]?.schedulable
      finalReady = service.adminGroups.value[0]?.priorityCandidateSummary?.candidatePriorityReady
      finalCandidateState = service.adminGroups.value[0]?.accounts[0]?.priorityCandidate?.state
    } finally {
      cancelRefreshSubscription(service)
      setRefreshWorkspace(service, '')
    }

    expect(immediateSchedulable).toBe(false)
    expect(immediatePlanPresent).toBe(false)
    expect(reloadRequests).toBe(2)
    expect(finalSchedulable).toBe(false)
    expect(finalReady).toBe(false)
    expect(finalCandidateState).toBe('safety_lock')
  })

  it('invalidates candidates after a target policy save and rejects an older ordinary read', async () => {
    const targetId = 'sub2api:workspace-candidate:candidate-account'
    const staleRead = deferred<ReturnType<typeof candidateGroup>[]>()
    const freshRead = deferred<ReturnType<typeof candidateGroup>[]>()
    getConnectionHealthAdminGroupsMock
      .mockResolvedValueOnce([candidateGroup(true)])
      .mockReturnValueOnce(staleRead.promise)
      .mockReturnValueOnce(freshRead.promise)
    setTargetPolicyAssignmentsMock.mockResolvedValueOnce({ targetId, policyIds: ['policy-next'], policies: [] })

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-candidate')
    let planAfterSave = true
    let oldReadResult = true
    let planAfterOldRead = true
    let planAfterFreshRead = false
    try {
      await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
      const oldRead = service.loadAdminGroups({ silent: true })

      await expect(service.saveTargetPolicyAssignments(targetId, ['policy-next'])).resolves.toEqual({
        assignments: { targetId, policyIds: ['policy-next'], policies: [] },
      })
      planAfterSave = candidatePlanPresent(service)

      staleRead.resolve([candidateGroup(true)])
      oldReadResult = await oldRead
      planAfterOldRead = candidatePlanPresent(service)

      freshRead.resolve([candidateGroup(true)])
      await vi.waitFor(() => expect(getConnectionHealthAdminGroupsMock).toHaveBeenCalledTimes(3))
      planAfterFreshRead = candidatePlanPresent(service)
    } finally {
      cancelRefreshSubscription(service)
      setRefreshWorkspace(service, '')
    }

    expect(planAfterSave).toBe(false)
    expect(oldReadResult).toBe(false)
    expect(planAfterOldRead).toBe(false)
    expect(planAfterFreshRead).toBe(true)
  })

  it.each(policyMutationCases)('immediately invalidates candidates after $name succeeds while the policy reload hangs and fails', async ({ input, invoke }) => {
    const staleRead = deferred<ReturnType<typeof candidateGroup>[]>()
    const authoritativeRead = deferred<ReturnType<typeof candidateGroup>[]>()
    const policyReload = deferred<ConnectionHealthPolicy[]>()
    getConnectionHealthAdminGroupsMock
      .mockResolvedValueOnce([candidateGroup(true)])
      .mockReturnValueOnce(staleRead.promise)
      .mockReturnValueOnce(authoritativeRead.promise)
    const result = policyResult(input)
    createConnectionHealthPolicyMock.mockResolvedValueOnce(result)
    updateConnectionHealthPolicyMock.mockResolvedValueOnce(result)
    listConnectionHealthPoliciesMock.mockReturnValueOnce(policyReload.promise)

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-candidate')
    let oldRead: Promise<boolean> | undefined
    let mutation: Promise<unknown> | undefined
    try {
      await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
      expect(candidatePlanPresent(service)).toBe(true)
      oldRead = service.loadAdminGroups({ silent: true })

      mutation = invoke(service, input)
      await vi.waitFor(() => expect(listConnectionHealthPoliciesMock).toHaveBeenCalledTimes(1))

      expect(candidatePlanPresent(service), 'the successful write must invalidate candidates before policy reload settles').toBe(false)

      staleRead.resolve([candidateGroup(true)])
      await expect(oldRead).resolves.toBe(false)
      expect(candidatePlanPresent(service), 'a read started before the write must not restore its candidate plan').toBe(false)

      policyReload.reject(new Error('admin.connectionHealth.errors.network'))
      await expect(mutation).resolves.not.toHaveProperty('errorKey')
      expect(candidatePlanPresent(service), 'a failed policy reload must leave the old candidate plan invalidated').toBe(false)

      await vi.waitFor(() => expect(getConnectionHealthAdminGroupsMock).toHaveBeenCalledTimes(3))
      authoritativeRead.resolve([candidateGroup(false, 'safety_lock')])
      await vi.waitFor(() => expect(service.adminGroups.value[0]?.accounts[0]?.priorityCandidate?.state).toBe('safety_lock'))
    } finally {
      staleRead.resolve([candidateGroup(true)])
      authoritativeRead.resolve([candidateGroup(false, 'safety_lock')])
      policyReload.reject(new Error('admin.connectionHealth.errors.network'))
      await Promise.allSettled([oldRead, mutation].filter((request): request is Promise<unknown> => Boolean(request)))
      cancelRefreshSubscription(service)
      setRefreshWorkspace(service, '')
    }
  })

  it('keeps the successful candidate reload when the page requests the same authoritative read after a policy save', async () => {
    const authoritativeRead = deferred<ReturnType<typeof candidateGroup>[]>()
    getConnectionHealthAdminGroupsMock
      .mockResolvedValueOnce([candidateGroup(true)])
      .mockReturnValueOnce(authoritativeRead.promise)
      .mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network'))
    createConnectionHealthPolicyMock.mockResolvedValueOnce(policyResult(policyInput(true)))
    listConnectionHealthPoliciesMock.mockResolvedValueOnce([])

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-candidate')
    try {
      await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
      await expect(service.savePolicy(policyInput(true))).resolves.toBe(true)
      await vi.waitFor(() => expect(getConnectionHealthAdminGroupsMock).toHaveBeenCalledTimes(2))

      const pageRead = service.loadAdminGroups({ silent: true })
      authoritativeRead.resolve([candidateGroup(false, 'safety_lock')])

      await expect(pageRead).resolves.toBe(true)
      expect(getConnectionHealthAdminGroupsMock).toHaveBeenCalledTimes(2)
      expect(service.adminGroups.value[0]?.priorityCandidateSummary?.candidatePriorityReady).toBe(false)
      expect(service.adminGroups.value[0]?.accounts[0]?.priorityCandidate?.state).toBe('safety_lock')
    } finally {
      authoritativeRead.resolve([candidateGroup(false, 'safety_lock')])
      cancelRefreshSubscription(service)
      setRefreshWorkspace(service, '')
    }
  })

  it('settles an old admin-group request when manual refresh supersedes it', async () => {
    const first = deferred<[]>()
    const manual = deferred<{ groups: []; refresh: { state: 'success'; sites: [] } }>()
    getConnectionHealthAdminGroupsMock.mockReturnValueOnce(first.promise)
    refreshConnectionHealthAdminGroupsMock.mockReturnValueOnce(manual.promise)

    const service = useConnectionHealth()
    const oldRequest = service.loadAdminGroups({ silent: true })
    const manualRequest = service.refreshAdminGroups()
    first.resolve([])

    const oldRequestResult = await Promise.race([
      oldRequest.then(value => ({ kind: 'settled' as const, value }), error => ({ kind: 'rejected' as const, error })),
      new Promise<{ kind: 'timeout' }>(resolve => setTimeout(() => resolve({ kind: 'timeout' }), 50)),
    ])
    expect(oldRequestResult).toEqual({ kind: 'settled', value: false })

    manual.resolve({ groups: [], refresh: { state: 'success', sites: [] } })
    await expect(manualRequest).resolves.toBe(true)
  })

  it('keeps existing events when an auxiliary events request fails without recording a page error', async () => {
    const event = {
      id: 'event-1',
      connectionId: 'target-1',
      modelName: 'gpt-4o',
      ownGroupName: 'vip',
      upstreamSiteId: 'site-1',
      upstreamGroupName: 'default',
      result: 'failed',
      fromState: 'healthy',
      toState: 'degraded',
      latencyMs: null,
      errorKey: 'upstream_timeout',
      remoteAction: 'none',
      createdAt: '2026-08-17T00:00:00Z',
    }
    getConnectionHealthEventsMock
      .mockResolvedValueOnce([event])
      .mockRejectedValueOnce(new Error('admin.connectionHealth.errors.request'))

    const service = useConnectionHealth()
    await expect(service.loadEvents()).resolves.toBe(true)
    expect(service.events.value).toEqual([event])

    await expect(service.loadEvents(undefined, { recordError: false })).resolves.toBe(false)

    expect(service.events.value).toEqual([event])
    expect(service.errorKey.value).toBe('')
  })

  it('records a failed refresh request error key in the terminal summary without clearing groups', async () => {
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([{ id: 'group-1', accounts: [] }])
    refreshConnectionHealthAdminGroupsMock.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network'))

    const service = useConnectionHealth()
    await expect(service.loadAdminGroups()).resolves.toBe(true)
    expect(service.adminGroups.value).toEqual([{ id: 'group-1', accounts: [] }])

    await expect(service.refreshAdminGroups()).resolves.toBe(false)

    expect(service.adminGroups.value).toEqual([{ id: 'group-1', accounts: [] }])
    expect(service.terminalRefreshSummary.value).toEqual({
      state: 'failure',
      errorKey: 'admin.connectionHealth.errors.network',
      sites: [],
    })
  })

  it('records an automatic failed refresh request error key in the terminal summary', async () => {
    refreshConnectionHealthAdminGroupsAutomaticallyMock.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.request'))

    const service = useConnectionHealth()
    await expect(service.refreshAdminGroupsAutomatically()).resolves.toBe(false)

    expect(service.terminalRefreshSummary.value).toEqual({
      state: 'failure',
      errorKey: 'admin.connectionHealth.errors.request',
      sites: [],
    })
  })

  it('rejects stale revisions and applies one terminal only once', async () => {
    const acceptedTerminal = {
      status: 'success',
      runId: 'run-revision-1',
      revision: 4,
      groups: [{ id: 'group-final', accounts: [] }],
      refresh: { state: 'success', sites: [] },
    }
    refreshConnectionHealthAdminGroupsMock.mockImplementationOnce(async (options: {
      onSnapshot: (snapshot: Record<string, unknown>) => void
      onTerminal: (terminal: Record<string, unknown>) => void
    }) => {
      options.onSnapshot({ runId: 'run-revision-1', revision: 3, runState: 'running', stage: 'main_groups' })
      options.onSnapshot({ runId: 'run-revision-1', revision: 2, runState: 'running', stage: 'site_sync' })
      options.onTerminal(acceptedTerminal)
      options.onTerminal({
        ...acceptedTerminal,
        revision: 5,
        groups: [{ id: 'group-duplicate', accounts: [] }],
      })
      return acceptedTerminal
    })

    const service = useConnectionHealth() as ReturnType<typeof useConnectionHealth> & {
      refreshRunSnapshot: { value: { runId: string; revision: number; stage: string } | null }
    }
    await expect(service.refreshAdminGroups()).resolves.toBe(true)

    expect(service.refreshRunSnapshot.value).toMatchObject({ runId: 'run-revision-1', revision: 3, stage: 'main_groups' })
    expect(service.adminGroups.value).toEqual([{ id: 'group-final', accounts: [] }])
    expect(service.terminalRefreshSummary.value).toEqual({ state: 'success', sites: [] })
  })

  it('cancels the active subscription before cleanup and rejects every late callback without leaking request state', async () => {
    const baselineTerminal = {
      status: 'success' as const,
      runId: 'run-cleanup-baseline',
      revision: 1,
      groups: [group('group-before-cleanup')],
      refresh: { state: 'success' as const, sites: [] },
    }
    const pending = deferred<typeof baselineTerminal>()
    let activeOptions: any
    refreshConnectionHealthAdminGroupsAutomaticallyMock
      .mockResolvedValueOnce(baselineTerminal)
      .mockImplementationOnce((options: any) => {
        activeOptions = options
        options.signal?.addEventListener('abort', () => {
          pending.reject(new DOMException('Aborted', 'AbortError'))
        }, { once: true })
        return pending.promise
      })

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-cleanup')
    await expect(service.refreshAdminGroupsAutomatically()).resolves.toBe(true)
    const previousGroups = [...service.adminGroups.value]
    const previousSummary = service.terminalRefreshSummary.value
    const request = service.refreshAdminGroupsAutomatically()
    await Promise.resolve()

    cancelRefreshSubscription(service)
    cancelRefreshSubscription(service)
    activeOptions?.onSnapshot?.({
      runId: 'run-cleanup-late', revision: 2, runState: 'running', stage: 'main_groups',
    })
    activeOptions?.onTerminal?.({
      status: 'success', runId: 'run-cleanup-late', revision: 3,
      groups: [group('group-from-late-terminal')], refresh: { state: 'partial', sites: [] },
    })
    const settled = await Promise.race([
      request.then(value => ({ state: 'settled' as const, value })),
      new Promise<{ state: 'timeout' }>(resolve => setTimeout(() => resolve({ state: 'timeout' }), 50)),
    ])
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([group('group-after-cleanup')])
    const reloadResult = await service.loadAdminGroups({ silent: true })
    pending.resolve(baselineTerminal)
    await request

    expect(activeOptions?.signal).toBeInstanceOf(AbortSignal)
    expect(activeOptions?.signal.aborted).toBe(true)
    expect(settled).toEqual({ state: 'settled', value: false })
    expect(service.adminGroups.value).toEqual([group('group-after-cleanup')])
    expect(service.adminGroups.value).not.toContainEqual(group('group-from-late-terminal'))
    expect(previousGroups).toEqual([group('group-before-cleanup')])
    expect(previousSummary).toEqual({ state: 'success', sites: [] })
    expect(service.terminalRefreshSummary.value).toBe(previousSummary)
    expect(service.errorKey.value).toBe('')
    expect(reloadResult).toBe(true)

    const followupPending = deferred<typeof baselineTerminal>()
    let followupOptions: any
    refreshConnectionHealthAdminGroupsAutomaticallyMock.mockImplementationOnce((options: any) => {
      followupOptions = options
      options.signal?.addEventListener('abort', () => {
        followupPending.reject(new DOMException('Aborted', 'AbortError'))
      }, { once: true })
      return followupPending.promise
    })
    const followupRequest = service.refreshAdminGroupsAutomatically()
    await Promise.resolve()
    const loadWhileFollowupActive = await service.loadAdminGroups({ silent: true })
    cancelRefreshSubscription(service)
    cancelRefreshSubscription(service)
    const followupSettled = await Promise.race([
      followupRequest.then(value => ({ state: 'settled' as const, value })),
      new Promise<{ state: 'timeout' }>(resolve => setTimeout(() => resolve({ state: 'timeout' }), 50)),
    ])
    followupPending.resolve(baselineTerminal)
    await followupRequest
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([group('group-after-followup-cleanup')])
    const reloadAfterFollowup = await service.loadAdminGroups({ silent: true })

    expect(followupOptions?.signal?.aborted).toBe(true)
    expect(loadWhileFollowupActive).toBe(false)
    expect(followupSettled).toEqual({ state: 'settled', value: false })
    expect(reloadAfterFollowup).toBe(true)
    expect(service.adminGroups.value).toEqual([group('group-after-followup-cleanup')])
  })

  it('isolates workspace B from workspace A terminal groups after leaving an active refresh', async () => {
    const pendingA = deferred<any>()
    let optionsA: any
    refreshConnectionHealthAdminGroupsAutomaticallyMock.mockImplementationOnce((options: any) => {
      optionsA = options
      options.signal?.addEventListener('abort', () => {
        pendingA.reject(new DOMException('Aborted', 'AbortError'))
      }, { once: true })
      return pendingA.promise
    })
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([group('workspace-a-group')])

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-a')
    await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
    const requestA = service.refreshAdminGroupsAutomatically()
    await Promise.resolve()

    cancelRefreshSubscription(service)
    setRefreshWorkspace(service, 'workspace-b')
    optionsA?.onSnapshot?.({ runId: 'run-workspace-a', revision: 5, runState: 'running', stage: 'main_groups' })
    optionsA?.onTerminal?.({
      status: 'success', runId: 'run-workspace-a', revision: 6,
      groups: [group('workspace-a-late-terminal')], refresh: { state: 'success', sites: [] },
    })
    pendingA.resolve({
      status: 'success', runId: 'run-workspace-a', revision: 6,
      groups: [group('workspace-a-late-terminal')], refresh: { state: 'success', sites: [] },
    })
    await requestA

    expect(optionsA?.signal?.aborted).toBe(true)
    expect(service.adminGroups.value).toEqual([])
    expect(service.refreshRunSnapshot.value).toBeNull()
    expect(service.terminalRefreshSummary.value).toBeNull()
    expect(service.errorKey.value).toBe('')

    const terminalB = {
      status: 'success' as const,
      runId: 'run-workspace-b',
      revision: 2,
      groups: [group('workspace-b-group')],
      refresh: { state: 'success' as const, sites: [] },
    }
    refreshConnectionHealthAdminGroupsAutomaticallyMock.mockImplementationOnce(async (options: any) => {
      options.onSnapshot({ runId: 'run-workspace-b', revision: 1, runState: 'running', stage: 'main_groups' })
      options.onTerminal(terminalB)
      return terminalB
    })

    await expect(service.refreshAdminGroupsAutomatically()).resolves.toBe(true)
    expect(service.adminGroups.value).toEqual([group('workspace-b-group')])
    expect(service.adminGroups.value).not.toContainEqual(group('workspace-a-late-terminal'))
  })

  it('re-enters the same workspace by subscribing to the original run without starting backend work again', async () => {
    const pendingFirstSubscription = deferred<any>()
    let firstOptions: any
    let backendRunStarts = 0
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([group('workspace-same-old-list')])
    refreshConnectionHealthAdminGroupsAutomaticallyMock.mockImplementationOnce((options: any) => {
      firstOptions = options
      backendRunStarts++
      options.onSnapshot({ runId: 'run-same-workspace', revision: 1, runState: 'running', stage: 'site_sync' })
      options.signal?.addEventListener('abort', () => {
        pendingFirstSubscription.reject(new DOMException('Aborted', 'AbortError'))
      }, { once: true })
      return pendingFirstSubscription.promise
    })

    const service = useConnectionHealth() as RefreshLifecycleService
    setRefreshWorkspace(service, 'workspace-same')
    await service.loadAdminGroups({ silent: true })
    const firstSubscription = service.refreshAdminGroupsAutomatically()
    await Promise.resolve()
    cancelRefreshSubscription(service)
    const firstSettled = await Promise.race([
      firstSubscription.then(value => ({ state: 'settled' as const, value })),
      new Promise<{ state: 'timeout' }>(resolve => setTimeout(() => resolve({ state: 'timeout' }), 50)),
    ])
    pendingFirstSubscription.resolve({
      status: 'success', runId: 'run-same-workspace', revision: 2,
      groups: [group('workspace-same-old-list')], refresh: { state: 'success', sites: [] },
    })
    await firstSubscription

    setRefreshWorkspace(service, 'workspace-same')
    const terminal = {
      status: 'success' as const,
      runId: 'run-same-workspace',
      revision: 3,
      groups: [group('workspace-same-terminal')],
      refresh: { state: 'success' as const, sites: [] },
    }
    refreshConnectionHealthAdminGroupsAutomaticallyMock.mockImplementationOnce(async (options: any) => {
      options.onSnapshot({ runId: 'run-same-workspace', revision: 2, runState: 'running', stage: 'main_groups' })
      options.onTerminal(terminal)
      return terminal
    })
    await expect(service.refreshAdminGroupsAutomatically()).resolves.toBe(true)

    expect(firstOptions?.signal?.aborted).toBe(true)
    expect(firstSettled).toEqual({ state: 'settled', value: false })
    expect(backendRunStarts).toBe(1)
    expect(service.refreshRunSnapshot.value).toMatchObject({ runId: 'run-same-workspace', revision: 2 })
    expect(service.adminGroups.value).toEqual([group('workspace-same-terminal')])
    expect(service.errorKey.value).toBe('')
  })
})


describe('test configuration save snapshot ordering', () => {
  const configuredGroup = (protocol: string, timeout: number) => ({ ...group('g1'), accounts: [{
    id: 'a', targetId: 'sub2api:protocol-race:a', modelHealth: [],
    testConfiguration: { status: 'inherited', protocol, probeTimeoutSeconds: timeout, sourceGroups: [] },
  }] })
  const saved = () => ({ adminGroupId: 'g1', adminGroupName: 'Group', configuration: { protocol: 'responses', probeTimeoutSeconds: 30 }, inventoryComplete: true, affectedAccountCount: 1, conflictAccountCount: 0,
    accounts: [{ targetId: 'sub2api:protocol-race:a', accountName: 'A', testConfiguration: configuredGroup('responses', 30).accounts[0].testConfiguration }],
  })

  const healthyGroup = () => ({
    ...candidateGroup(true), id: 'g1',
    healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 1, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
    accounts: [{ ...candidateGroup(true).accounts[0], ...configuredGroup('chat_completions', 10).accounts[0],
      modelHealth: [{ modelName: 'model-a', configured: true, state: 'healthy', lastSuccessLatencyMs: 123, currentHealthResult: { status: 'success', protocol: 'chat_completions' } }],
    }],
  })

  it('excludes unverified old states when an SSE terminal supplies the new overview', async () => {
    const service = useConnectionHealth()
    service.setAdminGroupsWorkspace('protocol-race')
    const next = healthyGroup()
    next.accounts[0].modelHealth[0].currentHealthResult = { status: 'unverified', protocol: 'chat_completions' }
    const terminal = { status: 'success' as const, runId: 'unverified-stream', revision: 1, groups: [next], refresh: { state: 'success' as const, sites: [] } }
    refreshConnectionHealthAdminGroupsMock.mockImplementationOnce(async (options: any) => { options.onTerminal(terminal); return terminal })
    try {
      await expect(service.refreshAdminGroups()).resolves.toBe(true)
      expect(service.overview.value).toMatchObject({ totalConnections: 1, healthy: 0, unconfigured: 1 })
    } finally { service.setAdminGroupsWorkspace('') }
  })

  it.each(['explicit', 'legacy'] as const)('preserves applicable Chat evidence for a timeout-only save from %s configuration even if reload fails', async configurationKind => {
    const service = useConnectionHealth()
    service.setAdminGroupsWorkspace('protocol-race')
    const previous = healthyGroup()
    if (configurationKind === 'legacy') delete (previous.accounts[0] as any).testConfiguration
    const next = saved()
    next.configuration.protocol = 'chat_completions'
    next.accounts[0].testConfiguration = configuredGroup('chat_completions', 30).accounts[0].testConfiguration
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([previous]).mockRejectedValue(new Error('reload unavailable'))
    setAdminGroupTestConfigurationMock.mockResolvedValue(next)
    try {
      await service.loadAdminGroups({ silent: true })
      await service.saveAdminGroupTestConfiguration('g1', { protocol: 'chat_completions', probeTimeoutSeconds: 30 })
      const account = service.adminGroups.value[0].accounts[0]
      expect(account.testConfiguration).toMatchObject({ protocol: 'chat_completions', probeTimeoutSeconds: 30 })
      expect(account.modelHealth[0].currentHealthResult?.status).toBe('success')
      expect(account.modelHealth[0].lastSuccessLatencyMs).toBe(123)
      expect(service.adminGroups.value[0].healthSummary).toMatchObject({ healthyModels: 1, unconfiguredModels: 0 })
      expect(service.overview.value).toMatchObject({ healthy: 1, unconfigured: 0 })
      expect(candidatePlanPresent(service)).toBe(false)
    } finally { service.setAdminGroupsWorkspace('') }
  })

  it('excludes unverified old-protocol state from overview after a successful reload', async () => {
    const service = useConnectionHealth()
    service.setAdminGroupsWorkspace('protocol-race')
    const next = healthyGroup()
    next.accounts[0].modelHealth[0].currentHealthResult = { status: 'unverified', protocol: 'chat_completions' }
    getConnectionHealthAdminGroupsMock.mockResolvedValue([next])
    try {
      await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
      expect(service.overview.value).toMatchObject({ totalConnections: 1, healthy: 0, unconfigured: 1 })
    } finally { service.setAdminGroupsWorkspace('') }
  })

  it('invalidates summary and candidate evidence immediately when save succeeds but reload fails', async () => {
    const service = useConnectionHealth()
    service.setAdminGroupsWorkspace('protocol-race')
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([healthyGroup()]).mockRejectedValue(new Error('reload unavailable'))
    setAdminGroupTestConfigurationMock.mockResolvedValue(saved())
    try {
      await expect(service.loadAdminGroups({ silent: true })).resolves.toBe(true)
      expect(service.overview.value?.healthy).toBe(1)
      expect(candidatePlanPresent(service)).toBe(true)
      await expect(service.saveAdminGroupTestConfiguration('g1', { protocol: 'responses', probeTimeoutSeconds: 30 })).resolves.toHaveProperty('configuration')
      expect(service.adminGroups.value[0].accounts[0].modelHealth[0].currentHealthResult?.status).toBe('unverified')
      expect(service.adminGroups.value[0].healthSummary).toMatchObject({ healthyModels: 0, unconfiguredModels: 1 })
      expect(service.overview.value).toMatchObject({ healthy: 0, unconfigured: 1 })
      expect(candidatePlanPresent(service)).toBe(false)
    } finally { service.setAdminGroupsWorkspace('') }
  })

  it('keeps saved configuration when an ordinary read started before save arrives late', async () => {
    const service = useConnectionHealth()
    service.setAdminGroupsWorkspace('protocol-race')
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([configuredGroup('chat_completions', 10)])
    await service.loadAdminGroups()
    const stale = deferred<unknown[]>()
    getConnectionHealthAdminGroupsMock.mockReturnValueOnce(stale.promise).mockResolvedValue([configuredGroup('responses', 30)])
    const oldRead = service.loadAdminGroups({ silent: true })
    setAdminGroupTestConfigurationMock.mockResolvedValue(saved())
    await service.saveAdminGroupTestConfiguration('g1', { protocol: 'responses', probeTimeoutSeconds: 30 })
    expect(service.adminGroups.value[0].accounts[0].testConfiguration?.protocol).toBe('responses')
    stale.resolve([configuredGroup('chat_completions', 10)])
    await oldRead
    await Promise.resolve()
    expect(service.adminGroups.value[0].accounts[0].testConfiguration?.protocol).toBe('responses')
    expect(service.adminGroups.value[0].accounts[0].testConfiguration?.probeTimeoutSeconds).toBe(30)
  })

  it('discards old SSE terminal data after a successful protocol save', async () => {
    const service = useConnectionHealth()
    service.setAdminGroupsWorkspace('protocol-race')
    getConnectionHealthAdminGroupsMock.mockResolvedValueOnce([configuredGroup('chat_completions', 10)])
    await service.loadAdminGroups()
    const terminal = deferred<any>()
    let options: any
    refreshConnectionHealthAdminGroupsMock.mockImplementationOnce((value: any) => { options = value; return terminal.promise })
    const refresh = service.refreshAdminGroups()
    setAdminGroupTestConfigurationMock.mockResolvedValue(saved())
    getConnectionHealthAdminGroupsMock.mockResolvedValue([configuredGroup('responses', 30)])
    await service.saveAdminGroupTestConfiguration('g1', { protocol: 'responses', probeTimeoutSeconds: 30 })
    const oldTerminal = { status: 'success', runId: 'old-run', revision: 1, groups: [configuredGroup('chat_completions', 10)], refresh: { state: 'success', sites: [] } }
    options.onTerminal(oldTerminal)
    expect(service.adminGroups.value[0].accounts[0].testConfiguration?.protocol).toBe('responses')
    terminal.resolve(oldTerminal)
    await refresh
    await Promise.resolve()
    expect(service.adminGroups.value[0].accounts[0].testConfiguration?.protocol).toBe('responses')
  })
})
