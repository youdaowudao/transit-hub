// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import type { SyncStreamEvent, UpstreamSiteResponse } from '@/modules/admin/types/upstream'

const harness = vi.hoisted(() => ({
  listUpstreamSites: vi.fn(),
  streamSyncAllUpstreamSites: vi.fn(),
  createUpstreamSite: vi.fn(),
  updateUpstreamSitePinned: vi.fn(),
  updateUpstreamSiteEnabled: vi.fn(),
  updateUpstreamSite: vi.fn(),
  updateSiteSettings: vi.fn(),
}))

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({
  getStrategySettings: vi.fn(async () => ({ enableRefreshInterval: false, refreshInterval: 300 })),
}))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: vi.fn(async () => []) }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: vi.fn(async () => []) }))
vi.mock('@/modules/admin/api/upstream', () => ({
  createUpstreamSite: harness.createUpstreamSite,
  listUpstreamSites: harness.listUpstreamSites,
  removeUpstreamSite: vi.fn(async () => undefined),
  streamSyncAllUpstreamSites: harness.streamSyncAllUpstreamSites,
  syncAllUpstreamSites: vi.fn(async () => []),
  syncUpstreamSite: vi.fn(),
  updateSiteSettings: harness.updateSiteSettings,
  updateUpstreamSite: harness.updateUpstreamSite,
  updateUpstreamSiteEnabled: harness.updateUpstreamSiteEnabled,
  updateUpstreamSitePinned: harness.updateUpstreamSitePinned,
}))

const metric = (value: number) => ({ value, display: value.toFixed(2) })
const singaporeToday = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Singapore', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date())

const siteFixture = (id: string, name: string, todayConsume: number, extra: Partial<UpstreamSiteResponse> = {}): UpstreamSiteResponse => ({
  id,
  name,
  baseUrl: `https://${id}.example.com`,
  platform: 'sub2api',
  requestedPlatform: 'sub2api',
  account: `${id}@example.com`,
  rechargeRate: 7,
  enabled: true,
  remark: '',
  status: 'connected',
  errorKey: null,
  metrics: {
    todayConsumeTimezone: 'Asia/Singapore',
    todayConsumeDate: singaporeToday,
    balance: metric(50),
    todayConsume: metric(todayConsume),
    historyRecharge: metric(100),
    group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' },
    groups: [],
  },
  settings: { balanceThreshold: null },
  lastSyncedAt: Date.parse('2026-11-01T02:30:00Z'),
  ...extra,
})

const mountedWrappers: VueWrapper[] = []

const mountView = async () => {
  const wrapper = mount(UpstreamView, {
    global: {
      stubs: {
        Teleport: true,
        Tooltip: { template: '<span class="tooltip-stub"><slot /></span>' },
        SiteSettingsModal: { props: ['open', 'site'], template: '<div v-if="open" data-test="site-settings-probe" />' },
      },
    },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const cards = (wrapper: VueWrapper) => wrapper.findAll('div').filter(item => item.classes().includes('group') && item.classes().includes('bg-card'))
const cardOrder = (wrapper: VueWrapper) => cards(wrapper).map(card => card.find('a').text())
const findCard = (wrapper: VueWrapper, name: string) => {
  const card = cards(wrapper).find(item => item.find('a').text() === name)
  if (!card) throw new Error(`missing card ${name}`)
  return card
}
const pinSwitch = (wrapper: VueWrapper) => wrapper.get('[role="switch"][aria-label="置顶站点"]')
const openEdit = async (wrapper: VueWrapper, name: string) => {
  await findCard(wrapper, name).findAll('button')[2]!.trigger('click')
  await nextTick()
}

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

beforeEach(() => {
  harness.listUpstreamSites.mockReset().mockResolvedValue([
    siteFixture('site-high', '高消费站', 20),
    siteFixture('site-low', '低消费站', 10),
    siteFixture('site-off', '停用置顶站', 99, { enabled: false, pinned: true }),
  ])
  harness.streamSyncAllUpstreamSites.mockReset().mockResolvedValue(undefined)
  harness.createUpstreamSite.mockReset()
  harness.updateUpstreamSitePinned.mockReset()
  harness.updateUpstreamSiteEnabled.mockReset()
  harness.updateUpstreamSite.mockReset()
  harness.updateSiteSettings.mockReset()
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('upstream site pinning', () => {
  it('saves pinning immediately from the edit dialog, moves the site first and shows 已置顶 left of the status', async () => {
    const wrapper = await mountView()
    expect(cardOrder(wrapper)).toEqual(['高消费站', '低消费站', '停用置顶站'])
    expect(wrapper.text()).not.toContain('已置顶')

    await openEdit(wrapper, '低消费站')
    const dialog = wrapper.get('[role="dialog"]')
    const switches = dialog.findAll('[role="switch"]')
    expect(switches.map(item => item.attributes('aria-label'))).toEqual(['启用站点', '置顶站点'])
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('false')
    expect(dialog.text()).toContain('开启后这个站点排到列表最前面，可以同时置顶多个站点。点一下立即生效，只改变本页排列顺序，不会修改主站。')

    const pending = deferred<UpstreamSiteResponse>()
    harness.updateUpstreamSitePinned.mockReturnValueOnce(pending.promise)
    await pinSwitch(wrapper).trigger('click')
    expect(harness.updateUpstreamSitePinned).toHaveBeenCalledWith('site-low', true)
    expect(harness.updateUpstreamSite).not.toHaveBeenCalled()
    expect(pinSwitch(wrapper).attributes('disabled')).toBeDefined()
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('false')
    expect(cardOrder(wrapper)[0]).toBe('高消费站')

    pending.resolve(siteFixture('site-low', '低消费站', 10, { pinned: true }))
    await flushPromises()
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[role="dialog"]').text()).toContain('已置顶：这个站点排在列表最前面，关闭后回到原来的位置；已停用的站点始终排在最后。只改变本页排列顺序，不会修改主站。')
    expect(cardOrder(wrapper)).toEqual(['低消费站', '高消费站', '停用置顶站'])

    const pinnedCard = findCard(wrapper, '低消费站')
    const label = pinnedCard.get('[data-test="site-pinned-label"]')
    expect(label.text()).toBe('已置顶')
    expect(label.attributes('title')).toBe('这个站点已置顶，排在列表最前面；可在“修改站点”里关闭置顶。')
    expect(label.classes()).toContain('text-muted-foreground')
    const statusRow = label.element.parentElement!
    expect(statusRow.lastElementChild!.textContent).toContain('已连接')
    expect(label.element.nextElementSibling?.textContent).toContain('已连接')
    expect(pinnedCard.findAll('button')).toHaveLength(4)
    expect(findCard(wrapper, '高消费站').find('[data-test="site-pinned-label"]').exists()).toBe(false)
    expect(findCard(wrapper, '停用置顶站').find('[data-test="site-pinned-label"]').exists()).toBe(false)
  })

  it('unpins back to the normal order and keeps the dialog open without submitting the form', async () => {
    harness.listUpstreamSites.mockResolvedValue([
      siteFixture('site-high', '高消费站', 20),
      siteFixture('site-low', '低消费站', 10, { pinned: true }),
    ])
    const wrapper = await mountView()
    expect(cardOrder(wrapper)).toEqual(['低消费站', '高消费站'])
    await openEdit(wrapper, '低消费站')
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('true')

    harness.updateUpstreamSitePinned.mockResolvedValueOnce(siteFixture('site-low', '低消费站', 10, { pinned: false }))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()
    expect(harness.updateUpstreamSitePinned).toHaveBeenCalledWith('site-low', false)
    expect(cardOrder(wrapper)).toEqual(['高消费站', '低消费站'])
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(harness.updateUpstreamSite).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="site-pinned-label"]').exists()).toBe(false)
  })

  it('shows a plain failure reason, keeps the order and lets the user retry', async () => {
    const wrapper = await mountView()
    await openEdit(wrapper, '低消费站')
    const detail = '保存本站资料失败：数据库连接中断；本站资料未保存，原数据库资料保留；本轮未发布新费用，连接恢复并核查后可重试；不会修改主站。'
    harness.updateUpstreamSitePinned.mockRejectedValueOnce(Object.assign(new Error('admin.upstream.errors.request'), { failure: { stage: 'save', errorDetail: detail } }))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()

    const alert = wrapper.get('[role="dialog"]').findAll('[role="alert"]').find(item => item.text().includes('置顶没有确认保存'))
    expect(alert?.text()).toBe(`置顶没有确认保存。原因：${detail} 页面上的排列先保持原样，不会修改主站；请按原因处理后再点开关重试。`)
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('false')
    expect(pinSwitch(wrapper).attributes('disabled')).toBeUndefined()
    expect(cardOrder(wrapper)).toEqual(['高消费站', '低消费站', '停用置顶站'])

    harness.updateUpstreamSitePinned.mockRejectedValueOnce(new Error('admin.upstream.errors.network'))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('置顶没有确认保存。原因：无法连接 TransitHub 后端，请检查网络后重试。 页面上的排列先保持原样')
    expect(wrapper.get('[role="dialog"]').text()).not.toContain('网络或 CORS')

    harness.updateUpstreamSitePinned.mockRejectedValueOnce(new Error('admin.upstream.errors.request'))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('置顶没有确认保存。原因：具体原因暂未确认，请刷新页面确认站点仍在列表中，再查看 TransitHub 后端日志。')
    expect(wrapper.get('[role="dialog"]').text()).not.toContain('上游接口请求失败')

    harness.updateUpstreamSitePinned.mockResolvedValueOnce(siteFixture('site-low', '低消费站', 10, { pinned: true }))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).not.toContain('置顶没有确认保存')
    expect(cardOrder(wrapper)[0]).toBe('低消费站')
  })

  it('does not claim nothing was saved when the database saved but running data did not update', async () => {
    const wrapper = await mountView()
    await openEdit(wrapper, '低消费站')
    const detail = '本站资料已保存，当前运行数据未更新：缓存连接中断；本轮新费用未发布，刷新网页无法恢复，请联系维护人员检查存储连接后恢复服务，不会修改主站。'
    harness.updateUpstreamSitePinned.mockRejectedValueOnce(Object.assign(new Error('admin.upstream.errors.request'), { failure: { stage: 'save', errorDetail: detail } }))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()
    const text = wrapper.get('[role="dialog"]').text()
    expect(text).toContain(`置顶没有确认保存。原因：${detail}`)
    expect(text).not.toContain('置顶没有保存')
    expect(text).not.toContain('原来的排列位置不变')
    expect(text).not.toContain('刷新页面可以看到')
  })

  it('keeps the result of a refresh that was already running when the site is pinned', async () => {
    const release = deferred<void>()
    harness.streamSyncAllUpstreamSites.mockImplementation(async (onEvent: (event: SyncStreamEvent) => void) => {
      onEvent({ event: 'syncing', siteId: 'site-low' })
      await release.promise
      onEvent({ event: 'done', siteId: 'site-low', site: siteFixture('site-low', '低消费站', 10, { pinned: false, metrics: { ...siteFixture('site-low', '低消费站', 10).metrics, balance: metric(77) } }) })
      onEvent({ event: 'complete', siteId: '' })
    })
    const wrapper = await mountView()
    expect(findCard(wrapper, '低消费站').text()).toContain('正在同步')

    await openEdit(wrapper, '低消费站')
    harness.updateUpstreamSitePinned.mockResolvedValueOnce(siteFixture('site-low', '低消费站', 10, { pinned: true }))
    await pinSwitch(wrapper).trigger('click')
    await flushPromises()
    expect(cardOrder(wrapper)[0]).toBe('低消费站')

    release.resolve()
    await flushPromises()
    const card = findCard(wrapper, '低消费站')
    expect(card.text()).toContain('77.00 USD')
    expect(card.text()).toContain('同步完成')
    expect(cardOrder(wrapper)[0]).toBe('低消费站')
    expect(card.get('[data-test="site-pinned-label"]').text()).toBe('已置顶')
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('true')
  })

  it('lets a new site be pinned on 确认新增 and leaves an unpinned create request unchanged', async () => {
    const wrapper = await mountView()
    const openAdd = async () => {
      const add = wrapper.findAll('button').find(item => item.text().includes('新增'))
      await add!.trigger('click')
      await nextTick()
    }
    await openAdd()
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.findAll('[role="switch"]').map(item => item.attributes('aria-label'))).toEqual(['置顶站点'])
    expect(dialog.text()).toContain('开启后，点“确认新增”添加成功时这个站点直接置顶，排在列表最前面。只改变本页排列顺序，不会修改主站。')
    await pinSwitch(wrapper).trigger('click')
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('true')
    expect(harness.updateUpstreamSitePinned).not.toHaveBeenCalled()

    await wrapper.get('#upstream-site-name').setValue('新置顶站')
    harness.createUpstreamSite.mockResolvedValueOnce(siteFixture('site-new', '新置顶站', 1, { pinned: true }))
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    expect(harness.createUpstreamSite).toHaveBeenCalledTimes(1)
    expect(harness.createUpstreamSite.mock.calls[0]).toEqual([expect.objectContaining({ name: '新置顶站' }), true])
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(cardOrder(wrapper)[0]).toBe('新置顶站')
    expect(findCard(wrapper, '新置顶站').get('[data-test="site-pinned-label"]').text()).toBe('已置顶')

    await openAdd()
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('false')
    await wrapper.get('#upstream-site-name').setValue('普通新站')
    harness.createUpstreamSite.mockResolvedValueOnce(siteFixture('site-plain', '普通新站', 0))
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    expect(harness.createUpstreamSite.mock.calls[1]).toHaveLength(1)
    expect(harness.createUpstreamSite.mock.calls[1]![0]).not.toHaveProperty('pinned')
    expect(cardOrder(wrapper).slice(0, 2)).toEqual(['新置顶站', '高消费站'])
  })

  it('keeps the pin choice after a failed create and resets it when the dialog closes', async () => {
    const wrapper = await mountView()
    const add = wrapper.findAll('button').find(item => item.text().includes('新增'))
    await add!.trigger('click')
    await nextTick()
    await pinSwitch(wrapper).trigger('click')
    await wrapper.get('#upstream-site-name').setValue('失败站')
    harness.createUpstreamSite.mockRejectedValueOnce(new Error('admin.upstream.errors.network'))
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('true')

    await wrapper.get('[role="dialog"]').find('button').trigger('click')
    await nextTick()
    await add!.trigger('click')
    await nextTick()
    expect(pinSwitch(wrapper).attributes('aria-checked')).toBe('false')
  })

  it('shows 已置顶 left of the status in list mode too', async () => {
    harness.listUpstreamSites.mockResolvedValue([
      siteFixture('site-high', '高消费站', 20),
      siteFixture('site-low', '低消费站', 10, { pinned: true }),
    ])
    const wrapper = await mountView()
    await wrapper.get('button[aria-label="列表模式"]').trigger('click')
    await nextTick()
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0]!.text()).toContain('低消费站')
    const label = rows[0]!.get('[data-test="site-pinned-label"]')
    expect(label.text()).toBe('已置顶')
    expect(label.element.nextElementSibling?.textContent).toContain('已连接')
    expect(rows[1]!.find('[data-test="site-pinned-label"]').exists()).toBe(false)
  })
})
