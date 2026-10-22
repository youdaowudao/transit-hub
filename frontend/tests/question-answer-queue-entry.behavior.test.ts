// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import QuestionAnswerScheduleExecutionDetail from '@/modules/admin/components/dashboard/QuestionAnswerScheduleExecutionDetail.vue'
import type { AdminGroupHealth, QuestionAnswerBatch, QuestionAnswerQueue, QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'
import { createDefaultConnectionHealthPreferences } from '@/modules/admin/utils/connectionHealthPreferences'
import { storeAccessToken } from '@/modules/auth/api/auth'
import { cancelQuestionAnswerBatch, getQuestionAnswerBatch, getQuestionAnswerHistory, getQuestionAnswerSummary, setQuestionAnswerJudgment, startQuestionAnswerBatch } from '@/modules/admin/api/connectionHealth'
import { questionAnswerFixtureStats, upgradeQuestionAnswerHistoryFixture } from './fixtures/c1QuestionAnswerHistory'
import { c2Execution } from './fixtures/c2QuestionAnswerSchedules'

const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(wrapper: T) => { wrappers.push(wrapper); return wrapper }
const target = 'sub2api:ws1:a', batchId = 'entry-batch'
const record = (): QuestionAnswerRecord => ({ id: 'entry-record', batchId, targetId: target, modelName: 'm', questionId: 'q', questionName: '入口隔离题', questionBody: 'q', questionKeywordSnapshot: [], reasoningEffort: 'medium', requestProtocol: 'chat_completions', repeatIndex: 1, answerBody: 'Existing answer survives queue failure', status: 'succeeded', errorType: '', answerJudgment: 'unreviewed', judgmentSource: null, manualError: false, createdAt: '2026-10-22T01:00:00Z', startedAt: '2026-10-22T01:00:01Z', completedAt: '2026-10-22T01:00:02Z', updatedAt: '2026-10-22T01:00:02Z' })
const batch = (): QuestionAnswerBatch => ({ batchId, records: [record()], active: false, reasoningEffort: 'medium', repeatCount: 1, submittedCount: 1, completedCount: 1, runningCount: 0, currentModel: '', currentQuestion: '', stats: questionAnswerFixtureStats([record()]) })
const queue = (): QuestionAnswerQueue => ({ snapshotAt: '2026-10-22T01:00:02Z', capacity: 15, availableSlots: 15, workspaceOccupied: 0, heldCount: 0, queued: [{ recordId: 'waiting-record', targetId: target, batchId, position: 1, modelName: 'm', questionName: '排队隔离题', repeatIndex: 1, createdAt: '2026-10-22T01:00:00Z' }], processing: [], preparingTargets: [], stopping: [], finalizingBatches: [], counts: { queued: 1, processing: 0, preparingTargets: 0, stopping: 0, finalizingBatches: 0 } })
const groups = (): AdminGroupHealth[] => [{ id: 'g', name: '入口隔离组', accounts: ['a', 'b'].map(id => ({ id, targetId: `sub2api:ws1:${id}`, name: `入口账号${id}`, platform: 'openai', type: 'apikey', status: 'active', probeAvailable: true, modelHealth: [] })) }] as AdminGroupHealth[]
const preferences = () => ({ ...createDefaultConnectionHealthPreferences().questionAnswer, modelIds: ['m'], questionIds: ['q'], repeatCount: 1, batchTargetIds: [target, 'sub2api:ws1:b'] })
const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
const baseReply = (path: string) => {
  if (path.includes('/question-answer-queue')) return reply(queue())
  if (path.endsWith('/models')) return reply([{ id: 'm', name: 'm' }])
  if (path.endsWith('/test-questions')) return reply([{ id: 'q', name: '入口隔离题', body: 'q', enabled: true, isDefault: true, keywords: [] }])
  if (path.includes('/question-answers/history')) return reply(upgradeQuestionAnswerHistoryFixture({ records: [record()], page: 1, totalItems: 1, totalPages: 1, todayStats: batch().stats }))
  if (path.includes('/question-answers/batches/')) return reply(batch())
  if (path.includes('/question-answer-schedule-executions/')) return reply(c2Execution())
  return reply({ items: [], counts: { total: 0, open: 0, closed: 0, attention: 0, untested: 0 }, settings: {} })
}
const manual = async () => {
  const wrapper = track(mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId: target, accountName: '入口账号a', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] } }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
  await wrapper.setProps({ open: true }); await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === '问答测试')!.trigger('click'); await flushPromises()
  return wrapper
}
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.innerHTML = ''; localStorage.clear() })

describe('B queue entry preservation', () => {
  it('does not accept a judgment for a different record as confirmation of the requested record', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply({ id: 'another-record', answerJudgment: 'correct' })))
    const error = await setQuestionAnswerJudgment(target, 'expected-record', 'correct', record().updatedAt).catch(cause => cause)
    expect(error.operationResult).toBe('unknown'); expect(error.message).toContain('先重新读取这条记录')
  })
  it.each(['history-empty', 'history-stats', 'history-nested', 'summary'] as const)('keeps malformed %s normalization read-only with no raw parser error', async shape => {
    const payload = shape === 'history-empty' ? {} : shape === 'history-stats' ? { batches: [], todayStats: null }
      : shape === 'history-nested' ? { batches: [{ batchId, models: [], questions: [{ byModel: {} }], stats: batch().stats }], todayStats: batch().stats } : { targetId: target, todayStats: null }
    const fetcher = vi.fn(async () => reply(payload)); vi.stubGlobal('fetch', fetcher)
    const error = await (shape === 'summary' ? getQuestionAnswerSummary(target) : getQuestionAnswerHistory(target, 1)).catch(cause => cause)
    expect(error.operationResult).toBe('unknown')
    expect(error.message).toContain('重新读取'); expect(error.message).not.toMatch(/Cannot read|\.map|undefined|\[object Object\]|是否已提交/)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('preserves valid old optional defaults while accepting a batch without diagnostics or runtime', async () => {
    const old = JSON.parse(JSON.stringify(batch()))
    delete old.repeatCount; delete old.stats.byModel; delete old.stats.byQuestion
    for (const field of ['judgmentSource', 'repeatIndex', 'questionKeywordSnapshot', 'diagnostics']) delete old.records[0][field]
    vi.stubGlobal('fetch', vi.fn(async () => reply(old)))
    const result = await getQuestionAnswerBatch(target, batchId)
    expect(result.repeatCount).toBe(1); expect(result.records[0]!.repeatIndex).toBeNull()
    expect(result.records[0]!.judgmentSource).toBeNull(); expect(result.records[0]!.questionKeywordSnapshot).toBeNull()
    expect(result.stats.byModel).toEqual([]); expect(result.stats.byQuestion).toEqual([])
  })
  it.each(['unauthorized', 'session-replaced', 'old-unauthorized'] as const)('does not continue the next account after %s invalidates a pending creation', async mode => {
    storeAccessToken('isolated-batch-session-a')
    const originalPath = window.location.pathname
    window.history.replaceState({}, '', '/login')
    let release: (() => void) | undefined, posts = 0
    vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/question-answers/batches') && init?.method === 'POST') {
        posts++; await new Promise<void>(resolve => { release = resolve })
        return mode !== 'session-replaced' ? reply({ message: 'auth.errors.unauthorized' }, 401) : reply(batch())
      }
      return baseReply(path)
    }))
    try {
      const wrapper = track(mount(QuestionAnswerBatchDrawer, { props: { groups: groups(), preferenceScope: 'ws1', preferences: preferences() } }))
      await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises()
      await wrapper.get('[data-testid="question-answer-batch-start"]').trigger('click'); await flushPromises()
      expect(posts).toBe(1)
      if (mode !== 'unauthorized') storeAccessToken('isolated-batch-session-b')
      release!(); await flushPromises()
      expect(posts).toBe(1)
      expect(wrapper.emitted('question-answer-started')).toBeUndefined()
      expect(wrapper.find('[data-testid="question-answer-batch-submitting"]').exists()).toBe(false)
      expect(wrapper.find(`[data-testid="question-answer-batch-outcome-${target}"]`).exists()).toBe(false)
      if (mode === 'old-unauthorized') expect(localStorage.getItem('transithub.auth.accessToken')).toBe('isolated-batch-session-b')
    } finally { window.history.replaceState({}, '', originalPath) }
  })

  it.each(['create', 'read', 'cancel', 'judge'] as const)('ignores an old401 %s response before authentication side effects can clear the new session', async operation => {
    storeAccessToken('isolated-api-session-a')
    let release: ((value: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { release = resolve })))
    const request = (operation === 'create' ? startQuestionAnswerBatch(target, ['m'], ['q'], 'medium', 1)
      : operation === 'read' ? getQuestionAnswerBatch(target, batchId)
        : operation === 'cancel' ? cancelQuestionAnswerBatch(target, batchId)
          : setQuestionAnswerJudgment(target, record().id, 'correct', record().updatedAt)).catch(cause => cause)
    storeAccessToken('isolated-api-session-b'); release!(reply({ message: 'auth.errors.unauthorized' }, 401))
    const error = await request
    expect(error.name).toBe('AbortError')
    expect(localStorage.getItem('transithub.auth.accessToken')).toBe('isolated-api-session-b')
  })
  it.each(['manual', 'read-only'] as const)('re-reads the exact current batch from the %s queue button with no new question or mutation', async entry => {
    const calls: { path: string; method?: string }[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => { calls.push({ path: String(input), method: init?.method }); return baseReply(String(input)) }))
    const wrapper = entry === 'manual' ? await manual() : track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: target, batchId, accountName: '入口账号a', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
    await flushPromises()
    const before = calls.filter(call => call.path.endsWith(`/batches/${batchId}`)).length
    await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    await wrapper.get('[data-testid="question-answer-queue-queued-row"] button').trigger('click'); await flushPromises()
    expect(calls.filter(call => call.path.endsWith(`/batches/${batchId}`))).toHaveLength(before + 1)
    expect(calls.some(call => call.method === 'POST' || call.method === 'PUT')).toBe(false)
    expect(wrapper.text()).toContain('Existing answer survives queue failure')
  })

  it.each(['manual', 'read-only'] as const)('keeps old results and original judgment permissions when the %s queue is404', async entry => {
    let judgments = 0
    vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/question-answer-queue')) return reply({ message: 'admin.connectionHealth.errors.request' }, 404)
      if (path.endsWith('/judgment') && init?.method === 'PUT') { judgments++; return reply({ ...record(), answerJudgment: 'correct', judgmentSource: 'manual' }) }
      return baseReply(path)
    }))
    const wrapper = entry === 'manual' ? await manual() : track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: target, batchId, accountName: '入口账号a', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
    await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('Existing answer survives queue failure'); expect(wrapper.text()).toContain('本次只读取状态')
    if (entry === 'manual') {
      const card = wrapper.get('[data-testid="question-answer-record-entry-record"]')
      await card.findAll('button').find(button => button.text() === '正确')!.trigger('click'); await flushPromises()
      expect(judgments).toBe(1)
    } else { expect(wrapper.findAll('button').some(button => button.text() === '正确')).toBe(false); expect(judgments).toBe(0) }
  })

  it.each(['batch', 'schedule'] as const)('emits the exact queue batch from the %s entry without guessing recovery or posting', async entry => {
    const calls: { path: string; method?: string }[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => { calls.push({ path: String(input), method: init?.method }); return baseReply(String(input)) }))
    const wrapper = entry === 'batch' ? track(mount(QuestionAnswerBatchDrawer, { props: { groups: groups(), preferenceScope: 'ws1', preferences: preferences() } })) : track(mount(QuestionAnswerScheduleExecutionDetail, { props: { executionId: 'execution1', workspace: 'ws1', scheduleName: '隔离定时计划' } }))
    if (entry === 'batch') await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click')
    await flushPromises(); await wrapper.get('[data-testid="question-answer-queue-toggle"]').trigger('click')
    await wrapper.get('[data-testid="question-answer-queue-queued-row"] button').trigger('click'); await flushPromises()
    expect(wrapper.emitted('question-answer-view')?.at(-1)?.[0]).toMatchObject({ targetId: target, batchId })
    expect(calls.some(call => call.method === 'POST' || call.method === 'PUT')).toBe(false)
    expect(wrapper.text()).not.toContain('重试保存并结束')
  })

  it.each(['network', 'unreadable', 'storage'] as const)('continues the frozen browser plan after an unknown %s creation and never automatically posts it twice across close/reopen', async reason => {
    const posts: string[] = []; let release: (() => void) | undefined
    vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/question-answers/batches') && init?.method === 'POST') {
        posts.push(path)
        if (posts.length === 1) {
          await new Promise<void>(resolve => { release = resolve })
          if (reason === 'network') throw new Error('isolated connection lost')
          if (reason === 'unreadable') return new Response('unreadable-private-body', { status: 502 })
          return reply({ message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: '创建结果未确认：isolated commit timeout；请查看最近批次核对。', step: 'creating', operationResult: 'unknown' }, 503)
        }
        return reply({ ...batch(), batchId: 'second-batch' })
      }
      return baseReply(path)
    }))
    const wrapper = track(mount(QuestionAnswerBatchDrawer, { props: { groups: groups(), preferenceScope: 'ws1', preferences: preferences() } }))
    await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="question-answer-batch-start"]').trigger('click'); await flushPromises()
    expect(posts).toHaveLength(1)
    expect(wrapper.get('[data-testid="question-answer-batch-submitting"]').text()).toContain('入口账号a')
    expect(wrapper.get('[data-testid="question-answer-batch-waiting"]').text()).toContain('入口账号b')
    expect(wrapper.get('[data-testid="question-answer-batch-run-summary"]').text()).not.toMatch(/实际接受总量\s*0/)
    expect(wrapper.text()).toContain('刷新页面会丢失尚未提交的计划')
    await wrapper.get('[data-testid="question-answer-batch-close"]').trigger('click')
    release!(); await flushPromises(); expect(posts).toHaveLength(2)
    await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises()
    const outcome = wrapper.get(`[data-testid="question-answer-batch-outcome-${target}"]`)
    expect(outcome.text()).toContain('提交未确认'); expect(outcome.text()).toContain('不会自动重发')
    expect(wrapper.get('[data-testid="question-answer-batch-run-summary"]').text()).toContain('实际接受总量尚未确认')
    expect(outcome.text()).not.toContain('unreadable-private-body')
    await outcome.findAll('button').find(button => button.text() === '查看最近批次')!.trigger('click')
    expect(wrapper.emitted('question-answer-view')?.at(-1)).toEqual([{ targetId: target }])
    expect(posts).toHaveLength(2)
  })
})
