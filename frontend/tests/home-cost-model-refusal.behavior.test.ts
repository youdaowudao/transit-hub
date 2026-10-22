// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
const harness = vi.hoisted(() => ({ discover: vi.fn() }))
vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  return {
    connectionHealthProbeResultLabelKey: actual.connectionHealthProbeResultLabelKey,
   connectionHealthMessageKey: (key: string) => key, connectionHealthRecordColorClass: () => '', formatConnectionHealthTime: (v: string) => v,
   useConnectionHealth: () => ({ discoverModels: harness.discover, manualProbeTarget: vi.fn(), runManualProbeOnce: vi.fn(), errorKey: { value: '' } }),
  }
})
vi.mock('@/modules/admin/api/connectionHealth', () => ({
 listTestQuestions: vi.fn(async () => []),
 getQuestionAnswerHistory: vi.fn(async () => ({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 0, todayStats: { requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 }, reviews: { unreviewed: 0, correct: 0, incorrect: 0 }, byModel: [] } })),
 getLatestQuestionAnswerBatch: vi.fn(async () => ({ batchId: '', records: [], active: false })),
}))
describe('B known model-list refusal presentation', () => {
 it.each([
  ['announcementAckRequired', '上游要求先在网页上确认新公告，确认前无法读取数据。', '请先在对应上游网页确认公告'],
  ['upstreamInsufficientBalance', '上游账户余额不足，上游不允许读取。', '请先到对应上游账户处理余额'],
  ['upstreamKeyQuotaExhausted', '该 Key 在上游的额度已用尽。', '请在对应上游检查该 Key 的额度'],
  ['upstreamKeyExpired', '该 Key 在上游已过期。', '请在对应上游更新该 Key'],
 ])('displays %s without a raw upstream message', async (reason, text, guidance) => {
  harness.discover.mockResolvedValue({ errorKey: `admin.upstream.errors.${reason}` })
  const wrapper = mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId: 'sub2api:fixture:1', accountName: '验收-模型', platform: 'sub2api', type: 'apikey', status: 'active', groupName: '验收-分组' } }, global: { stubs: { Teleport: true, Transition: false } } })
  try {
   await wrapper.setProps({ open: true }); await flushPromises()
   expect(wrapper.text()).toContain(text)
   expect(wrapper.get('[data-testid="question-answer-configuration"]').text()).toContain(guidance)
   expect(wrapper.text()).toContain('只重新读取模型列表，不发起答题，也不修改主站。')
   if (reason !== 'upstreamInsufficientBalance') expect(wrapper.text()).not.toContain('上游账户余额不足')
   expect(wrapper.text()).not.toContain('<script>')
   expect(wrapper.text()).not.toContain('raw-fixture')
  } finally { wrapper.unmount(); document.body.innerHTML = '' }
 })
})
