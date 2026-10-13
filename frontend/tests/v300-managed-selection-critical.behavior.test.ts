// @vitest-environment jsdom
import { c32AutoSettings } from './fixtures/c32ModelControl'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import Panel from '@/modules/admin/components/dashboard/QuestionAnswerModelControlPanel.vue'
import { batch, emptyBatch, managed, now, target, targetId } from './fixtures/v2911ModelControl'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'
const api = vi.hoisted(() => Object.fromEntries(['discoverTargetModels', 'listTestQuestions', 'getLatestQuestionAnswerBatch', 'getQuestionAnswerHistory', 'getQuestionAnswerBatch', 'startQuestionAnswerBatch', 'getModelControlTarget', 'getModelControlAutoSettings', 'getModelControlSettings', 'verifyModelControl', 'addModelControlManaged'].map(key => [key, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const defer = <T,>() => { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
const button = (w: VueWrapper, label: string) => { const found = w.findAll('button').find(b => b.text() === label); if (!found) throw new Error(`Missing ${label}: ${w.text()}`); return found }
const read = (models = ['M']) => ({ targetId, items: models.map(managed), candidates: [] })
const mounted = async () => { const w = mount(Dialog, { props: { open: false, target, questionAnswerPreferences: { modelIds: ['A'], questionIds: ['q'], reasoningEffort: 'medium', repeatCount: 1 } }, global: { stubs: { Teleport: true, Transition: false } } }); wrappers.push(w); await w.setProps({ open: true }); await flushPromises(); if (!w.find('[data-testid="question-answer-models"]').exists()) await w.get('[data-testid="question-answer-configuration-toggle"]').trigger('click'); await flushPromises(); return w }
beforeEach(() => {
 vi.resetAllMocks(); api.discoverTargetModels.mockResolvedValue(['A', 'B', 'M'].map(id => ({ id, name: id }))); api.listTestQuestions.mockResolvedValue([{ id: 'q', name: '题', body: '答案', keywords: ['答案'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }]); api.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch()); api.getQuestionAnswerHistory.mockResolvedValue({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 0, todayStats: questionAnswerFixtureStats([]) }); api.getQuestionAnswerBatch.mockResolvedValue(batch()); api.startQuestionAnswerBatch.mockResolvedValue(batch()); api.getModelControlTarget.mockResolvedValue(read()); api.getModelControlAutoSettings.mockResolvedValue(c32AutoSettings()); api.getModelControlSettings.mockResolvedValue({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 1 }); api.verifyModelControl.mockResolvedValue({ items: [managed()], errors: [] }); api.addModelControlManaged.mockResolvedValue(managed('A'))
})
afterEach(async () => { wrappers.splice(0).forEach(w => w.unmount()); await flushPromises(); vi.useRealTimers(); vi.restoreAllMocks() })
describe('V3.0.0 critical autonomous selection', () => {
 it('can deselect a managed model and sends only the user selection', async () => {
  const w = await mounted(); const label = w.get('[data-testid="question-answer-models"]').findAll('label').find(l => l.text().startsWith('M'))!;
  expect(label.get('input').attributes('disabled')).toBeUndefined();
  await label.get('input').setValue(true); await label.get('input').setValue(false);
  await button(w, '开始回答').trigger('click'); await flushPromises();
  expect(api.startQuestionAnswerBatch.mock.calls[0]?.[1]).toEqual(['A']);
  expect((w.emitted('question-answer-preferences-changed')!.at(-1)![0] as {modelIds:string[]}).modelIds).toEqual(['A']);
 });
 it('a management read failure does not prevent the selected test', async () => {
  api.getModelControlTarget.mockRejectedValue(new Error('fixture management unavailable'));
  const w = await mounted(); expect(button(w, '开始回答').attributes('disabled')).toBeUndefined();
  await button(w, '开始回答').trigger('click'); await flushPromises();
  expect(api.startQuestionAnswerBatch.mock.calls[0]?.[1]).toEqual(['A']);
 });
 it('an unselected missing managed model does not prevent the selected test', async () => {
  api.discoverTargetModels.mockResolvedValue([{id:'A',name:'A'}]); const w=await mounted();
  expect(button(w, '开始回答').attributes('disabled')).toBeUndefined();
  await button(w,'开始回答').trigger('click'); await flushPromises();
  expect(api.startQuestionAnswerBatch.mock.calls[0]?.[1]).toEqual(['A']);
 });
 it('keeps a running batch actual selection after completion', async () => {
  vi.useFakeTimers(); const running={...batch('A'),active:true,completedCount:0,runningCount:1,records:batch('A').records.map(r=>({...r,status:'running',answerJudgment:null,completedAt:null}))};
  api.getLatestQuestionAnswerBatch.mockResolvedValue(running); api.getQuestionAnswerBatch.mockResolvedValue(batch('A'));
  const w=await mounted(); await vi.advanceTimersByTimeAsync(2000); await flushPromises();
  const label=w.get('[data-testid="question-answer-models"]').findAll('label').find(l=>l.text().startsWith('M'))!;
  expect((label.get('input').element as HTMLInputElement).checked).toBe(false);
  expect(label.get('input').attributes('disabled')).toBeUndefined();
 });
});
