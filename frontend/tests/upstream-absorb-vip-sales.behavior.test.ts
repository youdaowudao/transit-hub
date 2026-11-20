// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import VipView from '@/modules/admin/views/VipView.vue'
import SalesView from '@/modules/admin/views/SalesView.vue'
import { t } from '@/locales'
import { account, deferred, scope, salesEntries, salesSummary, vipRow, vipSummary } from './fixtures/upstreamAbsorb'
const api = vi.hoisted(() => ({ current: null as any, switching: null as any, vip: vi.fn(), search: vi.fn(), watch: vi.fn(), unwatch: vi.fn(), restore: vi.fn(), vipSave: vi.fn(), sales: vi.fn(), entries: vi.fn(), exclude: vi.fn(), include: vi.fn(), salesSave: vi.fn(), clipboard: vi.fn() }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); api.current = ref(null); api.switching = ref(false); return { useAdminAccounts: () => ({ currentAccount: api.current, isSwitching: api.switching, loadCurrentAccount: vi.fn() }) } })
vi.mock('@/modules/admin/api/vip', () => ({ getVipSummary: api.vip, searchVipUsers: api.search, watchVipUser: api.watch, unwatchVipUser: api.unwatch, restoreVipRecommendations: api.restore, saveVipSettings: api.vipSave }))
vi.mock('@/modules/admin/api/sales', () => ({ getSalesSummary: api.sales, getSalesEntries: api.entries, excludeSalesEntries: api.exclude, restoreSalesEntries: api.include, saveSalesSettings: api.salesSave }))
const wrappers: VueWrapper[] = []
const keep = (w: VueWrapper) => { wrappers.push(w); return w }
const globals = { stubs: { RouterLink: { template: '<a><slot /></a>' }, DashboardEChart: true } }
const button = (w: VueWrapper, text: string) => { const found = w.findAll('button').find(item => item.text() === text); expect(found, text).toBeDefined(); return found! }
const openVip = async () => { const w = keep(mount(VipView, { global: globals })); await flushPromises(); return w }
const openSales = async () => { const w = keep(mount(SalesView, { global: globals })); await flushPromises(); return w }
beforeEach(() => {
  vi.resetAllMocks(); api.current.value = account(); api.switching.value = false
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: api.clipboard.mockResolvedValue(undefined) } })
  api.vip.mockResolvedValue(vipSummary()); api.search.mockResolvedValue({ ...scope, items: [] })
  const changed = { ...scope, saved: true, restored: 2, summary: vipSummary() }
  api.watch.mockResolvedValue(changed); api.unwatch.mockResolvedValue(changed); api.restore.mockResolvedValue(changed); api.vipSave.mockResolvedValue(changed)
  api.sales.mockResolvedValue(salesSummary()); api.entries.mockResolvedValue(salesEntries())
  const affected = { ...scope, requested: 1, changed: 1, unchanged: 0, range: { from: '2026-11-19', to: '2026-11-19', days: 1 }, exclusionSummary: { channels: [], entries: 1, amount: 100, deductions: 0 } }
  api.exclude.mockResolvedValue(affected); api.include.mockResolvedValue(affected)
  api.salesSave.mockResolvedValue({ ...scope, saved: true, settings: { excludedChannels: [] } })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks() })

describe('VIP local interactions', () => {
  it('shows known low balance with unknown seven-day estimates and copies only to clipboard', async () => {
    const w = await openVip()
    const row = w.get('tbody tr')
    expect(row.text()).toContain('快用完'); expect(row.text()).toContain('5'); expect(row.text()).toContain('未核实')
    expect(row.text()).not.toContain('2天')
    expect(w.get('input[type="checkbox"]').element).toHaveProperty('checked', false)
    await button(w, t('admin.vip.copy')).trigger('click'); await flushPromises()
    expect(api.clipboard).toHaveBeenCalledExactlyOnceWith('Fixture copied reminder')
    expect(api.vipSave).not.toHaveBeenCalled(); expect(api.watch).not.toHaveBeenCalled()
    expect(w.text()).toContain(t('admin.vip.copyHelp'))
  })
  it('handles zero search results and adds a local customer with a note', async () => {
    const w = await openVip()
    await w.get('form').trigger('submit'); await flushPromises()
    expect(w.text()).toContain(t('admin.vip.searchEmpty'))
    api.search.mockResolvedValue({ ...scope, items: [{ siteUserId: 'new-user', email: 'new@example.test', username: '', balance: null, watched: false, dismissed: false }] })
    await w.get('input[placeholder]').setValue('new'); await w.get('form').trigger('submit'); await flushPromises()
    await w.findAll('button').find(item => item.text() === t('admin.vip.watch'))!.trigger('click')
    const noteForm = w.findAll('form')[1]!
    await noteForm.get('input').setValue('Manual local note'); await noteForm.trigger('submit'); await flushPromises()
    expect(api.watch).toHaveBeenCalledWith(scope, 'new-user', 'Manual local note', expect.any(AbortSignal))
  })
  it('removes and restores recommendation suppression locally', async () => {
    const w = await openVip()
    await button(w, t('admin.vip.remove')).trigger('click'); await flushPromises()
    expect(api.unwatch).toHaveBeenCalledWith(scope, 'customer-1', expect.any(AbortSignal))
    await button(w, t('admin.vip.restore')).trigger('click'); await flushPromises()
    expect(api.restore).toHaveBeenCalledWith(scope, expect.any(AbortSignal))
    expect(w.text()).toContain(t('admin.vip.actionsHelp'))
  })
  it.each(['保存数据库失败，原设置仍保留。', '保存结果尚未确认；请先重新读取。'])('preserves draft and list after %s', async message => {
    api.vipSave.mockRejectedValue(new Error(message))
    const w = await openVip()
    const dailyTop = w.get('input[type="number"]'); await dailyTop.setValue('12')
    await w.findAll('form').at(-1)!.trigger('submit'); await flushPromises()
    expect(w.text()).toContain(message); expect(dailyTop.element).toHaveProperty('value', '12')
    expect(w.text()).toContain('vip@example.test'); expect(api.vipSave.mock.calls[0]![1]).toMatchObject({ alertsEnabled: false, dailyTop: 12, botIds: [] })
  })
  it('saves disabled reminders with one optional target without any sending operation', async () => {
    const w = await openVip()
    await w.findAll('form').at(-1)!.get('select').setValue('bot-1')
    await w.findAll('form').at(-1)!.trigger('submit')
    await flushPromises()
    expect(api.vipSave).toHaveBeenCalledExactlyOnceWith(scope, expect.objectContaining({ alertsEnabled: false, botIds: ['bot-1'] }), expect.any(AbortSignal))
    expect(w.text()).toContain(t('admin.vip.settingsSaved'))
    expect(api.watch).not.toHaveBeenCalled()
    expect(api.clipboard).not.toHaveBeenCalled()
  })
  it.each(['success', 'failure'] as const)('discards a late VIP settings write %s after recreation', async outcome => {
    const pending = deferred<any>()
    api.vipSave.mockReturnValueOnce(pending.promise)
    const w = await openVip()
    await w.get('input[type="number"]').setValue('12')
    await w.findAll('form').at(-1)!.trigger('submit')
    const signal = api.vipSave.mock.calls[0]![2]
    api.current.value = { ...account(), createdAt: '2026-11-20T04:00:00Z' }
    await flushPromises()
    expect(signal.aborted).toBe(true)
    if (outcome === 'success') pending.resolve({ ...scope, saved: true, summary: vipSummary({ items: [vipRow({ email: 'stale-write@example.test' })] }) })
    else pending.reject(new Error('stale-write-error'))
    await flushPromises()
    expect(w.text()).not.toMatch(/stale-write@example|stale-write-error/)
    expect(w.text()).toContain('vip@example.test')
  })
  it('labels unknown delivery without claiming receipt and retains manual disabled/admin rows from the backend', async () => {
    api.vip.mockResolvedValue(vipSummary({ items: [vipRow({ role: 'admin', userStatus: 'disabled', status: 'disabled', lastAlertStatus: 'unknown' })] }))
    const w = await openVip()
    expect(w.get('tbody tr').text()).toContain('停用'); expect(w.get('tbody tr').text()).toContain('admin')
    expect(w.text()).toContain(t('admin.vip.unknownDelivery')); expect(w.text()).not.toContain(t('admin.vip.confirmedDelivery'))
  })
  it.each(['resolve', 'reject'])('discards a late old-workspace %s', async mode => {
    const pending = deferred<ReturnType<typeof vipSummary>>()
    api.vip.mockReturnValueOnce(pending.promise)
    const w = keep(mount(VipView, { global: globals })); await flushPromises()
    api.vip.mockResolvedValue(vipSummary({ items: [vipRow({ email: 'current@example.test' })] }))
    api.current.value = account('workspace-b'); await flushPromises()
    if (mode === 'resolve') pending.resolve(vipSummary({ items: [vipRow({ email: 'late@example.test' })] }))
    else pending.reject(new Error('late-old-error'))
    await flushPromises()
    expect(w.text()).toContain('current@example.test'); expect(w.text()).not.toMatch(/late@example|late-old-error/)
    expect(api.vip.mock.calls[0]![1].aborted).toBe(true)
  })
})

describe('Sales management interactions', () => {
  it.each([
    [20, null], [null, 8], [0, 0],
  ] as const)('keeps credits %s and deductions %s distinct in totals and chart data', async (credits, deductions) => {
    const base = salesSummary()
    api.sales.mockResolvedValue(salesSummary({ coverage: { ...base.coverage, hasKnownData: true, complete: credits === 0 && deductions === 0 }, totals: { ...base.totals, credits, deductions }, exclusions: { ...base.exclusions, amount: credits === null ? null : 0, deductions: deductions === null ? null : 0 }, daily: [{ day: '2026-11-19', credits, deductions, complete: credits === 0 && deductions === 0 }] }))
    const w = await openSales()
    const cards = w.findAll('section.grid > div')
    expect(cards[0]!.get('p.text-2xl').text()).toBe(credits === null ? t('admin.sales.unknown') : String(credits))
    expect(cards[1]!.get('p.text-2xl').text()).toBe(deductions === null ? t('admin.sales.unknown') : String(deductions))
    const chart = w.findComponent({ name: 'DashboardEChart' }).props('option')
    expect(chart.series[0].data).toEqual([credits])
    expect(chart.series[1].data).toEqual([deductions])
    const excluded = w.findAll('p').find(item => item.text().includes(t('admin.sales.excludedDeductions')))!
    expect(excluded.text()).toBe(`${t('admin.sales.exclusions', { entries: 0 })} ${credits === null ? t('admin.sales.unknown') : '0'} · ${t('admin.sales.excludedDeductions')} ${deductions === null ? t('admin.sales.unknown') : '0'}`)
  })
  it('shows partial exclusion amounts, negative deductions and unknown totals honestly', async () => {
    const w = await openSales()
    const rows = w.findAll('tbody').at(-1)!.findAll('tr')
    expect(rows[0]!.text()).toMatch(/120.*100.*20/)
    expect(rows[0]!.text()).toContain(t('admin.sales.entryStatus.partial'))
    expect(rows[1]!.text()).toContain('-8'); expect(rows[1]!.get('input').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain(t('admin.sales.noData'))
    expect(w.text()).toContain('日'); expect(w.text()).toContain(t('admin.sales.exclusionHelp'))
  })
  it('renders confirmed zero as zero and keeps undefined comparisons unknown', async () => {
    const base = salesSummary(); base.coverage.complete = true; base.coverage.hasKnownData = true; base.totals.credits = 0; base.totals.deductions = 0; base.totals.payers = 0
    api.sales.mockResolvedValue(base)
    const w = await openSales()
    expect(w.text()).toContain(t('admin.sales.complete')); expect(w.text()).not.toContain(t('admin.sales.noData'))
    const cards = w.findAll('section.grid > div')
    expect(cards[0]!.text()).toMatch(/正向有效入账\s*0/); expect(cards[3]!.text()).toContain('未核实')
  })
  it.each([null, 0])('renders the registered cohort %s as unknown or confirmed zero without conflating them', async registered => {
    const base = salesSummary()
    api.sales.mockResolvedValue(salesSummary({ totals: { ...base.totals, registered } }))
    const w = await openSales()
    const label = t('admin.sales.registered')
    const cohort = w.findAll('p').find(item => item.text().startsWith(label + ' '))!
    expect(cohort.text()).toBe(label + ' ' + (registered === null ? t('admin.sales.unknown') : '0'))
  })
  it.each([false, true])('renders missing affiliate amounts only when confirmed coverage is %s', async complete => {
    const base = salesSummary()
    api.sales.mockResolvedValue(salesSummary({ coverage: { ...base.coverage, complete, hasKnownData: true }, channels: [
      { channel: 'payment', rawAmount: 50, excludedAmount: 0, effectiveAmount: 50, channelExcluded: false },
      { channel: 'affiliate', rawAmount: complete ? 0 : null, excludedAmount: complete ? 0 : null, effectiveAmount: complete ? 0 : null, channelExcluded: false },
    ] }))
    const w = await openSales()
    const row = w.findAll('tbody tr').find(row => row.find('td').exists() && row.get('td').text() === t('admin.sales.channel.affiliate'))!
    const values = row.findAll('td').slice(1, 4).map(cell => cell.text())
    expect(values).toEqual(complete ? ['0', '0', '0'] : Array(3).fill(t('admin.sales.unknown')))
  })
  it.each(['7', '30', '90'])('requests %s closed days and applies management filters', async days => {
    const w = await openSales()
    await w.get('select').setValue(days); await w.findAll('form')[0]!.trigger('submit'); await flushPromises()
    expect(api.sales).toHaveBeenLastCalledWith(scope, { days: Number(days) }, expect.any(AbortSignal))
    const form = w.findAll('form')[1]!
    await form.get('select').setValue('admin_deduct'); await form.get('input[type="text"]').setValue('payer')
    await form.get('input[type="checkbox"]').setValue(true); await form.findAll('select')[1]!.setValue('50')
    await form.trigger('submit'); await flushPromises()
    expect(api.entries).toHaveBeenLastCalledWith(scope, { days: Number(days) }, expect.objectContaining({ channel: 'admin_deduct', q: 'payer', excluded: '1', pageSize: 50, page: 1 }), expect.any(AbortSignal))
  })
  it('uses explicit custom dates and paginates within the loaded range', async () => {
    const w = await openSales(); await w.get('select').setValue('custom')
    const dates = w.findAll('input[type="date"]'); await dates[0]!.setValue('2026-11-01'); await dates[1]!.setValue('2026-11-19')
    const custom = { from: '2026-11-01', to: '2026-11-19', days: 19 }
    api.sales.mockResolvedValue(salesSummary({ range: custom })); api.entries.mockResolvedValue(salesEntries({ range: custom }))
    await w.findAll('form')[0]!.trigger('submit'); await flushPromises()
    expect(api.sales).toHaveBeenLastCalledWith(scope, { from: custom.from, to: custom.to }, expect.any(AbortSignal))
    await button(w, t('admin.sales.nextPage')).trigger('click'); await flushPromises()
    expect(api.entries).toHaveBeenLastCalledWith(scope, { from: custom.from, to: custom.to }, expect.objectContaining({ page: 2 }), expect.any(AbortSignal))
  })
  it.each(['exclude', 'restore'])('sends only selected keys on %s and re-reads the original range', async action => {
    const w = await openSales()
    await w.findAll('tbody').at(-1)!.findAll('tr')[0]!.get('input').setValue(true)
    await button(w, t(action === 'exclude' ? 'admin.sales.exclude' : 'admin.sales.restore')).trigger('click'); await flushPromises()
    const called = action === 'exclude' ? api.exclude : api.include
    expect(called).toHaveBeenCalledWith(scope, [{ siteUserId: 'customer-1', day: '2026-11-19', channel: 'payment' }], expect.any(AbortSignal))
    expect(api.sales).toHaveBeenLastCalledWith(scope, { from: '2026-10-21', to: '2026-11-19' }, expect.any(AbortSignal))
    expect(api.entries).toHaveBeenLastCalledWith(scope, { from: '2026-10-21', to: '2026-11-19' }, expect.any(Object), expect.any(AbortSignal))
    expect(w.text()).toContain('2026-10-21 — 2026-11-19')
  })
  it('preserves selection and old rows on failure, and explains successful save followed by failed read', async () => {
    const w = await openSales(); const row = w.findAll('tbody').at(-1)!.findAll('tr')[0]!
    await row.get('input').setValue(true); api.exclude.mockRejectedValueOnce(new Error('明确没有保存，请重试'))
    await button(w, t('admin.sales.exclude')).trigger('click'); await flushPromises()
    expect(row.get('input').element).toHaveProperty('checked', true); expect(row.text()).toContain('120')
    api.sales.mockRejectedValueOnce(new Error('保存后的读取连接中断'))
    await button(w, t('admin.sales.exclude')).trigger('click'); await flushPromises()
    expect(w.text()).toContain(t('admin.sales.savedNeedsRefresh')); expect(w.text()).toContain('保存后的读取连接中断')
    expect(w.text()).not.toContain('本次没有保存')
  })
  it('saves only explicit channel exclusions and preserves the selection after failure', async () => {
    const w = await openSales()
    const channelLabel = w.findAll('label').find(label => label.text() === t('admin.sales.channel.admin_add'))!
    await channelLabel.get('input').setValue(true)
    api.salesSave.mockRejectedValueOnce(new Error('保存渠道失败，请重试。'))
    await button(w, t('admin.sales.saveSettings')).trigger('click')
    await flushPromises()
    expect(api.salesSave).toHaveBeenLastCalledWith(scope, { excludedChannels: ['admin_add'] }, expect.any(AbortSignal))
    expect(channelLabel.get('input').element).toHaveProperty('checked', true)
    expect(w.text()).toContain('保存渠道失败')
    api.salesSave.mockResolvedValue({ ...scope, saved: true, settings: { excludedChannels: ['admin_add'] } })
    api.sales.mockResolvedValue(salesSummary({ settings: { excludedChannels: ['admin_add'] } }))
    await button(w, t('admin.sales.saveSettings')).trigger('click')
    await flushPromises()
    expect(w.text()).toContain(t('admin.sales.settingsSaved'))
    expect(api.sales).toHaveBeenLastCalledWith(scope, { from: '2026-10-21', to: '2026-11-19' }, expect.any(AbortSignal))
  })
  it.each(['success', 'failure'] as const)('discards a late sales exclusion write %s after scope replacement', async outcome => {
    const pending = deferred<any>()
    api.exclude.mockReturnValueOnce(pending.promise)
    const w = await openSales()
    await w.findAll('tbody').at(-1)!.findAll('tr')[0]!.get('input').setValue(true)
    await button(w, t('admin.sales.exclude')).trigger('click')
    const signal = api.exclude.mock.calls[0]![2]
    api.current.value = { ...account(), createdAt: '2026-11-20T04:00:00Z' }
    await flushPromises()
    expect(signal.aborted).toBe(true)
    if (outcome === 'success') pending.resolve({ requested: 999, changed: 999, unchanged: 0, range: { from: '1999-01-01', to: '1999-01-01' } })
    else pending.reject(new Error('stale-sales-write-error'))
    await flushPromises()
    expect(w.text()).not.toMatch(/1999-01-01|stale-sales-write-error/)
    expect(api.sales).toHaveBeenCalledTimes(2)
  })
  it.each(['resolve', 'reject'])('discards late sales data/error after scope switch: %s', async mode => {
    const pending = deferred<ReturnType<typeof salesSummary>>()
    api.sales.mockReturnValueOnce(pending.promise)
    const w = keep(mount(SalesView, { global: globals })); await flushPromises()
    api.sales.mockResolvedValue(salesSummary({ totals: { ...salesSummary().totals, credits: 909 } }))
    api.current.value = account('workspace-b'); await flushPromises()
    if (mode === 'resolve') pending.resolve(salesSummary({ totals: { ...salesSummary().totals, credits: 818 } }))
    else pending.reject(new Error('late-sales-error'))
    await flushPromises(); expect(w.text()).not.toMatch(/818|late-sales-error/)
    expect(api.sales.mock.calls[0]![2].aborted).toBe(true)
  })
})
