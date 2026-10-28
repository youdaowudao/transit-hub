// @vitest-environment jsdom
import { c32AutoSettings } from './fixtures/c32ModelControl'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Header from '@/modules/admin/components/dashboard/QuestionAnswerModelControlHeader.vue'
import Row from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import Drawer from '@/modules/admin/components/dashboard/QuestionAnswerModelControlDrawer.vue'
import { ConnectionHealthApiError } from '@/modules/admin/api/connectionHealth'
import { modelControlBusyTargets } from '@/modules/admin/utils/questionAnswerModelControl'
import { managed, targetId } from './fixtures/v2911ModelControl'
const api = vi.hoisted(() => Object.fromEntries(['getModelControlAutoSettings', 'getModelControlSettings', 'saveModelControlSettings', 'previewModelControl', 'executeModelControl', 'removeModelControlManaged', 'getModelControlVerifyTargets', 'listModelControlItems', 'listModelControlEvents', 'verifyModelControl'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(w: T): T => { wrappers.push(w); return w }
const button = (w: VueWrapper, text: string) => { const found = w.findAll('button').find(b => b.text() === text); if (!found) throw new Error(`Missing ${text}: ${w.text()}`); return found }
const row = (value = managed(), extra = {}) => { if (value.round && value.round.questions.length === 1) Object.assign(value.round.questions[0]!, { correct: value.round.correct, incorrect: value.round.incorrect, unreviewed: value.round.unreviewed, failed: value.round.failed, cancelled: value.round.cancelled, accuracyPercent: value.round.accuracyPercent }); return track(mount(Row, { props: { item: value, targetId, modelName: value.modelName, workspace: 'ws1', ...extra } })) }
const settings = { minAccuracyPercent: 50, minJudgedAnswers: 3, version: 1 }
const counts = { total: 47, open: 30, closed: 10, attention: 7, untested: 5 }
beforeEach(() => {
 vi.resetAllMocks(); modelControlBusyTargets.value = new Set()
 api.getModelControlAutoSettings.mockResolvedValue(c32AutoSettings()); api.getModelControlSettings.mockResolvedValue(settings); api.saveModelControlSettings.mockResolvedValue({ ...settings, version: 2 })
 api.previewModelControl.mockResolvedValue({ item: managed(), entries: [{ key: 'M', value: 'M', state: 'to_add' }], groups: [], blockReasonKey: '', requestHealth: { checked: true, allowed: true, reasonKey: '' }, planFingerprint: 'fp' })
 api.executeModelControl.mockResolvedValue({ item: managed(), outcome: 'added', entries: [{ key: 'M', value: 'M', state: 'added' }] }); api.removeModelControlManaged.mockResolvedValue(undefined)
 api.listModelControlItems.mockResolvedValue({ items: [managed()], page: 1, totalPages: 2, counts }); api.listModelControlEvents.mockResolvedValue({ items: [], page: 1, totalPages: 1 }); api.getModelControlVerifyTargets.mockResolvedValue({ targetIds: [targetId] }); api.verifyModelControl.mockResolvedValue({ items: [managed()], errors: [] })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks(); modelControlBusyTargets.value = new Set() })
const missingGeneral = '主站：这个账号目前未开放这个模型。可点“开放此模型”加入白名单；测试不足也能经二次确认处理，确认后会改主站。'
const missingBad = '主站：这个账号目前未开放这个模型，本轮测试建议保持关闭。仍可人工开放，须二次确认；确认后会改主站。'
const missingItem = () => {
 const value = managed(); value.control.observation.state = 'not_provided'; value.attention = false
 value.round!.correct = 0; value.round!.incorrect = 3; value.round!.accuracyPercent = 0
 return value
}
const expectNoSiteAction = (w: VueWrapper, visible = false) => {
 expect(w.findAll('button').some(b => ['开放此模型', '关闭此模型'].includes(b.text()))).toBe(visible)
 expect(api.previewModelControl).not.toHaveBeenCalled(); expect(api.executeModelControl).not.toHaveBeenCalled()
}
describe('missing whitelist model copy', () => {
 it('keeps an unnamed managed row identifiable with its existing connection label and does not hide a missing name', async () => {
  const value = managed(); value.accountName = ' '
  const w = row(value, { showAccount: true, accountLabel: { name: '已保存模型账号全名', siteNames: ['模型网站'] } })
  expect(w.text()).toContain('账号「已保存模型账号全名」')
  expect(w.text()).not.toContain('模型网站')
  expect(w.text()).not.toContain(targetId)
  await w.setProps({ accountLabel: undefined })
  expect(w.text()).toContain('账号（名称暂未读到）')
  expect(w.get('[data-testid="model-control-row"]').exists()).toBe(true)
  expect(api.executeModelControl).not.toHaveBeenCalled()
 })
 it('T1 identifies a judged bad round while preserving counts and allowing confirmed human opening', () => {
  const w = row(missingItem())
  expect(w.get('header').text()).toContain('本轮不合格 · 主站未开放')
  expect(w.get('[data-testid="model-control-state"]').text()).toBe(missingBad)
  expect(w.text()).toContain('0%'); expect(w.text()).toContain('已判 3/3')
  expect(w.text()).not.toContain('无需处理'); expect(w.text()).not.toContain(missingGeneral)
  expect(button(w, '不再管理').exists()).toBe(true); expect(w.classes()).toContain('border-border/50')
  expectNoSiteAction(w, true)
 })
 it.each(['no_evidence', 'testing', 'awaiting_review', 'insufficient'] as const)('T2 preserves %s progress without calling it a bad round', async decision => {
  const value = missingItem(); value.decision = decision; value.basis.decision = decision
  if (decision === 'no_evidence') { value.round = null; value.decisionReason = 'no_participating_round' }
  if (decision === 'testing') { value.round!.running = true; value.round!.completedAt = null; value.round!.incorrect = 0; value.round!.accuracyPercent = null }
  if (decision === 'awaiting_review') { value.round!.unreviewed = 1; value.round!.incorrect = 2 }
  if (decision === 'insufficient') { value.round!.incorrect = 2; value.round!.failed = 1; value.round!.cancelled = 1 }
  const w = row(value)
  expect(w.get('header').text()).toContain('主站未开放')
  expect(w.get('[data-testid="model-control-state"]').text()).toBe(missingGeneral)
  expect(w.text()).not.toContain('本轮不合格'); expect(w.text()).not.toContain('无需处理')
  const progress = { no_evidence: '今日未测', testing: '这一轮还没跑完', awaiting_review: '还有 1 条答案没判对错', insufficient: '这些题还没有足够样本' }
  expect(w.text()).toContain(progress[decision]); expectNoSiteAction(w, decision !== 'testing')
  if (decision === 'no_evidence') expect(w.text()).toContain('最近 20 批里没有这个模型的测试')
  if (decision === 'insufficient') {
   expect(w.text()).toContain('已判 2/3'); expect(w.text()).toContain('失败 1'); expect(w.text()).toContain('取消 1')
   await button(w, '看原因').trigger('click'); expect(w.emitted('view-round')![0]).toEqual([value.round!.batchId]); expectNoSiteAction(w, decision !== 'testing')
  }
 })
 it.each(['AccountReadFailed', 'Busy', 'Processing', 'executeUnconfirmed', 'accountUnconfirmed', 'accountPending', 'pending'] as const)('T4 preserves the %s override and reread behavior', async kind => {
  const value = missingItem()
  if (kind === 'accountPending') value.control.accountPending = { modelName: 'M', operation: 'close', state: 'unknown' }
  if (kind === 'pending') value.control.pending = { operation: 'close', phase: 'sending', receipt: '', state: 'unknown', startedAt: value.round!.createdAt, sendStartedAt: value.round!.createdAt }
  const extra = ['AccountReadFailed', 'Busy', 'Processing'].includes(kind) ? { issue: { reasonKey: `admin.connectionHealth.errors.modelControl${kind}`, at: 1 } } : kind === 'executeUnconfirmed' || kind === 'accountUnconfirmed' ? { [kind]: true } : {}
  const w = row(value, extra)
  const expected = { AccountReadFailed: '这次没读到', Busy: '这个账号正在做健康探活或其他操作', Processing: '正在写入主站', executeUnconfirmed: '主站没有明确答复', accountUnconfirmed: '主站没有明确答复', accountPending: '这个账号上一次操作还没确认结果', pending: missingBad }
  expect(w.get('[data-testid="model-control-state"]').text()).toContain(expected[kind])
  if (kind !== 'pending') expect(w.get('[data-testid="model-control-state"]').text()).not.toContain(missingBad)
  if (['accountPending', 'accountUnconfirmed', 'pending'].includes(kind)) expect(button(w, '不再管理').attributes('disabled')).toBeDefined()
  await button(w, '重新读取主站结果').trigger('click'); expect(w.emitted('verify-now')![0]).toEqual([targetId]); expectNoSiteAction(w, !['executeUnconfirmed', 'accountUnconfirmed'].includes(kind))
 })
 it.each(['closedEntries', 'conflictReason'] as const)('T5 excludes the bad missing sentence when only %s is present', kind => {
  const value = missingItem()
  if (kind === 'closedEntries') value.control.closedEntries = { alias: 'M' }
  else value.control.conflictReason = 'admin.connectionHealth.errors.modelControlAddKeyConflict'
  const w = row(value)
  expect(w.get('header').text()).toContain(kind === 'closedEntries' ? '已关闭 · 复测仍不合格' : '需要你处理')
  expect(w.get('[data-testid="model-control-state"]').text()).toBe(missingGeneral)
  expect(w.get('[data-testid="model-control-state"]').text()).not.toContain(missingBad)
  expectNoSiteAction(w)
 })
 it.each(['closed', 'partially_closed', 'account_missing', 'unverified'] as const)('T5 retains %s observation copy and the upstream model warning', state => {
  const value = missingItem(); value.control.observation.state = state
  if (state === 'closed' || state === 'partially_closed') value.control.closedEntries = { alias: 'M' }
  const w = row(value, { availableModels: [] })
  const expected = { closed: '主站：已关闭', partially_closed: '主站：部分关闭', account_missing: '主站：这个账号已在主站删除', unverified: '主站：还没读取' }
  expect(w.get('[data-testid="model-control-state"]').text()).toContain(expected[state])
  expect(w.text()).not.toContain(missingBad); expect(w.text()).toContain('这个账号现在的模型列表里没有它，问答测不到；可以不再管理')
  if (state === 'closed') expect(button(w, '开放此模型').exists()).toBe(true)
  if (state === 'partially_closed') { expect(button(w, '关闭剩余条目').exists()).toBe(true); expect(button(w, '加回已关闭条目').exists()).toBe(true) }
  if (state === 'account_missing' || state === 'unverified') expectNoSiteAction(w)
 })
})
describe('workspace header settings and complete counts', () => {
 it('uses the existing connection name for verification failures and events instead of a target suffix', async () => {
  const value = managed(); value.accountName = ''
  api.listModelControlItems.mockResolvedValue({ items: [value], page: 1, totalPages: 1, counts })
  api.verifyModelControl.mockResolvedValue({ items: [], errors: [{ targetId, reasonKey: 'request_failed' }] })
  api.listModelControlEvents.mockResolvedValue({ items: [{ id: 'name-event', targetId, modelName: 'M', eventType: 'verify_failed', actorUserId: '', createdAt: '2026-10-08T00:00:00Z', detail: {} }], page: 1, totalPages: 1 })
  const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api', accountLabels: { [targetId]: { name: '完整模型提醒账号', siteNames: ['模型网站'] } } } }))
  await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises()
  expect(w.text()).toContain('完整模型提醒账号')
  expect(w.text()).not.toContain(targetId.slice(-6))
  await w.get('[data-testid="model-control-tab-events"]').trigger('click'); await flushPromises()
  expect(w.get('[data-testid="model-control-event"]').text()).toContain('完整模型提醒账号')
  expect(w.text()).not.toContain(targetId)
 })
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
  const value = managed(); value.decision = 'usable'; value.basis.decision = 'usable'; value.round!.correct = 3; value.round!.incorrect = 0; value.round!.accuracyPercent = 100; value.control.observation.state = outcome === 'added' ? 'not_provided' : 'closed'; if (outcome === 'restored') value.control.closedEntries = { alias: 'M' }
  api.previewModelControl.mockResolvedValue({ item: value, entries: [{ key: 'M', value: 'M', state: outcome === 'added' ? 'to_add' : 'to_restore' }], groups: [], blockReasonKey: '', requestHealth: { checked: true, allowed: true, reasonKey: '' }, planFingerprint: 'fp' }); api.executeModelControl.mockResolvedValue({ item: managed(), outcome, entries: [] })
  const w = row(value)
  expect(w.classes()).toContain('border-green-500/35'); expect(w.text()).toContain('100%'); expect(w.text()).toContain('已判 3/3')
  if (outcome === 'added') { expect(w.get('header').text()).toContain('白名单没有 · 今天合格，可开放'); expect(w.get('[data-testid="model-control-state"]').text()).toBe(missingGeneral); expect(w.text()).not.toContain(missingBad) }
  await button(w, '开放此模型').trigger('click'); await flushPromises(); expect(api.previewModelControl.mock.calls[0]!.slice(0, 4)).toEqual([targetId, 'M', outcome === 'added' ? 'add' : 'restore', value.basis]); expect(w.text()).toContain(outcome === 'added' ? '将在白名单新增：M（转给 M）' : '将加回 TransitHub 之前删掉的：M（转给 M）'); expect(api.executeModelControl).not.toHaveBeenCalled(); await button(w, '确认').trigger('click'); await flushPromises(); expect(api.executeModelControl.mock.calls[0]!.slice(0, 6)).toEqual([targetId, 'M', outcome === 'added' ? 'add' : 'restore', value.basis, 'fp', false]); expect(w.text()).toContain('已开放。白名单已允许这个模型')
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
