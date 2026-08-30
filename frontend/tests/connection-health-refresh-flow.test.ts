import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { refreshConnectionHealthAdminGroupsAutomatically } from '@/modules/admin/api/connectionHealth'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'

const viewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
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

  it('stores the automatic terminal summary without adding a frontend timeout', async () => {
    const terminalSummary = {
      state: 'partial' as const,
      sites: [{ siteId: 'site-1', status: 'unavailable' as const }],
    }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      groups: [],
      refresh: terminalSummary,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null })

    const service = useConnectionHealth()
    await expect(service.refreshAdminGroupsAutomatically()).resolves.toBe(true)

    const automaticRefresh = viewSource.match(
      /const refreshAdminGroupsAutomatically = async[\s\S]*?\n}\n\nconst refresh = async/,
    )?.[0] ?? ''
    expect(service.terminalRefreshSummary.value).toEqual(terminalSummary)
    expect(automaticRefresh).not.toBe('')
    expect(automaticRefresh).not.toContain('AbortSignal.timeout')
    expect(automaticRefresh).not.toContain('setTimeout')
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

  it('preserves old groups and records the failed terminal reason even when its accumulated summary was success', async () => {
    const body = [
      'event: snapshot',
      'data: {"runId":"run-main-failure","revision":1,"runState":"running","stage":"main_groups"}',
      '',
      'event: terminal',
      'data: {"status":"failed","runId":"run-main-failure","revision":2,"errorKey":"main_groups_unavailable","failedStage":"main_groups","refresh":{"state":"success","sites":[]}}',
      '',
      '',
    ].join('\n')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })))
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })
    const service = useConnectionHealth()
    service.adminGroups.value = [{ id: 'retained-group', name: '旧分组', accounts: [] }] as any

    await expect(service.refreshAdminGroupsAutomatically()).resolves.toBe(false)

    expect(service.adminGroups.value).toEqual([{ id: 'retained-group', name: '旧分组', accounts: [] }])
    expect(service.terminalRefreshSummary.value).toEqual({
      state: 'failure',
      errorKey: 'main_groups_unavailable',
      sites: [],
    })
  })
})
