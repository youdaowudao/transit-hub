// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import QuestionAnswerScheduleExecutionDetail from '@/modules/admin/components/dashboard/QuestionAnswerScheduleExecutionDetail.vue'
import QuestionAnswerExecutionDetails from '@/modules/admin/components/dashboard/QuestionAnswerExecutionDetails.vue'
import type { AdminGroupHealth, QuestionAnswerBatch, QuestionAnswerDiagnostics, QuestionAnswerFinalization, QuestionAnswerQueue, QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'
import { createDefaultConnectionHealthPreferences } from '@/modules/admin/utils/connectionHealthPreferences'
import { questionAnswerFixtureStats, upgradeQuestionAnswerHistoryFixture } from './fixtures/c1QuestionAnswerHistory'
import { c2Execution } from './fixtures/c2QuestionAnswerSchedules'

const wrappers: VueWrapper[] = []
const keep = <T extends VueWrapper>(wrapper: T) => { wrappers.push(wrapper); return wrapper }
const targetId = 'sub2api:ws1:storage-account', batchId = 'storage-batch'
const record = (): QuestionAnswerRecord => ({ id: 'storage-record', targetId, batchId, modelName: 'm', questionId: 'q', questionName: '保存隔离题', questionBody: 'q', questionKeywordSnapshot: [], reasoningEffort: 'medium', requestProtocol: 'chat_completions', repeatIndex: 1, answerBody: 'already saved answer', status: 'succeeded', errorType: '', answerJudgment: 'correct', judgmentSource: 'manual', manualError: false, createdAt: '2026-10-22T01:00:00Z', startedAt: '2026-10-22T01:00:01Z', completedAt: '2026-10-22T01:00:04Z', updatedAt: '2026-10-22T01:00:04Z' })
const batch = (finalization?: QuestionAnswerFinalization): QuestionAnswerBatch => ({ batchId, records: [record()], active: false, reasoningEffort: 'medium', repeatCount: 1, submittedCount: 1, completedCount: 1, runningCount: 0, currentModel: '', currentQuestion: '', stats: questionAnswerFixtureStats([record()]), finalization })
const finalization = (overrides: Partial<QuestionAnswerFinalization> = {}): QuestionAnswerFinalization => ({ batchId, state: 'failed', recovery: 'cancel', reason: 'storage_error', message: 'fixture final ending interrupted after answers were saved', heldCount: 3, ...overrides })
const diagnostics = (): QuestionAnswerDiagnostics => ({ version: 1, selectedAt: '2026-10-22T01:00:01Z', requestStartedAt: '2026-10-22T01:00:02Z', requestFinishedAt: '2026-10-22T01:00:04Z', requestElapsedMs: 2000, timeoutMs: 600000, requestWrittenAt: null, firstResponseByteAt: null, responseHeadersAt: null, lastReceivedAt: null, receivedBytes: 0 })
const queue = (): QuestionAnswerQueue => ({ snapshotAt: '2026-10-22T01:00:12Z', capacity: 15, availableSlots: 12, workspaceOccupied: 3, heldCount: 3, queued: [], processing: [], stopping: [], preparingTargets: [], finalizingBatches: [], counts: { queued: 0, processing: 0, stopping: 0, preparingTargets: 0, finalizingBatches: 0 } })
const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
const install = (read: () => QuestionAnswerBatch, readQueue: () => QuestionAnswerQueue = queue, override?: (path: string, init?: RequestInit) => Promise<Response> | undefined) => {
  const fetcher = vi.fn(async (input: unknown, init?: RequestInit) => {
    const path = String(input), custom = override?.(path, init)
    if (custom) return custom
    if (path.includes('/question-answer-queue')) return reply(readQueue())
    if (path.endsWith('/models')) return reply([{ id: 'm', name: 'm' }])
    if (path.endsWith('/test-questions')) return reply([{ id: 'q', name: '保存隔离题', body: 'q', enabled: true, isDefault: true, keywords: [] }])
    if (path.includes('/question-answers/history')) return reply(upgradeQuestionAnswerHistoryFixture({ records: read().records, page: 1, totalItems: 1, totalPages: 1, todayStats: read().stats }))
    if (path.includes('/question-answers/batches/')) return reply(read())
    if (path.includes('/question-answer-schedule-executions/')) return reply(c2Execution())
    return reply({ items: [] })
  })
  vi.stubGlobal('fetch', fetcher)
  return fetcher
}
const manual = async () => {
  const wrapper = keep(mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId, accountName: '保存账号', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] } }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
  await wrapper.setProps({ open: true }); await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === '问答测试')!.trigger('click'); await flushPromises()
  return wrapper
}
const posts = (fetcher: ReturnType<typeof install>) => fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = '' })

it('retries only the original batch ending after every answer was saved and never equates held positions with missing answers', async () => {
  let current = batch(finalization())
  const fetcher = install(() => current, queue, (path, init) => path.endsWith('/cancel') && init?.method === 'POST' ? Promise.resolve(reply(current = batch())) : undefined)
  const wrapper = await manual(), panel = wrapper.get('[data-testid="question-answer-finalization"]')
  expect(panel.text()).toContain('fixture final ending interrupted after answers were saved')
  expect(panel.text()).toContain('3个位置暂时保留'); expect(panel.text()).toContain('这不是未保存的题数')
  expect(panel.text()).toContain('只保存已经取得的结果并结束剩余题，不会再次发送题目或修改主站')
  expect(panel.text()).not.toContain('3题未保存')
  const retry = panel.get('[data-testid="question-answer-finalization-retry"]')
  expect(retry.text()).toBe('重试保存并结束'); await retry.trigger('click'); await flushPromises()
  expect(posts(fetcher)).toHaveLength(1); expect(String(posts(fetcher)[0]![0])).toContain(`${batchId}/cancel`)
  expect(wrapper.find('[data-testid="question-answer-finalization"]').exists()).toBe(false)
  await wrapper.get('[data-testid="question-answer-result-group"]').get('button').trigger('click')
  expect(wrapper.get('[data-testid="question-answer-record-storage-record"]').text()).toContain('already saved answer')
  expect(wrapper.get('[data-testid="question-answer-judgment-source"]').text()).toContain('人工判定·正确')
})

it('offers save-only recovery when a later cancel interrupted saving an already sealed complete answer', async () => {
  const current = batch(finalization({ reason: 'cancelled' }))
  const active = { ...record(), status: 'running' as const, answerBody: '', answerJudgment: null, judgmentSource: null, completedAt: null }
  current.records = [active]; current.stats = questionAnswerFixtureStats([active]); current.active = true
  current.runtime = { snapshotAt: '2026-10-22T01:00:12Z', items: [{ recordId: active.id, step: 'confirming', pendingResultStatus: 'succeeded', storageError: '保存请求被停止操作中断，将确认保存结果', diagnostics: diagnostics() }] }
  const fetcher = install(() => current)
  const wrapper = await manual()
  expect(wrapper.get('[data-testid="question-answer-finalization-retry"]').text()).toBe('重试保存并结束')
  expect(wrapper.text()).toContain('已取得完整回答'); expect(wrapper.text()).toContain('保存请求被停止操作中断')
  expect(wrapper.get('[data-testid="question-answer-execution-details"]').text()).not.toContain('上游失败'); expect(wrapper.get('[data-testid="question-answer-execution-details"]').text()).not.toContain('答案正确')
  expect(posts(fetcher)).toHaveLength(0)
})

it('retains the ordinary pending-cancel label when no save failure was observed', async () => {
  install(() => batch(finalization({ state: 'pending', reason: 'cancelled', message: '已停止发送，结束结果尚在确认' })))
  const wrapper = await manual()
  expect(wrapper.get('[data-testid="question-answer-finalization-retry"]').text()).toBe('重试结束处理')
})

it('disables an in-flight save retry and guards a second click without creating a new batch', async () => {
  let release!: (value: Response) => void
  const fetcher = install(() => batch(finalization()), queue, (path, init) => path.endsWith('/cancel') && init?.method === 'POST' ? new Promise(resolve => { release = resolve }) : undefined)
  const wrapper = await manual(), retry = wrapper.get('[data-testid="question-answer-finalization-retry"]')
  await retry.trigger('click'); await flushPromises(); expect(retry.attributes('disabled')).toBeDefined()
  retry.element.dispatchEvent(new MouseEvent('click', { bubbles: true })); await flushPromises()
  expect(posts(fetcher)).toHaveLength(1)
  release(reply(batch())); await flushPromises()
})

it.each(['unavailable', 'service_shutdown'] as const)('does not invent a save recovery button for %s', async recovery => {
  const fetcher = install(() => batch(finalization({ recovery })))
  const wrapper = await manual()
  expect(wrapper.find('[data-testid="question-answer-finalization-retry"]').exists()).toBe(false)
  if (recovery === 'service_shutdown') expect(wrapper.text()).toContain('系统里没有可取消的批次记录')
  expect(posts(fetcher)).toHaveLength(0)
})

it('hides recovery while the result of a failed cancel and verification read remains unknown', async () => {
  let failed = false
  const fetcher = install(() => batch(finalization()), queue, (path, init) => {
    if (path.endsWith('/cancel') && init?.method === 'POST') { failed = true; return Promise.reject(new Error('fixture cancel connection interrupted')) }
    if (failed && path.endsWith('/batches/latest')) return Promise.reject(new Error('fixture verification read interrupted'))
    return undefined
  })
  const wrapper = await manual()
  await wrapper.get('[data-testid="question-answer-finalization-retry"]').trigger('click'); await flushPromises()
  expect(wrapper.text()).toContain('暂时查不到上一批问答是否已结束')
  expect(wrapper.find('[data-testid="question-answer-finalization-retry"]').exists()).toBe(false)
  expect(posts(fetcher)).toHaveLength(1)
})

it('recovers a first queued cancellation after its batch-presence read failed without sending a new question', async () => {
  const pending: QuestionAnswerRecord = { ...record(), status: 'pending', answerBody: '', answerJudgment: null, judgmentSource: null, startedAt: null, completedAt: null }
  let current: QuestionAnswerBatch = { ...batch(), records: [pending], active: true, completedCount: 0, stats: questionAnswerFixtureStats([pending]) }
  let cancels = 0
  const cause = '读取批次记录时失败，已经停止发送，结束结果尚未确认；batch lookup temporarily unavailable'
  const fetcher = install(() => current, queue, (path, init) => {
    if (!path.endsWith('/cancel') || init?.method !== 'POST') return undefined
    cancels++
    if (cancels === 1) {
      current = { ...current, finalization: finalization({ reason: 'cancelled', heldCount: 0, message: cause }) }
      return Promise.resolve(reply({ message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: cause, step: 'cancelling', operationResult: 'unknown', batchId }, 503))
    }
    const stopped: QuestionAnswerRecord = { ...pending, status: 'cancelled', completedAt: '2026-10-22T01:00:12Z' }
    current = { ...current, finalization: undefined, records: [stopped], active: false, completedCount: 1, stats: questionAnswerFixtureStats([stopped]) }
    return Promise.resolve(reply(current))
  })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  const wrapper = await manual()
  expect(wrapper.find('[data-testid="question-answer-finalization"]').exists()).toBe(false)
  await wrapper.get('[data-testid="question-answer-stop-latest"]').trigger('click'); await flushPromises()
  const panel = wrapper.get('[data-testid="question-answer-finalization"]')
  expect(panel.text()).toContain('batch lookup temporarily unavailable')
  expect(panel.text()).toContain('已经停止发送')
  expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(false)
  const retry = panel.get('[data-testid="question-answer-finalization-retry"]')
  expect(retry.text()).toBe('重试保存并结束')
  expect(retry.attributes('disabled')).toBeUndefined()
  expect(posts(fetcher)).toHaveLength(1)
  await retry.trigger('click'); await flushPromises()
  expect(wrapper.find('[data-testid="question-answer-finalization"]').exists()).toBe(false)
  expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(false)
  expect(wrapper.text()).not.toContain('already saved answer')
  expect(cancels).toBe(2)
  expect(posts(fetcher)).toHaveLength(2)
  expect(posts(fetcher).every(([path]) => String(path).endsWith(`/batches/${batchId}/cancel`))).toBe(true)
})

it.each(['cancel', 'service_shutdown', 'unavailable'] as const)('keeps fallback %s finalization read-only and gives only applicable recovery guidance', async recovery => {
  const fetcher = install(() => batch(finalization({ recovery })))
  const wrapper = keep(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId, batchId, accountName: '已离开当前列表的账号', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
  await flushPromises()
  const panel = wrapper.get('[data-testid="question-answer-readonly-finalization"]')
  expect(panel.text()).toContain('fixture final ending interrupted'); expect(panel.text()).toContain('3个位置暂时保留')
  expect(panel.text()).toContain('这里只能读取记录，不会重新发题或修改主站')
  expect(wrapper.find('[data-testid="question-answer-finalization-retry"]').exists()).toBe(false)
  if (recovery === 'cancel') { expect(panel.text()).toContain('回到分组健康查找这个账号'); expect(panel.text()).toContain('找不到账号时') }
  else { expect(panel.text()).not.toContain('回到分组健康查找这个账号'); expect(panel.text()).not.toContain('打开当前账号') }
  if (recovery === 'service_shutdown') expect(panel.text()).toContain('未保存的回答无法恢复')
  expect(posts(fetcher)).toHaveLength(0)
})

it.each(['manual', 'batch', 'scheduled', 'fallback'] as const)('shows a retained pre-HTTP storage cause through the %s queue without adding a restore or send action', async entry => {
  const state = queue(), d = { ...diagnostics(), requestStartedAt: null, requestFinishedAt: null, requestElapsedMs: null, timeoutMs: null, failureStep: 'starting' as const, localError: 'fixture start write interrupted' }
  state.processing = [{ targetId, batchId, recordId: record().id, modelName: 'm', questionName: '保存隔离题', repeatIndex: 1, createdAt: record().createdAt, runtime: { recordId: record().id, step: 'confirming', pendingResultStatus: 'failed', storageError: 'fixture start write interrupted', diagnostics: d } }]; state.counts.processing = 1
  const fetcher = install(() => batch(), () => state)
  let wrapper: VueWrapper
  if (entry === 'manual') wrapper = await manual()
  else if (entry === 'batch') {
    const groups = [{ id: 'g', name: 'G', accounts: [{ id: 'storage-account', targetId, name: '保存账号', platform: 'openai', type: 'apikey', status: 'active', probeAvailable: true, modelHealth: [] }] }] as AdminGroupHealth[]
    wrapper = keep(mount(QuestionAnswerBatchDrawer, { props: { groups, preferenceScope: 'ws1', preferences: createDefaultConnectionHealthPreferences().questionAnswer } }))
    await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click')
  } else if (entry === 'scheduled') wrapper = keep(mount(QuestionAnswerScheduleExecutionDetail, { props: { executionId: c2Execution().id, workspace: 'ws1', scheduleName: '保存计划' } }))
  else wrapper = keep(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId, batchId, accountName: '保存账号', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
  await flushPromises(); const panel = wrapper.get('[data-testid="question-answer-queue-panel"]')
  await panel.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
  const row = panel.get('[data-testid="question-answer-queue-processing-row"]')
  expect(row.text()).toContain('fixture start write interrupted'); expect(row.text()).toContain('没有调用答题接口')
  expect(row.text()).not.toContain('请求已结束'); expect(row.text()).not.toContain('上游失败')
  expect(panel.find('[data-testid="question-answer-finalization-retry"]').exists()).toBe(false)
  expect(posts(fetcher)).toHaveLength(0)
})

it('holds HTTP time fixed from its recorded end even when the old duration field is absent', async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2030-01-01T00:00:00Z'))
  const pending = { ...record(), status: 'running' as const, answerBody: '', answerJudgment: null, judgmentSource: null }
  const wrapper = keep(mount(QuestionAnswerExecutionDetails, { props: { record: pending, snapshotAt: '2026-10-22T01:00:12Z', runtime: { recordId: pending.id, step: 'confirming', pendingResultStatus: 'succeeded', diagnostics: { ...diagnostics(), requestElapsedMs: null } } } }))
  expect(wrapper.text()).toContain('HTTP等待 2.0秒'); await vi.advanceTimersByTimeAsync(3000)
  expect(wrapper.text()).toContain('HTTP等待 2.0秒'); expect(wrapper.get('[data-testid="question-answer-save-wait"]').text()).toContain('11.0秒')
  expect(wrapper.text()).not.toContain('HTTP已等待')
})


it.each(['cancelled', 'internal-stop', 'stopped-save-error'] as const)('does not invent a start database failure for %s before HTTP', mode => {
  const r = { ...record(), status: 'pending' as const, answerBody: '', answerJudgment: null, judgmentSource: null }
  const wrapper = keep(mount(QuestionAnswerExecutionDetails, { props: { record: r, snapshotAt: '2026-10-22T01:00:12Z', runtime: {
    recordId: r.id, step: 'confirming', pendingResultStatus: mode === 'cancelled' ? 'cancelled' : 'failed',
    storageError: mode === 'stopped-save-error' ? 'fixture stopped result save interrupted' : undefined,
    diagnostics: { ...diagnostics(), requestStartedAt: null, requestFinishedAt: null, requestElapsedMs: null, timeoutMs: null, failureStep: 'starting' },
  } } }))
  const stage = wrapper.get('[data-testid="question-answer-process-stage"]').text()
  expect(stage).toContain('调用答题接口前已停止'); expect(stage).toContain('没有调用答题接口')
  expect(stage).not.toContain('记录这题开始时失败'); expect(stage).not.toContain('没有取得开始状态'); expect(stage).not.toContain('请求已结束')
  if (mode === 'stopped-save-error') expect(stage).toContain('fixture stopped result save interrupted')
  else expect(stage).toContain('正在保存停止结果')
})

it('explains restart loss for a historical record without inventing recoverable answer or network evidence', () => {
  const r = { ...record(), status: 'failed' as const, errorType: 'service_restarted', answerBody: '', answerJudgment: null, judgmentSource: null, diagnostics: null }
  const wrapper = keep(mount(QuestionAnswerExecutionDetails, { props: { record: r } }))
  expect(wrapper.text()).toContain('未保存的回答无法恢复')
  expect(wrapper.text()).toContain('请先查看历史，确认后再手动测试')
  expect(wrapper.text()).toContain('当时未记录网络过程')
  expect(wrapper.text()).not.toContain('正在保存结果'); expect(wrapper.find('button').exists()).toBe(false)
})
