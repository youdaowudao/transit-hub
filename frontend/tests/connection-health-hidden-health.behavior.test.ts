// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'

import GroupHealthSetupDrawer from '@/modules/admin/components/dashboard/GroupHealthSetupDrawer.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const api = vi.hoisted(() => ({
  load: vi.fn(),
}))
vi.mock('@/modules/admin/composables/useConnectionHealth', () => ({
  connectionHealthMessageKey: (key: string) => key,
  useConnectionHealth: () => ({
    loadAdminGroupPolicyConfiguration: api.load,
    loadAdminGroupTestConfiguration: async () => ({ errorKey: 'fixture' }),
  }),
}))

it('locks screened rows to both original selections without changing another draft', async () => {
  const accounts = [
    { targetId: 'sub2api:ws1:a', name: '原来勾选', probeAvailable: true },
    { targetId: 'sub2api:ws1:b', name: '原来未勾选', probeAvailable: false },
    { targetId: 'sub2api:ws1:c', name: '其他账号', probeAvailable: true },
  ] as AdminGroupAccount[]
  const group = { id: 'g1', platform: 'openai', accounts } as AdminGroupHealth
  api.load.mockResolvedValue({ configuration: { policyIds: [], excludedTargetIds: [accounts[1].targetId] } })
  const wrapper = mount(GroupHealthSetupDrawer, {
    props: { open: false, group, workspacePlatform: 'sub2api', policies: [], allGroups: [group] },
    global: { stubs: { Teleport: true } },
  })
  try {
    await wrapper.setProps({ open: true })
    await flushPromises()
    const row = (name: string) => wrapper.findAll('label').find(label => label.text().includes(name))!
    const checkbox = (name: string) => row(name).get<HTMLInputElement>('input[type="checkbox"]')
    await checkbox('原来勾选').setValue(false)
    await checkbox('原来未勾选').setValue(true)
    await checkbox('其他账号').setValue(false)
    await wrapper.setProps({ hiddenTargetIds: new Set(accounts.slice(0, 2).map(account => account.targetId)) })
    expect(checkbox('原来勾选').element.checked).toBe(true)
    expect(checkbox('原来未勾选').element.checked).toBe(false)
    for (const name of ['原来勾选', '原来未勾选']) {
      expect(checkbox(name).element.disabled).toBe(true)
      expect(row(name).text()).toContain('已屏蔽，暂不参加健康管理')
      expect(row(name).text()).not.toMatch(/可探活|凭据待完善|只影响当前分组/)
    }
    expect(checkbox('其他账号').element.disabled).toBe(false)
    expect(checkbox('其他账号').element.checked).toBe(false)
    await wrapper.setProps({ hiddenTargetIds: new Set() })
    expect(checkbox('原来勾选').element.checked).toBe(true)
    expect(checkbox('原来未勾选').element.checked).toBe(false)
    expect(checkbox('原来未勾选').element.disabled).toBe(false)
  } finally {
    wrapper.unmount()
  }
})
