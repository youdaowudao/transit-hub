// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'

// V3.1.7：经真实 requestJson 解析失败响应，页面显示后端给出的整句原因（含邮箱和脱敏原因）。
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: vi.fn().mockResolvedValue([]) }))

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
let wrapper: VueWrapper | undefined

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/dashboard/gift-ledger/customer')) {
      return json(502, { message: 'dashboard.giftLedger.errors.usageLookupFailed', reason: '查到了 new@example.com，但读取他今天的消费失败（读取主站今天消费：HTTP 404，主站提示“not found”）；今天已花没有查到，请稍后重试' })
    }
    if (url.includes('/dashboard/gift-ledger')) return json(200, { supported: true, statusAvailable: true, statusError: '', today: '2026-08-22', customers: [] })
    if (url.includes('/dashboard/recharge-fee-rate?')) return json(200, { rate: 0.016, effectiveDate: '2026-08-22' })
    return json(200, { items: [], hasMore: false })
  }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals() })

describe('gift ledger lookup failure through the real request parser', () => {
  it('shows the backend sentence with the email and the masked reason for a customer never loaded before', async () => {
    wrapper = mount(AccountCostWorkspace, {
      props: { open: false, initialTab: 'rules', businessDate: '2026-08-22', workspaceId: 'workspace-a', directCost: 0, operatingCost: 0, adjustedNetProfit: 0 },
      global: { stubs: { Teleport: true } },
    })
    await wrapper.setProps({ open: true })
    await flushPromises()
    await wrapper.find('input[aria-label="主站用户 ID"]').setValue('777')
    await wrapper.findAll('button').find(b => b.text() === '查询这个用户')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('查到了 new@example.com，但读取他今天的消费失败（读取主站今天消费：HTTP 404，主站提示“not found”）；今天已花没有查到，请稍后重试')
    expect(wrapper.text()).not.toContain('主站找不到 ID 为 777')
    expect(wrapper.text()).not.toContain('dashboard.giftLedger')
  })
})
