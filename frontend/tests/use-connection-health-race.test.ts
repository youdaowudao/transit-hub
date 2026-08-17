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
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

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
})
