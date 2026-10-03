// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import type { EffectiveTestConfiguration, ModelHealth } from '@/modules/admin/types/connectionHealth'
import { manualProbeOnce, probeTargetWithProgress } from '../src/modules/admin/api/connectionHealth'

const harness = vi.hoisted(() => ({
  discoverModels: vi.fn(),
  manualProbeTarget: vi.fn(),
  runManualProbeOnce: vi.fn(),
  serviceErrorKey: { value: '' },
}))

vi.mock('@/modules/admin/composables/useConnectionHealth', () => ({
  connectionHealthMessageKey: (key: string) => key,
  connectionHealthRecordColorClass: () => '',
  formatConnectionHealthTime: (value: string) => value,
  useConnectionHealth: () => ({
    discoverModels: harness.discoverModels,
    manualProbeTarget: harness.manualProbeTarget,
    runManualProbeOnce: harness.runManualProbeOnce,
    errorKey: harness.serviceErrorKey,
  }),
}))

const mountedWrappers: VueWrapper[] = []

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

const result = (modelName: string): ModelHealth => ({
  modelName,
  providerFamily: 'openai',
  configured: true,
  state: 'healthy',
  currentWeight: 100,
  consecutiveFailures: 0,
  consecutiveSuccesses: 1,
  lastProbeAt: '2026-08-30T10:00:00Z',
  lastSuccessAt: '2026-08-30T10:00:00Z',
  lastFailureAt: null,
  lastLatencyMs: 123,
  lastSuccessLatencyMs: 123,
  lastErrorKey: '',
  lastErrorDetail: '',
  lastRemoteAction: '',
  probeResult: 'ok',
  updatedAt: '2026-08-30T10:00:00Z',
})

const mountFormalDialog = async (testConfiguration?: EffectiveTestConfiguration) => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: {
      open: false,
      target: {
        targetId: 'sub2api:ws1:account-1',
        accountName: '取消测试账号',
        platform: 'sub2api',
        type: 'subscription',
        status: 'active',
        groupName: '正式探活分组',
        testConfiguration,
        formalModels: [{ id: 'gpt-5.6-sol', name: 'gpt-5.6-sol', providerFamily: 'openai' }],
      },
    },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  const formalMode = wrapper.findAll('button').find(candidate => candidate.text().trim() === '正式手动探活')
  if (!formalMode) throw new Error('missing formal-probe mode button')
  await formalMode.trigger('click')
  await flushPromises()
  return wrapper
}

const startFormalProbe = async (wrapper: VueWrapper) => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().trim() === '开始正式探活')
  if (!button) throw new Error('missing formal-probe start button')
  await button.trigger('click')
  await flushPromises()
}

beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({ models: [{ id: 'discovered-model', name: 'Discovered Model' }] })
  harness.manualProbeTarget.mockReset()
  harness.runManualProbeOnce.mockReset()
  harness.serviceErrorKey.value = ''
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('manual probe cancellation', () => {
  it.each([
    { disposition: 'invalid', result: 'invalid_response', detail: '本次正文不完整', phase: '' },
    { disposition: 'applied', result: 'network_fluctuation', detail: '本次读取正文超时', phase: 'reading_body' },
  ])('shows the actual formal $result metadata independently from the old health projection', async ({ disposition, result: probeResult, detail, phase }) => {
    harness.manualProbeTarget.mockResolvedValue([{
      ...result('gpt-5.6-sol'),
      requestProtocol: 'responses',
      requestTimeoutSeconds: 30,
      requestLatencyMs: 12500,
      requestAt: '2026-10-03T01:02:03Z',
      requestErrorKey: probeResult,
      requestErrorDetail: detail,
      requestPhase: phase,
      probeDisposition: disposition,
      probeResult,
      lastLatencyMs: 111,
      lastErrorDetail: '旧有效失败原因',
    }])
    const wrapper = await mountFormalDialog({
      protocol: 'responses', probeTimeoutSeconds: 30, status: 'inherited',
      sourceGroups: [{ adminGroupId: 'g-response', adminGroupName: '来源分组', protocol: 'responses', probeTimeoutSeconds: 30 }],
      blockedReason: '',
    })
    await startFormalProbe(wrapper)

    expect(wrapper.text()).toContain('来源分组')
    expect(wrapper.text()).toContain('Responses / 30s')
    expect(wrapper.text()).toContain('128')
    expect(wrapper.text()).toContain('12500ms')
    expect(wrapper.text()).toContain('2026-10-03T01:02:03Z')
    expect(wrapper.text()).toContain(detail)
    if (phase) expect(wrapper.text()).toContain('截止阶段：读取完整响应正文')
    expect(wrapper.text()).not.toContain('111ms')
    expect(wrapper.text()).not.toContain('旧有效失败原因')
    expect(wrapper.find('.lucide-circle-check').exists()).toBe(false)
  })

  it('shows the temporary response protocol, timeout and elapsed time returned by the API', async () => {
    harness.runManualProbeOnce.mockResolvedValue({ results: [{
      modelName: 'discovered-model', protocol: 'responses', probeTimeoutSeconds: 30,
      result: 'slow_response', healthy: true, latencyMs: 12500,
      errorKey: '', errorDetail: '', probedAt: '2026-10-03T02:03:04Z', configurationChanged: true,
    }] })
    const wrapper = await mountFormalDialog()
    const once = wrapper.findAll('button').find(button => button.text().trim() === '一次性测试')
    if (!once) throw new Error('missing temporary mode')
    await once.trigger('click')
    await flushPromises()
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始测试')
    if (!start) throw new Error('missing temporary start')
    await start.trigger('click')
    await flushPromises()

    expect(harness.runManualProbeOnce).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Responses / 30s')
    expect(wrapper.text()).toContain('12500ms')
    expect(wrapper.text()).toContain('2026-10-03T02:03:04Z')
    expect(wrapper.text()).toContain('配置已变化')
    expect(harness.manualProbeTarget).not.toHaveBeenCalled()
  })

  it('passes the caller abort signal to the manual probe request', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('[]', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await expect(manualProbeOnce('sub2api:ws1:account-1', ['gpt-5.6-sol'], controller.signal)).resolves.toEqual([])

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/manual-probe',
      expect.objectContaining({ signal: controller.signal }),
    )
  })

  it.each([
    ['关闭按钮', async (wrapper: VueWrapper) => {
      const button = wrapper.findAll('button').find(candidate => candidate.text().trim() === '关闭')
      if (!button) throw new Error('missing close button')
      await button.trigger('click')
    }],
    ['遮罩', async (wrapper: VueWrapper) => {
      const overlay = wrapper.find('.fixed.inset-0 > .absolute.inset-0')
      if (!overlay.exists()) throw new Error('missing dialog overlay')
      await overlay.trigger('click')
    }],
  ])('aborts a pending formal probe through the %s, emits close, and ignores a late result', async (_label, closeDialog) => {
    const pending = deferred<ModelHealth[]>()
    let requestSignal: AbortSignal | undefined
    harness.manualProbeTarget.mockImplementation((_targetId, _models, signal) => {
      requestSignal = signal
      return pending.promise
    })
    const wrapper = await mountFormalDialog()

    expect(wrapper.text()).toContain('正式手动探活')
    expect(wrapper.text()).toContain('一次性测试')
    expect(wrapper.text()).toContain('问答测试')
    await startFormalProbe(wrapper)
    expect(requestSignal?.aborted).toBe(false)

    await closeDialog(wrapper)

    expect(requestSignal?.aborted).toBe(true)
    expect(wrapper.emitted('close')).toHaveLength(1)
    pending.resolve([result('late-result-must-not-render')])
    await flushPromises()
    expect(wrapper.text()).not.toContain('late-result-must-not-render')
  })

  it('shows queued and running phases from the formal probe stream', async () => {
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
  })
})
