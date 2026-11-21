// @vitest-environment jsdom
// 2026-11-21：模型检测「新建账号检测」的分组/账号名单由分组健康页发布，屏蔽清单里的账号不得进入。
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
import { chanshuiCatalog, clearChanshuiCatalogForSession } from '@/modules/admin/composables/chanshuiCatalog'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'
import { connectionHealthPreferencesStorageKey, createDefaultConnectionHealthPreferences } from '@/modules/admin/utils/connectionHealthPreferences'

vi.mock('@/modules/admin/api/mySites', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()), listRealConnections: vi.fn(async () => []) }))

const harness = vi.hoisted(() => ({
  refs: {} as Record<string, { value: any }>,
  currentAccount: null as { value: { id: string; displayName: string; platform: string } | null } | null,
  listHiddenAccounts: vi.fn(),
  hideAccount: vi.fn(),
  unhideAccount: vi.fn(),
}))

vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn(async () => undefined) }) }))
vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  return { useDocumentVisibility: () => ref('hidden'), useIntervalFn: () => ({ pause: vi.fn(), resume: vi.fn() }) }
})
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'ws1', displayName: 'Workspace One', platform: 'sub2api' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})
vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  const { ref } = await import('vue')
  const { chanshuiCatalogSession } = await import('@/modules/admin/composables/chanshuiCatalog')
  harness.refs = {
    overview: ref(null), groups: ref([]), adminGroups: ref([]), events: ref([]), policies: ref([]),
    isLoading: ref(false), isActionLoading: ref(false), errorKey: ref(''), terminalRefreshSummary: ref(null),
    refreshRunSnapshot: ref(null), refreshConflictNotice: ref(''), refreshConnectionState: ref('connected'),
    adminGroupsLoaded: ref(true), adminGroupsAcceptedRevision: ref(1),
  }
  const ok = vi.fn(async () => true)
  return {
    ...actual,
    useConnectionHealth: () => ({
      ...harness.refs,
      getAdminGroupsCatalogScope: () => ({ workspaceId: 'ws1', session: chanshuiCatalogSession() }),
      discoverModels: vi.fn(async () => ({ models: [] })), runManualProbeOnce: vi.fn(), manualProbeTarget: vi.fn(),
      loadAll: ok, loadGroups: ok, loadAdminGroups: ok, refreshAdminGroups: ok, refreshAdminGroupsAutomatically: ok,
      loadEvents: ok, loadPolicies: ok, cancelAdminGroupsRefresh: vi.fn(), setAdminGroupsWorkspace: vi.fn(),
      removePolicy: ok, savePolicy: ok, updateTargetSchedulable: ok,
    }),
  }
})
vi.mock('@/modules/admin/api/connectionHealth', async original => ({
  ...await original<typeof import('@/modules/admin/api/connectionHealth')>(),
  getModelControlAutoSettings: async () => ({ enabled: false }),
  getModelControlSettings: async () => ({ minAccuracyPercent: 50, minJudgedAnswers: 3, version: 0 }),
  getLatestQuestionAnswerBatch: async () => null,
  getPrioritySyncStatus: async () => ({ workspaceId: 'ws1', status: 'success', failedCount: 0 }),
  listTestQuestions: async () => [],
  listHiddenAccounts: harness.listHiddenAccounts,
  hideAccount: harness.hideAccount,
  unhideAccount: harness.unhideAccount,
}))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: vi.fn(async () => []) }))

const account = (targetId: string): AdminGroupAccount => ({
  id: targetId.split(':').at(-1), name: `Account ${targetId}`, platform: 'sub2api', type: 'subscription', status: 'active',
  targetId, probeAvailable: true, modelHealth: [],
} as AdminGroupAccount)
const group = (id: string, accounts: AdminGroupAccount[]): AdminGroupHealth => ({
  id, name: `Group ${id}`, platform: 'sub2api', status: 'active', type: 'subscription', isExclusive: false,
  subscriptionType: 'standard', multiplier: 1, multiplierDisplay: '1.00', accountCount: accounts.length,
  healthSummary: { totalAccounts: accounts.length, probeableAccounts: accounts.length, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
  accounts,
} as unknown as AdminGroupHealth)
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const hiddenList = (ids: string[]) => ({ workspaceId: 'ws1', items: ids.map(targetId => ({ targetId, accountName: targetId, hiddenAt: '2026-11-21T00:00:00Z' })) })

const image = 'sub2api:ws1:136', text = 'sub2api:ws1:7'
const groups = [group('10', [account(image), account(text)]), group('8', [account(image)])]
const catalogAccounts = () => chanshuiCatalog.value?.groups.map(item => [item.id, item.accounts.map(entry => entry.targetId)])
const mounted: VueWrapper[] = []
const mountView = async () => {
  window.localStorage.setItem(connectionHealthPreferencesStorageKey('ws1'), JSON.stringify(createDefaultConnectionHealthPreferences()))
  harness.refs.adminGroups.value = groups
  const wrapper = mount(ConnectionHealthView, {
    attachTo: document.body,
    global: { stubs: { Teleport: true, Transition: false, AdminGroupHealthDetail: true, ConnectionHealthEventsDialog: true, GroupHealthSetupDrawer: true, ManualOneTimeProbeDialog: true, PolicyConfigDrawer: true, ProbePolicyListDialog: true, TargetPolicyAssignmentDialog: true, QuestionAnswerModelControlDrawer: true, QuestionAnswerBatchDrawer: true } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  clearChanshuiCatalogForSession()
  window.localStorage.clear()
  harness.listHiddenAccounts.mockReset().mockResolvedValue(hiddenList([]))
  harness.hideAccount.mockReset()
  harness.unhideAccount.mockReset()
})
afterEach(() => {
  vi.restoreAllMocks()
  for (const wrapper of mounted.splice(0)) wrapper.unmount()
  clearChanshuiCatalogForSession()
  document.body.innerHTML = ''
  window.localStorage.clear()
})

describe('模型检测名单与屏蔽清单', () => {
  it('读到屏蔽清单后名单扣掉被屏蔽账号，取消屏蔽后重新出现；清单返回前与账号表一样暂时不扣', async () => {
    const first = deferred<ReturnType<typeof hiddenList>>()
    harness.listHiddenAccounts.mockReturnValueOnce(first.promise)
    await mountView()
    expect(catalogAccounts()).toEqual([['10', [image, text]], ['8', [image]]])

    first.resolve(hiddenList([image]))
    await flushPromises()
    expect(catalogAccounts()).toEqual([['10', [text]], ['8', []]])

    harness.unhideAccount.mockResolvedValue({ kind: 'saved', targetId: image, hidden: false })
    harness.listHiddenAccounts.mockResolvedValue(hiddenList([]))
    const wrapper = mounted[0]
    await wrapper.findAll('button').find(item => item.text().includes('管理分组显示'))!.trigger('click')
    await flushPromises()
    const unhide = wrapper.get('[data-testid="hidden-accounts-panel"]').findAll('button').find(item => item.text() === '取消屏蔽')!
    await unhide.trigger('click')
    await flushPromises()
    expect(catalogAccounts()).toEqual([['10', [image, text]], ['8', [image]]])
  })

  it('在分组健康屏蔽账号后，模型检测名单立即去掉它', async () => {
    await mountView()
    expect(catalogAccounts()).toEqual([['10', [image, text]], ['8', [image]]])
    harness.hideAccount.mockResolvedValueOnce({ kind: 'saved', targetId: image, hidden: true, hiddenAt: '2026-11-21T00:00:00Z' })
    const reread = deferred<ReturnType<typeof hiddenList>>()
    harness.listHiddenAccounts.mockReturnValueOnce(reread.promise)
    mounted[0].findComponent({ name: 'AdminGroupHealthDetail' }).vm.$emit('hide-account', account(image))
    await flushPromises()
    expect(harness.listHiddenAccounts).toHaveBeenCalledTimes(2)
    expect(catalogAccounts()).toEqual([['10', [text]], ['8', []]])
    reread.resolve(hiddenList([image]))
    await flushPromises()
    expect(catalogAccounts()).toEqual([['10', [text]], ['8', []]])
  })

  it('屏蔽清单读取失败时沿用账号表的做法照常发布名单', async () => {
    harness.listHiddenAccounts.mockRejectedValueOnce(new Error('offline'))
    await mountView()
    expect(catalogAccounts()).toEqual([['10', [image, text]], ['8', [image]]])
  })
})
