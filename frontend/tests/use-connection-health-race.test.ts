import { afterEach, describe, expect, it, vi } from 'vitest'

const getConnectionHealthAdminGroupsMock = vi.hoisted(() => vi.fn())
const refreshConnectionHealthAdminGroupsMock = vi.hoisted(() => vi.fn())
const refreshConnectionHealthAdminGroupsAutomaticallyMock = vi.hoisted(() => vi.fn())
const getConnectionHealthEventsMock = vi.hoisted(() => vi.fn())

vi.mock('../src/modules/admin/api/connectionHealth', async () => {
  const actual = await vi.importActual<typeof import('../src/modules/admin/api/connectionHealth')>('../src/modules/admin/api/connectionHealth')
  return {
    ...actual,
    getConnectionHealthAdminGroups: getConnectionHealthAdminGroupsMock,
    refreshConnectionHealthAdminGroups: refreshConnectionHealthAdminGroupsMock,
    refreshConnectionHealthAdminGroupsAutomatically: refreshConnectionHealthAdminGroupsAutomaticallyMock,
    getConnectionHealthEvents: getConnectionHealthEventsMock,
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
  }
})

import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'

afterEach(() => vi.clearAllMocks())

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

describe('useConnectionHealth request generations', () => {
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
