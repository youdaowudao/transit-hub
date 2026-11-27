// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import MainAccountCheckSettings from '@/modules/admin/components/settings/MainAccountCheckSettings.vue'
const h = vi.hoisted(()=>({account:null as any, push:vi.fn(), list:vi.fn(), check:vi.fn(), groups:vi.fn(), sites:vi.fn(), stream:vi.fn(), readSettings:vi.fn(), saveSettings:vi.fn()}))
vi.mock('vue-router',()=>({useRouter:()=>({push:h.push})}))
vi.mock('@/modules/admin/composables/useAdminAccounts',async()=>{const {ref}=await import('vue');h.account=ref(null);return {useAdminAccounts:()=>({currentAccount:h.account})}})
vi.mock('@/modules/admin/api/settings',()=>({getStrategySettings:async()=>({enableRefreshInterval:false,refreshInterval:60}),readMainAccountCheckSettings:h.readSettings,saveMainAccountCheckSettings:h.saveSettings}))
vi.mock('@/modules/admin/api/connectionHealth',()=>({getConnectionHealthAdminGroups:h.groups}))
vi.mock('@/modules/admin/api/mySites',()=>({listRealConnections:h.list,checkScopedRealConnections:h.check,ScopedCheckApiError:class extends Error{}}))
vi.mock('@/modules/admin/api/upstream',()=>({listUpstreamSites:h.sites,streamSyncAllUpstreamSites:h.stream,syncAllUpstreamSites:vi.fn(),syncUpstreamSite:vi.fn(),createUpstreamSite:vi.fn(),updateUpstreamSite:vi.fn(),updateUpstreamSiteEnabled:vi.fn(),updateSiteSettings:vi.fn(),removeUpstreamSite:vi.fn()}))
const scope={workspaceAdminAccountId:'ws',workspaceCreatedAt:'2026-11-27T01:00:00Z'}
const group={id:'1',name:'验收分组',platform:'openai',multiplier:1,multiplierDisplay:'1x'}
const metric={value:10,display:'10'}
const site={id:'site',name:'验收站点',baseUrl:'https://fixture.invalid',platform:'sub2api',requestedPlatform:'sub2api',account:'fixture',rechargeRate:1,enabled:false,remark:'',status:'connected',errorKey:null,settings:{balanceThreshold:null},lastSyncedAt:null,metrics:{balance:metric,todayConsume:metric,historyRecharge:metric,group,groups:[group]}}
const connection=(status='active',id='101')=>({id:'c-'+id,upstreamSiteId:'site',upstreamGroupId:'1',upstreamGroupName:'验收分组',adminAccountId:id,adminPlatform:'sub2api',status,ownGroupIds:['10'],ownGroupNames:['旧组']})
const health=(ids:string[])=>[{id:'10',name:'旧组',accounts:ids.map(id=>({id,name:'Account'})),hasEnabledProbePolicy:true}]
const result=(status='missing')=>({workspaceAdminAccountId:'ws',total:1,confirmedConnectionIds:['c-101'],confirmedStatuses:{'c-101':status},unconfirmedConnectionIds:[],changedConnectionIds:[],skippedConnectionIds:[],issues:[],storageUncertain:false})
const wrappers:VueWrapper[]=[]
const mountUpstream=async()=>{const w=mount(UpstreamView,{global:{stubs:{Teleport:true,Tooltip:{template:'<span><slot /></span>'}}}});wrappers.push(w);await flushPromises();return w}
const open=async(w:VueWrapper)=>{await w.findAll('button').find(b=>b.text().includes('可用分组'))!.trigger('click');await flushPromises()}
const card=(w:VueWrapper)=>w.get('[role="dialog"]').findAll('button').find(b=>b.text().includes('验收分组'))!
const settings=()=>({...scope,enabled:true,times:['09:00','13:00','18:00','22:00'],recentScheduled:null})
beforeEach(()=>{vi.resetAllMocks();h.account.value={id:'ws',createdAt:scope.workspaceCreatedAt,platform:'sub2api'};h.list.mockResolvedValue([connection()]);h.groups.mockResolvedValue(health(['101']));h.sites.mockResolvedValue([site]);h.stream.mockResolvedValue(undefined);h.readSettings.mockResolvedValue(settings());localStorage.clear()})
afterEach(()=>{wrappers.splice(0).forEach(w=>w.unmount());localStorage.clear();document.body.innerHTML=''})
describe('手动主站核对与原分组入口',()=>{
  it.each(['card','list'] as const)('已确认删除后 %s 入口可去原分组，不调用原上游同步',async(layout)=>{
    const w=await mountUpstream();if(layout==='list')await w.get('button[aria-label="列表模式"]').trigger('click');await open(w)
    expect(card(w).text()).not.toContain('已删除');const streamCount=h.stream.mock.calls.length
    h.check.mockImplementationOnce(async()=>{h.list.mockResolvedValue([connection('missing')]);return result()})
    await w.get('[data-testid="check-site-main-accounts"]').trigger('click');await flushPromises()
    expect(h.check).toHaveBeenCalledWith({...scope,mode:'inventory',siteId:'site'},expect.any(AbortSignal));expect(h.stream.mock.calls.length).toBe(streamCount)
    expect(card(w).text()).toContain('主站账号已删除 · 可重新对接');expect(card(w).text()).not.toContain('已调度');expect(w.text()).toContain('1 条已删除')
    await card(w).trigger('click');await flushPromises();expect(h.push).toHaveBeenCalledWith({name:'AdminGroupRates',query:{siteId:'site',focusGroup:'验收分组',focusGroupId:'1'}})
  })
  it('全站按钮及混合旧失效/新有效连接只定位新账号',async()=>{
    const w=await mountUpstream();h.check.mockResolvedValueOnce(result('active'));await w.get('[data-testid="check-all-main-accounts"]').trigger('click');await flushPromises();expect(h.check).toHaveBeenCalledWith({...scope,mode:'inventory',workspaceWide:true},expect.any(AbortSignal))
    h.list.mockResolvedValue([connection('missing'),connection('active','102')]);h.groups.mockResolvedValue(health(['102']));await open(w);expect(card(w).text()).not.toContain('主站账号已删除');await card(w).trigger('click');await flushPromises();expect(h.push).toHaveBeenCalledWith({name:'AdminConnectionHealth',query:{focusGroupId:'10',focusGroupName:'旧组',focusAccountIds:'102',focusWorkspace:'ws'}})
  })
  it('当前卡片保持打开时，本地状态改变后不跳到旧账号；读取失败也不跳',async()=>{
    const w=await mountUpstream();await open(w);h.list.mockRejectedValueOnce(new Error('本地读取失败'));await card(w).trigger('click');await flushPromises();expect(h.push).not.toHaveBeenCalled();expect(w.text()).toContain('读取最新账号对接失败')
    h.list.mockResolvedValue([connection('missing')]);await card(w).trigger('click');await flushPromises();expect(h.push.mock.calls.at(-1)![0].name).toBe('AdminGroupRates')
  })
  it.each(['card','list'] as const)('无编号的不同名称分组按名称隔离 %s',async layout=>{
    const first={...group,id:'',name:'已删无编号分组'},second={...group,id:'',name:'有效无编号分组'}
    h.sites.mockResolvedValue([{...site,metrics:{...site.metrics,group:first,groups:[first,second]}}])
    h.list.mockResolvedValue([{...connection('missing'),upstreamGroupId:'',upstreamGroupName:first.name},{...connection('active','102'),upstreamGroupId:'',upstreamGroupName:second.name}])
    h.groups.mockResolvedValue(health(['102']))
    const w=await mountUpstream();if(layout==='list')await w.get('button[aria-label="列表模式"]').trigger('click');await open(w)
    const named=(name:string)=>w.get('[role="dialog"]').findAll('button').find(b=>b.text().includes(name))!
    expect(named(first.name).text()).toContain('主站账号已删除 · 可重新对接');expect(named(second.name).text()).not.toContain('已删除')
    await named(first.name).trigger('click');await flushPromises();expect(h.push).toHaveBeenLastCalledWith({name:'AdminGroupRates',query:{siteId:'site',focusGroup:first.name,focusGroupId:undefined}})
    await open(w);await named(second.name).trigger('click');await flushPromises();expect(h.push).toHaveBeenLastCalledWith({name:'AdminConnectionHealth',query:{focusGroupId:'10',focusGroupName:'旧组',focusAccountIds:'102',focusWorkspace:'ws'}})
  })
  it('未在任何分组找到仍有效的账号并不判删除或跳空',async()=>{
    h.groups.mockResolvedValue(health([]));const w=await mountUpstream();await open(w);expect(card(w).text()).toContain('未确认删除');expect(card(w).text()).not.toContain('主站账号已删除');await card(w).trigger('click');await flushPromises();expect(h.push.mock.calls.at(-1)![0].name).toBe('AdminGroupRates')
  })
})
describe('主站定时核对设置',()=>{
  it('保存四个时刻和开关、重读，未保存不宣称已关闭',async()=>{
    const w=mount(MainAccountCheckSettings,{props:{scope}});wrappers.push(w);await flushPromises();expect(w.findAll('input[type=time]').map(x=>(x.element as HTMLInputElement).value)).toEqual(settings().times)
    await w.get('input[type=checkbox]').setValue(false);expect(w.text()).toContain('设置尚未保存');expect(w.text()).not.toContain('已关闭定时核对')
    await w.get('input[aria-label="第1次核对时间"]').setValue('08:30');h.saveSettings.mockResolvedValue({...settings(),enabled:false,times:['08:30','13:00','18:00','22:00']});h.readSettings.mockResolvedValue({...settings(),enabled:false,times:['08:30','13:00','18:00','22:00']})
    await w.findAll('button').find(b=>b.text()==='保存核对设置')!.trigger('click');await flushPromises();expect(h.saveSettings).toHaveBeenCalledWith(scope,{enabled:false,times:['08:30','13:00','18:00','22:00']},expect.any(AbortSignal));expect(w.text()).toContain('已关闭定时核对');expect(w.text()).toContain('核对设置已保存')
  })
  it('设置读取失败显示用户可理解的原因，不允许直接保存',async()=>{
    h.readSettings.mockRejectedValueOnce(new Error('admin.settings.errors.network'))
    const w=mount(MainAccountCheckSettings,{props:{scope}});wrappers.push(w);await flushPromises();expect(w.text()).toContain('主站账号核对设置未能读取');expect(w.text()).not.toContain('admin.settings.errors.network');expect(w.findAll('button').some(b=>b.text()==='保存核对设置')).toBe(false)
  })
  it.each([['partial','部分确认'],['skipped','本次未执行检查'],['failed','核对未完成']] as const)('最近定时结果 %s 不显示为成功',async(status,label)=>{
    h.readSettings.mockResolvedValue({...settings(),recentScheduled:{status,startedAt:'2026-11-27T01:00:00Z',scheduledAt:'2026-11-27T01:00:00Z',confirmedActive:status==='partial'?1:0,confirmedMissing:0,unconfirmed:1,skipped:0,reason:'主站名单未确认完整',next:'请手动核对'}})
    const w=mount(MainAccountCheckSettings,{props:{scope}});wrappers.push(w);await flushPromises();expect(w.text()).toContain(label);expect(w.text()).toContain('主站名单未确认完整');expect(w.text()).toContain('请手动核对');expect(w.text()).not.toContain('核对成功')
  })
  it('Redis记录读取失败时保持可编辑设置，不显示虚假成功',async()=>{
    h.readSettings.mockResolvedValue({...settings(),recentReadError:'定时核对记录暂不可读，请检查 Hub 的 Redis 连接，必要时手动核对。'})
    const w=mount(MainAccountCheckSettings,{props:{scope}});wrappers.push(w);await flushPromises();expect(w.text()).toContain('Redis 连接');expect(w.text()).not.toContain('核对成功');expect(w.findAll('input[type=time]')).toHaveLength(4)
  })
  it('拒绝重复时刻并显示上次结果未知，而不是成功',async()=>{
    h.readSettings.mockResolvedValue({...settings(),recentScheduled:{status:'unknown',startedAt:'2026-11-27T01:00:00Z',scheduledAt:'2026-11-27T01:00:00Z',confirmedActive:0,confirmedMissing:0,unconfirmed:0,skipped:0}})
    const w=mount(MainAccountCheckSettings,{props:{scope}});wrappers.push(w);await flushPromises();await w.get('input[aria-label="第2次核对时间"]').setValue('09:00');expect(w.findAll('button').find(b=>b.text()==='保存核对设置')!.element).toHaveProperty('disabled',true);expect(w.text()).toContain('上次核对结果尚未确认');expect(w.text()).not.toContain('核对成功');expect(h.saveSettings).not.toHaveBeenCalled()
  })
})
