// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Header from '@/modules/admin/components/dashboard/QuestionAnswerModelControlHeader.vue'
import Row from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import Drawer from '@/modules/admin/components/dashboard/QuestionAnswerModelControlDrawer.vue'
import { ConnectionHealthApiError } from '@/modules/admin/api/connectionHealth'
import { modelControlBusyTargets } from '@/modules/admin/utils/questionAnswerModelControl'
import { managed, targetId } from './fixtures/v2911ModelControl'
const api = vi.hoisted(() => Object.fromEntries(['getModelControlSettings', 'saveModelControlSettings', 'previewModelControl', 'executeModelControl', 'removeModelControlManaged', 'getModelControlVerifyTargets', 'listModelControlItems', 'listModelControlEvents', 'verifyModelControl'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(w: T): T => { wrappers.push(w); return w }
const button = (w: VueWrapper, text: string) => { const found = w.findAll('button').find(b => b.text() === text); if (!found) throw new Error(`Missing ${text}: ${w.text()}`); return found }
const row = (value = managed(), extra = {}) => track(mount(Row, { props: { item: value, targetId, modelName: value.modelName, workspace: 'ws1', ...extra } }))
const settings = { minAccuracyPercent: 50, minJudgedAnswers: 3, version: 1 }
const counts = { total: 47, open: 30, closed: 10, attention: 7, untested: 5 }
beforeEach(() => {
 vi.resetAllMocks(); modelControlBusyTargets.value = new Set()
 api.getModelControlSettings.mockResolvedValue(settings); api.saveModelControlSettings.mockResolvedValue({ ...settings, version: 2 })
 api.previewModelControl.mockResolvedValue({ item: managed(), entries: [{ key: 'M', value: 'M', state: 'to_add' }], groups: [], blockReasonKey: '', requestHealth: { checked: true, allowed: true, reasonKey: '' }, planFingerprint: 'fp' })
 api.executeModelControl.mockResolvedValue({ item: managed(), outcome: 'added', entries: [{ key: 'M', value: 'M', state: 'added' }] }); api.removeModelControlManaged.mockResolvedValue(undefined)
 api.listModelControlItems.mockResolvedValue({ items: [managed()], page: 1, totalPages: 2, counts }); api.listModelControlEvents.mockResolvedValue({ items: [], page: 1, totalPages: 1 }); api.getModelControlVerifyTargets.mockResolvedValue({ targetIds: [targetId] }); api.verifyModelControl.mockResolvedValue({ items: [managed()], errors: [] })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks(); modelControlBusyTargets.value = new Set() })
describe('workspace header settings and complete counts', () => {
 it('saves two settings without model/source fields and handles optimistic conflicts', async () => {
  const w = track(mount(Header, { props: { scope: 'account', workspace: 'ws1', counts } })); await flushPromises(); await button(w, '修改').trigger('click')
  expect(w.findAll('input[type="checkbox"]')).toHaveLength(0)
  api.saveModelControlSettings.mockRejectedValueOnce(new ConnectionHealthApiError('admin.connectionHealth.errors.modelControlVersionConflict', 409, { ...settings, minAccuracyPercent: 70, version: 3 }))
  await w.get('form').trigger('submit'); await flushPromises(); expect(w.text()).toContain('规则已被别处修改，已显示最新值'); expect((w.get('[aria-label="可用线"]').element as HTMLInputElement).value).toBe('70')
  await w.get('form').trigger('submit'); await flushPromises(); expect(api.saveModelControlSettings.mock.calls.at(-1)!.slice(0, 2)).toEqual([{ minAccuracyPercent: 70, minJudgedAnswers: 3 }, 3])
 })
 it('shows failed settings independently and retries only the settings read', async () => {
  api.getModelControlSettings.mockRejectedValueOnce(new Error('fixture'))
  const w = track(mount(Header, { props: { scope: 'workspace', workspace: 'ws1', counts } })); await flushPromises(); expect(w.text()).toContain('规则读取失败'); expect(w.get('[data-testid="model-control-count-total"]').text()).toContain('47')
  await button(w, '重试').trigger('click'); await flushPromises(); expect(w.text()).not.toContain('规则读取失败'); expect(api.getModelControlSettings).toHaveBeenCalledTimes(2); expect(api.verifyModelControl).not.toHaveBeenCalled()
 })
 it('retains global counts when filtering, paging and viewing operation records', async () => {
  const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api' } })); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises()
  await button(w, '下一页').trigger('click'); await flushPromises(); await w.get('[aria-label="模型筛选"]').setValue('M'); await w.get('[aria-label="模型筛选"]').trigger('change'); await flushPromises(); await w.get('[data-testid="model-control-tab-events"]').trigger('click'); await flushPromises()
  expect(w.get('[data-testid="model-control-count-total"]').text()).toContain('47'); expect(w.get('[data-testid="model-control-count-closed"]').text()).toContain('10'); expect(api.verifyModelControl).toHaveBeenCalledTimes(1); expect(w.find('[data-testid="model-control-tab-rules"]').exists()).toBe(false)
 })
})
describe('model action copy and protected removals', () => {
 it.each(['added', 'restored'] as const)('shows the open success copy for %s', async outcome => {
  const value = managed(); value.decision = 'usable'; value.control.observation.state = outcome === 'added' ? 'not_provided' : 'closed'; if (outcome === 'restored') value.control.closedEntries = { alias: 'M' }
  api.previewModelControl.mockResolvedValue({ item: value, entries: [{ key: 'M', value: 'M', state: outcome === 'added' ? 'to_add' : 'to_restore' }], groups: [], blockReasonKey: '', requestHealth: { checked: true, allowed: true, reasonKey: '' }, planFingerprint: 'fp' }); api.executeModelControl.mockResolvedValue({ item: managed(), outcome, entries: [] })
  const w = row(value); await button(w, '开放此模型').trigger('click'); await flushPromises(); expect(w.text()).toContain(outcome === 'added' ? '将在白名单新增：M（转给 M）' : '将加回 TransitHub 之前删掉的：M（转给 M）'); expect(api.executeModelControl).not.toHaveBeenCalled(); await button(w, '确认').trigger('click'); await flushPromises(); expect(w.text()).toContain('已开放。白名单已允许这个模型')
 })
 it('lists the existing conflicting entry without a confirmation write', async () => {
  const value = managed(); value.decision = 'usable'; value.control.observation.state = 'not_provided'
  api.previewModelControl.mockResolvedValue({ item: value, entries: [{ key: 'M', value: 'B', state: 'blocking' }], groups: [], blockReasonKey: 'admin.connectionHealth.errors.modelControlAddKeyConflict', requestHealth: { checked: false }, planFingerprint: 'fp' })
  const w = row(value); await button(w, '开放此模型').trigger('click'); await flushPromises(); expect(w.text()).toContain('M → B · 挡住了新增'); expect(w.findAll('button').some(b => b.text() === '确认')).toBe(false); expect(api.executeModelControl).not.toHaveBeenCalled()
 })
 it.each(['closed', 'unconfirmed', 'account_missing'] as const)('confirms %s removal without changing the upstream', async kind => {
  const value = managed(); if (kind === 'closed') value.control.closedEntries = { alias: 'M' }; if (kind === 'unconfirmed') value.control.unconfirmedClose = { entries: { late: 'M' }, sentAt: '2026-10-11T00:00:00Z', expiresAt: '2026-10-12T00:00:00Z' }; if (kind === 'account_missing') value.control.observation.state = kind
  const w = row(value); await button(w, '不再管理').trigger('click'); expect(api.removeModelControlManaged).not.toHaveBeenCalled(); expect(w.get('[data-testid="model-control-remove-confirm"]').text()).toContain(kind === 'account_missing' ? '这个账号已在主站删除' : '主站保持现在的样子'); await button(w, kind === 'account_missing' ? '确认不再管理' : '仍然不再管理（主站保持现状）').trigger('click'); await flushPromises(); expect(api.removeModelControlManaged.mock.calls[0]!.slice(0, 4)).toEqual([targetId, 'M', 1, true]); expect(api.executeModelControl).not.toHaveBeenCalled()
 })
 it.each(['prepare', 'remove'] as const)('explains Busy during %s without retrying', async action => {
  const cause = new ConnectionHealthApiError('admin.connectionHealth.errors.modelControlBusy', 409)
  api.previewModelControl.mockRejectedValue(cause); api.removeModelControlManaged.mockRejectedValue(cause)
  const w = row(); await button(w, action === 'prepare' ? '关闭此模型' : '不再管理').trigger('click'); await flushPromises(); expect(w.text()).toContain('这次没有完成，请几秒后再点一次'); expect(w.emitted('execute-unconfirmed')).toBeUndefined(); expect(action === 'prepare' ? api.previewModelControl : api.removeModelControlManaged).toHaveBeenCalledTimes(1)
 })
 it.each(['OpenAIPassthrough', 'EmptyMapping', 'WildcardMapping', 'WildcardFallback'])('explains %s from the current observation even when historical closed entries remain', reason => {
  const value = managed(); value.decision = 'usable'; value.control.closedEntries = { alias: 'M' }; value.control.observation.state = 'not_isolatable'; value.control.observation.reasonKey = `admin.connectionHealth.errors.modelControl${reason}`
  const w = row(value); expect(w.get('[data-testid="model-control-state"]').text()).toContain('主站：开放中，但无法单独关闭'); expect(w.get('[data-testid="model-control-state"]').text()).not.toContain('无法确认'); expect(button(w, '开放此模型').exists()).toBe(true)
 })
 it('offers reread for an interrupted operation without marking execution uncertain on a 409', async () => {
  api.previewModelControl.mockRejectedValue(new ConnectionHealthApiError('admin.connectionHealth.errors.modelControlLeaseLost', 409))
  const w = row(); await button(w, '关闭此模型').trigger('click'); await flushPromises(); expect(button(w, '重新读取主站结果').exists()).toBe(true); expect(w.emitted('execute-unconfirmed')).toBeUndefined()
 })
 it('emits the failed round and preserves one primary action while testing hides both', async () => {
  const value = managed(); value.round!.failed = 1
  const w = row(value); await button(w, '看原因').trigger('click'); expect(w.emitted('view-round')![0]).toEqual([value.round!.batchId]); await w.setProps({ item: { ...value, decision: 'testing' } }); expect(w.findAll('button').some(b => ['关闭此模型', '开放此模型'].includes(b.text()))).toBe(false)
 })
})
