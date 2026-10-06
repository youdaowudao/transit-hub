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
describe('B known model-list refusal presentation', () => {
 it.each([
  ['announcementAckRequired', '上游要求先在网页上确认新公告，确认前无法读取数据。'],
  ['upstreamInsufficientBalance', '上游账户余额不足，上游不允许读取。'],
  ['upstreamKeyQuotaExhausted', '该 Key 在上游的额度已用尽。'],
  ['upstreamKeyExpired', '该 Key 在上游已过期。'],
 ])('displays %s without a raw upstream message', async (reason, text) => {
  harness.discover.mockResolvedValue({ errorKey: `admin.upstream.errors.${reason}` })
  const wrapper = mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId: 'sub2api:fixture:1', accountName: '验收-模型', platform: 'sub2api', type: 'apikey', status: 'active', groupName: '验收-分组' } }, global: { stubs: { Teleport: true, Transition: false } } })
  try {
   await wrapper.setProps({ open: true }); await flushPromises()
   expect(wrapper.text()).toContain(text)
   expect(wrapper.text()).not.toContain('raw-fixture')
  } finally { wrapper.unmount(); document.body.innerHTML = '' }
 })
})
