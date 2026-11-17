// @vitest-environment jsdom
import { flushPromises, mount, DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isProxy, ref } from 'vue'
import ChanshuiHost from '../src/modules/admin/components/chanshui/ChanshuiHost.vue'
import { chanshuiOpenEvent, useChanshuiLauncher } from '../src/modules/admin/composables/useChanshuiLauncher'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'

const state = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/modules/admin/api/chanshui', async load => ({ ...await load<typeof import('@/modules/admin/api/chanshui')>(), requestChanshui: state.request }))
let wrapper: VueWrapper | undefined
const origin = window.location.origin
const portal = () => new DOMWrapper(document.body)
const launch = async (targetId = 'account-a') => { window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: { targetId, accountName: targetId, groupId: 'gpt' } })); await flushPromises() }
const message = (frame: HTMLIFrameElement, data: unknown, source: Window | null = frame.contentWindow, eventOrigin = origin) => window.dispatchEvent(new MessageEvent('message', { data, source, origin: eventOrigin }))
const captureClonedPosts = (frame: HTMLIFrameElement) => {
  const delivered: Array<Record<string, any>> = []
  const cloneErrors: unknown[] = []
  const post = vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(payload => {
    // Browser postMessage runs the structured clone algorithm. Recording a Vue
    // Proxy with a plain spy hides a real DataCloneError, so validate every send.
    try { delivered.push(structuredClone(payload)) } catch (error) { cloneErrors.push(error) }
  })
  return { post, delivered, cloneErrors }
}
beforeEach(() => { state.request.mockReset().mockResolvedValue({ items: [] }); wrapper = mount(ChanshuiHost, { attachTo: document.body, props: { workspaceId: 'workspace-a' }, global: { stubs: { Teleport: false } } }) })
afterEach(() => { wrapper?.unmount(); useChanshuiLauncher().clear(); localStorage.removeItem(authTokenStorageKey); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); document.body.innerHTML = '' })
describe('唯一检测弹窗', () => {
  it('普通RPC已满12项时停止仍优先送达，不等待普通查询释放', async () => {
    await launch()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    const { post } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    const generation = post.mock.calls[0][0].generation
    let release!: (value: unknown) => void
    state.request.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    for (let i = 0; i < 12; i++) message(frame, { kind: 'chanshui.request', v: 1, generation, id: `ordinary-${i}`, op: 'report', params: { reportId: 'a'.repeat(32) } })
    await flushPromises()
    try {
      message(frame, { kind: 'chanshui.request', v: 1, generation, id: 'priority-stop', op: 'cancel', params: { reportId: 'a'.repeat(32) } })
      await flushPromises()
      expect(state.request.mock.calls.some(([op]) => op === 'cancel')).toBe(true)
    } finally { release({}); await flushPromises() }
  })
  it('有工作区时入口默认可见，运行状态接口失败不隐藏入口，也不预读接口', async () => {
    const launcher = useChanshuiLauncher()
    launcher.clear()
    state.request.mockRejectedValue(new Error('检测资源暂时不可用'))
    await launcher.load('workspace-a')
    expect(launcher.enabled.value).toBe(true)
    expect(state.request).not.toHaveBeenCalled()
    launcher.clear()
    expect(launcher.enabled.value).toBe(false)
    await launcher.load('')
    expect(launcher.enabled.value).toBe(false)
    expect(state.request).not.toHaveBeenCalled()
  })
  it('总入口BOOT与账号切换消息使用可克隆普通快照', async () => {
    window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: {} })); await flushPromises()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    const { post, delivered, cloneErrors } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    const account = ref({ targetId: 'account-b', groupId: 'claude', accountName: '账号B', groupName: '克劳德' })
    expect(isProxy(account.value)).toBe(true)
    window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: account.value })); await flushPromises()
    account.value.accountName = '发送后的修改'
    expect(cloneErrors).toEqual([])
    expect(delivered.find(m => m.kind === 'chanshui.BOOT')).toMatchObject({ workspaceId: 'workspace-a', target: { targetId: undefined, groupId: undefined, accountName: undefined, groupName: undefined } })
    expect(delivered.find(m => m.kind === 'chanshui.OPEN_TARGET')).toMatchObject({ target: { targetId: 'account-b', groupId: 'claude', accountName: '账号B', groupName: '克劳德' } })
    for (const [payload] of post.mock.calls) if (payload.target) expect(isProxy(payload.target)).toBe(false)
    expect(state.request).not.toHaveBeenCalled()
  })
  it('收起保留同一个iframe，恢复和切账号不新开实例，关闭卸载但不取消任务', async () => {
    await launch()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    const { post, cloneErrors } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    expect(post.mock.calls[0][0]).toMatchObject({ kind: 'chanshui.BOOT', target: { targetId: 'account-a' } })
    await portal().get('[aria-label="收起检测页"]').trigger('click')
    expect(portal().find('[data-testid="chanshui-minimized"]').exists()).toBe(true)
    expect(portal().get('iframe').element).toBe(frame)
    expect(post.mock.calls.some(([m]) => m.kind === 'chanshui.VISIBILITY' && m.visible === false)).toBe(true)
    await launch('account-b')
    expect(portal().get('iframe').element).toBe(frame)
    expect(portal().findAll('iframe')).toHaveLength(1)
    expect(post.mock.calls.some(([m]) => m.kind === 'chanshui.OPEN_TARGET' && m.target.targetId === 'account-b')).toBe(true)
    await portal().get('[aria-label="关闭检测页"]').trigger('click')
    expect(portal().find('iframe').exists()).toBe(false)
    expect(state.request).not.toHaveBeenCalled()
    expect(cloneErrors).toEqual([])
  })
  it('拒绝错误origin/source/代数和未知操作，工作区切换丢弃迟到响应', async () => {
    await launch()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    const { post, cloneErrors } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    const generation = post.mock.calls[0][0].generation
    const req = { kind: 'chanshui.request', v: 1, generation, id: 'test-1', op: 'targets', params: {} }
    message(frame, req, window)
    message(frame, req, frame.contentWindow, 'https://untrusted.invalid')
    message(frame, { ...req, generation: generation - 1 })
    await flushPromises(); expect(state.request).not.toHaveBeenCalled()
    message(frame, { ...req, op: 'execute-shell' }); await flushPromises()
    expect(state.request).not.toHaveBeenCalled()
    let resolve!: (value: unknown) => void
    state.request.mockImplementation(() => new Promise(r => { resolve = r }))
    message(frame, req); await flushPromises(); expect(state.request).toHaveBeenCalledTimes(1)
    await wrapper!.setProps({ workspaceId: 'workspace-b' })
    resolve({ items: [{ targetId: 'old-workspace' }] }); await flushPromises()
    expect(portal().find('iframe').exists()).toBe(false)
    expect(post.mock.calls.some(([m]) => m.kind === 'chanshui.response' && m.ok === true)).toBe(false)
    expect(cloneErrors).toEqual([])
  })
  it('未保存设置关闭需明确确认，收起不丢稿，已开始任务仍不被取消', async () => {
    await launch()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    const { post, cloneErrors } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    message(frame, { kind: 'chanshui.DIRTY', v: 1, generation: post.mock.calls[0][0].generation, dirty: true })
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    await portal().get('[aria-label="关闭检测页"]').trigger('click')
    expect(portal().find('iframe').exists()).toBe(true)
    await portal().get('[aria-label="收起检测页"]').trigger('click')
    expect(portal().get('iframe').element).toBe(frame)
    expect(state.request).not.toHaveBeenCalled()
    expect(cloneErrors).toEqual([])
  })
  it('连续切换和清空工作区只更新入口显示，不保留前一工作区的读取请求', async () => {
    const launcher = useChanshuiLauncher(); launcher.clear()
    await launcher.load('workspace-a')
    expect(launcher.enabled.value).toBe(true)
    await launcher.load('workspace-b')
    expect(launcher.enabled.value).toBe(true)
    await launcher.load('')
    expect(launcher.enabled.value).toBe(false)
    await launcher.load('workspace-b')
    launcher.clear()
    expect(launcher.enabled.value).toBe(false)
    expect(state.request).not.toHaveBeenCalled()
  })
  it('缺页超时指引整站升级，重新载入恢复同一个弹窗且不会开始检测', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await launch()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    await vi.advanceTimersByTimeAsync(9999)
    expect(portal().get('[role="status"]').text()).not.toContain('10秒内没有收到')
    await vi.advanceTimersByTimeAsync(1)
    expect(portal().get('[role="status"]').text()).toContain('10秒内没有收到检测页响应。请重新执行整站升级，并查看升级日志中的检测页面构建步骤；此时没有开始检测。')
    expect(portal().text()).not.toContain('检查检测静态页面是否已安装')
    const reload = portal().findAll('button').find(button => button.text() === '重新载入页面')!
    await reload.trigger('click')
    expect(portal().get('iframe').element).toBe(frame)
    expect(frame.src).toBe(`${origin}/chanshui/`)
    expect(portal().get('[role="status"]').text()).toBe('正在打开检测页…')
    expect(portal().findAll('button').some(button => button.text() === '重新载入页面')).toBe(false)
    expect(state.request).not.toHaveBeenCalled()
    const { delivered, cloneErrors } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    expect(delivered.find(m => m.kind === 'chanshui.BOOT')).toMatchObject({ workspaceId: 'workspace-a', target: { targetId: 'account-a' } })
    await vi.advanceTimersByTimeAsync(10000)
    expect(portal().find('[role="status"]').exists()).toBe(false)
    expect(state.request).not.toHaveBeenCalled()
    expect(cloneErrors).toEqual([])
  })
  it('请求缺少工作区时先明确拒绝，不发HTTP请求', async () => {
    const actual = await vi.importActual<typeof import('@/modules/admin/api/chanshui')>('@/modules/admin/api/chanshui')
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    await expect(actual.requestChanshui('save-settings', {}, undefined as unknown as string)).rejects.toThrow('没有绑定工作区')
    expect(fetch).not.toHaveBeenCalled()
  })
  it.each(['save-settings', 'delete-question'])('%s在A发起后切到B，实际HTTP仍带A的快照且不能写B', async op => {
    const actual = await vi.importActual<typeof import('@/modules/admin/api/chanshui')>('@/modules/admin/api/chanshui')
    localStorage.setItem(authTokenStorageKey, 'fixture-token')
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    state.request.mockImplementation(async (...args: any[]) => { await gate; return (actual.requestChanshui as (...p: any[]) => Promise<unknown>)(...args) })
    let serverWorkspace = 'workspace-a', writes = 0
    const fetch = vi.fn(async (_url: string, init: RequestInit) => {
      // An abort cannot undo a request already delivered to the server.
      const scope = new Headers(init.headers).get('X-Chanshui-Workspace')
      if (scope !== serverWorkspace) return new Response(JSON.stringify({ message: '工作区已切换，本次未修改任何内容，请重新打开页面。' }), { status: 409 })
      writes++; return new Response('{}', { status: 200 })
    })
    vi.stubGlobal('fetch', fetch)
    await launch()
    const frame = portal().get('iframe').element as HTMLIFrameElement
    const { post } = captureClonedPosts(frame)
    message(frame, { kind: 'chanshui.READY', v: 1 }); await flushPromises()
    message(frame, { kind: 'chanshui.request', v: 1, generation: post.mock.calls[0][0].generation, id: 'write-a', op, params: op === 'save-settings' ? { revision: 1 } : { id: 'Q1', revision: 1 } })
    await flushPromises()
    expect(state.request).toHaveBeenCalledTimes(1)
    serverWorkspace = 'workspace-b'
    await wrapper!.setProps({ workspaceId: 'workspace-b' })
    release(); await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(new Headers(fetch.mock.calls[0][1].headers).get('X-Chanshui-Workspace')).toBe('workspace-a')
    expect(writes).toBe(0)
    expect(post.mock.calls.some(([m]) => m.kind === 'chanshui.response' && m.ok === true)).toBe(false)
  })
})
