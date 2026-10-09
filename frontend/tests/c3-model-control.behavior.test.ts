// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import QuestionAnswerModelControlRow from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import QuestionAnswerModelControlPanel from '@/modules/admin/components/dashboard/QuestionAnswerModelControlPanel.vue'
import QuestionAnswerModelControlDrawer from '@/modules/admin/components/dashboard/QuestionAnswerModelControlDrawer.vue'
import QuestionAnswerModelControlRuleForm from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRuleForm.vue'
import QuestionAnswerStatsBar from '@/modules/admin/components/dashboard/QuestionAnswerStatsBar.vue'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import { ConnectionHealthApiError, getQuestionAnswerRecentSummaries } from '@/modules/admin/api/connectionHealth'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import { useQuestionAnswerRecentSummaries } from '@/modules/admin/composables/useQuestionAnswerRecentSummaries'
import { modelControlBusyTargets } from '@/modules/admin/utils/questionAnswerModelControl'
import type { ModelControlItem, ModelControlPreview, ModelControlRule, QuestionAnswerRecentSummaryItem } from '@/modules/admin/types/connectionHealth'
import { c2Groups, c2Stats } from './fixtures/c2QuestionAnswerSchedules'

const api = vi.hoisted(() => Object.fromEntries(['getModelControlTarget', 'listModelControlItems', 'listModelControlRules', 'saveModelControlRule', 'deleteModelControlRule', 'listModelControlEvents', 'getModelControlVerifyTargets', 'verifyModelControl', 'addModelControlManaged', 'removeModelControlManaged', 'previewModelControl', 'executeModelControl', 'getConnectionHealthAdminGroups', 'refreshConnectionHealthAdminGroupsAutomatically'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const targetId = 'sub2api:ws1:a', now = '2026-10-09T12:00:00Z'
const errorKey = (suffix: string) => `admin.connectionHealth.errors.modelControl${suffix}`
const rule = (patch: Partial<ModelControlRule> = {}): ModelControlRule => ({ modelName: 'A', minAccuracyPercent: 50, minJudgedAnswers: 3, includeManual: true, includeScheduled: true, version: 1, ...patch })
const item = (patch: Partial<ModelControlItem> = {}): ModelControlItem => ({ targetId, accountName: '隔离账号', modelName: 'A', version: 1, rule: rule(), round: { batchId: 'batch-a', source: 'manual', scheduleName: null, createdAt: now, completedAt: now, running: false, correct: 1, incorrect: 2, unreviewed: 0, failed: 0, cancelled: 0, accuracyPercent: 100 / 3 }, previousRound: null, decision: 'close_recommended', decisionReason: '', basis: { batchId: 'batch-a', ruleVersion: 1, decision: 'close_recommended' }, control: { closedEntries: {}, closedAt: null, closedAccuracyPercent: null, pending: null, unconfirmedClose: null, accountPending: null, conflictReason: '', observation: { state: 'serving', reasonKey: '', sources: [], accountStatus: 'active', accountSchedulable: true, checkedAt: now }, lastAttempt: null }, coverage: { schedules: [] }, health: { recentlyProbed: false, state: null }, ...patch })
const closed = (): ModelControlItem => { const value = item(); value.control.closedEntries = { a: 'A', alias: 'A' }; value.control.closedAt = now; value.control.observation.state = 'closed'; return value }
const plan = (value = item(), patch: Partial<ModelControlPreview> = {}): ModelControlPreview => ({ item: value, entries: [{ key: 'a', value: 'A', state: 'to_close' }, { key: 'alias', value: 'A', state: 'to_close' }], groups: [{ groupId: 'g1', groupName: '隔离分组', key: 'a', count: 1, ok: true }], blockReasonKey: '', requestHealth: { checked: false, allowed: null, reasonKey: '' }, accountStatus: 'active', accountSchedulable: true, planFingerprint: 'fingerprint-1', ...patch })
const summary = (closedCount: number, patch: Partial<QuestionAnswerRecentSummaryItem> = {}): QuestionAnswerRecentSummaryItem => ({ targetId, recentQuestionAnswer: null, activeNewerBatch: false, modelControl: { closed: closedCount, attention: 1 }, ...patch })
const page = <T,>(items: T[], currentPage = 1, totalPages = 1) => ({ items, page: currentPage, totalPages })
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const wrappers: VueWrapper[] = [], coordinators: ReturnType<typeof useQuestionAnswerRecentSummaries>[] = []
const track = <T extends VueWrapper>(wrapper: T): T => { wrappers.push(wrapper); return wrapper }
const button = (wrapper: VueWrapper, text: string) => { const found = wrapper.findAll('button').find(value => value.text() === text); if (!found) throw new Error(`missing ${text}: ${wrapper.text()}`); return found }
const hasButton = (wrapper: VueWrapper, text: string) => wrapper.findAll('button').some(value => value.text() === text)
const row = (value = item()) => track(mount(QuestionAnswerModelControlRow, { props: { item: value, targetId: value.targetId, modelName: value.modelName, workspace: 'ws1' } }))
const drawer = () => track(mount(QuestionAnswerModelControlDrawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api' } }))
const openDrawer = async () => { const wrapper = drawer(); await wrapper.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises(); return wrapper }
const service = useConnectionHealth()
beforeEach(() => {
  vi.resetAllMocks(); modelControlBusyTargets.value = new Set(); service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('ws1')
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  api.getModelControlTarget.mockResolvedValue({ targetId, items: [item()], candidates: ['B'] })
  api.listModelControlItems.mockResolvedValue(page([item()])); api.listModelControlRules.mockResolvedValue({ items: [rule()] }); api.saveModelControlRule.mockResolvedValue(rule({ version: 2 })); api.deleteModelControlRule.mockResolvedValue(undefined)
  api.listModelControlEvents.mockResolvedValue(page([{ id: 'event1', targetId, modelName: 'A', eventType: 'managed_abandoned', createdAt: now, basis: {}, detail: { unconfirmedClose: { a: 'A' } } }]))
  api.getModelControlVerifyTargets.mockResolvedValue({ targetIds: [targetId] }); api.verifyModelControl.mockResolvedValue({ items: [item()], errors: [] }); api.previewModelControl.mockResolvedValue(plan()); api.executeModelControl.mockResolvedValue({ item: closed(), outcome: 'closed', entries: [{ key: 'a', value: 'A', state: 'closed' }] }); api.addModelControlManaged.mockResolvedValue(item({ modelName: 'B' })); api.removeModelControlManaged.mockResolvedValue(undefined)
  api.getConnectionHealthAdminGroups.mockResolvedValue(c2Groups())
  vi.stubGlobal('fetch', vi.fn(async () => json({ items: [summary(0)] })))
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); for (const coordinator of coordinators.splice(0)) coordinator.reset(); service.setAdminGroupsWorkspace(''); modelControlBusyTargets.value = new Set(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.innerHTML = '' })

describe('C3 shared row displays decisions and preserves protected actions', () => {
  it.each([['no_evidence', '无依据'], ['testing', '测试中'], ['awaiting_review', '待人工判题'], ['insufficient', '依据不足'], ['close_recommended', '建议关闭'], ['usable', '可用']] as const)('renders %s with actual evidence and expected availability', (decision, label) => {
    const value = item({ decision }); value.basis.decision = decision
    if (decision === 'testing') { value.control.closedEntries = { alias: 'A' }; value.control.observation.state = 'partially_closed'; value.previousRound = { ...value.round!, batchId: 'previous' } }
    const wrapper = row(value); expect(wrapper.text()).toContain(label)
    if (decision === 'testing') { expect(button(wrapper, '关闭此模型').attributes('disabled')).toBeDefined(); expect(button(wrapper, '恢复此模型').attributes('disabled')).toBeDefined(); expect(wrapper.text()).toContain('上一轮参考') }
    else expect(hasButton(wrapper, '关闭此模型')).toBe(decision === 'close_recommended')
    expect(api.previewModelControl).not.toHaveBeenCalled()
  })
  it('shows restore without close when already closed, both when partially closed, and only account action for the last model', () => {
    const shut = row(closed()); expect(hasButton(shut, '关闭此模型')).toBe(false); expect(hasButton(shut, '恢复此模型')).toBe(true)
    const partial = closed(); partial.control.observation.state = 'partially_closed'; const half = row(partial); expect(hasButton(half, '关闭此模型')).toBe(true); expect(hasButton(half, '恢复此模型')).toBe(true)
    const last = item(); last.control.observation.state = 'last_model'; const only = row(last); expect(hasButton(only, '关闭整个账号的调度')).toBe(true); expect(hasButton(only, '关闭此模型')).toBe(false); expect(hasButton(only, '恢复此模型')).toBe(false)
    last.control.observation.accountSchedulable = false; last.decision = 'usable'; const stopped = row(last); expect(stopped.text()).toContain('可以用调度开关重新打开'); expect(hasButton(stopped, '关闭整个账号的调度')).toBe(false)
    last.control.observation.accountSchedulable = null; const unknown = row(last); expect(hasButton(unknown, '关闭整个账号的调度')).toBe(false); expect(hasButton(unknown, '核对主站')).toBe(true)
  })
  it('adds an unmanaged candidate and reports a failed initial verification without removing it', async () => {
    api.addModelControlManaged.mockResolvedValue(item({ verifyErrorKey: errorKey('AccountReadFailed') }))
    const wrapper = track(mount(QuestionAnswerModelControlRow, { props: { targetId, modelName: 'A', workspace: 'ws1' } })); await button(wrapper, '纳入管理').trigger('click'); await flushPromises()
    expect(api.addModelControlManaged).toHaveBeenCalledWith(targetId, 'A', expect.any(AbortSignal)); expect(wrapper.text()).toContain('主站账号读取失败'); expect(hasButton(wrapper, '纳入管理')).toBe(false)
  })
  it('lists aliases and group counts before sending and reports partial attribution afterwards', async () => {
    api.executeModelControl.mockResolvedValue({ item: closed(), outcome: 'partial', entries: [{ key: 'a', value: 'A', state: 'closed' }, { key: 'alias', value: 'A', state: 'not_deleted' }] })
    const wrapper = row(); await button(wrapper, '关闭此模型').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="model-control-preview"]').text()).toContain('alias → A'); expect(wrapper.text()).toContain('隔离分组 · a · 剩余来源 1'); expect(api.executeModelControl).not.toHaveBeenCalled()
    await button(wrapper, '确认').trigger('click'); await flushPromises()
    expect(api.executeModelControl).toHaveBeenCalledWith(targetId, 'A', 'close', item().basis, 'fingerprint-1', false, expect.any(AbortSignal)); expect(wrapper.get('[data-testid="model-control-entry-results"]').text()).toContain('未删除'); expect(wrapper.text()).toContain('部分生效')
  })
  it('never confirms a preview without a fingerprint and explains the last-model account action', async () => {
    api.previewModelControl.mockResolvedValueOnce(plan(item(), { planFingerprint: null })); const wrapper = row(); await button(wrapper, '关闭此模型').trigger('click'); await flushPromises(); expect(hasButton(wrapper, '确认')).toBe(false); expect(api.executeModelControl).not.toHaveBeenCalled()
    const value = item(); value.control.observation.state = 'last_model'; api.previewModelControl.mockResolvedValue(plan(value)); const account = row(value); await button(account, '关闭整个账号的调度').trigger('click'); await flushPromises(); expect(account.text()).toContain('这是现有整号操作，C3 不会自动恢复'); await button(account, '确认').trigger('click'); await flushPromises(); expect(api.executeModelControl.mock.calls.at(-1)![2]).toBe('close_account')
  })
  it('retains persisted floor block after cancelling a preview and after rereading', async () => {
    const blocked = item(); blocked.control.lastAttempt = { operation: 'close', outcome: 'blocked', reasonKey: errorKey('FloorInsufficient'), entries: [], groups: [{ groupId: 'g1', groupName: '隔离分组', key: 'a', count: 0 }], at: now }
    api.previewModelControl.mockResolvedValue(plan(blocked, { blockReasonKey: errorKey('FloorInsufficient') }))
    const wrapper = row(); await button(wrapper, '关闭此模型').trigger('click'); await flushPromises(); expect(hasButton(wrapper, '确认')).toBe(false)
    await button(wrapper, '取消预览').trigger('click'); expect(wrapper.get('[data-testid="model-control-last-attempt"]').text()).toContain('保底不足')
    await wrapper.setProps({ item: blocked }); expect(wrapper.text()).toContain('隔离分组 · a · 剩余来源 0'); expect(api.executeModelControl).not.toHaveBeenCalled()
  })
  it('uses new plan on409, refuses its block, and retains it after modal closure', async () => {
    const blocked = item(); blocked.control.lastAttempt = { operation: 'close', outcome: 'blocked', reasonKey: errorKey('FloorInsufficient'), entries: [], groups: [], at: now }
    const changed = plan(blocked, { entries: [{ key: 'a2', value: 'A', state: 'to_close' }], blockReasonKey: errorKey('FloorInsufficient'), planFingerprint: 'fingerprint-2' })
    api.executeModelControl.mockRejectedValue(new ConnectionHealthApiError(errorKey('PlanChanged'), 409, changed))
    const wrapper = row(); await button(wrapper, '关闭此模型').trigger('click'); await flushPromises(); await button(wrapper, '确认').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="model-control-preview"]').text()).toContain('a2 → A'); expect(hasButton(wrapper, '确认')).toBe(false); await button(wrapper, '取消预览').trigger('click'); expect(wrapper.text()).toContain('保底不足'); expect(wrapper.emitted('updated')!.at(-1)![0]).toEqual(blocked)
  })
  it('closes preview on changed basis and refreshes to the authoritative decision', async () => {
    const changed = item({ decision: 'usable' }); api.executeModelControl.mockRejectedValue(new ConnectionHealthApiError(errorKey('BasisChanged'), 409, changed))
    const wrapper = row(); await button(wrapper, '关闭此模型').trigger('click'); await flushPromises(); await button(wrapper, '确认').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="model-control-preview"]').exists()).toBe(false); expect(wrapper.text()).toContain('依据已变化'); expect(wrapper.text()).toContain('可用'); expect(wrapper.emitted('refresh')).toHaveLength(1)
  })
  it('requires explicit evidence override on restore and displays an unschedulable account hint', async () => {
    const value = closed(); value.decision = 'insufficient'; api.previewModelControl.mockResolvedValue(plan(value, { entries: [{ key: 'a', value: 'A', state: 'to_restore' }] })); api.executeModelControl.mockResolvedValue({ item: item({ decision: 'usable' }), outcome: 'restored', hintKeys: [errorKey('AccountUnschedulable')] })
    const wrapper = row(value); await button(wrapper, '恢复此模型').trigger('click'); await flushPromises(); expect(button(wrapper, '确认').attributes('disabled')).toBeDefined(); await wrapper.get('[data-testid="model-control-preview"] input[type="checkbox"]').setValue(true); await button(wrapper, '确认').trigger('click'); await flushPromises()
    expect(api.executeModelControl.mock.calls[0]![5]).toBe(true); expect(wrapper.text()).toContain('模型要等调度打开后才会提供服务')
  })
  it.each(['close', 'restore'] as const)('disables an existing %s preview confirmation when authoritative evidence becomes testing, while retaining cancellation', async operation => {
    const original = operation === 'restore' ? closed() : item()
    if (operation === 'restore') { original.decision = 'usable'; original.basis.decision = 'usable' }
    api.previewModelControl.mockResolvedValue(plan(original, { entries: [{ key: 'alias', value: 'A', state: operation === 'restore' ? 'to_restore' : 'to_close' }] }))
    const wrapper = row(original); await button(wrapper, operation === 'restore' ? '恢复此模型' : '关闭此模型').trigger('click'); await flushPromises()
    expect(button(wrapper, '确认').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-testid="model-control-preview"] input[type="checkbox"]').exists()).toBe(false)
    const testing = structuredClone(original); testing.previousRound = testing.round; testing.round = { ...testing.round!, batchId: 'new-running', completedAt: null, running: true, correct: 0, incorrect: 0, unreviewed: 0, accuracyPercent: null }; testing.decision = 'testing'; testing.basis = { ...testing.basis, batchId: 'new-running', decision: 'testing' }
    expect(testing.control.accountPending).toBeNull(); expect(testing.control.pending).toBeNull()
    await wrapper.setProps({ item: testing }); await flushPromises()
    expect(wrapper.get('[data-testid="model-control-preview"]').text()).toContain('alias → A')
    const confirmation = button(wrapper, '确认'); expect.soft(confirmation.attributes('disabled')).toBeDefined()
    expect(button(wrapper, '取消预览').attributes('disabled')).toBeUndefined()
    await confirmation.trigger('click'); confirmation.element.dispatchEvent(new MouseEvent('click', { bubbles: true })); await flushPromises()
    expect(api.executeModelControl).not.toHaveBeenCalled()
    await button(wrapper, '取消预览').trigger('click'); expect(wrapper.find('[data-testid="model-control-preview"]').exists()).toBe(false)
    expect(api.executeModelControl).not.toHaveBeenCalled()
  })
  it('displays only verify for an unknown own operation, and disables conflicting restore', () => {
    const value = closed(); value.control.pending = { operation: 'close', phase: 'sending', receipt: '', state: 'unknown', startedAt: now, sendStartedAt: now }; value.control.accountPending = { modelName: 'A', operation: 'close', state: 'unknown' }
    const unknown = row(value); expect(hasButton(unknown, '恢复此模型')).toBe(false); expect(hasButton(unknown, '关闭此模型')).toBe(false); expect(button(unknown, '核对主站').attributes('disabled')).toBeUndefined(); expect(unknown.text()).toContain('结果未知')
    const conflict = closed(); conflict.control.conflictReason = errorKey('RestoreMappingEmpty'); const frozen = row(conflict); expect(button(frozen, '恢复此模型').attributes('disabled')).toBeDefined(); expect(frozen.text()).toContain('白名单已被清空')
  })
  it('protects all same-account rows while an operation is running and releases them afterwards', async () => {
    const wait = deferred<ModelControlPreview>(); api.previewModelControl.mockReturnValueOnce(wait.promise)
    const first = row(), second = row(item({ modelName: 'B' })); await button(first, '关闭此模型').trigger('click'); expect(button(second, '关闭此模型').attributes('disabled')).toBeDefined(); wait.resolve(plan()); await flushPromises(); expect(button(second, '关闭此模型').attributes('disabled')).toBeUndefined()
  })
  it('blocks ordinary removal for unconfirmed entries and explicitly lists them on abandonment', async () => {
    const value = item(); value.control.unconfirmedClose = { entries: { late: 'A' }, sentAt: now, expiresAt: '2026-10-10T12:00:00Z' }
    const wrapper = row(value); expect(button(wrapper, '移出管理').attributes('disabled')).toBeDefined(); expect(wrapper.text()).toContain('有未确认删除')
    await button(wrapper, '放弃管理').trigger('click'); expect(wrapper.get('[data-testid="model-control-abandon"]').text()).toContain('late → A'); await button(wrapper, '取消').trigger('click'); expect(api.removeModelControlManaged).not.toHaveBeenCalled(); expect(wrapper.find('[data-testid="model-control-row"]').exists()).toBe(true)
    await button(wrapper, '放弃管理').trigger('click'); await button(wrapper, '确认放弃管理').trigger('click'); await flushPromises(); expect(api.removeModelControlManaged).toHaveBeenCalledWith(targetId, 'A', 1, true, expect.any(AbortSignal)); expect(wrapper.emitted('removed')).toHaveLength(1)
  })
})

describe('C3 panel, rule form and drawer retain complete local lifecycle', () => {
  it('auto-verifies a closed target once per opening, retains last content after read failure and retries', async () => {
    api.getModelControlTarget.mockResolvedValue({ targetId, items: [closed()], candidates: [] }); api.verifyModelControl.mockResolvedValue({ items: [closed()], errors: [] })
    const wrapper = track(mount(QuestionAnswerModelControlPanel, { props: { targetId, refreshKey: 0 } })); await flushPromises(); expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
    await wrapper.setProps({ refreshKey: 1 }); await flushPromises(); expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
    api.getModelControlTarget.mockRejectedValueOnce(new Error(errorKey('Storage'))); await wrapper.setProps({ refreshKey: 2 }); await flushPromises(); expect(wrapper.text()).toContain('已关闭'); expect(wrapper.text()).toContain('重试')
    await button(wrapper, '重试').trigger('click'); await flushPromises(); expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })
  it('does not start an extra remote verification after a serving opening becomes closed', async () => {
    const wrapper = track(mount(QuestionAnswerModelControlPanel, { props: { targetId, refreshKey: 0 } })); await flushPromises(); expect(api.verifyModelControl).not.toHaveBeenCalled()
    api.getModelControlTarget.mockResolvedValue({ targetId, items: [closed()], candidates: [] }); await wrapper.setProps({ refreshKey: 1 }); await flushPromises(); expect(api.verifyModelControl).not.toHaveBeenCalled()
  })
  it('ignores a late panel response after switching account scope', async () => {
    const wait = deferred<{ targetId: string; items: ModelControlItem[]; candidates: string[] }>(); api.getModelControlTarget.mockReturnValueOnce(wait.promise)
    const wrapper = track(mount(QuestionAnswerModelControlPanel, { props: { targetId, refreshKey: 0 } })); const nextId = 'sub2api:ws2:b'; api.getModelControlTarget.mockResolvedValue({ targetId: nextId, items: [item({ targetId: nextId, modelName: 'New' })], candidates: [] }); await wrapper.setProps({ targetId: nextId }); await flushPromises(); wait.resolve({ targetId, items: [item({ accountName: '迟到旧账号' })], candidates: [] }); await flushPromises(); expect(wrapper.text()).toContain('New'); expect(wrapper.text()).not.toContain('迟到旧账号')
  })
  it('replaces rule draft with409 authority and validates both source switches before saving', async () => {
    const wrapper = track(mount(QuestionAnswerModelControlRuleForm, { props: { rule: rule(), workspace: 'ws1' } })); await wrapper.findAll('input[type="checkbox"]')[0]!.setValue(false); await wrapper.findAll('input[type="checkbox"]')[1]!.setValue(false); expect(button(wrapper, '保存规则').attributes('disabled')).toBeDefined(); await wrapper.findAll('input[type="checkbox"]')[1]!.setValue(true)
    api.saveModelControlRule.mockRejectedValueOnce(new ConnectionHealthApiError(errorKey('VersionConflict'), 409, rule({ minAccuracyPercent: 70, version: 3 })))
    await wrapper.get('form').trigger('submit'); await flushPromises(); expect((wrapper.get('[aria-label="可用线"]').element as HTMLInputElement).value).toBe('70'); expect(wrapper.text()).toContain('规则已被修改'); await wrapper.get('form').trigger('submit'); await flushPromises(); expect(api.saveModelControlRule.mock.calls.at(-1)![1]).toBe(3)
  })
  it('keeps account-level pending blocking B even when A is outside filtered list, then releases after refresh', async () => {
    const value = item({ modelName: 'B' }); value.control.accountPending = { modelName: 'A', operation: 'close', state: 'unknown' }; api.listModelControlItems.mockResolvedValue(page([value], 1, 2))
    const wrapper = await openDrawer(); await wrapper.get('[aria-label="模型筛选"]').setValue('B'); await wrapper.get('[aria-label="模型筛选"]').trigger('change'); await flushPromises(); expect(wrapper.text()).toContain('模型 A 结果未知'); expect(button(wrapper, '关闭此模型').attributes('disabled')).toBeDefined(); expect(button(wrapper, '移出管理').attributes('disabled')).toBeDefined(); expect(button(wrapper, '放弃管理').attributes('disabled')).toBeDefined(); expect(button(wrapper, '核对主站').attributes('disabled')).toBeUndefined()
    api.listModelControlItems.mockResolvedValue(page([item({ modelName: 'B' })])); await wrapper.get('[data-testid="model-control-tab-all"]').trigger('click'); await flushPromises(); expect(button(wrapper, '关闭此模型').attributes('disabled')).toBeUndefined()
  })
  it('provides inline actions and read-only basis viewing for an absent account, and events include abandon entries', async () => {
    const wrapper = await openDrawer(); expect(button(wrapper, '打开账号问答').attributes('disabled')).toBeDefined(); expect(button(wrapper, '关闭此模型').attributes('disabled')).toBeUndefined(); await button(wrapper, '查看依据批次').trigger('click'); expect(wrapper.emitted('batch-view')![0]![0]).toMatchObject({ targetId, batchId: 'batch-a', accountName: '隔离账号' })
    await wrapper.get('[data-testid="model-control-tab-events"]').trigger('click'); await flushPromises(); expect(wrapper.get('[data-testid="model-control-event"]').text()).toContain('unconfirmedClose'); expect(wrapper.text()).toContain('"a": "A"')
  })
  it('persists edited rules after closing and reopening, and paginates events', async () => {
    const wrapper = await openDrawer(); await wrapper.get('[data-testid="model-control-tab-rules"]').trigger('click'); await flushPromises(); await button(wrapper, '修改').trigger('click'); await wrapper.get('[aria-label="可用线"]').setValue('65'); api.saveModelControlRule.mockResolvedValue(rule({ minAccuracyPercent: 65, version: 2 })); api.listModelControlRules.mockResolvedValue({ items: [rule({ minAccuracyPercent: 65, version: 2 })] }); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('[aria-label="关闭模型管理"]').trigger('click'); await wrapper.get('[data-testid="model-control-open"]').trigger('click'); await wrapper.get('[data-testid="model-control-tab-rules"]').trigger('click'); await flushPromises(); expect(wrapper.text()).toContain('65%')
    api.listModelControlEvents.mockResolvedValue(page([], 1, 2)); await wrapper.get('[data-testid="model-control-tab-events"]').trigger('click'); await flushPromises(); await button(wrapper, '下一页').trigger('click'); await flushPromises(); expect(api.listModelControlEvents.mock.calls.at(-1)![2]).toBe(2)
  })
  it('polls local views every30 seconds only while visible and ignores an old workspace response', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] }); const wrapper = await openDrawer(); await vi.advanceTimersByTimeAsync(30000); await flushPromises(); expect(api.listModelControlItems).toHaveBeenCalledTimes(2); expect(api.verifyModelControl).not.toHaveBeenCalled()
    const stale = deferred<ReturnType<typeof page<ModelControlItem>>>(); api.listModelControlItems.mockReturnValueOnce(stale.promise); await vi.advanceTimersByTimeAsync(30000); await wrapper.setProps({ workspace: 'ws2' }); stale.resolve(page([item({ accountName: '迟到旧账号' })])); await flushPromises(); expect(wrapper.find('[data-testid="model-control-drawer"]').exists()).toBe(false); api.listModelControlItems.mockResolvedValue(page([])); await wrapper.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises(); expect(wrapper.text()).not.toContain('迟到旧账号')
  })
  it('verifies full deduplicated fixed inventory in50 batches and retries busy or failed accounts', async () => {
    const ids = Array.from({ length: 101 }, (_, index) => `sub2api:ws1:${String(index).padStart(3, '0')}`); api.getModelControlVerifyTargets.mockResolvedValue({ targetIds: [...ids.toReversed(), ids[0]!] }); api.verifyModelControl.mockImplementation(async (chunk: string[]) => ({ items: [], errors: chunk.includes(ids[0]!) ? [{ targetId: ids[0]!, reasonKey: errorKey('Processing') }] : [] }))
    const wrapper = await openDrawer(); await wrapper.get('[aria-label="模型筛选"]').setValue('B'); await button(wrapper, '核对全部').trigger('click'); await flushPromises(); expect(api.verifyModelControl.mock.calls.map(call => call[0].length)).toEqual([50, 50, 1]); expect(api.verifyModelControl.mock.calls[0]![0]).toEqual(ids.slice(0, 50)); expect(wrapper.text()).toContain('已核对 101 / 101'); expect(wrapper.text()).toContain('正在处理')
    api.verifyModelControl.mockResolvedValue({ items: [], errors: [] }); await button(wrapper, '重试未完成账号').trigger('click'); await flushPromises(); expect(api.verifyModelControl.mock.calls.at(-1)![0]).toEqual([ids[0]]); expect(hasButton(wrapper, '重试未完成账号')).toBe(false)
  })
  it('locks same-account row writes while verify-all is processing its chunk', async () => {
    const wait = deferred<{ items: ModelControlItem[]; errors: [] }>(); api.verifyModelControl.mockReturnValueOnce(wait.promise)
    const wrapper = await openDrawer(); await button(wrapper, '核对全部').trigger('click'); await flushPromises(); expect(button(wrapper, '关闭此模型').attributes('disabled')).toBeDefined(); wait.resolve({ items: [item()], errors: [] }); await flushPromises(); expect(button(wrapper, '关闭此模型').attributes('disabled')).toBeUndefined()
  })
  it('never starts verification after list failure and stops remaining batches when closed', async () => {
    api.getModelControlVerifyTargets.mockRejectedValueOnce(new Error(errorKey('Storage'))); const wrapper = await openDrawer(); await button(wrapper, '核对全部').trigger('click'); await flushPromises(); expect(api.verifyModelControl).not.toHaveBeenCalled(); expect(wrapper.text()).toContain('模型管理存储失败')
    api.getModelControlVerifyTargets.mockResolvedValue({ targetIds: Array.from({ length: 51 }, (_, index) => `sub2api:ws1:${index}`) }); const wait = deferred<{ items: ModelControlItem[]; errors: [] }>(); api.verifyModelControl.mockReturnValueOnce(wait.promise); await button(wrapper, '核对全部').trigger('click'); await flushPromises(); await wrapper.get('[aria-label="关闭模型管理"]').trigger('click'); wait.resolve({ items: [], errors: [] }); await flushPromises(); expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
  })
})

describe('C3 account summaries preserve independent success values and failures', () => {
  const mountedSummary = (failed: string[] = []) => track(mount(defineComponent({ setup: () => () => h(AdminGroupHealthDetail, { group: service.adminGroups.value[0]!, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false, modelControlFailures: failed }) })))
  it('displays summary click, hides null, shows group failure and preserves recent accuracy', async () => {
    const groups = c2Groups(); groups[0]!.accounts[0]!.modelControl = { closed: 1, attention: 2 }; groups[0]!.accounts[0]!.recentQuestionAnswer = { batchId: 'recent', source: 'manual', scheduleName: null, createdAt: now, completedAt: now, partial: false, requests: c2Stats(3, 1).requests, reviews: c2Stats(3, 1).reviews }; groups[0]!.modelControlError = errorKey('Storage'); service.adminGroups.value = groups
    const wrapper = mountedSummary(); expect(wrapper.text()).toContain('75%'); expect(wrapper.text()).toContain('模型管理摘要读取失败'); await wrapper.get('[data-testid="model-control-summary"]').trigger('click'); expect(wrapper.findComponent(AdminGroupHealthDetail).emitted('question-answer-view')![0]![0]).toEqual({ targetId })
    service.adminGroups.value = groups.map(group => ({ ...group, accounts: group.accounts.map(account => ({ ...account, modelControl: null })) })); await nextTick(); expect(wrapper.find('[data-testid="model-control-summary"]').exists()).toBe(false); expect(wrapper.text()).toContain('75%')
  })
  it('retains last model success after C3-only failure and clears failure on a later success', async () => {
    await service.loadAdminGroups(); const coordinator = useQuestionAnswerRecentSummaries({ workspace: () => 'ws1', visible: () => true, apply: service.applyQuestionAnswerRecentSummary }); coordinators.push(coordinator)
    const wrapper = track(mount(defineComponent({ setup: () => () => h(AdminGroupHealthDetail, { group: service.adminGroups.value[0]!, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false, modelControlFailures: coordinator.modelControlFailures.value }) })))
    vi.mocked(fetch).mockResolvedValueOnce(json({ items: [summary(1)] })); coordinator.refreshTargets([targetId]); await flushPromises(); expect(wrapper.text()).toContain('模型：已关闭 1')
    const recent = { batchId: 'newest', source: 'manual', scheduleName: null, createdAt: now, completedAt: now, partial: false, requests: c2Stats(1, 3).requests, reviews: c2Stats(1, 3).reviews }
    vi.mocked(fetch).mockResolvedValueOnce(json({ modelControlError: errorKey('Storage'), items: [summary(0, { modelControl: null, recentQuestionAnswer: recent })] })); coordinator.refreshTargets([targetId]); await flushPromises(); expect(wrapper.text()).toContain('模型：已关闭 1'); expect(wrapper.text()).toContain('25%'); expect(wrapper.text()).toContain('模型管理摘要读取失败')
    vi.mocked(fetch).mockResolvedValueOnce(json({ items: [summary(0)] })); coordinator.retry(targetId); await flushPromises(); expect(wrapper.text()).toContain('模型：已关闭 0'); expect(wrapper.text()).not.toContain('模型管理摘要读取失败')
  })
  it.each(['get', 'automatic'])('keeps model success through a failed recent read and late old %s response', async kind => {
    await service.loadAdminGroups(); const stale = c2Groups(); stale[0]!.accounts[0]!.modelControl = { closed: 0, attention: 0 }; const wait = deferred<any>()
    if (kind === 'get') api.getConnectionHealthAdminGroups.mockReturnValueOnce(wait.promise); else api.refreshConnectionHealthAdminGroupsAutomatically.mockReturnValueOnce(wait.promise)
    const read = kind === 'get' ? service.loadAdminGroups({ silent: true }) : service.refreshAdminGroupsAutomatically(); service.applyQuestionAnswerRecentSummary(summary(1)); service.applyQuestionAnswerRecentSummary(summary(0, { modelControl: null }), { modelControlFailed: true }); wait.resolve(kind === 'get' ? stale : { status: 'success', groups: stale, refresh: { state: 'success', sites: [] } }); await read; expect(service.adminGroups.value[0]!.accounts[0]!.modelControl?.closed).toBe(1)
  })
  it('clears model cache and failure state when switching workspace A toB and back toA', async () => {
    await service.loadAdminGroups(); service.applyQuestionAnswerRecentSummary(summary(1)); let workspace = 'ws1'; const coordinator = useQuestionAnswerRecentSummaries({ workspace: () => workspace, visible: () => true, apply: service.applyQuestionAnswerRecentSummary }); coordinators.push(coordinator)
    vi.mocked(fetch).mockResolvedValueOnce(json({ items: [summary(0, { modelControl: null })], modelControlError: errorKey('Storage') })); coordinator.refreshTargets([targetId]); await flushPromises(); expect(coordinator.modelControlFailures.value).toEqual([targetId])
    workspace = 'ws2'; service.setAdminGroupsWorkspace(workspace); coordinator.reset(); workspace = 'ws1'; service.setAdminGroupsWorkspace(workspace); coordinator.reset(); const fresh = c2Groups(); fresh[0]!.accounts[0]!.modelControl = { closed: 0, attention: 0 }; api.getConnectionHealthAdminGroups.mockResolvedValue(fresh); await service.loadAdminGroups(); expect(service.adminGroups.value[0]!.accounts[0]!.modelControl?.closed).toBe(0); expect(coordinator.modelControlFailures.value).toEqual([])
  })
  it('sends only rule contract fields when authority includes timestamps', async () => {
    const original = await vi.importActual<typeof import('@/modules/admin/api/connectionHealth')>('@/modules/admin/api/connectionHealth')
    vi.mocked(fetch).mockResolvedValueOnce(json(rule({ version: 2 }))); await original.saveModelControlRule({ ...rule(), createdAt: now, updatedAt: now } as ModelControlRule, 1)
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[0]![1]!.body))).toEqual({ modelName: 'A', minAccuracyPercent: 50, minJudgedAnswers: 3, includeManual: true, includeScheduled: true, expectedVersion: 1 })
  })
  it('rejects malformed model summary counts while accepting explicit null and optional failure', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ items: [summary(-1)] })); await expect(getQuestionAnswerRecentSummaries([targetId])).rejects.toThrow()
    vi.mocked(fetch).mockResolvedValueOnce(json({ items: [summary(0, { modelControl: null })], modelControlError: errorKey('Storage') })); await expect(getQuestionAnswerRecentSummaries([targetId])).resolves.toMatchObject({ modelControlError: errorKey('Storage'), items: [{ modelControl: null }] })
  })
  it('shows only current batch and today statistics, with no lifetime column', () => {
    const wrapper = track(mount(QuestionAnswerStatsBar, { props: { reviewStats: c2Stats(1, 2), todayStats: c2Stats(3, 1) } })); expect(wrapper.find('[data-testid="question-answer-stats-lifetime"]').exists()).toBe(false); expect(wrapper.text()).not.toContain('累计'); expect(wrapper.get('[data-testid="question-answer-periods"]').classes()).toContain('md:grid-cols-2')
  })
})
