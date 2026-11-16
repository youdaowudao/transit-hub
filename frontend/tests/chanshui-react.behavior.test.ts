// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubSettings from '../../chanshui/web/components/HubSettings'
import HubApp, { HubItemCard } from '../../chanshui/web/components/HubApp'
import { HubProbeEvidence, HubReportSummary } from '../../chanshui/web/components/ProbeEvidence'
import { HUB_KINDS } from '../../chanshui/web/lib/hub-policy'
import type { HubQuestion } from '../../chanshui/web/lib/hub-types'
import { flushPromises, mount } from '@vue/test-utils'
import ChanshuiHost from '../src/modules/admin/components/chanshui/ChanshuiHost.vue'
import { chanshuiOpenEvent } from '../src/modules/admin/composables/useChanshuiLauncher'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'
import { cleanupWarning, resultResponse } from './fixtures/chanshui-transfer'

vi.mock('../../chanshui/web/components/Select', () => ({ default: (props: { value: string; onChange: unknown; disabled?: boolean; children: unknown }) => (globalThis as unknown as { React: { createElement: (...args: unknown[]) => unknown } }).React.createElement('select', { value: props.value, onChange: props.onChange, disabled: props.disabled }, props.children) }))
const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
let root: ReturnType<typeof createRoot>, container: HTMLDivElement, cleanup: (() => void) | undefined
const settings = { timeoutSeconds: 120, totalTimeoutSeconds: 600, concurrency: 2, maxOutputTokens: 4096, retries: 0, maxAttempts: 128, memoryLimitMiB: 256, revision: 1 }
const run = async (work: () => unknown) => { await React.act(async () => { await work(); await new Promise(resolve => setTimeout(resolve, 0)); }) }
const tick = async () => { await run(() => undefined); await run(() => undefined) }
const click = async (text: string, within: ParentNode = container) => { const button = Array.from(within.querySelectorAll('button')).find(b => b.textContent?.trim() === text); if (!button) throw new Error(`Missing button ${text}`); await run(() => button.click()); await tick() }
const fill = async (label: string, value: string) => {
  const field = Array.from(container.querySelectorAll('label')).find(l => l.firstChild?.textContent?.includes(label))
  const input = field?.querySelector('input,textarea') as HTMLInputElement | HTMLTextAreaElement | undefined
  if (!input) throw new Error(`Missing field ${label}`)
  await run(() => { const proto = input.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype; Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })); })
}
beforeEach(() => { vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true); vi.spyOn(window, 'confirm').mockReturnValue(true); container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container) })
afterEach(async () => { await run(() => root.unmount()); cleanup?.(); cleanup = undefined; container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('父页真实HTTP到原检测页面的回收警告', () => {
  it('保存值经Host和实际bridge生效，清理警告可见且关闭时不误报未保存', async () => {
    localStorage.setItem(authTokenStorageKey, 'fixture-token')
    const host = mount(ChanshuiHost, { attachTo: document.body, props: { workspaceId: 'workspace-a' }, global: { stubs: { Teleport: false } } })
    cleanup = () => { host.unmount(); localStorage.removeItem(authTokenStorageKey) }
    window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: {} })); await flushPromises()
    const frame = document.querySelector('iframe')!, outgoing: Array<Record<string, any>> = [], incoming: Array<Record<string, any>> = []
    // Deliver each side with its real source identity. Neither origin/source nor
    // generation checks are weakened for the shared jsdom document.
    vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(payload => { const m = structuredClone(payload); incoming.push(m); window.dispatchEvent(new MessageEvent('message', { data: m, source: window, origin: location.origin })) })
    vi.spyOn(window, 'postMessage').mockImplementation(payload => { const m = structuredClone(payload); outgoing.push(m); window.dispatchEvent(new MessageEvent('message', { data: m, source: frame.contentWindow, origin: location.origin })) })
    let saved = settings, partial = false
    const reportId = 'd'.repeat(32), report = { reportId, accountName: '已保存报告账号', model: 'first', status: 'done', processExited: true, createdAtMs: Date.now() }
    const fetch = vi.fn(async (url: string, init: RequestInit) => {
      expect(new Headers(init.headers).get('X-Chanshui-Workspace')).toBe('workspace-a')
      const path = new URL(url, location.origin).pathname
      if (path.endsWith('/runtime')) return new Response(JSON.stringify({ enabled: true, available: true, active: null }))
      if (path.endsWith('/targets')) return new Response(JSON.stringify({ items: [] }))
      if (path.endsWith('/bank-info')) return new Response('{}')
      if (path.endsWith('/settings') && init.method === 'PUT') { saved = { ...JSON.parse(String(init.body)), revision: 2 }; return resultResponse(saved, 'application/json', { error: cleanupWarning }) }
      if (path.endsWith('/settings')) return resultResponse(saved)
      if (path.endsWith(`/reports/${reportId}`)) return new Response(JSON.stringify(report))
      if (path.endsWith(`/reports/${reportId}/items`)) return resultResponse({ items: [{ itemId: 'custom:1', status: 'done', definition: { id: 'custom:1', title: '已经读出的题目', kind: 'iq', maxScore: 0, judge: { type: 'manual' } }, prompt: '原题', expected: '', result: { answer: '已经完整读出的回答' } }] }, 'application/json', partial ? { complete: false, error: { message: '本次页面返回值不完整，请重新读取。' } } : {})
      if (path.endsWith('/reports')) return resultResponse({ items: [report] })
      if (path.endsWith('/questions')) return resultResponse({ items: [] })
      throw new Error(`Unexpected fixture route ${path}`)
    })
    vi.stubGlobal('fetch', fetch)
    await run(() => root.render(React.createElement(HubApp))); for (let i = 0; i < 6; i++) await tick()
    await click('设置与全部题目'); await tick()
    await fill('检测任务内存上限', '384')
    expect(outgoing.filter(m => m.kind === 'chanshui.DIRTY').at(-1)?.dirty).toBe(true)
    await click('保存运行设置'); await tick()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(cleanupWarning.message)
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(cleanupWarning.detail)
    expect(container.textContent).toContain('运行设置已保存，仅供新任务使用')
    const memory = Array.from(container.querySelectorAll('label')).find(l => l.firstChild?.textContent?.startsWith('检测任务内存上限'))!.querySelector('input')!
    expect(memory.value).toBe('384')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent === '保存运行设置')?.disabled).toBe(true)
    expect(outgoing.filter(m => m.kind === 'chanshui.DIRTY').at(-1)?.dirty).toBe(false)
    expect(incoming.some(m => m.kind === 'chanshui.response' && m.ok && m.value?.revision === 2 && m.warning?.message === cleanupWarning.message)).toBe(true)
    expect(fetch.mock.calls.filter(([, init]) => init.method === 'PUT')).toHaveLength(1)
    expect(outgoing.some(m => m.op === 'start' || m.op === 'models')).toBe(false)
    await run(() => (container.querySelector('.hub-history-list button') as HTMLButtonElement).click()); for (let i = 0; i < 5; i++) await tick()
    expect(container.textContent).toContain('已经完整读出的回答')
    partial = true
    await click('核查运行状态'); await tick()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('本次页面返回值不完整')
    expect(container.textContent).toContain('已经完整读出的回答')
    expect(outgoing.some(m => m.op === 'start' || m.op === 'models')).toBe(false)
    await run(() => (document.querySelector('[aria-label="关闭检测页"]') as HTMLButtonElement).click()); await flushPromises()
    expect(window.confirm).not.toHaveBeenCalled(); expect(document.querySelector('iframe')).toBeNull()
  })
})

describe('检测设置实际读写和预设删除', () => {
  it('所有分类可展开、预设可删除，新增题保存后出现在回答配置列表', async () => {
    const defs = HUB_KINDS.map((kind, index): HubQuestion => ({ id: kind.value === 'iq' ? 'Q1' : kind.value, title: `${kind.label}预设`, kind: kind.value, enabled: true, order: index * 100, maxScore: kind.value === 'iq' ? 6 : 0, template: '原题正文', parameters: { generator: 'static' }, judge: { type: kind.value === 'iq' ? 'legacy' : 'manual', expectedText: '不叫醒' }, revision: 1 }))
    const presetDefinitions = structuredClone(defs)
    const call = vi.fn(async (op: string, p: Record<string, any> = {}) => {
      if (op === 'bank-info') return { preset_definitions: presetDefinitions, preset_summaries: presetDefinitions.map(({ id, title, kind }) => ({ id, title, kind })) }
      if (op === 'settings') return settings
      if (op === 'questions') return { items: defs.filter(q => q.kind === p.kind).map(({ id, title, kind, enabled, order, revision }) => ({ id, title, kind, enabled, order, revision })) }
      if (op === 'question') return structuredClone(defs.find(q => q.id === p.id))
      if (op === 'delete-question') { const index = defs.findIndex(q => q.id === p.id && q.revision === p.revision); if (index < 0) throw new Error('题目已变化，请重新读取'); defs.splice(index, 1); return { deleted: true } }
      if (op === 'create-question') { defs.push({ ...p.definition, revision: 1 }); return defs[defs.length - 1] }
      if (op === 'save-question') { const index = defs.findIndex(q => q.id === p.definition.id); defs[index] = { ...p.definition, revision: defs[index].revision! + 1 }; return defs[index] }
      return {}
    })
    await run(() => root.render(React.createElement(HubSettings, { call, onDirtyChange: () => undefined })))
    await tick()
    for (const kind of HUB_KINDS) {
      const details = Array.from(container.querySelectorAll<HTMLDetailsElement>('details.hub-category')).find(d => d.querySelector('summary')?.textContent?.startsWith(kind.label))!
      await run(() => { details.open = true; details.dispatchEvent(new Event('toggle')) }); await tick()
      expect(details.textContent).toContain(`${kind.label}预设`)
      const row = details.querySelector('[data-question-id]')!
      await click('删除', row)
      expect(details.textContent).not.toContain(`${kind.label}预设`)
    }
    expect(call.mock.calls.filter(([op]) => op === 'delete-question')).toHaveLength(HUB_KINDS.length)
    expect(defs).toHaveLength(0)
    const presetSelect = () => Array.from(container.querySelectorAll('label')).find(l => l.firstChild?.textContent?.startsWith('安装包预设'))!.querySelector('select')!
    await run(() => { presetSelect().value = 'tools'; presetSelect().dispatchEvent(new Event('change', { bubbles: true })) })
    await click('新增所选预设副本'); await click('保存题目')
    expect(defs[0]).toMatchObject({ id: expect.stringMatching(/^custom:[0-9a-f]{32}$/), kind: 'tools', title: '工具调用预设（副本）' })
    expect(defs.some(q => q.id === 'tools')).toBe(false)
    await run(() => { presetSelect().value = 'Q1'; presetSelect().dispatchEvent(new Event('change', { bubbles: true })) })
    await click('新增所选预设副本'); await click('保存题目')
    expect(defs.find(q => q.title === '推理题预设（副本）')).toMatchObject({ id: expect.stringMatching(/^custom:[0-9a-f]{32}$/), maxScore: 0, judge: { type: 'manual' } })
    expect(defs.some(q => q.id === 'Q1')).toBe(false)
    await click('新增普通题')
    await fill('题目名称', '新建人工题')
    await fill('题干／提示模板', '请回答本次新增问题')
    await click('保存题目')
    const created = call.mock.calls.find(([op, p]) => op === 'create-question' && p?.definition.title === '新建人工题')![1]!.definition
    expect(created).toMatchObject({ id: expect.stringMatching(/^custom:[0-9a-f]{32}$/), title: '新建人工题', template: '请回答本次新增问题', maxScore: 0, judge: { type: 'manual' } })
    expect(container.textContent).toContain('新建人工题')
  })
  it('编辑原题题干自动改人工查看，保存冲突保留稿件并显示具体原因', async () => {
    const q: HubQuestion = { id: 'Q1', kind: 'iq', title: '原安眠药', enabled: true, order: 100, maxScore: 6, template: '原题正文', parameters: { generator: 'static' }, judge: { type: 'legacy', expectedText: '不叫醒' }, revision: 3 }
    const call = vi.fn(async (op: string, p: Record<string, any> = {}) => {
      if (op === 'settings') return settings
      if (op === 'questions') return { items: p.kind === 'iq' ? [q] : [] }
      if (op === 'question') return q
      if (op === 'save-question') throw new Error('这道题已被其他页面修改，请重新读取最新题目再保存；本次没有覆盖原值。')
      return {}
    })
    await run(() => root.render(React.createElement(HubSettings, { call, onDirtyChange: () => undefined }))); await tick()
    await click('编辑')
    const max = Array.from(container.querySelectorAll('label')).find(l => l.textContent?.startsWith('满分'))!.querySelector('input')!
    expect(max.disabled).toBe(true); expect(max.value).toBe('6')
    await fill('题干／提示模板', '改过的新题正文')
    await click('保存题目')
    expect(call.mock.calls.find(([op]) => op === 'save-question')![1]!.definition).toMatchObject({ template: '改过的新题正文', maxScore: 0, judge: { type: 'manual' } })
    expect(container.textContent).toContain('本次没有覆盖原值')
    expect((container.querySelector('[data-testid="hub-question-editor"] textarea') as HTMLTextAreaElement).value).toBe('改过的新题正文')
  })
})

describe('账号预填、模型下拉和实际回答列表', () => {
  it('开页只读模型，GPT和Claude采用实际默认ID，显式开始才发检测，0分历史不隐藏', async () => {
    const calls: { op: string; params: Record<string, any> }[] = []
    const now = Date.now(), reportId = 'a'.repeat(32)
    const saved = { reportId, targetId: 'b', accountName: '账号B', groupName: '克劳德', model: 'claude-opus-5-5', status: 'done', processExited: true, createdAtMs: now, summary: { score: 0 } }
    const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
    const server = async (event: MessageEvent) => {
      const m = event.data
      if (m?.kind === 'chanshui.READY') { respond({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'w', visible: true, target: { targetId: 'a', groupId: 'gpt' } }); return }
      if (m?.kind !== 'chanshui.request') return
      const p = m.params ?? {}; calls.push({ op: m.op, params: p }); let value: unknown = {}
      if (m.op === 'runtime') value = { enabled: true, available: true }
      if (m.op === 'targets') value = { items: [{ targetId: 'a', accountName: '账号A', groups: [{ id: 'gpt', name: 'GPT' }] }, { targetId: 'b', accountName: '账号B', groups: [{ id: 'claude', name: '克劳德' }] }] }
      if (m.op === 'settings') value = settings
      if (m.op === 'reports') value = { items: [{ ...saved, accountName: '零分历史账号' }, { ...saved, reportId: 'b'.repeat(32), createdAtMs: now - 3 * 86400000, accountName: '三天前账号' }] }
      if (m.op === 'context') value = { workspaceId: 'w', targetId: p.targetId, accountName: p.targetId === 'a' ? '账号A' : '账号B', groupId: p.groupId, groupName: p.targetId === 'a' ? 'GPT' : '克劳德', baseURL: 'https://safe.invalid', protocol: 'messages', canRun: true, expectedContext: { targetId: p.targetId, workspaceId: 'w' } }
      if (m.op === 'models') value = { models: p.targetId === 'a' ? [{ id: 'first' }, { id: 'gpt-6.1-sol' }] : [{ id: 'other' }, { id: 'claude-opus-5-5' }] }
      if (m.op === 'start') value = { reportId: p.reportId, accepted: true }
      if (m.op === 'report') value = { ...saved, reportId: p.reportId }
      if (m.op === 'items') value = { items: [{ itemId: 'custom:1', sectionId: 'iq', definition: { id: 'custom:1', title: '本次新增题', kind: 'iq', maxScore: 0, judge: { type: 'manual' } }, prompt: '新增题实际题干', expected: '人工核对', status: 'done', result: { answer: '模型的实际回答', judgment: 'unreviewed' } }] }
      respond({ kind: 'chanshui.response', v: 1, generation: m.generation, id: m.id, ok: true, value })
    }
    window.addEventListener('message', server); cleanup = () => window.removeEventListener('message', server)
    await run(() => root.render(React.createElement(HubApp))); for (let i = 0; i < 6; i++) await tick()
    const modelSelect = () => Array.from(container.querySelectorAll('label')).find(l => l.firstChild?.textContent?.startsWith('检测模型'))?.querySelector('select')
    expect(modelSelect()?.value).toBe('gpt-6.1-sol')
    expect(calls.some(c => c.op === 'start')).toBe(false)
    expect(container.textContent).toContain('零分历史账号'); expect(container.textContent).not.toContain('三天前账号')
    await run(() => respond({ kind: 'chanshui.OPEN_TARGET', v: 1, generation: 1, target: { targetId: 'b', groupId: 'claude' } })); for (let i = 0; i < 4; i++) await tick()
    expect(modelSelect()?.value).toBe('claude-opus-5-5')
    expect(calls.some(c => c.op === 'start')).toBe(false)
    await click('开始检测'); for (let i = 0; i < 5; i++) await tick()
    expect(calls.filter(c => c.op === 'start')).toHaveLength(1)
    expect(calls.find(c => c.op === 'start')?.params).toMatchObject({ targetId: 'b', model: 'claude-opus-5-5' })
    expect(container.textContent).toContain('本次新增题'); expect(container.textContent).toContain('模型的实际回答')
    expect(container.textContent).toContain('服务器不再保留这次检测进程')
    expect(calls.filter(c => c.op === 'report').length).toBeLessThanOrEqual(3)
  })
})


describe('原结果卡片的白话展示和未知边界', () => {
  it.each(['unknown', 'contradiction'])('知识%s不显示上下界为有效范围', async status => {
    await run(() => root.render(React.createElement(HubReportSummary, { summary: { knowledge: { status, lower: '2026-01-01', upper: '2026-06-01', observations: [{ date: '2026-01-01', kind: 'know' }], note: '观测记录仍可逐题查看' } } })))
    expect(container.textContent).toContain('无法确认日期范围')
    expect(container.textContent).not.toContain('已知覆盖下界')
    expect(container.textContent).not.toContain('未知或错误上界')
  })
  it('知识range仍显示已确认的观测上下界', async () => {
    await run(() => root.render(React.createElement(HubReportSummary, { summary: { knowledge: { status: 'range', lower: '2026-01-01', upper: '2026-06-01' } } })))
    expect(container.textContent).toContain('已知覆盖下界：2026-01-01')
    expect(container.textContent).toContain('未知或错误上界：2026-06-01')
    expect(container.textContent).not.toContain('无法确认日期范围')
  })
  it('工具、缓存、思维、知识和指纹主要结论无需读取JSON，缺字段不伪造0', async () => {
    const make = (kind: string, evidence: unknown) => ({ itemId: kind, status: 'done', definition: { kind }, result: { evidence } })
    const items = [
      make('tools', { pass: false, reason: '没有确认工具调用结果往返', evidence: { calls: [], roundtrip: false } }),
      make('cache', { pass: null, reason: '上游没有提供缓存读取字段', samples: [{ index: 1, read: 0, read_reported: false, write: 4, input: 7, output: 2 }] }),
      make('thinking', { pass: true, reason: '思维摘要正常回传', think_len: 23, sig_len: 40, thinking_tokens: 80 }),
      make('knowledge', { kind: 'know', reason: '最终答案与正确选项一致' }),
      make('fingerprint_numeric', { compatible: false, usedOutputs: 4, note: '模板已修改，原参考样本不再适用', attribution: { topModel: '不应归因', topProb: 1 } }),
    ]
    await run(() => root.render(React.createElement('div', null, ...items.map(item => React.createElement(HubProbeEvidence, { key: item.itemId, item })))))
    expect(container.textContent).toContain('未通过'); expect(container.textContent).toContain('未经验证')
    expect(container.textContent).toContain('思维摘要正常回传'); expect(container.textContent).toContain('23')
    expect(container.textContent).toContain('知道'); expect(container.textContent).toContain('参考不适用')
    expect(container.textContent).not.toContain('不应归因')
    expect(container.querySelector('table tbody tr')?.children[1].textContent).toBe('—')
    expect(container.textContent).not.toContain('"pass"')
  })
  it('未知适用项显示尚未完成，确认全部为零时可以正确显示0分', async () => {
    const part = { label: '推理表现', applicable: true, known: false, points: 0, max_points: 100, contribution: 0, coefficient: 100 }
    await run(() => root.render(React.createElement(HubReportSummary, { summary: { total: { score: null, max: 100, complete: false, parts: [part] } } })))
    expect(container.textContent).toContain('尚未完成'); expect(container.textContent).toContain('尚未计分')
    expect(container.textContent).not.toContain('0 / 100')
    await run(() => root.render(React.createElement(HubReportSummary, { summary: { total: { score: 0, max: 100, complete: true, parts: [{ ...part, known: true }] } } })))
    expect(container.querySelector('.metric-line strong')?.textContent).toBe('0')
    expect(container.textContent).not.toContain('尚未完成')
  })
})


describe('不适用和未抽到的题目', () => {
  it('活动报告仍显示真实等待状态，旧数据中的字符串跳过结果也不会误显示等待', async () => {
    const base = { itemId: 'tools', definition: { title: '工具检测', kind: 'tools', maxScore: 0, judge: { type: 'manual' } }, prompt: '', expected: '' }
    for (const [status, label] of [['queued', '准备中'], ['running', '检测中']]) {
      await run(() => root.render(React.createElement(HubItemCard, { item: { ...base, status }, reportId: 'a'.repeat(32), reportStatus: 'running', call: async () => ({}) })))
      expect(container.querySelector('.rc-head')?.textContent).toContain(label)
      expect(container.querySelector('.rc-head')?.textContent).not.toContain('未完成')
    }
    for (const [judgment, label] of [['not_asked', '本次未提问'], ['not_applicable', '不适用']]) {
      await run(() => root.render(React.createElement(HubItemCard, { item: { ...base, status: 'running', result: { judgment } }, reportId: 'a'.repeat(32), reportStatus: 'interrupted', call: async () => ({}) })))
      expect(container.querySelector('.rc-head')?.textContent).toContain(label)
      expect(container.textContent).not.toContain('本项正在检测')
    }
  })
  it.each(['cancelled', 'partial', 'interrupted', 'done'])('报告%s后旧的queued/running项只显示未完成，保留已完成和跳过结果', async reportStatus => {
    const make = (status: string, judgment?: Record<string, unknown>) => ({ itemId: status, definition: { title: '检测项', kind: 'tools', maxScore: 0, judge: { type: 'manual' } }, prompt: '', expected: '', status, result: judgment ? { judgment } : undefined })
    for (const status of ['queued', 'running']) {
      await run(() => root.render(React.createElement(HubItemCard, { item: make(status), reportId: 'a'.repeat(32), reportStatus, call: async () => ({}) })))
      expect(container.querySelector('.rc-head')?.textContent).toContain('未完成')
      expect(container.textContent).not.toContain('准备中')
      expect(container.textContent).not.toContain('正在检测')
      expect(container.textContent).toContain(status === 'queued' ? '此项未执行' : '此项未完成')
      if (status === 'running') expect(container.textContent).toContain('可能已计费')
    }
    await run(() => root.render(React.createElement(HubItemCard, { item: make('done', { status: 'known' }), reportId: 'a'.repeat(32), reportStatus, call: async () => ({}) })))
    expect(container.querySelector('.rc-head')?.textContent).toContain('完成')
    expect(container.querySelector('.rc-head')?.textContent).not.toContain('未完成')
    await run(() => root.render(React.createElement(HubItemCard, { item: { ...make('error'), result: { error: '已保存的具体上游错误' } }, reportId: 'a'.repeat(32), reportStatus, call: async () => ({}) })))
    expect(container.querySelector('.rc-head')?.textContent).toContain('失败')
    expect(container.textContent).toContain('已保存的具体上游错误')
    for (const [status, label] of [['not_asked', '本次未提问'], ['not_applicable', '不适用']]) {
      await run(() => root.render(React.createElement(HubItemCard, { item: make('queued', { status }), reportId: 'a'.repeat(32), reportStatus, call: async () => ({}) })))
      expect(container.querySelector('.rc-head')?.textContent).toContain(label)
      expect(container.querySelector('.rc-head')?.textContent).not.toContain('未完成')
    }
  })
  it('不把不适用/未提问显示成完成检测，也不制造缺证据错误或人工总分', async () => {
    const item = { itemId: 'thinking', definition: { title: '思维摘要', kind: 'thinking', maxScore: 0, judge: { type: 'manual' } }, prompt: '', expected: '', status: 'done', result: { judgment: { status: 'not_applicable', reason: '此项仅适用Claude，本次没有发送请求' } } }
    await run(() => root.render(React.createElement(HubItemCard, { item, reportId: 'a'.repeat(32), call: async () => ({}) })))
    expect(container.querySelector('.rc-head')?.textContent).toContain('不适用')
    expect(container.textContent).not.toContain('尚无可确认的检测证据')
    const skipped = { ...item, definition: { ...item.definition, kind: 'knowledge' }, result: { judgment: { status: 'not_asked', reason: '本次事件题未抽到，不作为答错' } } }
    await run(() => root.render(React.createElement(HubItemCard, { item: skipped, reportId: 'a'.repeat(32), call: async () => ({}) })))
    expect(container.querySelector('.rc-head')?.textContent).toContain('本次未提问')
    expect(container.textContent).not.toContain('尚无可确认的检测证据')
    await run(() => root.render(React.createElement(HubReportSummary, { summary: { total: { score: null, complete: true, parts: [{ label: '推理', applicable: false, known: false }] } } })))
    expect(container.querySelector('.metric-line')?.textContent).toContain('本次不计总分')
    expect(container.querySelector('.metric-line')?.textContent).not.toContain('尚未完成')
  })
})

describe('手动运行状态核查', () => {
  const setup = async (initialAvailable = true, initialBusy = false) => {
    const pendingPolls: Array<{ id: number; run: () => void }> = []
    const realSetTimeout = globalThis.setTimeout.bind(globalThis)
    const realClearTimeout = globalThis.clearTimeout.bind(globalThis)
    let nextTimerId = 0
    vi.spyOn(globalThis, 'setTimeout').mockImplementation((callback, delay, ...args) => {
      if (delay === 2000 && typeof callback === 'function') {
        const id = --nextTimerId
        pendingPolls.push({ id, run: () => callback(...args) })
        return id as unknown as ReturnType<typeof setTimeout>
      }
      return realSetTimeout(callback, delay, ...args)
    })
    vi.spyOn(globalThis, 'clearTimeout').mockImplementation(id => {
      const index = pendingPolls.findIndex(timer => timer.id === id)
      if (index >= 0) pendingPolls.splice(index, 1)
      else realClearTimeout(id)
    })
    let now = Date.now()
    vi.spyOn(Date, 'now').mockImplementation(() => now)
    const state = { phase: 'running', available: initialAvailable, busy: initialBusy, holdNextReport: false, holdNextItems: false }
    const heldReports: Array<() => void> = []
    const heldItems: Array<(error?: string) => void> = []
    const calls: { op: string; params: Record<string, any> }[] = []
    const base = { reportId: 'c'.repeat(32), targetId: 'a', accountName: '核查账号', groupName: 'GPT', model: 'gpt-6.1-sol', status: 'running', processExited: false, createdAtMs: now }
    const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
    // Replies stay in one React interaction batch, as cached/fast IPC replies
    // can in the browser. A manual check must not rely on pollId changing.
    vi.spyOn(window, 'postMessage').mockImplementation(m => {
      if (m?.kind === 'chanshui.READY') { respond({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'w', visible: true, target: { targetId: 'a', groupId: 'gpt' } }); return }
      if (m?.kind !== 'chanshui.request') return
      const p = m.params ?? {}; calls.push({ op: m.op, params: p }); let value: unknown = {}
      if (m.op === 'runtime') value = { enabled: true, available: state.available, busy: state.busy, reason: state.available ? '' : '运行状态暂时无法确认，请手动核查', active: state.busy || state.phase === 'recovered' ? null : base }
      if (m.op === 'targets') value = { items: [{ targetId: 'a', accountName: '核查账号', groups: [{ id: 'gpt', name: 'GPT' }] }] }
      if (m.op === 'settings') value = settings
      if (m.op === 'context') value = { ...base, workspaceId: 'w', groupId: 'gpt', baseURL: 'https://safe.invalid', protocol: 'messages', canRun: true, expectedContext: { targetId: 'a', workspaceId: 'w' } }
      if (m.op === 'models') value = { models: [{ id: 'gpt-6.1-sol' }] }
      if (m.op === 'reports') value = { items: [] }
      if (m.op === 'items') value = { items: [{ itemId: p.cursor ? 'page-two' : 'page-one', sectionId: 'iq', definition: { title: p.cursor ? '第二页题' : '第一页题', kind: 'iq', maxScore: 0, judge: { type: 'manual' } }, prompt: p.cursor ? '第二页实际题干' : '第一页实际题干', expected: '', status: 'done', result: { answer: p.cursor ? '第二页回答' : '第一页回答', judgment: 'unreviewed' } }], nextCursor: p.cursor ? '' : 'page-two' }
      if (m.op === 'report') {
        if (state.phase === 'failed') { respond({ kind: 'chanshui.response', v: 1, generation: m.generation, id: m.id, ok: false, error: { message: '夹具模拟状态读取中断，请重试读取' } }); return }
        const oldReport = p.reportId !== base.reportId
        value = { ...base, reportId: p.reportId, accountName: oldReport ? '已结束旧报告' : base.accountName, status: oldReport || state.phase !== 'running' ? 'done' : 'running', processExited: oldReport || state.phase === 'recovered' }
      }
      const reply = { kind: 'chanshui.response', v: 1, generation: m.generation, id: m.id, ok: true, value }
      if (m.op === 'report' && state.holdNextReport) { state.holdNextReport = false; heldReports.push(() => respond(reply)); return }
      if (m.op === 'items' && state.holdNextItems) { state.holdNextItems = false; heldItems.push(error => respond(error ? { ...reply, ok: false, error: { message: error } } : reply)); return }
      respond(reply)
    })
    await run(() => root.render(React.createElement(HubApp))); await tick()
    const poll = async (elapsed: number) => {
      now += elapsed
      const next = pendingPolls.shift()
      if (!next) throw new Error('Expected one pending report poll')
      await run(next.run); await tick()
    }
    return { state, calls, pendingPolls, poll, respond, heldReports, heldItems }
  }

  it.each(['exit-timeout', 'read-errors'])('自动核查因%s停止后，手动核查立即清掉旧阻塞和矛盾提示', async reason => {
    const { state, calls, pendingPolls, poll } = await setup()
    await click('查看该任务')
    if (reason === 'exit-timeout') {
      state.phase = 'waiting'
      await poll(2000)
      await poll(16001)
      expect(container.textContent).toContain('无法确认检测程序已退出')
    } else {
      state.phase = 'failed'
      await poll(2000); await poll(2000); await poll(2000)
      expect(container.textContent).toContain('已停止自动读取')
    }
    expect(pendingPolls).toHaveLength(0)
    const before = calls.length
    state.phase = 'recovered'
    await click('核查运行状态')
    expect(container.textContent).toContain('本次检测程序已结束，服务器不再保留这次检测进程')
    expect(container.textContent).not.toContain('无法确认检测程序已退出')
    expect(container.textContent).not.toContain('连续三次无法读取')
    expect(container.textContent).not.toContain('当前任务：')
    expect(calls.slice(before).filter(c => c.op === 'report')).toHaveLength(1)
    expect(pendingPolls).toHaveLength(0)
    await click('新建检测')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === '开始检测')?.disabled).toBe(false)
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })
  it('其他工作区任务结束后可手动核查解除busy，不新增轮询或再次调用模型', async () => {
    const { state, calls, pendingPolls } = await setup(true, true)
    expect(container.textContent).toContain('当前已有检测正在执行')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === '开始检测')?.disabled).toBe(true)
    expect(pendingPolls).toHaveLength(0)
    state.busy = false; state.phase = 'recovered'
    await click('核查运行状态')
    expect(container.textContent).not.toContain('当前已有检测正在执行')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === '开始检测')?.disabled).toBe(false)
    expect(pendingPolls).toHaveLength(0)
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })

  it('只有runtime返回active=null、没有选中报告时，也清除旧任务而不再调用模型', async () => {
    const { state, calls, respond } = await setup(false)
    expect(container.textContent).toContain('当前任务：')
    await run(() => respond({ kind: 'chanshui.VISIBILITY', v: 1, generation: 1, visible: false }))
    const before = calls.length
    state.phase = 'recovered'; state.available = true
    await click('核查运行状态')
    expect(container.textContent).not.toContain('当前任务：')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === '开始检测')?.disabled).toBe(false)
    expect(calls.slice(before).map(c => c.op)).toEqual(['runtime'])
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })

  it('查看已结束旧报告不会清除另一个仍在执行的任务', async () => {
    const { calls } = await setup()
    await click('查找更早记录')
    await fill('按报告编号查找', 'd'.repeat(32))
    await click('打开此报告')
    expect(container.textContent).toContain('已结束旧报告')
    expect(container.textContent).toContain('当前任务：核查账号')
    await click('新建检测')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === '开始检测')?.disabled).toBe(true)
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })

  it('手动确认退出后，较早的轮询迟到回复不能恢复旧任务或重新启动自动读取', async () => {
    const { state, calls, pendingPolls, poll, heldReports } = await setup()
    await click('查看该任务')
    state.holdNextReport = true
    await poll(2000)
    expect(heldReports).toHaveLength(1)
    state.phase = 'recovered'
    await click('核查运行状态')
    expect(container.textContent).toContain('服务器不再保留这次检测进程')
    await run(heldReports[0]); await tick()
    expect(container.textContent).toContain('服务器不再保留这次检测进程')
    expect(container.textContent).not.toContain('当前任务：')
    expect(pendingPolls).toHaveLength(0)
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })

  it('翻到下一页后，旧页迟到的成功结果不能写回同一报告', async () => {
    const { state, poll, heldItems } = await setup()
    state.holdNextItems = true
    await click('查看该任务')
    expect(heldItems).toHaveLength(1)
    // A newer refresh of page one completes while its first read is delayed.
    await poll(2000)
    expect(container.textContent).toContain('第一页实际题干')
    await click('下一页回答')
    expect(container.textContent).toContain('第二页实际题干')
    await run(() => heldItems[0]()); await tick()
    expect(container.textContent).toContain('第二页实际题干')
    expect(container.textContent).not.toContain('第一页实际题干')
  })

  it('旧页迟到的错误和finally不能污染新页或提前结束新页加载', async () => {
    const { state, poll, heldItems } = await setup()
    state.holdNextItems = true
    await click('查看该任务')
    await poll(2000)
    state.holdNextItems = true
    await click('下一页回答')
    expect(heldItems).toHaveLength(2)
    expect(container.textContent).toContain('正在读取本页回答')
    await run(() => heldItems[0]('旧第一页读取失败，不应显示在第二页')); await tick()
    expect(container.textContent).not.toContain('旧第一页读取失败')
    expect(container.textContent).toContain('正在读取本页回答')
    expect(Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === '上一页回答')?.disabled).toBe(true)
    await run(() => heldItems[1]()); await tick()
    expect(container.textContent).toContain('第二页实际题干')
    expect(container.textContent).not.toContain('正在读取本页回答')
  })
})
