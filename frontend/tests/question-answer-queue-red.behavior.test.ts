// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import type { AdminGroupHealth } from '@/modules/admin/types/connectionHealth'
import { createDefaultConnectionHealthPreferences } from '@/modules/admin/utils/connectionHealthPreferences'

let wrapper: VueWrapper | undefined
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals() })

it('keeps browser waiting targets separate and never resends an uncertain creation', async () => {
  const targets = ['sub2api:ws1:a', 'sub2api:ws1:b']
  const posts: string[] = []
  let release: ((response: Response) => void) | undefined
  const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path.endsWith('/test-questions')) return reply([{ id: 'q', name: '排队隔离题', body: 'q', enabled: true, isDefault: true, keywords: [] }])
    if (path.endsWith('/models')) return reply([{ id: 'm', name: 'm' }])
    if (path.includes('/question-answer-queue')) return reply({ snapshotAt: new Date().toISOString(), capacity: 15, availableSlots: 15, workspaceOccupied: 0, heldCount: 0, preparingTargets: [], queued: [], processing: [], stopping: [], finalizingBatches: [], counts: { preparingTargets: 0, queued: 0, processing: 0, stopping: 0, finalizingBatches: 0 } })
    if (path.endsWith('/question-answers/batches') && init?.method === 'POST') {
      posts.push(path)
      if (posts.length === 1) return await new Promise<Response>(resolve => { release = resolve })
      return reply({ batchId: 'accepted-b', records: [{ id: 'accepted-record', targetId: targets[1], batchId: 'accepted-b', modelName: 'm', questionId: 'q', questionName: '排队隔离题', questionBody: 'q', questionKeywordSnapshot: [], reasoningEffort: 'medium', requestProtocol: 'chat_completions', repeatIndex: 1, answerBody: '', status: 'pending', errorType: '', answerJudgment: null, judgmentSource: null, manualError: false, createdAt: new Date().toISOString(), startedAt: null, completedAt: null, updatedAt: new Date().toISOString() }], active: true, reasoningEffort: 'medium', repeatCount: 1, submittedCount: 1, completedCount: 0, runningCount: 0, currentModel: '', currentQuestion: '', stats: { requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 }, reviews: { correct: 0, incorrect: 0, unreviewed: 0 }, byModel: [], byQuestion: [] } })
    }
    throw new Error('unexpected isolated request')
  }))
  const groups = [{ id: 'g1', name: '隔离组', accounts: targets.map((targetId, index) => ({ id: index ? 'b' : 'a', targetId, name: index ? '隔离账号B' : '隔离账号A', platform: 'openai', type: 'apikey', status: 'active', probeAvailable: true, modelHealth: [] })) }] as AdminGroupHealth[]
  wrapper = mount(QuestionAnswerBatchDrawer, {
    props: { groups, preferenceScope: 'isolated-queue-red', preferences: { ...createDefaultConnectionHealthPreferences().questionAnswer, modelIds: ['m'], questionIds: ['q'], repeatCount: 1, batchTargetIds: targets } }, global: { stubs: { Teleport: true } },
  })
  await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click')
  await flushPromises()
  await vi.waitFor(() => expect(wrapper!.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeUndefined())
  await wrapper.get('[data-testid="question-answer-batch-start"]').trigger('click')
  await flushPromises()
  expect(posts).toHaveLength(1)
  expect(wrapper.text()).toContain('正在提交')
  expect(wrapper.text()).toContain('等待提交')
  expect(wrapper.get('[data-testid="question-answer-batch-run-summary"]').text()).not.toMatch(/实际接受总量\s*0/)
  release!(reply({ message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: '创建批次返回前连接中断，目前无法确认是否已接收；请先查看最近批次。', step: 'create', operationResult: 'unknown' }, 503))
  await flushPromises()
  await vi.waitFor(() => expect(posts).toHaveLength(2))
  const first = wrapper.get(`[data-testid="question-answer-batch-outcome-${targets[0]}"]`)
  expect(first.text()).toContain('未确认')
  expect(first.text()).toContain('不会自动重发')
  expect(first.text()).not.toMatch(/已受理\s*0/)
  expect(first.text()).not.toContain('admin.connectionHealth.errors.')
  expect(wrapper.get('[data-testid="question-answer-batch-run-summary"]').text()).not.toMatch(/实际接受总量\s*[01](?:\s|$)/)
  expect(first.findAll('button').some(button => button.text().includes('查看最近批次'))).toBe(true)
  const second = wrapper.get(`[data-testid="question-answer-batch-outcome-${targets[1]}"]`)
  expect(second.findAll('button').some(button => button.text() === '查看该批次')).toBe(true)
  expect(wrapper.emitted('question-answer-started')).toEqual([[targets[1]]])
  expect(posts.filter(path => path.includes(encodeURIComponent(targets[0])) || path.includes(targets[0]))).toHaveLength(1)
})
