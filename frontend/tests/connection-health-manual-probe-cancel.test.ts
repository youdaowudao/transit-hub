import { readFileSync } from 'node:fs'

import { afterEach, describe, expect, it, vi } from 'vitest'
import { manualProbeOnce, probeTargetWithProgress } from '../src/modules/admin/api/connectionHealth'

const dialogSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue', import.meta.url),
  'utf8',
)

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('manual probe cancellation', () => {
  it('passes the caller abort signal to the manual probe request', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const fetchMock = vi.fn().mockResolvedValue(new Response('[]', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await expect(manualProbeOnce('sub2api:ws1:account-1', ['gpt-5.6-sol'], controller.signal)).resolves.toEqual([])

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/manual-probe',
      expect.objectContaining({ signal: controller.signal }),
    )
  })

  it('closes and aborts even while a test is running', () => {
    const closeBody = dialogSource.match(/const close = \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const cleanupBody = dialogSource.match(/const cleanupFrontendWork = \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    expect(closeBody).toContain('cleanupFrontendWork()')
    expect(cleanupBody).toContain('cancelActiveRequest()')
    expect(closeBody).toContain("emit('close')")
    expect(closeBody).not.toContain("phase.value === 'testing'")
    expect(dialogSource).toContain('controller.signal')
  })

  it('shows queued and running phases from the formal probe stream', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('data: {"type":"phase","phase":"queued"}\n\n'))
        controller.enqueue(new TextEncoder().encode('data: {"type":"phase","phase":"running"}\n\ndata: {"type":"result","results":[]}\n\n'))
        controller.close()
      },
    })
    const fetchMock = vi.fn().mockResolvedValue(new Response(body, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const phases: string[] = []

    await expect(probeTargetWithProgress('sub2api:ws1:account-1', ['gpt-5.6-sol'], phase => phases.push(phase))).resolves.toEqual([])

    expect(phases).toEqual(['queued', 'running'])
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/probe-stream',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'text/event-stream' }) }),
    )
    expect(dialogSource).toContain("formalProgress.value = 'queued'")
    expect(dialogSource).toContain("formalProgress.value === 'queued' ? 'running' : 'direct'")
    expect(dialogSource).toContain('progress.${formalProgress}')
  })
})
