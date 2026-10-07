// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import { questionAnswerAccuracy, formatQuestionAnswerAccuracy } from '@/modules/admin/utils/questionAnswers'
import type { AdminGroupHealth, QuestionAnswerStats } from '@/modules/admin/types/connectionHealth'

const api = vi.hoisted(() => ({ questions: vi.fn(), models: vi.fn(), start: vi.fn(), refresh: vi.fn() }))
vi.mock('@/modules/admin/api/connectionHealth', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>(),
  listTestQuestions: api.questions, discoverTargetModels: api.models, startQuestionAnswerBatch: api.start,
  refreshConnectionHealthAdminGroups: api.refresh, refreshConnectionHealthAdminGroupsAutomatically: api.refresh,
}))

const targetId = 'sub2api:c1-workspace:1'
const group = (id: string): AdminGroupHealth => ({
  id, name: id, platform: 'openai', status: 'active', type: 'standard', isExclusive: false, subscriptionType: '',
  multiplier: 1, multiplierDisplay: '1x', accountCount: 1, monitoredAccountCount: 1,
  healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0,
    degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
  accounts: [{ id: '1', name: 'C1 account', platform: 'openai', type: 'subscription', status: 'active', schedulable: true,
    targetId, priority: 15, accountTier: 1, probeAvailable: true, modelHealth: [],
    todayQuestionAnswerSubmitted: 8, todayQuestionAnswerCorrect: 1 }],
})
const groups = () => [group('one'), group('two')]
const service = useConnectionHealth()
const wrappers: VueWrapper[] = []
beforeEach(() => {
  api.questions.mockReset().mockResolvedValue([{ id: 'q', name: 'C1 question', body: 'Body', keywords: ['hit'], enabled: true, isDefault: true }])
  api.models.mockReset().mockResolvedValue([{ id: 'model', name: 'model' }])
  api.refresh.mockReset()
  api.start.mockReset().mockResolvedValue({ batchId: 'exact-started-c1', submittedCount: 3 })
  service.setAdminGroupsWorkspace('')
  service.setAdminGroupsWorkspace('c1-workspace')
})
afterEach(async () => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  service.setAdminGroupsWorkspace('')
  await flushPromises()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})
const drawer = async () => {
  const wrapper = mount(QuestionAnswerBatchDrawer, { attachTo: document.body, props: {
    groups: groups(), preferenceScope: 'c1-workspace', preferences: { modelIds: ['model'], questionIds: ['q'], reasoningEffort: 'medium', repeatCount: 3, batchTargetIds: [targetId] },
  } })
  wrappers.push(wrapper)
  ;(wrapper.vm as unknown as { open: () => void }).open()
  await flushPromises()
  const button = wrapper.findAll('button').find(item => item.text().includes('开始') && !item.attributes('disabled'))
  expect(button).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
  return wrapper
}

describe('C1 fixed business RED', () => {
  it('uses only correct plus incorrect as accuracy denominator', () => {
    const stats: QuestionAnswerStats = { requests: { submitted: 8, inProgress: 2, succeeded: 4, failed: 1, cancelled: 1 }, reviews: { correct: 1, incorrect: 1, unreviewed: 2 }, byModel: [], byQuestion: [] }
    expect(questionAnswerAccuracy(stats)).toBe(50)
    expect(questionAnswerAccuracy({ ...stats, reviews: { correct: 0, incorrect: 0, unreviewed: 4 } })).toBeNull()
    expect(formatQuestionAnswerAccuracy(null)).toBe('—')
  })
  it('opens a started outcome by its authoritative batch ID', async () => {
    const wrapper = await drawer()
    const view = wrapper.findAll('button').find(item => item.text().trim() === '查看该批次')
    expect(view).toBeDefined()
    await view!.trigger('click')
    expect(wrapper.emitted('question-answer-view')?.at(-1)).toEqual([{ targetId, batchId: 'exact-started-c1' }])
  })
  it('opens an active skip through latest without inventing a batch ID', async () => {
    api.start.mockRejectedValue(new Error('admin.connectionHealth.errors.questionAnswerActive'))
    const wrapper = await drawer()
    expect(wrapper.findAll('button').some(item => item.text().trim() === '查看该批次')).toBe(false)
    const latest = wrapper.findAll('button').find(item => item.text().trim() === '打开该账号最近批次')
    expect(latest).toBeDefined()
    await latest!.trigger('click')
    expect(wrapper.emitted('question-answer-view')?.at(-1)).toEqual([{ targetId }])
  })
  it('protects all cross-group projections against an earlier admin-groups response', async () => {
    const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
    vi.stubGlobal('fetch', vi.fn(async () => json(groups())))
    expect(await service.loadAdminGroups()).toBe(true)
    let resolve!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(done => { resolve = done })))
    const oldRead = service.loadAdminGroups()
    await flushPromises()
    const apply = (service as unknown as { applyQuestionAnswerTodaySummary?: (summary: unknown) => void }).applyQuestionAnswerTodaySummary
    try {
      expect(apply).toBeTypeOf('function')
      apply!({ targetId, todayStats: { requests: { submitted: 10, inProgress: 0, succeeded: 10, failed: 0, cancelled: 0 }, reviews: { correct: 7, incorrect: 3, unreviewed: 0 } } })
    } finally { resolve(json(groups())) }
    await oldRead
    for (const entry of service.adminGroups.value) {
      expect(entry.accounts[0]).toMatchObject({ todayQuestionAnswerSubmitted: 10, todayQuestionAnswerCorrect: 7, todayQuestionAnswerJudged: 10, priority: 15, accountTier: 1 })
    }
  })
  it.each(['JSON', 'SSE'] as const)('protects local summary against an earlier %s refresh terminal', async (transport) => {
    const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
    vi.stubGlobal('fetch', vi.fn(async () => json(groups())))
    expect(await service.loadAdminGroups()).toBe(true)
    let resolve!: (value: unknown) => void
    let options: { onTerminal: (value: unknown) => void }
    api.refresh.mockImplementationOnce((value) => {
      options = value
      return new Promise(done => { resolve = done })
    })
    const oldRead = service.refreshAdminGroupsAutomatically()
    await flushPromises()
    service.applyQuestionAnswerTodaySummary({ targetId, todayStats: {
      requests: { submitted: 10, inProgress: 0, succeeded: 10, failed: 0, cancelled: 0 },
      reviews: { correct: 7, incorrect: 3, unreviewed: 0 },
    } })
    const terminal = { status: 'success', runId: `c1-${transport}`, revision: 1, groups: groups(), refresh: { state: 'success', sites: [] } }
    if (transport === 'SSE') options!.onTerminal(terminal)
    resolve(terminal)
    expect(await oldRead).toBe(true)
    for (const entry of service.adminGroups.value) {
      expect(entry.accounts[0]).toMatchObject({ todayQuestionAnswerSubmitted: 10, todayQuestionAnswerCorrect: 7, todayQuestionAnswerJudged: 10, priority: 15, accountTier: 1, status: 'active', schedulable: true })
    }
  })

})
