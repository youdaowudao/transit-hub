// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubApp, { HubItemCard } from '../../chanshui/web/components/HubApp'
import { restorationItems, restorationReport, restorationSettings } from './fixtures/chanshui-report-restoration'
import type { HubItem, HubReport } from '../../chanshui/web/lib/hub-types'
import { hubReportView, newItemRead, readReportItems, reportError } from '../../chanshui/web/lib/hub-report-view'

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
let root: ReturnType<typeof createRoot>, container: HTMLDivElement, dispose: (() => void) | undefined
const act = async (fn: () => unknown = () => undefined) => { await React.act(async () => { await fn(); await new Promise(r => setTimeout(r, 0)) }) }
const settle = async () => { for (let i = 0; i < 8; i++) await act() }
const click = async (text: string) => {
  const button = [...container.querySelectorAll('button')].find(b => b.textContent?.includes(text))
  expect(button, `缺少按钮：${text}`).toBeTruthy(); await act(() => button!.click()); await settle()
}
beforeEach(() => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined)
  container = document.createElement('div'); document.body.append(container); root = createRoot(container)
})
afterEach(async () => { await act(() => root.unmount()); dispose?.(); dispose = undefined; container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

async function openReport(report: HubReport = restorationReport(), items: HubItem[] = restorationItems(), failCursor = '', history: HubReport[] = [report], hold?: { cursor: string; used?: boolean; fail?: () => void }) {
  const calls: { op: string; params: Record<string, any> }[] = []
  let failed = false
  const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
  const server = (event: MessageEvent) => {
    const m = event.data
    if (m?.kind === 'chanshui.READY') { respond({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'workspace-a', target: {}, visible: true }); return }
    if (m?.kind !== 'chanshui.request') return
    calls.push({ op: m.op, params: m.params }); let value: unknown = {}
    if (m.op === 'runtime') value = { enabled: true, available: true, active: null }
    if (m.op === 'settings') value = restorationSettings
    if (m.op === 'targets') value = { items: [] }
    if (m.op === 'reports') value = { items: history }
    if (m.op === 'report') value = report
    if (m.op === 'items') {
      if (hold && !hold.used && m.params.cursor === hold.cursor) {
        hold.used = true
        hold.fail = () => respond({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '收起期间第2批读取失败，请重试读取。' } })
        return
      }
      if (failCursor && m.params.cursor === failCursor && !failed) {
        failed = true; respond({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: false, error: { message: '磁盘查询暂时被占用，请重试读取' } }); return
      }
      const n = Number(m.params.cursor || 0)
      value = { items: items.slice(n, n + 5), ...(n + 5 < items.length ? { nextCursor: String(n + 5) } : {}) }
    }
    respond({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ok: true, value })
  }
  window.addEventListener('message', server); dispose = () => window.removeEventListener('message', server)
  await act(() => root.render(React.createElement(HubApp))); await settle(); await click(report.accountName)
  return calls
}

describe('原版报告恢复的业务回归', () => {
  it('接回原版卡片并保留66分与0指纹贡献，正常空错误不显示红字', async () => {
    const calls = await openReport()
    expect(container.querySelector('.results > .total-card')).toBeTruthy()
    expect(container.querySelector('.total-card')?.textContent).toContain('66')
    expect(container.querySelector('.integrity-signals')?.textContent).toContain('0/21')
    expect(container.textContent).not.toContain('报告已记录异常')
    expect(container.querySelector('.total-card table')).toBeNull()
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })
  it('超过两批的12题自动读齐，全部默认收起且没有答案翻页', async () => {
    const calls = await openReport()
    const questions = [...container.querySelectorAll<HTMLDetailsElement>('.question-list > details.question')]
    expect(questions).toHaveLength(12)
    expect(questions[0].querySelector('summary')?.textContent).toMatch(/^Q1 · /)
    expect(questions[11].querySelector('summary')?.textContent).toMatch(/^Q12 · /)
    expect(questions.every(q => !q.open)).toBe(true)
    expect(container.textContent).not.toContain('下一页回答')
    expect(calls.filter(c => c.op === 'items').map(c => c.params.cursor || '')).toEqual(['', '5', '10'])
    await act(() => questions[0].querySelector('summary')!.click())
    expect(questions[0].open).toBe(true); expect(questions[1].open).toBe(false)
    await act(() => questions[0].querySelector('summary')!.click()); expect(questions[0].open).toBe(false)
  })
  it('后批失败保留已读题目，继续读取从失败游标开始并读齐', async () => {
    const calls = await openReport(restorationReport(), restorationItems(), '5')
    expect(container.querySelectorAll('.question-list > details.question')).toHaveLength(5)
    expect(container.textContent).toContain('磁盘查询暂时被占用')
    await click('继续读取回答')
    expect(container.querySelectorAll('.question-list > details.question')).toHaveLength(12)
    expect(calls.filter(c => c.op === 'items').map(c => c.params.cursor || '')).toEqual(['', '5', '5', '10'])
  })
  it('题目结果是一个默认闭合的紧凑折叠，图片只在明确点击后读取', async () => {
    const call = vi.fn(async () => ({ dataURL: 'data:image/png;base64,AA==' }))
    const item = { ...restorationItems(1)[0], imageAvailable: true }
    await act(() => root.render(React.createElement(HubItemCard, { item, call, reportId: 'a'.repeat(32), reportStatus: 'done' })))
    const fold = container.querySelector<HTMLDetailsElement>('details.question')
    expect(fold).toBeTruthy(); expect(fold!.open).toBe(false); expect(call).not.toHaveBeenCalled()
    await act(() => fold!.querySelector('summary')!.click()); await click('查看题目图片')
    expect(call).toHaveBeenCalledExactlyOnceWith('image', { reportId: 'a'.repeat(32), itemId: item.itemId })
    expect(container.querySelector('img')?.src).toContain('data:image/png')
  })
  it('人工识图不被显示成答错，真实总数来自服务器而非已读首批', async () => {
    const items = restorationItems(1); items[0].definition.kind = 'vision'; items[0].definition.judge.type = 'manual'; items[0].result = { answer: '人工查看的回答', judgment: { status: 'unreviewed' } }
    await openReport({ ...restorationReport(), itemCounts: { done: 35 } } as HubReport, items)
    const vision = container.querySelector('.vision-card')
    expect(vision?.textContent).toContain('待人工查看')
    expect(vision?.textContent).not.toContain('答错'); expect(vision?.textContent).not.toContain('识图失败')
    expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuemax')).toBe('35')
  })
  it('再次刷新仍读取已有的后两批，保持已展开题目和后段最新结果', async () => {
    const report = restorationReport(), items = restorationItems()
    const calls = await openReport(report, items)
    const first = container.querySelector<HTMLDetailsElement>('details.question')!
    await act(() => first.querySelector('summary')!.click()); expect(first.open).toBe(true)
    items[11].result!.answer = '第12题已更新的回答'
    await click('核查运行状态')
    expect(container.querySelectorAll('.question-list > details.question')).toHaveLength(12)
    expect(container.querySelector('details.question')).toBe(first); expect(first.open).toBe(true)
    expect(container.textContent).toContain('第12题已更新的回答')
    expect(calls.filter(c => c.op === 'items').map(c => c.params.cursor || '')).toEqual(['', '5', '10', '', '5', '10'])
  })
  it('多道识图保留每题，未知和未提问不变成失败，终态剩余项不再显示检测中', async () => {
    const items = restorationItems(3)
    items.forEach(i => { i.definition.kind = 'vision' })
    items[0].result = { judgment: { status: 'unknown' } }
    items[1].result = { judgment: { status: 'not_asked' } }
    items[2].status = 'running'; items[2].result = {}
    await openReport({ ...restorationReport(), itemCounts: { done: 2, running: 1 } }, items)
    const vision = container.querySelector('.vision-card')!
    expect(vision.textContent).toContain('本次有3道识图题')
    expect(vision.querySelectorAll('details.question')).toHaveLength(3)
    expect(vision.textContent).not.toContain('识图失败'); expect(vision.textContent).not.toContain('答错')
    expect(vision.textContent).toContain('尚未确认'); expect(vision.textContent).toContain('本次未提问'); expect(vision.textContent).toContain('未完成')
    expect(container.querySelector('.timeline')?.textContent).toContain('另有1项未完成')
    expect(container.querySelector('.timeline')?.textContent).not.toContain('检测中')
  })
  it('空错误不报警但非空未知异常保留原因指引，未知总分不当0分', async () => {
    expect(reportError({})).toBe(''); expect(reportError({ step: 'persist' })).toContain('未提供具体原因')
    const report = restorationReport({ summary: { total: { score: null, complete: false, parts: [{ key: 'iq', label: '推理表现', applicable: true, known: false, points: 0, contribution: 0, coefficient: 100 }] } } })
    await openReport(report)
    expect(container.querySelector('.total-card')?.textContent).toContain('尚未完成')
    expect(container.querySelector('.total-card')?.textContent).toContain('未知不当0分')
    expect(container.querySelector('.total-card')?.textContent).not.toContain('0 / 100')
    expect(container.querySelector('.integrity-signals')?.textContent).toContain('尚未确认')
    const empty = hubReportView(restorationReport({ itemCounts: undefined }), restorationItems(5), false)
    expect(empty.progress.total).toBeNull()
  })
  it('重复游标和无进展有界停止；关闭后的迟到批次不能写入', async () => {
    const item = restorationItems(1)[0], applied: HubItem[][] = []
    const repeated = vi.fn(async () => ({ items: [item], nextCursor: 'again' }))
    await expect(readReportItems(repeated as any, 'a', newItemRead(), () => true, rows => applied.push(rows))).rejects.toThrow('没有前进')
    expect(repeated).toHaveBeenCalledTimes(2); expect(applied).toHaveLength(1)
    let current = true, release!: (page: unknown) => void
    const delayed = vi.fn(() => new Promise(resolve => { release = resolve }))
    const read = readReportItems(delayed as any, 'a', newItemRead(), () => current, rows => applied.push(rows))
    current = false; release({ items: [item] }); await read
    expect(applied).toHaveLength(1)
  })
  it('GPT不新增原版没有的思维链卡片，不适用项目仍能在原进度区折叠查看', async () => {
    const items = restorationItems(1)
    items.push({ ...items[0], itemId: 'thinking', definition: { ...items[0].definition, id: 'thinking', title: '思维摘要检测', kind: 'thinking', maxScore: 0, judge: { type: 'manual' } }, result: { judgment: { status: 'not_applicable', reason: '本项仅适用于Claude' } } })
    await openReport(restorationReport(), items)
    expect(container.querySelector('.results')?.textContent).not.toContain('思维链回传')
    const fold = container.querySelector<HTMLDetailsElement>('.timeline details.question')
    expect(fold?.textContent).toContain('思维摘要检测'); expect(fold?.open).toBe(false)
  })
  it.each(['iq', 'vision'])('人工%s请求失败仍显示失败原因，不能被待人工查看覆盖', async kind => {
    const item = restorationItems(1)[0]; item.definition.kind = kind as 'iq' | 'vision'; item.definition.judge.type = 'manual'; item.status = 'error'; item.result = { error: '上游没有返回答案，请核对账号后重试新任务。' }
    await act(() => root.render(React.createElement(HubItemCard, { item, call: vi.fn(), reportId: 'a'.repeat(32), reportStatus: 'done' })))
    expect(container.querySelector('summary')?.textContent).toContain('请求失败')
    expect(container.querySelector('summary')?.textContent).not.toContain('待人工查看')
    expect(container.getElementsByClassName('error-box')[0]?.textContent).toContain('上游没有返回答案')
  })
  it('本站同名组按ID区分，零分/未完成/已删除账号的历史保留且不把推理分当总分', async () => {
    const report = restorationReport({ groupName: '同名组' })
    const history = [report,
      { ...report, reportId: 'b'.repeat(32), groupId: 'group-b', accountName: '零分账号', summary: { totalScore: 0 } },
      { ...report, reportId: 'c'.repeat(32), accountName: '部分报告账号', status: 'partial', summary: { totalScore: null, iqScore: 42 } },
      { ...report, reportId: 'd'.repeat(32), targetId: 'deleted-account', groupId: undefined, groupName: '', accountName: '已删除账号快照' },
    ]
    const calls = await openReport(report, restorationItems(), '', history)
    const sidebar = container.querySelector('.sidebar-list')!
    expect(sidebar.textContent).toContain('同名组 #group-a'); expect(sidebar.textContent).toContain('同名组 #group-b')
    expect(sidebar.querySelector('[data-report-id="' + 'b'.repeat(32) + '"]')?.textContent).toContain('0 分')
    const partial = sidebar.querySelector('[data-report-id="' + 'c'.repeat(32) + '"]')!
    expect(partial.textContent).toContain('未完成'); expect(partial.querySelector('.hist-badge')?.getAttribute('title')).toBe('推理分')
    expect(sidebar.textContent).toContain('未记录分组'); expect(sidebar.textContent).toContain('已删除账号快照')
    expect(calls.filter(c => c.op === 'reports').every(c => c.params.scope === 'sidebar')).toBe(true)
  })
  it('第2批在收起期间失败，恢复同版本的已结束报告后仍自动读齐', async () => {
    const report = restorationReport(), hold: { cursor: string; used?: boolean; fail?: () => void } = { cursor: '5' }
    const calls = await openReport(report, restorationItems(), '', [report], hold)
    expect(container.querySelectorAll('details.question')).toHaveLength(5)
    const visible = (value: boolean) => window.dispatchEvent(new MessageEvent('message', { data: { kind: 'chanshui.VISIBILITY', v: 1, generation: 1, visible: value }, source: window, origin: location.origin }))
    await act(() => visible(false)); await act(() => hold.fail!()); await settle()
    await act(() => visible(true)); await settle()
    expect(container.querySelectorAll('details.question')).toHaveLength(12)
    expect(container.textContent).toContain('本次回答已读完')
    expect(calls.filter(c => c.op === 'items').map(c => c.params.cursor || '')).toEqual(['', '5', '', '5', '10'])
    expect(calls.some(c => c.op === 'start')).toBe(false)
  })
  it.each(['manual', 'none'])('推理%s没有自动分数时不显示尚未完成，也不新增未选识图卡片', async mode => {
    const items = mode === 'manual' ? restorationItems(2) : []
    items.forEach(i => { i.definition.judge.type = 'manual'; i.result = { answer: '人工查看', judgment: { status: 'unreviewed' } } })
    await openReport(restorationReport({ status: 'running', processExited: false, itemCounts: { done: items.length }, summary: { total: { score: null, complete: true, parts: [] } } }), items)
    const iq = [...container.querySelectorAll('.results > .result-card')].find(card => card.querySelector('h3')?.textContent === '推理表现')!
    expect(iq.textContent).toContain('不计算推理分')
    expect(iq.textContent).not.toContain('尚未完成')
    expect(iq.querySelectorAll('details.question')).toHaveLength(items.length)
    expect(container.querySelector('.vision-card')).toBeNull()
    expect(container.textContent).not.toContain('识图测试中')
  })
  it('全部知识事件不适用、未提问时保留原因，不能说答案矛盾', async () => {
    const item = restorationItems(1)[0]; item.definition.kind = 'knowledge'; item.definition.judge.type = 'manual'; item.result = { judgment: { status: 'not_asked' } }
    await openReport(restorationReport({ summary: { knowledge: { status: 'not_applicable', note: '本次事件都在未来，没有适用事件。' } } }), [item])
    const knowledge = container.querySelector('.knowledge-card')!
    expect(knowledge.textContent).toContain('没有适用事件')
    expect(knowledge.textContent).toContain('本次未提问')
    expect(knowledge.textContent).not.toContain('答案存在矛盾')
    expect(knowledge.textContent).not.toContain('已知覆盖下界')
  })
  it.each([[6, false, 'good'], [3, false, 'warn'], [3, true, 'warn'], [0, false, 'warn']])('得分%s/6、半分%s保持原版颜色%s', async (earned, half, color) => {
    const item = restorationItems(1)[0]; item.result = { judgment: { status: 'known', earned: Number(earned), max: 6, half: Boolean(half) } }
    await act(() => root.render(React.createElement(HubItemCard, { item, call: vi.fn(), reportId: 'a'.repeat(32), reportStatus: 'done' })))
    expect(container.querySelector('summary b')?.className).toBe(color)
  })
  it.each(['queued', 'running'])('终态旧题仍为%s时，不能把旧的已保存分数显示成已确认', async status => {
    const item = restorationItems(1)[0]; item.status = status; item.result = { judgment: { status: 'known', earned: 6, max: 6 } }
    await act(() => root.render(React.createElement(HubItemCard, { item, call: vi.fn(), reportId: 'a'.repeat(32), reportStatus: 'interrupted' })))
    expect(container.querySelector('summary')?.textContent).toContain('未完成')
    expect(container.querySelector('summary')?.textContent).not.toContain('6 / 6')
    expect(container.querySelector('summary b')?.className).toBe('warn')
  })
  it('完整人工题列表重读期间保留不计分说明，不闪回尚未完成', async () => {
    const items = restorationItems(2); items.forEach(i => { i.definition.judge.type = 'manual'; i.result = { answer: '人工查看', judgment: { status: 'unreviewed' } } })
    const report = restorationReport({ summary: { total: { score: null, complete: true, parts: [] } } })
    const hold: { cursor: string; used?: boolean; fail?: () => void } = { cursor: '', used: true }
    await openReport(report, items, '', [report], hold)
    hold.used = false; await click('核查运行状态')
    const iq = [...container.querySelectorAll('.result-card')].find(c => c.querySelector('h3')?.textContent === '推理表现')!
    expect(iq.textContent).toContain('不计算推理分'); expect(iq.textContent).not.toContain('尚未完成')
    await act(() => hold.fail!()); await settle()
    expect(iq.textContent).toContain('不计算推理分'); expect(iq.querySelectorAll('details.question')).toHaveLength(2)
  })
})
