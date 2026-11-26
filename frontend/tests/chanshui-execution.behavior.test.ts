// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import { reportPollDecision } from '../../chanshui/web/lib/hub-policy'
import { restorationItems, restorationReport } from './fixtures/chanshui-report-restoration'
import { approveFixtureStart } from './fixtures/chanshui-transfer'

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
  localStorage.removeItem('transithub.chanshui.sections.v1:workspace-a')
  container = document.createElement('div'); document.body.append(container); root = createRoot(container)
})
afterEach(async () => { await act(() => root.unmount()); dispose?.(); dispose = undefined; container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

async function boot(target: Record<string, unknown> = {}) {
  const state = { recoverable: false, report: restorationReport(), items: restorationItems(3), failRead: false, failAfterCancel: false, rejectStart: false, storedStart: false, holdContextResponse: false, releaseContextResponse: undefined as (() => void) | undefined, holdStart: false, holdStartResponse: false, releaseStartResponse: undefined as (() => void) | undefined, startProtocol: 'chat', releaseStart: undefined as (() => void) | undefined, calls: [] as { op: string; params: any }[] }
  const reply = (m: any) => window.dispatchEvent(new MessageEvent('message', { data: m, source: window, origin: location.origin }))
  const catalog = { workspaceId: 'workspace-a', groups: [
    { id: 'group-a', name: '同名组', accounts: [{ targetId: 'account-a', accountId: 'a', accountName: '账号甲', models: 'model-a', protocol: 'chat' }] },
    { id: 'group-b', name: '同名组', accounts: [{ targetId: 'account-b', accountId: 'b', accountName: '账号乙', models: 'model-a', protocol: 'chat' }] },
  ] }
  const run = (m: any) => {
    if (!approveFixtureStart(m, state.startProtocol)) {
      reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '原来的检测选项已变化，本次没有发送问题。', status: 409, operationResult: 'not_started' } }); return
    }
    state.calls.push({ op: m.op, params: m.params })
    let value: unknown = {}
    if (m.op === 'runtime') value = { enabled: true, available: true, active: null, recoverable: state.recoverable, queueBlocked: state.recoverable, reason: state.recoverable ? '写入结束状态失败' : '' }
    if (m.op === 'settings') value = { timeoutSeconds: 120, totalTimeoutSeconds: 3600, concurrency: 32, maxOutputTokens: 4096, retries: 0, maxAttempts: 128, memoryLimitMiB: 256, revision: 1 }
    if (m.op === 'reports') value = { items: [state.report] }
    if (m.op === 'report') {
      if (state.failRead) { state.failRead = false; reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '另一个检测查询正在处理，请稍后重试', status: 429 } }); return }
      value = { ...state.report, reportId: m.params.reportId }
    }
    if (m.op === 'items') value = { items: state.items }
    // 选中账号后页面向上游读取模型；夹具按名单里该账号原有的模型回答。
    if (m.op === 'models') value = { models: ['model-a'] }
    if (m.op === 'cancel') { state.failRead = state.failAfterCancel; value = { reportId: state.report.reportId, status: 'stopping', processExited: false } }
    if (m.op === 'context') {
      const context = { workspaceId: 'workspace-a', targetId: m.params.targetId, groupId: m.params.groupId, baseURL: 'https://fixture.invalid', protocol: 'chat' }
      value = { ...context, accountName: '当前账号', groupName: '同名组', canRun: true, expectedContext: context }
      if (state.holdContextResponse) {
        state.releaseContextResponse = () => reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: true, value }); return
      }
    }
    if (m.op === 'start') {
      if (state.storedStart) {
        reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '已创建报告，但运行设置或等待状态无法确认；未开始检测，请按原编号查看，不要重复新建', status: 503, step: 'storage', operationResult: 'not_started', reportId: m.params.reportId } }); return
      }
      if (state.rejectStart) {
        reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '检测服务正在结束旧任务，本次未开始', status: 503, operationResult: 'not_started' } }); return
      }
      value = { reportId: m.params.reportId, accepted: true }
      if (state.holdStartResponse) {
        state.releaseStartResponse = () => reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: true, value }); return
      }
    }
    reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: true, value })
  }
  const listener = (event: MessageEvent) => {
    const m = event.data
    if (m?.kind === 'chanshui.READY') { reply({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'workspace-a', target, visible: true, catalog }); return }
    if (m?.kind !== 'chanshui.request') return
    if (m.op === 'start' && state.holdStart) { state.releaseStart = () => run(m); return }
    run(m)
  }
  window.addEventListener('message', listener); dispose = () => window.removeEventListener('message', listener)
  await act(() => root.render(React.createElement(HubApp))); await settle()
  return state
}

describe('检测执行入口与停止后的结果保持', () => {
  it.each([{}, { targetId: 'account-a', groupId: 'group-a' }])('两个入口首次勾选缓存，用户取消后换账号、热入口和重新检测保持取消', async target => {
    const s = await boot(target)
    const cache = () => [...container.querySelectorAll('label.hub-checkbox')].find(l => l.textContent === '缓存读写')!.querySelector('input')!
    expect(cache().checked).toBe(true)
    expect(container.textContent).toContain('缓存默认勾选')
    await act(() => cache().click()); expect(cache().checked).toBe(false)
    await choose('分组', 'group-b'); expect(cache().checked).toBe(false)
    await act(() => window.dispatchEvent(new MessageEvent('message', { data: { kind: 'chanshui.OPEN_TARGET', v: 1, generation: 1, target: { targetId: 'account-a', groupId: 'group-a' } }, source: window, origin: location.origin }))); await settle()
    expect(cache().checked).toBe(false)
    await act(() => button(s.report.accountName).click()); await settle()
    const modelReads = s.calls.filter(c => c.op === 'models').length
    await act(() => button('重新检测（新任务）').click()); await settle()
    expect(s.calls.filter(c => c.op === 'models')).toHaveLength(modelReads + 1)
    expect(container.querySelector<HTMLInputElement>('#model')?.value).toBe('model-a')
    expect(cache().checked).toBe(false)
    await act(() => cache().click()); expect(cache().checked).toBe(true)
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
  })

  it.each([true, false])('开始请求准确保留缓存选择=%s', async selected => {
    const s = await boot()
    const cache = [...container.querySelectorAll('label.hub-checkbox')].find(l => l.textContent === '缓存读写')!.querySelector('input')!
    if (!selected) await act(() => cache.click())
    await choose('分组', 'group-a'); await choose('账号', 'account-a')
    await act(() => button('开始检测').click()); await settle()
    const request = s.calls.find(c => c.op === 'start')!
    expect(request).toBeTruthy(); expect(request.params.selectedSectionIds.includes('cache')).toBe(selected)
    expect(container.querySelector('.total-card')).toBeTruthy()
  })
  it('首次请求的 503 明确未开始时清除待确认，下一次使用新编号重新核验', async () => {
    const s = await boot({ targetId: 'account-a', groupId: 'group-a' })
    const selectionChecks = s.calls.filter(c => c.op === 'context').length
    s.rejectStart = true
    await act(() => button('开始检测').click()); await settle()
    expect(container.textContent).toContain('检测服务正在结束旧任务')
    expect(container.textContent).not.toContain('用原编号重试受理')
    expect(button('开始检测').disabled).toBe(false)
    s.rejectStart = false
    await act(() => button('开始检测').click()); await settle()
    expect(s.calls.filter(c => c.op === 'context')).toHaveLength(selectionChecks + 2)
    const ids = s.calls.filter(c => c.op === 'start').map(c => c.params.reportId)
    expect(ids).toHaveLength(2); expect(ids[0]).not.toBe(ids[1])
  })
  it('首次明确没开始但已创建记录的 503 保留原编号，只读查询可以打开该报告', async () => {
    const s = await boot({ targetId: 'account-a', groupId: 'group-a' })
    const selectionChecks = s.calls.filter(c => c.op === 'context').length
    s.storedStart = true
    await act(() => button('开始检测').click()); await settle()
    const id = s.calls.find(c => c.op === 'start')?.params.reportId
    const pending = container.querySelector('[aria-label="普通检测受理"]')
    expect(pending?.textContent).toContain(id)
    expect(container.textContent).toContain('请按原编号查看')
    expect(button('开始检测').disabled).toBe(true)
    // 受理结果未确认、等用户查询或重试时，模型框和刷新可用；查询或重试会直接打开原编号报告。
    expect(container.querySelector<HTMLInputElement>('#model')!.disabled).toBe(false)
    expect(container.querySelector<HTMLButtonElement>('button[aria-label="获取模型列表"]')!.disabled).toBe(false)
    await act(() => button('查询本次受理结果').click()); await settle()
    expect(s.calls.filter(c => c.op === 'start')).toHaveLength(1)
    expect(s.calls.filter(c => c.op === 'context')).toHaveLength(selectionChecks + 1)
    expect(s.calls.some(c => c.op === 'report' && c.params.reportId === id)).toBe(true)
    expect(container.querySelector('.total-card')).toBeTruthy()
  })
  it.each([false, true])('发出首次 POST 后切换账号，再切回原账号=%s，迟到的成功回执不丢失当前草稿', async switchBack => {
    const s = await boot({ targetId: 'account-a', groupId: 'group-a' })
    s.holdStartResponse = true
    await act(() => button('开始检测').click()); await settle()
    expect(s.releaseStartResponse).toBeTypeOf('function')
    await choose('分组', 'group-b'); await choose('账号', 'account-b')
    if (switchBack) { await choose('分组', 'group-a'); await choose('账号', 'account-a') }
    const query = () => container.querySelector<HTMLTextAreaElement>('#question')
    const unsentQuestion = '切换账号后新写的问题，仍未发送'
    await act(() => {
      const textarea = query()!
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(textarea, unsentQuestion)
      textarea.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(query()?.value).toBe(unsentQuestion)
    await act(() => s.releaseStartResponse?.()); await settle()
    expect(field('账号')?.value).toBe(switchBack ? 'account-a' : 'account-b')
    expect(query()?.value).toBe(unsentQuestion)
    expect(button('开始检测').disabled).toBe(false)
    expect(s.calls.filter(c => c.op === 'start')).toHaveLength(1)
  })
  it('宿主收起或换目标早于异步界面消息时，只取消当前未发送的核验', async () => {
    const s = await boot({ targetId: 'account-a', groupId: 'group-a' })
    s.holdContextResponse = true
    await act(() => button('开始检测').click()); await settle()
    expect(s.releaseContextResponse).toBeTypeOf('function')
    const notify = (detail: Record<string, unknown>) => window.dispatchEvent(new CustomEvent('chanshui.discard-checks', { detail }))
    await act(() => notify({ generation: 0 }))
    await act(() => notify({ generation: 1, target: { groupId: 'group-a', targetId: 'account-a' } }))
    expect(container.textContent).toContain('正在核对所选账号和协议')
    await act(() => notify({ generation: 1, target: { groupId: 'group-b', targetId: 'account-b' } }))
    await act(() => s.releaseContextResponse?.()); await settle()
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
    expect(field('账号').value).toBe('account-a')
    expect(button('开始检测').disabled).toBe(false)
  })
  it('账号核验通过后切组，排队的首次发送不留下报告编号', async () => {
    const s = await boot({ targetId: 'account-a', groupId: 'group-a' })
    const selectionChecks = s.calls.filter(c => c.op === 'context').length
    s.holdStart = true
    await act(() => button('开始检测').click()); await settle()
    expect(s.releaseStart).toBeTypeOf('function')
    expect(s.calls.filter(c => c.op === 'context')).toHaveLength(selectionChecks + 1)
    expect(container.querySelector('[aria-label="普通检测受理"]')).toBeNull()
    await choose('分组', 'group-b')
    await act(() => s.releaseStart?.()); await settle()
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
    expect(container.querySelector('[aria-label="普通检测受理"]')).toBeNull()
    expect(container.textContent).toContain('核对期间更换了分组或账号')
  })
  it('名单推送到达之前，也按发送时的配置协议拒绝过期选择', async () => {
    const s = await boot({ targetId: 'account-a', groupId: 'group-a' })
    s.holdStart = true
    await act(() => button('开始检测').click()); await settle()
    expect(s.releaseStart).toBeTypeOf('function')
    s.startProtocol = 'anthropic'
    await act(() => s.releaseStart?.()); await settle()
    expect(container.textContent).toContain('当前名单中的检测协议已变化')
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
    expect(container.querySelector('[aria-label="普通检测受理"]')).toBeNull()
  })
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
    const lookup = container.querySelector<HTMLInputElement>('#hub-report-id')!
    expect(lookup.value).toBe(''); expect(lookup.closest('details')?.open).toBe(false)
    await act(() => { lookup.closest('details')!.open = true; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(lookup, s.report.reportId); lookup.dispatchEvent(new Event('input', {bubbles:true})); });
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
    expect(s.calls.some(c => ['context', 'models'].includes(c.op))).toBe(false)
    await choose('账号', 'account-a')
    expect(container.querySelector<HTMLInputElement>('#model')?.value).toBe('model-a')
    expect(s.calls.filter(c => ['targets', 'context', 'models'].includes(c.op)).map(c => c.op)).toEqual(['context', 'models'])
    expect(s.calls.find(c => c.op === 'models')?.params).toMatchObject({ targetId: 'account-a', groupId: 'group-a' })
    await choose('分组', 'group-b')
    expect(field('账号').value).toBe('')
    expect([...field('账号').options].map(o => o.value)).toEqual(['', 'account-b'])
    expect(container.querySelector('#model')).toBeNull()
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
  it('停止后保存状态失败仍保留只重试保存的入口，不重复停止或检测', async () => {
    const s = await boot()
    Object.assign(s.report, { status: 'running', processExited: true, stopRequested: true, executionPhase: 'paused', finishConfirmed: false, recoveryReason: '程序已退出，但报告状态尚未保存，请重试保存。' }); s.recoverable = true
    await act(() => button(s.report.accountName).click()); await settle()
    await act(() => button('核查运行状态').click()); await settle()
    const retry = button('核查并重试保存')
    expect(retry).toBeTruthy(); expect(retry.disabled).toBe(false)
    expect(button('停止检测')).toBeUndefined()
    expect(container.textContent).toContain('已保存回答1')
    await act(() => retry.click()); await settle()
    expect(s.calls.filter(c => c.op === 'recover-runtime')).toHaveLength(1)
    expect(s.calls.some(c => c.op === 'cancel')).toBe(false)
    expect(s.calls.some(c => c.op === 'start')).toBe(false)
  })
})
