// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { expect, it, vi } from 'vitest'
import HubReportContent from '../../chanshui/web/components/HubReportContent'
import { hubReportView } from '../../chanshui/web/lib/hub-report-view'
import { I18nProvider } from '../../chanshui/web/lib/i18n'
import type { HubItem } from '../../chanshui/web/lib/hub-types'
import { restorationReport } from './fixtures/chanshui-report-restoration'

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')

it('型号核对展示结果、折叠未提问题并只补测失败题', async () => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const failed: HubItem = {
    itemId: 'knowledge:a', sectionId: 'knowledge', status: 'error', prompt: '题目', expected: 'A',
    definition: { id: 'knowledge:a', title: '证明事件', kind: 'knowledge', enabled: true, order: 1, maxScore: 0, template: '', parameters: {}, judge: { type: 'manual' } },
    result: { error: '上游没有回答，请重试', judgment: { status: 'unknown' } },
  }
  const skipped: HubItem = { ...failed, itemId: 'knowledge:b', status: 'done', definition: { ...failed.definition, id: 'knowledge:b', title: '其他事件' }, result: { judgment: { status: 'not_asked' } } }
  const knowledge = { status: 'unknown', label: '型号核对未确认', reason: '本轮有题目未完成，请补测。', protocol: 'chat',
    boundary_review: { method: 'model_group', status: 'unconfirmed', reason: 'invalid' },
    rows: [{ id: 'a', event: '证明事件', happened_at: '2026-02-13', kind: 'error', error: '上游没有回答，请重试', unsure: true, ok: false, answer: '' }] }
  const report = restorationReport({ model: 'gpt-6.1-sol', status: 'partial', summary: { verdict: { knowledge } } })
  const retry = vi.fn(), call = vi.fn()
  const node = document.createElement('div'); document.body.append(node)
  const root = createRoot(node)
  try {
    await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(HubReportContent, { report, items: [failed, skipped], complete: true, call, onRetry: retry }))))
    const card = node.querySelector('.knowledge-card')!
    expect(card.textContent).toContain('型号核对未确认')
    expect(card.textContent).toContain('本轮只核对该型号的题组')
    expect(card.textContent).toContain('这道事件题用于核对型号表现')
    expect(card.textContent).not.toContain('这道知识事件题用于判断日期范围')
    expect(card.textContent).not.toContain('公开知识截止日期为')
    const skippedFold = Array.from(card.querySelectorAll('details')).find(el => el.textContent?.includes('本轮未提问的题目与原因'))!
    expect(skippedFold.open).toBe(false)
    const button = Array.from(card.querySelectorAll('button')).find(el => el.textContent?.includes('补测此项'))!
    await React.act(async () => button.click())
    expect(retry).toHaveBeenCalledOnce()
    expect(retry).toHaveBeenCalledWith(['knowledge:a'])
    expect(call).not.toHaveBeenCalled()

    const mismatch = { ...knowledge, status: 'mismatch', correct: 4, label: '与该型号不符，答出了更新型号才知道的事件', boundary_review: { method: 'model_group', status: 'complete', reason: 'complete' } }
    const mismatchReport = restorationReport({ model: 'gpt-6.1-sol', status: 'done', summary: { verdict: { knowledge: mismatch } } })
    await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(HubReportContent, { report: mismatchReport, items: [failed, skipped], complete: true, call, onRetry: retry }))))
    expect(card.querySelector('.metric-line strong')?.className).toBe('warn')
    expect(card.textContent).toContain('与该型号不符')

    const pending = { ...knowledge, reason: '型号题组尚未完成，可查看检测状态和已保存的回答。', boundary_review: { method: 'model_group', status: 'unconfirmed', reason: 'pending' }, rows: [] }
    const pendingReport = restorationReport({ model: 'gpt-6.1-sol', status: 'running', summary: { verdict: { knowledge: pending } } })
    const waiting: HubItem = { ...failed, itemId: 'knowledge:c', status: 'queued', definition: { ...failed.definition, id: 'knowledge:c', title: '等待候选事件' }, result: {} }
    const probe = hubReportView(pendingReport, [waiting, skipped], false).probes?.find(p => p.id === 'knowledge')
    expect(probe?.name).toBe('知识检测按型号核对')
    expect(probe?.status).toBe('queued')
    await React.act(async () => root.render(React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(HubReportContent, { report: pendingReport, items: [waiting], complete: false, call, onRetry: retry }))))
    expect(card.textContent).toContain('型号题组尚未完成')
    expect(card.textContent).toContain('另有 1 道事件题等待处理')
    expect(card.textContent).not.toContain('等待候选事件')
    expect(card.textContent).not.toContain('回答无效或疑似联网')
    expect(card.textContent).not.toContain('请展开查看具体原因并补测')
  } finally {
    await React.act(async () => root.unmount()); node.remove(); vi.unstubAllGlobals()
  }
})
