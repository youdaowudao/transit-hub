// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import HubReportContent from '../../chanshui/web/components/HubReportContent'
import { reportVersion } from '../../chanshui/web/lib/hub-report-view'
import type { HubItem, HubReport } from '../../chanshui/web/lib/hub-types'
import { HUB_SCORING_VERSION } from '../../chanshui/web/lib/report-view'

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react')
const { createRoot } = require('react-dom/client')
const totalReason = '缓存读写尚未确认，这一项暂按 0 分计入总分；详情见下方「缓存读写验证」，确认后总分会更新。'
const finishedReason = '缓存尚未确认，总分里这一项暂按 0 分计算。请展开下面的缓存项查看原因：六轮都已完成的，可以人工判为通过或不通过；请求失败或未完成的，可以补测。'
const runningReason = '检测还在进行，缓存这一项暂按 0 分计入总分；检测结束后这里会显示缓存结果和可用的处理办法。'
const oldReason = '缓存尚未确认。可以展开本项查看缺少的回包字段或未完成的轮次，本次报告的总分也暂未确认。'
const judgeHelp = '看过下面各轮的缓存读取和写入后由你判定：通过计入缓存这一项的分数，不通过得 0 分。只改本报告这一项并重算总分，不重新请求、不产生费用、不修改主站；之后可以改判。'
const automatedReason = '缓存尚未确认：第3轮没有返回有效的缓存读取或总输入。'
const pass = '人工判为通过'
const fail = '人工判为不通过'

let root: ReturnType<typeof createRoot>
let container: HTMLDivElement

function fixture({ score = 93, known = false, fingerprintPending = false }: { score?: number | null; known?: boolean; fingerprintPending?: boolean } = {}) {
  const cache = { key: 'cache', label: '缓存读写', applicable: true, known, points: Number(known), max_points: 1, coefficient: 5, contribution: known ? 5 : 0 }
  const fingerprint = { key: 'fingerprint', label: '模型指纹', applicable: true, known: true, points: 0, max_points: 1, coefficient: 19, contribution: 0 }
  const total = { score, max: 100, parts: fingerprintPending ? [cache, fingerprint] : [cache], reason: score === null ? oldReason : totalReason, complete: known }
  const report: HubReport = {
    reportId: '1'.repeat(32), targetId: 'fixture', accountName: '隔离缓存验收', groupName: '', model: 'gpt-6.2-sol',
    status: known ? 'done' : 'partial', processExited: true, createdAtMs: 1, updatedAtMs: 2,
    summary: { totalScore: score, total, verdict: { scoring_version: HUB_SCORING_VERSION, total, ...(fingerprintPending ? { fingerprint: {} } : {}) } },
  }
  const item: HubItem = {
    itemId: 'cache', sectionId: 'cache', status: 'done', prompt: '', expected: '',
    definition: { id: 'cache', title: '缓存读写验证', kind: 'cache', enabled: true, order: 1, maxScore: 0, template: '', revision: 1, parameters: {}, judge: { type: 'manual' } },
    result: {
      judgment: { status: known ? 'known' : 'unknown', max: 1, ...known && { earned: 1 } },
      evidence: { reason: automatedReason, pass: known ? true : null, samples: Array.from({ length: 6 }, (_, index) => ({ index: index + 1, read: 0, write: 0, total: 200 })) },
    },
  }
  return { report, item }
}

async function show(report: HubReport, item?: HubItem, call = vi.fn(), onRefresh = vi.fn(), itemsReadVersion = reportVersion(report)) {
  await React.act(async () => root.render(React.createElement(HubReportContent, {
    key: report.reportId, report, items: item ? [item] : [], complete: Boolean(item), call, onRefresh, onRetry: vi.fn(), itemsReadVersion, retryHold: false, readHold: false,
  })))
}

const totalCard = () => container.querySelector('.total-card')!
const cacheCard = () => [...container.querySelectorAll('.result-card')].find(card => card.querySelector('h3')?.textContent === '缓存读写验证')!
const cacheItem = () => cacheCard().querySelector<HTMLDetailsElement>('details[data-item-id="cache"]')!
const button = (label: string) => [...cacheItem().querySelectorAll('button')].find(node => node.textContent?.trim() === label)

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
  await React.act(async () => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

describe('模型检测报告的缓存人工判定和总分', () => {
  it('显示后端数字总分、缓存未确认分项和检测结束后的处理说明', async () => {
    const { report, item } = fixture()
    await show(report, item)
    expect(totalCard().textContent).toContain('93 / 100')
    expect(totalCard().textContent).toContain(totalReason)
    expect(totalCard().textContent).not.toContain('总分尚未确认')
    expect(totalCard().textContent).not.toContain('未知不当0分')
    expect([...totalCard().querySelectorAll('.integrity-signal')].find(row => row.querySelector('span')?.textContent === '缓存读写')?.querySelector('b')?.textContent).toBe('尚未确认')
    expect(cacheCard().textContent).toContain(finishedReason)
    expect(cacheCard().textContent).not.toContain(oldReason)
  })

  it('展开后可调用本报告的人工判定并刷新', async () => {
    const { report, item } = fixture()
    const call = vi.fn().mockResolvedValue(report)
    const refresh = vi.fn()
    await show(report, item, call, refresh)
    expect(cacheItem().open).toBe(false)
    await React.act(async () => cacheItem().querySelector('summary')!.click())
    expect(cacheItem().open).toBe(true)
    expect(cacheItem().textContent).toContain(judgeHelp)
    expect(button(pass)?.disabled).toBe(false)
    expect(button(fail)?.disabled).toBe(false)
    expect(button('补测此项')).toBeDefined()
    call.mockClear()
    await React.act(async () => button(pass)!.click())
    expect(call).toHaveBeenCalledExactlyOnceWith('judge-item', { reportId: report.reportId, itemId: 'cache', verdict: 'pass' })
    expect(refresh).toHaveBeenCalledOnce()
    expect(refresh).toHaveBeenCalledWith(report.reportId)
  })

  it.each([[1, pass, fail], [0, fail, pass]])('人工判定为 %s 后保留自动记录并允许改判', async (earned, selected, alternative) => {
    const { report, item } = fixture({ score: earned === 1 ? 98 : 93, known: true })
    const total = report.summary!.total as { parts: { points: number; contribution: number }[] }
    Object.assign(total.parts[0], { points: earned, contribution: earned === 1 ? 5 : 0 })
    item.result = { judgment: { status: 'known', max: 1, earned, source: 'manual' }, evidence: { ...(item.result!.evidence as object), pass: null } }
    await show(report, item)
    expect(cacheItem().querySelector('summary')?.textContent).toContain('人工判定')
    expect(cacheItem().querySelector('.metric-line strong')?.textContent).toBe(selected)
    expect(cacheItem().textContent).toContain(`自动检测记录：${automatedReason}`)
    expect(button('补测此项')).toBeUndefined()
    expect(button(selected)?.disabled).toBe(true)
    expect(button(alternative)?.disabled).toBe(false)
  })

  it('补测运行中遮住数字，保留后端总分理由和缓存区块原句', async () => {
    const { report, item } = fixture()
    report.status = 'running'
    report.retry = { id: 'retry', expectedRetryId: '', itemIds: ['cache'], attemptsBefore: 6, acceptedAtMs: 3 }
    await show(report, { ...item, status: 'running' })
    expect(totalCard().textContent).toContain('补测中')
    expect(totalCard().textContent).toContain(totalReason)
    expect(totalCard().textContent).not.toContain('93 / 100')
    expect(totalCard().textContent).not.toContain('未知不当0分')
    expect(cacheCard().textContent).toContain(oldReason)
  })

  it('失败或未完成只可补测，自动通过和 v6 缓存也不能人工判定', async () => {
    const { report, item } = fixture()
    for (const unavailable of [{ ...item, status: 'error', result: { ...item.result, error: '隔离缓存请求失败' } }, { ...item, status: 'queued' }]) {
      await show(report, unavailable)
      expect(button('补测此项')).toBeDefined()
      expect(button(pass)).toBeUndefined()
      expect(button(fail)).toBeUndefined()
    }
    const confirmed = fixture({ score: 98, known: true })
    await show(confirmed.report, confirmed.item)
    expect(button(pass)).toBeUndefined()
    expect(button(fail)).toBeUndefined()
    const legacy = fixture({ score: null })
    Object.assign(legacy.report.summary!.verdict as object, { scoring_version: '2026-10-02-v6-total' })
    await show(legacy.report, legacy.item)
    expect(cacheCard()).toBeDefined()
    expect([...cacheCard().querySelectorAll('button')].map(node => node.textContent?.trim())).not.toContain(pass)
    expect([...cacheCard().querySelectorAll('button')].map(node => node.textContent?.trim())).not.toContain(fail)
  })

  it('旧报告已保存的空总分和缓存区块原句仍显示', async () => {
    const { report, item } = fixture({ score: null })
    await show(report, item)
    expect(totalCard().textContent).toContain('总分尚未确认')
    expect(cacheCard().textContent).toContain(oldReason)
    expect(cacheCard().textContent).not.toContain(finishedReason)
  })

  it('检测运行中未确认缓存的人工按钮禁用', async () => {
    const { report, item } = fixture()
    const call = vi.fn()
    await show({ ...report, status: 'running' }, item, call)
    expect(button(pass)?.disabled).toBe(true)
    expect(button(fail)?.disabled).toBe(true)
    expect(cacheItem().textContent).toContain('检测或补测进行中，结束后才能人工判定。')
    await React.act(async () => button(pass)!.click())
    expect(call).not.toHaveBeenCalled()
  })

  it('首次检测显示已有分数，缓存区块只说明当前进度', async () => {
    const { report, item } = fixture()
    await show({ ...report, status: 'running' }, { ...item, status: 'running', result: undefined })
    expect(totalCard().textContent).toContain('93 / 100')
    expect(totalCard().textContent).toContain(totalReason)
    expect(cacheCard().textContent).toContain(runningReason)
    expect(cacheCard().textContent).not.toMatch(/人工判为通过或不通过|可以补测|总分也暂未确认/)
  })

  it('指纹尚无结果且缓存未确认时直接显示后端的 60 分', async () => {
    const { report, item } = fixture({ score: 60, fingerprintPending: true })
    await show({ ...report, status: 'running' }, { ...item, status: 'running', result: undefined })
    expect(totalCard().querySelector('.score-chip')?.textContent).toBe('60 / 100')
    expect(totalCard().textContent).not.toContain('79 / 100')
  })

  it.each([{ earned: 0, saved: 60, inflated: 79 }, { earned: 1, saved: 65, inflated: 84 }])('人工缓存判定得 $earned 分时显示保存的 $saved 分', async ({ earned, saved, inflated }) => {
    const { report, item } = fixture({ score: saved, known: true, fingerprintPending: true })
    report.status = 'partial'
    Object.assign(report.summary!.verdict as object, { fingerprint: { error: '指纹请求失败' } })
    const total = report.summary!.total as { parts: { points: number; contribution: number; known: boolean }[] }
    Object.assign(total.parts[0], { points: earned, contribution: 5 * earned })
    total.parts[1].known = false
    item.result = { judgment: { status: 'known', max: 1, earned, source: 'manual' }, evidence: { ...(item.result!.evidence as object), pass: null } }
    await show(report, item)
    expect(totalCard().querySelector('.score-chip')?.textContent).toBe(`${saved} / 100`)
    expect(totalCard().textContent).not.toContain(`${inflated} / 100`)
    const fingerprint = [...totalCard().querySelectorAll('.integrity-signal')].find(row => row.querySelector('span')?.textContent === '模型指纹')
    expect(fingerprint?.querySelector('b')?.textContent).toBe('0/19')
  })

  it('重开或刷新已判定报告时，逐题未读齐也先显示保存分数', async () => {
    const { report, item } = fixture({ score: 65, known: true, fingerprintPending: true })
    await show(report)
    expect(totalCard().querySelector('.score-chip')?.textContent).toBe('65 / 100')
    await show(report, item, vi.fn(), vi.fn(), '')
    expect(totalCard().querySelector('.score-chip')?.textContent).toBe('65 / 100')
    item.result = { judgment: { status: 'known', max: 1, earned: 1, source: 'manual' }, evidence: { ...(item.result!.evidence as object), pass: null } }
    await show(report, item)
    expect(totalCard().querySelector('.score-chip')?.textContent).toBe('65 / 100')
  })

  it('逐题读完后的自动缓存结论沿用原指纹展示', async () => {
    const { report, item } = fixture({ score: 65, known: true, fingerprintPending: true })
    await show(report, item)
    expect(totalCard().querySelector('.score-chip')?.textContent).toBe('84 / 100')
  })

  it('刚选择另一份报告时，缓存判定的迟到响应不会发起原报告读取', async () => {
    const original = fixture()
    const current = fixture({ score: 98, known: true })
    current.report.reportId = '2'.repeat(32)
    const reports = new Map([[original.report.reportId, original], [current.report.reportId, current]])
    const reportReads: string[] = []
    let replyToJudge: (() => void) | undefined
    let replyToNewReport: (() => void) | undefined
    const settleHub = async () => {
      for (let i = 0; i < 5; i++) await React.act(async () => new Promise(resolve => setTimeout(resolve, 0)))
    }
    const sendToHub = (data: Record<string, unknown>) => window.dispatchEvent(new MessageEvent('message', {
      data: { v: 1, generation: 1, ...data }, origin: location.origin, source: window,
    }))
    const onRequest = ({ data }: MessageEvent) => {
      if (data?.kind === 'chanshui.READY') {
        sendToHub({ kind: 'chanshui.BOOT', workspaceId: 'cache-review', visible: true, target: { reportId: original.report.reportId } })
        return
      }
      if (data?.kind !== 'chanshui.request') return
      const reply = (value: unknown) => sendToHub({ kind: 'chanshui.response', id: data.id, ok: true, value })
      const target = reports.get(data.params.reportId)
      switch (data.op) {
        case 'report':
          reportReads.push(data.params.reportId)
          if (target === current && !replyToNewReport) { replyToNewReport = () => reply(current.report); return }
          reply(target?.report)
          return
        case 'items': reply({ items: target ? [target.item] : [] }); return
        case 'reports': reply({ items: [original.report, current.report] }); return
        case 'runtime': reply({ enabled: true, available: true, active: null }); return
        case 'judge-item':
          expect(data.params).toEqual({ reportId: original.report.reportId, itemId: 'cache', verdict: 'pass' })
          replyToJudge = () => reply(original.report)
          return
        default: reply({})
      }
    }

    window.addEventListener('message', onRequest)
    try {
      await React.act(async () => root.render(React.createElement(HubApp)))
      await settleHub()
      expect(totalCard().querySelector('.score-chip')?.textContent).toBe('93 / 100')
      await React.act(async () => button(pass)!.click())
      await settleHub()
      expect(replyToJudge).toBeTypeOf('function')

      const earlierReads = reportReads.length
      await React.act(async () => {
        sendToHub({ kind: 'chanshui.OPEN_TARGET', target: { reportId: current.report.reportId } })
        expect(button(pass)).toBeDefined()
        replyToJudge!()
      })
      await settleHub()
      expect(replyToNewReport).toBeTypeOf('function')
      await React.act(async () => replyToNewReport!())
      await settleHub()
      expect(reportReads.slice(earlierReads)).toContain(current.report.reportId)
      expect(reportReads.slice(earlierReads)).not.toContain(original.report.reportId)
      expect(totalCard().querySelector('.score-chip')?.textContent).toBe('98 / 100')
    } finally {
      window.removeEventListener('message', onRequest)
    }
  })
})
