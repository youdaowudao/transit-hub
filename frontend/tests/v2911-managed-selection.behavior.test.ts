// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import Panel from '@/modules/admin/components/dashboard/QuestionAnswerModelControlPanel.vue'
import { batch, emptyBatch, managed, now, target, targetId } from './fixtures/v2911ModelControl'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'
const api = vi.hoisted(() => Object.fromEntries(['discoverTargetModels', 'listTestQuestions', 'getLatestQuestionAnswerBatch', 'getQuestionAnswerHistory', 'getQuestionAnswerBatch', 'startQuestionAnswerBatch', 'getModelControlTarget', 'getModelControlSettings', 'verifyModelControl', 'addModelControlManaged'].map(key => [key, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const defer = <T,>() => { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
const button = (w: VueWrapper, label: string) => { const found = w.findAll('button').find(b => b.text() === label); if (!found) throw new Error(`Missing ${label}: ${w.text()}`); return found }
const read = (models = ['M']) => ({ targetId, items: models.map(managed), candidates: [] })
const mounted = async () => { const w = mount(Dialog, { props: { open: false, target, questionAnswerPreferences: { modelIds: ['A'], questionIds: ['q'], reasoningEffort: 'medium', repeatCount: 1 } }, global: { stubs: { Teleport: true, Transition: false } } }); wrappers.push(w); await w.setProps({ open: true }); await flushPromises(); if (!w.find('[data-testid="question-answer-models"]').exists()) await w.get('[data-testid="question-answer-configuration-toggle"]').trigger('click'); await flushPromises(); return w }
beforeEach(() => {
 vi.resetAllMocks(); api.discoverTargetModels.mockResolvedValue(['A', 'B', 'M'].map(id => ({ id, name: id }))); api.listTestQuestions.mockResolvedValue([{ id: 'q', name: '题', body: '答案', keywords: ['答案'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }]); api.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch()); api.getQuestionAnswerHistory.mockResolvedValue({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 0, todayStats: questionAnswerFixtureStats([]) }); api.getQuestionAnswerBatch.mockResolvedValue(batch()); api.startQuestionAnswerBatch.mockResolvedValue(batch()); api.getModelControlTarget.mockResolvedValue(read()); api.getModelControlSettings.mockResolvedValue({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 1 }); api.verifyModelControl.mockResolvedValue({ items: [managed()], errors: [] }); api.addModelControlManaged.mockResolvedValue(managed('A'))
})
afterEach(async () => { wrappers.splice(0).forEach(w => w.unmount()); await flushPromises(); vi.useRealTimers(); vi.restoreAllMocks() })
describe('managed model selection waits for a current complete read', () => {
 it.each(['success', 'failure'] as const)('blocks during add and until its complete read after %s', async outcome => {
  const add = defer<ReturnType<typeof managed>>(), reload = defer<ReturnType<typeof read>>()
  const w = await mounted(); const initialReads = api.getModelControlTarget.mock.calls.length; api.addModelControlManaged.mockReturnValueOnce(add.promise); api.getModelControlTarget.mockReturnValueOnce(reload.promise)
  await button(w, '纳入管理').trigger('click'); expect(button(w, '开始回答').attributes('disabled')).toBeDefined(); expect(w.text()).toContain('正在纳入管理')
  if (outcome === 'success') add.resolve(managed('A')); else add.reject(new Error('admin.connectionHealth.errors.request'))
  await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeDefined(); w.getComponent(Panel).vm.$emit('managed-change', { targetId, kind: 'updated', models: ['A', 'M'], refreshKey: 99 }); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeDefined()
  reload.resolve(read(['A', 'M'])); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined(); expect(api.getModelControlTarget).toHaveBeenCalledTimes(initialReads + 1); expect(api.verifyModelControl).toHaveBeenCalledTimes(1); if (outcome === 'failure') expect(w.text()).toContain('纳入管理失败')
 })
 it('does not let old loaded or updated events bypass multiple additions', async () => {
  const first = defer<ReturnType<typeof managed>>(), second = defer<ReturnType<typeof managed>>()
  const w = await mounted(); api.addModelControlManaged.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
  const links = w.get('[data-testid="question-answer-models"]').findAll('button').filter(b => b.text() === '纳入管理'); await links[0]!.trigger('click'); await links[1]!.trigger('click')
  first.resolve(managed('A')); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeDefined()
  w.getComponent(Panel).vm.$emit('managed-change', { targetId, kind: 'loaded', models: ['A', 'M'], refreshKey: 0 }); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeDefined()
  api.getModelControlTarget.mockResolvedValue(read(['A', 'B', 'M'])); second.resolve(managed('B')); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined(); expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
 })
 it('keeps error readiness after a card update and permits only a complete retry', async () => {
  api.getModelControlTarget.mockRejectedValueOnce(new Error('fixture'))
  const w = await mounted(); expect(button(w, '开始回答').attributes('disabled')).toBeDefined(); w.getComponent(Panel).vm.$emit('managed-change', { targetId, kind: 'updated', models: ['M'], refreshKey: 0 }); await flushPromises(); expect(w.text()).toContain('受管模型读取失败')
  await w.getComponent(Panel).findAll('button').find(b => b.text() === '重试')!.trigger('click'); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined()
 })
 it('lists missing managed names and never submits the remaining models', async () => {
  api.discoverTargetModels.mockResolvedValue([{ id: 'A', name: 'A' }]); const w = await mounted(); expect(w.text()).toContain('受管模型 M 不在这个账号现在的模型列表里'); expect(button(w, '开始回答').attributes('disabled')).toBeDefined(); await button(w, '开始回答').trigger('click'); expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
 })
 it('ignores a late addition after closing and reopening the same account', async () => {
  const late = defer<ReturnType<typeof managed>>(); const w = await mounted(); api.addModelControlManaged.mockReturnValueOnce(late.promise); await button(w, '纳入管理').trigger('click'); await w.setProps({ open: false }); await w.setProps({ open: true }); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined()
  const reads = api.getModelControlTarget.mock.calls.length; late.reject(new Error('fixture old failure')); await flushPromises(); expect(api.getModelControlTarget).toHaveBeenCalledTimes(reads); expect(w.text()).not.toContain('纳入管理失败'); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined()
 })
 it('blocks on reentering question mode until its remounted management panel is ready', async () => {
  const w = await mounted(); await button(w, '一次性测试').trigger('click'); await flushPromises(); const reload = defer<ReturnType<typeof read>>(); api.getModelControlTarget.mockReturnValueOnce(reload.promise); await button(w, '问答测试').trigger('click'); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeDefined(); reload.resolve(read()); await flushPromises(); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined()
 })
 it('shows an active C2 batch actual selection and includes its missing managed model only afterwards', async () => {
  vi.useFakeTimers(); const running = { ...batch('A'), active: true, completedCount: 0, runningCount: 1, records: batch('A').records.map(r => ({ ...r, status: 'running', answerJudgment: null, completedAt: null })) }; api.getLatestQuestionAnswerBatch.mockResolvedValue(running); api.getQuestionAnswerBatch.mockResolvedValue(batch('A'))
  const w = await mounted(); const m = w.get('[data-testid="question-answer-models"]').findAll('label').find(label => label.text().startsWith('M'))!
  expect((m.get('input').element as HTMLInputElement).checked).toBe(false); expect(m.text()).toContain('本批未包含'); expect(w.get('[data-testid="question-answer-models"]').findAll('button').filter(b => b.text() === '纳入管理')).toHaveLength(0)
  await vi.advanceTimersByTimeAsync(2000); await flushPromises(); expect((m.get('input').element as HTMLInputElement).checked).toBe(true); expect(m.get('input').attributes('disabled')).toBeDefined(); expect(m.text()).not.toContain('本批未包含')
 })
})
