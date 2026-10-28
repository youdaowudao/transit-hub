// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import QuestionAnswerExecutionDetails from '@/modules/admin/components/dashboard/QuestionAnswerExecutionDetails.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import { ConnectionHealthApiError, getLatestQuestionAnswerBatch, getConnectionHealthOverview, setQuestionAnswerJudgment, startQuestionAnswerBatch, cancelQuestionAnswerBatch } from '@/modules/admin/api/connectionHealth'
import { questionAnswerReadableError } from '@/modules/admin/utils/questionAnswers'
import { t, te } from '@/locales'
import type { AdminGroupHealth, QuestionAnswerBatch, QuestionAnswerDiagnostics, QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'
import { createDefaultConnectionHealthPreferences } from '@/modules/admin/utils/connectionHealthPreferences'
import { questionAnswerFixtureStats, upgradeQuestionAnswerHistoryFixture } from './fixtures/c1QuestionAnswerHistory'

const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(wrapper: T): T => { wrappers.push(wrapper); return wrapper }
const diagnostics = (patch: Partial<QuestionAnswerDiagnostics> = {}): QuestionAnswerDiagnostics => ({
  version: 1, timeoutMs: 600000, selectedAt: '2026-10-22T01:00:01Z', requestStartedAt: '2026-10-22T01:00:02Z', requestFinishedAt: '2026-10-22T01:00:04Z', requestElapsedMs: 2000,
  requestWrittenAt: null, firstResponseByteAt: null, responseHeadersAt: null, lastReceivedAt: null, receivedBytes: 0, ...patch,
})
const record = (patch: Partial<QuestionAnswerRecord> = {}): QuestionAnswerRecord => ({
  id: 'diagnostics-record', targetId: 'sub2api:ws1:diagnostics-account', batchId: 'diagnostics-batch', modelName: 'diagnostics-model', questionId: 'diagnostics-question', questionName: '过程隔离题', questionBody: 'question', questionKeywordSnapshot: [],
  reasoningEffort: 'medium', requestProtocol: 'responses', answerBody: '', status: 'failed', errorType: 'network', answerJudgment: null, judgmentSource: null, repeatIndex: 1, manualError: false,
  createdAt: '2026-10-22T01:00:00Z', startedAt: '2026-10-22T01:00:01Z', completedAt: '2026-10-22T01:00:04Z', updatedAt: '2026-10-22T01:00:04Z', ...patch,
})
const batch = (records: QuestionAnswerRecord[]): QuestionAnswerBatch => ({ batchId: 'diagnostics-batch', records, active: records.some(r => r.status === 'running' || r.status === 'pending'), reasoningEffort: 'medium', repeatCount: 1, submittedCount: records.length, completedCount: records.filter(r => r.status !== 'pending' && r.status !== 'running').length, runningCount: records.filter(r => r.status === 'running').length, currentModel: '', currentQuestion: '', stats: questionAnswerFixtureStats(records) })
const response = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.unstubAllGlobals(); vi.restoreAllMocks(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = '' })

describe('A request diagnostics and direct causes', () => {
  it.each([true, false])('shows historical-name provenance beside the manual title even when records exist=%s', async hasRecords => {
    const r = record({ status: 'succeeded', errorType: '' }), current = batch(hasRecords ? [r] : [])
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path.endsWith('/models')) return response([])
      if (path.endsWith('/test-questions')) return response([])
      if (path.includes('/question-answers/history')) return response(upgradeQuestionAnswerHistoryFixture({ records: current.records, page: 1, totalItems: current.records.length, totalPages: 1, todayStats: current.stats }))
      if (path.includes('/question-answers/batches/')) return response(current)
      return response({ items: [] })
    }))
    const wrapper = track(mount(ManualOneTimeProbeDialog, { props: {
      open: false, target: { targetId: r.targetId, accountName: '', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] },
      initialQuestionAnswerBatchId: r.batchId, accountLabels: { [r.targetId]: { name: '当前补读名称', siteNames: [] } },
    }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
    await wrapper.setProps({ open: true }); await flushPromises()
    const title = wrapper.get('[role="dialog"] > div').text()
    expect(title).toContain('当前补读名称')
    expect(title).toContain('当时未记录账号名称，以下名称来自当前资料')
    expect(wrapper.findAll('details[open]')).toHaveLength(0)
    expect(wrapper.findAllComponents(QuestionAnswerExecutionDetails)).toHaveLength(hasRecords ? 1 : 0)
  })

  it.each([['已保存的当前名称', false], ['', false], ['已保存的当前名称', true], ['', true]] as const)('uses available names without website suffixes in manual errors and stop confirmation, with no cancel write: %s, dataFailure=%s', async (name, dataFailure) => {
    const r = record({ status: 'running', errorType: '', completedAt: null }), current = batch([r])
    const requests: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input); requests.push(path)
      if (path.endsWith('/models')) return response({ message: 'admin.connectionHealth.errors.modelsFetch' }, 500)
      if (path.endsWith('/test-questions')) return response([])
      if (dataFailure && (path.includes('/question-answers/history') || path.includes('/question-answers/batches/'))) return response({ message: 'admin.connectionHealth.errors.questionAnswerStorage' }, 500)
      if (path.includes('/question-answers/history')) return response(upgradeQuestionAnswerHistoryFixture({ records: [r], page: 1, totalItems: 1, totalPages: 1, todayStats: current.stats }))
      if (path.includes('/question-answers/batches/')) return response(current)
      return response({ items: [] })
    }))
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = track(mount(ManualOneTimeProbeDialog, { props: {
      open: false, target: { targetId: r.targetId, accountName: '', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] },
      initialQuestionAnswerBatchId: r.batchId, accountLabels: { [r.targetId]: { name, siteNames: ['当前网站'] } },
    }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
    await wrapper.setProps({ open: true }); await flushPromises()
    const expected = name || '账号（名称暂未读到）'
    expect(wrapper.get('[data-testid="question-answer-model-status"]').text()).toContain(expected)
    expect(wrapper.get('[data-testid="question-answer-model-status"]').text()).not.toContain('当前网站')
    if (dataFailure) {
      expect(wrapper.get('[data-testid="question-answer-stats-error"]').text()).toContain(expected)
      expect(wrapper.get('[data-testid="question-answer-stats-error"]').text()).not.toContain('当前网站')
      expect(confirm).not.toHaveBeenCalled()
      expect(requests.some(path => path.endsWith('/cancel'))).toBe(false)
      return
    }
    await wrapper.get('[data-testid="question-answer-stop-latest"]').trigger('click'); await flushPromises()
    expect(confirm).toHaveBeenCalledOnce()
    expect(confirm.mock.calls[0]![0]).toContain(expected)
    expect(confirm.mock.calls[0]![0]).not.toContain('当前网站')
    expect(requests.some(path => path.endsWith('/cancel'))).toBe(false)
  })

  it('keeps the known name while a batch with an empty raw name is still being submitted', async () => {
    const targetId = record().targetId
    let resolve!: (value: Response) => void
    const pending = new Promise<Response>(done => { resolve = done })
    const submitted: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path.endsWith('/test-questions')) return response([{ id: 'diagnostics-question', name: 'Q', body: 'question', enabled: true, isDefault: true, keywords: [] }])
      if (path.endsWith('/models')) return response([{ id: 'diagnostics-model', name: 'model', providerFamily: 'openai' }])
      if (path.endsWith('/question-answers/batches')) { submitted.push(path); return pending }
      return response({ items: [] })
    }))
    const groups = [{ id: 'g', name: 'G', accounts: [{ id: 'diagnostics-account', targetId, name: '', platform: 'openai', type: 'apikey', status: 'active', probeAvailable: true, modelHealth: [] }] }] as AdminGroupHealth[]
    const wrapper = track(mount(QuestionAnswerBatchDrawer, { props: { groups, accountLabels: { [targetId]: { name: '提交中的真实账号名', siteNames: [] } }, preferenceScope: 'diagnostics', preferences: { ...createDefaultConnectionHealthPreferences().questionAnswer, modelIds: ['diagnostics-model'], questionIds: ['diagnostics-question'], batchTargetIds: [targetId] } }, global: { stubs: { Teleport: true } } }))
    try {
      await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises()
      await wrapper.get('[data-testid="question-answer-batch-start"]').trigger('click'); await flushPromises()
      expect(wrapper.get('[data-testid="question-answer-batch-submitting"]').text()).toContain('提交中的真实账号名')
      expect(wrapper.get('[data-testid="question-answer-batch-submitting"]').text()).not.toContain('名称暂未读到')
      expect(submitted).toEqual([`/api/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches`])
    } finally { resolve(response(batch([]))); await flushPromises() }
  })

  it.each(['succeeded', 'failed', 'running', 'pending', 'cancelled'] as const)('passes the batch account name through every read-only %s record component', async status => {
    const r = record({ status, answerBody: status === 'succeeded' ? '保留的回答' : '', errorType: status === 'failed' ? 'network' : '' })
    vi.stubGlobal('fetch', vi.fn(async () => response(batch([r]))))
    const wrapper = track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: r.targetId, batchId: r.batchId, accountName: '批次中的原账号名', accountNameNote: '', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
    await flushPromises()
    const details = wrapper.findAllComponents(QuestionAnswerExecutionDetails)
    expect(details.length).toBeGreaterThan(0)
    for (const component of details) {
      expect(component.text()).toContain('批次中的原账号名')
      expect(component.text()).not.toContain('账号 diagnostics-account')
    }
    if (status === 'succeeded') expect(wrapper.text()).toContain('保留的回答')
  })

  it('uses an exact historical name in the manual dialog and switches back to current identity for another batch', async () => {
    const r = record({ status: 'succeeded', answerBody: '保留原回答', errorType: '', answerJudgment: 'unreviewed' })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path.includes('/models')) return response([{ id: 'diagnostics-model', name: 'model' }])
      if (path.endsWith('/test-questions')) return response([{ id: 'diagnostics-question', name: 'Q', body: 'question', enabled: true, isDefault: true, keywords: [] }])
      if (path.includes('/question-answers/history')) return response(upgradeQuestionAnswerHistoryFixture({ records: [r], page: 1, totalItems: 1, totalPages: 1, todayStats: batch([r]).stats }))
      if (path.endsWith('/batches/new-batch')) return response({ ...batch([{ ...r, batchId: 'new-batch' }]), batchId: 'new-batch' })
      if (path.includes('/question-answers/batches/')) return response(batch([r]))
      return response({ items: [], counts: { total: 0, open: 0, closed: 0, attention: 0, untested: 0 }, settings: {} })
    }))
    const wrapper = track(mount(ManualOneTimeProbeDialog, { props: {
      open: false, target: { targetId: r.targetId, accountName: '当前名称', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] },
      initialQuestionAnswerBatchId: r.batchId,
      accountLabels: { [r.targetId]: { name: '当前名称', siteNames: [] } },
      historyAccountLabel: { targetId: r.targetId, batchId: r.batchId, name: '执行当时名称', note: '' },
    }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
    await wrapper.setProps({ open: true }); await flushPromises()
    expect(wrapper.findComponent(QuestionAnswerExecutionDetails).text()).toContain('执行当时名称')
    const queue = wrapper.findComponent({ name: 'QuestionAnswerQueuePanel' })
    expect(queue.props('accounts')[r.targetId]).toBe('当前名称')
    queue.vm.$emit('view', { targetId: r.targetId, batchId: 'new-batch' }); await flushPromises()
    const detail = wrapper.findComponent(QuestionAnswerExecutionDetails)
    expect(detail.text()).toContain('当前名称')
    expect(detail.text()).not.toContain('执行当时名称')
    expect(wrapper.text()).toContain('保留原回答')
  })
  it('uses the supplied account name throughout request details and never relabels its target number as a name', () => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ targetId: 'sub2api:ws1:229' }), accountName: '完整请求账号229' } }))
    expect(wrapper.text()).toContain('完整请求账号229')
    expect(wrapper.text()).not.toContain('账号 229')
    expect(wrapper.text()).not.toContain('sub2api:ws1:229')
  })
  it.each([
    ['parse', 'invalid character', 'invalid_response'], ['receive', '1MiB读取上限', 'response_too_large'], ['parse', 'max_tokens输出上限', 'response_incomplete'], ['build', '账号类型不支持', 'account_type_unsupported'],
  ] as const)('keeps known local %s reason and never invents a network root cause', (failureStep, localError, errorType) => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ diagnostics: diagnostics({ failureStep, localError }), errorType }) } }))
    expect(wrapper.get('[data-testid="question-answer-direct-failure"]').text()).toContain(localError)
    expect(wrapper.text()).toContain('未取得可判题的完整回答')
    expect(wrapper.text()).not.toContain('目前不能确认是本机、网络还是对端中断')
    expect(wrapper.text()).not.toContain('重试保存并结束')
  })
  it('explains a starting failure without claiming the HTTP request was sent', () => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ diagnostics: diagnostics({ failureStep: 'starting', localError: 'statement timeout', requestStartedAt: null }), errorType: 'storage_error' }) } }))
    expect(wrapper.text()).toContain('这条题目没有调用答题接口')
    expect(wrapper.text()).not.toContain('上游失败')
    expect(wrapper.text()).not.toContain('重试保存并结束')
  })
  it('retains an actual network cause while leaving its origin unknown', () => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ diagnostics: diagnostics({ failureStep: 'request', localError: 'no such host' }) }) } }))
    expect(wrapper.text()).toContain('no such host')
    expect(wrapper.text()).toContain('目前不能确认是本机、网络还是对端中断')
    expect(wrapper.text()).toContain('手动重测可能重复计费')
    expect(wrapper.text()).not.toContain('对端已经收到')
  })
  it('does not infer balance, permissions or a particular gateway from HTTP status', () => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ upstreamStatus: 403, upstreamExcerpt: '', diagnostics: diagnostics() }) } }))
    expect(wrapper.text()).toContain('接口未返回具体原因')
    for (const forbidden of ['余额', '权限不足', 'Cloudflare', 'Nginx']) expect(wrapper.text()).not.toContain(forbidden)
  })
  it.each([
    ['auth', '请求被接口拒绝'], ['server_error', '请求被记录为接口错误'], ['timeout', '请求被记录为等待超时'],
  ])('keeps old %s classification neutral in the real read-only batch entry', async (errorType, label) => {
    const r = record({ errorType, diagnostics: null, upstreamStatus: errorType === 'auth' ? 403 : undefined })
    vi.stubGlobal('fetch', vi.fn(async () => response(batch([r]))))
    const wrapper = track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: r.targetId, batchId: r.batchId, accountName: '隔离账号', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
    await flushPromises()
    expect(wrapper.text()).toContain(label)
    expect(wrapper.text()).toContain('当时未记录网络过程')
    for (const forbidden of ['上游鉴权失败', '上游服务返回错误', '20 分钟', '10 分钟', '余额不足', '权限不足']) expect(wrapper.text()).not.toContain(forbidden)
    expect(wrapper.text()).not.toContain('0.0秒已到')
  })
  it('renders hostile details as plain text and preserves old or corrupt diagnostics', async () => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ diagnostics: diagnostics({ failureStep: 'parse', localError: '<img src=x onerror="throw 1">' }) }) } }))
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('<img')
    await wrapper.setProps({ record: record({ diagnostics: null }) })
    expect(wrapper.text()).toContain('当时未记录网络过程')
    expect(wrapper.text()).not.toContain('HTTP等待 0.0秒')
    await wrapper.setProps({ record: record({ diagnostics: null, diagnosticsUnavailable: true }) })
    expect(wrapper.text()).toContain('过程记录无法读取，答案和原状态仍保留')
  })
  it('shows the actual budget on local expiry and retains the previous interface explanation', () => {
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: record({ errorType: 'timeout', upstreamExcerpt: '接口已说明 socket hang up', diagnostics: diagnostics({ timeoutMs: 1200000 }) }) } }))
    expect(wrapper.text()).toContain('本地最长等待1200.0秒已到')
    expect(wrapper.text()).not.toContain('600.0秒已到')
  })
  it('keeps phase observations conservative and freezes server-relative HTTP time on stale data', async () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2030-01-01T00:00:00Z'))
    const d = diagnostics({ requestFinishedAt: null, requestElapsedMs: null, requestStartedAt: '2026-10-22T01:00:02Z' })
    const r = record({ status: 'running', errorType: '', diagnostics: null })
    const wrapper = track(mount(QuestionAnswerExecutionDetails, { props: { record: r, runtime: { recordId: r.id, step: 'request', diagnostics: d }, snapshotAt: '2026-10-22T01:00:12Z' } }))
    expect(wrapper.text()).toContain('HTTP等待 10.0秒')
    expect(wrapper.text()).not.toContain('HTTP已等待')
    expect(wrapper.text()).toContain('尚无请求写出完成的记录')
    await wrapper.setProps({ runtime: { recordId: r.id, step: 'request', diagnostics: { ...d, requestWrittenAt: '2026-10-22T01:00:03Z' } } })
    expect(wrapper.text()).toContain('这不能证明对端已经收到')
    await vi.advanceTimersByTimeAsync(3000)
    expect(wrapper.text()).toContain('HTTP等待 13.0秒')
    await wrapper.setProps({ stale: true })
    const stale = wrapper.text()
    await vi.advanceTimersByTimeAsync(200000)
    expect(wrapper.text()).toBe(stale)
    expect(wrapper.text()).not.toContain('HTTP已等待')
    await wrapper.setProps({ stale: false, runtime: { recordId: r.id, step: 'receiving', diagnostics: { ...d, responseHeadersAt: '2026-10-22T01:00:04Z', lastReceivedAt: '2026-10-22T01:00:10Z', receivedBytes: 8 } } })
    expect(wrapper.text()).toContain('内容可能是心跳，不代表答案已生成')
  })
})

describe('A dedicated API detail compatibility', () => {
  it('retains the original business key and HTTP status alongside safe operation detail', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ message: 'admin.connectionHealth.errors.questionAnswerActive', detail: '这个账号已有一批正在回答，请先查看最近批次。', step: 'preparing', operationResult: 'not_started' }, 409)))
    const error = await getLatestQuestionAnswerBatch('sub2api:ws1:a').catch(error => error)
    expect(error).toBeInstanceOf(ConnectionHealthApiError)
    expect(error).toMatchObject({ errorKey: 'admin.connectionHealth.errors.questionAnswerActive', status: 409, operationResult: 'not_started' })
    expect(error.message).toContain('这个账号已有一批正在回答')
    expect(questionAnswerReadableError(error.message, t, te)).toBe(error.message)
  })
  it('does not change another module when it happens to return detail', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ message: 'admin.connectionHealth.errors.request', detail: 'must stay unused', step: 'reading', operationResult: 'completed' }, 500)))
    const error = await getConnectionHealthOverview().catch(error => error)
    expect(error.message).toBe('admin.connectionHealth.errors.request')
    expect(error.operationResult).toBeUndefined()
    expect(error.step).toBeUndefined()
  })
  it('treats an unreadable answer-operation response as unknown without exposing its content', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('private-unreadable-body<bad>', { status: 502 })))
    const error = await getLatestQuestionAnswerBatch('sub2api:ws1:a').catch(error => error)
    expect(error.operationResult).toBe('unknown')
    expect(error.message).toContain('返回的答题内容无法读取')
    expect(error.message).not.toContain('private-unreadable-body')
    expect(error.message).not.toContain('已保存')
  })
  it.each(['create', 'read'])('treats a200/null %s response as unknown and keeps the original operation guidance', async operation => {
    const fetcher = vi.fn(async () => response(null))
    vi.stubGlobal('fetch', fetcher)
    const error = await (operation === 'create' ? startQuestionAnswerBatch(record().targetId, ['m'], ['q'], 'medium', 1) : getLatestQuestionAnswerBatch(record().targetId)).catch(error => error)
    expect(error.operationResult).toBe('unknown')
    expect(error.message).not.toContain('Cannot read')
    if (operation === 'create') { expect(error.message).toContain('无法确认'); expect(error.message).toContain('最近批次') }
    else { expect(error.message).toContain('重新读取'); expect(error.message).not.toContain('是否已提交') }
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
  it('reports an unconfirmed judgment without claiming it was saved', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: '保存这题的人工判断失败：statement timeout。目前无法确认是否已保存，请先重新读取。', step: 'judging', operationResult: 'unknown' }, 500)))
    const error = await setQuestionAnswerJudgment('sub2api:ws1:a', 'record', 'correct', '2026-10-22T01:00:00Z').catch(error => error)
    expect(error.operationResult).toBe('unknown')
    expect(error.message).toContain('先重新读取')
    expect(error.errorKey).toBe('admin.connectionHealth.errors.questionAnswerStorage')
  })
  it.each([
    ['questionAnswerActive', '这个账号已有正在进行的答题批次，请查看最近批次', '已有活动批次，已跳过'],
    ['questionAnswerSelection', '请先选择模型和题目', '请先选择模型和题目'],
    ['questionAnswerActive', '', '已有活动批次，已跳过'],
    ['questionAnswerSelection', '', '请至少选择一个模型和一个测试问题'],
  ])('preserves known %s rejection in the real batch drawer with separate display detail', async (key, detail, expected) => {
    const targetId = record().targetId
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path.endsWith('/test-questions')) return response([{ id: 'diagnostics-question', name: 'Q', body: 'question', enabled: true, isDefault: true, keywords: [] }])
      if (path.endsWith('/models')) return response([{ id: 'diagnostics-model', name: 'model', providerFamily: 'openai' }])
      if (path.endsWith('/question-answers/batches')) return response(detail ? { message: `admin.connectionHealth.errors.${key}`, detail, operationResult: 'not_started', step: 'preparing' } : { message: `admin.connectionHealth.errors.${key}` }, key === 'questionAnswerActive' ? 409 : 400)
      throw new Error('unexpected isolated route')
    }))
    const groups = [{ id: 'g', name: 'G', accounts: [{ id: 'diagnostics-account', targetId, name: 'A', platform: 'openai', type: 'apikey', status: 'active', probeAvailable: true, modelHealth: [] }] }] as AdminGroupHealth[]
    const wrapper = track(mount(QuestionAnswerBatchDrawer, { props: { groups, preferenceScope: 'diagnostics', preferences: { ...createDefaultConnectionHealthPreferences().questionAnswer, modelIds: ['diagnostics-model'], questionIds: ['diagnostics-question'], batchTargetIds: [targetId] } }, global: { stubs: { Teleport: true } } }))
    await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="question-answer-batch-start"]').trigger('click'); await flushPromises()
    const outcome = wrapper.get(`[data-testid="question-answer-batch-outcome-${targetId}"]`).text()
    expect(outcome).toContain(expected)
    if (key === 'questionAnswerActive') expect(outcome).not.toContain('提交失败')
    expect(outcome).not.toContain('admin.connectionHealth')
    expect(outcome).not.toContain('接口未提供更具体原因')
    expect(wrapper.emitted('question-answer-started')).toBeUndefined()
  })
  it.each([
    ['create-selection', 'questionAnswerSelection', 400, 'not_started'],
    ['create-active', 'questionAnswerActive', 409, 'not_started'],
    ['create-accounts', 'accountsFetch', 400, 'not_started'],
    ['create-storage400', 'questionAnswerStorage', 400, 'unknown'],
    ['create-storage500', 'questionAnswerStorage', 500, 'unknown'],
    ['create-unknown403', 'request', 403, 'unknown'],
    ['judge-conflict', 'questionAnswerJudgmentConflict', 409, 'not_started'],
  ] as const)('retains the known key and conservative legacy result for %s', async (operation, key, status, result) => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ message: `admin.connectionHealth.errors.${key}` }, status)))
    const error = await (operation.startsWith('judge') ? setQuestionAnswerJudgment(record().targetId, 'r', 'correct', record().updatedAt) : startQuestionAnswerBatch(record().targetId, ['m'], ['q'], 'medium', 1)).catch(error => error)
    expect(error.errorKey).toBe(`admin.connectionHealth.errors.${key}`)
    expect(error.operationResult).toBe(result)
    if (result === 'not_started') expect(error.message).not.toContain('是否已提交')
    else expect(error.message).toContain('无法确认')
    for (const falseCause of ['数据库损坏', '余额不足', '权限不足', '凭据过期']) expect(error.message).not.toContain(falseCause)
    expect(error.message).not.toContain('admin.connectionHealth')
  })
  it.each(['create', 'cancel', 'judge', 'read'])('keeps malformed legacy %s responses safe and operation-specific', async operation => {
    const fetcher = vi.fn(async () => new Response('untrusted-legacy-private-body', { status: 502 }))
    vi.stubGlobal('fetch', fetcher)
    const execute = operation === 'create' ? () => startQuestionAnswerBatch(record().targetId, ['m'], ['q'], 'medium', 1)
      : operation === 'cancel' ? () => cancelQuestionAnswerBatch(record().targetId, 'b')
        : operation === 'judge' ? () => setQuestionAnswerJudgment(record().targetId, 'r', 'correct', record().updatedAt)
          : () => getLatestQuestionAnswerBatch(record().targetId)
    const error = await execute().catch(error => error)
    expect(error.operationResult).toBe('unknown')
    expect(error.message).not.toContain('untrusted-legacy-private-body')
    if (operation === 'create') expect(error.message).toContain('最近批次')
    else expect(error.message).toContain('重新读取')
    if (operation === 'read') expect(error.message).not.toContain('是否已提交')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

it.each([409, 500])('re-reads after an unconfirmed judgment response HTTP %d without claiming it saved', async status => {
  let wrote = false, exactReads = 0
  const before = record({ status: 'succeeded', answerBody: 'saved answer', errorType: '', answerJudgment: 'unreviewed' })
  const after = { ...before, answerJudgment: 'incorrect' as const, judgmentSource: 'manual' as const }
  const detail = status === 409 ? '这条记录已被其他操作修改，请重新读取后再判断。' : '保存判断时发生statement timeout，目前无法确认是否已保存，请先重新读取。'
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (init?.method === 'PUT' && path.includes('/judgment')) { wrote = true; return response({ message: `admin.connectionHealth.errors.${status === 409 ? 'questionAnswerJudgmentConflict' : 'questionAnswerStorage'}`, detail, step: 'judging', operationResult: 'unknown' }, status) }
    if (path.includes('/models')) return response([{ id: 'diagnostics-model', name: 'model' }])
    if (path.endsWith('/test-questions')) return response([{ id: 'diagnostics-question', name: 'Q', body: 'question', enabled: true, isDefault: true, keywords: [] }])
    if (path.includes('/question-answers/history')) return response(upgradeQuestionAnswerHistoryFixture({ records: [wrote ? after : before], page: 1, totalItems: 1, totalPages: 1, todayStats: batch([wrote ? after : before]).stats }))
    if (path.endsWith('/question-answers/batches/diagnostics-batch')) exactReads++
    if (path.includes('/question-answers/batches/')) return response(batch([wrote ? after : before]))
    return response({ items: [], counts: { total: 0, open: 0, closed: 0, attention: 0, untested: 0 }, settings: {} })
  }))
  const wrapper = track(mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId: before.targetId, accountName: '过程隔离账号', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] } }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
  await wrapper.setProps({ open: true }); await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === '问答测试')!.trigger('click'); await flushPromises()
  await wrapper.get(`[data-testid="question-answer-record-${before.id}"]`).findAll('button').find(b => b.text() === '正确')!.trigger('click'); await flushPromises()
  expect(exactReads).toBeGreaterThan(0)
  expect(wrapper.text()).toContain(detail)
  expect(wrapper.text()).not.toContain('判定已保存')
  expect(wrapper.text()).toContain('错误 1')
  expect(wrapper.text()).not.toContain('admin.connectionHealth.errors.questionAnswerJudgmentConflict')
})

it('uses the same server-relative HTTP timing in the actual manual summary and process details', async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2030-01-01T00:00:00Z'))
  const r = record({ status: 'running', errorType: '', diagnostics: null, completedAt: null })
  const current = { ...batch([r]), runtime: { snapshotAt: '2026-10-22T01:00:12Z', items: [{ recordId: r.id, step: 'request', diagnostics: diagnostics({ requestFinishedAt: null, requestElapsedMs: null }) }] } }
  let failRead = false
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.includes('/models')) return response([{ id: 'diagnostics-model', name: 'model' }])
    if (path.endsWith('/test-questions')) return response([{ id: 'diagnostics-question', name: 'Q', body: 'question', enabled: true, isDefault: true, keywords: [] }])
    if (path.includes('/question-answers/history')) return response(upgradeQuestionAnswerHistoryFixture({ records: [r], page: 1, totalItems: 1, totalPages: 1, todayStats: current.stats }))
    if (path.includes('/question-answers/batches/')) return failRead ? response({ message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: '读取失败：statement timeout' }, 500) : response(current)
    // The existing managed model panel can perform read-only summary requests.
    return response({ items: [], counts: { total: 0, open: 0, closed: 0, attention: 0, untested: 0 }, settings: {} })
  }))
  const wrapper = track(mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId: r.targetId, accountName: '过程隔离账号', platform: 'sub2api', type: 'apikey', status: 'active', groupName: 'G', formalModels: [] } }, global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } } }))
  await wrapper.setProps({ open: true }); await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === '问答测试')!.trigger('click'); await flushPromises()
  expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).not.toContain('HTTP等待已超过')
  expect(wrapper.get('[data-testid="question-answer-active-processes"]').text()).toContain('HTTP等待 10.0秒')
  expect(wrapper.text()).not.toContain('HTTP已等待')
  failRead = true
  await vi.advanceTimersByTimeAsync(2100); await flushPromises()
  const before = wrapper.get('[data-testid="question-answer-active-processes"]').text()
  await vi.advanceTimersByTimeAsync(190000); await flushPromises()
  expect(wrapper.get('[data-testid="question-answer-active-processes"]').text()).toBe(before)
  expect(wrapper.text()).not.toContain('HTTP已等待')
})
