import { beforeEach, describe, expect, it, vi } from 'vitest'

const connectionHealthApi = vi.hoisted(() => ({
  createConnectionHealthPolicy: vi.fn(),
  deleteConnectionHealthPolicy: vi.fn(),
  disableConnection: vi.fn(),
  discoverTargetModels: vi.fn(),
  emergencyClearConnectionHealthSafety: vi.fn(),
  getAdminGroupPolicyConfiguration: vi.fn(),
  getConnectionHealthAdminGroups: vi.fn(),
  getConnectionHealthEvents: vi.fn(),
  getConnectionHealthGroups: vi.fn(),
  getConnectionHealthOverview: vi.fn(),
  getConnectionHealthPrioritySync: vi.fn(),
  getConnectionHealthSafety: vi.fn(),
  getTargetPolicyAssignments: vi.fn(),
  listConnectionHealthPolicies: vi.fn(),
  manualProbeOnce: vi.fn(),
  probeConnection: vi.fn(),
  probeTarget: vi.fn(),
  restoreConnection: vi.fn(),
  setAdminGroupPolicyConfiguration: vi.fn(),
  setTargetPolicyAssignments: vi.fn(),
  setTargetSchedulable: vi.fn(),
  updateConnectionHealthPolicy: vi.fn(),
  updateConnectionHealthSafety: vi.fn(),
}))

vi.mock('../src/modules/admin/api/connectionHealth', () => connectionHealthApi)

import { useConnectionHealth } from '../src/modules/admin/composables/useConnectionHealth'

const priorityState = (count: number) => ({
  lastDecision: 'pending',
  lastSuppressionReason: '',
  lastError: '',
  lastInventoryError: '',
  inventoryStatus: 'ready',
  lastActionSource: 'writeback',
  policyVersion: '',
  minWriteIntervalSeconds: 30,
  writebackSpreadSeconds: 5,
  maxPendingAgeSeconds: 300,
  reconcileIntervalSeconds: 30,
  inventorySnapshotTtlSeconds: 60,
  reconcileFailureBackoffSeconds: 30,
  driftAction: 'alert_only',
  readMode: 'inventory_snapshot',
  pendingAgeSeconds: 0,
  pendingTargetCount: 2,
  lastWriteRoundTargetCount: count,
  lastInventoryReadDurationMs: 0,
  lastWriteDurationMs: 0,
  evaluationCount: 0,
  probeEvaluationCount: 0,
  signatureChangeCount: 0,
  reconcileAttemptCount: 0,
  reconcileSuccessCount: 0,
  reconcileFailureCount: 0,
  snapshotHitCount: 0,
  snapshotMissCount: 0,
  writeAttemptCount: 0,
  writeSuccessCount: 0,
  writeFailureCount: 0,
  unchangedSkipCount: 0,
  windowSuppressionCount: 0,
  driftCount: 0,
  updatedAt: '',
})

type Deferred<T> = {
  promise: Promise<T>
  resolve: (value: T) => void
}

const deferred = <T>(): Deferred<T> => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((next) => { resolve = next })
  return { promise, resolve }
}

describe('connection health F priority sync state', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    const state = useConnectionHealth()
    state.overview.value = null
    state.prioritySync.value = null
    state.prioritySyncErrorKey.value = ''
    state.groups.value = []
    state.adminGroups.value = []
    state.errorKey.value = ''
    connectionHealthApi.getConnectionHealthGroups.mockResolvedValue([])
  })

  it.each(['priority-first', 'admin-groups-first'])('keeps priority sync when %s resolves first', async (order) => {
    const adminGroups = deferred<Array<{ id: string; accounts: [] }>>()
    const prioritySync = deferred<ReturnType<typeof priorityState> | null>()
    connectionHealthApi.getConnectionHealthAdminGroups.mockReturnValue(adminGroups.promise)
    connectionHealthApi.getConnectionHealthPrioritySync.mockReturnValue(prioritySync.promise)
    const state = useConnectionHealth()
    const loaded = state.loadAll()
    const expected = priorityState(7)

    if (order === 'priority-first') {
      prioritySync.resolve(expected)
      adminGroups.resolve([{ id: 'group-1', accounts: [] }])
    } else {
      adminGroups.resolve([{ id: 'group-1', accounts: [] }])
      prioritySync.resolve(expected)
    }
    await loaded

    expect(state.prioritySync.value).toEqual(expected)
    expect(state.adminGroups.value).toHaveLength(1)
    expect(state.overview.value?.prioritySync).toBeUndefined()
  })

  it('isolates priority-sync load failures from the account health main list', async () => {
    const state = useConnectionHealth()
    state.prioritySync.value = priorityState(7)
    connectionHealthApi.getConnectionHealthAdminGroups.mockResolvedValue([{ id: 'group-1', accounts: [] }])
    connectionHealthApi.getConnectionHealthPrioritySync.mockRejectedValue(new Error('priority-sync unavailable'))

    await state.loadAll()

    expect(state.adminGroups.value).toHaveLength(1)
    expect(state.prioritySync.value?.lastWriteRoundTargetCount).toBe(7)
    expect(state.prioritySyncErrorKey.value).toBe('priority-sync unavailable')
    expect(state.errorKey.value).toBe('')
  })
})
