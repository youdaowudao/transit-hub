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

it('跨年知识卡显示逐题结果与未提问原因，补测期间隐藏旧结论', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const eventRow = { id: 'ao_2026_women', happened_at: '2026-01-31', event: '澳网女单决赛', kind: 'unknown', unsure: true, answer: 'D 不知道' }
  const item = (id: string, judgment: string): HubItem => ({
    itemId: 'knowledge:' + id, sectionId: 'knowledge', definition: {
      id: 'knowledge:' + id, title: id, kind: 'knowledge', enabled: true, order: 1, maxScore: 0, template: '',
      parameters: {}, judge: { type: 'reference' },
    }, status: 'done', prompt: '冻结题面', expected: '', result: { judgment: { status: judgment } },
  })
  const seen = item(eventRow.id, 'reference')
  seen.result = { answer: '保存的真实答复', evidence: { row: eventRow }, judgment: { status: 'reference' } }
  const skipped = item('worldcup_2026_boot', 'not_asked')
  const node = document.createElement('div'); document.body.append(node)
  const root = createRoot(node)
  const call = vi.fn()
  const show = (status: string, reason: string, label: string) => React.act(async () => {
    const unstarted = reason === 'no_anchor'
    const knowledge = { status: 'unknown', label, reason: unstarted ? '知识结论尚未发布，可查看检测状态和已保存回答。' : '',
      protocol: 'chat', rows: unstarted ? [] : [eventRow],
      boundary_review: { method: 'cross_year', status: unstarted ? 'not_needed' : 'unconfirmed', reason } }
    const report = restorationReport({ model: 'gpt-6.1-sol', status, summary: { verdict: { knowledge } } })
    root.render(React.createElement(I18nProvider, { fixedLang: 'zh' },
      React.createElement(HubReportContent, { report, items: [seen, skipped], complete: true, call, onRetry: vi.fn() })))
  })
  try {
    await show('done', 'no_question', '知识截止日期未确认')
    const card = node.querySelector('.knowledge-card')!
    const fold = card.querySelector('details.knowledge-answer') as HTMLDetailsElement
    expect(fold.open).toBe(false)
    expect(fold.textContent).toContain('保存的真实答复')
    expect(card.textContent).toContain('附近没有第二道明确回答不知道的事件')
    expect(card.textContent).toContain('完整默认题库先问两道 2026 年事件')
    expect(card.querySelector('.fold button')).toBeNull()
    expect(card.textContent).not.toContain('2025-10-19 至 2025-11-04')
    expect(call).not.toHaveBeenCalled()
    await show('running', 'retry_pending', '旧区间候选')
    expect(card.textContent).toContain('知识截止日期未确认')
    expect(card.textContent).not.toContain('旧区间候选')
    for (const status of ['cancelled', 'interrupted']) {
      await show(status, 'no_anchor', '知识截止日期未确认')
      expect(card.textContent).toContain('检测已停止，已保存的回答仍可查看。')
      expect(card.textContent).toContain('保存的真实答复')
      expect(card.textContent).not.toContain('知识结论尚未发布')
    }
  } finally {
    await React.act(async () => root.unmount())
    node.remove()
  }
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

it('知识结果只在报告结束且读完时显示同厂商一句对照，原事件行仍在', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const node = document.createElement('div'); document.body.append(node); const root = createRoot(node)
  const knowledge = {
    status: 'estimated', reason: '原版说明', protocol: 'chat', rows: [
      { id: 'bulgaria', happened_at: '2026-01-01', event: '保加利亚加入欧元区', kind: 'know', ok: true },
      { id: 'olympics', happened_at: '2026-02-22', event: '冬奥会闭幕', kind: 'unknown', unsure: true },
    ],
  }
  const render = (status: string, complete: boolean, verdict = knowledge) => React.act(async () => root.render(
    React.createElement(I18nProvider, { fixedLang: 'zh' },
      React.createElement(HubReportContent, { report: restorationReport({ model:'chat-latest', status, summary: { verdict: { knowledge: verdict } } }), items: [], complete, call: vi.fn() })),
  ))
  const comparison = () => node.querySelector('[data-testid="knowledge-date-comparison"]')
  try {
    await render('running', true)
    expect(comparison()).toBeNull()
    await render('done', false)
    expect(comparison()).toBeNull()
    await render('done', true)
    expect(comparison()?.textContent).toContain('GPT-5.6 Luna')
    expect(comparison()?.textContent).not.toContain('Claude')
    expect(node.querySelectorAll('.knowledge-ladder li')).toHaveLength(2)
    for (const status of ['not_selected', 'not_applicable']) {
      await render('done', true, { ...knowledge, status, rows: [] })
      expect(comparison()).toBeNull()
    }
  } finally { await React.act(async () => root.unmount()); node.remove() }
})

it('原生数字证据不能盖掉题目单独保存的错误和补测中状态', () => {
  const fingerprint = { basis:'numeric', family:'gpt', family_prob:1, top_model:'gpt-6-astra', used_outputs:3, top_prob:1 }
  const item = {itemId:'fingerprint:numeric',definition:{kind:'fingerprint_numeric'},status:'error',result:{error:'检测已停止，请查看已保存样本',evidence:fingerprint}} as HubItem
  const report = restorationReport({summary:{verdict:{fingerprint}}})
  expect(hubReportView(report,[item],true).verdict.fingerprint?.error).toBe('检测已停止，请查看已保存样本')
  item.status='running'
  item.result!.error=undefined
  report.retry={id:'fixture',itemIds:[item.itemId]} as any
  expect(hubReportView(report,[item],false).verdict.fingerprint?.error).toContain('正在补测')
})

it('知识汇总等待重算时只展示最新逐题证据，隐藏旧日期标签', async () => {
  vi.stubGlobal('React',React);vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true)
  const node=document.body.appendChild(document.createElement('div')),root=createRoot(node)
  const knowledge={status:'estimated',label:'旧范围已经确认',reason:'旧摘要',
    boundary_review:{status:'unconfirmed',reason:'retry_pending'},rows:[{id:'mamdani',happened_at:'2025-11-04',kind:'unknown',event:'旧标签不应显示',answer:'旧答案'}]}
  const item={itemId:'knowledge:mamdani',sectionId:'knowledge',status:'done',definition:{id:'knowledge:mamdani',kind:'knowledge',title:'最新题目',parameters:{},judge:{type:'manual'}},result:{answer:'最新正确回答',judgment:{status:'reference'},evidence:{row:{id:'mamdani',kind:'know',happened_at:'2025-11-04',event:'最新题目'}}}} as HubItem
  const report=restorationReport({status:'interrupted',summary:{knowledge,verdict:{knowledge}}})
  try{
    await React.act(async()=>root.render(React.createElement(I18nProvider,{fixedLang:'zh'},React.createElement(HubReportContent,{report,items:[item],complete:true,call:vi.fn()}))))
    expect(node.textContent).toContain('未能保存复核结论')
    expect(node.textContent).toContain('最新正确回答')
    expect(node.textContent).not.toContain('旧范围已经确认')
    expect(node.textContent).not.toContain('旧标签不应显示')
    expect(node.textContent).not.toContain('正在补测知识题')
    report.status='running'
    item.status='queued'
    report.retry={id:'fixture',itemIds:[item.itemId]} as any
    await React.act(async()=>root.render(React.createElement(I18nProvider,{fixedLang:'zh'},React.createElement(HubReportContent,{report,items:[item],complete:true,call:vi.fn()}))))
    expect(node.textContent).toContain('下面保留的是上次结果')
    expect(node.textContent).not.toContain('未能保存复核结论')
    report.retry={id:'other-section',itemIds:['tools']} as any
    await React.act(async()=>root.render(React.createElement(I18nProvider,{fixedLang:'zh'},React.createElement(HubReportContent,{report,items:[item],complete:true,call:vi.fn()}))))
    expect(node.textContent).not.toContain('正在补测知识题')
    expect(node.textContent).not.toContain('正在补测；下面')
    report.status='interrupted'
    report.retry={id:'fixture',itemIds:[item.itemId]} as any
    await React.act(async()=>root.render(React.createElement(I18nProvider,{fixedLang:'zh'},React.createElement(HubReportContent,{report,items:[item],complete:true,call:vi.fn()}))))
    expect(node.textContent).not.toContain('正在补测')
    expect(node.textContent).toContain('停止前保存的结果')
  }finally{await React.act(async()=>root.unmount());node.remove()}
})

it('数字重叠的共同日期说明也适用于原知识报告，并依赖实际数字结果', async () => {
  vi.stubGlobal('React',React);vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true)
  const node=document.body.appendChild(document.createElement('div')),root=createRoot(node)
  const knowledge={status:'estimated',label:'原知识说明',reason:'原版',rows:[]}
  const fingerprint={basis:'numeric',family:'gpt',top_model:'gpt-6-astra',top_prob:1,used_outputs:3}
  const report=restorationReport({model:'gpt-6-astra',status:'done',summary:{verdict:{knowledge,fingerprint}}})
  const render=()=>React.act(async()=>root.render(React.createElement(I18nProvider,{fixedLang:'zh'},React.createElement(HubReportContent,{report,items:[],complete:true,call:vi.fn()}))))
  try{
    await render()
    expect(node.textContent).toContain('知识日期无法区分两款')
    fingerprint.top_model='gpt-6.2-sol'
    await render()
    expect(node.textContent).not.toContain('知识日期无法区分两款')
  }finally{await React.act(async()=>root.unmount());node.remove()}
})
