// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  getConnectionHealthAdminGroups,
  refreshConnectionHealthAdminGroups,
} from '@/modules/admin/api/connectionHealth'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const harness = vi.hoisted(() => ({
  setTargetIntelligenceWeight: vi.fn(),
}))

vi.mock('@/modules/admin/api/connectionHealth', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/modules/admin/api/connectionHealth')>(),
  setTargetIntelligenceWeight: harness.setTargetIntelligenceWeight,
}))

const wrappers: VueWrapper[] = []

const account = (
  id: string,
  intelligenceWeight: number | null,
): AdminGroupAccount => ({
  id,
  name: `Account ${id}`,
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  schedulable: true,
  targetId: `sub2api:ws1:${id}`,
  probeAvailable: true,
  modelHealth: [],
  assignedPolicyIds: ['policy-1'],
  assignedPolicies: [{ policyId: 'policy-1', policyName: '正式策略', enabled: true, strategyMode: 'health_probe' }],
  hasAssignedPolicy: true,
  hasEnabledPolicy: true,
  hasEnabledProbePolicy: true,
  priorityManaged: true,
  probeModelsConfigured: true,
  productionSortOrder: 0,
  todayQuestionAnswerSubmitted: 0,
  todayQuestionAnswerCorrect: 0,
  intelligenceWeight,
} as AdminGroupAccount)

const group = (accounts: AdminGroupAccount[]): AdminGroupHealth => ({
  id: 'group-1',
  name: '测试分组',
  platform: 'sub2api',
  status: 'active',
  type: 'subscription',
  isExclusive: false,
  subscriptionType: '',
  multiplier: null,
  multiplierDisplay: '-',
  accountCount: accounts.length,
  monitoredAccountCount: accounts.length,
  healthSummary: {
    totalAccounts: accounts.length,
    probeableAccounts: accounts.length,
    unprobeableAccounts: 0,
    healthyModels: 0,
    degradedModels: 0,
    suspendedModels: 0,
    disabledModels: 0,
    unconfiguredModels: 0,
    lastProbeAt: null,
  },
  accounts,
})

const mountDetail = (accounts: AdminGroupAccount[]) => {
  const wrapper = mount(AdminGroupHealthDetail, {
    props: {
      group: group(accounts),
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: [],
      actionLoading: false,
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

const rowFor = (wrapper: VueWrapper, name: string): VueWrapper => {
  const row = wrapper.findAll('tbody > tr').find(candidate => candidate.text().includes(name))
  if (!row) throw new Error(`missing account row: ${name}`)
  return row
}

const editorFor = (wrapper: VueWrapper, name: string): VueWrapper =>
  rowFor(wrapper, name).get('[data-testid="account-intelligence-weight-editor"]')

const beginEditing = async (editor: VueWrapper) => {
  await editor.get('button[aria-label="编辑智商权重"]').trigger('click')
  return editor.get('[data-testid="intelligence-weight-input"]')
}

const saveDraft = async (editor: VueWrapper, value: string) => {
  const input = editor.get('[data-testid="intelligence-weight-input"]')
  await input.setValue(value)
  await editor.get('[data-testid="intelligence-weight-save"]').trigger('click')
}

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

const refreshPayload = (accountPayload: Record<string, unknown>) => ({
  status: 'success',
  runId: 'task6-contract-run',
  revision: 1,
  groups: [{
    id: 'group-1',
    accounts: [{ targetId: 'sub2api:ws1:account-1', ...accountPayload }],
  }],
  refresh: { state: 'success', sites: [] },
})

const refreshSSE = (accountPayload: Record<string, unknown>) => new Response(
  `event: terminal\ndata: ${JSON.stringify(refreshPayload(accountPayload))}\n\n`,
  { status: 200, headers: { 'Content-Type': 'text/event-stream; charset=utf-8' } },
)

beforeEach(() => {
  harness.setTargetIntelligenceWeight.mockReset()
})

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('admin group intelligence weight response contract', () => {
  it.each([null, 0, 1, 100])('accepts the required legal value %s from the list response', async (value) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([{
      id: 'group-1',
      accounts: [{ targetId: 'sub2api:ws1:account-1', intelligenceWeight: value }],
    }]), { status: 200 })))

    const result = await getConnectionHealthAdminGroups()

    expect(result[0]?.accounts[0]?.intelligenceWeight).toBe(value)
  })

  it.each([
    {},
    { intelligenceWeight: '9' },
    { intelligenceWeight: 1.5 },
    { intelligenceWeight: -1 },
    { intelligenceWeight: 101 },
  ])('rejects a missing, mistyped, fractional, or out-of-range list value: %j', async (accountPayload) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([{
      id: 'group-1',
      accounts: [{ targetId: 'sub2api:ws1:account-1', ...accountPayload }],
    }]), { status: 200 })))

    await expect(getConnectionHealthAdminGroups()).rejects.toBeInstanceOf(Error)
  })

  it.each([null, 0])('accepts legal value %s from the refresh JSON fallback', async (value) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
      JSON.stringify(refreshPayload({ intelligenceWeight: value })),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )))

    const result = await refreshConnectionHealthAdminGroups()

    expect(result.groups?.[0]?.accounts?.[0]?.intelligenceWeight).toBe(value)
  })

  it.each([
    {},
    { intelligenceWeight: '9' },
  ])('rejects a missing or invalid refresh JSON fallback value: %j', async (accountPayload) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
      JSON.stringify(refreshPayload(accountPayload)),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )))

    await expect(refreshConnectionHealthAdminGroups()).rejects.toBeInstanceOf(Error)
  })

  it.each([null, 0])('accepts legal value %s from an SSE success terminal', async (value) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(refreshSSE({ intelligenceWeight: value })))

    const result = await refreshConnectionHealthAdminGroups()

    expect(result.groups?.[0]?.accounts?.[0]?.intelligenceWeight).toBe(value)
  })

  it.each([
    {},
    { intelligenceWeight: '9' },
  ])('rejects a missing or invalid SSE success terminal value: %j', async (accountPayload) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(refreshSSE(accountPayload)))

    await expect(refreshConnectionHealthAdminGroups()).rejects.toBeInstanceOf(Error)
  })
})

describe('account intelligence weight editor', () => {
  it('renders null as unscored and preserves real zero and integer boundaries', () => {
    const wrapper = mountDetail([
      account('unscored', null),
      account('zero', 0),
      account('one', 1),
      account('hundred', 100),
    ])

    expect(editorFor(wrapper, 'Account unscored').get('[data-testid="intelligence-weight-current"]').text()).toBe('未评分')
    expect(editorFor(wrapper, 'Account zero').get('[data-testid="intelligence-weight-current"]').text()).toBe('0')
    expect(editorFor(wrapper, 'Account one').get('[data-testid="intelligence-weight-current"]').text()).toBe('1')
    expect(editorFor(wrapper, 'Account hundred').get('[data-testid="intelligence-weight-current"]').text()).toBe('100')
  })

  it.each(['', ' ', '-1', '101', '1.5', 'abc'])('rejects invalid draft %j without a request', async (draft) => {
    const wrapper = mountDetail([account('invalid', 7)])
    const editor = editorFor(wrapper, 'Account invalid')
    await beginEditing(editor)
    await saveDraft(editor, draft)

    expect(harness.setTargetIntelligenceWeight).not.toHaveBeenCalled()
    expect(editor.get('[data-testid="intelligence-weight-error"]').text()).toContain('0-100')
    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe('7')
  })

  it.each([0, 1, 100])('sends the exact valid integer %i and adopts only the authoritative response', async (value) => {
    const wrapper = mountDetail([account(`valid-${value}`, null)])
    const editor = editorFor(wrapper, `Account valid-${value}`)
    harness.setTargetIntelligenceWeight.mockResolvedValue({
      targetId: `sub2api:ws1:valid-${value}`,
      intelligenceWeight: value,
    })

    await beginEditing(editor)
    await saveDraft(editor, String(value))
    await flushPromises()

    expect(harness.setTargetIntelligenceWeight).toHaveBeenCalledWith(`sub2api:ws1:valid-${value}`, value)
    expect(wrapper.emitted('intelligence-weight-saved')).toEqual([[
      { targetId: `sub2api:ws1:valid-${value}`, intelligenceWeight: value },
    ]])
    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe(String(value))
  })

  it('blocks duplicate saves, keeps the old value in flight, and adopts the server result', async () => {
    const request = deferred<{ targetId: string; intelligenceWeight: number | null }>()
    harness.setTargetIntelligenceWeight.mockReturnValue(request.promise)
    const wrapper = mountDetail([account('concurrent', 5)])
    const editor = editorFor(wrapper, 'Account concurrent')
    await beginEditing(editor)
    await editor.get('[data-testid="intelligence-weight-input"]').setValue('6')
    const save = editor.get('[data-testid="intelligence-weight-save"]')

    await save.trigger('click')
    await save.trigger('click')

    expect(harness.setTargetIntelligenceWeight).toHaveBeenCalledTimes(1)
    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe('5')
    request.resolve({ targetId: 'sub2api:ws1:concurrent', intelligenceWeight: 8 })
    await flushPromises()

    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe('8')
    expect(wrapper.emitted('intelligence-weight-saved')).toEqual([[
      { targetId: 'sub2api:ws1:concurrent', intelligenceWeight: 8 },
    ]])
  })

  it('clears only through the explicit null action and does not repeat an already-null clear', async () => {
    harness.setTargetIntelligenceWeight.mockResolvedValue({
      targetId: 'sub2api:ws1:clear',
      intelligenceWeight: null,
    })
    const wrapper = mountDetail([account('clear', 9)])
    const editor = editorFor(wrapper, 'Account clear')
    await beginEditing(editor)
    await editor.get('[data-testid="intelligence-weight-clear"]').trigger('click')
    await flushPromises()

    expect(harness.setTargetIntelligenceWeight).toHaveBeenCalledWith('sub2api:ws1:clear', null)
    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe('未评分')
    await beginEditing(editor)
    expect(editor.get('[data-testid="intelligence-weight-clear"]').attributes('disabled')).toBeDefined()
  })

  it('keeps the old value and draft on request failure without forwarding a saved event', async () => {
    harness.setTargetIntelligenceWeight.mockRejectedValue(new Error('write failed'))
    const wrapper = mountDetail([account('failure', 7)])
    const editor = editorFor(wrapper, 'Account failure')
    await beginEditing(editor)
    await saveDraft(editor, '9')
    await flushPromises()

    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe('7')
    expect((editor.get('[data-testid="intelligence-weight-input"]').element as HTMLInputElement).value).toBe('9')
    expect(editor.get('[data-testid="intelligence-weight-error"]').text()).not.toBe('')
    expect(wrapper.emitted('intelligence-weight-saved')).toBeUndefined()
  })

  it.each([
    { targetId: 'sub2api:ws1:contract', intelligenceWeight: undefined },
    { targetId: 'sub2api:ws1:contract', intelligenceWeight: '9' },
    { targetId: 'sub2api:ws1:contract', intelligenceWeight: 101 },
  ])('surfaces an invalid success contract instead of treating it as unscored: %j', async (response) => {
    harness.setTargetIntelligenceWeight.mockResolvedValue(response)
    const wrapper = mountDetail([account('contract', 7)])
    const editor = editorFor(wrapper, 'Account contract')
    await beginEditing(editor)
    await saveDraft(editor, '9')
    await flushPromises()

    expect(editor.get('[data-testid="intelligence-weight-current"]').text()).toBe('7')
    expect(editor.get('[data-testid="intelligence-weight-error"]').text()).not.toBe('')
    expect(wrapper.emitted('intelligence-weight-saved')).toBeUndefined()
  })
})
