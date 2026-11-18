// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, expect, it, vi } from 'vitest'
import { I18nProvider, useI18n } from '../../chanshui/web/lib/i18n'
import { hubReportView } from '../../chanshui/web/lib/hub-report-view'
import { restorationReport } from './fixtures/chanshui-report-restoration'
import { HubProbeEvidence } from '../../chanshui/web/components/ProbeEvidence'
import HubReportContent from '../../chanshui/web/components/HubReportContent'
import type { HubItem } from '../../chanshui/web/lib/hub-types'

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); localStorage.clear() })

it('嵌入原界面在英文浏览器和旧英文偏好下仍明确中文，独立站自动语言保持', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.spyOn(navigator, 'languages', 'get').mockReturnValue(['en-US', 'en'])
  localStorage.setItem('chanshui-lang', 'en')
  const node = document.createElement('div'); document.body.append(node)
  const root = createRoot(node)
  const Heading = () => React.createElement('h3', null, useI18n().t('知识截止日期'))
  await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(Heading))))
  expect(node.textContent).toBe('知识截止日期')
  expect(document.documentElement.lang).toBe('zh-CN')
  expect(localStorage.getItem('chanshui-lang')).toBe('en')
  await React.act(async () => root.unmount()); node.remove()
})

it('原生知识DTO的label/reason/gap_note/web_suspect直接传原卡片，禁止另造日期范围', () => {
  const knowledge = { status: 'estimated', label: '知识≥2026-02-22', cutoff: '2026-02-22', correct: 2, total: 3,
    gap_note: '有 1 题结果对不上，未用它们移动边界', web_suspect: '原程序诊断', reason: '按 2025–2026 客观事件串行二分估计知识边界，不参与评分。', rows: [] }
  const verdict = { knowledge, total: { score: 62, max: 100, parts: [], reason: '原总分说明' }, iq: null }
  const report = restorationReport({ summary: { verdict } })
  const view = hubReportView(report, [], true)
  expect(view.verdict.knowledge).toEqual(knowledge)
  expect(JSON.stringify(view)).not.toContain('无法确认连续日期范围')
  expect(view.totalLabel).toBe('')
  expect(view.verdict.total?.score).toBe(62)
})

it('原题用原版题目明细和用量模板，默认折叠且不被接入层大卡片替换', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined)
  const question = { id: 'Q2', title: '洗车路径', max: 5, earned: 5, half: false, prompt: '原版洗车题面', expected: '开车去', answer: '最终答案: 开车去', thinking_text: '', signature: '', ms: 30, tags: ['自评'], metrics: { InputTokens: 42, OutputTokens: 7, Effort: 'medium', CacheRead: 11, CacheCreation5m: 12, CacheCreation1h: 0 } }
  const fingerprint = { basis: 'numeric', family: 'gpt', family_name: 'GPT', family_prob: 1, top_model: 'gpt-6.1-sol', top_display: '原库显示名', top_prob: 1, sample_min: 3, sample_limit: 10, used_outputs: 3, requests: 3, low_confidence: false, confidence_target: .9, confidence_reached: true, results: [], evidence: [{ id: '原采样ID', answer: '原采样完整回答', expected_count: 300, parsed_count: 300 }], note: '原指纹说明' }
  const item = (id: string, kind: string, evidence: unknown): HubItem => ({ itemId: id, sectionId: kind, definition: { id, title: id === 'Q2' ? '洗车路径' : '数字指纹', kind: kind as any, enabled: true, order: 1, maxScore: 5, template: '', parameters: {}, judge: { type: 'legacy' } }, prompt: '不应取代原题面的接入层文本', expected: '', status: 'done', result: { evidence, judgment: { status: 'known', earned: 5, max: 5 } } })
  const items = [item('Q2', 'iq', question), item('fingerprint:numeric', 'fingerprint_numeric', fingerprint)]
  const report = restorationReport({ summary: { verdict: { iq: { score: 100, max: 100, paper_max: 5, complete: true, note: '原卷面说明' }, fingerprint: null, total: { score: 100, max: 100, parts: [], reason: '原总分说明' } } } })
  const view = hubReportView(report, items, true)
  expect(view.verdict.fingerprint).toEqual(fingerprint)
  expect(view.verdict.iq?.per_question).toEqual([question])
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node)
  try {
    await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(HubReportContent, { report, items, complete: true, call: vi.fn() }))))
    const fold = node.querySelector('details[data-item-id="Q2"]') as HTMLDetailsElement
    expect(fold).toBeTruthy(); expect(fold.open).toBe(false)
    expect(fold.textContent).toContain('原版洗车题面')
    expect(fold.textContent).toContain('标准结论')
    expect(fold.textContent).toContain('用量与耗时')
    expect(fold.textContent).toContain('自评')
    expect(fold.textContent).not.toContain('本次实际题目')
    expect(fold.textContent).not.toContain('不应取代原题面的接入层文本')
    expect(node.querySelectorAll('details[data-item-id="fingerprint:numeric"]')).toHaveLength(0)
    expect(node.textContent).toContain('数字挑战原始证据')
    expect(node.textContent).toContain('原采样完整回答')
    const fingerprintCard = [...node.querySelectorAll('.result-card')].find(card => card.querySelector('h3')?.textContent === '模型指纹归因')!
    expect(fingerprintCard.querySelector(':scope > .question-list')).toBeNull()
  } finally { await React.act(async () => root.unmount()); node.remove() }
})


it('仅自定义人工题时仍能展开已有问答，不伪造原生IQ分数', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined)
  const item: HubItem = { itemId: 'custom:' + 'b'.repeat(32), sectionId: 'iq', definition: { id: 'custom:' + 'b'.repeat(32), title: '人工问题', kind: 'iq', enabled: true, order: 1, maxScore: 0, template: '人工题面', parameters: {}, judge: { type: 'manual' } }, prompt: '人工题面', expected: '', status: 'done', result: { answer: '已有完整回答', judgment: { status: 'reference', reason: '人工核对' } } }
  const report = restorationReport({ summary: { verdict: { iq: null, total: { score: 0, max: 100, parts: [], reason: '未计分' } } } })
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node)
  try {
    await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(HubReportContent, { report, items: [item], complete: true, call: vi.fn() }))))
    const fold = node.querySelector('details[data-item-id]') as HTMLDetailsElement
    expect(fold).toBeTruthy(); expect(fold.open).toBe(false)
    expect(fold.textContent).toContain('人工题面'); expect(fold.textContent).toContain('已有完整回答')
    expect(node.textContent).not.toContain('本次测评')
  } finally { await React.act(async () => root.unmount()); node.remove() }
})


it('原生报告混有自定义或旧格式时，逐项保留已存回答和终态补测入口', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined)
  const make = (id: string, kind: string, evidence: any, answer = ''): HubItem => ({ itemId: id, sectionId: kind, definition: { id, title: id, kind: kind as any, enabled: true, order: 1, maxScore: 0, template: id, parameters: {}, judge: { type: 'manual' } }, prompt: id, expected: '', status: 'done', result: { answer, evidence, judgment: { status: 'reference' } } })
  const custom = make('custom:event', 'knowledge', { kind: 'know' }, '自定义事件完整回答')
  const behavior = make('fingerprint:behavior:1', 'fingerprint_behavior', {}, '独立行为完整回答')
  const numeric = make('fingerprint:numeric', 'fingerprint_numeric', { compatible: false, samples: [{ prompt: '改动数字题面', answer: '旧数字采样完整回答' }], note: '改动原模板' })
  const pending = make('tools', 'tools', {}); pending.status = 'queued'
  const report = restorationReport({ status: 'cancelled', summary: { knowledge: { status: 'custom', observations: [{ id: custom.itemId, kind: 'know' }] }, verdict: { knowledge: { status: 'custom', label: '自定义知识事件', correct: 1, total: 1, reason: '逐题查看', rows: [] }, total: { score: 0, max: 100, parts: [], reason: '' } } } })
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node); const retry = vi.fn()
  try {
    await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(HubReportContent, { report, items: [custom, behavior, numeric, pending], complete: true, call: vi.fn(), onRetry: retry }))))
    for (const [id, text] of [[custom.itemId, '自定义事件完整回答'], [behavior.itemId, '独立行为完整回答'], [numeric.itemId, '旧数字采样完整回答']]) {
      const fold = node.querySelector(`details[data-item-id="${id}"]`) as HTMLDetailsElement
      expect(fold, id).toBeTruthy(); expect(fold.open).toBe(false); expect(fold.textContent).toContain(text)
    }
    const fold = node.querySelector('details[data-item-id="tools"]')!
    const button = [...fold.querySelectorAll('button')].find(x => x.textContent === '补测此项')!
    expect(button).toBeTruthy(); await React.act(async () => button.click()); expect(retry).toHaveBeenCalledWith(['tools'])
  } finally { await React.act(async () => root.unmount()); node.remove() }
})

it.each([['know', '知道'], ['wrong', '答错'], ['unknown', '不知道'], ['invalid', '无法判定'], ['error', '请求失败']])('知识展开复用原row的%s判定，兼容旧格式', async (kind, label) => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node)
  const item = { status: 'done', definition: { kind: 'knowledge' }, result: { evidence: { row: { kind, answer: '原判定依据', error: kind === 'error' ? '原具体失败' : '' } } } } as HubItem
  try {
    await React.act(async () => root.render(React.createElement(HubProbeEvidence, { item })))
    expect(node.querySelector('.metric-line strong')?.textContent).toBe(label)
    expect(node.textContent).not.toContain('未确认')
    item.result!.evidence = { kind, reason: '旧判定依据' }
    await React.act(async () => root.render(React.createElement(HubProbeEvidence, { item })))
    expect(node.querySelector('.metric-line strong')?.textContent).toBe(label)
  } finally { await React.act(async () => root.unmount()); node.remove() }
})

it('旧数字指纹成功记录在原报告补测后仍显示原兼容归因和采样', () => {
  const item = { itemId: 'fingerprint:numeric', definition: { kind: 'fingerprint_numeric', judge: { type: 'manual' }, parameters: {} }, status: 'done', result: { evidence: { compatible: true, usedOutputs: 3, samples: [{ answer: '旧成功采样' }], attribution: { family: 'gpt', familyName: 'GPT', familyProb: 1, topModel: 'legacy-model', topProb: 1, lowConfidence: false, results: [] } } } } as HubItem
  const report = restorationReport({ summary: { verdict: { total: { score: 80, max: 100, parts: [], reason: '' } } } })
  const view = hubReportView(report, [item], true)
  expect(view.verdict.fingerprint?.top_model).toBe('legacy-model')
  expect(view.verdict.fingerprint?.evidence?.[0].answer).toBe('旧成功采样')
})
