// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useUpstreamSites } from '@/modules/admin/composables/useUpstreamSites'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import type { SyncStreamEvent, UpstreamSiteResponse } from '@/modules/admin/types/upstream'

const harness = vi.hoisted(() => ({
  listUpstreamSites: vi.fn(),
  streamSyncAllUpstreamSites: vi.fn(),
  syncUpstreamSite: vi.fn(),
  routerPush: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: harness.routerPush }),
}))

vi.mock('@/modules/admin/api/settings', () => ({
  getStrategySettings: vi.fn(async () => ({
    enableRefreshInterval: false,
    refreshInterval: 300,
  })),
}))

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  getConnectionHealthAdminGroups: vi.fn(async () => []),
}))

vi.mock('@/modules/admin/api/mySites', () => ({
  listRealConnections: vi.fn(async () => []),
}))

vi.mock('@/modules/admin/api/upstream', () => ({
  createUpstreamSite: vi.fn(),
  listUpstreamSites: harness.listUpstreamSites,
  removeUpstreamSite: vi.fn(async () => undefined),
  streamSyncAllUpstreamSites: harness.streamSyncAllUpstreamSites,
  syncAllUpstreamSites: vi.fn(async () => []),
  syncUpstreamSite: harness.syncUpstreamSite,
  updateSiteSettings: vi.fn(),
  updateUpstreamSite: vi.fn(),
  updateUpstreamSiteEnabled: vi.fn(),
}))

const metric = (value: number, display = value.toFixed(2)) => ({ value, display })

const siteFixture = (
  id: string,
  name: string,
  errorKey: string | null = null,
): UpstreamSiteResponse => ({
  id,
  name,
  baseUrl: `https://${id}.example.com`,
  platform: 'sub2api',
  requestedPlatform: 'sub2api',
  account: `${id}@example.com`,
  rechargeRate: 7,
  enabled: true,
  remark: `${name}备注`,
  status: 'connected',
  errorKey,
  metrics: {
    balance: metric(50),
    todayConsume: metric(id === 'site-failed' ? 20 : 10),
    historyRecharge: metric(100),
    group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' },
    groups: [],
  },
  settings: { balanceThreshold: null },
  lastSyncedAt: Date.parse('2026-08-28T02:30:00Z'),
})

const mountedWrappers: VueWrapper[] = []

const mountView = async () => {
  const wrapper = mount(UpstreamView, {
    global: {
      stubs: {
        Teleport: true,
        Tooltip: { template: '<span class="tooltip-stub"><slot /></span>' },
        SiteSettingsModal: {
          props: ['open', 'site'],
          emits: ['close'],
          template: '<div v-if="open" data-test="site-settings-probe">{{ site.name }}<button data-test="close-settings" @click="$emit(\'close\')">关闭</button></div>',
        },
      },
    },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const mountUpstreamSites = async () => {
  let upstreamSites!: ReturnType<typeof useUpstreamSites>
  const wrapper = mount(defineComponent({
    setup() {
      upstreamSites = useUpstreamSites()
      return () => h('div')
    },
  }))
  mountedWrappers.push(wrapper)
  await flushPromises()
  return upstreamSites
}

const findSiteCard = (wrapper: VueWrapper, siteName: string) => {
  const card = wrapper.findAll('div').find(item => (
    item.classes().includes('group')
    && item.classes().includes('bg-card')
    && item.text().includes(siteName)
  ))
  if (!card) throw new Error(`missing upstream card: ${siteName}`)
  return card
}

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

beforeEach(() => {
  const failedSite = siteFixture('site-failed', '失败站点', 'admin.upstream.errors.request')
  const healthySite = siteFixture('site-healthy', '正常站点')

  harness.listUpstreamSites.mockReset().mockResolvedValue([failedSite, healthySite])
  harness.streamSyncAllUpstreamSites.mockReset().mockImplementation(async (
    onEvent: (event: SyncStreamEvent) => void,
  ) => {
    onEvent({
      event: 'error',
      siteId: failedSite.id,
      site: failedSite,
      errorKey: 'admin.upstream.errors.network',
    })
    onEvent({ event: 'complete', siteId: '' })
  })
  harness.syncUpstreamSite.mockReset().mockResolvedValue(siteFixture('site-failed', '失败站点'))
  harness.routerPush.mockReset()
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
  vi.clearAllTimers()
  vi.useRealTimers()
})

describe('upstream refresh failure card behavior', () => {
  it('R1/R3 keeps the failed card visible and shows the latest stream error inline without a blocking overlay', async () => {
    const wrapper = await mountView()
    const failedCard = findSiteCard(wrapper, '失败站点')
    const healthyCard = findSiteCard(wrapper, '正常站点')

    expect(failedCard.find('.absolute.inset-0').exists()).toBe(false)
    expect(failedCard.text()).toContain('失败站点')
    expect(failedCard.text()).toContain('已连接')
    expect(failedCard.text()).toContain('50.00 USD')
    expect(failedCard.text()).toContain('更新时间')
    expect(failedCard.text()).toContain('网络或 CORS 请求失败，请检查站点地址与跨域配置。')
    expect(failedCard.text()).not.toContain('上游接口请求失败，请稍后重试。')
    expect(failedCard.text()).not.toContain('正在同步...')
    expect(failedCard.text()).not.toContain('同步完成')

    expect(healthyCard.text()).toContain('正常站点')
    expect(healthyCard.text()).not.toContain('同步失败')
    expect(healthyCard.text()).not.toContain('网络或 CORS 请求失败')
  })

  it('R2 leaves refresh, settings, edit and delete actions operable on the failed card', async () => {
    const wrapper = await mountView()
    const failedCard = findSiteCard(wrapper, '失败站点')

    expect(failedCard.find('.absolute.inset-0').exists()).toBe(false)
    const actions = failedCard.findAll('button')
    expect(actions).toHaveLength(4)

    await actions[1].trigger('click')
    await nextTick()
    expect(wrapper.get('[data-test="site-settings-probe"]').text()).toBe('失败站点关闭')
    expect(findSiteCard(wrapper, '失败站点').text()).toContain('网络或 CORS 请求失败')
    await wrapper.get('[data-test="close-settings"]').trigger('click')
    await nextTick()

    await actions[2].trigger('click')
    await nextTick()
    expect((wrapper.get('#upstream-site-name').element as HTMLInputElement).value).toBe('失败站点')
    expect(findSiteCard(wrapper, '失败站点').text()).toContain('网络或 CORS 请求失败')
    await wrapper.get('[role="dialog"]').find('button').trigger('click')
    await nextTick()

    await actions[3].trigger('click')
    await nextTick()
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('失败站点')
    expect(findSiteCard(wrapper, '失败站点').text()).toContain('网络或 CORS 请求失败')
    await wrapper.get('[role="alertdialog"]').findAll('button')[0].trigger('click')
    await nextTick()

    await actions[0].trigger('click')
    await flushPromises()
    expect(harness.syncUpstreamSite).toHaveBeenCalledWith('site-failed')
    expect(findSiteCard(wrapper, '正常站点').text()).not.toContain('同步失败')
  })

  it('R3 keeps the connected status and adds the latest failure reason in list mode', async () => {
    const wrapper = await mountView()

    await wrapper.get('button[aria-label="列表模式"]').trigger('click')
    await nextTick()

    const failedRow = wrapper.findAll('tbody tr').find(row => row.text().includes('失败站点'))
    if (!failedRow) throw new Error('missing failed site row')
    expect(failedRow.text()).toContain('已连接')
    expect(failedRow.text()).toContain('同步失败')
    expect(failedRow.text()).toContain('网络或 CORS 请求失败，请检查站点地址与跨域配置。')
    expect(failedRow.text()).not.toContain('上游接口请求失败，请稍后重试。')
  })

  it('R5 clears the previous stream error after a successful single-site retry', async () => {
    const syncResult = deferred<UpstreamSiteResponse>()
    harness.syncUpstreamSite.mockReturnValueOnce(syncResult.promise)
    const wrapper = await mountView()
    let failedCard = findSiteCard(wrapper, '失败站点')
    expect(failedCard.text()).toContain('网络或 CORS 请求失败')

    const refreshButton = failedCard.findAll('button')[0]
    await refreshButton.trigger('click')
    await nextTick()
    expect((refreshButton.element as HTMLButtonElement).disabled).toBe(true)

    await refreshButton.trigger('click')
    expect(harness.syncUpstreamSite).toHaveBeenCalledTimes(1)

    syncResult.resolve(siteFixture('site-failed', '失败站点'))
    await flushPromises()

    failedCard = findSiteCard(wrapper, '失败站点')
    expect(failedCard.text()).not.toContain('同步失败')
    expect(failedCard.text()).not.toContain('网络或 CORS 请求失败')
    expect(failedCard.text()).toContain('已连接')
    expect((failedCard.findAll('button')[0].element as HTMLButtonElement).disabled).toBe(false)

    await wrapper.get('button[aria-label="列表模式"]').trigger('click')
    await nextTick()
    const failedRow = wrapper.findAll('tbody tr').find(row => row.text().includes('失败站点'))
    if (!failedRow) throw new Error('missing retried site row')
    expect(failedRow.text()).toContain('已连接')
    expect(failedRow.text()).not.toContain('同步失败')
    expect(failedRow.text()).not.toContain('网络或 CORS 请求失败')
  })

  it('R5 replaces the previous stream error with the latest single-site request failure', async () => {
    const upstreamSites = await mountUpstreamSites()
    upstreamSites.siteSyncStates.value.set('site-failed', {
      phase: 'error',
      errorKey: 'admin.upstream.errors.network',
    })
    harness.syncUpstreamSite.mockRejectedValueOnce(new Error('admin.upstream.errors.request'))

    await expect(upstreamSites.refreshSingleSite('site-failed')).resolves.toBeUndefined()

    expect(upstreamSites.siteSyncStates.value.get('site-failed')).toEqual({
      phase: 'error',
      errorKey: 'admin.upstream.errors.request',
    })
    expect(upstreamSites.syncingSiteIds.value.has('site-failed')).toBe(false)
  })

  it.each([
    ['raw upstream message', new Error('401: upstream token=secret')],
    ['empty error message', new Error('')],
    ['non-error rejection', 'request failed'],
  ])('R5 maps an unsafe %s to the generic error key', async (_caseName, rejection) => {
    const upstreamSites = await mountUpstreamSites()
    upstreamSites.siteSyncStates.value.set('site-failed', {
      phase: 'error',
      errorKey: 'admin.upstream.errors.network',
    })
    harness.syncUpstreamSite.mockRejectedValueOnce(rejection)

    await upstreamSites.refreshSingleSite('site-failed')

    expect(upstreamSites.siteSyncStates.value.get('site-failed')).toEqual({
      phase: 'error',
      errorKey: 'admin.upstream.errors.unknown',
    })
  })

  it('R3 falls back to the site error when the stream event has no error key', async () => {
    harness.streamSyncAllUpstreamSites.mockImplementationOnce(async (
      onEvent: (event: SyncStreamEvent) => void,
    ) => {
      onEvent({
        event: 'error',
        siteId: 'site-failed',
        site: siteFixture('site-failed', '失败站点', 'admin.upstream.errors.request'),
      })
      onEvent({ event: 'complete', siteId: '' })
    })

    const wrapper = await mountView()
    const failedCard = findSiteCard(wrapper, '失败站点')
    expect(failedCard.text()).toContain('上游接口请求失败，请稍后重试。')
    expect(failedCard.text()).not.toContain('网络或 CORS 请求失败')
  })

  it('R3 shows the stream error when the site itself has no error key', async () => {
    const failedSite = siteFixture('site-failed', '失败站点')
    harness.listUpstreamSites.mockResolvedValueOnce([
      failedSite,
      siteFixture('site-healthy', '正常站点'),
    ])
    harness.streamSyncAllUpstreamSites.mockImplementationOnce(async (
      onEvent: (event: SyncStreamEvent) => void,
    ) => {
      onEvent({
        event: 'error',
        siteId: 'site-failed',
        site: failedSite,
        errorKey: 'admin.upstream.errors.network',
      })
      onEvent({ event: 'complete', siteId: '' })
    })

    const wrapper = await mountView()
    const failedCard = findSiteCard(wrapper, '失败站点')
    expect(failedCard.text()).toContain('网络或 CORS 请求失败，请检查站点地址与跨域配置。')
    expect(failedCard.text()).not.toContain('上游接口请求失败，请稍后重试。')
  })

  it('R4 preserves the blocking overlay while a site is syncing', async () => {
    harness.streamSyncAllUpstreamSites.mockImplementationOnce(async (
      onEvent: (event: SyncStreamEvent) => void,
    ) => {
      onEvent({ event: 'syncing', siteId: 'site-failed' })
    })

    const wrapper = await mountView()
    const overlay = findSiteCard(wrapper, '失败站点').get('.absolute.inset-0')
    expect(overlay.text()).toContain('正在同步...')
  })

  it('R4 preserves the done overlay and its two-second dismissal', async () => {
    vi.useFakeTimers()
    harness.streamSyncAllUpstreamSites.mockImplementationOnce(async (
      onEvent: (event: SyncStreamEvent) => void,
    ) => {
      onEvent({
        event: 'done',
        siteId: 'site-failed',
        site: siteFixture('site-failed', '失败站点'),
      })
      onEvent({ event: 'complete', siteId: '' })
    })

    const wrapper = await mountView()
    let failedCard = findSiteCard(wrapper, '失败站点')
    expect(failedCard.get('.absolute.inset-0').text()).toContain('同步完成')

    vi.advanceTimersByTime(2000)
    await nextTick()

    failedCard = findSiteCard(wrapper, '失败站点')
    expect(failedCard.find('.absolute.inset-0').exists()).toBe(false)
  })
})
