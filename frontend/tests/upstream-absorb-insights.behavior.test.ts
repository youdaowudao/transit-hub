// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import OperatingInsights from '@/modules/admin/components/dashboard/OperatingInsights.vue'
import { t } from '@/locales'
import { account, deferred, insights, scope } from './fixtures/upstreamAbsorb'
const api = vi.hoisted(() => ({ read: vi.fn(), current: null as any, switching: null as any }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); api.current = ref(null); api.switching = ref(false); return { useAdminAccounts: () => ({ currentAccount: api.current, isSwitching: api.switching }) } })
vi.mock('@/modules/admin/api/operatingInsights', () => ({ getOperatingInsights: api.read }))
const wrappers: VueWrapper[] = []
const setup = () => { const w = mount(OperatingInsights); wrappers.push(w); return w }
const button = (w: VueWrapper, key: string) => w.findAll('button').find(b => b.text() === t(`admin.dashboard.operating.${key}`))!
beforeEach(() => { vi.clearAllMocks(); api.current.value = account(); api.switching.value = false; api.read.mockResolvedValue(insights()) })
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount() })
describe('on-demand existing settlement insights', () => {
  it('does not read on mount or ordinary rerender and reads only on expand and explicit retry', async () => {
    const w = setup(); await flushPromises(); expect(api.read).not.toHaveBeenCalled()
    await w.vm.$forceUpdate(); await flushPromises(); expect(api.read).not.toHaveBeenCalled()
    await button(w, 'expand').trigger('click'); await flushPromises()
    expect(api.read).toHaveBeenCalledExactlyOnceWith(scope, expect.any(AbortSignal))
    expect(w.text()).toContain('700'); expect(w.text()).toContain('210'); expect(w.text()).toContain('280'); expect(w.text()).toContain('420'); expect(w.text()).toContain('60.0%')
    expect(w.text()).toContain('上游原始额度'); expect(w.text()).toContain('60')
    expect(w.text()).toContain(t('admin.dashboard.operating.originalObserved'))
    await button(w, 'retry').trigger('click'); await flushPromises(); expect(api.read).toHaveBeenCalledTimes(2)
    await button(w, 'collapse').trigger('click'); await flushPromises(); expect(api.read).toHaveBeenCalledTimes(2)
    expect(w.find('#operating-insights-content').exists()).toBe(false)
  })
  it('keeps unknowns, missing dates and each concrete reason instead of a false total or refill advice', async () => {
    const base = insights().siteEstimates[0]!
    api.read.mockResolvedValue(insights({ snapshotTotals: null, missingDates: ['2026-11-13'], siteEstimates: [
      { ...base, siteId: 'missing', name: 'Missing cost', averageRawCost: null, daysLeft: null, suggestedTopUp: null, reasons: ['缺少7日原始成本，请等待原日结完成。'] },
      { ...base, siteId: 'stale', name: 'Stale balance', daysLeft: null, suggestedTopUp: null, reasons: ['原同步观察时间超过2小时，请使用原同步入口更新。'] },
      { ...base, siteId: 'zero', name: 'Zero mean', averageRawCost: 0, daysLeft: null, suggestedTopUp: null, reasons: ['7日日均为0，暂不估计可用时间。'] },
      { ...base, siteId: 'disabled', name: 'Disabled site', enabled: false, daysLeft: null, suggestedTopUp: null, reasons: ['站点已停用，不建议补款。'] },
    ] }))
    const w = setup(); await button(w, 'expand').trigger('click'); await flushPromises()
    expect(w.text()).toContain(t('admin.dashboard.operating.totalsUnavailable')); expect(w.text()).toContain('2026-11-13')
    for (const text of ['缺少7日原始成本', '超过2小时', '7日日均为0', '站点已停用']) expect(w.text()).toContain(text)
    for (const row of w.findAll('tbody tr')) expect(row.text()).toContain(t('admin.dashboard.operating.unknown'))
    expect(w.text()).not.toContain(t('admin.dashboard.operating.topUp', { amount: '60' })); expect(w.text()).not.toContain('60.0%')
  })
  it('shows a concrete failure and recovers only after explicit retry', async () => {
    api.read.mockRejectedValueOnce(new Error('读取原日结失败；请检查本站数据库后重试。'))
    const w = setup(); await button(w, 'expand').trigger('click'); await flushPromises()
    expect(w.get('[role="alert"]').text()).toContain('读取原日结失败'); expect(api.read).toHaveBeenCalledOnce()
    await button(w, 'retry').trigger('click'); await flushPromises(); expect(w.find('[role="alert"]').exists()).toBe(false); expect(w.text()).toContain('Upstream fixture')
  })
  it.each(['success', 'failure'] as const)('aborts and discards late %s on a scope change and remains folded', async outcome => {
    const old = deferred<ReturnType<typeof insights>>(); api.read.mockReturnValueOnce(old.promise)
    const w = setup(); await button(w, 'expand').trigger('click'); const signal = api.read.mock.calls[0]![1]
    api.current.value = account('new-workspace'); await flushPromises(); expect(signal.aborted).toBe(true)
    if (outcome === 'success') old.resolve(insights()); else old.reject(new Error('old-workspace-error'))
    await flushPromises(); expect(w.find('#operating-insights-content').exists()).toBe(false); expect(w.text()).not.toContain('old-workspace-error'); expect(w.text()).not.toContain('Upstream fixture'); expect(api.read).toHaveBeenCalledOnce()
    await button(w, 'expand').trigger('click'); await flushPromises(); expect(api.read.mock.calls[1]![0]).toEqual({ ...scope, workspaceAdminAccountId: 'new-workspace' })
  })
  it('aborts on collapse and unmount without displaying a late result', async () => {
    const old = deferred<ReturnType<typeof insights>>(); api.read.mockReturnValueOnce(old.promise)
    const w = setup(); await button(w, 'expand').trigger('click'); const signal = api.read.mock.calls[0]![1]
    await button(w, 'collapse').trigger('click'); expect(signal.aborted).toBe(true); old.resolve(insights()); await flushPromises(); expect(w.text()).not.toContain('Upstream fixture')
    api.read.mockReturnValueOnce(new Promise(() => {})); await button(w, 'expand').trigger('click'); const next = api.read.mock.calls[1]![1]; w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); expect(next.aborted).toBe(true)
  })
})
