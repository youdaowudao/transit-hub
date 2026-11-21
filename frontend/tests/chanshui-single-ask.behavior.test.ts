// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, expect, it, vi } from 'vitest'
import SingleAskPanel from '../../chanshui/web/components/SingleAskPanel'
import { I18nProvider } from '../../chanshui/web/lib/i18n'
import HubApp from '../../chanshui/web/components/HubApp'
import { approveFixtureStart } from './fixtures/chanshui-transfer'

vi.mock('../../chanshui/web/components/Select', () => ({ default: (p: any) => (globalThis as any).React.createElement('select', { id: p.id, value: p.value, onChange: p.onChange, disabled: p.disabled }, p.children) }))
const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
const act = async (work: () => unknown) => React.act(async () => { await work(); await new Promise(r => setTimeout(r, 0)) })
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
const answer = { Answer: '原完整答复', ThinkingText: '原思维摘要', Signature: '原签名', InputTokens: 12, OutputTokens: 7, ThinkingTokens: 4, CacheCreation5m: 1, CacheCreation1h: 2, CacheRead: 9, Err: '' }

it('单问单答保留原模式、预算、努力级别、Ctrl+Enter及原答复用量模板，默认不发送', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node), send = vi.fn().mockResolvedValue(answer)
  const render = (model = 'claude-opus-5-5') => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(SingleAskPanel, { model, canRun: true, onAsk: send })))
  const set = async (id: string, value: string) => {
    const input = node.querySelector('#' + id) as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
    expect(input).toBeTruthy()
    await act(() => { const proto = input.tagName === 'SELECT' ? HTMLSelectElement.prototype : input.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype; Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(input, value); input.dispatchEvent(new Event(input.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true })) })
  }
  try {
    await act(() => render())
    expect(send).not.toHaveBeenCalled(); expect((node.querySelector('details.ask-panel') as HTMLDetailsElement).open).toBe(false)
    await set('thinking-mode', 'budget'); await set('budget', '8192'); await set('effort', 'max'); await set('question', '只发送原问题')
    await act(() => node.querySelector('#question')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true })))
    expect(send).toHaveBeenCalledTimes(1)
    expect(send.mock.calls[0][0]).toEqual({ prompt: '只发送原问题', thinking: { type: 'enabled', budget_tokens: 8192, display: 'summarized' }, effort: 'max' })
    expect(send.mock.calls[0][1]).toBeInstanceOf(AbortSignal)
    expect(node.textContent).toContain('原完整答复'); expect(node.textContent).toContain('输入 12 · 输出 7 · 思考 4 · 缓存写入 3 · 缓存读取 9')
    expect(node.textContent).toContain('原思维摘要'); expect(node.textContent).toContain('原签名')
    await act(() => render('gpt-6.1-sol')); expect(node.querySelector('#thinking-mode')).toBeNull()
    await act(() => [...node.querySelectorAll('button')].find(b => b.textContent === '发送问题')!.click())
    expect(send.mock.calls[1][0].thinking).toBeNull()
  } finally { await act(() => root.unmount()); node.remove() }
})

it('离开原面板中止前端读取，不把关闭转换成停止或另发模型请求', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node)
  const send = vi.fn((_payload, _signal: AbortSignal) => new Promise(() => {}))
  await act(() => root.render(React.createElement(SingleAskPanel, { model: 'gpt-6.1-sol', canRun: true, onAsk: send })))
  const question = node.querySelector('#question') as HTMLTextAreaElement
  await act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(question, '原问题'); question.dispatchEvent(new Event('input', { bubbles: true })) })
  await act(() => [...node.querySelectorAll('button')].find(b => b.textContent === '发送问题')!.click())
  expect(send).toHaveBeenCalledTimes(1)
  await act(() => root.unmount()); node.remove()
  expect(send.mock.calls[0][1].aborted).toBe(true); expect(send).toHaveBeenCalledTimes(1)
})

for (const scenario of ['start-response-lost', 'retry-response-lost'] as const) it(`单问单答${scenario}仍使用同一记录，不重复发送成功问题`, async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node)
  const starts: any[] = [], retries: any[] = []; let checks = 0, reportId = '', failed = scenario === 'retry-response-lost'
  const report = () => ({ reportId, targetId: 'a', accountName: '原账号', groupName: 'Claude', model: 'claude-opus-5-5', status: failed ? 'partial' : 'done', processExited: true, createdAtMs: Date.now(), updatedAtMs: failed ? 1 : 2, summary: { singleAsk: true } })
  const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { source: window, origin: location.origin, data }))
  const catalog = { workspaceId: 'w', groups: [{ id: 'g', name: 'Claude', accounts: [{ targetId: 'a', accountId: '1', accountName: '原账号', models: 'claude-opus-5-5', protocol: 'anthropic' }] }] }
  const server = (event: MessageEvent) => {
    const m = event.data
    if (m?.kind === 'chanshui.READY') { respond({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'w', visible: true, target: { targetId: 'a', groupId: 'g' }, catalog }); return }
    if (m?.kind !== 'chanshui.request') return
    if (!approveFixtureStart(m)) return
    let value: unknown = {}
    if (m.op === 'runtime') value = { available: true, enabled: true, active: null }
    if (m.op === 'context') {
      checks++
      const context = { workspaceId: 'w', targetId: 'a', groupId: 'g', baseURL: 'https://fixture.invalid', protocol: 'anthropic' }
      value = { ...context, accountName: '原账号', groupName: 'Claude', canRun: true, expectedContext: context }
    }
    if (m.op === 'reference-info') value = { supported_models: ['claude-opus-5-5'], fingerprint_count: 1 }
    // 选中账号后页面向上游读取模型；夹具按该账号原有模型回答。
    if (m.op === 'models') value = { models: ['claude-opus-5-5'] }
    if (m.op === 'reports') value = { items: reportId ? [report()] : [] }
    if (m.op === 'settings') value = { protocol: 'auto', timeoutSeconds: 360, totalTimeoutSeconds: 3600, concurrency: 32, retries: 3, maxOutputTokens: 8192, maxAttempts: 0, memoryLimitMiB: 256, revision: 1 }
    if (m.op === 'start') {
      starts.push(structuredClone(m.params)); reportId ||= m.params.reportId; value = report()
      if (scenario === 'start-response-lost' && starts.length === 1) { respond({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { status: 503, operationResult: 'unknown', message: '受理返回丢失' } }); return }
    }
    if (m.op === 'report') value = { ...report(), reportId: m.params.reportId }
    if (m.op === 'items') value = { items: [{ itemId: 'custom:' + reportId, definition: { kind: 'iq', title: '单问单答', judge: { type: 'manual' } }, status: failed ? 'error' : 'done', result: { answer: answer.Answer, ...(failed ? { error: '模拟上游失败' } : {}), evidence: { single_ask: { ...answer, Err: failed ? '模拟上游失败' : '' } } } }] }
    if (m.op === 'retry') {
      retries.push(structuredClone(m.params)); failed = false
      respond({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { status: 503, operationResult: 'unknown', message: '补测返回丢失' } }); return
    }
    respond({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: true, value })
  }
  window.addEventListener('message', server)
  const settle = async () => { for (let i = 0; i < 8; i++) await act(() => undefined) }
  const click = async (label: string) => {
    const button = [...node.querySelectorAll('button')].find(b => b.textContent === label)!
    expect(button).toBeTruthy()
    await act(() => button.click()); await settle()
  }
  try {
    await act(() => root.render(React.createElement(HubApp))); await settle()
    const question = node.querySelector('#question') as HTMLTextAreaElement; expect(question).toBeTruthy()
    await act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(question, '原样问题'); question.dispatchEvent(new Event('input', { bubbles: true })) })
    const selectionChecks = checks
    await click('发送问题'); expect(starts).toHaveLength(1); const firstId = starts[0].reportId
    if (scenario === 'retry-response-lost') {
      await click('发送问题')
      expect(starts).toHaveLength(1); expect(checks - selectionChecks).toBe(1)
      expect(retries).toHaveLength(0)
      await click('查看原问答记录')
      await click('补测失败项（1）')
      expect(retries).toHaveLength(1); expect(retries[0]).toMatchObject({ reportId: firstId, itemIds: ['custom:' + firstId] })
      await click('核查运行状态'); expect(starts).toHaveLength(1); expect(retries).toHaveLength(1)
    } else {
      expect(node.textContent).toContain(firstId)
      await click('用原编号重试受理')
      expect(starts).toHaveLength(2); expect(starts[1]).toEqual(starts[0]); expect(retries).toHaveLength(0)
    }
    expect(node.querySelector('.answer-panel')?.textContent).toContain('原完整答复')
  } finally { await act(() => root.unmount()); window.removeEventListener('message', server); node.remove() }
})
