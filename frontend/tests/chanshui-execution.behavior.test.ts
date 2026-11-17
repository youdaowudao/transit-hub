// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import { reportPollDecision } from '../../chanshui/web/lib/hub-policy'
import { restorationItems, restorationReport } from './fixtures/chanshui-report-restoration'

vi.mock('../../chanshui/web/components/Select', () => ({ default: (props: any) => (globalThis as any).React.createElement('select', { value: props.value, onChange: props.onChange, disabled: props.disabled }, props.children) }))
const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
let root: ReturnType<typeof createRoot>, container: HTMLDivElement, dispose: (() => void) | undefined
const act = async (fn: () => unknown = () => undefined) => { await React.act(async () => { await fn(); await new Promise(r => setTimeout(r, 0)) }) }
const settle = async () => { for (let i = 0; i < 7; i++) await act() }
const button = (text: string) => [...container.querySelectorAll('button')].find(b => b.textContent?.includes(text))!
const field = (label: string) => [...container.querySelectorAll('label')].find(l => l.firstChild?.textContent?.trim() === label)?.querySelector('select') as HTMLSelectElement
const choose = async (label: string, value: string) => { const input = field(label); expect(input, label).toBeTruthy(); await act(() => { input.value = value; input.dispatchEvent(new Event('change', { bubbles: true })) }); await settle() }
beforeEach(() => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined)
  container = document.createElement('div'); document.body.append(container); root = createRoot(container)
})
afterEach(async () => { await act(() => root.unmount()); dispose?.(); dispose = undefined; container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

async function boot() {
  const state = { report: restorationReport(), items: restorationItems(3), failRead: false, failAfterCancel: false, calls: [] as { op: string; params: any }[] }
  const reply = (m: any) => window.dispatchEvent(new MessageEvent('message', { data: m, source: window, origin: location.origin }))
  const listener = (event: MessageEvent) => {
    const m = event.data
    if (m?.kind === 'chanshui.READY') { reply({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'workspace-a', target: {}, visible: true }); return }
    if (m?.kind !== 'chanshui.request') return
    state.calls.push({ op: m.op, params: m.params })
    let value: unknown = {}
    if (m.op === 'runtime') value = { enabled: true, available: true, active: null }
    if (m.op === 'targets') value = { items: [
      { targetId: 'account-a', accountName: '账号甲', groups: [{ id: 'group-a', name: '同名组' }] },
      { targetId: 'account-b', accountName: '账号乙', groups: [{ id: 'group-b', name: '同名组' }] },
    ] }
    if (m.op === 'settings') value = { timeoutSeconds: 120, totalTimeoutSeconds: 3600, concurrency: 32, maxOutputTokens: 4096, retries: 0, maxAttempts: 128, memoryLimitMiB: 256, revision: 1 }
    if (m.op === 'reports') value = { items: [state.report] }
    if (m.op === 'report') {
      if (state.failRead) { state.failRead = false; reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '另一个检测查询正在处理，请稍后重试', status: 429 } }); return }
      value = state.report
    }
    if (m.op === 'items') value = { items: state.items }
    if (m.op === 'cancel') { state.failRead = state.failAfterCancel; value = { reportId: state.report.reportId, status: 'stopping', processExited: false } }
    if (m.op === 'context') value = { targetId: m.params.targetId, accountName: '当前账号', groupId: m.params.groupId, groupName: '同名组', canRun: true, protocol: 'chat', expectedContext: {} }
    if (m.op === 'models') value = { models: ['model-a'] }
    reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: true, value })
  }
  window.addEventListener('message', listener); dispose = () => window.removeEventListener('message', listener)
  await act(() => root.render(React.createElement(HubApp))); await settle()
  return state
}

describe('检测执行入口与停止后的结果保持', () => {
  it('停止后最新查询繁忙仍保留原报告、回答和已展开题目', async () => {
    const s = await boot(); s.report.status = 'running'; s.report.processExited = false
    await act(() => button(s.report.accountName).click()); await settle()
    const fold = container.querySelector<HTMLDetailsElement>('details.question')!
    await act(() => fold.querySelector('summary')!.click()); expect(fold.open).toBe(true)
    s.failAfterCancel = true
    await act(() => button('停止检测').click()); await settle()
    expect(container.querySelector('.total-card')).toBeTruthy()
    expect(container.textContent).toContain('已保存回答1')
    expect(container.querySelector<HTMLDetailsElement>('details.question')?.open).toBe(true)
    expect(s.calls.filter(c => c.op === 'cancel')).toHaveLength(1)
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
  })
  it('按相同报告编号重读失败也不清空已经显示的回答', async () => {
    const s = await boot(); await act(() => button(s.report.accountName).click()); await settle()
    s.failRead = true
    await act(() => (container.querySelector('form.report-lookup') as HTMLFormElement).requestSubmit()); await settle()
    expect(container.querySelector('.total-card')).toBeTruthy()
    expect(container.textContent).toContain('已保存回答1')
    expect(container.textContent).toContain('另一个检测查询正在处理')
  })
  it('空入口先选分组再选本组账号，同名组按ID区分，换组清空账号和模型', async () => {
    const s = await boot()
    expect(field('分组').value).toBe(''); expect(field('账号').disabled).toBe(true)
    expect([...field('分组').options].map(o => o.value)).toEqual(['', 'group-a', 'group-b'])
    expect(s.calls.some(c => ['context', 'models', 'start'].includes(c.op))).toBe(false)
    await choose('分组', 'group-a')
    expect([...field('账号').options].map(o => o.value)).toEqual(['', 'account-a'])
    await choose('账号', 'account-a')
    expect(s.calls.filter(c => c.op === 'models')).toHaveLength(1)
    await choose('分组', 'group-b')
    expect(field('账号').value).toBe('')
    expect([...field('账号').options].map(o => o.value)).toEqual(['', 'account-b'])
    expect(container.textContent).not.toContain('当前账号 ·')
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
  })
  it('排队报告没有进程时继续等待，不误报检测程序异常退出', () => {
    const d = reportPollDecision({ status: 'queued', processExited: true }, null, 0)
    expect(d.poll).toBe(true); expect(d.message).toContain('等待'); expect(d.message).not.toContain('已退出')
  })
  it('排队报告显示前方数量且不把等待时间当检测耗时', async () => {
    const s = await boot()
    Object.assign(s.report, { status: 'queued', startedAtMs: 0, processExited: true, queuePosition: 1, itemCounts: { queued: 3 }, summary: {} })
    s.items = s.items.map(item => ({ ...item, status: 'queued', result: undefined }))
    await act(() => button(s.report.accountName).click()); await settle()
    expect(container.textContent).toContain('前面还有1份报告')
    expect(container.textContent).toContain('等待执行，尚未开始计时')
    expect(container.textContent).not.toContain('检测总耗时')
    expect(container.textContent).not.toContain('检测程序已退出')
    expect(button('取消等待')).toBeTruthy()
  })
  it('停止后保存状态失败仍保留再次停止并重试保存的入口', async () => {
    const s = await boot()
    Object.assign(s.report, { status: 'running', processExited: true, stopRequested: true, diagnostic: '程序已退出，但报告状态尚未保存，请重试保存。' })
    await act(() => button(s.report.accountName).click()); await settle()
    const retry = button('停止检测并重试保存状态')
    expect(retry).toBeTruthy(); expect(retry.disabled).toBe(false)
    expect(container.textContent).toContain('已保存回答1')
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
  })
})
