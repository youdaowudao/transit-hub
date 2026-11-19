// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import { useHiddenAccounts } from '@/modules/admin/composables/useHiddenAccounts'
import { modelControlExcludedTargets, modelControlSections } from '@/modules/admin/utils/questionAnswerModelControl'
import type { AdminGroupAccount, AdminGroupHealth, ModelControlItem } from '@/modules/admin/types/connectionHealth'

vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn() }) }))

type Reply = { status: number; body?: unknown; raw?: string } | 'network'
let replies: Array<{ match: (url: string, init?: RequestInit) => boolean; reply: () => Reply | Promise<Reply> }> = []
const calls: Array<{ url: string; method: string }> = []

const respond = (reply: Reply): Response | Promise<never> => {
  if (reply === 'network') return Promise.reject(new TypeError('Failed to fetch'))
  return new Response(reply.raw ?? (reply.body === undefined ? '' : JSON.stringify(reply.body)), { status: reply.status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  replies = []
  calls.length = 0
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, method: init?.method ?? 'GET' })
    const index = replies.findIndex(item => item.match(url, init))
    if (index < 0) throw new Error(`unexpected request ${url}`)
    const [entry] = replies.splice(index, 1)
    return respond(await entry.reply())
  }))
})
afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })

const onGet = (reply: () => Reply | Promise<Reply>) => replies.push({ match: (url, init) => url.includes('/hidden-accounts?') && (init?.method ?? 'GET') === 'GET', reply })
const onWrite = (method: string, reply: () => Reply | Promise<Reply>) => replies.push({ match: (url, init) => url.includes('/hidden-accounts/') && init?.method === method, reply })
const list = (workspaceId: string, ids: string[]) => ({ status: 200, body: { workspaceId, items: ids.map(id => ({ targetId: id, accountName: `name-${id}`, hiddenAt: '2026-11-19T00:00:00Z' })) } })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }

describe('hidden account list state', () => {
  it('reads with the page workspace, keeps the old list on a workspace conflict, and ignores a list for another workspace', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    onGet(() => list('ws1', ['sub2api:ws1:a']))
    expect(await state.load()).toBe(true)
    expect(calls[0].url).toContain('workspace=ws1')
    expect([...state.hiddenTargetIds.value]).toEqual(['sub2api:ws1:a'])

    onGet(() => ({ status: 409, body: { message: 'admin.connectionHealth.errors.workspaceChanged', detail: '读取屏蔽清单失败：当前工作区已变化' } }))
    expect(await state.load()).toBe(false)
    expect([...state.hiddenTargetIds.value]).toEqual(['sub2api:ws1:a'])
    expect(state.readError.value).toContain('当前工作区已变化')
    expect(state.status.value).toBe('loaded')

    onGet(() => list('ws2', []))
    expect(await state.load()).toBe(false)
    expect([...state.hiddenTargetIds.value]).toEqual(['sub2api:ws1:a'])
  })

  it('shows the never-loaded state only until the first successful read and the empty state only after an empty list', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    onGet(() => 'network')
    await state.load()
    expect(state.status.value).toBe('failed')
    expect(state.readError.value).toBe('网络中断或超时')
    onGet(() => list('ws1', []))
    await state.load()
    expect(state.status.value).toBe('loaded')
    expect(state.items.value).toEqual([])
  })

  it('applies a confirmed write locally, discards reads started before the write ends, and rereads after it', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    const stale = deferred<Reply>()
    onGet(() => stale.promise)
    const staleLoad = state.load()
    onWrite('PUT', () => ({ status: 200, body: { targetId: 'sub2api:ws1:a', hidden: true, hiddenAt: '2026-11-19T01:00:00Z' } }))
    onGet(() => list('ws1', ['sub2api:ws1:a']))
    const result = await state.write('sub2api:ws1:a', true, '画图账号')
    expect(result?.outcome.kind).toBe('saved')
    expect(result?.reread).toBe(true)
    stale.resolve(list('ws1', []))
    await staleLoad
    expect([...state.hiddenTargetIds.value]).toEqual(['sub2api:ws1:a'])
    expect(state.writing.value).toBe(false)
  })

  it('treats a structurally wrong 2xx and a 4xx without a server key as unconfirmed, and only a 4xx with a key as not saved', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    onWrite('PUT', () => ({ status: 200, body: { targetId: 'other', hidden: true, hiddenAt: 'x' } }))
    onGet(() => list('ws1', []))
    expect((await state.write('sub2api:ws1:a', true, 'a'))?.outcome).toMatchObject({ kind: 'unknown', reason: 'unparseable' })
    onWrite('PUT', () => ({ status: 400, raw: '' }))
    onGet(() => list('ws1', []))
    expect((await state.write('sub2api:ws1:a', true, 'a'))?.outcome.kind).toBe('unknown')
    onWrite('PUT', () => ({ status: 400, body: { message: 'admin.connectionHealth.errors.targetNotFound' } }))
    expect((await state.write('sub2api:ws1:a', true, 'a'))?.outcome).toMatchObject({ kind: 'rejected', status: 400 })
    onWrite('DELETE', () => ({ status: 500, body: { message: 'x' } }))
    onGet(() => list('ws1', []))
    expect((await state.write('sub2api:ws1:a', false, ''))?.outcome).toMatchObject({ kind: 'unknown', reason: 'server' })
  })

  it('keeps the unconfirmed notice after automatic reads and clears it only after a manual reread succeeds', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    onWrite('PUT', () => 'network')
    onGet(() => list('ws1', []))
    await state.write('sub2api:ws1:a', true, 'a')
    expect(state.unconfirmed.value).toBe('网络中断或超时')
    onGet(() => list('ws1', ['sub2api:ws1:a']))
    await state.load()
    expect(state.unconfirmed.value).not.toBe('')
    onGet(() => list('ws1', ['sub2api:ws1:a']))
    await state.load(true)
    expect(state.unconfirmed.value).toBe('')
  })

  it('allows one write at a time and drops results after a workspace switch', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    const slow = deferred<Reply>()
    onWrite('PUT', () => slow.promise)
    const first = state.write('sub2api:ws1:a', true, 'a')
    expect(await state.write('sub2api:ws1:b', true, 'b')).toBeNull()
    state.reset('ws2')
    slow.resolve({ status: 200, body: { targetId: 'sub2api:ws1:a', hidden: true, hiddenAt: 't' } })
    expect(await first).toBeNull()
    expect(state.items.value).toEqual([])
  })
})

describe('review fixes', () => {
  it('drops a write result from before a workspace switch even after switching back', async () => {
    const state = useHiddenAccounts()
    state.reset('ws1')
    const slow = deferred<Reply>()
    onWrite('PUT', () => slow.promise)
    const old = state.write('sub2api:ws1:a', true, 'a')
    state.reset('ws2')
    state.reset('ws1')
    // 写锁保留到实际请求结束，切换回来也不能再发第二个写入。
    expect(state.writing.value).toBe(true)
    expect(await state.write('sub2api:ws1:a', false, '')).toBeNull()
    onGet(() => list('ws1', ['sub2api:ws1:a']))
    slow.resolve({ status: 200, body: { targetId: 'sub2api:ws1:a', hidden: true, hiddenAt: 't' } })
    expect(await old).toBeNull()
    expect(state.writing.value).toBe(false)
    // 已切回该请求所属的工作区：为当前会话重读清单，四处显示用到最新结果。
    await vi.waitFor(() => expect([...state.hiddenTargetIds.value]).toEqual(['sub2api:ws1:a']))
  })

  it('keeps unconfirmed model operations when only the display scope changes', async () => {
    const { createModelControlVerifyState } = await import('@/modules/admin/utils/questionAnswerModelControl')
    const verify = createModelControlVerifyState({ verify: async () => ({ items: [], errors: [] }) })
    verify.markUnconfirmed('sub2api:ws1:a', 'model-x')
    verify.resetScope()
    expect(verify.isUnconfirmed('sub2api:ws1:a', 'model-x')).toBe(true)
    verify.dispose()
    expect(verify.isUnconfirmed('sub2api:ws1:a', 'model-x')).toBe(false)
  })
})

const account = (targetId: string, overrides: Partial<AdminGroupAccount> = {}): AdminGroupAccount => ({
  id: targetId.split(':').pop()!, name: `Account ${targetId}`, platform: 'openai', type: 'apikey', status: 'active', targetId, probeAvailable: true, modelHealth: [], ...overrides,
} as AdminGroupAccount)
const group = (id: string, accounts: AdminGroupAccount[], overrides: Partial<AdminGroupHealth> = {}): AdminGroupHealth => ({
  id, name: `Group ${id}`, platform: 'sub2api', status: 'active', type: 'standard', isExclusive: false, subscriptionType: '', multiplier: 1, multiplierDisplay: '1x', accountCount: accounts.length,
  healthSummary: { totalAccounts: accounts.length, probeableAccounts: 0, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
  accounts, ...overrides,
} as AdminGroupHealth)

describe('group detail hides only table rows', () => {
  const mountDetail = (hidden: string[], accounts = [account('sub2api:ws1:img'), account('sub2api:ws1:txt')], focused: string[] = []) => mount(AdminGroupHealthDetail, {
    attachTo: document.body,
    props: { group: group('g1', accounts), hiddenTargetIds: new Set(hidden), hideEnabled: true, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false, focusedAccountIds: focused },
  })

  it('removes hidden accounts from rows and displayed targets while keeping statistics on all members', async () => {
    const wrapper = mountDetail(['sub2api:ws1:img'])
    await flushPromises()
    expect(wrapper.text()).toContain('另有 1 个已屏蔽账号未显示')
    expect(wrapper.text()).toContain('账号总数包含它们，健康监控不计入')
    expect(wrapper.text()).not.toContain('Account sub2api:ws1:img')
    expect(wrapper.text()).toContain('Account sub2api:ws1:txt')
    const displayed = wrapper.emitted('displayed-targets')!.at(-1)![0] as string[]
    expect(displayed).toEqual(['sub2api:ws1:txt'])
    wrapper.unmount()
  })

  it('shows the all-hidden empty row and the hidden notice when locating a hidden account', async () => {
    const wrapper = mountDetail(['sub2api:ws1:img', 'sub2api:ws1:txt'], undefined, ['img'])
    await flushPromises()
    expect(wrapper.get('[data-testid="account-table-empty"]').text()).toBe('本组账号都已屏蔽。')
    expect(wrapper.text()).toContain('已屏蔽，不在列表中显示')
    expect(wrapper.text()).not.toContain('没有找到')
    wrapper.unmount()
  })

  it('emits hide-account from the row button', async () => {
    const wrapper = mountDetail([], [account('sub2api:ws1:txt')])
    await flushPromises()
    await wrapper.get('[data-testid="hide-account-button"]').trigger('click')
    expect((wrapper.emitted('hide-account')![0][0] as AdminGroupAccount).targetId).toBe('sub2api:ws1:txt')
    wrapper.unmount()
  })
})

const item = (targetId: string, modelName: string, accountName = targetId): ModelControlItem => ({ targetId, modelName, accountName } as ModelControlItem)

describe('model control sections', () => {
  it('follows the sidebar order, skips hidden groups, sorts by Priority ascending with empty last, and places shared accounts once', () => {
    const groups = [
      group('second', [account('t-low', { priority: 1 }), account('t-shared', { priority: 5 })]),
      group('first', [account('t-null'), account('t-high', { priority: 9 }), account('t-shared', { priority: 5 })]),
      group('hidden', [account('t-hidden-only', { priority: 0 })]),
    ]
    const items = [item('t-high', 'b'), item('t-high', 'a'), item('t-null', 'x'), item('t-low', 'x'), item('t-shared', 'x'), item('t-gone', 'x', 'Zed')]
    const sections = modelControlSections(items, groups, ['hidden'])
    expect(sections.map(section => section.title)).toEqual(['Group second', 'Group first', '不在已读到分组里的账号'])
    expect(sections[0].rows.map(row => row.targetId)).toEqual(['t-low', 't-shared'])
    expect(sections[0].rows[1].alsoIn).toBe('Group first')
    expect(sections[1].rows.map(row => row.targetId)).toEqual(['t-high', 't-null'])
    expect(sections[1].rows[0].items.map(entry => entry.modelName)).toEqual(['a', 'b'])
  })

  it('excludes hidden-list accounts and accounts only in hidden groups, but only the hidden list when a visible group failed to load', () => {
    const groups = [group('show', [account('both')]), group('hide', [account('both'), account('only-hidden')])]
    expect(modelControlExcludedTargets(groups, ['hide'], new Set(['blocked']))).toEqual({ excludeTargetIds: ['blocked', 'only-hidden'], partial: false })
    const partialGroups = [group('show', [], { accountsError: 'admin.connectionHealth.errors.accountsFetch' }), group('hide', [account('only-hidden')])]
    expect(modelControlExcludedTargets(partialGroups, ['hide'], new Set(['blocked']))).toEqual({ excludeTargetIds: ['blocked'], partial: true })
  })
})
