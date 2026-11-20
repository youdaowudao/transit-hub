// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { t } from '@/locales'
import UserActivityView from '@/modules/admin/views/UserActivityView.vue'

const api = vi.hoisted(() => ({ getActivitySummary: vi.fn(), getActivityUsers: vi.fn(), getActivitySync: vi.fn(), getActivityDetail: vi.fn(), startActivitySync: vi.fn() }))
vi.mock('@/modules/admin/api/userActivity', () => api)
vi.mock('@/modules/admin/composables/useAdminAccounts', () => ({ useAdminAccounts: () => ({ currentAccount: { value: { id: 'fixture-workspace', createdAt: '2026-11-20T00:00:00Z' } }, isSwitching: { value: false }, loadCurrentAccount: vi.fn() }) }))
vi.mock('@/modules/admin/components/dashboard/DashboardEChart.vue', () => ({ default: { template: '<div />' } }))
const wrappers: VueWrapper[] = []
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.clearAllMocks() })
async function read(observedAt: string | null, amounts: { todayRecharge: number | null; todayDeduction: number | null } = { todayRecharge: null, todayDeduction: null }) {
  const sync = { status: 'idle', userCount: 0, warnings: [], usageFrom: '', usageTo: '', rechargeFrom: '', rechargeTo: '', startedAt: null, lastSyncedAt: null, todayUsageObservedAt: null }
  const coverage = { usageFrom: '', usageTo: '', rechargeFrom: '', rechargeTo: '', warnings: [] }
  api.getActivitySummary.mockResolvedValue({ sync, coverage, warnings: [], daily: [], periods: {}, today: { day: '2026-11-20', requests: null, tokens: null, cost: null, activeUsers: null, recharge: null, deduction: null, temporary: true }, money: { totalBalance: observedAt ? 0 : null, observedAt, ...amounts, yesterdayRecharge: null, yesterdayDeduction: null, channels: [], complete: false } })
  api.getActivityUsers.mockResolvedValue({ items: [], total: 0, page: 1, pageSize: 20, sync, coverage })
  const wrapper = mount(UserActivityView)
  wrappers.push(wrapper)
  await flushPromises()
  const heading = wrapper.findAll('h2').find(node => node.text() === t('admin.userActivity.users'))
  expect(heading).toBeDefined()
  const result = heading!.element.parentElement!.querySelector('p.text-2xl')
  return result?.textContent?.trim()
}
describe('complete inventory observations distinguish an empty inventory from no observation', () => {
  it.each([[20, null], [null, 8], [0, 0]] as const)('renders each current-day amount category separately: %s / %s', async (todayRecharge, todayDeduction) => {
    await read(null, { todayRecharge, todayDeduction })
    const w = wrappers.at(-1)!
    const heading = w.findAll('h2').find(node => node.text() === t('admin.userActivity.todayRecharge'))!
    const card = heading.element.parentElement!
    expect(card.querySelector('p.text-2xl')?.textContent?.trim()).toBe(todayRecharge === null ? t('admin.userActivity.unknown') : String(todayRecharge))
    const deduction = [...card.querySelectorAll('p')].find(node => node.textContent?.includes(t('admin.userActivity.deduction')))?.textContent
    expect(deduction).toBe(t('admin.userActivity.deduction') + (todayDeduction === null ? t('admin.userActivity.unknown') : String(todayDeduction)))
  })
  it('does not claim zero users before the first complete inventory', async () => {
    expect(await read(null)).toBe(t('admin.userActivity.unknown'))
    expect(api.startActivitySync).not.toHaveBeenCalled()
  })
  it('shows a confirmed empty inventory as zero', async () => {
    expect(await read('2026-11-20T00:00:00Z')).toBe('0')
  })
})
