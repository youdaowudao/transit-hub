// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import Drawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import { c2Groups } from './fixtures/c2QuestionAnswerSchedules'
import { c32AutoSettings } from './fixtures/c32ModelControl'
import { batch, emptyBatch, now, target, targetId } from './fixtures/v2911ModelControl'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'
const api = vi.hoisted(() => Object.fromEntries(['discoverTargetModels', 'listTestQuestions', 'getLatestQuestionAnswerBatch', 'getQuestionAnswerHistory', 'getQuestionAnswerBatch', 'startQuestionAnswerBatch', 'getModelControlTarget', 'getModelControlAutoSettings', 'getModelControlSettings', 'verifyModelControl'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const button = (w: VueWrapper, name: string) => { const b = w.findAll('button').find(b => b.text() === name); if (!b) throw new Error(`Missing ${name}`); return b }
const preferences = { modelIds: [] as string[], questionIds: [] as string[], reasoningEffort: 'medium' as const, repeatCount: 1 }
const text = '此时还没有发请求'
beforeEach(() => {
 vi.resetAllMocks(); api.discoverTargetModels.mockResolvedValue([{ id: 'A', name: 'A' }]); api.listTestQuestions.mockResolvedValue([{ id: 'q', name: '题', body: '答案', keywords: ['答案'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }]); api.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch()); api.getQuestionAnswerHistory.mockResolvedValue({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 0, todayStats: questionAnswerFixtureStats([]) }); api.getQuestionAnswerBatch.mockResolvedValue(batch()); api.startQuestionAnswerBatch.mockResolvedValue(batch()); api.getModelControlTarget.mockResolvedValue({ targetId, items: [], candidates: [] }); api.getModelControlAutoSettings.mockResolvedValue(c32AutoSettings()); api.getModelControlSettings.mockResolvedValue({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 1 }); api.verifyModelControl.mockResolvedValue({ items: [], errors: [] })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks() })
const dialog = async (selection = preferences) => { const w = mount(Dialog, { props: { open: false, target, questionAnswerPreferences: selection }, global: { stubs: { Teleport: true, Transition: false } } }); wrappers.push(w); await w.setProps({ open: true }); await flushPromises(); return w }
describe('V3.0.0 restored defaults stay explicit without a contradictory post-submit notice', () => {
 it('account defaults appear before any request and disappear during and after the actual request', async () => {
  let resolve!: (value: ReturnType<typeof batch>) => void; api.startQuestionAnswerBatch.mockReturnValueOnce(new Promise(done => { resolve = done }))
  const w = await dialog(); expect(w.text()).toContain(text); expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
  await button(w, '开始回答').trigger('click'); await flushPromises(); expect(api.startQuestionAnswerBatch.mock.calls[0]![1]).toEqual(['A']); expect(w.text()).not.toContain(text)
  resolve(batch()); await flushPromises(); expect(w.text()).not.toContain(text)
 })
 it('an explicit model edit clears only the default-restoration notice', async () => {
  const w = await dialog(); expect(w.text()).toContain(text); await w.get('[data-testid="question-answer-models"] input[type="checkbox"]').setValue(false); await flushPromises(); expect(w.text()).not.toContain(text); expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
 })
 it('account replaces remembered disabled questions with visible defaults before starting, preserving repeat count', async () => {
  api.listTestQuestions.mockResolvedValue([{ id: 'disabled', name: '旧停用题', body: '旧题', keywords: [], enabled: false, isDefault: false, createdAt: now, updatedAt: now }, { id: 'q', name: '启用默认题', body: '答案', keywords: ['答案'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }])
  const w = await dialog({ ...preferences, modelIds: ['A'], questionIds: ['disabled'], repeatCount: 2 })
  expect(w.get('[data-testid="question-answer-configuration-toggle"]').attributes('aria-expanded')).toBe('true')
  const questions = w.get('[data-testid="question-answer-questions"]'); expect(questions.text()).toContain('启用默认题'); expect(questions.text()).not.toContain('旧停用题'); expect((questions.get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)
  expect((w.get('#question-answer-repeat-count').element as HTMLSelectElement).value).toBe('2'); expect(w.text()).toContain(text); expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
  await button(w, '开始回答').trigger('click'); await flushPromises(); expect(api.startQuestionAnswerBatch.mock.calls[0]!.slice(0, 5)).toEqual([targetId, ['A'], ['q'], 'medium', 2]); expect(w.text()).not.toContain(text)
 })
 it('C1 uses enabled default question preparation when all remembered questions are disabled, preserving repeat count', async () => {
  api.listTestQuestions.mockResolvedValue([{ id: 'disabled', name: '旧停用题', body: '旧题', keywords: [], enabled: false, isDefault: false, createdAt: now, updatedAt: now }, { id: 'q', name: '启用默认题', body: '答案', keywords: ['答案'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }])
  const groups = c2Groups(); groups[0]!.accounts = [{ ...groups[0]!.accounts[0]!, targetId }]
  const w = mount(Drawer, { props: { groups, preferenceScope: 'ws1', preferences: { ...preferences, modelIds: ['A'], questionIds: ['disabled'], repeatCount: 2, batchTargetIds: [targetId] } }, global: { stubs: { Teleport: true } } }); wrappers.push(w)
  await w.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises()
  const config = w.get('[data-testid="question-answer-batch-config"]'); expect(config.text()).toContain('启用默认题'); expect(config.text()).not.toContain('旧停用题'); expect(config.text()).toContain('每组合 2 次'); expect(w.text()).toContain(text); expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
  await w.get('[data-testid="question-answer-batch-start"]').trigger('click'); await flushPromises(); expect(api.startQuestionAnswerBatch.mock.calls[0]!.slice(0, 5)).toEqual([targetId, ['A'], ['q'], 'medium', 2]); expect(w.text()).not.toContain(text)
 })
 it('C1 restored preparation is visible before requests, and the notice does not return after completion', async () => {
  const groups = c2Groups(); groups[0]!.accounts = [{ ...groups[0]!.accounts[0]!, targetId }]
  let resolve!: (value: ReturnType<typeof batch>) => void; api.startQuestionAnswerBatch.mockReturnValueOnce(new Promise(done => { resolve = done }))
  const w = mount(Drawer, { props: { groups, preferenceScope: 'ws1', preferences: { ...preferences, batchTargetIds: [targetId] } }, global: { stubs: { Teleport: true } } }); wrappers.push(w)
  await w.get('[data-testid="question-answer-batch-open"]').trigger('click'); await flushPromises(); expect(w.text()).toContain(text); expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
  await w.get('[data-testid="question-answer-batch-start"]').trigger('click'); await flushPromises(); expect(api.startQuestionAnswerBatch).toHaveBeenCalledTimes(1); expect(w.text()).not.toContain(text)
  resolve(batch()); await flushPromises(); expect(w.text()).not.toContain(text)
 })
})
