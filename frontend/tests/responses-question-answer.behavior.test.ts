// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import QuestionAnswerFailureEvidence from '@/modules/admin/components/dashboard/QuestionAnswerFailureEvidence.vue'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import type { QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'
import { questionAnswerFixtureStats, upgradeQuestionAnswerHistoryFixture } from './fixtures/c1QuestionAnswerHistory'

const api = vi.hoisted(() => ({
  discoverModels: vi.fn(), listTestQuestions: vi.fn(), getQuestionAnswerHistory: vi.fn(),
  getLatestQuestionAnswerBatch: vi.fn(), getQuestionAnswerBatch: vi.fn(),
  startQuestionAnswerBatch: vi.fn(), cancelQuestionAnswerBatch: vi.fn(), setQuestionAnswerJudgment: vi.fn(),
  runManualProbeOnce: vi.fn(), manualProbeTarget: vi.fn(),
}))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({
  ...await original<typeof import('@/modules/admin/api/connectionHealth')>(),
  ...Object.fromEntries(Object.entries(api).filter(([name]) => !['discoverModels', 'runManualProbeOnce', 'manualProbeTarget'].includes(name))),
}))
vi.mock('@/modules/admin/composables/useConnectionHealth', async original => ({
  ...await original<typeof import('@/modules/admin/composables/useConnectionHealth')>(),
  useConnectionHealth: () => ({ discoverModels: api.discoverModels, runManualProbeOnce: api.runManualProbeOnce, manualProbeTarget: api.manualProbeTarget, errorKey: { value: '' } }),
}))

const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(wrapper: T): T => { wrappers.push(wrapper); return wrapper }
const record = (patch: Partial<QuestionAnswerRecord> = {}): QuestionAnswerRecord => ({
  id: 'responses-isolated-record', targetId: 'sub2api:ws1:responses-isolated', batchId: 'responses-isolated-batch',
  modelName: 'responses-isolated-model', questionId: 'responses-isolated-question', questionName: 'Responses隔离题', questionBody: '隔离问题', questionKeywordSnapshot: [],
  reasoningEffort: 'medium', requestProtocol: 'responses', answerBody: '', status: 'failed', errorType: 'server_error', answerJudgment: null, manualError: false,
  createdAt: '2026-10-22T01:00:00Z', startedAt: '2026-10-22T01:00:01Z', completedAt: '2026-10-22T01:00:03.500Z', updatedAt: '2026-10-22T01:00:03.500Z',
  upstreamStatus: 200, upstreamExcerpt: '接收回答失败：specific tail failure；响应编号：resp_isolated；最后事件：response.failed；本题不判对错。', ...patch,
})
const batch = (records = [record()]) => ({
  batchId: records[0]!.batchId, records, active: false, reasoningEffort: 'medium' as const, repeatCount: 1,
  submittedCount: records.length, completedCount: records.length, runningCount: 0, stats: questionAnswerFixtureStats(records),
})
const evidence = (r = record()) => track(mount(QuestionAnswerFailureEvidence, { props: { record: r } }))
const findButton = (wrapper: VueWrapper, text: string) => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().includes(text))
  if (!button) throw new Error(`missing button ${text}`)
  return button
}
const assertNoWrites = () => {
  for (const name of ['startQuestionAnswerBatch', 'cancelQuestionAnswerBatch', 'setQuestionAnswerJudgment', 'runManualProbeOnce', 'manualProbeTarget'] as const) expect(api[name]).not.toHaveBeenCalled()
}
beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  api.discoverModels.mockResolvedValue({ models: [{ id: 'responses-isolated-model', name: 'Responses模型' }] })
  api.listTestQuestions.mockResolvedValue([{ id: 'responses-isolated-question', name: 'Responses隔离题', body: '隔离问题', enabled: true, isDefault: true, keywords: [] }])
  api.getQuestionAnswerBatch.mockResolvedValue(batch())
  api.getLatestQuestionAnswerBatch.mockResolvedValue(batch())
  const stats = questionAnswerFixtureStats([record()])
  api.getQuestionAnswerHistory.mockResolvedValue(upgradeQuestionAnswerHistoryFixture({ records: [record()], page: 1, totalItems: 1, totalPages: 1, todayStats: stats }))
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('unexpected real request in isolated Responses test'))))
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = ''
})

describe('Responses failure details and existing timestamps (T13/T15)', () => {
  it('shows the saved specific cause, reference, status, start and one-decimal elapsed time', () => {
    const wrapper = evidence()
    expect(wrapper.text()).toContain('specific tail failure')
    expect(wrapper.text()).toContain('resp_isolated')
    expect(wrapper.text()).toContain('200')
    expect(wrapper.text()).toContain('开始时间')
    expect(wrapper.text()).toMatch(/耗时[：:\s]*2\.5\s*秒/)
    expect(wrapper.text()).toContain('失败详情')
    expect(wrapper.text()).not.toContain('上游原话（')
    expect(wrapper.findAll('button')).toHaveLength(0)
    assertNoWrites()
  })
  it.each([
    [null, null], [null, '2026-10-22T01:00:03Z'], ['2026-10-22T01:00:01Z', null],
    ['invalid', '2026-10-22T01:00:03Z'], ['2026-10-22T01:00:01Z', 'invalid'],
    ['2026-10-22T01:00:03Z', '2026-10-22T01:00:01Z'],
  ])('does not estimate elapsed time for invalid/missing/reversed time: %s, %s', (startedAt, completedAt) => {
    const wrapper = evidence(record({ startedAt, completedAt }))
    expect(wrapper.text()).toMatch(/耗时[：:\s]*未记录/)
    expect(wrapper.text()).not.toMatch(/耗时[：:\s]*0(?:\.0)?\s*秒/)
    if (!startedAt || startedAt === 'invalid') expect(wrapper.text()).toMatch(/开始时间[：:\s]*未记录/)
  })
  it('allows an actual recorded zero duration', () => {
    expect(evidence(record({ completedAt: '2026-10-22T01:00:01Z' })).text()).toMatch(/耗时[：:\s]*0\.0\s*秒/)
  })
  it.each(['account_type_unsupported', 'storage_error', 'service_shutdown', 'service_restarted', 'internal_error'])('keeps the known local cause neutral when no extra details exist: %s', errorType => {
    const wrapper = evidence(record({ errorType, upstreamStatus: null, upstreamExcerpt: '' }))
    expect(wrapper.get('[data-testid="question-answer-failure-evidence"]').text()).toContain('没有保存额外详情')
    expect(wrapper.text()).not.toContain('原因未知')
    expect(wrapper.text()).not.toContain('具体原因无法确认')
    expect(wrapper.text()).not.toContain('请上游核查这一次')
    expect(wrapper.text()).toContain('上方')
  })
  it.each(['succeeded', 'cancelled', 'running', 'pending'] as const)('shows no failure evidence for %s even with stale metadata', status => {
    expect(evidence(record({ status })).find('[data-testid="question-answer-failure-evidence"]').exists()).toBe(false)
  })
  it('renders old 500-character text as text and keeps the shared incomplete message protocol-aware', () => {
    const old = '旧'.repeat(490) + '<b>旧文</b>'
    const wrapper = evidence(record({ errorType: 'response_incomplete', upstreamExcerpt: old }))
    expect(wrapper.text()).toContain(old)
    expect(wrapper.find('b').exists()).toBe(false)
    expect(wrapper.text()).toContain('Responses的实际输出上限以上游为准')
    expect(wrapper.text()).not.toContain('通常是题目难')
  })
})

describe('Responses details in real manual/history and schedule parents (T15/T16)', () => {
  const manual = async (initialQuestionAnswerBatchId: string | null = null) => {
    const wrapper = track(mount(ManualOneTimeProbeDialog, {
      props: { open: false, target: { targetId: record().targetId, accountName: 'Responses隔离账号', platform: 'sub2api', type: 'apikey', status: 'active', groupName: '隔离组', formalModels: [] }, initialQuestionAnswerBatchId, initialQuestionAnswerExpandFailed: true },
      global: { stubs: { Teleport: true, Transition: false, QuestionAnswerModelControlPanel: true } },
    }))
    await wrapper.setProps({ open: true }); await flushPromises()
    await findButton(wrapper, '问答测试').trigger('click'); await flushPromises()
    return wrapper
  }
  it.each([null, 'responses-isolated-batch'])('keeps current/history or specified batch failure expansion and details read-only: %s', async initialBatch => {
    const wrapper = await manual(initialBatch)
    if (!wrapper.find('[data-testid="question-answer-failed-content"]').exists()) await findButton(wrapper, '失败').trigger('click')
    await flushPromises()
    const details = wrapper.findComponent(QuestionAnswerFailureEvidence)
    expect(details.text()).toContain('specific tail failure')
    expect(details.text()).toMatch(/耗时[：:\s]*2\.5\s*秒/)
    const toggle = findButton(wrapper, '失败')
    await toggle.trigger('click'); expect(wrapper.find('[data-testid="question-answer-failed-content"]').exists()).toBe(false)
    await toggle.trigger('click'); expect(wrapper.text()).toContain('resp_isolated')
    assertNoWrites()
  })
  it('preserves saved failure evidence when models cannot be read after the neighbor fix', async () => {
    api.discoverModels.mockResolvedValue({ errorKey: 'admin.upstream.errors.upstreamInsufficientBalance' })
    const wrapper = await manual('responses-isolated-batch')
    expect(wrapper.text()).toContain('specific tail failure')
    expect(wrapper.text()).toContain('上游账户余额不足')
    expect(findButton(wrapper, '开始回答').attributes('disabled')).toBeDefined()
    expect(wrapper.findComponent(QuestionAnswerFailureEvidence).text()).toMatch(/耗时[：:\s]*2\.5\s*秒/)
    assertNoWrites()
  })
  it('keeps saved details available when configuration blocks new questions', async () => {
    const wrapper = await manual('responses-isolated-batch')
    await wrapper.setProps({ target: { ...wrapper.props('target'), testConfiguration: { status: 'conflict', sourceGroups: [], blockedReason: 'admin.connectionHealth.errors.testConfigurationConflict' } } })
    await flushPromises()
    expect(wrapper.text()).toContain('specific tail failure')
    expect(findButton(wrapper, '开始回答').attributes('disabled')).toBeDefined()
    assertNoWrites()
  })
  it('keeps schedule fallback details, refresh, close and stale-workspace protection without inference requests', async () => {
    const wrapper = track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: record().targetId, batchId: record().batchId, accountName: '已不在列表的隔离账号', workspace: 'ws1' }, global: { stubs: { Teleport: true } } }))
    await flushPromises()
    expect(wrapper.text()).toContain('只能查看')
    expect(wrapper.text()).toContain('specific tail failure')
    expect(wrapper.text()).toMatch(/耗时[：:\s]*2\.5\s*秒/)
    await findButton(wrapper, '刷新').trigger('click'); await flushPromises()
    expect(api.getQuestionAnswerBatch).toHaveBeenCalledTimes(2)
    let resolve!: (value: ReturnType<typeof batch>) => void
    api.getQuestionAnswerBatch.mockReturnValueOnce(new Promise<ReturnType<typeof batch>>(done => { resolve = done }))
    await findButton(wrapper, '刷新').trigger('click'); await flushPromises()
    await wrapper.setProps({ workspace: 'ws2', targetId: 'sub2api:ws2:other', batchId: 'responses-other-batch' }); await flushPromises()
    resolve(batch([record({ upstreamExcerpt: 'stale workspace result must never appear' })])); await flushPromises()
    expect(wrapper.text()).not.toContain('stale workspace result must never appear')
    await wrapper.get('[aria-label="关闭批次"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(api.discoverModels).not.toHaveBeenCalled()
    assertNoWrites()
  })
})
