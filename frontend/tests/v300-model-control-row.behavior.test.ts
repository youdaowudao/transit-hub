// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Row from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import Rule from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRuleForm.vue'
import { questionAnswerDuplicateQuestionNames } from '@/modules/admin/utils/questionAnswers'
import { modelControlBusyTargets, modelControlEventLines } from '@/modules/admin/utils/questionAnswerModelControl'
import { managed, targetId, now } from './fixtures/v2911ModelControl'
import type { ModelControlDecision, ModelControlOperation } from '@/modules/admin/types/connectionHealth'
const api = vi.hoisted(() => ({ previewModelControl: vi.fn(), executeModelControl: vi.fn(), saveModelControlSettings: vi.fn() }))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const button = (w: VueWrapper, text: string) => { const value = w.findAll('button').find(b => b.text() === text); if (!value) throw new Error(`Missing ${text}: ${w.text()}`); return value }
const itemFor = (op: ModelControlOperation, decision: ModelControlDecision) => { const item = managed(); item.decision = decision; item.basis.decision = decision; if (op === 'add') item.control.observation.state = 'not_provided'; if (op === 'restore') { item.control.observation.state = 'closed'; item.control.closedEntries = { a: 'M' } } return item }
const plan = (item = managed()) => ({ item, entries: [{ key: 'a', value: 'M', state: 'to_close' }], groups: [], blockReasonKey: '', requestHealth: { checked: false, allowed: null, reasonKey: '' }, accountStatus: 'active', accountSchedulable: true, planFingerprint: 'fp' })
const row = (item = managed()) => { const w = mount(Row, { props: { item, targetId, modelName: 'M', workspace: 'ws1' } }); wrappers.push(w); return w }
beforeEach(() => { vi.resetAllMocks(); modelControlBusyTargets.value = new Set() })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); modelControlBusyTargets.value = new Set(); vi.restoreAllMocks() })
describe('V3.0.0 human model controls', () => {
 for (const op of ['close', 'add', 'restore'] as const) for (const decision of ['no_evidence', 'awaiting_review', 'insufficient', 'close_recommended', 'usable'] as const) {
  it(`${op}/${decision} previews independently and requires confirmation only without supporting advice`, async () => {
   const item = itemFor(op, decision); api.previewModelControl.mockResolvedValue(plan(item)); api.executeModelControl.mockResolvedValue({ item, outcome: op === 'close' ? 'closed' : op === 'add' ? 'added' : 'restored' })
   const w = row(item); await button(w, op === 'close' ? '关闭此模型' : '开放此模型').trigger('click'); await flushPromises()
   const unsupported = op === 'close' ? decision !== 'close_recommended' : decision !== 'usable'
   const checkbox = w.find('[data-testid="model-control-preview"] input[type="checkbox"]'); expect(checkbox.exists()).toBe(unsupported)
   if (unsupported) { expect(button(w, '确认').attributes('disabled')).toBeDefined(); await checkbox.setValue(true) }
   await button(w, '确认').trigger('click'); await flushPromises(); expect(api.executeModelControl.mock.calls[0]!.slice(0, 6)).toEqual([targetId, 'M', op, item.basis, 'fp', unsupported])
  })
 }
 it('partially closed exposes two actions while switching cancels the old preview and confirmation', async () => {
  const item = itemFor('restore', 'insufficient'); item.control.observation.state = 'partially_closed'; api.previewModelControl.mockResolvedValue(plan(item))
  const w = row(item); await button(w, '关闭剩余条目').trigger('click'); await flushPromises(); await w.get('[data-testid="model-control-preview"] input').setValue(true)
  await button(w, '加回已关闭条目').trigger('click'); await flushPromises(); expect(w.findAll('[data-testid="model-control-preview"]')).toHaveLength(1); expect((w.get('[data-testid="model-control-preview"] input').element as HTMLInputElement).checked).toBe(false); expect(api.previewModelControl.mock.calls.map(call => call[2])).toEqual(['close', 'restore']); expect(api.executeModelControl).not.toHaveBeenCalled()
 })
 it.each(['testing', 'missing', 'pending', 'conflict', 'basis'])('invalidates an opened restoration preview when %s changes', async kind => {
  const item = itemFor('restore', 'insufficient'); api.previewModelControl.mockResolvedValue(plan(item)); const w = row(item); await button(w, '开放此模型').trigger('click'); await flushPromises(); await w.get('[data-testid="model-control-preview"] input').setValue(true)
  const changed = structuredClone(item)
  if (kind === 'testing') changed.decision = 'testing'
  if (kind === 'missing') changed.control.observation.state = 'account_missing'
  if (kind === 'pending') changed.control.accountPending = { modelName: 'M', operation: 'restore', state: 'unknown' }
  if (kind === 'conflict') changed.control.conflictReason = 'changed'
  if (kind === 'basis') changed.basis.batchId = 'new'
  await w.setProps({ item: changed }); expect(w.find('[data-testid="model-control-preview"]').exists()).toBe(false); expect(api.executeModelControl).not.toHaveBeenCalled()
 })
 it('displays the concrete current failure ahead of a legacy generic reason', async () => {
  const item = managed(); item.control.lastAttempt = { operation: 'close', outcome: 'failed', reasonKey: 'admin.connectionHealth.errors.modelControlAccountReadFailed', entries: [], groups: [], at: now }; api.previewModelControl.mockResolvedValue(plan(item)); api.executeModelControl.mockResolvedValue({ item, outcome: 'failed', reasonKey: 'admin.connectionHealth.errors.modelControlAccountReadFailed', errorKey: 'M模型的写入步骤未完成：Invalid request（HTTP 400）。已读回并确认原条目保持，请处理原因后重新预览。' })
  const w = row(item); await button(w, '关闭此模型').trigger('click'); await flushPromises(); await button(w, '确认').trigger('click'); await flushPromises(); expect(w.get('[role="alert"]').text()).toContain('Invalid request'); expect(w.get('[role="alert"]').text()).toContain('写入'); expect(w.get('[role="alert"]').text()).not.toContain('读不到这个账号的白名单'); expect(w.get('[data-testid="model-control-last-attempt"]').text()).not.toContain('读不到这个账号的白名单'); expect(w.get('[data-testid="model-control-last-attempt"]').text()).toContain('上次操作记录'); expect(api.executeModelControl).toHaveBeenCalledTimes(1)
 })
 it('clears preview and emits unknown-result protection after an invalid execution response', async () => {
  const item = managed(); api.previewModelControl.mockResolvedValue(plan(item)); api.executeModelControl.mockRejectedValue(new Error('M的操作请求已发送，但返回内容无法读取：缺少逐题统计。暂时无法确认主站是否修改，请点“重新读取主站结果”，不要直接重试。'))
  const w = row(item); await button(w, '关闭此模型').trigger('click'); await flushPromises(); await button(w, '确认').trigger('click'); await flushPromises(); expect(w.find('[data-testid="model-control-preview"]').exists()).toBe(false); expect(w.emitted('execute-unconfirmed')?.[0]).toEqual([targetId, 'M']); expect(w.text()).toContain('不要直接重试'); expect(w.text()).not.toContain('没有修改主站')
 })
 it('renders per-question progress and snapshot duplicates without aggregate sample credit', () => {
  const item = itemFor('close', 'insufficient'); item.round!.correct = 9; item.round!.incorrect = 1; item.round!.accuracyPercent = 50; item.round!.questions = [{ questionId: 'a', questionName: 'A', correct: 9, incorrect: 0, unreviewed: 0, failed: 0, cancelled: 0, accuracyPercent: 100 }, { questionId: 'b', questionName: 'B', correct: 0, incorrect: 1, unreviewed: 0, failed: 2, cancelled: 0, accuracyPercent: 0 }]; item.round!.duplicateQuestionIds = ['a', 'b']
  const w = row(item); expect(w.text()).toContain('参考正确率（样本不足）'); expect(w.text()).toContain('B 1/3 条'); expect(w.text()).toContain('别的题多答不能补足'); expect(w.text()).toContain('按独立题目分别计分'); expect(w.text()).not.toContain('已判 10/3')
 })
 it('duplicate prompt normalization matches snapshots while preserving inner whitespace and distinct IDs', () => {
  const names = questionAnswerDuplicateQuestionNames([{ id: 'a', name: 'A', body: '\u0085 X\r\nY \u0085' }, { id: 'b', name: 'B', body: 'X\nY' }, { id: 'c', name: 'C', body: 'X  Y' }, { id: 'd', name: 'D', body: '\ufeffX\nY' }]); expect(names).toEqual(['A', 'B'])
 })
 it('retains old settings event meaning', () => { expect(modelControlEventLines({ id: 'old', targetId, modelName: 'M', eventType: 'settings_saved', createdAt: now, basis: {}, detail: { minAccuracyPercent: 50, minJudgedAnswers: 2 } }).sentence).toBe('当时保存的规则：可用线 50%，最少已判回答 2 条（按当时规则）。') })
 it('shows legacy high settings and accepts only a new 3—10 save', async () => {
  const w = mount(Rule, { props: { workspace: 'ws1', settings: { minAccuracyPercent: 50, minJudgedAnswers: 12, version: 1 } } }); wrappers.push(w); expect(w.text()).toContain('以前设为每题 12 条'); expect(button(w, '保存规则').attributes('disabled')).toBeDefined(); const count = w.get('input[aria-label="每题最少已判回答（3—10）"]'); await count.setValue(2); expect(button(w, '保存规则').attributes('disabled')).toBeDefined(); await count.setValue(3); expect(button(w, '保存规则').attributes('disabled')).toBeUndefined(); api.saveModelControlSettings.mockResolvedValue({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 2 }); await w.get('form').trigger('submit'); await flushPromises(); expect(api.saveModelControlSettings.mock.calls[0]![0]).toEqual({ minAccuracyPercent: 50, minJudgedAnswers: 3 })
 })
})
