// @vitest-environment jsdom
import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import ChanshuiHost from '../src/modules/admin/components/chanshui/ChanshuiHost.vue'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'
import { chanshuiOpenEvent } from '../src/modules/admin/composables/useChanshuiLauncher'
import { chanshuiCatalog, chanshuiCatalogSession, clearChanshuiCatalogForSession, publishChanshuiCatalog } from '../src/modules/admin/composables/chanshuiCatalog'
import type { AdminGroupHealth } from '../src/modules/admin/types/connectionHealth'

const api = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/modules/admin/api/chanshui', async original => ({ ...await original<typeof import('@/modules/admin/api/chanshui')>(), requestChanshui: api.request }))
const workspace = 'catalog-workspace'
const storageKey = `transithub.chanshui.catalog.v1:${workspace}`
const fixture = () => ({ workspaceId: workspace, groups: [{ id: '10', name: '已存分组', accounts: [{ targetId: `sub2api:${workspace}:7`, accountId: '7', accountName: '已存账号', protocol: 'chat', priority: 1 }] }] })
const portal = () => new DOMWrapper(document.body)
let wrapper: VueWrapper | undefined
let confirmed = ref(true)
const mountHost = () => { wrapper = mount(ChanshuiHost, { attachTo: document.body, props: { workspaceId: workspace, isWorkspaceConfirmed: () => confirmed.value } as any, global: { stubs: { Teleport: false } } }) }
const launch = async (target = {}) => { window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: target })); await flushPromises() }
const boot = async () => {
  const frame = portal().get('iframe').element as HTMLIFrameElement
  const delivered: any[] = []
  vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(packet => { delivered.push(structuredClone(packet)) })
  window.dispatchEvent(new MessageEvent('message', { data: { kind: 'chanshui.READY', v: 1 }, origin: window.location.origin, source: frame.contentWindow }))
  await flushPromises()
  return { delivered, frame, packet: delivered.find(packet => packet.kind === 'chanshui.BOOT') }
}
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
beforeEach(() => {
  clearChanshuiCatalogForSession(); localStorage.clear()
  localStorage.setItem(authTokenStorageKey, 'catalog-fixture-token')
  confirmed = ref(true)
  api.request.mockReset().mockResolvedValue({ catalog: fixture() })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; clearChanshuiCatalogForSession(); localStorage.clear(); vi.restoreAllMocks(); document.body.innerHTML = ''; window.history.replaceState({}, '', '/') })

it('从未打开分组健康时直接读取名单并交给检测页，没有健康刷新或检测请求', async () => {
  mountHost(); await launch()
  expect(api.request.mock.calls.map(([op]) => op)).toEqual(['catalog'])
  expect((await boot()).packet.catalog).toEqual(fixture())
  expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual(fixture())
})

it('整页刷新后的空内存可恢复上次检测名单，打开不再读取主站', async () => {
  localStorage.setItem(storageKey, JSON.stringify(fixture()))
  mountHost(); await launch()
  expect((await boot()).packet.catalog).toEqual(fixture())
  expect(api.request).not.toHaveBeenCalled()
})

it('健康页已有内存继续直接用，不为补存储新增读取或保存', async () => {
  publishChanshuiCatalog(workspace, [{ id: '10', name: '内存分组', accounts: [{ id: '7', name: '内存账号', targetId: `sub2api:${workspace}:7` }] }] as AdminGroupHealth[], chanshuiCatalogSession())
  mountHost(); await launch()
  expect((await boot()).packet.catalog.groups[0].name).toBe('内存分组')
  expect(api.request).not.toHaveBeenCalled()
  expect(localStorage.getItem(storageKey)).toBeNull()
})

it('手动更新失败保留旧名单并显示具体原因，重试成功空名单替换原账号', async () => {
  localStorage.setItem(storageKey, JSON.stringify(fixture()))
  mountHost(); await launch(); await boot()
  api.request.mockRejectedValueOnce(new Error('读取分组10的账号失败：主站要求先确认公告'))
  await portal().get('button[aria-label="更新分组和账号"]').trigger('click'); await flushPromises()
  expect(portal().text()).toContain('主站要求先确认公告')
  expect(portal().text()).toContain('已保留上次名单')
  expect(chanshuiCatalog.value?.groups[0]?.accounts[0]?.accountName).toBe('已存账号')
  api.request.mockResolvedValueOnce({ catalog: { workspaceId: workspace, groups: [] } })
  await portal().get('button[aria-label="更新分组和账号"]').trigger('click'); await flushPromises()
  expect(chanshuiCatalog.value?.groups).toEqual([])
  expect(portal().text()).not.toContain('主站要求先确认公告')
  expect(JSON.parse(localStorage.getItem(storageKey)!).groups).toEqual([])
})

it('未确认工作区只显示等待壳，同ID确认后继续打开并清掉无法确认的账号预填', async () => {
  confirmed.value = false; mountHost()
  await launch({ targetId: `sub2api:${workspace}:7`, accountName: '不该显示的账号', groupId: '10' })
  expect(portal().find('iframe').exists()).toBe(false)
  expect(portal().text()).toContain('重新选择工作区')
  expect(portal().text()).not.toContain('不该显示的账号')
  expect(api.request).not.toHaveBeenCalled()
  confirmed.value = true; await flushPromises()
  const { packet } = await boot()
  expect(packet.catalog).toEqual(fixture())
  expect(packet.target.targetId).toBeUndefined()
})

it('目录读取不会被iframe READY递增的消息代次废弃', async () => {
  const pending = deferred<any>(); api.request.mockReturnValueOnce(pending.promise)
  mountHost(); await launch(); const { delivered } = await boot()
  expect(delivered[0].catalog).toBeNull()
  pending.resolve({ catalog: fixture() }); await flushPromises()
  expect(delivered.find(packet => packet.kind === 'chanshui.CATALOG')?.catalog).toEqual(fixture())
})

it('关闭后迟到的读取不得发布或写存储', async () => {
  const pending = deferred<any>(); api.request.mockReturnValueOnce(pending.promise)
  mountHost(); await launch()
  await portal().get('button[aria-label="关闭检测页"]').trigger('click')
  pending.resolve({ catalog: fixture() }); await flushPromises()
  expect(chanshuiCatalog.value).toBeNull()
  expect(localStorage.getItem(storageKey)).toBeNull()
})

it('同ID确认后自动消费分享报告，工作区不匹配的账号预填退回普通打开', async () => {
  window.history.replaceState({}, '', `/?detectorReport=${'a'.repeat(32)}`)
  confirmed.value = false; mountHost(); await flushPromises()
  expect(portal().find('iframe').exists()).toBe(false)
  confirmed.value = true; await flushPromises()
  expect((await boot()).packet.target.reportId).toBe('a'.repeat(32))
  await launch({ targetId: 'sub2api:another:7', accountName: '其他工作区名字', groupId: '10', groupName: '其他组' })
  expect(portal().text()).not.toContain('其他工作区名字')
  expect(portal().text()).not.toContain('其他组')
})

it.each(['workspace', 'token', 'confirmed'])('%s变化时直接读取结果不能写入任一工作区记录', async kind => {
  const pending = deferred<any>(); api.request.mockReturnValueOnce(pending.promise)
  mountHost(); await launch()
  if (kind === 'workspace') await wrapper!.setProps({ workspaceId: 'another' })
  if (kind === 'token') localStorage.setItem(authTokenStorageKey, 'new-fixture-token')
  if (kind === 'confirmed') { confirmed.value = false; await flushPromises() }
  pending.resolve({ catalog: fixture() }); await flushPromises()
  expect(chanshuiCatalog.value).toBeNull()
  expect(localStorage.getItem(storageKey)).toBeNull()
  expect(localStorage.getItem('transithub.chanshui.catalog.v1:another')).toBeNull()
})

it('健康页发布较新目录时迟到的轻读取不覆盖它，也不把健康目录补写进存储', async () => {
  const pending = deferred<any>(); api.request.mockReturnValueOnce(pending.promise)
  mountHost(); await launch(); await boot()
  publishChanshuiCatalog(workspace, [{ id: 'new', name: '健康页新目录', accounts: [] }] as unknown as AdminGroupHealth[], chanshuiCatalogSession())
  pending.resolve({ catalog: fixture() }); await flushPromises()
  expect(chanshuiCatalog.value?.groups[0]?.name).toBe('健康页新目录')
  expect(localStorage.getItem(storageKey)).toBeNull()
  expect(api.request).toHaveBeenCalledTimes(1)
})

it('收起后确认丢失，恢复不能显示旧iframe或旧账号名称', async () => {
  localStorage.setItem(storageKey, JSON.stringify(fixture()))
  mountHost(); await launch({ targetId: `sub2api:${workspace}:7`, accountName: '已确认账号', groupId: '10' }); await boot()
  await portal().get('button[aria-label="收起检测页"]').trigger('click')
  confirmed.value = false; await flushPromises()
  expect(portal().find('iframe').exists()).toBe(false)
  expect(portal().text()).not.toContain('已确认账号')
  expect(portal().text()).toContain('重新选择工作区')
})
