// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import QuestionAnswerBatchDrawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import type { AdminGroupHealth } from '@/modules/admin/types/connectionHealth'
import { createDefaultConnectionHealthPreferences } from '@/modules/admin/utils/connectionHealthPreferences'

const wrappers: VueWrapper[] = []
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  vi.unstubAllGlobals()
})

describe('question answer public internal-error response', () => {
  it('shows the legacy internal-error classification and asks to read before another submission', async () => {
    const targetId = 'sub2api:ws1:a'
    const wireMessage = 'admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes.internal_error'
    const posts: Array<{ path: string; body: unknown }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      const response = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
      if (path.endsWith('/test-questions')) return response([{ id: 'question', name: '隔离题目', body: 'question', enabled: true, isDefault: true, keywords: [] }])
      if (path.endsWith('/models')) return response([{ id: 'available', name: 'available', providerFamily: 'openai' }])
      if (path.endsWith('/question-answers/batches') && init?.method === 'POST') {
        posts.push({ path, body: JSON.parse(String(init.body)) })
        return response({ message: wireMessage }, 500)
      }
      throw new Error('unexpected isolated request')
    }))
    const groups = [{ id: 'g1', name: '隔离分组', accounts: [{ id: 'a', targetId, name: '隔离账号', platform: 'openai', type: 'apikey', status: 'active', probeAvailable: true, modelHealth: [] }] }] as AdminGroupHealth[]
    const wrapper = mount(QuestionAnswerBatchDrawer, {
      props: { groups, preferenceScope: 'isolated-http-error', preferences: { ...createDefaultConnectionHealthPreferences().questionAnswer, modelIds: ['available'], questionIds: ['question'], repeatCount: 1, batchTargetIds: [targetId] } },
      global: { stubs: { Teleport: true } },
    })
    wrappers.push(wrapper)
    await wrapper.get('[data-testid="question-answer-batch-open"]').trigger('click')
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeUndefined())
    await wrapper.get('[data-testid="question-answer-batch-start"]').trigger('click')
    await flushPromises()
    const outcome = wrapper.get(`[data-testid="question-answer-batch-outcome-${targetId}"]`)
    expect(posts).toHaveLength(1)
    expect(posts[0].body).toMatchObject({ models: ['available'], questionIds: ['question'], repeatCount: 1 })
    expect(outcome.text()).toContain('接口返回了答题程序内部错误分类')
    expect(outcome.text()).toContain('目前无法确认这批题是否已提交')
    expect(outcome.text()).toContain('最近批次或历史核对')
    expect(outcome.text()).toContain('本次不会自动重发')
    expect(outcome.text()).not.toContain('本批次已停止')
    expect(outcome.text()).not.toContain('暂时无法读取分组健康数据')
    expect(outcome.text()).not.toContain(wireMessage)
    expect(wrapper.emitted('question-answer-started')).toBeUndefined()
  })
})
