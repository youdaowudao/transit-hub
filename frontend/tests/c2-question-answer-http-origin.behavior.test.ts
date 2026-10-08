// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import QuestionAnswerScheduleDrawer from '@/modules/admin/components/dashboard/QuestionAnswerScheduleDrawer.vue'
import { c2Execution, c2Groups, c2Limits, c2Schedule } from './fixtures/c2QuestionAnswerSchedules'

const api = vi.hoisted(() => ({ listQuestionAnswerSchedules: vi.fn(), getQuestionAnswerScheduleLimits: vi.fn(), runQuestionAnswerSchedule: vi.fn(), getQuestionAnswerScheduleExecution: vi.fn() }))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('HTTP Tailnet origin accepts run-now and preserves its requestId across an ambiguous retry', async () => {
  const nativeCrypto = globalThis.crypto
  vi.stubGlobal('crypto', { getRandomValues: nativeCrypto.getRandomValues.bind(nativeCrypto) })
  expect(globalThis.crypto.randomUUID).toBeUndefined()
  api.listQuestionAnswerSchedules.mockResolvedValue({ items: [c2Schedule()], totalPages: 1 })
  api.getQuestionAnswerScheduleLimits.mockResolvedValue(c2Limits())
  api.getQuestionAnswerScheduleExecution.mockResolvedValue(c2Execution())
  api.runQuestionAnswerSchedule.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network')).mockResolvedValueOnce(c2Execution())
  const wrapper = mount(QuestionAnswerScheduleDrawer, { props: { groups: c2Groups(), workspace: 'ws1', platform: 'sub2api', preferences: { modelIds: ['m1'], questionIds: ['q1'], reasoningEffort: 'medium', repeatCount: 1 } } })
  try {
    await wrapper.get('[data-testid="question-answer-schedule-open"]').trigger('click'); await flushPromises()
    const run = () => wrapper.findAll('button').find(button => button.text() === '立即执行')!
    await run().trigger('click'); await flushPromises()
    expect(api.runQuestionAnswerSchedule).toHaveBeenCalledTimes(1)
    const requestId = api.runQuestionAnswerSchedule.mock.calls[0]![1]
    expect(requestId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i)
    expect(wrapper.find('[data-testid="schedule-execution-detail"]').exists()).toBe(false)
    await run().trigger('click'); await flushPromises()
    expect(api.runQuestionAnswerSchedule).toHaveBeenCalledTimes(2)
    expect(api.runQuestionAnswerSchedule.mock.calls[1]![1]).toBe(requestId)
    expect(wrapper.find('[data-testid="schedule-execution-detail"]').exists()).toBe(true)
  } finally { wrapper.unmount() }
})
