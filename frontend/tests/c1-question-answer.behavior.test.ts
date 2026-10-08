// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import QuestionAnswerStatsBar from '@/modules/admin/components/dashboard/QuestionAnswerStatsBar.vue'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import type { AdminGroupHealth, QuestionAnswerRecord, QuestionAnswerBatch, QuestionAnswerHistory } from '@/modules/admin/types/connectionHealth'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'

const workspace = vi.hoisted(() => ({ current: null as any }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn() }) }))
vi.mock('@vueuse/core', async () => { const { ref } = await import('vue'); return { useDocumentVisibility: () => ref('hidden'), useIntervalFn: () => ({ pause: vi.fn(), resume: vi.fn() }) } })
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { workspace.current = (await import('vue')).ref({ id: 'c1-workspace', displayName: 'C1 workspace', platform: 'sub2api' }); return { useAdminAccounts: () => ({ currentAccount: workspace.current }) } })

const targetId = 'sub2api:c1-workspace:1'
const now = '2026-10-07T10:00:00Z'
const record = (overrides: Partial<QuestionAnswerRecord> = {}): QuestionAnswerRecord => ({ id: 'r1', targetId, batchId: 'old-batch', modelName: 'model-a', questionId: 'q', questionName: '关键词题', questionBody: '只回答 hit', questionKeywordSnapshot: ['hit'], reasoningEffort: 'high', requestProtocol: 'responses', repeatIndex: 1, answerBody: 'HIT', status: 'succeeded', errorType: '', answerJudgment: 'correct', judgmentSource: 'automatic', manualError: false, createdAt: now, startedAt: now, completedAt: now, updatedAt: now, ...overrides })
const batch = (id: string, records: QuestionAnswerRecord[]): QuestionAnswerBatch => ({ batchId: id, records: records.map(item => ({ ...item, batchId: id })), reasoningEffort: 'high', repeatCount: Math.max(1, ...records.map(item => item.repeatIndex ?? 1)), submittedCount: records.length, completedCount: records.filter(item => !['running', 'pending'].includes(item.status)).length, runningCount: records.filter(item => item.status === 'running').length, active: records.some(item => ['running', 'pending'].includes(item.status)), currentModel: '', currentQuestion: '', stats: questionAnswerFixtureStats(records) })
const groups = (): AdminGroupHealth[] => ['one', 'two'].map(id => ({ id, name: id, platform: 'openai', status: 'active', type: 'standard', isExclusive: false, subscriptionType: '', multiplier: 1, multiplierDisplay: '1x', accountCount: 1, healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null }, accounts: [{ id: '1', name: 'C1 account', platform: 'openai', type: 'subscription', status: 'active', schedulable: true, targetId, probeAvailable: true, modelHealth: [], todayQuestionAnswerSubmitted: 9, todayQuestionAnswerCorrect: 0, todayQuestionAnswerJudged: 9, priority: 15, accountTier: 1 }] }))
let stored: Map<string, QuestionAnswerBatch>
let latestId: string
let reads: string[]
let writes: Array<{ url: string; body: any }>
let failHistory: boolean
let conflict: boolean
let failExact: boolean
let exactOverride: (() => Promise<Response>) | undefined
let historyOverride: (() => Promise<Response>) | undefined
let summaryOverride: (() => Promise<Response>) | undefined
let summaryCalls: number

// SPEC C2 §12.5 replaces ratio wording while retaining the original raw judgment counts.
const expectAccuracyCounts = (period: VueWrapper, correct: number, judged: number) => {
  expect(period.get('[data-testid="question-answer-accuracy"]').text()).toBe(judged ? `${Number((correct / judged * 100).toFixed(1))}%` : '—')
  const counts = period.get('dl').findAll('dd')
  expect(counts[4]!.text()).toBe(String(correct))
  expect(counts[5]!.text()).toBe(String(judged - correct))
}

const wrappers: VueWrapper[] = []
const service = useConnectionHealth()
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const history = (): QuestionAnswerHistory => {
  const records = [...stored.values()].flatMap(item => item.records)
  const stats = questionAnswerFixtureStats(records)
  return { batches: [...stored.values()].map(item => ({ batchId: item.batchId, createdAt: now, startedAt: now, completedAt: item.active ? null : now, requestProtocol: 'responses', reasoningEffort: 'high', models: ['model-a'], questions: item.stats.byQuestion, repeatCount: item.repeatCount, active: item.active, stats: item.stats })), page: 1, pageSize: 20, totalBatches: stored.size, totalPages: 1, todayStats: stats, allTimeStats: stats }
}
beforeEach(() => {
  vi.useFakeTimers({ now: new Date(now) })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  workspace.current.value = { id: 'c1-workspace', displayName: 'C1 workspace', platform: 'sub2api' }
  service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('c1-workspace')
  stored = new Map([['old-batch', batch('old-batch', [record()])]])
  latestId = 'old-batch'; reads = []; writes = []; failHistory = false; conflict = false; failExact = false; exactOverride = undefined; historyOverride = undefined; summaryOverride = undefined; summaryCalls = 0
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = decodeURIComponent(String(input)); reads.push(url)
    if (url.endsWith('/admin-groups')) return json(groups())
    if (url.endsWith('/test-questions')) return json([{ id: 'q', name: '关键词题', body: '只回答 hit', keywords: ['hit'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }])
    if (url.endsWith('/models')) return json([{ id: 'model-a', name: 'model-a' }])
    if (url.includes('/question-answers/history')) return historyOverride ? historyOverride() : failHistory ? json({ message: 'admin.connectionHealth.errors.request' }, 500) : json(history())
    if (url.endsWith('/question-answers/summary')) { summaryCalls++; return summaryOverride ? summaryOverride() : json({ targetId, todayStats: { requests: history().todayStats.requests, reviews: history().todayStats.reviews } }) }
    if (url.endsWith('/batches/latest')) return json(stored.get(latestId))
    if (url.endsWith('/judgment')) {
      const body = JSON.parse(String(init?.body)); writes.push({ url, body })
      const recordId = url.split('/records/')[1]!.split('/')[0]!
      const entry = [...stored.values()].find(item => item.records.some(row => row.id === recordId))!
      const previous = entry.records.find(row => row.id === recordId)!
      if (conflict) { const next = { ...previous, answerJudgment: 'incorrect' as const, judgmentSource: 'manual' as const, updatedAt: '2026-10-07T10:00:01Z' }; stored.set(entry.batchId, batch(entry.batchId, entry.records.map(row => row.id === recordId ? next : row))); return json({ message: 'admin.connectionHealth.errors.questionAnswerJudgmentConflict' }, 409) }
      const next = { ...previous, answerJudgment: body.judgment, judgmentSource: 'manual', manualError: body.judgment === 'incorrect', updatedAt: '2026-10-07T10:00:01Z' } as QuestionAnswerRecord
      stored.set(entry.batchId, batch(entry.batchId, entry.records.map(row => row.id === recordId ? next : row)))
      return json(next)
    }
    if (url.endsWith('/cancel')) { const id = url.split('/batches/')[1]!.split('/')[0]!; const entry = stored.get(id)!; const stopped = batch(id, entry.records.map(row => ['pending', 'running'].includes(row.status) ? { ...row, status: 'cancelled', completedAt: now } : row)); stored.set(id, stopped); return json(stopped) }
    if (url.includes('/question-answers/batches/')) return exactOverride ? exactOverride() : failExact ? json({ message: 'admin.connectionHealth.errors.request' }, 500) : json(stored.get(url.split('/batches/')[1]!))
    return json([])
  }))
})
afterEach(async () => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount()); service.setAdminGroupsWorkspace(''); await flushPromises(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); document.body.innerHTML = ''
})
const dialog = async (initialId: string | null = null) => {
  const wrapper = mount(ManualOneTimeProbeDialog, { props: { open: false, initialQuestionAnswerBatchId: initialId, target: { targetId, accountName: 'C1 account', platform: 'openai', type: 'subscription', status: 'active', groupName: 'one', formalModels: [] } }, global: { stubs: { Teleport: true, Transition: false } } })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises(); return wrapper
}
const expandGroups = async (wrapper: VueWrapper) => {
  for (const group of wrapper.findAll('[data-testid="question-answer-result-group"]')) if (group.get('button').attributes('aria-expanded') !== 'true') await group.get('button').trigger('click')
  await flushPromises()
}
const view = async () => {
  const wrapper = mount(ConnectionHealthView, { global: { stubs: { Teleport: true, Transition: false } } }); wrappers.push(wrapper); await flushPromises(); return wrapper
}

describe('C1 frontend complete behavior', () => {
  it('opens exact historical ID while stop and its confirmation refer only to latest running ID', async () => {
    stored.set('new-runtime', batch('new-runtime', [record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })])); latestId = 'new-runtime'
    const wrapper = await dialog('old-batch')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain('#old-batc')
    expect(wrapper.get('[data-testid="question-answer-latest-runtime"]').text()).toContain('#new-runt')
    expect(reads.some(url => url.endsWith('/batches/old-batch'))).toBe(true)
    await wrapper.get('[data-testid="question-answer-stop-latest"]').trigger('click'); await flushPromises()
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('new-runtime'))
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('C1 account'))
    expect(reads.some(url => url.endsWith('/batches/new-runtime/cancel'))).toBe(true)
    expect(reads.some(url => url.endsWith('/batches/old-batch/cancel'))).toBe(false)
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain('#old-batc')
    expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(false)
  })
  it('renders every repeat, source and snapshot with all-success highlight, while failures and cancellations have no judgment controls', async () => {
    const records = [record(), record({ id: 'r2', repeatIndex: 2, answerBody: 'no hit here', answerJudgment: 'incorrect' }), record({ id: 'r3', repeatIndex: 3, judgmentSource: 'manual' }), record({ id: 'legacy', repeatIndex: null, judgmentSource: null }), record({ id: 'manual-only', questionId: 'manual', questionName: '人工题', questionKeywordSnapshot: [], answerJudgment: 'unreviewed', judgmentSource: null }), record({ id: 'failed', status: 'failed', answerJudgment: null, judgmentSource: null, errorType: 'network' }), record({ id: 'cancelled', status: 'cancelled', answerJudgment: null, judgmentSource: null })]
    stored.set('old-batch', batch('old-batch', records)); const wrapper = await dialog()
    expect(wrapper.get('[data-testid="question-answer-processed-content"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid^="question-answer-record-r"]').length).toBe(0)
    await expandGroups(wrapper)
    expect(wrapper.text()).toContain('第1次'); expect(wrapper.text()).toContain('第2次'); expect(wrapper.text()).toContain('第3次'); expect(wrapper.text()).toContain('历史样本')
    expect(wrapper.text()).toContain('自动判定·正确'); expect(wrapper.text()).toContain('人工判定·正确'); expect(wrapper.text()).toContain('历史判定·正确')
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').findAll('mark')).toHaveLength(1)
    expect(wrapper.get('[data-testid="question-answer-record-manual-only"]').text()).toContain('仅人工判断')
    expect(wrapper.find('[data-testid="question-answer-record-failed"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="question-answer-record-cancelled"]').exists()).toBe(false)
  })
  it('keeps an empty successful answer automatically incorrect without highlighting placeholder text', async () => {
    stored.set('old-batch', batch('old-batch', [record({ answerBody: '', questionKeywordSnapshot: ['回答'], answerJudgment: 'incorrect' })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    const card = wrapper.get('[data-testid="question-answer-record-r1"]')
    expect(card.text()).toContain('没有回答正文。')
    expect(card.text()).toContain('自动判定·错误')
    expect(card.findAll('mark')).toHaveLength(0)
    expect(card.findAll('button').find(button => button.text() === '错误')?.attributes('aria-pressed')).toBe('true')
    expect(card.text()).not.toContain('自动判定·正确')
  })
  it('keeps same-value manual confirmation and both changed judgments authoritative when follow-up reads fail', async () => {
    const wrapper = await dialog(); await expandGroups(wrapper)
    failHistory = true
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(writes[0]!.body).toEqual({ judgment: 'correct', expectedUpdatedAt: now })
    expect(wrapper.text()).toContain('人工判定·正确'); expect(wrapper.text()).toContain('判定已保存，统计刷新失败')
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-review"]'), 1, 1)
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[1]!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('人工判定·错误'); expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-review"]'), 0, 1)
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('人工判定·正确'); expect(wrapper.emitted('question-answer-stats-dirty')).toHaveLength(3)
  })
  it('refreshes the authoritative exact record on version conflict without retrying mutation', async () => {
    const wrapper = await dialog(); await expandGroups(wrapper); conflict = true
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(writes).toHaveLength(1); expect(wrapper.text()).toContain('人工判定·错误'); expect(wrapper.text()).toContain('记录已变化')
  })
  it('history shows full batch summaries only, supports today/all and fetches full answers by exact click', async () => {
    stored.set('old-batch', batch('old-batch', Array.from({ length: 50 }, (_, index) => record({ id: `r${index}`, repeatIndex: index % 10 + 1, modelName: `model-${Math.floor(index / 10)}` })))); const wrapper = await dialog()
    const historySection = wrapper.get('[data-testid="question-answer-history"]'); await historySection.get('button').trigger('click'); await flushPromises()
    expect(historySection.findAll('[data-testid="question-answer-history-batch"]')).toHaveLength(1)
    expect(historySection.text()).toContain('提交 50'); expect(historySection.findAll('mark')).toHaveLength(0); expect(historySection.text()).not.toContain('HIT')
    const all = historySection.findAll('button').find(button => button.text() === '全部')!; await all.trigger('click'); await flushPromises()
    expect(reads.some(url => url.includes('scope=all'))).toBe(true)
    const exact = historySection.findAll('button').find(button => button.text() === '查看该批次')!; await exact.trigger('click'); await flushPromises(); await expandGroups(wrapper)
    expect(wrapper.findAll('[data-testid^="question-answer-record-r"]')).toHaveLength(50)
  })
  it('supports explicit local summary retry, pending changes, stale workspace discard and updates shared account projections', async () => {
    const wrapper = await view(); const child = wrapper.getComponent(ManualOneTimeProbeDialog)
    const externalReadCount = () => reads.filter(url => /manual-probe|\/targets\/[^/]+\/probe|admin-groups\/refresh/.test(url)).length
    const initialExternalReadCount = externalReadCount()
    summaryOverride = async () => json({ message: 'admin.connectionHealth.errors.request' }, 500)
    child.vm.$emit('question-answer-stats-dirty', targetId); await flushPromises(); expect(summaryCalls).toBe(1)
    summaryOverride = undefined; child.vm.$emit('question-answer-stats-retry', targetId); await flushPromises(); expect(summaryCalls).toBe(2)
    expect(service.adminGroups.value.every(group => group.accounts[0]!.todayQuestionAnswerJudged === 1)).toBe(true)
    let resolve!: (value: Response) => void
    summaryOverride = () => new Promise(done => { resolve = done })
    child.vm.$emit('question-answer-stats-dirty', targetId); await flushPromises(); child.vm.$emit('question-answer-stats-dirty', targetId)
    summaryOverride = undefined; resolve(json({ targetId, todayStats: { requests: { submitted: 99 }, reviews: { correct: 99, incorrect: 0 } } })); await flushPromises()
    expect(summaryCalls).toBe(4); expect(service.adminGroups.value.every(group => group.accounts[0]!.todayQuestionAnswerCorrect === 1)).toBe(true)
    expect(externalReadCount()).toBe(initialExternalReadCount)
    summaryOverride = () => new Promise(done => { resolve = done }); child.vm.$emit('question-answer-stats-dirty', targetId); await flushPromises()
    workspace.current.value = { id: 'other', displayName: 'Other' }; await flushPromises(); resolve(json({ targetId, todayStats: { requests: { submitted: 99 }, reviews: { correct: 99, incorrect: 0 } } })); await flushPromises()
    expect(service.adminGroups.value.every(group => group.accounts[0]!.todayQuestionAnswerCorrect !== 99)).toBe(true)
  })
  it('exposes counts and each model/question dimension while no judged samples stay empty', async () => {
    const stats = questionAnswerFixtureStats([record({ answerJudgment: 'unreviewed', judgmentSource: null })])
    const wrapper = mount(QuestionAnswerStatsBar, { props: { reviewStats: stats, todayStats: stats, lifetimeStats: stats } }); wrappers.push(wrapper)
    expect(wrapper.findAll('[data-testid="question-answer-accuracy"]').every(node => node.text() === '—')).toBe(true)
    await wrapper.findAll('button')[2]!.trigger('click'); expect(wrapper.findAll('[data-testid="question-answer-question-stats"]')).toHaveLength(3); expect(wrapper.text()).toContain(`#${stats.byQuestion[0]!.questionSnapshotKey.slice(0, 8)}`)
    expect(wrapper.text()).toContain('混合配置汇总'); expect(wrapper.text()).not.toContain('版本1')
  })
  it('shows initial read failure and retry, and preserves safe long-answer text with narrow-screen action classes', async () => {
    failHistory = true; const wrapper = await dialog(); expect(wrapper.get('[data-testid="question-answer-stats-error"]').text()).toContain('重新加载')
    failHistory = false; stored.set('old-batch', batch('old-batch', [record({ answerBody: `HIT<script>${'换行\n长答案。'.repeat(100)}` })])); await wrapper.get('[data-testid="question-answer-stats-error"] button').trigger('click'); await flushPromises(); await expandGroups(wrapper)
    const card = wrapper.get('[data-testid="question-answer-record-r1"]'); expect(card.find('script').exists()).toBe(false); expect(card.text()).not.toContain('长答案。'.repeat(100))
    const expand = card.findAll('button').find(button => button.text() === '展开详情')!; await expand.trigger('click'); expect(card.text()).toContain(stored.get('old-batch')!.records[0]!.answerBody)
    expect(card.find('p.whitespace-pre-wrap').exists()).toBe(true); expect(card.get('.grid.grid-cols-2').classes()).toContain('md:grid-cols-1')
    const footer = wrapper.get('[data-testid="question-answer-footer"]')
    expect(footer.get('p').classes()).toEqual(expect.arrayContaining(['min-w-0', 'flex-1']))
    expect(footer.findAll('button').every(button => button.classes().includes('whitespace-nowrap'))).toBe(true)
    expect(footer.get('button').element.parentElement?.classList.contains('shrink-0')).toBe(true)
  })
  it('rejects a late conflict read after runtime cancellation', async () => {
    stored.set('old-batch', batch('old-batch', [record(), record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    let resolve!: (response: Response) => void
    let snapshot!: QuestionAnswerBatch
    exactOverride = () => { snapshot = structuredClone(stored.get('old-batch')!); exactOverride = undefined; return new Promise(done => { resolve = done }) }
    conflict = true
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(resolve).toBeTypeOf('function')
    await wrapper.get('[data-testid="question-answer-stop-latest"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(false)
    resolve(json(snapshot)); await flushPromises()
    expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('最新运行批次')
  })
  it('rejects a late conflict read that predates another saved record judgment', async () => {
    stored.set('old-batch', batch('old-batch', [record(), record({ id: 'r2', repeatIndex: 2 })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    let resolve!: (response: Response) => void
    let snapshot!: QuestionAnswerBatch
    exactOverride = () => { snapshot = structuredClone(stored.get('old-batch')!); exactOverride = undefined; return new Promise(done => { resolve = done }) }
    conflict = true
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(resolve).toBeTypeOf('function')
    conflict = false
    await wrapper.get('[data-testid="question-answer-record-r2"]').findAll('button')[1]!.trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').text()).toContain('人工判定·错误')
    resolve(json(snapshot)); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').text()).toContain('人工判定·错误')
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').text()).not.toContain('自动判定·正确')
  })
  it('does not apply an old record delta to a newer history aggregation on idempotent save', async () => {
    const wrapper = await dialog(); await expandGroups(wrapper)
    stored.set('old-batch', batch('old-batch', [record({ answerJudgment: 'incorrect', judgmentSource: 'manual', manualError: true, updatedAt: '2026-10-07T10:00:01Z' })]))
    const section = wrapper.get('[data-testid="question-answer-history"]')
    await section.get('button').trigger('click'); await flushPromises()
    await section.findAll('button').find(button => button.text() === '全部')!.trigger('click'); await flushPromises()
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-today"]'), 0, 1)
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).toContain('自动判定·正确')
    failExact = true; failHistory = true
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[1]!.trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).toContain('人工判定·错误')
    for (const period of ['review', 'today', 'lifetime']) {
      expectAccuracyCounts(wrapper.get(`[data-testid="question-answer-stats-${period}"]`), 0, 1)
      expect(wrapper.get(`[data-testid="question-answer-stats-${period}"]`).text()).not.toContain('-100%')
    }
  })
  it.each(['exact', 'history'] as const)('actually reloads failed local %s reads and clears the saved-judgment warning', async failure => {
    const wrapper = await dialog(); await expandGroups(wrapper)
    failExact = failure === 'exact'; failHistory = failure === 'history'
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('判定已保存，统计刷新失败')
    const before = reads.filter(url => url.includes(failure === 'exact' ? '/question-answers/batches/old-batch' : '/question-answers/history')).length
    failExact = false; failHistory = false
    const retry = wrapper.get('[data-testid="question-answer-statistics-error"]').findAll('button').find(button => button.text() === '重新加载')
    expect(retry).toBeDefined()
    await retry!.trigger('click'); await flushPromises()
    expect(reads.filter(url => url.includes(failure === 'exact' ? '/question-answers/batches/old-batch' : '/question-answers/history')).length).toBeGreaterThan(before)
    expect(wrapper.text()).not.toContain('判定已保存，统计刷新失败')
    expect(wrapper.find('[data-testid="question-answer-statistics-error"]').exists()).toBe(false)
    await expandGroups(wrapper)
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).toContain('人工判定·正确')
    expect(wrapper.emitted('question-answer-stats-retry')).toBeUndefined()
  })
  it('labels a null historical protocol honestly without implying Chat Completions', async () => {
    stored.set('old-batch', batch('old-batch', [record({ requestProtocol: null, repeatIndex: null, judgmentSource: null })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    const card = wrapper.get('[data-testid="question-answer-record-r1"]')
    expect(card.text()).toContain('旧记录未记录协议')
    expect(card.text()).not.toContain('Chat Completions')
  })

  it.each(['success', 'failure'] as const)('continues active runtime polling after a conflicting judgment %s read invalidates an older poll', async outcome => {
    stored.set('old-batch', batch('old-batch', [record(), record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    let resolve!: (response: Response) => void
    let snapshot!: QuestionAnswerBatch
    exactOverride = () => { snapshot = structuredClone(stored.get('old-batch')!); exactOverride = undefined; return new Promise(done => { resolve = done }) }
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(resolve).toBeTypeOf('function')
    conflict = true; failExact = outcome === 'failure'
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('记录已变化')
    resolve(json(snapshot)); await flushPromises()
    failExact = false
    const entry = stored.get('old-batch')!
    stored.set('old-batch', batch('old-batch', entry.records.map(row => row.status === 'running' ? {...row, status: 'cancelled', completedAt: now} : row)))
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('最新运行批次')
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('进行中0')
  })

  it('applies a successful history-only retry even when an active poll succeeds while it is pending', async () => {
    stored.set('old-batch', batch('old-batch', [record(), record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    const section = wrapper.get('[data-testid="question-answer-history"]')
    await section.get('button').trigger('click'); await flushPromises()
    failHistory = true
    await section.findAll('button').find(button => button.text() === '全部')!.trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-statistics-error"]').text()).toContain('重新加载')
    stored.set('old-batch', batch('old-batch', [record({ answerJudgment: 'incorrect', judgmentSource: 'manual', manualError: true }), record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    failHistory = false
    let resolve!: (response: Response) => void
    const freshHistory = history()
    historyOverride = () => { historyOverride = undefined; return new Promise(done => { resolve = done }) }
    await wrapper.get('[data-testid="question-answer-statistics-error"]').findAll('button').find(button => button.text() === '重新加载')!.trigger('click'); await flushPromises()
    expect(resolve).toBeTypeOf('function')
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-review"]'), 0, 1)
    resolve(json(freshHistory)); await flushPromises()
    expect(wrapper.find('[data-testid="question-answer-statistics-error"]').exists()).toBe(false)
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-today"]'), 0, 1)
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-lifetime"]'), 0, 1)
    expect(writes).toHaveLength(0)
  })

  it.each(['success', 'failure'] as const)('retains successful history retry after a newer poll restores batch while its older exact retry %s is settled', async exactOutcome => {
    stored.set('old-batch', batch('old-batch', [record(), record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    const oldBatch = structuredClone(stored.get('old-batch')!)
    failExact = true
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-statistics-error"]').text()).toContain('重新加载')
    failExact = false
    stored.set('old-batch', batch('old-batch', [record({ answerJudgment: 'incorrect', judgmentSource: 'manual', manualError: true, updatedAt: '2026-10-07T10:00:01Z' }), record({ id: 'running', status: 'running', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    exactOverride = async () => { exactOverride = undefined; return exactOutcome === 'success' ? json(oldBatch) : json({ message: 'admin.connectionHealth.errors.request' }, 500) }
    let resolve!: (response: Response) => void
    const freshHistory = history()
    historyOverride = () => { historyOverride = undefined; return new Promise(done => { resolve = done }) }
    await wrapper.get('[data-testid="question-answer-statistics-error"]').findAll('button').find(button => button.text() === '重新加载')!.trigger('click'); await flushPromises()
    expect(resolve).toBeTypeOf('function')
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).toContain('人工判定·错误')
    resolve(json(freshHistory)); await flushPromises()
    expect(wrapper.find('[data-testid="question-answer-statistics-error"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).toContain('人工判定·错误')
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).not.toContain('自动判定·正确')
    for (const period of ['review', 'today', 'lifetime']) expectAccuracyCounts(wrapper.get(`[data-testid="question-answer-stats-${period}"]`), 0, 1)
    expect(writes).toHaveLength(0)
  })

  it.each(['settled-active', 'inflight-active', 'settled-terminal', 'terminal-retry-first'] as const)('preserves newer runtime progress and polling when a failed runtime selection is retried: %s', async order => {
    const initial = batch('new-runtime', [record({ answerBody: 'FIRST' }), record({ id: 'r2', repeatIndex: 2, status: 'running', answerBody: '', answerJudgment: null, judgmentSource: null, completedAt: null }), record({ id: 'r3', repeatIndex: 3, status: 'running', answerBody: '', answerJudgment: null, judgmentSource: null, completedAt: null })])
    const progressed = batch('new-runtime', initial.records.map(row => row.id === 'r2' ? { ...row, status: 'succeeded', answerBody: 'SECOND HIT', answerJudgment: 'correct', judgmentSource: 'automatic', completedAt: now, updatedAt: now } : row))
    const terminal = batch('new-runtime', progressed.records.map(row => row.id === 'r3' ? { ...row, status: 'succeeded', answerBody: 'THIRD HIT', answerJudgment: 'correct', judgmentSource: 'automatic', completedAt: now, updatedAt: now } : row))
    stored.set('new-runtime', initial); latestId = 'new-runtime'
    const wrapper = await dialog('old-batch')
    const section = wrapper.get('[data-testid="question-answer-history"]')
    await section.get('button').trigger('click'); await flushPromises()
    failExact = true
    const runtimeHistory = section.findAll('[data-testid="question-answer-history-batch"]').find(node => node.text().includes('#new-runt'))!
    expect(runtimeHistory).toBeDefined()
    await runtimeHistory.findAll('button').find(button => button.text() === '查看该批次')!.trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain('#old-batc')
    expect(wrapper.get('[data-testid="question-answer-statistics-error"]').text()).toContain('重新加载')
    failExact = false
    let resolveRetry!: (value: Response) => void
    exactOverride = () => { exactOverride = undefined; return new Promise(done => { resolveRetry = done }) }
    await wrapper.get('[data-testid="question-answer-statistics-error"]').findAll('button').find(button => button.text() === '重新加载')!.trigger('click'); await flushPromises()
    expect(resolveRetry).toBeTypeOf('function')
    if (order === 'inflight-active' || order === 'terminal-retry-first') {
      let resolvePoll!: (value: Response) => void
      exactOverride = () => { exactOverride = undefined; return new Promise(done => { resolvePoll = done }) }
      await vi.advanceTimersByTimeAsync(2000); await flushPromises()
      expect(resolvePoll).toBeTypeOf('function')
      const pollRequest = vi.mocked(fetch).mock.calls.filter(([url]) => decodeURIComponent(String(url)).endsWith('/batches/new-runtime')).at(-1)!
      const pollSignal = pollRequest[1]?.signal as AbortSignal
      expect(pollSignal.aborted).toBe(false)
      resolveRetry(json(order === 'terminal-retry-first' ? terminal : initial)); await flushPromises()
      expect(pollSignal.aborted).toBe(false)
      resolvePoll(json(progressed)); await flushPromises()
    } else {
      stored.set('new-runtime', order === 'settled-terminal' ? terminal : progressed)
      await vi.advanceTimersByTimeAsync(2000); await flushPromises()
      resolveRetry(json(initial)); await flushPromises()
    }
    await expandGroups(wrapper)
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain('#new-runt')
    expect(wrapper.find('[data-testid="question-answer-statistics-error"]').exists()).toBe(false)
    const expectedCorrect = order.includes('terminal') ? 3 : 2
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-review"]'), expectedCorrect, expectedCorrect)
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').text()).toContain('SECOND HIT')
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').text()).toContain('自动判定·正确')
    expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(!order.includes('terminal'))
    expect(writes).toHaveLength(0)
  })

  it.each(['saved-selection', 'saved-poll', 'conflict-selection', 'conflict-poll'] as const)('preserves newer runtime selection and in-flight poll after a judgment follow-up read: %s', async order => {
    stored.set('old-batch', batch('old-batch', [record(), record({ id: 'r2', repeatIndex: 2, status: 'running', answerBody: '', answerJudgment: null, judgmentSource: null, completedAt: null }), record({ id: 'r3', repeatIndex: 3, status: 'running', answerBody: '', answerJudgment: null, judgmentSource: null, completedAt: null })]))
    const wrapper = await dialog(); await expandGroups(wrapper)
    let resolveFollowup!: (value: Response) => void
    let oldSnapshot!: QuestionAnswerBatch
    exactOverride = () => { oldSnapshot = structuredClone(stored.get('old-batch')!); exactOverride = undefined; return new Promise(done => { resolveFollowup = done }) }
    conflict = order.startsWith('conflict')
    await wrapper.get('[data-testid="question-answer-record-r1"]').findAll('button')[1]!.trigger('click'); await flushPromises()
    expect(resolveFollowup).toBeTypeOf('function')
    expect(writes).toHaveLength(1)
    stored.set('old-batch', batch('old-batch', stored.get('old-batch')!.records.map(row => row.id === 'r2' ? { ...row, status: 'succeeded', answerBody: 'SECOND HIT', answerJudgment: 'correct', judgmentSource: 'automatic', completedAt: now, updatedAt: now } : row)))
    const section = wrapper.get('[data-testid="question-answer-history"]')
    await section.get('button').trigger('click'); await flushPromises()
    await section.findAll('button').find(button => button.text() === '查看该批次')!.trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-latest-running-hint"]').text()).toContain('2/3')
    let resolvePoll!: (value: Response) => void
    let pollSignal: AbortSignal | undefined
    const fresh = batch('old-batch', stored.get('old-batch')!.records.map(row => row.id === 'r2' ? { ...row, answerBody: 'SECOND NEW HIT' } : row))
    if (order.endsWith('poll')) {
      exactOverride = () => { exactOverride = undefined; return new Promise(done => { resolvePoll = done }) }
      await vi.advanceTimersByTimeAsync(2000); await flushPromises()
      expect(resolvePoll).toBeTypeOf('function')
      const request = vi.mocked(fetch).mock.calls.filter(([url]) => decodeURIComponent(String(url)).endsWith('/batches/old-batch')).at(-1)!
      pollSignal = request[1]?.signal as AbortSignal
      expect(pollSignal.aborted).toBe(false)
    }
    resolveFollowup(json(oldSnapshot)); await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-latest-running-hint"]').text()).toContain('2/3')
    expectAccuracyCounts(wrapper.get('[data-testid="question-answer-stats-review"]'), 1, 2)
    if (order.endsWith('poll')) {
      expect(pollSignal!.aborted).toBe(false)
      resolvePoll(json(fresh)); await flushPromises()
    }
    await expandGroups(wrapper)
    expect(wrapper.get('[data-testid="question-answer-record-r2"]').text()).toContain(order.endsWith('poll') ? 'SECOND NEW HIT' : 'SECOND HIT')
    expect(wrapper.get('[data-testid="question-answer-record-r1"]').text()).toContain('人工判定·错误')
    expect(wrapper.find('[data-testid="question-answer-stop-latest"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="question-answer-stats-review"] [data-testid="question-answer-accuracy"]').text()).not.toBe('0%')
    expect(writes).toHaveLength(1)
  })

})
