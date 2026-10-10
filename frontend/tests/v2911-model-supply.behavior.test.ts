// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Detail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import { modelControlSupplyWarning, modelControlSummaryTooltip } from '@/modules/admin/utils/questionAnswerModelControl'
import type { ModelControlAccountSummary, ModelControlSupplyItem } from '@/modules/admin/types/connectionHealth'
import { c2Groups } from './fixtures/c2QuestionAnswerSchedules'
const wrappers: VueWrapper[] = []
const supply = (modelName = 'M', open = 0, unknown = 0): ModelControlSupplyItem => ({ modelName, open, unknown, closed: 1, openAccounts: open ? ['可用账号'] : [], closedAccounts: ['关闭账号'] })
const mounted = (items: ModelControlSupplyItem[], other = 0, summary?: ModelControlAccountSummary) => { const group = c2Groups()[0]!; group.modelSupply = { items, otherTypeAccounts: other }; if (summary) group.accounts[0]!.modelControl = summary; const w = mount(Detail, { props: { group, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false } }); wrappers.push(w); return w }
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks() })
describe('group supply and account summary remain compact and explain saved states', () => {
 it.each([[0, 0, '没有账号', 'red'], [0, 2, '无法确认', 'amber'], [1, 0, '只剩 1 个', 'amber']] as const)('renders %s open/%s unknown with the proper warning', (open, unknown, text, tone) => {
  const w = mounted([supply('M', open, unknown)]); const card = w.get('[data-testid="model-control-supply-M"]'); expect(card.text()).toContain(text); expect(card.classes().some(value => value.includes(`border-${tone}`))).toBe(true); if (unknown) expect(card.text()).not.toContain('没有账号')
  const warning = modelControlSupplyWarning([supply('M', open, unknown)])!; expect(warning.tone).toBe(tone); expect(warning.tooltip).toContain(text)
 })
 it('omits a healthy group warning and the section when there are no managed models', () => {
  expect(modelControlSupplyWarning([supply('M', 3)])).toBeNull(); const w = mounted([]); expect(w.find('[data-testid="model-control-supply"]').exists()).toBe(false)
 })
 it('prioritizes a missing-account warning and folds more than eight models', async () => {
  const items = Array.from({ length: 9 }, (_, i) => supply(`M${i}`, i)); const warning = modelControlSupplyWarning(items)!; expect(warning.tone).toBe('red'); expect(warning.label).toBe('M0 没有账号'); expect(warning.tooltip).toContain('M1：只剩 1 个')
  const w = mounted(items, 2); expect(w.find('[data-testid="model-control-supply-M8"]').exists()).toBe(false); await w.findAll('button').find(b => b.text() === '展开全部（9）')!.trigger('click'); expect(w.find('[data-testid="model-control-supply-M8"]').exists()).toBe(true); expect(w.text()).toContain('本组另有 2 个账号不是 OpenAI/Anthropic 的 API Key 账号'); expect(w.text()).toContain('随分组刷新更新')
 })
 it('shows open and closed values and attention only when it is positive', async () => {
  const summary: ModelControlAccountSummary = { open: 2, closed: 1, attention: 0, models: [] }; const w = mounted([], 0, summary); expect(w.get('[data-testid="model-control-summary"]').text()).toBe('模型 开 2 · 关 1'); expect(w.get('[data-testid="model-control-summary"]').text()).not.toContain('需处理'); await w.get('[data-testid="model-control-summary"]').trigger('click'); expect(w.emitted('question-answer-view')).toHaveLength(1)
  const group = c2Groups()[0]!; group.accounts[0]!.modelControl = { ...summary, attention: 2 }; await w.setProps({ group }); expect(w.get('[data-testid="model-control-summary"]').text()).toContain('需处理 2')
 })
 it('renders each model confirmation time and the never-read state in its tooltip', async () => {
  vi.useFakeTimers(); const summary: ModelControlAccountSummary = { open: 1, closed: 1, attention: 1, models: [{ modelName: 'open-M', status: 'open', attention: false, decision: 'usable', checkedAt: '2026-10-11T08:31:00Z' }, { modelName: 'closed-M', status: 'closed', attention: true, decision: 'close_recommended', checkedAt: '2026-10-09T02:31:00Z' }, { modelName: 'unknown-M', status: 'unknown', attention: false, decision: 'no_evidence', checkedAt: null }] }
  const text = modelControlSummaryTooltip(summary); expect(text).toContain('以下是各模型上次确认的结果'); expect(text).toContain('open-M（2026年10月11日 16:31 确认）'); expect(text).toContain('closed-M（2026年10月9日 10:31 确认）'); expect(text).toContain('unknown-M（未确认）')
  const w = mounted([], 0, summary); await w.get('[data-testid="model-control-summary"]').element.parentElement!.dispatchEvent(new MouseEvent('mouseenter')); await vi.advanceTimersByTimeAsync(150); await flushPromises(); expect(document.body.textContent).toContain('unknown-M（未确认）')
 })
})
