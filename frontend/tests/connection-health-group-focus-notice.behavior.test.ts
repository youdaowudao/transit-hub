// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import { connectionHealthPreferencesStorageKey } from '@/modules/admin/utils/connectionHealthPreferences'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConnectionHealthView from '@/modules/admin/views/ConnectionHealthView.vue'
vi.mock('@/modules/admin/api/mySites', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/modules/admin/api/mySites')>()), listRealConnections: harness.listRealConnections }))

const harness = vi.hoisted(() => ({
  route: null as any, replace: vi.fn(), refs: {} as Record<string, { value: any }>,
  currentAccount: null as any,
  activeWorkspaceScope: '',
  loadAll: vi.fn(),
  loadGroups: vi.fn(),
  loadAdminGroups: vi.fn(),
  refreshAdminGroups: vi.fn(),
  refreshAdminGroupsAutomatically: vi.fn(),
  loadEvents: vi.fn(),
  loadPolicies: vi.fn(),
  getPrioritySyncStatus: vi.fn(),
  listUpstreamSites: vi.fn(),
  listRealConnections: vi.fn(),
  cancelAdminGroupsRefresh: vi.fn(),
  setAdminGroupsWorkspace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => harness.route,
  useRouter: () => ({ replace: harness.replace }),
}))

vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  return {
    useDocumentVisibility: () => ref('hidden'),
    useIntervalFn: () => ({ pause: vi.fn(), resume: vi.fn() }),
  }
})

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  getPrioritySyncStatus: harness.getPrioritySyncStatus,
}))

vi.mock('@/modules/admin/api/upstream', () => ({
  listUpstreamSites: harness.listUpstreamSites,
}))

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'ws-a', displayName: '测试工作区' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})

vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  const { ref } = await import('vue')
  harness.refs = {
    overview: ref(null),
    groups: ref([]),
    adminGroups: ref([]), adminGroupsLoaded: ref(false),
    events: ref([]),
    policies: ref([]),
    isLoading: ref(false),
    isActionLoading: ref(false),
    errorKey: ref(''),
    terminalRefreshSummary: ref(null),
    refreshRunSnapshot: ref(null),
    refreshConflictNotice: ref(''),
    refreshConnectionState: ref('connected'),
  }
  return {
    ...original,
    useConnectionHealth: () => ({
      ...harness.refs,
      loadAll: harness.loadAll,
      loadGroups: harness.loadGroups,
      loadAdminGroups: harness.loadAdminGroups,
      refreshAdminGroups: harness.refreshAdminGroups,
      refreshAdminGroupsAutomatically: harness.refreshAdminGroupsAutomatically,
      loadEvents: harness.loadEvents,
      loadPolicies: harness.loadPolicies,
      cancelAdminGroupsRefresh: harness.cancelAdminGroupsRefresh,
      setAdminGroupsWorkspace: harness.setAdminGroupsWorkspace,
      removePolicy: vi.fn(async () => true),
      savePolicy: vi.fn(async () => true),
      updateTargetSchedulable: vi.fn(async () => true),
    }),
  }
})

const mountedWrappers: VueWrapper[] = []

const mountView = async (componentStubs: Record<string, unknown> = {}) => {
  const wrapper = mount(ConnectionHealthView, { attachTo: document.body,
    global: {
      stubs: {
        Button: { template: '<button v-bind="$attrs"><slot /></button>' },
        
        ConnectionHealthEventsDialog: true,
        GroupHealthSetupDrawer: true,
        ManualOneTimeProbeDialog: true,
        PolicyConfigDrawer: true,
        ProbePolicyListDialog: true,
        TargetPolicyAssignmentDialog: true,
        ...componentStubs,
      },
    },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const findButton = (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => item.text().trim() === label || item.text().includes(label))
  if (!button) throw new Error(`missing button: ${label}`)
  return button
}

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

beforeEach(() => {
  for (const refValue of Object.values(harness.refs)) refValue.value = Array.isArray(refValue.value) ? [] : null
  if (harness.refs.isLoading) harness.refs.isLoading.value = false
  if (harness.refs.isActionLoading) harness.refs.isActionLoading.value = false
  if (harness.refs.errorKey) harness.refs.errorKey.value = ''
  if (harness.refs.refreshConflictNotice) harness.refs.refreshConflictNotice.value = ''
  if (harness.refs.refreshConnectionState) harness.refs.refreshConnectionState.value = 'connected'
  harness.loadAll.mockReset().mockResolvedValue(undefined)
  harness.loadGroups.mockReset().mockResolvedValue(true)
  harness.loadAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroups.mockReset().mockResolvedValue(true)
  harness.refreshAdminGroupsAutomatically.mockReset().mockResolvedValue(true)
  harness.loadEvents.mockReset().mockResolvedValue(true)
  harness.loadPolicies.mockReset().mockResolvedValue(true)
  harness.getPrioritySyncStatus.mockReset().mockResolvedValue({ workspaceId: 'ws-a', status: 'success', failedCount: 0 })
  harness.listUpstreamSites.mockReset().mockResolvedValue([{ id: 'site-wait', name: '阻塞站点' }, { id: 'site-failed', name: '失败站点' }])
  harness.listRealConnections.mockReset().mockResolvedValue([])
  harness.currentAccount.value = { id: 'ws-a', displayName: '测试工作区' }
  harness.activeWorkspaceScope = 'ws-a'
  harness.route = reactive({ query: {} })
  harness.replace.mockReset().mockImplementation(async (value:any) => { harness.route.query = value.query })
  harness.refs.adminGroupsLoaded.value = true
  harness.refs.adminGroups.value = [group('11','甲组'),group('12','乙组')]
  Element.prototype.scrollIntoView = vi.fn()
  harness.cancelAdminGroupsRefresh.mockReset()
  harness.setAdminGroupsWorkspace.mockReset().mockImplementation((workspaceId: string) => {
    if (harness.activeWorkspaceScope === workspaceId) return
    harness.activeWorkspaceScope = workspaceId
    harness.refs.adminGroups.value = []; harness.refs.adminGroupsLoaded.value = false
    harness.refs.terminalRefreshSummary.value = null
    harness.refs.refreshRunSnapshot.value = null
    harness.refs.errorKey.value = ''
  })
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear(); delete (Element.prototype as any).scrollIntoView
})

function group(id:string,name:string) {return {
 id,name,platform:'openai',status:'active',type:'standard',isExclusive:false,subscriptionType:'',multiplier:1,multiplierDisplay:'1x',accountCount:2,monitoredAccountCount:2,
 healthSummary:{totalAccounts:2,probeableAccounts:2,unprobeableAccounts:0,healthyModels:0,degradedModels:0,suspendedModels:0,disabledModels:0,unconfiguredModels:0,lastProbeAt:null},
 accounts:['501','502'].map(a=>({id:a,name:'账号'+a,targetId:'sub2api:ws-a:'+a,platform:'openai',type:'subscription',status:'active',schedulable:true,priority:5,concurrency:2,loadFactor:2,accountTier:1,probeAvailable:true,modelHealth:[],hasEnabledProbePolicy:true,hasEnabledPolicy:true,hasAssignedPolicy:true})),
}}
const focus=(id='11',extra:Record<string,string>={})=>{harness.route.query={focusGroupId:id,...extra}}
const notice=(w:VueWrapper)=>w.find('[data-group-focus-notice]')
const detail=(w:VueWrapper)=>w.findComponent(AdminGroupHealthDetail)
const leftGroup=(w:VueWrapper,name:string)=>w.findAll('nav button').find(b=>b.text().includes(name))!
const marked=(w:VueWrapper)=>w.findAll('tr[data-focused-account]')
const accountNotices=(w:VueWrapper)=>w.findAll('[data-account-focus-notice]').map(item=>item.text()).join('\n')
const accountFocus={focusAccountIds:'501,502',focusWorkspace:'ws-a'}
const G1='要查看的分组「甲组」在本页被设为不显示。请点左侧‘管理分组显示’勾选它，再在列表里点它；这只改本机显示，不会修改主站。'
const G2='要查看的分组「甲组」被当前搜索或类型筛选挡住了。请清空搜索、把类型选为‘全部类型’，再在列表里点它。'
const G3=(name:string)=>`主站分组列表里没有找到「${name}」，可能已在主站删除或改名。请点右上角‘刷新’重新读取；仍没有时，到分组倍率检查这条对接。`
const G4='这次要查看的分组属于另一个工作区，当前工作区已经不同，所以没有定位。需要时请回到分组倍率重新点站点名称。'
const hide=()=>localStorage.setItem(connectionHealthPreferencesStorageKey('ws-a'),JSON.stringify({version:1,hiddenGroupIds:['11']}))
const reveal=async(w:VueWrapper)=>{await findButton(w,'管理分组显示').trigger('click');await flushPromises();const label=w.findAll('label').find(l=>l.text().includes('甲组'))!;await label.get('input').setValue(true);await flushPromises()}

describe('new group focus RED',()=>{
 it('N2 reports hidden groups with the live name and preserves preferences',async()=>{
  hide();focus('11',{focusGroupName:'旧名'});const w=await mountView();expect(notice(w).text()).toBe(G1);expect(harness.route.query).toEqual({});expect(detail(w).props('group').id).not.toBe('11')
  expect(JSON.parse(localStorage.getItem(connectionHealthPreferencesStorageKey('ws-a'))!).hiddenGroupIds).toEqual(['11'])
 })
 it('N3 reports a group blocked by the active search',async()=>{
  const w=await mountView();await w.get('input[type="search"]').setValue('不匹配');focus();await flushPromises();expect(notice(w).text()).toBe(G2);expect(harness.route.query).toEqual({})
 })
 it.each([['99','旧组','旧组'],['98','','分组编号 98']])('N4 reports absent group %s',async(id,name,display)=>{
  focus(id,name?{focusGroupName:name}:{});const w=await mountView();expect(notice(w).text()).toBe(G3(display));expect(harness.route.query).toEqual({});expect(detail(w).props('group').id).not.toBe('11')
 })
 it.each(['click','next-focus','workspace'])('N5 clears the notice on %s',async mode=>{
  if(mode==='click')hide();focus(mode==='click'?'11':'99');const w=await mountView();expect(notice(w).exists()).toBe(true)
  if(mode==='click')await leftGroup(w,'乙组').trigger('click')
  if(mode==='next-focus')focus('12')
  if(mode==='workspace')harness.currentAccount.value={id:'ws-b'}
  await flushPromises();expect(notice(w).exists()).toBe(false)
 })
 it('N6 reports a successfully loaded empty list',async()=>{
  harness.refs.adminGroups.value=[];focus('99');const w=await mountView();expect(notice(w).text()).toBe(G3('分组编号 99'));expect(harness.route.query).toEqual({})
 })
 it('N7 delivers IDs, marks rows and clears all four parameters',async()=>{
  focus('11',accountFocus);const w=await mountView();expect(detail(w).props('focusedAccountIds')).toEqual(['501','502']);expect(marked(w)).toHaveLength(2);expect(harness.route.query).toEqual({})
 })
 it('N8 preserves account marks when page clicks clear the group highlight',async()=>{
  focus('11',accountFocus);const w=await mountView();expect(leftGroup(w,'甲组').classes()).toContain('outline-primary/70');await w.trigger('click');expect(leftGroup(w,'甲组').classes()).not.toContain('outline-primary/70');expect(marked(w)).toHaveLength(2)
 })
 it('N9 clears targets when another group is selected and does not restore on return',async()=>{
  focus('11',accountFocus);const w=await mountView();expect(marked(w)).toHaveLength(2);await leftGroup(w,'乙组').trigger('click');expect(detail(w).props('focusedAccountIds')).toEqual([]);await leftGroup(w,'甲组').trigger('click');expect(detail(w).props('focusedAccountIds')).toEqual([])
 })
 it('N10 retains account targets while the hidden group is revealed and selected',async()=>{
  hide();focus('11',accountFocus);const w=await mountView();expect(notice(w).text()).toBe(G1);await reveal(w);await leftGroup(w,'甲组').trigger('click');await flushPromises();expect(marked(w)).toHaveLength(2);expect(notice(w).exists()).toBe(false)
 })
 it('N11 clears targets for an absent group and on workspace changes',async()=>{
  focus('11',accountFocus);const w=await mountView();expect(marked(w)).toHaveLength(2);focus('99',accountFocus);await flushPromises();expect(detail(w).props('focusedAccountIds')).toEqual([])
  focus('11',accountFocus);await flushPromises();expect(marked(w)).toHaveLength(2);harness.currentAccount.value={id:'ws-b'};await flushPromises();harness.refs.adminGroups.value=[group('11','甲组')];harness.refs.adminGroupsLoaded.value=true;await flushPromises();await leftGroup(w,'甲组').trigger('click');expect(detail(w).props('focusedAccountIds')).toEqual([])
 })
 it('N12 passes an empty ID array for the legacy upstream navigation',async()=>{focus();const w=await mountView();expect(detail(w).props('focusedAccountIds')).toEqual([])})
 it('N13 retains targets when the search filter is cleared',async()=>{
  const w=await mountView();const search=w.get('input[type="search"]');await search.setValue('不匹配');focus('11',accountFocus);await flushPromises();expect(notice(w).text()).toBe(G2);await search.setValue('');await leftGroup(w,'甲组').trigger('click');await flushPromises();expect(marked(w)).toHaveLength(2)
 })
 it('N14 trims and deduplicates account IDs',async()=>{focus('11',{focusAccountIds:' 501 ,,501, 502',focusWorkspace:'ws-a'});const w=await mountView();expect(detail(w).props('focusedAccountIds')).toEqual(['501','502'])})
 it('N15 rejects old-workspace targets even when group and account IDs collide',async()=>{
  harness.currentAccount.value={id:'ws-b'};harness.activeWorkspaceScope='ws-b';harness.refs.adminGroups.value.unshift(group('10','A组'));focus('11',accountFocus);const w=await mountView();expect(notice(w).text()).toBe(G4);expect(detail(w).props('group').id).not.toBe('11');expect(leftGroup(w,'甲组').classes()).not.toContain('outline-primary/70');expect(marked(w)).toHaveLength(0);expect(harness.route.query).toEqual({})
  focus('11',{...accountFocus,focusWorkspace:'ws-b'});await flushPromises();expect(notice(w).exists()).toBe(false);expect(marked(w)).toHaveLength(2)
 })
 it('N16 waits for a known workspace then for its newly loaded data',async()=>{
  harness.currentAccount.value=null;harness.activeWorkspaceScope='anonymous';harness.refs.adminGroups.value.unshift(group('10','A组'));focus('11',accountFocus);const w=await mountView();expect(notice(w).exists()).toBe(false);expect(detail(w).props('group').id).not.toBe('11');expect(harness.route.query.focusWorkspace).toBe('ws-a')
  harness.currentAccount.value={id:'ws-a'};await flushPromises();expect(harness.route.query.focusGroupId).toBe('11');harness.refs.adminGroups.value=[group('11','甲组')];harness.refs.adminGroupsLoaded.value=true;await flushPromises();expect(marked(w)).toHaveLength(2);expect(harness.route.query).toEqual({})
 })
 it('N17 uses existing reads for full names without appended websites or mixed identities',async()=>{
  harness.refs.adminGroups.value[1].accounts.push({...group('12','乙组').accounts[0],id:'447',name:'当前完整账号甲',targetId:'sub2api:ws-a:447'})
  harness.listUpstreamSites.mockResolvedValue([{id:'site-a',name:'当前网站甲'}])
  harness.listRealConnections.mockResolvedValue([
   {adminAccountId:'447',adminAccountName:'其他平台错误账号',adminPlatform:'newapi',upstreamSiteId:'other',siteName:'其他平台网站'},
   {adminAccountId:'448',adminAccountName:'完整账号乙',adminPlatform:'',upstreamSiteId:'site-c',siteName:'网站丙',status:'missing'},
   {adminAccountId:' 447 ',adminAccountName:'旧账号甲',adminPlatform:'sub2api',upstreamSiteId:'site-a',siteName:'旧网站甲'},
   {adminAccountId:'447',adminAccountName:'旧账号甲',adminPlatform:'sub2api',upstreamSiteId:'site-a',siteName:'旧网站甲'},
   {adminAccountId:'447',adminAccountName:'旧账号甲',adminPlatform:'sub2api',upstreamSiteId:'site-b',siteName:'网站乙'},
  ])
  focus('11',{focusAccountIds:'448,447',focusWorkspace:'ws-a'});const w=await mountView()
  expect(accountNotices(w)).toContain('账号「完整账号乙」、账号「当前完整账号甲」')
  for(const text of ['旧账号甲','旧网站甲','其他平台错误账号','其他平台网站','账号编号'])expect(accountNotices(w)).not.toContain(text)
  expect(accountNotices(w)).not.toContain('（网站');expect(accountNotices(w)).not.toContain('网站丙')
  expect(harness.listRealConnections).toHaveBeenCalledTimes(1);expect(harness.listUpstreamSites).toHaveBeenCalledTimes(2)
  expect(harness.route.query).toEqual({});expect(marked(w)).toHaveLength(0)
 })
 it('N18 keeps connection names without a website suffix when the website list cannot be read',async()=>{
  harness.listUpstreamSites.mockRejectedValue(new Error('admin.upstream.errors.network'))
  harness.listRealConnections.mockResolvedValue([{adminAccountId:'447',adminAccountName:'完整账号甲',siteName:'对接保存的网站',upstreamSiteId:'site-a',adminPlatform:'sub2api'}])
  focus('11',{focusAccountIds:'447',focusWorkspace:'ws-a'});const w=await mountView()
  expect(accountNotices(w)).toContain('账号「完整账号甲」');expect(accountNotices(w)).not.toContain('名称暂未读到')
  expect(w.find('[data-bound-site-rate-status]').exists()).toBe(true)
 })
 it('N19 explains an unknown name on connection failure and retries through the existing refresh button',async()=>{
  harness.listRealConnections.mockRejectedValue(new Error('admin.mySites.errors.network'))
  focus('11',{focusAccountIds:'447',focusWorkspace:'ws-a'});const w=await mountView()
  expect(accountNotices(w)).toContain('账号（名称暂未读到）');expect(accountNotices(w)).not.toContain('447')
  harness.listRealConnections.mockResolvedValue([{adminAccountId:'447',adminAccountName:'完整账号甲',siteName:'网站甲',upstreamSiteId:'site-a'}])
  await findButton(w,'刷新').trigger('click');await flushPromises()
  expect(accountNotices(w)).toContain('账号「完整账号甲」');expect(accountNotices(w)).not.toContain('名称暂未读到')
  expect(harness.listRealConnections).toHaveBeenCalledTimes(2)
  harness.listRealConnections.mockRejectedValue(new Error('admin.mySites.errors.network'))
  await findButton(w,'刷新').trigger('click');await flushPromises()
  expect(accountNotices(w)).toContain('名称暂未读到');expect(accountNotices(w)).not.toContain('完整账号甲');expect(accountNotices(w)).not.toContain('网站甲')
 })
 it('N20 discards a late label read after a newer refresh in the same workspace',async()=>{
  const old=deferred<any[]>();harness.listRealConnections.mockImplementationOnce(()=>old.promise)
  focus('11',{focusAccountIds:'447',focusWorkspace:'ws-a'});const w=await mountView();expect(accountNotices(w)).toContain('名称暂未读到')
  harness.listRealConnections.mockResolvedValue([{adminAccountId:'447',adminAccountName:'新读取账号',siteName:'新读取网站',upstreamSiteId:'site-a'}])
  await findButton(w,'刷新').trigger('click');await flushPromises();expect(accountNotices(w)).toContain('新读取账号')
  old.resolve([{adminAccountId:'447',adminAccountName:'迟到旧账号',siteName:'迟到旧网站',upstreamSiteId:'site-old'}]);await flushPromises()
  expect(accountNotices(w)).toContain('新读取账号');expect(accountNotices(w)).not.toContain('迟到旧');expect(harness.listRealConnections).toHaveBeenCalledTimes(2)
 })
 it('N21 clears labels on workspace change and rejects late same-ID names from the previous workspace',async()=>{
  const old=deferred<any[]>();harness.listRealConnections.mockImplementationOnce(()=>old.promise)
  focus('11',{focusAccountIds:'447',focusWorkspace:'ws-a'});const w=await mountView()
  harness.listRealConnections.mockResolvedValue([{adminAccountId:'447',adminAccountName:'乙工作区账号',siteName:'乙工作区网站',upstreamSiteId:'site-b'}])
  harness.currentAccount.value={id:'ws-b'};await flushPromises()
  harness.refs.adminGroups.value=[group('11','甲组')];harness.refs.adminGroupsLoaded.value=true;focus('11',{focusAccountIds:'447',focusWorkspace:'ws-b'});await flushPromises()
  expect(accountNotices(w)).toContain('乙工作区账号')
  old.resolve([{adminAccountId:'447',adminAccountName:'甲工作区旧账号',siteName:'甲工作区旧网站',upstreamSiteId:'site-a'}]);await flushPromises()
  expect(accountNotices(w)).toContain('乙工作区账号');expect(accountNotices(w)).not.toContain('甲工作区旧');expect(harness.listRealConnections).toHaveBeenCalledTimes(2)
 })
})
describe('old behavior preserved',()=>{
 it('N1 N12 selects, highlights and consumes legacy navigation without account marks',async()=>{focus();const w=await mountView();expect(detail(w).props('group').id).toBe('11');expect(leftGroup(w,'甲组').classes()).toContain('outline-primary/70');expect(harness.route.query).toEqual({});expect(notice(w).exists()).toBe(false);expect(marked(w)).toHaveLength(0);expect(w.text()).not.toContain('要找的账号')})
 it.each(['pending','error'])('N6 retains query while data is %s then selects on success',async mode=>{harness.refs.adminGroups.value=[];harness.refs.adminGroupsLoaded.value=false;if(mode==='error')harness.refs.errorKey.value='admin.connectionHealth.errors.network';focus();const w=await mountView();expect(notice(w).exists()).toBe(false);expect(harness.route.query.focusGroupId).toBe('11');harness.refs.adminGroups.value=[group('11','甲组')];harness.refs.adminGroupsLoaded.value=true;await flushPromises();expect(detail(w).props('group').id).toBe('11');expect(harness.route.query).toEqual({})})
 it('N16 processes legacy navigation without requiring a known workspace',async()=>{harness.currentAccount.value=null;harness.activeWorkspaceScope='anonymous';focus();const w=await mountView();expect(detail(w).props('group').id).toBe('11');expect(harness.route.query).toEqual({})})
})
