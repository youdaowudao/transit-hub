import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  refreshConnectionHealthAdminGroups,
  refreshConnectionHealthAdminGroupsAutomatically,
} from '@/modules/admin/api/connectionHealth'

const interruptedStream = (): Response => new Response(
  'event: snapshot\ndata: {"runId":"run-http-terminal","revision":1,"runState":"running","stage":"site_sync"}\n\n',
  { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
)

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('connection health refresh reconnect terminal responses', () => {
  it.each([401, 403, 404])('stops after reconnect returns HTTP %s', async (status) => {
    vi.useFakeTimers()
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(interruptedStream())
      .mockResolvedValueOnce(new Response(JSON.stringify({
        errorKey: status === 404 ? 'refresh_run_not_found' : 'auth.errors.unauthorized',
      }), { status, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', {
      getItem: () => 'frontend-test-token',
      removeItem: vi.fn(),
    })

    const outcomePromise = refreshConnectionHealthAdminGroupsAutomatically().then(
      value => ({ value }),
      error => ({ error }),
    )
    await vi.runAllTimersAsync()
    const outcome = await outcomePromise

    expect(outcome).toHaveProperty('error')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('run_id=run-http-terminal')
    await vi.runAllTimersAsync()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('accepts the first terminal once even when its observer throws and the chunk contains a duplicate', async () => {
    const firstTerminal = {
      status: 'success',
      runId: 'run-terminal-once',
      revision: 2,
      groups: [{ id: 'first-terminal', accounts: [] }],
      refresh: { state: 'success', sites: [] },
    }
    const duplicateTerminal = {
      ...firstTerminal,
      revision: 3,
      groups: [{ id: 'duplicate-terminal', accounts: [] }],
    }
    const body = [firstTerminal, duplicateTerminal]
      .map(terminal => `event: terminal\ndata: ${JSON.stringify(terminal)}\n\n`)
      .join('')
    const fetchMock = vi.fn().mockResolvedValue(new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    }))
    const onTerminal = vi.fn(() => { throw new Error('observer failed') })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    const result = await refreshConnectionHealthAdminGroups({ onTerminal })

    expect(result).toMatchObject({ revision: 2, groups: [{ id: 'first-terminal' }] })
    expect(onTerminal).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('reports interrupted and reconnected transport state around a bounded run_id reconnect', async () => {
    vi.useFakeTimers()
    const terminal = new Response(
      'event: terminal\ndata: {"status":"success","runId":"run-visible-reconnect","revision":2,"groups":[],"refresh":{"state":"success","sites":[]}}\n\n',
      { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
    )
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(
        'event: snapshot\ndata: {"runId":"run-visible-reconnect","revision":1,"runState":"running","stage":"site_sync"}\n\n',
        { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
      ))
      .mockResolvedValueOnce(terminal)
    const onConnectionState = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => 'frontend-test-token' })

    const outcome = refreshConnectionHealthAdminGroupsAutomatically({ onConnectionState } as any)
    await vi.runAllTimersAsync()
    await expect(outcome).resolves.toMatchObject({ status: 'success', runId: 'run-visible-reconnect' })

    expect(onConnectionState).toHaveBeenCalledWith('reconnecting')
    expect(onConnectionState).toHaveBeenLastCalledWith('connected')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})
