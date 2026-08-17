import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { refreshConnectionHealthAdminGroupsAutomatically } from '@/modules/admin/api/connectionHealth'

const viewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)
const composableSource = readFileSync(
  new URL('../src/modules/admin/composables/useConnectionHealth.ts', import.meta.url),
  'utf8',
)

afterEach(() => vi.unstubAllGlobals())

describe('connection health terminal refresh flow', () => {
  it('uses GET for automatic terminal refresh with the same validated response contract', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      groups: [],
      refresh: { state: 'success', sites: [] },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null })

    await expect(refreshConnectionHealthAdminGroupsAutomatically()).resolves.toMatchObject({
      groups: [],
      refresh: { state: 'success' },
    })
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/connection-health/admin-groups/refresh',
      expect.not.objectContaining({ method: 'POST' }),
    )
  })

  it('uses automatic terminal refresh for page entry and interval callbacks', () => {
    const entry = viewSource.match(/const refreshOnEntry = \(\) => \{[\s\S]*?\n\}/)?.[0] ?? ''
    const interval = viewSource.match(/const autoRefresh = async \(\) => \{[\s\S]*?\n\}/)?.[0] ?? ''
    expect(entry).toContain('refreshAdminGroupsAutomatically()')
    expect(entry).not.toContain('loadAll()')
    expect(interval).toContain('refreshAdminGroupsAutomatically()')
    expect(interval).not.toContain('loadAll({ silent: true })')
  })

  it('keeps an existing list visible while a refresh is in progress', () => {
    expect(viewSource).toContain('isLoading && adminGroups.length === 0')
  })

  it('stores the automatic terminal summary without adding a frontend timeout', () => {
    expect(composableSource).toContain('terminalRefreshSummary.value = response.refresh')
    expect(viewSource).not.toContain('AbortSignal.timeout')
    expect(viewSource).not.toContain('setTimeout')
  })

  it('uses the latest automatic terminal summary for the visible status', () => {
    expect(viewSource).toContain('terminalRefreshSummary')
    expect(viewSource).toContain('terminalRefreshSummary.value?.state')
    expect(viewSource).toContain('terminalRefreshSummary.value?.sites')
  })

  it('renders only failed site results', () => {
    expect(viewSource).toContain('failedRefreshSites')
    expect(viewSource).toContain('failedRefreshSites.length > 0')
    expect(viewSource).not.toContain('successfulRefreshSites')
  })
})
