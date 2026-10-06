// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import GroupHealthSetupDrawer from '@/modules/admin/components/dashboard/GroupHealthSetupDrawer.vue'
import type { AdminGroupHealth } from '@/modules/admin/types/connectionHealth'
vi.mock('@/modules/admin/composables/useConnectionHealth', () => ({
  connectionHealthMessageKey: (key: string) => key,
  useConnectionHealth: () => ({
    loadAdminGroupTestConfiguration: async () => ({ configuration: { adminGroupId: 'g', adminGroupName: '分组', configuration: { protocol: 'responses', probeTimeoutSeconds: 30 }, inventoryComplete: true, affectedAccountCount: 0, conflictAccountCount: 0, accounts: [] } }),
    loadAdminGroupPolicyConfiguration: async () => ({ configuration: { adminGroupId: 'g', policyIds: [], policies: [], excludedTargetIds: [] } }),
    saveAdminGroupTestConfiguration: vi.fn(), createPolicyForSetup: vi.fn(), saveAdminGroupPolicyConfiguration: vi.fn(), updatePolicyForSetup: vi.fn(),
  }),
}))
describe('task A group timeout warning', () => {
  it('warns only above 30 seconds, retains the timeout range and does not save merely by editing', async () => {
    const group = { id: 'g', name: '分组', platform: 'openai', accounts: [], healthSummary: {}, multiplier: 1 } as AdminGroupHealth
    const wrapper = mount(GroupHealthSetupDrawer, { props: { open: false, workspacePlatform: 'sub2api', group, policies: [], allGroups: [group] }, global: { stubs: { Teleport: true } } })
    try {
      await wrapper.setProps({ open: true }); await flushPromises()
      expect(wrapper.find('[data-testid="group-test-timeout-warning"]').exists()).toBe(false)
      await wrapper.get('[data-testid="group-test-timeout"]').setValue(31)
      expect(wrapper.get('[data-testid="group-test-timeout-warning"]').text()).toContain('一次卡住会拖慢整轮，复测也会变慢')
      expect(wrapper.get('[data-testid="group-test-timeout"]').attributes()).toMatchObject({ min: '5', max: '120' })
      await wrapper.get('[data-testid="group-test-timeout"]').setValue(30)
      expect(wrapper.find('[data-testid="group-test-timeout-warning"]').exists()).toBe(false)
      expect(wrapper.emitted('saved')).toBeUndefined()
    } finally { wrapper.unmount() }
  })
})
