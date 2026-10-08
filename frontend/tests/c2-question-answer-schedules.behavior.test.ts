// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { c2Execution, c2Groups, c2Limits, c2Preview, c2Schedule, c2Stats } from './fixtures/c2QuestionAnswerSchedules'
import QuestionAnswerScheduleDrawer from '@/modules/admin/components/dashboard/QuestionAnswerScheduleDrawer.vue'
import QuestionAnswerScheduleForm from '@/modules/admin/components/dashboard/QuestionAnswerScheduleForm.vue'
import QuestionAnswerScheduleProtection from '@/modules/admin/components/dashboard/QuestionAnswerScheduleProtection.vue'
import QuestionAnswerScheduleExecutionDetail from '@/modules/admin/components/dashboard/QuestionAnswerScheduleExecutionDetail.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import QuestionAnswerStatsBar from '@/modules/admin/components/dashboard/QuestionAnswerStatsBar.vue'
import { ConnectionHealthApiError } from '@/modules/admin/api/connectionHealth'
import { questionAnswerScheduleSlots, validQuestionAnswerScheduleGrid } from '@/modules/admin/utils/questionAnswerSchedules'
import type { QuestionAnswerScheduleExecutionDetail as Execution, QuestionAnswerSchedulePreview as Preview } from '@/modules/admin/types/connectionHealth'

const api = vi.hoisted(() => Object.fromEntries(['listTestQuestions', 'previewQuestionAnswerSchedule', 'getQuestionAnswerSchedule', 'createQuestionAnswerSchedule', 'updateQuestionAnswerSchedule', 'listQuestionAnswerSchedules', 'getQuestionAnswerScheduleLimits', 'getQuestionAnswerRuntimeSettings', 'saveQuestionAnswerRuntimeSettings', 'saveQuestionAnswerScheduleLimits', 'runQuestionAnswerSchedule', 'setQuestionAnswerScheduleState', 'deleteQuestionAnswerSchedule', 'listQuestionAnswerScheduleExecutions', 'getQuestionAnswerScheduleExecution', 'cancelQuestionAnswerScheduleExecution', 'getQuestionAnswerBatch', 'getLatestQuestionAnswerBatch', 'discoverManualProbeModels'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async (importOriginal) => ({ ...await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const wrappers: VueWrapper[] = []
const preferences = { modelIds: ['m1'], questionIds: ['q1'], reasoningEffort: 'medium' as const, repeatCount: 1 }
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const button = (wrapper: VueWrapper, text: string) => { const found = wrapper.findAll('button').find(candidate => candidate.text() === text); if (!found) throw new Error(`missing button ${text}: ${wrapper.text()}`); return found }
const track = <T extends VueWrapper>(wrapper: T): T => { wrappers.push(wrapper); return wrapper }
const form = (initial = null as ReturnType<typeof c2Schedule> | null) => track(mount(QuestionAnswerScheduleForm, { props: { initial, groups: c2Groups(), workspace: 'ws1', preferences, limits: c2Limits() } }))
const detail = () => track(mount(QuestionAnswerScheduleExecutionDetail, { props: { executionId: 'execution1', workspace: 'ws1', scheduleName: '核心定时计划' } }))
const drawer = () => track(mount(QuestionAnswerScheduleDrawer, { props: { groups: c2Groups(), workspace: 'ws1', platform: 'sub2api', preferences } }))
const page = <T,>(items: T[]) => ({ items, page: 1, pageSize: 20, total: items.length, totalPages: items.length ? 1 : 0 })

beforeEach(() => {
  vi.resetAllMocks()
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  api.listTestQuestions.mockResolvedValue(c2Execution().configSnapshot.resolved!.questions)
  api.previewQuestionAnswerSchedule.mockResolvedValue(c2Preview())
  api.getQuestionAnswerSchedule.mockResolvedValue(c2Schedule())
  api.createQuestionAnswerSchedule.mockResolvedValue(c2Schedule())
  api.updateQuestionAnswerSchedule.mockResolvedValue(c2Schedule({ version: 2 }))
  api.listQuestionAnswerSchedules.mockResolvedValue(page([c2Schedule()]))
  api.getQuestionAnswerScheduleLimits.mockResolvedValue(c2Limits())
  api.getQuestionAnswerRuntimeSettings.mockResolvedValue({ questionAnswerConcurrency: 15, version: 3, updatedAt: null })
  api.saveQuestionAnswerRuntimeSettings.mockResolvedValue({ questionAnswerConcurrency: 2, version: 4, updatedAt: null })
  api.saveQuestionAnswerScheduleLimits.mockResolvedValue({ ...c2Limits(), version: 2 })
  api.getQuestionAnswerScheduleExecution.mockResolvedValue(c2Execution())
  api.listQuestionAnswerScheduleExecutions.mockResolvedValue(page([c2Execution()]))
  api.setQuestionAnswerScheduleState.mockResolvedValue(c2Schedule({ enabled: false, version: 2 }))
  api.deleteQuestionAnswerSchedule.mockResolvedValue(c2Schedule({ enabled: false, version: 2, deletedAt: '2026-10-08T00:05:00Z' }))
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.useRealTimers(); vi.restoreAllMocks(); document.body.innerHTML = '' })

describe('C2 plan form uses explicit inventory preview and saved versions', () => {
  it('loads local questions, preserves matrix, switches mutually exclusive scope, and creates without executing', async () => {
    const wrapper = form()
    await flushPromises()
    expect(api.previewQuestionAnswerSchedule).toHaveBeenCalledTimes(1)
    expect(api.previewQuestionAnswerSchedule.mock.calls[0]![0]).toEqual({ targetMode: 'groups', selectedGroupIds: ['g1'], selectedAccountTargetIds: [], models: ['m1'], questionIds: ['q1'], reasoningEffort: 'medium', repeatCount: 1 })
    expect(api.discoverManualProbeModels).not.toHaveBeenCalled()
    await wrapper.get('[aria-label="计划名称"]').setValue('隔离计划')
    await wrapper.findAll('input[type="radio"]')[1]!.setValue()
    expect(button(wrapper, '保存计划').attributes('disabled')).toBeDefined()
    const accountLabel = wrapper.findAll('label').find(label => label.text().includes('账号A'))!
    await accountLabel.get('input').setValue(true)
    await button(wrapper, '刷新当前预计').trigger('click'); await flushPromises()
    const previewInput = api.previewQuestionAnswerSchedule.mock.calls.at(-1)![0]
    expect(previewInput.selectedGroupIds).toEqual([])
    expect(previewInput.selectedAccountTargetIds).toEqual(['sub2api:ws1:a'])
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.createQuestionAnswerSchedule).toHaveBeenCalledWith(expect.objectContaining({ name: '隔离计划', enabled: true, targetMode: 'accounts', selectedGroupIds: [], selectedAccountTargetIds: ['sub2api:ws1:a'] }), expect.any(AbortSignal))
    expect(api.runQuestionAnswerSchedule).not.toHaveBeenCalled()
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('retains saved unavailable values and disabled state, prevents unsaved run, and PUT excludes enabled', async () => {
    const initial = c2Schedule({ enabled: false, models: ['saved-only'], questionIds: ['q-deleted'], selectedGroupIds: ['g-deleted'] })
    const wrapper = form(initial); await flushPromises()
    expect(wrapper.text()).toContain('saved-only')
    expect(wrapper.text()).toContain('g-deleted · 保存的分组')
    expect(wrapper.text()).toContain('q-deleted · 保存的题目已失效')
    expect(wrapper.find('input[type="checkbox"][aria-label="保存后启用"]').exists()).toBe(false)
    await wrapper.get('[aria-label="计划名称"]').setValue('保存名称')
    expect(button(wrapper, '立即执行').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    const [id, input, version] = api.updateQuestionAnswerSchedule.mock.calls[0]!
    expect(id).toBe(initial.id); expect(version).toBe(1)
    expect(input).toMatchObject({ models: ['saved-only'], questionIds: ['q-deleted'], selectedGroupIds: ['g-deleted'] })
    expect(input).not.toHaveProperty('enabled')
    expect(api.runQuestionAnswerSchedule).not.toHaveBeenCalled()
  })

  it('invalidates edited previews and ignores late responses after workspace change', async () => {
    const pending = deferred<Preview>()
    api.previewQuestionAnswerSchedule.mockReturnValueOnce(pending.promise)
    const wrapper = form(); await flushPromises()
    const oldSignal = api.previewQuestionAnswerSchedule.mock.calls[0]![1] as AbortSignal
    await wrapper.setProps({ workspace: 'ws2', groups: [] }); await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    pending.resolve({ ...c2Preview(), estimatedTargetCount: 199 }); await flushPromises()
    expect(wrapper.text()).not.toContain('当前预计 199')
    expect(button(wrapper, '保存计划').attributes('disabled')).toBeDefined()
  })

  it('keeps draft on conflict, explicitly reloads authority, then saves the new version', async () => {
    const authoritative = c2Schedule({ name: '服务端名称', version: 5 })
    api.updateQuestionAnswerSchedule.mockRejectedValueOnce(new ConnectionHealthApiError('admin.connectionHealth.errors.questionAnswerScheduleVersionConflict', 409, authoritative))
    const wrapper = form(c2Schedule()); await flushPromises()
    await wrapper.get('[aria-label="计划名称"]').setValue('用户输入')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect((wrapper.get('[aria-label="计划名称"]').element as HTMLInputElement).value).toBe('用户输入')
    expect(button(wrapper, '保存计划').attributes('disabled')).toBeDefined()
    api.previewQuestionAnswerSchedule.mockResolvedValue({ ...c2Preview(), scheduleVersion: 5 })
    await button(wrapper, '重新载入当前计划').trigger('click'); await flushPromises()
    expect((wrapper.get('[aria-label="计划名称"]').element as HTMLInputElement).value).toBe('服务端名称')
    expect(button(wrapper, '保存计划').attributes('disabled')).toBeUndefined()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.updateQuestionAnswerSchedule.mock.calls.at(-1)![2]).toBe(5)
  })

  it('rejects an equal time window, preserves strict future slots, and allows dynamic group estimates to be checked at execution', async () => {
    const wrapper = form(c2Schedule()); await flushPromises()
    await wrapper.get('[aria-label="高峰结束"]').setValue('08:00')
    expect(button(wrapper, '保存计划').attributes('disabled')).toBeDefined()
    await wrapper.get('[aria-label="高峰结束"]').setValue('22:00')
    api.previewQuestionAnswerSchedule.mockResolvedValue({ ...c2Preview(), estimatedTargetCount: 100, estimatedRequestsPerExecution: 1000 })
    await button(wrapper, '刷新当前预计').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('动态范围在执行开始时按实际目标检查')
    expect(button(wrapper, '保存计划').attributes('disabled')).toBeUndefined()
    const config = { peakStart: '22:00', peakEnd: '02:00', peakIntervalMinutes: 90, offPeakIntervalMinutes: 180 }
    const after = Date.parse('2026-10-07T14:00:00Z')
    const slots = questionAnswerScheduleSlots(config, after)
    expect(slots.slice(0, 4)).toEqual(['2026-10-07T15:30:00.000Z', '2026-10-07T17:00:00.000Z', '2026-10-07T18:00:00.000Z', '2026-10-07T21:00:00.000Z'])
    expect(slots.every(value => Date.parse(value) > after)).toBe(true)
    expect(validQuestionAnswerScheduleGrid({ ...config, peakIntervalMinutes: 31 })).toBe(false)
  })
})

describe('C2 drawer retains actions, local polling, and deleted history', () => {
  it('reads only saved local lists every30 seconds and retains rows on read failure', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = drawer(); await wrapper.get('[data-testid="question-answer-schedule-open"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-row"]').text()).toContain('保存时预计 1')
    expect(api.previewQuestionAnswerSchedule).not.toHaveBeenCalled(); expect(api.listTestQuestions).not.toHaveBeenCalled()
    api.listQuestionAnswerSchedules.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network'))
    await vi.advanceTimersByTimeAsync(30000); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-row"]').text()).toContain('核心定时计划')
    expect(wrapper.get('[role="alert"]').text()).toContain('网络')
    await button(wrapper, '重试读取').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('[aria-label="关闭定时测试"]').trigger('click')
    const count = api.listQuestionAnswerSchedules.mock.calls.length
    await vi.advanceTimersByTimeAsync(60000); expect(api.listQuestionAnswerSchedules).toHaveBeenCalledTimes(count)
  })

  it('preserves an idempotency request after network failure and opens authority on an active conflict', async () => {
    api.listQuestionAnswerSchedules.mockResolvedValue(page([c2Schedule({ enabled: false })]))
    api.runQuestionAnswerSchedule.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network')).mockRejectedValueOnce(new ConnectionHealthApiError('admin.connectionHealth.errors.questionAnswerScheduleActive', 409, c2Execution(), 'execution1'))
    const wrapper = drawer(); await wrapper.get('[data-testid="question-answer-schedule-open"]').trigger('click'); await flushPromises()
    expect(button(wrapper, '恢复').exists()).toBe(true)
    await button(wrapper, '立即执行').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('网络')
    expect(api.listQuestionAnswerSchedules).toHaveBeenCalledTimes(1)
    await button(wrapper, '立即执行').trigger('click'); await flushPromises()
    expect(api.runQuestionAnswerSchedule.mock.calls[0]![1]).toBe(api.runQuestionAnswerSchedule.mock.calls[1]![1])
    expect(wrapper.get('[data-testid="schedule-execution-detail"]').text()).toContain('执行中')
    expect(wrapper.text()).not.toContain('本次执行已接受，计划时刻')
  })

  it('soft-deletes without cancelling accepted work and reaches deleted active history and exact snapshots', async () => {
    const wrapper = drawer(); await wrapper.get('[data-testid="question-answer-schedule-open"]').trigger('click'); await flushPromises()
    api.listQuestionAnswerSchedules.mockResolvedValue(page([]))
    await button(wrapper, '删除').trigger('click'); await flushPromises()
    expect(api.deleteQuestionAnswerSchedule).toHaveBeenCalledWith('plan1', 1, expect.any(AbortSignal))
    expect(api.cancelQuestionAnswerScheduleExecution).not.toHaveBeenCalled()
    api.listQuestionAnswerSchedules.mockResolvedValue(page([c2Schedule({ enabled: false, deletedAt: '2026-10-08T00:05:00Z' })]))
    await button(wrapper, '已删除计划').trigger('click'); await flushPromises()
    const row = wrapper.get('[data-testid="schedule-row"]')
    expect(row.findAll('button').map(item => item.text())).toEqual(['历史'])
    await button(row, '历史').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="schedule-history-row"]').trigger('click'); await flushPromises()
    expect(button(wrapper, '终止本次执行').exists()).toBe(true)
    await button(wrapper, '查看批次').trigger('click')
    expect(wrapper.emitted('question-answer-view')?.[0]?.[0]).toMatchObject({ targetId: 'sub2api:ws1:a', batchId: 'exact-batch', accountName: '冻结账号名', groupName: '冻结组名' })
  })
})

describe('C2 execution details show records-derived totals and cancel authority', () => {
  it('keeps active on cancel409 finalizer conflict, ignores an earlier read, and polls until actual terminal', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = detail(); await flushPromises()
    const late = deferred<Execution>()
    api.getQuestionAnswerScheduleExecution.mockReturnValueOnce(late.promise)
    await button(wrapper, '刷新详情').trigger('click'); await flushPromises()
    const readSignal = api.getQuestionAnswerScheduleExecution.mock.calls.at(-1)![1] as AbortSignal
    api.cancelQuestionAnswerScheduleExecution.mockRejectedValueOnce(new ConnectionHealthApiError('admin.connectionHealth.errors.questionAnswerExecutionVersionConflict', 409, c2Execution({ version: 3, statusReason: 'c1_finalization_pending' })))
    await button(wrapper, '终止本次执行').trigger('click'); await flushPromises()
    expect(readSignal.aborted).toBe(true)
    expect(api.cancelQuestionAnswerScheduleExecution).toHaveBeenCalledWith('execution1', 2, expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="schedule-execution-status"]').text()).toContain('执行中')
    expect(wrapper.text()).toContain('准确批次正在收口')
    expect(wrapper.text()).not.toContain('终止请求已接受')
    late.resolve(c2Execution({ status: 'completed', version: 2, active: false })); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-execution-status"]').text()).toContain('执行中')
    api.getQuestionAnswerScheduleExecution.mockResolvedValue(c2Execution({ status: 'completed', version: 4, active: false }))
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-execution-status"]').text()).toContain('已完成')
    const count = api.getQuestionAnswerScheduleExecution.mock.calls.length
    await vi.advanceTimersByTimeAsync(4000); expect(api.getQuestionAnswerScheduleExecution).toHaveBeenCalledTimes(count)
    expect(wrapper.emitted('settled')).toEqual([[['sub2api:ws1:a']]])
  })

  it('keeps an accepted cancellation active until terminal and removes contradictory finalization text', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    api.cancelQuestionAnswerScheduleExecution.mockResolvedValue(c2Execution({ version: 3, terminationCause: 'user_cancel', cancelRequestedAt: '2026-10-08T00:02:00Z', statusReason: 'preparation_pending' }))
    const wrapper = detail(); await flushPromises()
    await button(wrapper, '终止本次执行').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-execution-status"]').text()).toContain('执行中')
    expect(wrapper.text()).toContain('已接受用户终止，等待收口')
    expect(wrapper.findAll('button').some(item => item.text() === '终止本次执行')).toBe(false)
    api.getQuestionAnswerScheduleExecution.mockResolvedValue(c2Execution({ version: 4, status: 'cancelled', active: false, terminationCause: 'user_cancel', cancelRequestedAt: '2026-10-08T00:02:00Z', statusReason: 'user_cancel' }))
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-execution-status"]').text()).toContain('已终止')
    expect(wrapper.text()).toContain('终止原因：用户终止')
    expect(wrapper.text()).not.toContain('等待收口')
  })

  it('shows all-account missing as skipped with zero totals, preserved failure snapshots and no batch links', async () => {
    const target = { ...c2Execution().targets[0]!, batchId: '', batchAvailable: false, status: 'failed' as const, statusReason: 'account_not_found', availableModels: [], stats: c2Stats(), plannedRequestCount: 0 }
    api.getQuestionAnswerScheduleExecution.mockResolvedValue(c2Execution({ status: 'skipped', active: false, statusReason: 'all_accounts_missing', batchCreatedTargetCount: 0, requestRecordCount: 0, reservedRequestCount: 0, stats: c2Stats(), targets: [target] }))
    const wrapper = detail(); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-execution-status"]').text()).toBe('跳过 · 所有指定账号已删除')
    expect(wrapper.get('[data-testid="schedule-total-accuracy"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="schedule-target"]').text()).toContain('冻结账号名 · —')
    expect(wrapper.get('[data-testid="schedule-target"]').text()).toContain('失败 · 账号已删除')
    expect(wrapper.text()).toContain('已建批 0 · 请求记录 0 · 保留预算 0')
    expect(wrapper.findAll('button').some(item => item.text() === '查看批次')).toBe(false)
    expect(wrapper.findAll('button').some(item => item.text() === '终止本次执行')).toBe(false)
  })

  it('derives total percentage from raw totals, shows unavailable model and rereads manual judgment without changing frozen content', async () => {
    const execution = c2Execution({ status: 'partial', active: false, stats: c2Stats(10, 90) })
    execution.stats.byModel = [{ modelName: 'm1', ...c2Stats(1) }, { modelName: 'm2', ...c2Stats(9, 90) }]
    execution.targets[0]!.requestedModels = ['m1', 'm2', 'gone-model']
    execution.targets[0]!.unavailableModels = [{ modelName: 'gone-model', reason: 'model_unavailable' }]
    api.getQuestionAnswerScheduleExecution.mockResolvedValue(execution)
    const wrapper = detail(); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-total-accuracy"]').text()).toBe('10%')
    expect(wrapper.text()).not.toContain('54.5%')
    expect(wrapper.text()).toContain('gone-model · — · 本次不可用')
    expect(wrapper.text()).toContain('旧题目正文')
    api.getQuestionAnswerScheduleExecution.mockResolvedValue({ ...execution, stats: c2Stats(20, 80) })
    await wrapper.setProps({ statsRevision: 1 }); await flushPromises()
    expect(wrapper.get('[data-testid="schedule-total-accuracy"]').text()).toBe('20%')
    expect(wrapper.text()).toContain('旧题目正文')
    expect(api.previewQuestionAnswerSchedule).not.toHaveBeenCalled()
    expect(api.listTestQuestions).not.toHaveBeenCalled()
  })
})

describe('C2 running protection and retained account result display', () => {
  it('loads the two settings independently and saves each version with its own contract', async () => {
    api.getQuestionAnswerScheduleLimits.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.network'))
    const wrapper = track(mount(QuestionAnswerScheduleProtection, { props: { workspace: 'ws1' } })); await flushPromises()
    expect(button(wrapper, '保存共享请求槽').attributes('disabled')).toBeUndefined()
    expect(button(wrapper, '保存工作区运行保护').attributes('disabled')).toBeDefined()
    await wrapper.get('[aria-label="问答请求并发"]').setValue('0')
    expect(button(wrapper, '保存共享请求槽').attributes('disabled')).toBeDefined()
    await wrapper.get('[aria-label="问答请求并发"]').setValue('2')
    await button(wrapper, '保存共享请求槽').trigger('click'); await flushPromises()
    expect(api.saveQuestionAnswerRuntimeSettings).toHaveBeenCalledWith(2, 3, expect.any(AbortSignal))
    expect(api.saveQuestionAnswerScheduleLimits).not.toHaveBeenCalled()
    await button(wrapper, '重试读取').trigger('click'); await flushPromises()
    await wrapper.get('[aria-label="单计划最大实际目标数"]').setValue('10')
    await button(wrapper, '保存工作区运行保护').trigger('click'); await flushPromises()
    expect(api.saveQuestionAnswerScheduleLimits).toHaveBeenCalledWith(expect.objectContaining({ maxScheduleTargets: 10, maxActiveScheduleExecutions: 3, scheduleLateGraceMinutes: 5 }), 1, expect.any(AbortSignal))
    expect(api.saveQuestionAnswerScheduleLimits.mock.calls[0]![0]).not.toHaveProperty('todayReservedRequests')
  })

  it('applies conflict authority and does not claim settings saved', async () => {
    api.saveQuestionAnswerRuntimeSettings.mockRejectedValueOnce(new ConnectionHealthApiError('admin.connectionHealth.errors.settingsConflict', 409, { questionAnswerConcurrency: 9, version: 7, updatedAt: null }))
    const wrapper = track(mount(QuestionAnswerScheduleProtection, { props: { workspace: 'ws1' } })); await flushPromises()
    await wrapper.get('[aria-label="问答请求并发"]').setValue('3')
    await button(wrapper, '保存共享请求槽').trigger('click'); await flushPromises()
    expect((wrapper.get('[aria-label="问答请求并发"]').element as HTMLInputElement).value).toBe('9')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    await button(wrapper, '保存共享请求槽').trigger('click'); await flushPromises()
    expect(api.saveQuestionAnswerRuntimeSettings.mock.calls.at(-1)![1]).toBe(7)
  })

  it('keeps totals and today/lifetime while expanding model percentages by default', async () => {
    const stats = c2Stats(3, 1); stats.byModel = [{ modelName: 'm1', ...c2Stats(3, 1) }]
    const wrapper = track(mount(QuestionAnswerStatsBar, { props: { reviewStats: stats, todayStats: c2Stats(2, 1), lifetimeStats: c2Stats(5, 1) } }))
    expect(button(wrapper, '按模型').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('75%')
    expect(wrapper.get('[data-testid="question-answer-stats-today"]').text()).toContain('66.7%')
    expect(wrapper.get('[data-testid="question-answer-stats-lifetime"]').text()).toContain('83.3%')
    expect(wrapper.text()).toContain('m1 · 75%')
    expect(wrapper.text()).not.toMatch(/正确\s*\/|3\s*\/\s*4/)
    await button(wrapper, '按题目').trigger('click'); await nextTick()
    expect(wrapper.get('[data-testid="question-answer-stats-review"] [data-testid="question-answer-accuracy"]').text()).toBe('75%')
  })

  it('polls a read-only active exact batch locally and stops at its actual terminal state', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    api.getQuestionAnswerBatch.mockResolvedValueOnce({ batchId: 'exact-batch', active: true, stats: c2Stats(1), records: [] }).mockResolvedValue({ batchId: 'exact-batch', active: false, stats: c2Stats(3, 1), records: [] })
    const wrapper = track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: 'sub2api:ws1:a', batchId: 'exact-batch', accountName: '冻结账号名', workspace: 'ws1' }, global: { stubs: { Teleport: true } } })); await flushPromises()
    expect(wrapper.text()).toContain('当前批次总正确率 100%')
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(wrapper.text()).toContain('当前批次总正确率 75%')
    await vi.advanceTimersByTimeAsync(4000)
    expect(api.getQuestionAnswerBatch).toHaveBeenCalledTimes(2)
    expect(api.getLatestQuestionAnswerBatch).not.toHaveBeenCalled()
  })

  it('opens the supplied exact batch read-only after the account leaves current projection', async () => {
    api.getQuestionAnswerBatch.mockResolvedValue({ id: 'exact-batch', stats: c2Stats(3, 1), records: [] })
    const wrapper = track(mount(QuestionAnswerScheduleBatchDialog, { props: { targetId: 'sub2api:ws1:a', batchId: 'exact-batch', accountName: '冻结账号名', workspace: 'ws1' }, global: { stubs: { Teleport: true } } })); await flushPromises()
    expect(api.getQuestionAnswerBatch).toHaveBeenCalledWith('sub2api:ws1:a', 'exact-batch', expect.any(AbortSignal))
    expect(wrapper.text()).toContain('当前批次总正确率 75%')
    expect(wrapper.text()).toContain('只读查看该准确批次')
    expect(api.getLatestQuestionAnswerBatch).not.toHaveBeenCalled()
    expect(api.discoverManualProbeModels).not.toHaveBeenCalled()
    expect(api.listTestQuestions).not.toHaveBeenCalled()
    expect(wrapper.findAll('button').some(item => /判断|发题/.test(item.text()))).toBe(false)
  })
})
