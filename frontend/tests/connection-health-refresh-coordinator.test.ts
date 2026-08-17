import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { createRefreshCoordinator } from '@/modules/admin/utils/connectionHealthRefresh'
import { refreshConnectionHealthAdminGroups } from '@/modules/admin/api/connectionHealth'

afterEach(() => vi.unstubAllGlobals())

describe('connection health manual refresh coordinator', () => {
  it('pauses automatic refresh, rejects duplicate clicks, and ignores stale completions', () => {
    const coordinator = createRefreshCoordinator()
    const first = coordinator.begin()
    expect(first).toBe(1)
    expect(coordinator.isManualRefreshActive()).toBe(true)
    expect(coordinator.shouldRunAutomaticRefresh()).toBe(false)
    expect(coordinator.begin()).toBeNull()

    expect(coordinator.complete(999)).toBe(false)
    expect(coordinator.isManualRefreshActive()).toBe(true)
    expect(coordinator.complete(first!)).toBe(true)
    expect(coordinator.isManualRefreshActive()).toBe(false)
    expect(coordinator.shouldRunAutomaticRefresh()).toBe(true)
  })
})

describe('connection health fresh refresh API', () => {
  it('uses one explicit POST request for the方案 A refresh', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      groups: [],
      refresh: { state: 'success', sites: [] },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null })

    await expect(refreshConnectionHealthAdminGroups()).resolves.toMatchObject({ groups: [], refresh: { state: 'success' } })
    expect(fetchMock).toHaveBeenCalledWith('/api/connection-health/admin-groups/refresh', expect.objectContaining({ method: 'POST' }))
  })

  it('rejects a legacy array response instead of treating HTTP 200 as a completed refresh', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('[]', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null })

    await expect(refreshConnectionHealthAdminGroups()).rejects.toThrow('admin.connectionHealth.errors.request')
  })

  it('returns the terminal refresh summary separately from account-level groups', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      groups: [],
      refresh: { state: 'partial', sites: [{ siteId: 'site-1', status: 'auth_failed', errorKey: 'auth' }] },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null })

    const result = await refreshConnectionHealthAdminGroups()
    expect(result.refresh.state).toBe('partial')
    expect(result.refresh.sites[0]).toMatchObject({ siteId: 'site-1', status: 'auth_failed' })
  })

  it('rejects non-terminal refresh states and site statuses', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      groups: [],
      refresh: { state: 'running', sites: [{ siteId: 'site-1', status: 'running' }] },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null })

    await expect(refreshConnectionHealthAdminGroups()).rejects.toThrow('admin.connectionHealth.errors.request')
  })
})

it('keeps the manual refresh path separate from full loadAll and admin-groups polling', () => {
  const source = readFileSync(new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url), 'utf8')
  const refreshBody = source.match(/const refresh = async \(\) => \{[\s\S]*?\n\}/)?.[0] ?? ''
  expect(refreshBody).toContain('refreshAdminGroups()')
  expect(refreshBody).not.toContain('loadAll(')
  expect(source).toContain('!refreshCoordinator.shouldRunAutomaticRefresh()')
})

it('does not reload admin groups when the page is activated during manual refresh', () => {
  const source = readFileSync(new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url), 'utf8')
  const entryBody = source.match(/const refreshOnEntry = \(\) => \{[\s\S]*?\n\}/)?.[0] ?? ''
  expect(entryBody).toContain('!refreshCoordinator.isManualRefreshActive()')
})

it('guards interaction-triggered admin-group reads during manual refresh', () => {
  const source = readFileSync(new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url), 'utf8')
  expect(source).toContain('const loadMainListIfIdle = async')
  for (const functionName of ['onFormalProbeCompleted', 'onSetTargetSchedulable', 'handleSavePolicy', 'togglePolicyEnabled', 'handleDeletePolicy']) {
    const body = source.match(new RegExp(`(?:const|async function) ${functionName}[\\s\\S]*?\\n\\}`))?.[0] ?? ''
    expect(body, `${functionName} must use the manual-refresh guard`).toContain('loadMainListIfIdle')
  }
})

it('guards composable loadAll while the manual refresh request is active', () => {
  const source = readFileSync(new URL('../src/modules/admin/composables/useConnectionHealth.ts', import.meta.url), 'utf8')
  const loadAllBody = source.match(/const loadAll = async[\s\S]*?\n  \}/)?.[0] ?? ''
  expect(loadAllBody).toContain('manualRefreshRequests.value > 0')
})

it('renders per-site terminal results after the one-shot request', () => {
  const source = readFileSync(new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url), 'utf8')
  expect(source).toContain('terminalRefreshSummary')
  expect(source).toContain('failedRefreshSites')
  expect(source).toContain('successfulRefreshSites')
  expect(source).toContain('refreshStatus.site')
})
