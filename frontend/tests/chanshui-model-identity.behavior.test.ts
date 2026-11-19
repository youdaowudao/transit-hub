// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HubProbeEvidence } from '../../chanshui/web/components/ProbeEvidence'
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
 it('distinguishes an answered but ungraded self report from a failed request in the progress view', async () => {
  const { report, item } = fixture(false)
  const show = async (entry: HubItem) => {
   await act(() => root.render(React.createElement(HubReportContent, { report, items: [entry], complete: true, call: vi.fn() })))
   await settle()
   return container.querySelector('.timeline')!
  }
  let progress = await show(item)
  expect(progress.textContent).toContain('检测已结束，模型自报题尚未判分')
  expect(progress.querySelector('.probe-state')?.textContent).toContain('尚未确认')
  expect(progress.textContent).not.toContain('部分请求失败')
  report.itemCounts = { done: 1, error: 1 }
  progress = await show(item)
  expect(progress.textContent).toContain('报告已生成，部分请求失败')
  expect(progress.textContent).not.toContain('模型自报题尚未判分')
  report.itemCounts = undefined
  progress = await show({ ...item, status: 'error', result: { ...item.result, answer: '', error: '上游未返回正文' } })
  expect(progress.textContent).toContain('报告已生成，部分请求失败')
  expect(progress.textContent).toContain('上游未返回正文')
  expect(progress.textContent).not.toContain('模型自报题尚未判分')
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

describe('缓存报告按当轮输入显示命中率', () => {
 const samples = [
  { index: 4, input: 3, total: 2000, read: 1997, write: 0, input_reported: false, read_reported: true, write_reported: true, hit_rate: 101.53 },
  { index: 5, input: 3, total: 2030, read: 2027, write: 0, input_reported: true, read_reported: true, write_reported: true, hit_rate: 101.50 },
  { index: 6, input: 3, total: 2060, read: 2057, write: 0, input_reported: true, read_reported: true, write_reported: true, hit_rate: 101.48 },
 ]
 const evidence = { status: 'pass', pass: true, samples, third_read_ratio: 101.48, third_read_baseline: 2027, delta: 30,
  reason: '上一轮读加写与末轮读相差不超过 64 token。末轮缓存率是末轮读取除以上一轮读加写，相等为 100%。' }
 const cache: HubItem = { itemId: 'cache', sectionId: 'cache', status: 'done', prompt: '', expected: '',
  definition: { ...question, id: 'cache', kind: 'cache' }, result: { judgment: { status: 'known' }, evidence } }
 it.each(['report card', 'saved item detail'])('shows both metrics with their own label in %s', async entry => {
  const { report } = fixture(true)
  await act(() => root.render(entry === 'report card'
   ? React.createElement(HubReportContent, { report, items: [cache], complete: true, call: vi.fn() })
   : React.createElement(HubProbeEvidence, { item: cache })))
  await settle()
  expect(container.querySelector('.cache-rates')?.textContent).toContain('末轮缓存命中率99.85%')
  const rows = container.querySelectorAll('tbody tr')
  expect(rows[0].children[5].textContent).toBe('—')
  expect(rows[1].children[5].textContent).toBe('99.85%')
  expect(rows[2].children[5].textContent).toBe('99.85%')
  expect(container.textContent).toContain('末轮读取与上一轮读加写的比值')
  expect(container.textContent).toContain('101.48%')
  expect(container.textContent).toContain('30 token')
  expect(container.querySelector('.cache-rates')?.previousElementSibling?.textContent).not.toContain('末轮缓存率是末轮读取除以上一轮读加写')
 })
})

it('names the two boundary events, sorts dates, and opens only their saved answers', async () => {
 const { report } = fixture(true)
 const rows = [
  { id: 'ucl', happened_at: '2026-05-30', event: '欧洲冠军联赛决赛', kind: 'unknown', unsure: true, answer: 'D 不知道' },
  { id: 'old', happened_at: '2025-11-04', event: '弗吉尼亚州长选举', kind: 'know', ok: true, answer: 'A' },
  { id: 'europa', happened_at: '2026-05-20', event: '欧洲联赛决赛', kind: 'unknown', unsure: true, answer: 'D 不知道' },
 ]
 const saved = (report.summary as any).verdict
 saved.knowledge = { rows, status: 'estimated', label: '这两道事件均未确认',
  boundary_review: { status: 'two_unknown', reason: 'two_unknown', anchor_id: 'europa', check_id: 'ucl' } }
 const items: HubItem[] = rows.map(row => ({ itemId: 'knowledge:' + row.id, sectionId: 'knowledge', status: 'done',
  definition: { ...question, id: 'knowledge:' + row.id, title: row.event, kind: 'knowledge', judge: { type: 'manual' }, parameters: { date: row.happened_at } },
  prompt: '实际事件题', expected: '参考答案', result: { judgment: { status: 'reference' }, evidence: { row },
   ...(row.id === 'ucl' ? { answer: '这是模型实际输出：D' } : {}) } }))
 const call = vi.fn(), retry = vi.fn()
 await act(() => root.render(React.createElement(HubReportContent, { report, items, complete: true, call, onRetry: retry })))
 await settle()
 const card = container.querySelector('.knowledge-card')!
 const ordered = [...card.querySelectorAll<HTMLDetailsElement>('.knowledge-answer')]
 expect(ordered.map(row => row.dataset.itemId)).toEqual(['old', 'europa', 'ucl'])
 expect(ordered[1].querySelector('summary')?.textContent).toContain('边界待核查')
 expect(ordered[2].querySelector('summary')?.textContent).toContain('边界待核查')
 expect(ordered[0].querySelector('summary')?.textContent).not.toContain('边界待核查')
 const note = card.querySelector('.knowledge-note')!
 expect(note.textContent).toContain('2026-05-20 欧洲联赛决赛')
 expect(note.textContent).toContain('2026-05-30 欧洲冠军联赛决赛')
 await click('查看这两道题的回答', card)
 expect(ordered.map(row => row.open)).toEqual([false, true, true])
 expect(ordered[2].textContent).toContain('模型原始回答这是模型实际输出：D')
 expect(ordered[1].textContent).toContain('这份报告没有保存模型原话')
 expect(ordered[1].textContent).toContain('已保存的判定摘录D 不知道')
 expect(call).not.toHaveBeenCalled()
 expect(retry).not.toHaveBeenCalled()

 const range = '本轮事件题估计范围 2025-11-04 至 2026-05-20'
 saved.knowledge = { ...saved.knowledge, label: range, boundary_review: {
  ...saved.knowledge.boundary_review, method: 'cross_year', from: '2025-11-04', to: '2026-05-20',
 } }
 await act(() => root.render(React.createElement(HubReportContent, { report, items, complete: true, call, onRetry: retry })))
 await settle()
 expect(card.textContent).toContain(range)
 expect(card.textContent).not.toContain('现有结果还不能确定知识日期边界')
 expect(card.textContent).not.toContain('边界待核查')
 expect([...card.querySelectorAll('.knowledge-answer summary')].filter(row => row.textContent?.includes('估计范围依据'))).toHaveLength(2)
})
