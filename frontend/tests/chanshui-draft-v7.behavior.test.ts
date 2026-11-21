// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import { modelKey, modelReadReason, upstreamModelIds, waitingForModels } from '../../chanshui/web/lib/hub-draft'
import type { HubCatalog } from '../../chanshui/web/lib/hub-types'
import { chanshuiCatalog, chanshuiCatalogSession, clearChanshuiCatalogForSession, publishChanshuiCatalog } from '../src/modules/admin/composables/chanshuiCatalog'
import type { AdminGroupHealth } from '../src/modules/admin/types/connectionHealth'
import { approveFixtureStart } from './fixtures/chanshui-transfer'

vi.mock('../../chanshui/web/components/Select', () => ({
  default: (props: any) => (globalThis as any).React.createElement('select', props, props.children),
}))

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
const groups: HubCatalog['groups'] = [
  { id: 'z', name: 'Z', accounts: [{ targetId: 'first', accountId: '13', accountName: '较优先账号', priority: 0, models: 'local-model', protocol: 'chat' }] },
  { id: 'a', name: 'A', accounts: [] },
]
const prefKey = 'transithub.chanshui.sections.v1:workspace-v7-fixture'
let root: ReturnType<typeof createRoot>
let node: HTMLDivElement
let removeServer: () => void
// 上游模型读取的模拟回包：默认返回 remote-model；可改为失败或延迟放行。
let upstream: { models: unknown[]; fail?: string; failStatus?: number; gate?: Promise<void>; contextGate?: Promise<void>; contextOverride?: Record<string, unknown>; contextFail?: { message: string; status: number }; startGate?: Promise<void> } = { models: ['remote-model'] }
let requests: { op: string; params: Record<string, unknown> }[] = []
let generation = 1
let bank: string[] = ['题库模型A']
const host = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))

const act = async (work?: () => unknown) => React.act(async () => {
  await work?.()
  await new Promise(resolve => setTimeout(resolve, 0))
})
const settle = async () => { for (let i = 0; i < 5; i++) await act() }

beforeEach(() => {
  vi.stubGlobal('React', React)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  node = document.createElement('div')
  document.body.append(node)
  root = createRoot(node)
  localStorage.removeItem(prefKey)
  upstream = { models: ['remote-model'] }
  requests = []
  generation = 1
  bank = ['题库模型A']
  Element.prototype.scrollIntoView = vi.fn()
})

afterEach(async () => {
  await act(() => root.unmount())
  removeServer?.()
  node.remove()
  localStorage.removeItem(prefKey)
  delete (Element.prototype as any).scrollIntoView
  vi.unstubAllGlobals()
})

async function boot(catalog: HubCatalog = { workspaceId: 'workspace-v7-fixture', groups }) {
  const workspaceId = 'workspace-v7-fixture'
  const calls: string[] = []
  const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
  const server = (event: MessageEvent) => {
    const request = event.data
    if (request?.kind === 'chanshui.READY') {
      respond({ kind: 'chanshui.BOOT', v: 1, generation, workspaceId, visible: true, target: {}, catalog })
    }
    if (request?.kind !== 'chanshui.request') return
    if (!approveFixtureStart(request)) return
    calls.push(request.op)
    requests.push({ op: request.op, params: request.params ?? {} })
    const gen = generation
    if (request.op === 'models' && (upstream.gate || upstream.fail)) {
      const reply = upstream // 回包内容在请求发出时固定，放行后仍是当时账号的结果。
      void (async () => {
        await reply.gate
        if (reply.fail) respond({ kind: 'chanshui.response', v: 1, generation: gen, id: request.id, ok: false, error: { code: 'models', message: reply.fail, status: reply.failStatus ?? 503 } })
        else respond({ kind: 'chanshui.response', v: 1, generation: gen, id: request.id, ok: true, value: { models: reply.models } })
      })()
      return
    }
    if (request.op === 'context' && upstream.contextFail) {
      respond({ kind: 'chanshui.response', v: 1, generation: gen, id: request.id, ok: false, error: upstream.contextFail })
      return
    }
    if (request.op === 'context' && upstream.contextGate) {
      const gate = upstream.contextGate, ctx = { workspaceId, targetId: request.params.targetId, groupId: request.params.groupId, baseURL: 'https://fixture.invalid', protocol: 'chat' }
      void gate.then(() => respond({ kind: 'chanshui.response', v: 1, generation: gen, id: request.id, ok: true, value: { ...ctx, canRun: true, expectedContext: ctx } }))
      return
    }
    let value: unknown = {}
    if (request.op === 'runtime') value = { enabled: true, available: true, active: null }
    if (request.op === 'reports') value = { items: [] }
    if (request.op === 'reference-info') value = { supported_models: bank, fingerprint_count: 1 }
    if (request.op === 'targets') value = { items: [{ targetId: 'remote', accountName: '远端账号', groups: [{ id: 'remote', name: '远端组' }] }] }
    const context = { workspaceId, targetId: request.params.targetId, groupId: request.params.groupId, baseURL: 'https://fixture.invalid', protocol: 'chat' }
    if (request.op === 'context') value = { ...context, canRun: true, expectedContext: context, ...upstream.contextOverride }
    if (request.op === 'start' && upstream.startGate) {
      void upstream.startGate.then(() => respond({ kind: 'chanshui.response', v: 1, generation: gen, id: request.id, ok: true, value: { reportId: request.params.reportId, status: 'queued' } }))
      return
    }
    if (request.op === 'models') value = { models: upstream.models }
    if (request.op === 'start') value = { reportId: request.params.reportId, status: 'queued' }
    if (request.op === 'report') value = { reportId: request.params.reportId, status: 'done', processExited: true, model: 'local-model', createdAtMs: Date.now() }
    if (request.op === 'items') value = { items: [] }
    respond({ kind: 'chanshui.response', v: 1, generation: gen, id: request.id, ok: true, value })
  }
  window.addEventListener('message', server)
  removeServer = () => window.removeEventListener('message', server)
  await act(() => root.render(React.createElement(HubApp)))
  await settle()
  return calls
}

it('从工作区恢复全部取消，勾选后当场保存', async () => {
  localStorage.setItem(prefKey, '[]')
  await boot()
  const sections = [...node.querySelectorAll<HTMLInputElement>('.hub-checkbox input')]
  expect(sections.length).toBeGreaterThan(0)
  expect(sections.every(box => !box.checked)).toBe(true)
  await act(() => sections[0].click())
  expect(JSON.parse(localStorage.getItem(prefKey)!)).toEqual(['iq'])
})

it('按分组健康名单顺序选组，选中账号后读取上游模型，按下开始后再核验账号', async () => {
  const calls = await boot()
  const field = (label: string) => [...node.querySelectorAll('label')].find(l => l.firstChild?.textContent?.trim() === label)?.querySelector('select') as HTMLSelectElement
  const choose = async (label: string, value: string) => {
    const input = field(label)
    await act(() => { input.value = value; input.dispatchEvent(new Event('change', { bubbles: true })) })
    await settle()
  }
  const targetRead = () => calls.filter(op => ['targets', 'context', 'models', 'start'].includes(op))
  expect([...field('分组').options].map(o => o.value)).toEqual(['', 'z', 'a'])
  await choose('分组', 'z')
  expect(targetRead()).toEqual([])
  await choose('账号', 'first')
  // 名单里的 local-model 不再作为候选，模型来自上游读取。
  expect(node.querySelector<HTMLInputElement>('#model')?.value).toEqual('remote-model')
  expect(targetRead()).toEqual(['context', 'models'])
  const start = [...node.querySelectorAll('button')].find(button => button.textContent?.trim() === '开始检测')!
  expect(start?.disabled).toBe(false)
  await act(() => start.click())
  await settle()
  expect(targetRead()).toEqual(['context', 'models', 'context', 'start'])
  expect(requests.find(r => r.op === 'start')?.params).toMatchObject({ targetId: 'first', groupId: 'z', model: 'remote-model' })
})

const chooseIn = async (label: string, value: string) => {
  const select = [...node.querySelectorAll('label')].find(item => item.firstChild?.textContent?.trim() === label)!.querySelector('select')!
  await act(() => { select.value = value; select.dispatchEvent(new Event('change', { bubbles: true })) })
  await settle()
}
const upstreamCatalog: HubCatalog = { workspaceId: 'workspace-v7-fixture', groups: [
  { id: '2', name: 'GPT 满血正价PRO 0.26x', accounts: [
    { targetId: 't523', accountId: '523', accountName: 'A-【yeclaw】-0.08x', priority: 1, protocol: 'openai' },
    { targetId: 't600', accountId: '600', accountName: '另一个账号', priority: 2, protocol: 'openai' },
  ] },
] }

it('选中账号后向上游读取该账号的模型，上游返回哪些就列出哪些', async () => {
  upstream = { models: ['gpt-5.6-sol', { id: 'gpt-6.1-sol' }, 'gpt-5.6-sol', ''] }
  // 这个账号在名单里另有模型文本，题库也有模型（题库模型A）；下拉只能出现上游返回的模型。
  const withListModels = structuredClone(upstreamCatalog); withListModels.groups[0].accounts[0].models = '名单模型X, 名单模型Y'
  const calls = await boot(withListModels)
  await chooseIn('分组', '2')
  expect(calls.filter(op => ['context', 'models'].includes(op))).toEqual([])
  await chooseIn('账号', 't523')
  expect(calls.filter(op => ['context', 'models', 'start'].includes(op))).toEqual(['context', 'models'])
  expect(requests.find(r => r.op === 'context')?.params).toEqual({ targetId: 't523', groupId: '2' })
  expect(requests.find(r => r.op === 'models')?.params).toEqual({ targetId: 't523', groupId: '2', expectedContext: { workspaceId: 'workspace-v7-fixture', targetId: 't523', groupId: '2', baseURL: 'https://fixture.invalid', protocol: 'chat' } })
  const input = node.querySelector<HTMLInputElement>('#model')!
  expect(input.value).toBe('gpt-6.1-sol')
  await act(() => { input.focus(); input.dispatchEvent(new Event('focus')); input.click() })
  await settle()
  expect([...document.querySelectorAll('[role="option"]')].map(option => option.getAttribute('title'))).toEqual(['gpt-5.6-sol', 'gpt-6.1-sol'])
  expect(node.textContent).not.toContain('当前名单里没有此账号的模型记录')
  expect(node.textContent).toContain('选中账号后会向该账号的上游读取模型列表')
  expect(calls).not.toContain('start')
})

it('上游读取失败或没返回模型时说明原因，暂列题库模型并可手输，刷新后按上游结果更新', async () => {
  upstream = { models: [], fail: '上游模型列表格式无法读取，请检查该账号接口后重试' }
  const calls = await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const input = node.querySelector<HTMLInputElement>('#model')!
  const refresh = () => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!
  expect(input.value).toBe('')
  expect(input.disabled).toBe(false)
  expect(node.querySelector('[role="alert"]')?.textContent).toBe('读取此账号的模型列表未完成：读取上游模型时上游模型列表格式无法读取，请检查该账号接口后重试。可点模型框右侧的刷新按钮重新读取，也可以直接输入模型名称；本次读取不会开始检测。')
  expect(node.textContent).toContain('下拉里暂列的是检测题库的模型，不代表此账号支持。')
  await act(() => { input.focus(); input.click() })
  await settle()
  expect([...document.querySelectorAll('[role="option"]')].map(option => option.textContent ?? '').some(text => text.includes('题库模型A'))).toBe(true)
  upstream = { models: [] }
  await act(() => refresh().click())
  await settle()
  expect(calls.filter(op => op === 'models')).toHaveLength(2)
  expect(node.textContent).not.toContain('读取此账号的模型列表未完成')
  expect(node.textContent).toContain('此账号的上游没有返回可选择的模型，请检查上游模型列表后点刷新按钮重新读取。')
  expect(node.textContent).toContain('下拉里暂列的是检测题库的模型，不代表此账号支持。')
  // 空列表 → 手输 → 刷新成功：和 V3.1.29 一样按分组默认规则重新预选，空列表提示和题库兜底都消失。
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(() => { setter.call(input, '手输模型'); input.dispatchEvent(new Event('input', { bubbles: true })) })
  upstream = { models: ['gpt-6.1-sol'] }
  await act(() => refresh().click())
  await settle()
  expect(input.value).toBe('gpt-6.1-sol')
  expect(node.textContent).not.toContain('没有返回可选择的模型')
  expect(node.textContent).not.toContain('下拉里暂列的是检测题库的模型')
  expect(calls).not.toContain('start')
})

it('默认模型不在列表时提示已选第一项；提示只跟随最近一次成功读取，手输时消失', async () => {
  upstream = { models: ['other-model', 'another-model'] }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const input = () => node.querySelector<HTMLInputElement>('#model')!
  const notice = '本分组默认模型 6.1 sol 未在此账号列表中，已选择第一项；开始前可更换。'
  const refresh = async () => { await act(() => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.click()); await settle() }
  expect(input().value).toBe('other-model')
  expect(node.textContent).toContain(notice)
  // 手输其他名称：提示当场消失。
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(() => { setter.call(input(), 'another-model'); input().dispatchEvent(new Event('input', { bubbles: true })) })
  expect(input().value).toBe('another-model')
  expect(node.textContent).not.toContain(notice)
  // 刷新后默认模型出现在列表里：选中默认模型，不再提示。
  upstream = { models: ['x-model', 'gpt-6.1-sol'] }
  await refresh()
  expect(input().value).toBe('gpt-6.1-sol')
  expect(node.textContent).not.toContain(notice)
  // 刷新后默认模型不在、首项变化：选中新的首项并提示。
  upstream = { models: ['first-new', 'second-new'] }
  await refresh()
  expect(input().value).toBe('first-new')
  expect(node.textContent).toContain(notice)
  // 和 V3.1.29 一样，刷新后空列表会清空模型，不再提示「已选择第一项」。
  upstream = { models: [] }
  await refresh()
  expect(input().value).toBe('')
  expect(node.textContent).not.toContain(notice)
  expect(node.textContent).toContain('此账号的上游没有返回可选择的模型')
  // 刷新失败同样清空模型，说明读取失败并允许重选或手输。
  upstream = { models: ['first-new'] }
  await refresh()
  expect(node.textContent).toContain(notice)
  upstream = { models: [], fail: '上游暂时不可用' }
  await refresh()
  expect(input().value).toBe('')
  expect(node.textContent).not.toContain(notice)
  expect(node.textContent).toContain('读取此账号的模型列表未完成：读取上游模型时上游暂时不可用。')
})

it('模型读取失败时不带报告专用指引：登录失效、超时、连不上调度站分别说明；上游原文照样保留', async () => {
  upstream = { models: [], fail: '登录已失效，请重新登录后查看原报告。', failStatus: 401 }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const alert = node.querySelector('[role="alert"]')?.textContent ?? ''
  expect(alert).toContain('读取上游模型时登录已失效，请重新登录调度站后再选择账号。')
  expect(alert).not.toContain('原报告')
  // 上游错误正文里有同样的短语、但调度站没有返回 401 时，不当作调度站登录失效。
  upstream = { models: [], fail: '上游返回：登录已失效，请在上游后台重新登录', failStatus: 503 }
  await act(() => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.click())
  await settle()
  const upstreamAlert = node.querySelector('[role="alert"]')?.textContent ?? ''
  expect(upstreamAlert).toContain('读取上游模型时上游返回：登录已失效，请在上游后台重新登录。')
  expect(upstreamAlert).not.toContain('重新登录调度站')
  expect(modelReadReason('等待调度站结果超过45秒，请重试读取；若刚开始检测，请按原报告编号查询，部分请求可能已计费。')).toBe('等待调度站结果超过45秒')
  expect(modelReadReason('无法连接调度站。请检查网络后重试读取；若刚点击开始，请按原报告编号查询，部分请求可能已经计费。')).toBe('无法连接调度站，请检查网络')
  expect(modelReadReason('上游说：等待调度站结果超过45秒以后再试', 502)).toBe('上游说：等待调度站结果超过45秒以后再试')
  expect(modelReadReason('调度站返回的内容无法读取（HTTP 502）。请查看运行诊断后重试读取；不要重复新建检测。', 502)).toBe('调度站返回的内容无法读取（HTTP 502），请查看运行诊断')
  expect(modelReadReason('账号地址、分组或协议已变化，请刷新账号后重新读取模型。', 409)).toBe('账号地址、分组或协议已变化，请刷新账号后重新读取模型')
  expect(modelReadReason('')).toBe('原因没有返回，请到分组健康核对此账号的地址和密钥')
})

it('换账号后丢弃迟到的旧账号模型；手输后刷新，按新列表重新预选', async () => {
  let release!: () => void
  upstream = { models: ['旧账号模型'], gate: new Promise<void>(resolve => { release = resolve }) }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  expect(node.textContent).toContain('正在向上游读取此账号的模型列表，读完后才能编辑模型、开始检测或发送问题；本次只读取模型列表，不发送检测题目。')
  upstream = { models: ['新账号模型'] }
  await chooseIn('账号', 't600')
  await act(() => release())
  await settle()
  const input = node.querySelector<HTMLInputElement>('#model')!
  expect(input.value).toBe('新账号模型')
  expect(node.textContent).not.toContain('旧账号模型')
  await act(() => { input.focus(); input.click() })
  await settle()
  const listed = () => [...document.querySelectorAll('[role="option"]')].map(option => option.textContent ?? '')
  expect(listed().some(text => text.includes('新账号模型'))).toBe(true)
  expect(listed().some(text => text.includes('旧账号模型'))).toBe(false)
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(() => { setter.call(input, '手输模型'); input.dispatchEvent(new Event('input', { bubbles: true })) })
  upstream = { models: ['另一个模型'] }
  await act(() => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.click())
  await settle()
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('另一个模型')
})

it('读取模型期间模型框、刷新、开始检测和发送问题都不可用；读完后恢复', async () => {
  upstream = { models: ['remote-model'] }
  const calls = await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const button = (text: string) => [...node.querySelectorAll('button')].find(item => item.textContent?.trim() === text)!
  const input = node.querySelector<HTMLInputElement>('#model')!
  const refresh = () => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!
  const question = node.querySelector<HTMLTextAreaElement>('#question')!
  await act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(question, '读取中的问题'); question.dispatchEvent(new Event('input', { bubbles: true })) })
  expect(input.value).toBe('remote-model')
  expect(button('开始检测').disabled).toBe(false)
  expect(button('发送问题').disabled).toBe(false)
  // 已有模型和问题时刷新：先清空旧模型，等待回包时禁止编辑、刷新和提交。
  let release!: () => void
  upstream = { models: ['remote-model'], gate: new Promise<void>(resolve => { release = resolve }) }
  await act(() => refresh().click())
  await settle()
  expect(input.value).toBe('')
  expect(input.disabled).toBe(true)
  expect(refresh().disabled).toBe(true)
  expect(button('开始检测').disabled).toBe(true)
  expect(button('发送问题').disabled).toBe(true)
  await act(() => release())
  await settle()
  expect(input.value).toBe('remote-model')
  expect(input.disabled).toBe(false)
  expect(button('开始检测').disabled).toBe(false)
  expect(button('发送问题').disabled).toBe(false)
  expect(calls).not.toContain('start')
  // 读取刚要开始、还没有结果的那次渲染也算读取中。
  expect(waitingForModels(true, undefined)).toBe(true)
  expect(waitingForModels(true, 'loading')).toBe(true)
  expect(waitingForModels(true, 'done')).toBe(false)
  expect(waitingForModels(true, 'failed')).toBe(false)
  expect(waitingForModels(false, undefined)).toBe(false)
})

it('旧账号的核对回包迟到时不再读取它的模型', async () => {
  let release!: () => void
  upstream = { models: ['旧账号模型'], contextGate: new Promise<void>(resolve => { release = resolve }) }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  upstream = { models: ['新账号模型'] }
  await chooseIn('账号', 't600')
  await act(() => release())
  await settle()
  expect(requests.filter(r => r.op === 'models').map(r => r.params.targetId)).toEqual(['t600'])
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('新账号模型')
})

it('再次打开同一账号会重新读取并重新预选；名单原样重推不重复读取，账号移出名单后不再读取', async () => {
  upstream = { models: ['gpt-5.6-sol', 'gpt-6.1-sol'] }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const input = () => node.querySelector<HTMLInputElement>('#model')
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(() => { setter.call(input()!, '临时名称'); input()!.dispatchEvent(new Event('input', { bubbles: true })) })
  const models = () => requests.filter(r => r.op === 'models').length
  expect(models()).toBe(1)
  await act(() => host({ kind: 'chanshui.OPEN_TARGET', v: 1, generation: 1, target: { groupId: '2', targetId: 't523' } }))
  await settle()
  expect(models()).toBe(2)
  expect(input()!.value).toBe('gpt-6.1-sol')
  await act(() => host({ kind: 'chanshui.CATALOG', v: 1, generation: 1, catalog: structuredClone(upstreamCatalog) }))
  await settle()
  expect(models()).toBe(2)
  const removed = structuredClone(upstreamCatalog); removed.groups[0].accounts = removed.groups[0].accounts.filter(account => account.targetId !== 't523')
  await act(() => host({ kind: 'chanshui.CATALOG', v: 1, generation: 1, catalog: removed }))
  await settle()
  expect(models()).toBe(2)
  expect(input()).toBeNull()
  expect(node.textContent).toContain('这个分组或账号已不在当前名单。请重新选择；此时没有开始检测。')
  expect(requests.some(r => r.op === 'start')).toBe(false)
})

it('新一代 BOOT 预填同一账号时重新读取，不沿用上一代列表；名单迟到时到达后才读取', async () => {
  upstream = { models: ['上一代模型'] }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('上一代模型')
  let release!: () => void
  upstream = { models: ['新一代模型'], gate: new Promise<void>(resolve => { release = resolve }) }
  generation = 2
  await act(() => host({ kind: 'chanshui.BOOT', v: 1, generation: 2, workspaceId: 'workspace-v7-fixture', visible: true, target: { groupId: '2', targetId: 't523' }, catalog: upstreamCatalog }))
  await settle()
  expect(node.textContent).toContain('正在向上游读取此账号的模型列表')
  const input = node.querySelector<HTMLInputElement>('#model')!
  expect(input.value).toBe('')
  await act(() => { input.focus(); input.click() })
  await settle()
  expect([...document.querySelectorAll('[role="option"]')].map(option => option.textContent ?? '').some(text => text.includes('上一代模型'))).toBe(false)
  await act(() => release())
  await settle()
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('新一代模型')
  // 名单迟到：BOOT 不带名单时只记下预填，名单到达后才核对并读取。
  upstream = { models: ['名单到达后模型'] }
  generation = 3
  const before = requests.length
  await act(() => host({ kind: 'chanshui.BOOT', v: 1, generation: 3, workspaceId: 'workspace-v7-fixture', visible: true, target: { groupId: '2', targetId: 't600' }, catalog: null }))
  await settle()
  expect(requests.slice(before).filter(r => ['context', 'models'].includes(r.op))).toEqual([])
  await act(() => host({ kind: 'chanshui.CATALOG', v: 1, generation: 3, catalog: upstreamCatalog }))
  await settle()
  expect(requests.slice(before).filter(r => ['context', 'models'].includes(r.op)).map(r => `${r.op}:${r.params.targetId}`)).toEqual(['context:t600', 'models:t600'])
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('名单到达后模型')
})

it('检测宿主名单不再带探活模型，模型读取回包兼容字符串和 id 写法', () => {
  const member = (targetId: string, models?: string) => ({ targetId, id: targetId, name: targetId, models, modelHealth: [{ modelName: 'gpt-6.1-sol' }], unprobedModels: [{ modelName: '待探活' }] })
  try {
    publishChanshuiCatalog('workspace-v7-fixture', [{ id: '10', name: 'GPT混合分组', accounts: [member('439'), member('existing', 'a,b')] }] as AdminGroupHealth[], chanshuiCatalogSession())
    const accounts = chanshuiCatalog.value!.groups[0].accounts
    expect(accounts.every(account => !('probeModels' in account))).toBe(true)
    expect(accounts.find(account => account.targetId === 'existing')?.models).toBe('a,b')
  } finally {
    clearChanshuiCatalogForSession()
  }
  // 和 V3.1.29 一样，只去掉空字符串，保持原 ID 和顺序。
  expect(upstreamModelIds({ models: [' gpt-6.1-sol ', { id: 'route-a,route-b' }, 'gpt-6.1-sol', '', '  ', { id: 3 }, null, 'gpt-6.1-sol'] })).toEqual([' gpt-6.1-sol ', 'route-a,route-b', 'gpt-6.1-sol', '  '])
  expect(upstreamModelIds({ models: ['route-a', 'route-a '] })).toEqual(['route-a', 'route-a '])
  expect(upstreamModelIds({})).toEqual([])
  // 同一分组和账号在不同刷新计数下是不同的读取结果（重新打开、BOOT、刷新都会让计数加一）。
  expect(modelKey(1, '2', 't523')).not.toBe(modelKey(2, '2', 't523'))
  expect(modelKey(1, '2', 't523')).not.toBe(modelKey(1, '2', 't600'))
  expect(modelKey(1, '2', '')).toBe('')
  expect(upstreamModelIds(null)).toEqual([])
})

it('屏蔽清单里的账号不进入模型检测名单，取消屏蔽后重新出现', () => {
  const member = (id: string, priority: number) => ({ targetId: id, id, name: id, priority })
  const source = [
    { id: '10', name: 'GPT混合分组', accounts: [member('画图A', 1), member('普通', 2), member('画图B', 3)] },
    { id: '8', name: '专门画图分组', accounts: [member('画图A', 1), member('画图B', 2)] },
  ] as AdminGroupHealth[]
  const accountsOf = (groupId: string) => chanshuiCatalog.value?.groups.find(group => group.id === groupId)?.accounts.map(account => account.targetId)
  try {
    publishChanshuiCatalog('workspace-hidden', source, chanshuiCatalogSession(), new Set(['画图A', '画图B']))
    expect(accountsOf('10')).toEqual(['普通'])
    expect(chanshuiCatalog.value?.groups.find(group => group.id === '8')).toMatchObject({ accountsError: false, accounts: [] })
    publishChanshuiCatalog('workspace-hidden', source, chanshuiCatalogSession(), new Set(['画图B']))
    expect(accountsOf('10')).toEqual(['画图A', '普通'])
    expect(accountsOf('8')).toEqual(['画图A'])
    publishChanshuiCatalog('workspace-hidden', source, chanshuiCatalogSession())
    expect(accountsOf('10')).toEqual(['画图A', '普通', '画图B'])
  } finally {
    clearChanshuiCatalogForSession()
  }
})

it('分组健康名单保留左栏顺序，按数字 Priority 排列并隔离旧登录', () => {
  const member = (id: string, priority?: number) => ({ targetId: id, id, name: id, priority })
  const source = [
    { id: 'z', name: '先展示的组', accounts: [member('missing'), member('10', 10), member('b', 2), member('a', 2), member('zero', 0), member('b', 2)] },
    { id: 'a', name: '账号读取失败的组', accountsError: 'fixture.error', accounts: [member('not-current', 0)] },
  ] as AdminGroupHealth[]
  const originalSession = chanshuiCatalogSession()
  try {
    publishChanshuiCatalog('workspace-catalog', source, originalSession)
    expect(chanshuiCatalog.value?.groups.map(group => group.id)).toEqual(['z', 'a'])
    expect(chanshuiCatalog.value?.groups[0].accounts.map(account => account.targetId)).toEqual(['zero', 'a', 'b', '10', 'missing'])
    expect(chanshuiCatalog.value?.groups[1]).toMatchObject({ accountsError: true, accounts: [] })
    clearChanshuiCatalogForSession()
    publishChanshuiCatalog('workspace-catalog', source, originalSession)
    expect(chanshuiCatalog.value).toBeNull()
    publishChanshuiCatalog('next-workspace', source.slice(0, 1), chanshuiCatalogSession())
    expect(chanshuiCatalog.value?.workspaceId).toBe('next-workspace')
  } finally {
    clearChanshuiCatalogForSession()
  }
})

it('同一账号重新打开时，旧读取立即作废：旧核对回包不再读模型，旧模型回包不会填入', async () => {
  // 顺序一：旧核对回包在重新打开后、新读取开始前到达。
  let releaseContext!: () => void
  upstream = { models: ['新读取模型'], contextGate: new Promise<void>(resolve => { releaseContext = resolve }) }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  await act(() => { host({ kind: 'chanshui.OPEN_TARGET', v: 1, generation: 1, target: { groupId: '2', targetId: 't523' } }); releaseContext() })
  await settle()
  expect(requests.filter(r => r.op === 'models')).toHaveLength(1)
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('新读取模型')
  // 顺序二：旧模型回包在重新打开后到达。
  let releaseModels!: () => void
  upstream = { models: ['旧列表模型'], gate: new Promise<void>(resolve => { releaseModels = resolve }) }
  await act(() => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.click())
  await settle()
  upstream = { models: ['gpt-6.1-sol'] }
  await act(() => { host({ kind: 'chanshui.OPEN_TARGET', v: 1, generation: 1, target: { groupId: '2', targetId: 't523' } }); releaseModels() })
  await settle()
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('gpt-6.1-sol')
  expect(node.textContent).not.toContain('旧列表模型')
  expect(requests.some(r => r.op === 'start')).toBe(false)
})

it('同一页面切账号后，旧模型请求迟到失败不会覆盖新账号的核对失败', async () => {
  let releaseOldModels!: () => void
  upstream = { models: [], fail: '旧账号上游暂不可用', gate: new Promise<void>(resolve => { releaseOldModels = resolve }) }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  expect(requests.filter(r => r.op === 'models').map(r => r.params.targetId)).toEqual(['t523'])

  // 宿主请求队列已满时，新账号的核对会先收到 429，旧模型请求仍在进行。
  const queueFull = '正在处理其他页面操作，已显示内容仍可查看；请稍后重试读取。'
  upstream = { models: [], contextFail: { status: 429, message: queueFull } }
  await chooseIn('账号', 't600')
  const currentError = () => node.querySelector('[role="alert"]')?.textContent ?? ''
  expect(currentError()).toContain('核对账号时' + queueFull)

  await act(() => releaseOldModels())
  await settle()
  expect(currentError()).toContain('核对账号时' + queueFull)
  expect(node.textContent).not.toContain('旧账号上游暂不可用')
  expect(node.textContent).not.toContain('正在向上游读取此账号的模型列表')
  expect(node.querySelector<HTMLInputElement>('#model')!.disabled).toBe(false)
  expect(node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.disabled).toBe(false)
  expect(requests.filter(r => ['context', 'models', 'start'].includes(r.op)).map(r => `${r.op}:${r.params.targetId}`)).toEqual(['context:t523', 'models:t523', 'context:t600'])
})

it('读取未完成时账号移出名单、页面换代或卸载，旧读取不再继续也不写回', async () => {
  // 账号移出名单：旧核对回包到达后不再读模型。
  let release!: () => void
  upstream = { models: ['不应读取'], contextGate: new Promise<void>(resolve => { release = resolve }) }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const removed = structuredClone(upstreamCatalog); removed.groups[0].accounts = removed.groups[0].accounts.filter(account => account.targetId !== 't523')
  await act(() => { host({ kind: 'chanshui.CATALOG', v: 1, generation: 1, catalog: removed }); release() })
  await settle()
  expect(requests.filter(r => r.op === 'models')).toEqual([])
  // 页面换代：桥接丢弃上一代迟到的模型成功回包。
  await act(() => host({ kind: 'chanshui.CATALOG', v: 1, generation: 1, catalog: upstreamCatalog }))
  let releaseModels!: () => void
  upstream = { models: ['上一代模型'], gate: new Promise<void>(resolve => { releaseModels = resolve }) }
  await chooseIn('账号', 't523')
  expect(requests.filter(r => r.op === 'models')).toHaveLength(1)
  upstream = { models: ['新一代模型'] }
  generation = 2
  await act(() => { host({ kind: 'chanshui.BOOT', v: 1, generation: 2, workspaceId: 'workspace-v7-fixture', visible: true, target: { groupId: '2', targetId: 't523' }, catalog: upstreamCatalog }); releaseModels() })
  await settle()
  expect(node.textContent).not.toContain('读取此账号的模型列表未完成')
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('新一代模型')
  // 卸载：旧核对回包到达后不再读模型。
  let releaseLast!: () => void
  upstream = { models: ['卸载后不应读取'], contextGate: new Promise<void>(resolve => { releaseLast = resolve }) }
  await act(() => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.click())
  const before = requests.filter(r => r.op === 'models').length
  await act(() => { root.unmount(); releaseLast() })
  await settle()
  expect(requests.filter(r => r.op === 'models')).toHaveLength(before)
  root = createRoot(node) // 供收尾的 unmount 使用。
})

it('账号核对不通过时不读取模型：说明核对失败，暂列题库模型并可手输', async () => {
  upstream = { models: ['不应读取'], contextOverride: { canRun: false, reason: '此账号已停用，请到分组健康启用后再检测。' } }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  expect(requests.filter(r => r.op === 'models')).toEqual([])
  expect(node.querySelector('[role="alert"]')?.textContent).toBe('读取此账号的模型列表未完成：核对账号时此账号已停用，请到分组健康启用后再检测。可点模型框右侧的刷新按钮重新读取，也可以直接输入模型名称；本次读取不会开始检测。')
  expect(node.textContent).toContain('下拉里暂列的是检测题库的模型，不代表此账号支持。')
  expect(node.querySelector<HTMLInputElement>('#model')!.disabled).toBe(false)
  // 核对结果与所选账号的地址不一致：同样不读取模型。
  upstream = { models: ['不应读取'], contextOverride: { expectedContext: { workspaceId: 'workspace-v7-fixture', targetId: 't523', groupId: '2', baseURL: 'https://other.invalid', protocol: 'chat' } } }
  await act(() => node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.click())
  await settle()
  expect(requests.filter(r => r.op === 'models')).toEqual([])
  expect(node.querySelector('[role="alert"]')?.textContent).toContain('核对账号时服务器没有返回完整且一致的账号地址或检测配置。')
})

it('开始检测等待受理时模型框和刷新不可用，受理后打开原报告', async () => {
  let release!: () => void
  // 名单里这个账号的协议是 openai，核对结果按同一协议返回，开始检测才会通过协议校验。
  const openai = { workspaceId: 'workspace-v7-fixture', targetId: 't523', groupId: '2', baseURL: 'https://fixture.invalid', protocol: 'openai' }
  upstream = { models: ['other-model'], startGate: new Promise<void>(resolve => { release = resolve }), contextOverride: { protocol: 'openai', expectedContext: openai } }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const start = [...node.querySelectorAll('button')].find(button => button.textContent?.trim() === '开始检测')!
  await act(() => start.click())
  await settle()
  expect(requests.some(r => r.op === 'start')).toBe(true)
  expect(node.querySelector<HTMLInputElement>('#model')!.disabled).toBe(true)
  expect(node.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.disabled).toBe(true)
  const reads = requests.filter(r => r.op === 'models').length
  await act(() => release())
  await settle()
  const reportId = requests.find(r => r.op === 'start')?.params.reportId
  expect(requests.some(r => r.op === 'report' && r.params.reportId === reportId)).toBe(true)
  expect(requests.filter(r => r.op === 'models')).toHaveLength(reads)
})

it('预选优先用本次核对返回的分组名：名单里仍是旧名时，也按新的 GPT 分组选默认模型', async () => {
  const renamed = structuredClone(upstreamCatalog); renamed.groups[0].name = '旧名分组'
  upstream = { models: ['x-model', 'gpt-6.1-sol'], contextOverride: { groupName: 'GPT 新名分组' } }
  await boot(renamed)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('gpt-6.1-sol')
  expect(node.textContent).toContain('旧名分组')
})

it('上游模型 ID 带首尾空格时原样列出，选中后开始检测按原值发送', async () => {
  const openai = { workspaceId: 'workspace-v7-fixture', targetId: 't523', groupId: '2', baseURL: 'https://fixture.invalid', protocol: 'openai' }
  upstream = { models: ['route-a ', 'route-a'], contextOverride: { protocol: 'openai', expectedContext: openai } }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  const input = node.querySelector<HTMLInputElement>('#model')!
  expect(input.value).toBe('route-a ')
  await act(() => { input.focus(); input.click() })
  await settle()
  expect([...document.querySelectorAll('[role="option"]')].map(option => option.getAttribute('title'))).toEqual(['route-a ', 'route-a'])
  await act(() => [...node.querySelectorAll('button')].find(button => button.textContent?.trim() === '开始检测')!.click())
  await settle()
  expect(requests.find(r => r.op === 'start')?.params.model).toBe('route-a ')
})

it('上游和题库都没有模型时，按下拉实际选项说明没有可选模型并提示手输', async () => {
  upstream = { models: [] }
  await boot(upstreamCatalog)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  expect(node.textContent).toContain('下拉里暂列的是检测题库的模型')
  bank = []
  generation = 2
  await act(() => host({ kind: 'chanshui.BOOT', v: 1, generation: 2, workspaceId: 'workspace-v7-fixture', visible: true, target: { groupId: '2', targetId: 't523' }, catalog: upstreamCatalog }))
  await settle()
  expect(requests.filter(r => r.op === 'reference-info')).toHaveLength(2)
  expect(node.textContent).not.toContain('下拉里暂列的是检测题库的模型')
  expect(node.textContent).toContain('下拉里没有可选模型，可以直接在模型框输入模型名称。')
  expect(node.querySelector<HTMLInputElement>('#model')!.disabled).toBe(false)
})

it('同一账号在两个分组间切换：每次读取带各自的分组，旧分组的迟到回包不覆盖新分组的预选', async () => {
  const twoGroups = structuredClone(upstreamCatalog)
  twoGroups.groups.push({ id: '3', name: 'Claude 分组', accounts: [{ targetId: 't523', accountId: '523', accountName: 'A-【yeclaw】-0.08x', priority: 1, protocol: 'openai' }] })
  let release!: () => void
  upstream = { models: ['gpt-6.1-sol'], gate: new Promise<void>(resolve => { release = resolve }) }
  await boot(twoGroups)
  await chooseIn('分组', '2')
  await chooseIn('账号', 't523')
  upstream = { models: ['x-model', 'claude-opus-5-5'] }
  await chooseIn('分组', '3')
  await chooseIn('账号', 't523')
  await act(() => release())
  await settle()
  expect(requests.filter(r => r.op === 'models').map(r => r.params.groupId)).toEqual(['2', '3'])
  expect(node.querySelector<HTMLInputElement>('#model')!.value).toBe('claude-opus-5-5')
})
