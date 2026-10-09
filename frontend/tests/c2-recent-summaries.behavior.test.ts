// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { c2Groups, c2Stats } from './fixtures/c2QuestionAnswerSchedules'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import { getQuestionAnswerRecentSummaries } from '@/modules/admin/api/connectionHealth'
import { useQuestionAnswerRecentSummaries } from '@/modules/admin/composables/useQuestionAnswerRecentSummaries'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import type { AdminGroupHealth, QuestionAnswerRecentSummaryItem } from '@/modules/admin/types/connectionHealth'

const api = vi.hoisted(() => ({ getConnectionHealthAdminGroups: vi.fn(), refreshConnectionHealthAdminGroupsAutomatically: vi.fn(), getConnectionHealthGroups: vi.fn(), getConnectionHealthEvents: vi.fn(), listConnectionHealthPolicies: vi.fn(), getPrioritySyncStatus: vi.fn(), listUpstreamSites: vi.fn(), workspace: null as any }))
vi.mock('@/modules/admin/api/connectionHealth', async (importOriginal) => ({ ...await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>(), getConnectionHealthAdminGroups: api.getConnectionHealthAdminGroups, refreshConnectionHealthAdminGroupsAutomatically: api.refreshConnectionHealthAdminGroupsAutomatically, getConnectionHealthGroups: api.getConnectionHealthGroups, getConnectionHealthEvents: api.getConnectionHealthEvents, listConnectionHealthPolicies: api.listConnectionHealthPolicies, getPrioritySyncStatus: api.getPrioritySyncStatus }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: api.listUpstreamSites }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); api.workspace = ref({ id: 'ws1', displayName: 'Workspace1', platform: 'sub2api' }); return { useAdminAccounts: () => ({ currentAccount: api.workspace }) } })
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn(async () => undefined) }) }))

const wrappers: VueWrapper[] = [], coordinators: ReturnType<typeof useQuestionAnswerRecentSummaries>[] = []
const service = useConnectionHealth()
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const targetId = 'sub2api:ws1:a'
const recent = (id = targetId, correct = 3, incorrect = 1, batchId = 'latest-frozen'): QuestionAnswerRecentSummaryItem => ({ targetId: id, modelControl: null, activeNewerBatch: false, recentQuestionAnswer: { batchId, source: 'scheduled', scheduleName: '冻结计划', createdAt: '2026-10-08T00:00:00Z', completedAt: '2026-10-08T00:01:00Z', partial: false, requests: c2Stats(correct, incorrect).requests, reviews: c2Stats(correct, incorrect).reviews } })
const idsOf = (input: string) => new URL(input, 'http://localhost').searchParams.getAll('targetId')
const makeCoordinator = (apply: (item: QuestionAnswerRecentSummaryItem) => void = vi.fn(), workspace = () => 'ws1', visible = () => true) => { const coordinator = useQuestionAnswerRecentSummaries({ workspace, visible, apply }); coordinators.push(coordinator); return coordinator }
const mountedRows = () => { const wrapper = mount(defineComponent({ setup: () => () => h('div', service.adminGroups.value.map(group => h(AdminGroupHealthDetail, { key: group.id, group, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false }))) })); wrappers.push(wrapper); return wrapper }
const projectGroups = (): AdminGroupHealth[] => { const first = c2Groups()[0]!; return [first, { ...first, id: 'g2', name: '另一组', accounts: first.accounts.map(account => ({ ...account })) }] }
const track = <T extends VueWrapper>(wrapper: T) => { wrappers.push(wrapper); return wrapper }

beforeEach(() => {
  vi.resetAllMocks(); localStorage.clear()
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  api.workspace.value = { id: 'ws1', displayName: 'Workspace1', platform: 'sub2api' }
  service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('ws1')
  api.getConnectionHealthAdminGroups.mockResolvedValue(projectGroups())
  api.refreshConnectionHealthAdminGroupsAutomatically.mockImplementation(async () => ({ status: 'success', groups: service.adminGroups.value, refresh: { state: 'success', sites: [] } }))
  api.getConnectionHealthGroups.mockResolvedValue([]); api.getConnectionHealthEvents.mockResolvedValue([]); api.listConnectionHealthPolicies.mockResolvedValue([]); api.getPrioritySyncStatus.mockRejectedValue(new Error('isolated auxiliary unavailable')); api.listUpstreamSites.mockResolvedValue([])
  vi.stubGlobal('fetch', vi.fn(async (input: string) => json({ items: idsOf(String(input)).map(id => recent(id)) })))
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  for (const coordinator of coordinators.splice(0)) coordinator.reset()
  service.cancelAdminGroupsRefresh(); service.setAdminGroupsWorkspace('')
  vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear(); document.body.innerHTML = ''
})

describe('C2 pure local recent API and dirty coordinator', () => {
  it('preserves null with first-active, source and raw counts, and deduplicates requested IDs in order', async () => {
    const first = { targetId, recentQuestionAnswer: null, activeNewerBatch: true, modelControl: null }, second = recent('sub2api:ws1:b', 1, 2)
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [first, second] })); vi.stubGlobal('fetch', fetchMock)
    const result = await getQuestionAnswerRecentSummaries([targetId, targetId, second.targetId])
    expect(result.items).toEqual([first, second]); expect(result.items[0]!.recentQuestionAnswer).toBeNull()
    expect(idsOf(fetchMock.mock.calls[0]![0])).toEqual([targetId, second.targetId])
    expect(fetchMock.mock.calls[0]![0]).toContain('/question-answer-recent-summaries?')
    expect(fetchMock.mock.calls[0]![1].method).toBeUndefined()
    expect(result.items[1]!.recentQuestionAnswer?.reviews).toEqual({ correct: 1, incorrect: 2, unreviewed: 0 })
  })

  it('fails incomplete or mismatched responses without applying fabricated null', async () => {
    const apply = vi.fn(), fetchMock = vi.fn().mockImplementation(async () => json({ items: [] })); vi.stubGlobal('fetch', fetchMock)
    await expect(getQuestionAnswerRecentSummaries([targetId])).rejects.toThrow('admin.connectionHealth.errors.request')
    const coordinator = makeCoordinator(apply); coordinator.refreshTargets([targetId]); await flushPromises()
    expect(apply).not.toHaveBeenCalled(); expect(coordinator.failures.value).toEqual([targetId])
    fetchMock.mockImplementation(async () => json({ items: [recent('sub2api:ws2:a')] }))
    coordinator.retry(targetId); await flushPromises(); expect(apply).not.toHaveBeenCalled()
  })

  it('chunks displayed targets at50, coalesces duplicate dirty events and never requests discovery', async () => {
    const apply = vi.fn(), coordinator = makeCoordinator(apply)
    const ids = Array.from({ length: 121 }, (_, index) => `sub2api:ws1:${index}`)
    coordinator.refreshTargets([...ids, ids[0]!, 'sub2api:ws2:foreign']); await flushPromises()
    const calls = vi.mocked(fetch).mock.calls
    expect(calls.map(call => idsOf(String(call[0])).length)).toEqual([50, 50, 21])
    expect(apply).toHaveBeenCalledTimes(121)
    expect(calls.every(call => String(call[0]).includes('/question-answer-recent-summaries?') && !call[1]?.method)).toBe(true)
  })

  it('does not let an in-flight older summary overwrite a newer dirty identity', async () => {
    const old = deferred<Response>(), apply = vi.fn()
    const fetchMock = vi.fn().mockReturnValueOnce(old.promise).mockImplementation(async () => json({ items: [recent(targetId, 1, 3, 'newest-batch')] })); vi.stubGlobal('fetch', fetchMock)
    const coordinator = makeCoordinator(apply)
    coordinator.refreshTargets([targetId]); await flushPromises()
    coordinator.refreshTargets([targetId]); coordinator.refreshTargets([targetId]); await flushPromises()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    old.resolve(json({ items: [recent(targetId, 4, 0, 'old-batch')] })); await flushPromises()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(apply).toHaveBeenCalledTimes(1)
    expect(apply.mock.calls[0]![0].recentQuestionAnswer.batchId).toBe('newest-batch')
  })

  it('aborts hidden requests, keeps pending work, resumes visible and discards a previous workspace response', async () => {
    const first = deferred<Response>(), oldWorkspace = deferred<Response>(), apply = vi.fn()
    const fetchMock = vi.fn().mockReturnValueOnce(first.promise).mockImplementationOnce(async () => json({ items: [recent()] })).mockReturnValueOnce(oldWorkspace.promise).mockImplementation(async () => json({ items: [{ targetId: 'sub2api:ws2:a', recentQuestionAnswer: null, activeNewerBatch: true, modelControl: null }] })); vi.stubGlobal('fetch', fetchMock)
    let visible = true, workspace = 'ws1'
    const coordinator = makeCoordinator(apply, () => workspace, () => visible)
    coordinator.refreshTargets([targetId]); await flushPromises()
    visible = false; coordinator.suspend()
    expect((fetchMock.mock.calls[0]![1].signal as AbortSignal).aborted).toBe(true)
    first.resolve(json({ items: [recent(targetId, 4, 0, 'hidden-old')] })); await flushPromises(); expect(apply).not.toHaveBeenCalled()
    visible = true; coordinator.resume(); await flushPromises(); expect(apply).toHaveBeenCalledTimes(1)
    coordinator.refreshTargets([targetId]); await flushPromises()
    workspace = 'ws2'; coordinator.reset(); coordinator.refreshTargets(['sub2api:ws2:a']); await flushPromises()
    oldWorkspace.resolve(json({ items: [recent(targetId, 4, 0, 'workspace-old')] })); await flushPromises()
    expect(apply).toHaveBeenCalledTimes(2)
    expect(apply.mock.calls[1]![0]).toEqual({ targetId: 'sub2api:ws2:a', recentQuestionAnswer: null, activeNewerBatch: true, modelControl: null })
  })

  it('retains prior value on failure, displays local retry, and clears only the failed target after success', async () => {
    const retained = ref(recent()), fetchMock = vi.fn().mockRejectedValueOnce(new Error('network')).mockImplementation(async () => json({ items: [{ targetId, recentQuestionAnswer: null, activeNewerBatch: true, modelControl: null }] })); vi.stubGlobal('fetch', fetchMock)
    const coordinator = makeCoordinator(item => { retained.value = item })
    const wrapper = track(mount(defineComponent({ setup: () => () => h('section', [h('span', retained.value.recentQuestionAnswer?.batchId ?? '—'), ...(coordinator.failures.value.includes(targetId) ? [h('button', { onClick: () => coordinator.retry(targetId) }, '局部重试')] : [])]) })))
    coordinator.refreshTargets([targetId]); await flushPromises()
    expect(wrapper.text()).toContain('latest-frozen'); expect(wrapper.text()).toContain('局部重试')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.text()).toBe('—'); expect(coordinator.failures.value).toEqual([])
    expect(retained.value.activeNewerBatch).toBe(true)
  })
})

describe('C2 recent and today revisions update all projections independently', () => {
  it.each(['get', 'automatic'])('protects newer recent and today from an older %s admin group response', async kind => {
    await service.loadAdminGroups()
    const wrapper = mountedRows(), stale = projectGroups(), response = deferred<any>()
    stale.forEach(group => Object.assign(group.accounts[0]!, { recentQuestionAnswer: recent(targetId, 4, 0, 'stale-batch').recentQuestionAnswer }))
    if (kind === 'get') api.getConnectionHealthAdminGroups.mockReturnValueOnce(response.promise)
    else api.refreshConnectionHealthAdminGroupsAutomatically.mockReturnValueOnce(response.promise)
    const read = kind === 'get' ? service.loadAdminGroups({ silent: true }) : service.refreshAdminGroupsAutomatically()
    service.applyQuestionAnswerRecentSummary(recent(targetId, 1, 3, 'newest-batch'))
    service.applyQuestionAnswerTodaySummary({ targetId, todayStats: c2Stats(7, 3) })
    response.resolve(kind === 'get' ? stale : { status: 'success', groups: stale, refresh: { state: 'success', sites: [] } })
    await read; await flushPromises()
    expect(service.adminGroups.value.map(group => group.accounts[0]!.recentQuestionAnswer?.batchId)).toEqual(['newest-batch', 'newest-batch'])
    expect(service.adminGroups.value.map(group => group.accounts[0]!.todayQuestionAnswerCorrect)).toEqual([7, 7])
    expect(wrapper.findAll('tbody > tr').every(row => row.findAll('td')[8]!.text().includes('25%'))).toBe(true)
    expect(wrapper.text()).not.toContain('100%')
  })

  it('shows partial unreviewed and all-failed latest states without falling back to old today results', async () => {
    await service.loadAdminGroups(); const wrapper = mountedRows()
    const summary = recent()
    summary.recentQuestionAnswer!.partial = true
    summary.recentQuestionAnswer!.requests = c2Stats(0, 0, 2, 1).requests
    summary.recentQuestionAnswer!.reviews = c2Stats(0, 0, 2, 1).reviews
    service.applyQuestionAnswerRecentSummary(summary); await nextTick()
    expect(wrapper.findAll('tbody > tr').every(row => { const cell = row.findAll('td')[8]!.text(); return cell.includes('—') && cell.includes('待人工判断') && cell.includes('部分完成') && !cell.includes('100%') })).toBe(true)
    summary.recentQuestionAnswer!.partial = false
    summary.recentQuestionAnswer!.requests = c2Stats(0, 0, 0, 2).requests
    summary.recentQuestionAnswer!.reviews = c2Stats().reviews
    service.applyQuestionAnswerRecentSummary(summary); await nextTick()
    expect(wrapper.findAll('tbody > tr').every(row => row.findAll('td')[8]!.text().includes('最近测试全部失败'))).toBe(true)
  })

  it('preserves explicit null/active in every projection and refuses a foreign workspace mutation', async () => {
    await service.loadAdminGroups(); const wrapper = mountedRows()
    service.applyQuestionAnswerRecentSummary({ targetId, recentQuestionAnswer: null, activeNewerBatch: true, modelControl: null })
    service.applyQuestionAnswerRecentSummary(recent('sub2api:ws2:a', 4))
    await nextTick()
    expect(wrapper.findAll('tbody > tr').every(row => row.findAll('td')[8]!.text().includes('—') && row.findAll('td')[8]!.text().includes('测试中'))).toBe(true)
    expect(service.adminGroups.value.every(group => group.accounts[0]!.recentQuestionAnswer === null && group.accounts[0]!.activeNewerQuestionAnswerBatch)).toBe(true)
    expect(service.adminGroups.value.map(group => group.accounts[0]!.todayQuestionAnswerCorrect)).toEqual([10, 10])
  })
})

const mountMain = async () => {
  service.adminGroups.value = projectGroups()
  service.adminGroups.value.forEach(group => { group.accounts[0]!.probeAvailable = true })
  const wrapper = track(mount(ConnectionHealthView, { global: { stubs: { Teleport: true, ConnectionHealthEventsDialog: true, GroupHealthSetupDrawer: true, ManualOneTimeProbeDialog: true, QuestionAnswerBatchDrawer: true, QuestionAnswerScheduleDrawer: true, PolicyConfigDrawer: true, ProbePolicyListDialog: true, TargetPolicyAssignmentDialog: true } } }))
  await flushPromises(); return wrapper
}

describe('C2 mounted main page keeps pure local recent polling separate from today failures', () => {
  it('rereads latest identity after manual edits despite today failure, then retries both without clearing recent', async () => {
    let recentValue = recent(), failToday = true, failRecent = false
    const fetchMock = vi.fn(async (input: string) => {
      if (String(input).includes('/question-answer-recent-summaries?')) return failRecent ? json({ message: 'admin.connectionHealth.errors.questionAnswerStorage' }, 500) : json({ items: idsOf(String(input)).map(id => ({ ...recentValue, targetId: id })) })
      if (String(input).endsWith('/question-answers/summary')) return failToday ? json({ message: 'admin.connectionHealth.errors.questionAnswerStorage' }, 500) : json({ targetId, todayStats: c2Stats(6, 4) })
      throw new Error(`unexpected request ${input}`)
    }); vi.stubGlobal('fetch', fetchMock)
    const wrapper = await mountMain(), dialog = wrapper.findComponent(ManualOneTimeProbeDialog)
    expect(wrapper.text()).toContain('75%')
    recentValue = recent(targetId, 1, 3, 'new-latest-after-old-edit')
    dialog.vm.$emit('question-answer-stats-dirty', targetId); await flushPromises()
    expect(wrapper.text()).toContain('25%')
    expect(service.adminGroups.value.map(group => group.accounts[0]!.recentQuestionAnswer?.batchId)).toEqual(['new-latest-after-old-edit', 'new-latest-after-old-edit'])
    expect(service.adminGroups.value.map(group => group.accounts[0]!.todayQuestionAnswerCorrect)).toEqual([10, 10])
    failToday = false; failRecent = true
    dialog.vm.$emit('question-answer-stats-dirty', targetId); await flushPromises()
    expect(wrapper.text()).toContain('25%'); expect(wrapper.text()).toContain('重试')
    expect(service.adminGroups.value.map(group => group.accounts[0]!.todayQuestionAnswerCorrect)).toEqual([6, 6])
    failRecent = false; recentValue = recent(targetId, 3, 1, 'real-latest')
    dialog.vm.$emit('question-answer-stats-retry', targetId); await flushPromises()
    expect(wrapper.text()).toContain('75%')
    expect(service.adminGroups.value.map(group => group.accounts[0]!.recentQuestionAnswer?.batchId)).toEqual(['real-latest', 'real-latest'])
    expect(fetchMock.mock.calls.every(call => /recent-summaries\?|question-answers\/summary/.test(String(call[0])))).toBe(true)
  })

  it('polls displayed targets only, suspends when hidden, catches up visible, and cancels on workspace/unmount', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] })
    const wrapper = await mountMain(), fetchMock = vi.mocked(fetch)
    const initialReads = fetchMock.mock.calls.length
    expect(initialReads).toBeGreaterThan(0)
    await vi.advanceTimersByTimeAsync(30000); await flushPromises()
    expect(fetchMock.mock.calls.length).toBeGreaterThan(initialReads)
    expect(fetchMock.mock.calls.every(call => idsOf(String(call[0])).length === 1)).toBe(true)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); await nextTick()
    const hiddenReads = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(60000); await flushPromises(); expect(fetchMock.mock.calls.length).toBe(hiddenReads)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }); document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(fetchMock.mock.calls.length).toBeGreaterThan(hiddenReads)
    const late = deferred<Response>(); fetchMock.mockReturnValueOnce(late.promise)
    await vi.advanceTimersByTimeAsync(30000); await flushPromises()
    const signal = fetchMock.mock.calls.at(-1)![1]!.signal as AbortSignal
    api.workspace.value = { id: 'ws2', displayName: 'Workspace2', platform: 'sub2api' }; await flushPromises()
    expect(signal.aborted).toBe(true)
    late.resolve(json({ items: [recent(targetId, 4, 0, 'foreign-old')] })); await flushPromises()
    expect(service.adminGroups.value).toEqual([])
    const finalReads = fetchMock.mock.calls.length
    wrapper.unmount(); wrappers.splice(wrappers.indexOf(wrapper), 1)
    await vi.advanceTimersByTimeAsync(60000); expect(fetchMock.mock.calls.length).toBe(finalReads)
  })

  it('opens the exact latest batch even if the account is presently unable to start a new test', async () => {
    const wrapper = await mountMain()
    service.adminGroups.value.forEach(group => { group.accounts[0]!.probeAvailable = false }); await nextTick()
    vi.mocked(fetch).mockImplementation(async (input: string) => {
      if (String(input).includes('/batches/latest-frozen')) return json({ batchId: 'latest-frozen', records: [], stats: c2Stats(3, 1), active: false })
      throw new Error(`unexpected ${input}`)
    })
    const cell = wrapper.get('tbody > tr').findAll('td')[8]!
    await cell.get('button').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('只读查看该准确批次')
    expect(wrapper.text()).toContain('当前批次总正确率 75%')
    expect(vi.mocked(fetch).mock.calls.at(-1)![0]).toContain('/batches/latest-frozen')
    expect(vi.mocked(fetch).mock.calls.some(call => String(call[0]).endsWith('/models'))).toBe(false)
  })
})
