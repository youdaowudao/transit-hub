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
  it('shows the stopped-batch cause and retry guidance in the actual submission drawer', async () => {
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
    expect(outcome.text()).toContain('测试程序内部出错，本批次已停止，没有修改主站；可以重新发起测试。')
    expect(outcome.text()).not.toContain('暂时无法读取分组健康数据')
    expect(outcome.text()).not.toContain(wireMessage)
    expect(wrapper.emitted('question-answer-started')).toBeUndefined()
  })
})
