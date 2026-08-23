import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  refreshConnectionHealthAdminGroups,
  refreshConnectionHealthAdminGroupsAutomatically,
} from '@/modules/admin/api/connectionHealth'

type RefreshSnapshot = {
  runId: string
  revision: number
  runState: 'running' | 'complete'
  stage: string
}

type RefreshTerminal = {
  status: 'success' | 'failed'
  runId?: string
  revision?: number
  groups?: unknown[]
  refresh: { state: string; sites: unknown[] }
}

type RefreshStreamOptions = {
  onSnapshot?: (snapshot: RefreshSnapshot) => void
  onTerminal?: (terminal: RefreshTerminal) => void
}

type RefreshStreamResult = RefreshTerminal & {
  conflict?: { errorKey: string; runId: string }
}

const manualRefresh = refreshConnectionHealthAdminGroups as unknown as (
  options?: RefreshStreamOptions,
) => Promise<RefreshStreamResult>

const automaticRefresh = refreshConnectionHealthAdminGroupsAutomatically as unknown as (
  options?: RefreshStreamOptions,
) => Promise<RefreshStreamResult>

const sseResponse = (...events: Array<{ event: string; data: unknown }>): Response => {
  const body = events.map(({ event, data }) => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`).join('')
  return new Response(body, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
  })
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('connection health refresh stream transport', () => {
  it('uses authenticated fetch with SSE Accept while retaining old JSON terminal fallback', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      groups: [],
      refresh: { state: 'success', sites: [] },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    const result = await manualRefresh()

    expect(result).toMatchObject({ groups: [], refresh: { state: 'success', sites: [] } })
    const [, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(options.method).toBe('POST')
    expect(new Headers(options.headers).get('Accept')).toBe('text/event-stream')
    expect(new Headers(options.headers).get('Authorization')).toBe('Bearer frontend-test-token')
  })

  it('streams snapshots and returns the legal terminal without EventSource', async () => {
    const snapshot = {
      runId: 'run-stream-1', revision: 3, runState: 'running' as const, stage: 'multiplier_refresh',
      stageCompletedSites: 1, stageTotalSites: 2,
      waiting: [{ siteId: 'site-2', siteName: '站点二', phase: 'multiplier_refresh', elapsedSeconds: 12 }],
      issues: [{ siteId: 'site-1', siteName: '站点一', phase: 'site_sync', status: 'failed', errorKey: 'site_sync_network' }],
    }
    const terminal = {
      status: 'success' as const,
      runId: 'run-stream-1', revision: 4,
      groups: [], refresh: { state: 'partial', sites: [{ siteId: 'site-1', status: 'unavailable' }] },
    }
    const fetchMock = vi.fn().mockResolvedValue(sseResponse(
      { event: 'snapshot', data: snapshot },
      { event: 'terminal', data: terminal },
    ))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })
    vi.stubGlobal('EventSource', class ForbiddenEventSource {
      constructor() { throw new Error('native EventSource must not be used') }
    })
    const snapshots: RefreshSnapshot[] = []
    const terminals: RefreshTerminal[] = []

    const result = await manualRefresh({
      onSnapshot: value => snapshots.push(value),
      onTerminal: value => terminals.push(value),
    })

    expect(snapshots).toEqual([expect.objectContaining({ runId: 'run-stream-1', revision: 3, stage: 'multiplier_refresh' })])
    expect(terminals).toEqual([expect.objectContaining({ status: 'success', revision: 4 })])
    expect(result).toMatchObject({ status: 'success', groups: [], refresh: { state: 'partial' } })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('reconnects an interrupted stream with run_id and permanently stops after terminal', async () => {
    vi.useFakeTimers()
    const first = sseResponse({
      event: 'snapshot',
      data: { runId: 'run-reconnect-1', revision: 1, runState: 'running', stage: 'site_sync' },
    })
    const terminal = {
      status: 'success', runId: 'run-reconnect-1', revision: 2,
      groups: [], refresh: { state: 'success', sites: [] },
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(first)
      .mockResolvedValueOnce(sseResponse({ event: 'terminal', data: terminal }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    const refreshPromise = automaticRefresh().then(
      value => ({ value }),
      error => ({ error }),
    )
    await vi.runAllTimersAsync()
    const outcome = await refreshPromise

    expect(outcome).not.toHaveProperty('error')
    expect('value' in outcome ? outcome.value : null).toMatchObject({ status: 'success', revision: 2 })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('run_id=run-reconnect-1')
    await vi.runAllTimersAsync()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('reconnects the same run when EOF cuts a terminal event before its SSE boundary', async () => {
    vi.useFakeTimers()
    const interrupted = new Response([
      'event: snapshot',
      'data: {"runId":"run-half-terminal","revision":1,"runState":"running","stage":"site_sync"}',
      '',
      'event: terminal',
      'data: {"status":"success","runId":"run-half-terminal"',
    ].join('\n'), {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
    const terminal = {
      status: 'success', runId: 'run-half-terminal', revision: 2,
      groups: [{ id: 'group-from-real-terminal', accounts: [] }],
      refresh: { state: 'success', sites: [] },
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(interrupted)
      .mockResolvedValueOnce(sseResponse({ event: 'terminal', data: terminal }))
    const onTerminal = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    const outcomePromise = automaticRefresh({ onTerminal }).then(
      value => ({ value }),
      error => ({ error }),
    )
    await vi.runAllTimersAsync()
    const outcome = await outcomePromise

    expect(outcome).not.toHaveProperty('error')
    expect('value' in outcome ? outcome.value : null).toMatchObject({
      status: 'success',
      runId: 'run-half-terminal',
      groups: [{ id: 'group-from-real-terminal', accounts: [] }],
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('run_id=run-half-terminal')
    expect(onTerminal).toHaveBeenCalledTimes(1)
    expect(onTerminal).toHaveBeenCalledWith(expect.objectContaining({ revision: 2 }))
  })

  it('rejects a complete malformed SSE event without treating it as reconnectable EOF', async () => {
    const malformed = new Response([
      'event: snapshot',
      'data: {"runId":"run-malformed-terminal","revision":1,"runState":"running","stage":"site_sync"}',
      '',
      'event: terminal',
      'data: {not-json}',
      '',
      '',
    ].join('\n'), {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(malformed)
      .mockResolvedValueOnce(sseResponse({
        event: 'terminal',
        data: {
          status: 'success', runId: 'run-malformed-terminal', revision: 2,
          groups: [], refresh: { state: 'success', sites: [] },
        },
      }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    await expect(automaticRefresh()).rejects.toThrow('admin.connectionHealth.errors.request')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('handles manual-against-automatic 409 by joining the reported run without retrying POST', async () => {
    const conflict = new Response(JSON.stringify({
      errorKey: 'refresh_run_conflict',
      runId: 'run-auto-1',
    }), { status: 409, headers: { 'Content-Type': 'application/json' } })
    const terminal = {
      status: 'success', runId: 'run-auto-1', revision: 8,
      groups: [], refresh: { state: 'success', sites: [] },
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(conflict)
      .mockResolvedValueOnce(sseResponse({ event: 'terminal', data: terminal }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    const result = await manualRefresh()

    expect(result).toMatchObject({
      status: 'success',
      conflict: { errorKey: 'refresh_run_conflict', runId: 'run-auto-1' },
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const firstOptions = fetchMock.mock.calls[0]?.[1] as RequestInit
    const secondOptions = fetchMock.mock.calls[1]?.[1] as RequestInit
    expect(firstOptions.method).toBe('POST')
    expect(secondOptions.method).not.toBe('POST')
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('run_id=run-auto-1')
  })

  it.each([401, 403, 404])('does not loop on terminal reconnect status %s', async (status) => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      errorKey: status === 404 ? 'refresh_run_not_found' : 'auth.errors.unauthorized',
    }), { status, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    await expect(automaticRefresh()).rejects.toThrow()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
