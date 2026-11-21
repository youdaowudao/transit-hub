// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import HubReportContent from '../../chanshui/web/components/HubReportContent'
import ResultCards from '../../chanshui/web/components/ResultCards'
import { HubRequestError } from '../../chanshui/web/lib/hub-bridge'
import { checkLines, lineText } from '../../chanshui/web/lib/hub-crosscheck'
import { reportVersion } from '../../chanshui/web/lib/hub-report-view'
import type { HubCrossCheck, HubItem, HubReport } from '../../chanshui/web/lib/hub-types'
import fixture from '../../chanshui/internal/hub/prefixcheck/testdata/prefixcheck-fixture.json'

vi.mock('../../chanshui/web/components/Select', () => ({
  default: (p: any) => (globalThis as any).React.createElement('select', { value: p.value, onChange: p.onChange, disabled: p.disabled }, p.children),
}))
const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
let root: ReturnType<typeof createRoot>, container: HTMLDivElement, dispose: (() => void) | undefined
const act = async (work: () => unknown = () => undefined) => {
  await React.act(async () => { await work(); await Promise.resolve() })
}
const text = () => container.textContent ?? ''
const findButton = (label: string) => [...container.querySelectorAll('button')].find(b => b.textContent?.trim() === label)
const click = async (label: string) => {
  const button = findButton(label)
  expect(button, label).toBeTruthy()
  await act(() => button!.click())
}
const fold = (label: string) => [...container.querySelectorAll('details')].find(d => d.querySelector('summary')?.textContent?.includes(label))
const toggle = async (label: string, open: boolean) => {
  const details = fold(label)
  expect(details, label).toBeTruthy()
  await act(async () => { details!.open = open; await new Promise(resolve => setTimeout(resolve, 0)) })
}
const deferred = <T,>() => {
  let resolve!: (value: T) => void, reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const waitTime = async (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms))
const crossCalls = (call: ReturnType<typeof vi.fn>) => call.mock.calls.filter(([op]) => op === 'cross-check').length
const H3 = '检测结束后生成判读小结。'
const H3b = '题目读完后生成判读小结。'
const H4 = '正在读取同一模型的其他报告进行对照……'
const H5 = '判读对照读取失败，不影响下面的检测结果。'
const H5b = '重新读取对照'
const H5c = '报告在读取对照期间有更新'
const H5d = '重新读取报告与对照'
const T2c = '正在等待判读结果，读到后显示上游加了多少内容。'
const T8 = '两次请求明细与算式'
const T8f = '只取得了请求 A，无法连线计算斜率'
const T8h = '两次请求的结果连不成合理的直线'
const T8d = '两次合计连成一条直线'
const T7 = '查看这份提示词全文'

function sample(suffix = '0b', old = false) {
  const row = fixture.reports.find(r => r.reportId.endsWith(suffix))!
  const expected = old ? fixture.expectedWithoutPerItem : fixture.expected
  const data = structuredClone(expected[row.reportId as keyof typeof expected].crossCheck) as HubCrossCheck
  const report = {
    reportId: row.reportId, accountName: row.accountName, model: row.model, status: row.status, createdAtMs: row.createdAtMs,
    updatedAtMs: row.updatedAtMs, processExited: true, summary: structuredClone(row.summary),
  } as HubReport
  const items = row.items.map(item => ({
    itemId: item.itemId, orderIndex: item.order, status: 'done', result: structuredClone(item.result),
    prompt: item.prompt, imageAvailable: item.imageAvailable,
    definition: { id: item.itemId, kind: item.kind, title: item.itemId, template: item.template, judge: { type: item.judgeType }, maxScore: 0, parameters: {}, enabled: true, order: item.order, revision: 1 },
    ...(item.kind === 'injection' ? { usableInjectionPoints: data.injectionPoints } : {}),
  })) as HubItem[]
  return { report, items, data }
}

type Content = Parameters<typeof HubReportContent>[0]
async function render(input: ReturnType<typeof sample>, call: ReturnType<typeof vi.fn>, props: Partial<Content> = {}, strict = false) {
  const el = React.createElement(HubReportContent, {
    key: input.report.reportId, report: input.report, items: input.items, complete: true,
    itemsReadVersion: reportVersion(input.report), retryHold: false, readHold: false,
    call, onRefresh: async () => undefined, ...props,
  })
  await act(() => root.render(strict ? React.createElement(React.StrictMode, null, el) : el))
}

function changeSample(input: ReturnType<typeof sample>, update: (evidence: any, item: HubItem) => void) {
  const item = input.items.find(item => item.definition.kind === 'injection')!
  update(item.result!.evidence, item)
  return input
}

beforeEach(() => {
  vi.stubGlobal('React', React)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0)
  vi.stubGlobal('cancelAnimationFrame', () => undefined)
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(() => root.unmount())
  dispose?.()
  dispose = undefined
  container.remove()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('判读文案合同', () => {
  it('新旧报告的 28 组检查项逐字匹配代码、分组、选择与文字', () => {
    for (const expected of [fixture.expected, fixture.expectedWithoutPerItem]) {
      const entries = Object.entries(expected)
      expect(entries).toHaveLength(14)
      for (const [id, row] of entries) {
        const cross = row.crossCheck as HubCrossCheck
        expect(checkLines(cross).map(line => ({ ...line, text: lineText(line, cross) })), id).toEqual(row.lines)
      }
    }
  })
})

describe('实际报告页判读', () => {
  it('1、2、3 报告未结束、题目版本未就绪及单问单答', async () => {
    const v = sample()
    const call = vi.fn().mockResolvedValue(v.data)
    await render({ ...v, report: { ...v.report, status: 'running' },
      items: v.items.map(item => item.definition.kind === 'injection' ? { ...item, status: 'queued' } : item),
    }, call)
    expect(text()).toContain(H3)
    expect(text()).toContain(T2c)
    await render(v, call, { itemsReadVersion: 'another-version' })
    expect(text()).toContain(H3b)
    expect(text()).not.toContain(T2c)
    expect(fold(T8)).toBeUndefined()
    await render({ ...v, report: { ...v.report, summary: { singleAsk: true } } }, call)
    expect(text()).toContain('单问单答')
    expect(text()).not.toContain('判读小结')
    expect(crossCalls(call)).toBe(0)
  })

  it('4、5 就绪只读一次，D 档显示不同线路和逐题结果', async () => {
    const v = sample()
    const waiting = deferred<HubCrossCheck>()
    const call = vi.fn().mockReturnValue(waiting.promise)
    await render(v, call, {}, true)
    expect(text()).toContain(H4)
    expect(crossCalls(call)).toBe(1)
    await act(() => waiting.resolve(v.data))
    expect(text()).toContain('结论：D 多项证据指向不是所选模型')
    for (const code of ['route.mixed', 'prompt.mismatch', 'prompt.other.match', 'self.gpt5', 'self.gpt5.byPrompt', 'knowledge.older']) {
      const line = checkLines(v.data).find(l => l.code === code)!
      expect(text()).toContain(lineText(line, v.data))
    }
    expect(text()).toContain('应配「Codex GPT-6.2 Sol 版提示词」')
    expect(text()).toContain('按长度判断，这段内容与')
    const details = fold('逐题核对（')
    expect(details?.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(details?.textContent).toContain('Q1（1,445）')
    expect(text()).not.toContain(H4)
  })

  it('6 版本不一致只等待用户刷新，并在重读期间禁用按钮', async () => {
    const v = sample()
    const refreshed = deferred<void>()
    const onRefresh = vi.fn(() => refreshed.promise)
    const call = vi.fn().mockResolvedValueOnce({ ...v.data, reportUpdatedAtMs: -1 }).mockResolvedValue(v.data)
    await render(v, call, { onRefresh })
    expect(text()).toContain(H5c)
    expect(crossCalls(call)).toBe(1)
    expect(onRefresh).not.toHaveBeenCalled()
    await click(H5d)
    expect(findButton(H5d)?.disabled).toBe(true)
    expect(onRefresh).toHaveBeenCalledTimes(1)
    await act(() => refreshed.resolve())
    expect(text()).toContain('结论：D')
    expect(text()).not.toContain(H5c)
    expect(crossCalls(call)).toBe(2)
  })

  it.each([2, 4, 0])('7 遇忙退避次数 %s，其他错误不自动重试', async failures => {
    vi.useFakeTimers()
    const v = sample()
    const call = vi.fn().mockResolvedValue(v.data)
    const error = new HubRequestError('隔离读取失败', failures ? 429 : 500)
    for (let i = 0; i < (failures || 1); i++) call.mockRejectedValueOnce(error)
    await render(v, call)
    if (failures) {
      expect(text()).toContain(H4)
      await waitTime(1000)
      expect(crossCalls(call)).toBe(2)
      expect(text()).toContain(H4)
      await waitTime(2000)
      if (failures === 4) await waitTime(4000)
    }
    const succeeded = failures === 2
    expect(crossCalls(call)).toBe(succeeded ? 3 : failures || 1)
    expect(text()).not.toContain(H4)
    if (!succeeded) {
      expect(text()).toContain(H5)
      await click(H5b)
      expect(crossCalls(call)).toBe((failures || 1) + 1)
    }
    expect(text()).toContain('结论：D')
    expect(text()).not.toContain(H5)
  })

  it.each([
    ['0e', false, 'A 未见异常', '正是配这份提示词', '应配「Codex'],
    ['07', false, 'A 未见异常', '上游没有在请求 A 前加内容。', '影响：走这条线路'],
    ['0b', true, 'D', '这份报告没有可用的逐题核对记录', '逐题核对（'],
    ['0a', true, 'B 线路不稳定', T8h, T8d],
  ])('8–11 样本 %s（旧报告=%s）', async (id, old, grade, shown, absent) => {
    const v = sample(id, old)
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain(`结论：${grade}`)
    expect(text()).toContain(shown)
    expect(text()).not.toContain(absent)
    if (id === '07') {
      expect(text()).toContain('6～10 token 之间')
      expect(fold(T7)).toBeUndefined()
      expect(text()).not.toContain('加内容在订阅反代中很常见')
    }
    if (id === '0b') {
      for (const code of ['route.unchecked', 'self.gpt5', 'self.gpt5.byInjection']) {
        const line = checkLines(v.data).find(l => l.code === code)!
        expect(text()).toContain(lineText(line, v.data))
      }
      expect(checkLines(v.data).some(l => l.code === 'self.gpt5.byPrompt')).toBe(false)
    }
  })

  it('12 没有四类证据显示 E', async () => {
    const v = sample()
    v.data = { ...v.data, grade: 'E', reference: null, perItemChecked: false, groups: null, selfReport: { present: false, earned: null, max: null, byPrompt: false, itemId: '', status: '' }, codes: [] }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain('结论：E 无法判断')
    expect(text()).not.toContain('结论：D')
  })

  it('13 仅 A 可用时保留 B 的未取得行及明确说明', async () => {
    const v = changeSample(sample(), (ev, item) => { ev.points = ev.points.slice(0, 1); item.usableInjectionPoints = 1 })
    await render(v, vi.fn().mockResolvedValue(v.data))
    const details = fold(T8)
    expect(details?.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(details?.textContent).toContain('未取得')
    expect(details?.textContent).toContain(T8f)
    expect(details?.textContent).not.toContain(T8d)
    expect(details?.textContent).not.toContain(T8h)
  })

  it.each(['overflow', 'less-than-input', 'swapped', 'decimal-wire'] as const)('13a 请求 A 无效时保留原卡片：%s', async kind => {
    const v = changeSample(sample('07'), (ev, item) => {
      if (kind === 'overflow') ev.points[0].total = Number.MAX_SAFE_INTEGER + 1
      if (kind === 'less-than-input') ev.points[0].total = ev.points[0].input - 1
      if (kind === 'swapped') ev.points.reverse()
      item.usableInjectionPoints = 0
    })
    v.data = { ...v.data, reference: null, injectionPoints: 0, groups: [], referenceUnavailableReason: '注入检测的请求 A 没有可用的用量记录' }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(fold(T8)).toBeUndefined()
    expect(fold('采样明细与评分标准')).toBeTruthy()
    expect(text()).toContain(v.data.referenceUnavailableReason)
  })

  it('13a、13b B 长度不符时保留原四列、附加列与包装修正', async () => {
    const v = changeSample(sample('07'), (ev, item) => {
      item.definition.template = '说明：{{sample}}'
      ev.points[0].n = 10
      ev.points[0].round = 3
      ev.points[1].n = 1599
      ev.round_overhead = 2.5
      item.usableInjectionPoints = 1
    })
    await render(v, vi.fn().mockResolvedValue(v.data))
    const details = fold(T8)!
    expect([...details.querySelectorAll('th')].map(th => th.textContent)).toEqual(['轮次', '样本字符', '发送内容', '输入', '其中缓存读写', '合计'])
    expect([...details.querySelectorAll('tbody tr:first-child td')].map(td => td.textContent)).toEqual(['3', '10', '修改后的模板，填入 1 个 x', '7', '0', '7'])
    expect(details.textContent).toContain('每轮新增包装修正 2.50 token')
    expect(details.textContent).toContain(T8f)
    expect(details.textContent).not.toContain(T8d)
  })

  it('14 全文首次展开才读，失败后可再展开，成功后复用', async () => {
    const v = sample()
    let promptCalls = 0
    const call = vi.fn(async (op: string) => {
      if (op === 'cross-check') return v.data
      if (++promptCalls === 1) throw new Error('没有收到全文')
      return { text: '附带提示词正文', source: '隔离来源' }
    })
    await render(v, call)
    expect(promptCalls).toBe(0)
    await toggle(T7, true)
    expect(text()).toContain('提示词全文读取失败')
    await toggle(T7, false)
    await toggle(T7, true)
    expect(text()).toContain('附带提示词正文')
    expect(text()).not.toContain('提示词全文读取失败')
    await toggle(T7, false)
    await toggle(T7, true)
    expect(promptCalls).toBe(2)
  })

  it('15 两道注入题跨页时从始至终保留原注入卡片', async () => {
    const v = sample()
    v.data = { ...v.data, reference: null, injectionCount: 2, injectionPoints: 0, groups: [], referenceUnavailableReason: '本报告有多道注入题，无法确定按哪一道计算' }
    const item = v.items.find(i => i.definition.kind === 'injection')!
    const call = vi.fn().mockResolvedValue(v.data)
    await render(v, call, { complete: false })
    expect(fold(T8)).toBeUndefined()
    expect(crossCalls(call)).toBe(0)
    await render({ ...v, items: [...v.items, { ...item, itemId: 'other-injection', orderIndex: 50 }] }, call)
    expect(fold(T8)).toBeUndefined()
    expect(text()).toContain(v.data.referenceUnavailableReason)
  })

  it('16 独立页调用方式继续展示原明细及悬停 JSON', async () => {
    const base = sample('07')
    const item = base.items.find(i => i.definition.kind === 'injection')!
    await act(() => root.render(React.createElement(ResultCards, { verdict: { injection: item.result!.evidence } })))
    const old = fold('采样明细与评分标准')!
    expect(old.querySelector('tbody td')?.getAttribute('title')).toContain('"total":7')
    expect(fold(T8)).toBeUndefined()
  })

  it('17 切换报告后迟到结果只属于原报告', async () => {
    const v1 = sample(), v2 = sample('0e')
    const waiting = deferred<HubCrossCheck>()
    const call = vi.fn().mockReturnValueOnce(waiting.promise).mockResolvedValue(v2.data)
    await render(v1, call)
    await render(v2, call)
    await act(() => waiting.resolve(v1.data))
    expect(text()).toContain('结论：A 未见异常')
    expect(text()).not.toContain('结论：D')
  })

  it.each([
    ['stale', '已保存的逐题核对可能过时'],
    ['unavailable', '逐题核对没有完成：隔离错误。'],
    ['invalid', '逐题核对记录不完整'],
    ['none', '这份报告没有可用的逐题核对记录'],
    ['ok', '本次没有可核对用量的题目'],
  ] as const)('18 核对状态 %s 只显示对应说明', async (status, expected) => {
    const v = sample()
    v.data = { ...v.data, prefixStatus: status, prefixReason: '隔离错误', perItemChecked: status === 'ok', groups: status === 'ok' ? [] : null }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(fold('逐题核对')?.textContent).toContain(expected)
    expect(text()).not.toContain('逐题核对（14 题）')
  })
})

async function bootApp() {
  const v = sample()
  const calls: Array<{ op: string; params: Record<string, any> }> = []
  const next = new Map<string, (() => unknown)[]>()
  const state = {
    // Keep the report in the sidebar's recent window without changing its fixture version.
    report: { ...v.report, createdAtMs: Date.now() }, items: v.items, data: v.data,
    calls,
    count(op: string) { return calls.filter(call => call.op === op).length },
    enqueue(op: string, callback: () => unknown) {
      const group = next.get(op) ?? []
      group.push(callback)
      next.set(op, group)
    },
  }
  const reply = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
  const response = (m: any, fields: object) => reply({ kind: 'chanshui.response', v: 1, generation: 1, id: m.id, ...fields })
  const post = vi.spyOn(window, 'postMessage').mockImplementation((m: any) => {
    if (m.kind === 'chanshui.READY') {
      reply({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId: 'workspace-prefix', visible: true, target: {}, catalog: { workspaceId: 'workspace-prefix', groups: [] } })
      return
    }
    if (m.kind !== 'chanshui.request') return
    calls.push({ op: m.op, params: m.params })
    const overridden = next.get(m.op)?.shift()
    let value: unknown = {}
    if (m.op === 'runtime') value = { enabled: true, available: true, active: null }
    if (m.op === 'reports') value = { items: [state.report] }
    if (m.op === 'report') value = state.report
    if (m.op === 'items') value = { items: state.items }
    if (m.op === 'cross-check') value = state.data
    if (m.op === 'image') value = { dataURL: 'data:image/png;base64,eA==' }
    if (m.op === 'settings') value = { timeoutSeconds: 120, totalTimeoutSeconds: 3600, concurrency: 32, maxOutputTokens: 4096, retries: 0, maxAttempts: 128, memoryLimitMiB: 256, revision: 1 }
    void Promise.resolve().then(() => overridden ? overridden() : value).then(
      data => response(m, { ok: true, value: data }),
      e => response(m, { ok: false, error: { message: e.message, status: e.status ?? 500, operationResult: 'not_started' } }),
    )
  })
  dispose = () => post.mockRestore()
  await act(() => root.render(React.createElement(HubApp)))
  return state
}

async function openSidebarReport(id: string) {
  const selected = container.querySelector<HTMLButtonElement>(`button[data-report-id="${id}"]`)
  expect(selected).toBeTruthy()
  await act(() => selected!.click())
}

async function startRetry() {
  const button = [...container.querySelectorAll('button')].find(b => b.textContent?.startsWith('补测失败项'))
  expect(button?.disabled).toBe(false)
  await act(() => button!.click())
}

function retryable(items: HubItem[]) {
  return items.map(i => i.itemId === 'Q1' ? { ...i, status: 'error', result: { ...i.result, error: '隔离失败题' } } : i)
}

describe('实际 HubApp 的读取、核查和补测', () => {
  it('19 终态打开与补测结束都在完整的当前版本题目之后判读', async () => {
    const s = await bootApp()
    const first = deferred<{ items: HubItem[]; nextCursor?: string }>(), second = deferred<{ items: HubItem[] }>()
    s.enqueue('items', () => first.promise)
    s.enqueue('items', () => second.promise)
    await openSidebarReport(s.report.reportId)
    expect(text()).toContain(H3b)
    expect(s.count('cross-check')).toBe(0)
    await act(() => first.resolve({ items: s.items.slice(0, 2), nextCursor: 'page-2' }))
    expect(s.count('cross-check')).toBe(0)
    await act(() => second.resolve({ items: s.items.slice(2) }))
    expect(s.count('cross-check')).toBe(1)
    expect(text()).toContain('结论：D')

    s.report = { ...s.report, status: 'running', updatedAtMs: s.report.updatedAtMs! + 1, processExited: false }
    await click('核查运行状态')
    expect(text()).toContain(H3)
    expect(text()).not.toContain('结论：D')
    expect(s.count('cross-check')).toBe(1)
    s.report = { ...s.report, status: 'done', updatedAtMs: s.report.updatedAtMs! + 1, processExited: true }
    s.data = { ...s.data, reportStatus: 'done', reportUpdatedAtMs: s.report.updatedAtMs! }
    const reread = deferred<{ items: HubItem[] }>()
    s.enqueue('items', () => reread.promise)
    await click('核查运行状态')
    expect(text()).toContain(H3b)
    expect(s.count('cross-check')).toBe(1)
    await act(() => reread.resolve({ items: s.items }))
    expect(s.count('cross-check')).toBe(2)
    expect(text()).toContain('结论：D')
  })

  it('20、23 接受补测后丢弃旧读取，受理明确拒绝时恢复当前版本', async () => {
    const s = await bootApp()
    s.items = retryable(s.items)
    const cross1 = deferred<HubCrossCheck>(), rejected = deferred<unknown>()
    s.enqueue('cross-check', () => cross1.promise)
    s.enqueue('retry', () => rejected.promise)
    await openSidebarReport(s.report.reportId)
    const reads = s.count('report')
    await startRetry()
    await act(() => cross1.resolve({ ...s.data, reportUpdatedAtMs: -1 }))
    expect(s.count('report')).toBe(reads)
    expect(text()).toContain(H3)
    expect(text()).toContain(T2c)
    expect(text()).not.toContain(H5c)
    await act(() => rejected.reject(new HubRequestError('隔离补测未受理', 409, { operationResult: 'not_started' })))
    expect(s.count('cross-check')).toBe(2)
    expect(text()).toContain('结论：D')

    // Open a fresh report version to make another read in flight before the retry.
    s.report = { ...s.report, updatedAtMs: s.report.updatedAtMs! + 2 }
    s.data = { ...s.data, reportUpdatedAtMs: s.report.updatedAtMs! }
    const old = s.data, cross2 = deferred<HubCrossCheck>()
    s.enqueue('cross-check', () => cross2.promise)
    await click('核查运行状态')
    s.enqueue('retry', () => {
      s.report = { ...s.report, status: 'queued', updatedAtMs: s.report.updatedAtMs! + 1, processExited: false }
      return {}
    })
    await startRetry()
    await act(() => cross2.resolve(old))
    expect(text()).toContain(H3)
    expect(text()).not.toContain('结论：D')
    expect(s.count('cross-check')).toBe(3)
  })

  it('21 不一致等待用户，真实 openReport 成功后才读取新版本', async () => {
    const s = await bootApp()
    const waiting = deferred<HubCrossCheck>()
    s.enqueue('cross-check', () => waiting.promise)
    await openSidebarReport(s.report.reportId)
    const readsBefore = s.count('report')
    await act(() => waiting.resolve({ ...s.data, reportUpdatedAtMs: -1 }))
    expect(text()).toContain(H5c)
    expect(s.count('report')).toBe(readsBefore)
    s.report = { ...s.report, updatedAtMs: s.report.updatedAtMs! + 1 }
    s.data = { ...s.data, reportUpdatedAtMs: s.report.updatedAtMs! }
    const refreshed = deferred<HubReport>()
    s.enqueue('report', () => refreshed.promise)
    await click(H5d)
    expect(s.count('report')).toBe(readsBefore + 1)
    await act(() => refreshed.resolve(s.report))
    // The existing openReport finally advances pollEpoch and checks status again.
    expect(s.count('report')).toBe(readsBefore + 2)
    expect(s.count('cross-check')).toBe(2)
    expect(text()).toContain('结论：D')
    expect(text()).not.toContain(H5c)
  })

  it('22 用户等待刷新时出现的 V2 成功结果不会被重置', async () => {
    const s = await bootApp()
    s.enqueue('cross-check', () => ({ ...s.data, reportUpdatedAtMs: -1 }))
    await openSidebarReport(s.report.reportId)
    const late = deferred<HubReport>(), original = s.report
    s.enqueue('report', () => late.promise)
    await click(H5d)
    expect(findButton(H5d)?.disabled).toBe(true)
    s.report = { ...s.report, updatedAtMs: s.report.updatedAtMs! + 1 }
    s.data = { ...s.data, reportUpdatedAtMs: s.report.updatedAtMs! }
    await click('核查运行状态')
    expect(text()).toContain('结论：D')
    expect(s.count('cross-check')).toBe(2)
    await act(() => late.resolve(original))
    expect(text()).toContain('结论：D')
    expect(s.count('cross-check')).toBe(2)
  })

  it('重读报告失败后等待成功的新报告和题目才再次判读', async () => {
    const s = await bootApp()
    s.enqueue('cross-check', () => ({ ...s.data, reportUpdatedAtMs: -1 }))
    await openSidebarReport(s.report.reportId)
    expect(text()).toContain(H5c)
    const originalReads = s.count('report')
    const reread = deferred<HubReport>(), page = deferred<{ items: HubItem[] }>()
    s.enqueue('report', () => { throw new HubRequestError('隔离报告读取失败', 500) })
    s.enqueue('report', () => reread.promise)
    s.enqueue('items', () => page.promise)
    await click(H5d)
    expect(text()).toContain('隔离报告读取失败')
    expect(text()).toContain(H3b)
    expect(s.count('report')).toBe(originalReads + 2)
    expect(s.count('cross-check')).toBe(1)
    s.report = { ...s.report, updatedAtMs: s.report.updatedAtMs! + 1 }
    s.data = { ...s.data, reportUpdatedAtMs: s.report.updatedAtMs! }
    await act(() => reread.resolve(s.report))
    expect(s.count('cross-check')).toBe(1)
    await act(() => page.resolve({ items: s.items }))
    expect(s.count('cross-check')).toBe(2)
    expect(text()).toContain('结论：D')
  })

  it.each(['runtime', 'report'])('重读报告与接手核查均失败时保留回答并等待新读取：%s', async failingOperation => {
    const s = await bootApp()
    s.enqueue('cross-check', () => ({ ...s.data, reportUpdatedAtMs: -1 }))
    await openSidebarReport(s.report.reportId)
    const refresh = deferred<HubReport>(), failedCheck = deferred<never>()
    s.enqueue('report', () => refresh.promise)
    s.enqueue(failingOperation, () => failedCheck.promise)

    await click(H5d)
    expect(findButton(H5d)?.disabled).toBe(true)
    await click('核查运行状态')
    await act(() => refresh.reject(new HubRequestError('隔离手动重读失败', 500)))
    expect(s.count('cross-check')).toBe(1)
    await act(() => failedCheck.reject(new HubRequestError('隔离接手核查失败', 500)))
    expect(text()).toContain('隔离接手核查失败')
    expect(text()).toContain(`已读取${s.items.length}项回答；本次回答已读完。`)
    expect(s.count('cross-check')).toBe(1)
    expect(text()).toContain(H3b)
    expect(text()).not.toContain('结论：D')

    const page = deferred<{ items: HubItem[] }>()
    s.enqueue('items', () => page.promise)
    await click('核查运行状态')
    expect(s.count('cross-check')).toBe(1)
    await act(() => page.resolve({ items: s.items }))
    expect(s.count('cross-check')).toBe(2)
    expect(text()).toContain('结论：D')
  })

  it.each(['runtime-window', 'item-window', 'same-version', 'busy', 'double-click'])('24 核查期间保留读取约束：%s', async test => {
    const s = await bootApp()
    const pending = deferred<HubCrossCheck>()
    s.enqueue('cross-check', () => pending.promise)
    await openSidebarReport(s.report.reportId)
    const readsBefore = s.count('report')
    const runtime = deferred<{ enabled: boolean; available: boolean; active: null }>(), items = deferred<{ items: HubItem[] }>()
    const runtimeValue = { enabled: true, available: true, active: null }
    if (test !== 'item-window') s.enqueue('runtime', () => runtime.promise)
    else s.enqueue('items', () => items.promise)
    await click('核查运行状态')
    expect(text()).toContain(H3b)
    expect(text()).not.toContain('结论：D')
    let second: typeof runtime | undefined
    if (test === 'double-click') {
      second = deferred()
      s.enqueue('runtime', () => second!.promise)
      await click('核查运行状态')
      await act(() => runtime.resolve(runtimeValue))
      expect(text()).toContain(H3b)
    }
    if (test === 'busy') await act(() => pending.reject(new HubRequestError('隔离忙', 429)))
    else await act(() => pending.resolve(test === 'same-version' ? s.data : { ...s.data, reportUpdatedAtMs: -1 }))
    expect(text()).toContain(H3b)
    expect(text()).not.toContain(H5c)
    expect(text()).not.toContain(H5)
    expect(s.count('cross-check')).toBe(1)
    expect(s.count('report')).toBe(readsBefore + (test === 'item-window' ? 1 : 0))
    if (test === 'item-window') await act(() => items.resolve({ items: s.items }))
    else await act(() => (second ?? runtime).resolve(runtimeValue))
    expect(s.count('report')).toBe(readsBefore + 1)
    expect(s.count('cross-check')).toBe(test === 'same-version' ? 1 : 2)
    expect(text()).toContain('结论：D')
  })

  it('25 判读的退避不占宿主调用，期间可以打开已保存图片', async () => {
    vi.useFakeTimers()
    const s = await bootApp()
    s.items = s.items.map(item => item.itemId === 'vision' ? { ...item, imageAvailable: true } : item)
    s.enqueue('cross-check', () => { throw new HubRequestError('隔离查询槽暂忙', 429) })
    await openSidebarReport(s.report.reportId)
    expect(text()).toContain(H4)
    const image = container.querySelector<HTMLDetailsElement>('details[data-item-id="vision"]')
    expect(image).toBeTruthy()
    await act(() => {
      image!.open = true
      const button = [...image!.querySelectorAll('button')].find(btn => btn.textContent === '查看题目图片')
      expect(button).toBeTruthy()
      button!.click()
    })
    expect(s.count('image')).toBe(1)
    expect(s.count('cross-check')).toBe(1)
    await waitTime(1000)
    expect(s.count('cross-check')).toBe(2)
    expect(text()).toContain('结论：D')
  })
})

const oldPlaceholder = { id: 'sub2api-placeholder', name: 'Sub2API 旧版占位提示词', identity: '', tokens: 7, diff: 0, exact: true }
const gpt55 = { id: 'codex-gpt-5.5', name: 'Codex GPT-5.5 版提示词', identity: 'GPT-5', tokens: 4376, diff: 4, exact: false }
const group = (overhead: number, match: typeof gpt55, sameAsReference = false) => ({
  overhead, overheadMax: overhead, prefixTokens: overhead - 10, count: 1,
  items: [{ itemId: 'Q1', orderIndex: 0, overhead }], sameAsReference, match,
})

describe('判读合成边界', () => {
  it('26 参考值未知不掩盖相差 5 token 的唯一分组匹配', async () => {
    const v = sample()
    v.data = { ...v.data, grade: 'C', codes: ['prompt_mismatch'],
      reference: { ...v.data.reference!, prefixTokens: 4385, overhead: 4395, match: null },
      groups: [group(4390, gpt55, true)],
    }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain('结论：C')
    expect(text()).toContain('上游加的内容：注入检测请求 A 加了约 4,385 token，不在已知提示词库里，无法判断是什么。')
    expect(text()).toContain('另有 1 道题（固定开销约 4,390 token）配的是接近「Codex GPT-5.5 版提示词」的内容（相差 4 token），不是 gpt-6.2-sol 应配的提示词。')
  })

  it('26b 请求 A 未加内容与题目使用 GPT-6.2 Sol 提示词同时成立', async () => {
    const v = sample('07')
    const match = { id: 'codex-gpt-6.2-sol', name: 'Codex GPT-6.2 Sol 版提示词', identity: 'GPT-6', tokens: 5410, diff: 0, exact: true }
    v.data = { ...v.data, model: 'gpt-6.2-sol', checked: 1, groups: [group(5420, match)], expectedPromptIds: [match.id] }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain('线路：本次核对的 1 道题都走同一条线路。')
    expect(text()).toContain('上游加的内容：注入检测请求 A 没有加内容。')
    expect(text()).toContain('另有 1 道题（固定开销约 5,420 token）配的是「Codex GPT-6.2 Sol 版提示词」')
    expect(text()).toContain('上游没有在请求 A 前加内容。')
  })

  it('26c 多条知识发现各自显示实际答对份数', async () => {
    const v = sample()
    v.data.knowledge = { rows: 2, peerReports: 3, contradictions: [
      { eventId: 'x', event: '甲', date: '2025-01-01', knownBy: 3 },
      { eventId: 'y', event: '乙', date: '2025-02-01', knownBy: 2 },
    ] }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain('有至少 2 份报告答对了它')
    expect(text()).toContain('甲（2025-01-01，3 份答对）；乙（2025-02-01，2 份答对）')
    expect(text()).not.toContain('有 3 份报告答对了它')
  })

  it('27 对照表外的模型也显示其它线路用了旧占位提示词', async () => {
    const v = sample('07')
    v.data = { ...v.data, model: 'claude-opus-5-5', grade: 'C', expectedPromptIds: [], expectedPromptNames: [], codes: ['prompt_mismatch'], groups: [group(17, oldPlaceholder)] }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain('结论：C')
    expect(text()).toContain('另有 1 道题（固定开销约 17 token）配的是「Sub2API 旧版占位提示词」。')
  })

  it('28 接近占位提示词用不同说明，展开全文保持接近措辞', async () => {
    const v = sample()
    v.data.reference = { ...v.data.reference!, prefixTokens: 15, match: { ...oldPlaceholder, diff: 8, exact: false } }
    const call = vi.fn(async (op: string) => op === 'cross-check' ? v.data : { text: '程序附带正文', source: '隔离来源' })
    await render(v, call)
    expect(text()).toContain('这段内容接近 Sub2API 2026-05-04 之前版本的占位提示词（相差 8 token）')
    expect(text()).toContain('接近「Sub2API 旧版占位提示词」的内容（相差 8 token）')
    expect(text()).not.toContain('之前版本的占位提示词吻合')
    await toggle(T7, true)
    expect(text()).toContain('可能是它的改动版本')
    expect(text()).toContain('来源：隔离来源。')
    expect(text()).not.toContain('推断上游加的就是它')
  })

  it.each([null, 0, 'not_applicable'] as const)('29、29a 不适用、无效分数和明确 0 分互斥：%s', async earned => {
    const v = sample()
    v.data = { ...v.data, reference: null, groups: null, perItemChecked: false, expectedPromptIds: [], codes: [],
      grade: earned === 0 ? 'C' : 'E',
      selfReport: { present: true, status: earned === 'not_applicable' ? 'not_applicable' : 'known', earned: earned === 0 ? 0 : null, itemId: 'Q10', max: 5, byPrompt: false },
    }
    await render(v, vi.fn().mockResolvedValue(v.data))
    const selected = earned === 'not_applicable' ? 'self.na' : earned === null ? 'self.unconfirmed' : 'self.gpt5'
    const line = checkLines(v.data).find(l => l.code === selected)!
    expect(text()).toContain(lineText(line, v.data))
    expect(text()).toContain(earned === 0 ? '结论：C' : '结论：E 无法判断')
    for (const code of ['self.unconfirmed', 'self.na', 'self.gpt5']) {
      if (code !== selected) expect(checkLines(v.data).some(l => l.code === code)).toBe(false)
    }
  })

  it('29 两组对称差值各写自己的固定开销', async () => {
    const v = sample('07')
    v.data = { ...v.data, model: 'gpt-6.2-sol', grade: 'C', expectedPromptIds: ['codex-gpt-6.2-sol'],
      groups: [group(4382, { ...gpt55, diff: -4 }), group(4390, gpt55)] }
    await render(v, vi.fn().mockResolvedValue(v.data))
    expect(text()).toContain('另有 1 道题（固定开销约 4,382 token）配的是接近「Codex GPT-5.5 版提示词」的内容（相差 4 token）')
    expect(text()).toContain('另有 1 道题（固定开销约 4,390 token）配的是接近「Codex GPT-5.5 版提示词」的内容（相差 4 token）')
  })
})
