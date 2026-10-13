// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Header from '@/modules/admin/components/dashboard/QuestionAnswerModelControlHeader.vue'
import AutoForm from '@/modules/admin/components/dashboard/QuestionAnswerModelControlAutoForm.vue'
import Row from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import Panel from '@/modules/admin/components/dashboard/QuestionAnswerModelControlPanel.vue'
import Drawer from '@/modules/admin/components/dashboard/QuestionAnswerModelControlDrawer.vue'
import { ConnectionHealthApiError } from '@/modules/admin/api/connectionHealth'
import { modelControlRuleChanged, modelControlBusyTargets } from '@/modules/admin/utils/questionAnswerModelControl'
import type { ModelControlAutoSettings, ModelControlEvent } from '@/modules/admin/types/connectionHealth'
import { managed, now, targetId } from './fixtures/v2911ModelControl'
const api = vi.hoisted(() => Object.fromEntries(['getModelControlSettings', 'getModelControlAutoSettings', 'saveModelControlAutoSettings', 'getModelControlTarget', 'listModelControlItems', 'listModelControlEvents', 'getModelControlVerifyTargets', 'verifyModelControl'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const defaults = (patch: Partial<ModelControlAutoSettings> = {}): ModelControlAutoSettings => ({ autoCloseEnabled: false, autoCloseEnabledAt: null, autoRestoreEnabled: false, autoRestoreEnabledAt: null, healthCoordinationEnabled: false, verifyIntervalMinutes: 10, retryIntervalMinutes: 5, version: 0, lastScanStartedAt: null, lastScanFinishedAt: null, lastScanErrorKey: '', ...patch })
const counts = { total: 1, open: 1, closed: 0, attention: 1, untested: 0 }
const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(w: T): T => { wrappers.push(w); return w }
const button = (w: VueWrapper, text: string) => { const b = w.findAll('button').find(b => b.text() === text); if (!b) throw new Error(`missing ${text}: ${w.text()}`); return b }
const header = () => track(mount(Header, { props: { scope: 'account', workspace: 'ws1', counts, refreshKey: 1 } }))
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
beforeEach(() => {
 vi.resetAllMocks(); modelControlBusyTargets.value = new Set()
 Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
 api.getModelControlSettings.mockResolvedValue({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 1 })
 api.getModelControlAutoSettings.mockResolvedValue(defaults()); api.saveModelControlAutoSettings.mockImplementation(async value => ({ ...value, version: value.version + 1 }))
 api.getModelControlTarget.mockResolvedValue({ targetId, items: [managed()], candidates: [] })
 api.listModelControlItems.mockResolvedValue({ items: [managed()], page: 1, totalPages: 1, counts }); api.listModelControlEvents.mockResolvedValue({ items: [], page: 1, totalPages: 1 })
 api.getModelControlVerifyTargets.mockResolvedValue({ targetIds: [] }); api.verifyModelControl.mockResolvedValue({ items: [managed()], errors: [] })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); modelControlBusyTargets.value = new Set(); document.body.innerHTML = '' })
describe('C3.2 header and auto settings', () => {
 it('adds only Settings while all switches are off, and explains every field permanently before enabling', async () => {
  const w = header(); await flushPromises()
  expect(button(w, '手动').exists()).toBe(true); expect(button(w, '设置').exists()).toBe(true)
  expect(w.find('[data-testid="model-control-auto-last-scan"]').exists()).toBe(false); expect(w.find('[data-testid="model-control-auto-form"]').exists()).toBe(false)
  expect(w.findAll('span').some(s => ['自动关闭', '自动恢复', '健康配合'].includes(s.text()))).toBe(false)
  await button(w, '设置').trigger('click')
  const form = w.get('[data-testid="model-control-auto-form"]')
  expect(form.get('p').text()).toBe('现在三个开关都关着，系统不会自动做任何事。')
  expect(form.findAll('p.break-words.text-muted-foreground').map(p => p.text())).toEqual([
   '打开后：今天新测完的一轮正确率低于可用线时，系统自动把这个模型从主站白名单关掉，不用你点。会改主站。只处理打开之后测完或改判的那一轮，之前已显示“建议关闭”的仍要你手动点。账号只剩这一个模型时不会自动关。',
   '打开后：被 TransitHub 关掉的模型（包括你手动点关闭的），今天新测完的一轮合格时，系统自动重新开放。会改主站。白名单里本来没有的模型不会自动加，仍要你点“开放此模型”。换一天不会自动恢复，要当天测过才算。',
   '打开后：① 分组里某个已关闭的模型只剩一个账号还开着时，这个账号不会被健康检查停用，你手动停用、关调度、删除它也会被拦下并说明原因；② 已关闭的模型探活失败，不再让整个账号被停用或降 Priority。不打开时，健康检查和现在完全一样。不改白名单。',
   '任一开关打开时，系统每隔这么久读一次主站，看已关闭的模型有没有被人在主站改回。只读主站，不改主站。可填 5–60。',
   '主站读不到、分组里没有别的账号可用等情况，会按这个间隔重试；其他失败可能需要今天的新一轮测试或手动处理，请以卡片提示为准。可填 1–60。',
  ])
  expect(form.findAll('input[type="checkbox"]')).toHaveLength(3); expect(form.text()).not.toContain('保存后，系统会在没有人确认的情况下改主站白名单。')
  await form.get('[aria-label="健康配合"]').setValue(true); expect(form.text()).toContain('已打开的功能会在后台运行，不需要你点确认。'); expect(form.text()).not.toContain('保存后，系统会在没有人确认')
  await form.get('[aria-label="自动关闭"]').setValue(true); expect(form.text()).toContain('保存后，系统会在没有人确认的情况下改主站白名单。'); expect(form.text()).not.toContain('每测一轮可能开关一次')
  await form.get('[aria-label="自动恢复"]').setValue(true); expect(form.text()).toContain('正确率在可用线附近的模型，每测一轮可能开关一次。可以提高‘每题最少已判回答（3—10）’来减少。'); expect(form.text()).not.toContain('每轮最少已判答案'); expect(api.saveModelControlAutoSettings).not.toHaveBeenCalled()
  await form.trigger('submit'); await flushPromises(); expect(api.saveModelControlAutoSettings.mock.calls[0]!.slice(0, 2)).toEqual([defaults({ autoCloseEnabled: true, autoRestoreEnabled: true, healthCoordinationEnabled: true }), 0]); expect(w.find('form').exists()).toBe(false)
 })
 it.each([
  [{}, '手动', '手动：系统根据今天的测试给出建议，关闭和开放都要你点按钮确认。点“设置”可以打开自动处理。'],
  [{}, '设置', '设置自动处理和健康配合。全部默认关闭。'],
  [{ autoCloseEnabled: true }, '自动关闭', '自动关闭已打开：今天新测完的一轮不合格时，系统自动关闭这个模型。'],
  [{ autoRestoreEnabled: true }, '自动恢复', '自动恢复已打开：已关闭的模型今天新测完的一轮合格时，系统自动重新开放。'],
  [{ healthCoordinationEnabled: true }, '健康配合', '健康配合已打开：已关闭的模型只剩一个账号开着时保留这个账号；已关闭的模型探活失败不再连累整个账号。'],
 ] as const)('explains the %s %s tag through the actual tooltip', async (patch, label, text) => {
  vi.useFakeTimers(); api.getModelControlAutoSettings.mockResolvedValue(defaults(patch)); const w = header(); await flushPromises()
  const trigger = w.findAll('div.relative.inline-flex').find(div => div.text() === label); if (!trigger) throw new Error(`missing tooltip ${label}`)
  await trigger.trigger('mouseenter'); await vi.advanceTimersByTimeAsync(150); await flushPromises(); expect(document.querySelector('[role="tooltip"]')?.textContent?.trim()).toBe(text); await trigger.trigger('mouseleave')
 })
 it.each([['定时读取主站：每', 4], ['定时读取主站：每', 61], ['定时读取主站：每', 5.5], ['失败后重试：每', 0], ['失败后重试：每', 61], ['失败后重试：每', 1.5]])('rejects %s=%s without saving', async (label, value) => {
  const w = track(mount(AutoForm, { props: { settings: defaults(), workspace: 'ws1' } })); await w.get(`[aria-label="${label}"]`).setValue(value)
  expect(w.text()).toContain('请填写范围内的整数'); expect(button(w, '保存').attributes('disabled')).toBeDefined(); await w.get('form').trigger('submit'); expect(api.saveModelControlAutoSettings).not.toHaveBeenCalled()
 })
 it('uses current values after 409 and preserves canceled editing', async () => {
  const w = header(); await flushPromises(); await button(w, '设置').trigger('click')
  api.saveModelControlAutoSettings.mockRejectedValueOnce(new ConnectionHealthApiError('admin.connectionHealth.errors.modelControlVersionConflict', 409, defaults({ autoRestoreEnabled: true, version: 3, verifyIntervalMinutes: 60 })))
  await w.get('form').trigger('submit'); await flushPromises(); expect(w.text()).toContain('设置已被别处修改，已显示最新值。'); expect((w.get('[aria-label="自动恢复"]').element as HTMLInputElement).checked).toBe(true)
  await w.get('form').trigger('submit'); await flushPromises(); expect(api.saveModelControlAutoSettings.mock.calls.at(-1)![1]).toBe(3)
  await button(w, '设置').trigger('click'); await w.get('[aria-label="自动关闭"]').setValue(true); await button(w, '取消').trigger('click'); expect(api.saveModelControlAutoSettings).toHaveBeenCalledTimes(2)
  await button(w, '设置').trigger('click'); expect((w.get('[aria-label="自动关闭"]').element as HTMLInputElement).checked).toBe(false)
 })
 it('shows auto-read failure independently and retries only its read, hiding Settings', async () => {
  api.getModelControlAutoSettings.mockRejectedValueOnce(new Error('fixture')); const w = header(); await flushPromises()
  expect(w.text()).toContain('自动设置读取失败'); expect(w.find('[data-testid="model-control-auto-open"]').exists()).toBe(false); expect(button(w, '修改').exists()).toBe(true); expect(w.text()).toContain('受管')
  await button(w, '重试').trigger('click'); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(2); expect(api.getModelControlSettings).toHaveBeenCalledTimes(1); expect(button(w, '设置').exists()).toBe(true)
 })
 it.each([{ autoCloseEnabled: true }, { autoRestoreEnabled: true }, { healthCoordinationEnabled: true }, { autoCloseEnabled: true, autoRestoreEnabled: true, healthCoordinationEnabled: true }])('shows actual enabled tags %j and scan result', async patch => {
  api.getModelControlAutoSettings.mockResolvedValue(defaults({ ...patch, lastScanFinishedAt: now, lastScanErrorKey: 'admin.connectionHealth.errors.modelControlAutoDisabled' })); const w = header(); await flushPromises()
  const pressed = w.findAll('button[aria-pressed="true"]').map(x => x.text()); expect(pressed.includes('手动')).toBe(!patch.autoCloseEnabled && !patch.autoRestoreEnabled)
  expect(w.get('[data-testid="model-control-auto-last-scan"]').text()).toContain('失败（自动设置刚被修改）')
  for (const [key, text] of [['autoCloseEnabled', '自动关闭'], ['autoRestoreEnabled', '自动恢复'], ['healthCoordinationEnabled', '健康配合']] as const) expect(w.findAll('span').some(x => x.text() === text)).toBe(Boolean(patch[key as keyof typeof patch]))
 })
 it('refreshes scan status for each read, keeps edited values, and aborts stale workspace responses', async () => {
  api.getModelControlAutoSettings.mockResolvedValue(defaults({ autoCloseEnabled: true })); const w = header(); await flushPromises(); expect(w.text()).toContain('还没有运行过')
  api.getModelControlAutoSettings.mockResolvedValue(defaults({ autoCloseEnabled: true, lastScanFinishedAt: now })); await w.setProps({ refreshKey: 2 }); await flushPromises(); expect(w.text()).toContain('正常')
  await button(w, '设置').trigger('click'); await w.get('[aria-label="自动恢复"]').setValue(true); const before = api.getModelControlAutoSettings.mock.calls.length; await w.setProps({ refreshKey: 3 }); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before); expect((w.get('[aria-label="自动恢复"]').element as HTMLInputElement).checked).toBe(true)
  await button(w, '取消').trigger('click'); const stale = deferred<ModelControlAutoSettings>(); api.getModelControlAutoSettings.mockReturnValueOnce(stale.promise); await w.setProps({ refreshKey: 4 }); const signal = api.getModelControlAutoSettings.mock.calls.at(-1)![0] as AbortSignal
  api.getModelControlAutoSettings.mockResolvedValue(defaults()); await w.setProps({ workspace: 'ws2' }); await flushPromises(); stale.resolve(defaults({ autoCloseEnabled: true })); await flushPromises(); expect(signal.aborted).toBe(true); expect(w.find('[data-testid="model-control-auto-last-scan"]').exists()).toBe(false)
 })
 it('updates header on panel Refresh, failed Retry, external refresh and rules notification', async () => {
  const w = track(mount(Panel, { props: { targetId, refreshKey: 1 } })); await flushPromises(); const before = api.getModelControlAutoSettings.mock.calls.length
  await button(w, '刷新').trigger('click'); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before + 1)
  api.getModelControlTarget.mockRejectedValueOnce(new Error('fixture')); await button(w, '刷新').trigger('click'); await flushPromises(); await button(w, '重试').trigger('click'); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before + 3)
  await w.setProps({ refreshKey: 2 }); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before + 4)
  modelControlRuleChanged('ws1'); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before + 5)
 })
 it('changes only the drawer explanation when automatic writes are enabled, then restores manual copy conservatively', async () => {
  const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api', refreshKey: 0 } })); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises(); expect(w.text()).toContain('每次都要你确认。')
  for (const [revision, patch] of [[1, { autoCloseEnabled: true }], [2, { autoRestoreEnabled: true }], [3, { healthCoordinationEnabled: true }], [4, {}]] as const) {
   api.getModelControlAutoSettings.mockResolvedValue(defaults(patch)); await w.setProps({ refreshKey: revision }); await flushPromises()
   const automatic = revision < 3
   expect(w.text().includes('已打开的自动功能在后台运行，其余操作仍由你确认。')).toBe(automatic); expect(w.text().includes('每次都要你确认。')).toBe(!automatic)
  }
  api.getModelControlAutoSettings.mockResolvedValue(defaults({ autoCloseEnabled: true })); await w.setProps({ refreshKey: 5 }); await flushPromises(); api.getModelControlAutoSettings.mockRejectedValueOnce(new Error('fixture')); await w.setProps({ refreshKey: 6 }); await flushPromises(); expect(w.text()).not.toContain('每次都要你确认。'); expect(w.text()).toContain('已打开的自动功能在后台运行')
 })
 it.each(['autoCloseEnabled', 'autoRestoreEnabled'] as const)('keeps previously loaded %s enabled scope and tag when refresh fails', async key => {
  api.getModelControlAutoSettings.mockResolvedValue(defaults({ [key]: true })); const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api', refreshKey: 0 } })); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises()
  api.getModelControlAutoSettings.mockRejectedValueOnce(new Error('fixture')); await w.setProps({ refreshKey: 1 }); await flushPromises()
  expect(w.text()).toContain('已打开的自动功能在后台运行，其余操作仍由你确认。'); expect(w.text()).not.toContain('每次都要你确认。'); expect(w.findAll('span').some(span => span.text() === (key === 'autoCloseEnabled' ? '自动关闭' : '自动恢复'))).toBe(true)
  expect(w.text()).toContain('自动设置读取失败'); expect(w.find('[data-testid="model-control-auto-open"]').exists()).toBe(false)
  api.getModelControlAutoSettings.mockRejectedValueOnce(new Error('fixture')); await w.setProps({ workspace: 'ws2' }); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises(); expect(w.text()).toContain('每次都要你确认。'); expect(w.text()).not.toContain('已打开的自动功能在后台运行'); expect(w.findAll('span').some(span => span.text() === (key === 'autoCloseEnabled' ? '自动关闭' : '自动恢复'))).toBe(false)
 })
 it('keeps the old manual scope when the initial auto-settings read fails without a loaded value', async () => {
  api.getModelControlAutoSettings.mockRejectedValueOnce(new Error('fixture')); const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api' } })); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises()
  expect(w.text()).toContain('每次都要你确认。'); expect(w.text()).not.toContain('已打开的自动功能在后台运行'); expect(w.text()).toContain('自动设置读取失败'); expect(w.find('[data-testid="model-control-auto-open"]').exists()).toBe(false)
 })
 it('aborts saving and ignores the result after switching workspace', async () => {
  const wait = deferred<ModelControlAutoSettings>(); api.saveModelControlAutoSettings.mockReturnValueOnce(wait.promise)
  const w = track(mount(AutoForm, { props: { settings: defaults(), workspace: 'ws1' } })); await w.get('form').trigger('submit'); const signal = api.saveModelControlAutoSettings.mock.calls[0]![2] as AbortSignal
  await w.setProps({ workspace: 'ws2' }); wait.resolve(defaults({ autoCloseEnabled: true, version: 2 })); await flushPromises(); expect(signal.aborted).toBe(true); expect(w.emitted('saved')).toBeUndefined(); expect((w.get('[aria-label="自动关闭"]').element as HTMLInputElement).checked).toBe(false)
 })
 it('uses existing drawer 30-second reads and stops them when hidden', async () => {
  vi.useFakeTimers(); const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api' } })); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises(); const before = api.getModelControlAutoSettings.mock.calls.length
  api.getModelControlAutoSettings.mockResolvedValue(defaults({ healthCoordinationEnabled: true, lastScanFinishedAt: now })); await vi.advanceTimersByTimeAsync(30000); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before + 1); expect(w.text()).toContain('最近一次自动检查：')
  await w.get('[aria-label="关闭模型管理"]').trigger('click'); await vi.advanceTimersByTimeAsync(30000); await flushPromises(); expect(api.getModelControlAutoSettings).toHaveBeenCalledTimes(before + 1)
 })
})
describe('C3.2 cards and events preserve manual controls', () => {
 it.each([false, true, undefined])('keeps the original probed warning only without health coordination: %s', coordination => {
  const value = managed(); value.healthCoordination = coordination; value.health = { recentlyProbed: true, state: 'healthy' }
  const w = track(mount(Row, { props: { item: value, targetId, modelName: 'M', workspace: 'ws1' } })); expect(button(w, '关闭此模型').exists()).toBe(true)
  expect(w.text().includes('如果它答不好，整个账号可能被判为异常。')).toBe(!coordination)
 })
 it.each([['eligible', '等待后台处理'], ['waiting', '等今天新的一轮测试'], ['retry', '上次没做成（主站上有人改过这个模型）'], ['busy', '上一次操作还没确认结果'], ['none', ''], ['done', ''], ['off', '']] as const)('renders %s appropriately', (state, sentence) => {
  const value = managed(); value.auto = { state, operation: 'close', nextAt: now, changedAt: now, reasonKey: 'admin.connectionHealth.errors.modelControlAutoManualChange' }
  const w = track(mount(Row, { props: { item: value, targetId, modelName: 'M', workspace: 'ws1' } })); expect(w.findAll('[data-testid="model-control-auto-status"]').length).toBe(sentence ? state === 'waiting' ? 2 : 1 : 0)
  if (sentence) expect(w.text()).toContain(sentence); if (state === 'waiting') expect(w.text()).toContain('这个模型的开关状态在'); expect(button(w, '关闭此模型').exists()).toBe(true)
 })
 it.each([['AutoLastModel', '要停请到账号行点“关闭主站调度”'], ['AutoNotIsolatable', '这个模型无法单独关闭'], ['AutoNotProvided', '主站白名单里没有这个模型'], ['AutoPartiallyClosed', '不会继续关'], ['AutoConflict', '白名单设置变了'], ['AutoAccountMissing', '账号已不在主站'], ['AutoUnconfirmedClose', '上一次关闭的结果还没确认']])('explains manual reason %s', (reason, text) => {
  const value = managed(); value.auto = { state: 'manual', operation: 'close', nextAt: null, changedAt: now, reasonKey: `admin.connectionHealth.errors.modelControl${reason}` }; const w = track(mount(Row, { props: { item: value, targetId, modelName: 'M', workspace: 'ws1' } })); expect(w.get('[data-testid="model-control-auto-status"]').text()).toContain(text)
 })
 it('uses retry fallback for unknown error, and shows removal warning only for closed+coordination', async () => {
  for (const closed of [false, true]) for (const coordination of [false, true]) {
   const value = managed(); value.healthCoordination = coordination; value.control.closedEntries = closed ? { alias: 'M' } : {}; value.auto = { state: 'retry', operation: 'close', nextAt: now, changedAt: now, reasonKey: 'unknown' }
   const w = track(mount(Row, { props: { item: value, targetId, modelName: 'M', workspace: 'ws1' } })); expect(w.text()).toContain('上次没做成（暂时没做成）'); if (!closed) value.control.unconfirmedClose = { entries: { late: 'M' }, sentAt: now, expiresAt: now }
   await w.setProps({ item: { ...value } }); await button(w, '不再管理').trigger('click'); expect(w.text().includes('它会重新计入整号判断')).toBe(closed && coordination)
  }
 })
 it('marks only empty-actor events automatic and explains all five saved settings', async () => {
  const events: ModelControlEvent[] = ['', 'human', undefined].map((actorUserId, n) => ({ id: String(n), targetId, modelName: 'M', createdAt: now, eventType: n === 0 ? 'auto_settings_saved' : 'close_succeeded', basis: {}, detail: { autoCloseEnabled: true, autoRestoreEnabled: false, healthCoordinationEnabled: true, verifyIntervalMinutes: 10, retryIntervalMinutes: 5 }, actorUserId }))
  api.listModelControlEvents.mockResolvedValue({ items: events, page: 1, totalPages: 1 }); const w = track(mount(Drawer, { props: { groups: [], workspace: 'ws1', platform: 'sub2api' } })); await w.get('[data-testid="model-control-open"]').trigger('click'); await flushPromises(); await w.get('[data-testid="model-control-tab-events"]').trigger('click'); await flushPromises()
  expect(w.findAll('[data-testid="model-control-event-auto"]')).toHaveLength(1); expect(w.text()).toContain('自动设置改为：自动关闭 开，自动恢复 关，健康配合 开，每 10 分钟读取主站，失败后每 5 分钟重试。'); expect(w.findAll('[data-testid="model-control-event"]')[1]!.text()).toContain('关闭成功')
 })
})
