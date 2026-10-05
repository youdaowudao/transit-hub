// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamKeyUsageTodayModal from '@/modules/admin/components/dashboard/UpstreamKeyUsageTodayModal.vue'

const harness = vi.hoisted(() => ({ read: vi.fn() }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({ getUpstreamKeyUsageToday: harness.read }))
beforeEach(() => harness.read.mockReset())

describe('D2 saved cost snapshot presentation', () => {
  it('does not display a confirmed zero when every site snapshot is missing', async () => {
    harness.read.mockResolvedValue({ total: 0, keys: [], failedSites: 1, totalSites: 1, sites: [{ siteId: 'missing', siteName: '验收-全缺失', status: 'missing', collectedAt: null }] })
    const wrapper = mount(UpstreamKeyUsageTodayModal, { props: { open: false }, global: { stubs: { Teleport: true } } })
    try {
      await wrapper.setProps({ open: true })
      await flushPromises()
      expect(wrapper.text()).toContain('合计 —')
      expect(wrapper.text()).not.toContain('合计 0.00')
      expect(wrapper.text()).not.toContain('暂无今日消费的 key。')
      expect(wrapper.text()).toContain('验收-全缺失')
    } finally { wrapper.unmount() }
  })
  it('shows all site states and collection times while excluding missing costs', async () => {
    harness.read.mockResolvedValue({ date: '2026-10-05', total: 9, failedSites: 1, totalSites: 3, autoRefreshEnabled: false,
      sites: [
        { siteId: 'ok', siteName: '验收-正常', status: 'ok', collectedAt: '2026-10-05T03:04:00Z' },
        { siteId: 'retained', siteName: '验收-保留', status: 'retained', collectedAt: '2026-10-05T02:04:00Z', errorKey: 'admin.upstream.errors.network' },
        { siteId: 'missing', siteName: '验收-缺失', status: 'missing', collectedAt: null, errorKey: 'admin.upstream.errors.request' },
      ], keys: [{ siteId: 'retained', siteName: '验收-保留', platform: 'new-api', keyId: '1', keyIds: ['1', '2'], merged: true, keyName: 'shared', groupName: 'vip', todayAmount: 9, rawAmount: 9, rechargeRate: 1 }] })
    const wrapper = mount(UpstreamKeyUsageTodayModal, { props: { open: false }, global: { stubs: { Teleport: true } } })
    try {
      await wrapper.setProps({ open: true })
      await flushPromises()
      const text = wrapper.text()
      for (const value of ['验收-正常', '验收-保留', '验收-缺失', '读取完整', '保留', '不可用', '11:04', '10:04', '当前未开启自动刷新，数据来自最近一次手动刷新', '同名 Token 无法拆分', '以下站点没有读到今日逐 Key 用量，合计不含这些站点']) expect(text).toContain(value)
      expect(text).not.toContain('当前合计仅包含成功站点')
      expect(harness.read).toHaveBeenCalledTimes(1)
    } finally { wrapper.unmount() }
  })
})
