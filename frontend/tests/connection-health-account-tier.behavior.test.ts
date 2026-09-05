// @vitest-environment jsdom
import { defineComponent, h, ref } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

type TierResult = { targetId: string; accountTier: 1 | 2 }
const targetId = 'sub2api:ws1:tier-fixture'
const account = (): AdminGroupAccount => ({
  id: 'tier-fixture', name: 'Task 8 fixture', platform: 'openai', type: 'subscription',
  status: 'disabled', schedulable: false, priority: 7, targetId, probeAvailable: true,
  modelHealth: [], assignedPolicyIds: [], assignedPolicies: [], hasAssignedPolicy: false,
  hasEnabledPolicy: false, hasEnabledProbePolicy: false, priorityManaged: false,
  priorityConflict: true, prioritySyncBlocked: true, prioritySyncBlockReason: 'manual_priority',
  probeModelsConfigured: false, productionSortOrder: 0,
  todayQuestionAnswerSubmitted: 10, todayQuestionAnswerCorrect: 7,
})
const groups = (): AdminGroupHealth[] => ['one', 'two'].map(id => ({
  id, name: id, platform: 'openai', status: 'active', type: 'subscription', isExclusive: false,
  subscriptionType: '', multiplier: null, multiplierDisplay: '-', accountCount: 1, monitoredAccountCount: 0,
  healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0,
    degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
  accounts: [account()],
}))
const wrappers: VueWrapper[] = []
const service = useConnectionHealth() as ReturnType<typeof useConnectionHealth> & {
  applyAccountTier: (result: TierResult) => void
}
let stored: 1 | 2 | undefined
let failSave = false
let invalidResponse = false
let saveRequests: Array<{ url: string; body: unknown }> = []
let deferGroups: (() => Promise<Response>) | undefined
let deferSave: (() => Promise<Response>) | undefined
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const payload = () => groups().map(group => ({ ...group, accounts: group.accounts.map(row => ({ ...row, ...(stored ? { accountTier: stored } : {}) })) }))
const stats = { requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 }, reviews: { unreviewed: 0, correct: 0, incorrect: 0 }, byModel: [] }

beforeEach(() => {
  service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('ws1')
  stored = undefined; failSave = false; invalidResponse = false; saveRequests = []; deferGroups = undefined; deferSave = undefined
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = decodeURIComponent(String(input))
    if (url.endsWith('/admin-groups')) return deferGroups ? deferGroups() : json(payload())
    if (url.endsWith('/tier')) {
      if (init?.method === 'PUT') {
        const body = JSON.parse(String(init.body)); saveRequests.push({ url, body })
        if (deferSave) return deferSave()
        if (failSave) return json({ message: 'admin.connectionHealth.errors.request' }, 500)
        stored = body.accountTier
        return json({ targetId: invalidResponse ? 'sub2api:ws2:other' : targetId, accountTier: stored })
      }
      return json({ targetId, accountTier: stored ?? 2 })
    }
    if (url.includes('/question-answers/batches/latest')) return json({ batchId: '', records: [], active: false, runningCount: 0, completedCount: 0, submittedCount: 0, stats })
    if (url.includes('/question-answers/history')) return json({ records: [], page: 1, pageSize: 20, totalItems: 0, totalPages: 0, stats, todayStats: stats })
    if (url.endsWith('/test-questions')) return json([{ id: 'tier-question', name: 'Tier question', body: 'Fixture question', keywords: [], enabled: true, isDefault: true, createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' }])
    if (url.endsWith('/models')) return json([{ id: 'tier-model', name: 'Tier model' }])
    throw new Error(`unexpected network request: ${url}`)
  }))
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  service.cancelAdminGroupsRefresh(); service.setAdminGroupsWorkspace('')
  vi.unstubAllGlobals(); document.body.innerHTML = ''
})

const mountRows = async () => {
  await service.loadAdminGroups()
  const wrapper = mount(defineComponent({ setup: () => () => h('div', service.adminGroups.value.map(group => h(AdminGroupHealthDetail, {
    key: group.id, group, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false,
    onTierSaved: (result: TierResult) => service.applyAccountTier(result),
  }))) }))
  wrappers.push(wrapper); await flushPromises(); return wrapper
}
const editors = (wrapper: VueWrapper) => wrapper.findAll('[data-testid="account-tier-editor"]')
const edit = async (wrapper: VueWrapper, value: 1 | 2, index = 0) => {
  const editor = editors(wrapper)[index]!
  await editor.get('button[aria-label="编辑账号层级"]').trigger('click')
  await editor.get('select[aria-label="账号层级"]').setValue(String(value))
  return editor
}
const save = async (wrapper: VueWrapper, value: 1 | 2, index = 0) => {
  const editor = await edit(wrapper, value, index)
  await editor.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
}
const tiers = () => service.adminGroups.value.map(group => (group.accounts[0] as AdminGroupAccount & { accountTier?: number }).accountTier ?? 2)
const withoutTiers = () => JSON.parse(JSON.stringify(service.adminGroups.value, (key, value) => key === 'accountTier' ? undefined : value))

describe('account-global tier editing', () => {
  it('mounts the real list with default second tier and retains existing account controls and statuses', async () => {
    const wrapper = await mountRows()
    expect(editors(wrapper)).toHaveLength(2)
    for (const editor of editors(wrapper)) { expect(editor.text()).toContain('第二层'); expect(editor.text()).not.toContain('第一层') }
    expect(wrapper.text()).toContain('今日正确率')
    expect(wrapper.text()).toContain('7/10')
    expect(wrapper.findAll('button[aria-label="编辑账号层级"]')).toHaveLength(2)
    expect(saveRequests).toEqual([])
  })

  it('saves both directions, updates every group, and retains tier after authoritative refresh and remount', async () => {
    let wrapper = await mountRows(); const before = withoutTiers()
    for (const tier of [1, 2, 1] as const) {
      await save(wrapper, tier)
      expect(tiers()).toEqual([tier, tier]); expect(withoutTiers()).toEqual(before)
      for (const editor of editors(wrapper)) {
        expect(editor.text()).toContain(tier === 1 ? '第一层' : '第二层')
        expect(editor.find('select').exists()).toBe(false)
      }
      await service.loadAdminGroups(); await flushPromises(); expect(tiers()).toEqual([tier, tier])
    }
    wrapper.unmount(); wrappers.splice(wrappers.indexOf(wrapper), 1)
    wrapper = await mountRows()
    expect(editors(wrapper).every(editor => editor.text().includes('第一层'))).toBe(true)
    expect(saveRequests.map(request => request.body)).toEqual([{ accountTier: 1 }, { accountTier: 2 }, { accountTier: 1 }])
    expect(saveRequests.every(request => request.url.endsWith(`/targets/${targetId}/tier`))).toBe(true)
  })

  it('cancel makes no write; failed or mismatched save keeps original state and a retryable draft', async () => {
    const wrapper = await mountRows(); const before = withoutTiers()
    let editor = await edit(wrapper, 1)
    await editor.get('button[aria-label="取消编辑层级"]').trigger('click')
    expect(saveRequests).toHaveLength(0); expect(tiers()).toEqual([2, 2])
    failSave = true; await save(wrapper, 1)
    editor = editors(wrapper)[0]!
    expect(editor.find('[role="alert"]').exists()).toBe(true)
    expect((editor.get('select').element as HTMLSelectElement).value).toBe('1')
    expect(tiers()).toEqual([2, 2]); expect(withoutTiers()).toEqual(before)
    failSave = false; invalidResponse = true
    await editor.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
    expect(tiers()).toEqual([2, 2]); expect(editor.find('[role="alert"]').exists()).toBe(true)
    invalidResponse = false
    await editor.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
    expect(tiers()).toEqual([1, 1]); expect(editor.find('[role="alert"]').exists()).toBe(false)
  })

  it('an older list response cannot overwrite a tier saved while that read was in flight', async () => {
    const wrapper = await mountRows()
    let release!: (response: Response) => void
    const stale = payload(); deferGroups = () => new Promise(resolve => { release = resolve })
    const read = service.loadAdminGroups({ silent: true })
    await save(wrapper, 1); expect(tiers()).toEqual([1, 1])
    release(json(stale)); await read; await flushPromises()
    expect(tiers()).toEqual([1, 1])
    expect(editors(wrapper).every(editor => editor.text().includes('第一层'))).toBe(true)
  })

  it('serializes the same target across two real editors so a late first save cannot overwrite the final second tier', async () => {
    const wrapper = await mountRows()
    const first = await edit(wrapper, 1, 0)
    const second = await edit(wrapper, 2, 1)
    let release!: (response: Response) => void
    deferSave = () => new Promise(resolve => { release = resolve })
    await first.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
    try {
      expect(second.get('button[aria-label="保存账号层级"]').attributes('disabled')).toBeDefined()
      await second.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
      expect(saveRequests.map(request => request.body)).toEqual([{ accountTier: 1 }])
      expect(tiers()).toEqual([2, 2])
    } finally {
      stored = 1
      release(json({ targetId, accountTier: 1 })); await flushPromises()
      deferSave = undefined
    }
    expect(tiers()).toEqual([1, 1])
    expect(second.get('button[aria-label="保存账号层级"]').attributes('disabled')).toBeUndefined()
    await second.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
    await service.loadAdminGroups(); await flushPromises()
    expect(saveRequests.map(request => request.body)).toEqual([{ accountTier: 1 }, { accountTier: 2 }])
    expect(tiers()).toEqual([2, 2]); expect(stored).toBe(2)
    expect(editors(wrapper).every(editor => editor.text().includes('第二层') && !editor.text().includes('第一层'))).toBe(true)
  })

  it('changing workspace and unmounting an in-flight editor never applies the old save to the new workspace', async () => {
    const wrapper = await mountRows(); let release!: (response: Response) => void
    deferSave = () => new Promise(resolve => { release = resolve })
    const editor = await edit(wrapper, 1)
    await editor.get('button[aria-label="保存账号层级"]').trigger('click'); await flushPromises()
    expect(editor.get('button[aria-label="保存账号层级"]').attributes('disabled')).toBeDefined()
    wrapper.unmount(); wrappers.splice(wrappers.indexOf(wrapper), 1)
    service.setAdminGroupsWorkspace('ws2')
    service.adminGroups.value = [{ ...groups()[0]!, accounts: [{ ...account(), targetId: 'sub2api:ws2:tier-fixture' }] }]
    release(json({ targetId, accountTier: 1 })); await flushPromises()
    expect(tiers()).toEqual([2])
    expect(service.adminGroups.value[0]?.accounts[0]?.targetId).toBe('sub2api:ws2:tier-fixture')
  })

  it('mounts the real account detail with the same editor and saves both directions without losing question-answer modes', async () => {
    stored = 1; await service.loadAdminGroups()
    const open = ref(false)
    const wrapper = mount(defineComponent({ setup: () => () => h(ManualOneTimeProbeDialog, {
      open: open.value,
      target: { targetId, accountName: 'Task 8 fixture', platform: 'openai', type: 'subscription', status: 'disabled', groupName: 'one', formalModels: [], accountTier: tiers()[0] as 1 | 2 },
      onTierSaved: (result: TierResult) => service.applyAccountTier(result),
    }) }), { global: { stubs: { Teleport: true, Transition: false } } })
    wrappers.push(wrapper); open.value = true; await flushPromises()
    expect(editors(wrapper)).toHaveLength(1)
    expect(editors(wrapper)[0]!.text()).toContain('第一层')
    for (const tier of [2, 1] as const) { await save(wrapper, tier); expect(tiers()).toEqual([tier, tier]) }
    expect(wrapper.text()).toContain('问答测试')
    expect(wrapper.text()).not.toContain('智商权重')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(saveRequests).toHaveLength(2)
  })
})
