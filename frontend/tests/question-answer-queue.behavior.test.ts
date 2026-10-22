// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Ref } from 'vue'
import QuestionAnswerQueuePanel from '@/modules/admin/components/dashboard/QuestionAnswerQueuePanel.vue'
import { authTokenStorageKey, clearAccessToken, storeAccessToken } from '@/modules/auth/api/auth'
import { refreshQuestionAnswerQueue } from '@/modules/admin/composables/useQuestionAnswerQueue'
import type { QuestionAnswerQueue, QuestionAnswerQueueItem } from '@/modules/admin/types/connectionHealth'

const harness = vi.hoisted(() => ({ visibility: null as unknown as Ref<'visible' | 'hidden'> }))
vi.mock('@vueuse/core', async original => {
  const actual = await original<typeof import('@vueuse/core')>(), { ref } = await import('vue')
  harness.visibility = ref('visible')
  return { ...actual, useDocumentVisibility: () => harness.visibility }
})
const wrappers: VueWrapper[] = []
const panel = (props: { workspace?: string; active?: boolean; visible?: boolean; accounts?: Record<string, string> } = {}) => {
  const wrapper = mount(QuestionAnswerQueuePanel, { props: { workspace: 'ws1', ...props } }); wrappers.push(wrapper); return wrapper
}
const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
const item = (index: number, patch: Partial<QuestionAnswerQueueItem> = {}): QuestionAnswerQueueItem => ({ targetId: 'sub2api:ws1:a', batchId: 'queue-batch', recordId: `record-${index}`, position: index + 1, modelName: 'Queue model', questionName: `Queue question ${index}`, repeatIndex: 1, createdAt: '2020-01-01T00:00:00Z', ...patch })
const queue = (count = 0, patch: Partial<QuestionAnswerQueue> = {}): QuestionAnswerQueue => {
  const result = { snapshotAt: '2020-01-01T00:04:00Z', capacity: 15, availableSlots: 15, workspaceOccupied: 0, heldCount: 0, preparingTargets: [], queued: Array.from({ length: count }, (_, index) => item(index)), processing: [], stopping: [], finalizingBatches: [], ...patch } as QuestionAnswerQueue
  result.counts = { queued: result.queued.length, processing: result.processing.length, stopping: result.stopping.length, preparingTargets: result.preparingTargets.length, finalizingBatches: result.finalizingBatches.length }
  return result
}
beforeEach(() => { harness.visibility.value = 'visible'; localStorage.clear() })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); localStorage.clear() })

describe('B shared local queue projection', () => {
  it('reads idle scopes only on an existing task notification or an explicit refresh and ignores unobserved scopes', async () => {
    vi.useFakeTimers()
    const fetcher = vi.fn(async () => reply(queue())); vi.stubGlobal('fetch', fetcher)
    const wrapper = panel(); await flushPromises()
    expect(wrapper.text()).toContain('当前没有已知任务；其他账号后来加入的任务请点击重新读取查看')
    expect(wrapper.text()).toContain('重新读取只查询状态，不会重新发题，也不会修改主站')
    await vi.advanceTimersByTimeAsync(8000); expect(fetcher).toHaveBeenCalledTimes(1)
    refreshQuestionAnswerQueue('unobserved'); await flushPromises(); expect(fetcher).toHaveBeenCalledTimes(1)
    refreshQuestionAnswerQueue('ws1'); await flushPromises(); expect(fetcher).toHaveBeenCalledTimes(2)
    await wrapper.get('[data-testid="question-answer-queue-refresh"]').trigger('click'); await flushPromises(); expect(fetcher).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[data-testid="question-answer-queue-toggle"]').attributes('aria-expanded')).toBe('false')
  })
  it('shares one pending request and serial two-second polling across folded panels', async () => {
    vi.useFakeTimers()
    const pending: ((value: Response) => void)[] = [], signals: AbortSignal[] = []
    const fetcher = vi.fn((_input: unknown, init?: RequestInit) => { signals.push(init!.signal!); return new Promise<Response>(resolve => pending.push(resolve)) })
    vi.stubGlobal('fetch', fetcher)
    const first = panel(), second = panel()
    expect(fetcher).toHaveBeenCalledTimes(1)
    pending.shift()!(reply(queue(2))); await flushPromises()
    expect(first.text()).toContain('2等待'); expect(second.text()).toContain('2等待')
    await first.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    await first.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    await vi.advanceTimersByTimeAsync(1999); expect(fetcher).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(fetcher).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(6000); expect(fetcher).toHaveBeenCalledTimes(2)
    pending.shift()!(reply(queue(1))); await flushPromises()
    await vi.advanceTimersByTimeAsync(1999); expect(fetcher).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1); expect(fetcher).toHaveBeenCalledTimes(3)
    first.unmount(); expect(signals[2]!.aborted).toBe(false)
    second.unmount(); expect(signals[2]!.aborted).toBe(true)
    pending.shift()!(reply(queue(99))); await flushPromises(); await vi.advanceTimersByTimeAsync(8000)
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('suspends both hidden panels and performs exactly one read on resume without idle discovery', async () => {
    vi.useFakeTimers()
    const fetcher = vi.fn(async () => reply(queue(1))); vi.stubGlobal('fetch', fetcher)
    const first = panel(), second = panel(); await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(1)
    harness.visibility.value = 'hidden'; await flushPromises(); await vi.advanceTimersByTimeAsync(6000)
    expect(fetcher).toHaveBeenCalledTimes(1)
    fetcher.mockImplementation(async () => reply(queue()))
    harness.visibility.value = 'visible'; await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(6000); expect(fetcher).toHaveBeenCalledTimes(2)
    await first.setProps({ visible: false }); await second.setProps({ visible: false })
    await vi.advanceTimersByTimeAsync(6000); expect(fetcher).toHaveBeenCalledTimes(2)
    await first.setProps({ visible: true }); await flushPromises(); expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('rejects an aborted old read after both panels return to the same visible workspace', async () => {
    const pending: ((value: Response) => void)[] = [], signals: AbortSignal[] = []
    const fetcher = vi.fn((_input: unknown, init?: RequestInit) => { signals.push(init!.signal!); return new Promise<Response>(resolve => pending.push(resolve)) })
    vi.stubGlobal('fetch', fetcher)
    const first = panel(), second = panel()
    harness.visibility.value = 'hidden'; await flushPromises(); expect(signals[0]!.aborted).toBe(true)
    harness.visibility.value = 'visible'; await flushPromises(); expect(fetcher).toHaveBeenCalledTimes(2)
    pending[1]!(reply(queue(1, { queued: [item(0, { questionName: 'Resumed current row' })] }))); await flushPromises()
    pending[0]!(reply(queue(1, { queued: [item(0, { questionName: 'OLD-HIDDEN-RESPONSE' })] }))); await flushPromises()
    await first.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    await second.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(first.text()).toContain('Resumed current row'); expect(second.text()).toContain('Resumed current row')
    expect(first.text()).not.toContain('OLD-HIDDEN-RESPONSE'); expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('invalidates another session immediately, rejects its late response, and releases auth listeners on final unmount', async () => {
    storeAccessToken('isolated-session-a')
    const pending: ((value: Response) => void)[] = [], signals: AbortSignal[] = []
    const fetcher = vi.fn((_input: unknown, init?: RequestInit) => { signals.push(init!.signal!); return new Promise<Response>(resolve => pending.push(resolve)) })
    vi.stubGlobal('fetch', fetcher)
    const wrapper = panel({ active: true }); pending.shift()!(reply(queue(1))); await flushPromises()
    await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('Queue question 0')
    await wrapper.get('[data-testid="question-answer-queue-refresh"]').trigger('click')
    storeAccessToken('isolated-session-b'); await flushPromises()
    expect(signals[1]!.aborted).toBe(true); expect(wrapper.text()).not.toContain('Queue question 0')
    pending.shift()!(reply(queue(1, { queued: [item(0, { questionName: 'OLD-SESSION-DO-NOT-SHOW' })] }))); await flushPromises()
    expect(wrapper.text()).not.toContain('OLD-SESSION')
    pending.shift()!(reply(queue(1, { queued: [item(0, { questionName: 'New session row' })] }))); await flushPromises()
    expect(wrapper.text()).toContain('New session row')
    clearAccessToken(); await flushPromises(); expect(wrapper.text()).not.toContain('New session row')
    expect(fetcher).toHaveBeenCalledTimes(3)
    wrapper.unmount(); storeAccessToken('isolated-session-c'); await flushPromises(); expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('never lets a cancelled old401 queue response expire a newer authenticated session', async () => {
    storeAccessToken('isolated-old-request-session')
    const pending: ((value: Response) => void)[] = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => pending.push(resolve))))
    const wrapper = panel()
    storeAccessToken('isolated-current-request-session')
    pending.shift()!(reply({ message: 'auth.errors.unauthorized' }, 401)); await flushPromises()
    expect(localStorage.getItem(authTokenStorageKey)).toBe('isolated-current-request-session')
    pending.shift()!(reply(queue(1, { queued: [item(0, { questionName: 'Current authenticated row' })] }))); await flushPromises()
    await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('Current authenticated row')
  })

  it('clears the shared snapshot and stops polling when the current queue response expires authentication', async () => {
    storeAccessToken('isolated-expiring-session')
    const originalPath = window.location.pathname
    window.history.replaceState({}, '', '/login')
    try {
      const fetcher = vi.fn(async () => reply(queue(1))); vi.stubGlobal('fetch', fetcher)
      const wrapper = panel(); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
      expect(wrapper.text()).toContain('Queue question 0')
      fetcher.mockImplementation(async () => reply({ message: 'auth.errors.unauthorized' }, 401))
      await wrapper.get('[data-testid="question-answer-queue-refresh"]').trigger('click'); await flushPromises()
      expect(localStorage.getItem(authTokenStorageKey)).toBeNull()
      expect(wrapper.text()).not.toContain('Queue question 0')
      expect(wrapper.text()).not.toContain('auth.errors.unauthorized')
      expect(fetcher).toHaveBeenCalledTimes(2)
    } finally { window.history.replaceState({}, '', originalPath) }
  })

  it('clears old data when a storage identity replacement is observed after a request', async () => {
    vi.useFakeTimers()
    storeAccessToken('isolated-storage-a')
    let release: ((value: Response) => void) | undefined
    const fetcher = vi.fn(async () => reply(queue(1))); vi.stubGlobal('fetch', fetcher)
    const wrapper = panel(); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    fetcher.mockImplementation(() => new Promise(resolve => { release = resolve }))
    await wrapper.get('[data-testid="question-answer-queue-refresh"]').trigger('click')
    localStorage.setItem(authTokenStorageKey, 'isolated-storage-b')
    release!(reply(queue(1, { queued: [item(0, { questionName: 'UNSAFE-LATE-RESPONSE' })] }))); await flushPromises()
    expect(wrapper.text()).not.toContain('Queue question 0'); expect(wrapper.text()).not.toContain('UNSAFE-LATE-RESPONSE')
    expect(wrapper.text()).toContain('后台过程信息不可用')
    fetcher.mockImplementation(async () => reply(queue(1, { queued: [item(0, { questionName: 'Latest storage session row' })] })))
    await wrapper.get('[data-testid="question-answer-queue-refresh"]').trigger('click'); await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).toContain('Latest storage session row')
    localStorage.setItem(authTokenStorageKey, 'isolated-storage-c')
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).not.toContain('Latest storage session row')
  })

  it('aborts a previous workspace and never uses its late response or stale snapshot after reopening', async () => {
    const pending: ((value: Response) => void)[] = [], signals: AbortSignal[] = [], paths: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: unknown, init?: RequestInit) => { paths.push(String(input)); signals.push(init!.signal!); return new Promise<Response>(resolve => pending.push(resolve)) }))
    const wrapper = panel()
    await wrapper.setProps({ workspace: 'ws2' }); expect(signals[0]!.aborted).toBe(true)
    pending.shift()!(reply(queue(1, { queued: [item(0, { questionName: 'OLD-WORKSPACE-DO-NOT-SHOW' })] }))); await flushPromises()
    pending.shift()!(reply(queue(1, { queued: [item(0, { targetId: 'sub2api:ws2:b', questionName: 'Workspace two row' })] }))); await flushPromises()
    await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('Workspace two row'); expect(wrapper.text()).not.toContain('OLD-WORKSPACE')
    await wrapper.setProps({ workspace: 'ws1' }); expect(wrapper.text()).not.toContain('Workspace two row'); expect(paths).toHaveLength(3)
    expect(paths[1]).toContain('workspace=ws2'); expect(paths[2]).toContain('workspace=ws1')
  })

  it('keeps the last rows and freezes HTTP duration after a failed read despite a different local clock', async () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2030-01-01T00:00:00Z'))
    const processing = item(0, { runtime: { recordId: 'record-0', step: 'request', diagnostics: { version: 1, timeoutMs: 600000, selectedAt: '2020-01-01T00:00:00Z', requestStartedAt: '2020-01-01T00:00:00Z', requestWrittenAt: '2020-01-01T00:00:01Z', receivedBytes: 0 } } })
    const fetcher = vi.fn(async () => reply(queue(1, { processing: [processing], availableSlots: 14, workspaceOccupied: 1 }))); vi.stubGlobal('fetch', fetcher)
    const wrapper = panel(); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('HTTP已等待240.0秒'); expect(wrapper.text()).toContain('最长等待600.0秒')
    expect(wrapper.get('[data-testid="question-answer-queue-queued-row"]').text()).toContain('尚未开始HTTP等待')
    fetcher.mockImplementation(async () => reply({ message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: '读取排队信息失败：isolated read timeout。只读取状态，可以重新读取。', operationResult: 'completed', step: 'queue' }, 503))
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    const frozen = wrapper.get('[data-testid="question-answer-queue-processing-row"]').text()
    expect(wrapper.text()).toContain('isolated read timeout'); expect(wrapper.text()).toContain('等待时间暂停递增')
    await vi.advanceTimersByTimeAsync(8000); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-queue-processing-row"]').text()).toBe(frozen)
    expect(wrapper.findAll('[data-testid="question-answer-queue-queued-row"]')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('admin.connectionHealth')
  })

  it('renders fifty more at a time all the way to the last received row without a POST', async () => {
    const fetcher = vi.fn(async () => reply(queue(151))); vi.stubGlobal('fetch', fetcher)
    const wrapper = panel(); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    for (const expected of [50, 100, 150, 151]) {
      expect(wrapper.findAll('[data-testid="question-answer-queue-queued-row"]')).toHaveLength(expected)
      if (expected < 151) await wrapper.get('[data-testid="question-answer-queue-more"]').trigger('click')
    }
    expect(wrapper.text()).toContain('Queue question 150'); expect(wrapper.find('[data-testid="question-answer-queue-more"]').exists()).toBe(false)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(fetcher.mock.calls.every(call => call[1]?.method === undefined)).toBe(true)
  })

  it('shows full ten-thousand metadata counts while initially rendering only fifty', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(queue(10000))))
    const wrapper = panel(); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('10000等待'); expect(wrapper.findAll('[data-testid="question-answer-queue-queued-row"]')).toHaveLength(50)
    await wrapper.get('[data-testid="question-answer-queue-more"]').trigger('click')
    expect(wrapper.findAll('[data-testid="question-answer-queue-queued-row"]')).toHaveLength(100)
  })

  it('locates preparation, stopped and all-confirmed finalizing batches without guessing recovery or unsaved question counts', async () => {
    const snapshot = queue(0, { heldCount: 3, availableSlots: 12, workspaceOccupied: 3, preparingTargets: [{ targetId: 'sub2api:ws1:b', batchId: 'preparing-batch' }], stopping: [item(1)], finalizingBatches: [{ targetId: 'sub2api:ws1:c', batchId: 'final-batch', state: 'failed', reason: 'storage_error', message: 'final write confirmation lost', heldCount: 3 }] })
    vi.stubGlobal('fetch', vi.fn(async () => reply(snapshot)))
    const wrapper = panel({ accounts: { 'sub2api:ws1:b': 'Named account' } }); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('Named account'); expect(wrapper.text()).toContain('账号编号 c'); expect(wrapper.text()).toContain('暂时保留3个位置')
    expect(wrapper.text()).toContain('final write confirmation lost'); expect(wrapper.text()).toContain('结束结果尚在确认')
    expect(wrapper.text()).not.toMatch(/3[条个]题|未保存3|service_shutdown|重试保存并结束|租约|回执/)
    await wrapper.get('[data-testid="question-answer-queue-finalizing-row"] button').trigger('click')
    expect(wrapper.emitted('view')).toEqual([[{ targetId: 'sub2api:ws1:c', batchId: 'final-batch' }]])
    await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('本工作区有3个位置因批次结束尚未确认而暂时保留')
    expect(wrapper.find('[data-testid="question-answer-queue-finalizing-row"]').exists()).toBe(false)
  })

  it('treats a queue404 as read-only unavailability without claiming creation is unknown', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply({ message: 'admin.connectionHealth.errors.request' }, 404)))
    const wrapper = panel(); await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('本次只读取状态'); expect(wrapper.text()).toContain('重新读取')
    expect(wrapper.text()).toContain('原批次结果和人工判断仍可查看')
    expect(wrapper.text()).not.toContain('是否已提交')
  })
})
