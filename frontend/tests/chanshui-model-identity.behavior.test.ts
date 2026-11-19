// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubReportContent from '../../chanshui/web/components/HubReportContent'
import HubSettings, { HubQuestionEditor } from '../../chanshui/web/components/HubSettings'
import type { HubItem, HubQuestion, HubReport } from '../../chanshui/web/lib/hub-types'
import { hubReportView } from '../../chanshui/web/lib/hub-report-view'
vi.mock('../../chanshui/web/components/Select', () => ({ default: (p: any) => (globalThis as any).React.createElement('select', { value: p.value, onChange: p.onChange }, p.children) }))
const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
let root: ReturnType<typeof createRoot>, container: HTMLDivElement
const act = async (fn: () => unknown = () => undefined) => { await React.act(async () => { await fn(); await new Promise(r => setTimeout(r, 0)) }) }
const settle = async () => { for (let i = 0; i < 4; i++) await act() }
const click = async (text: string, parent: ParentNode = container) => { const b = [...parent.querySelectorAll('button')].find(x => x.textContent?.trim() === text)!; expect(b).toBeTruthy(); await act(() => b.click()); await settle() }
const question: HubQuestion = { id: 'Q10', title: '模型自报检查', kind: 'iq', enabled: true, order: 950, maxScore: 5, template: '你的实际模型是什么？不允许联网，马上回答。', parameters: { generator: 'static' }, judge: { type: 'model_identity' }, revision: 1 }
function fixture(known: boolean, earned = 5) {
 const part = { key: 'iq', label: '推理表现', applicable: true, known, points: known ? earned === 5 ? 100 : 95 : 0, max_points: 100, contribution: known ? earned === 5 ? 100 : 95 : 0, coefficient: 100 }
 const note = '模型自报检查尚未确认，请展开查看并补测；其他题的已保存分数保留，暂不公布推理分和总分。'
 const total = { score: part.contribution, max: 100, parts: [part], reason: known ? '原评分理由' : note }
 const report = { reportId: '1'.repeat(32), targetId: 'fixture', accountName: '隔离自报验收', model: 'gpt-6.2-sol', status: known ? 'done' : 'partial', processExited: true, createdAtMs: Date.now(), summary: { iqScore: known ? part.points : null, totalScore: known ? part.contribution : null, total, verdict: { total, iq: { score: known ? part.points : null, max: 100, paper_max: 105, complete: known, per_question: [], note: known ? '原评分理由' : note } } } } as unknown as HubReport
 const item: HubItem = { itemId: 'Q10', sectionId: 'iq', status: 'done', definition: structuredClone(question), prompt: question.template, expected: '检测端自报规则', result: { answer: known ? earned ? '我是GPT-6。' : '我是GPT-5。' : '我是一个AI助手。', judgment: { status: known ? 'known' : 'unknown', max: 5, ...(known ? { earned } : {}), reason: known ? earned ? '未发现自报型号异常' : '自报型号异常' : note } } }
 return { report, item }
}
beforeEach(() => { vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true); vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} })); vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined); vi.spyOn(window, 'confirm').mockReturnValue(true); container = document.createElement('div'); document.body.append(container); root = createRoot(container) })
afterEach(async () => { await act(() => root.unmount()); container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
describe('模型自报计分在原报告中的展示', () => {
 it('原版报告未确认时遮蔽总分与两处IQ贡献，保留原话和补测', async () => {
  const { report, item } = fixture(false), retry = vi.fn()
  await act(() => root.render(React.createElement(HubReportContent, { report, items: [item], complete: true, call: vi.fn(), onRetry: retry }))); await settle()
  const total = container.querySelector('.total-card')!
  expect(total.textContent).toContain('总分尚未确认'); expect(total.textContent).not.toContain('0 / 100'); expect(total.textContent).not.toContain('0/100')
  const iq = [...container.querySelectorAll('.result-card')].find(x => x.querySelector('h3')?.textContent === '推理表现')!
  expect(iq.querySelector('.rc-score')?.textContent).toContain('尚未确认'); expect(iq.textContent).not.toContain('0/100')
  expect(container.textContent).not.toContain('未完成的适用项按 0 分计入'); expect(container.textContent).not.toContain('旧版未保存的原诊断不能补回')
  const fold = container.querySelector<HTMLDetailsElement>('details[data-item-id="Q10"]')!; expect(fold.open).toBe(false)
  await act(() => fold.querySelector('summary')!.click()); expect(fold.textContent).toContain('我是一个AI助手。'); expect(fold.textContent).toContain('不调用模型自评')
  await click('补测此项', fold); expect(retry).toHaveBeenCalledWith(['Q10'])
 })
 it.each([0, 5])('确定结果%d分参与原推理折算并继续折叠', async earned => {
  const { report, item } = fixture(true, earned)
  await act(() => root.render(React.createElement(HubReportContent, { report, items: [item], complete: true, call: vi.fn() }))); await settle()
  expect(container.querySelector('.total-card')?.textContent).toContain(`${earned ? 100 : 95} / 100`)
  expect(container.querySelector('details[data-item-id="Q10"] summary')?.textContent).toContain(`${earned} / 5`)
  expect(container.textContent).not.toContain('旧版未保存的原诊断不能补回')
 })
 it('新题规则不改变旧原版IQ失败展示，补测中标签仍优先', () => {
  const { report, item } = fixture(false)
  const v = (report.summary as any).verdict; v.iq.score = 95; v.total.parts[0].known = true
  expect(hubReportView(report, [item], true).totalLabel).toBe('')
  v.iq.score = null; v.total.parts[0].known = false; report.status = 'queued'; report.retry = { id: 'retry', itemIds: ['Q10'] } as any
  expect(hubReportView(report, [item], true).totalLabel).toBe('补测中')
 })
})
describe('自报题设置与两条复制入口', () => {
 it('已有题可编辑满分，保存模型自报规则和5分；不调用检测', async () => {
  const call = vi.fn(async () => ({})), saved = vi.fn()
  await act(() => root.render(React.createElement(HubQuestionEditor, { original: question, call, saved, cancelled: vi.fn(), onDirtyChange: vi.fn() }))); await settle()
  expect(container.textContent).toContain('默认满分5分，参与推理和总分折算')
  expect(container.querySelector<HTMLSelectElement>('select')?.value).toBe('model_identity')
  await click('保存题目'); expect(call).toHaveBeenCalledWith('save-question', expect.objectContaining({ definition: expect.objectContaining({ maxScore: 5, judge: { type: 'model_identity' } }), revision: 1 }))
  expect(call.mock.calls.every(([op]) => op === 'save-question')).toBe(true)
 })
 it.each(['list', 'preset'])('从%s复制默认为人工0分，可明确选择自报规则后保存新编号', async mode => {
  const call = vi.fn(async (op: string) => op === 'settings' ? { protocol: 'auto', timeoutSeconds: 360, totalTimeoutSeconds: 3600, concurrency: 32, maxOutputTokens: 8192, retries: 3, maxAttempts: 0, memoryLimitMiB: 256, revision: 1 } : op === 'bank-info' ? { preset_definitions: [question] } : op === 'questions' ? { items: [question] } : op === 'question' ? question : {})
  await act(() => root.render(React.createElement(HubSettings, { call, onDirtyChange: vi.fn() }))); await settle()
  if (mode === 'list') await click('复制新增', container.querySelector('[data-question-id="Q10"]')!)
  else { const select = [...container.querySelectorAll('label')].find(x => x.firstChild?.textContent === '安装包预设')!.querySelector('select')!; await act(() => { select.value = 'Q10'; select.dispatchEvent(new Event('change', { bubbles: true })) }); await click('新增所选预设副本') }
  const editor = container.querySelector('[data-testid="hub-question-editor"]')!
  const method = editor.querySelector<HTMLSelectElement>('select')!; expect(method.value).toBe('manual'); expect(container.textContent).toContain('该副本默认人工查看、不计分')
  await act(() => { method.value = 'model_identity'; method.dispatchEvent(new Event('change', { bubbles: true })) })
  await click('保存题目', editor)
  const entry = call.mock.calls.find(([op]) => op === 'create-question') as any
  expect(entry[1].definition.id).toMatch(/^custom:[0-9a-f]{32}$/); expect(entry[1].definition.maxScore).toBe(5); expect(entry[1].definition.judge.type).toBe('model_identity')
 })
})
