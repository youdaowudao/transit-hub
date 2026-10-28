// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const account = (id:string,monitored=true):AdminGroupAccount => ({
 id,name:'账号'+id,targetId:'sub2api:ws-a:'+id,platform:'openai',type:'subscription',status:'active',schedulable:true,
 priority:5,concurrency:2,loadFactor:2,accountTier:1,probeAvailable:true,modelHealth:[],hasAssignedPolicy:monitored,hasEnabledPolicy:monitored,hasEnabledProbePolicy:monitored,
})
const group = (overrides:Partial<AdminGroupHealth>={}):AdminGroupHealth => ({
 id:'11',name:'甲组',platform:'openai',status:'active',type:'standard',isExclusive:false,subscriptionType:'',multiplier:1,multiplierDisplay:'1x',accountCount:3,monitoredAccountCount:2,
 healthSummary:{totalAccounts:3,probeableAccounts:3,unprobeableAccounts:0,healthyModels:0,degradedModels:0,suspendedModels:0,disabledModels:0,unconfiguredModels:0,lastProbeAt:null},
 accounts:[account('501'),account('502',false),account('503')],...overrides,
})
const wrappers:VueWrapper[]=[]
let scrolled:Element[]=[]
const scroll=vi.fn(function(this:Element){scrolled.push(this)})
const open=async(props:Record<string,unknown>={})=>{const w=mount(AdminGroupHealthDetail,{attachTo:document.body,props:{group:group(),hideUnmonitoredAccounts:false,questionAnswerUnreadTargetIds:[],actionLoading:false,...props}});wrappers.push(w);await flushPromises();return w}
const marked=(w:VueWrapper)=>w.findAll('tr[data-focused-account]')
const A1='要找的账号「账号502」没有开启监控，被‘隐藏未监控’藏起来了。取消勾选分组标题右侧的‘隐藏未监控’就能看到；这只改本机显示，不会修改主站。'
const A2='在分组「甲组」中没有找到账号（名称暂未读到）。请点右上角‘刷新’；仍找不到时，到分组倍率检查这条对接。'
beforeEach(()=>{scrolled=[];scroll.mockClear();vi.stubGlobal('fetch',vi.fn(async()=>new Response('[]')));Element.prototype.scrollIntoView=scroll})
afterEach(()=>{for(const w of wrappers.splice(0))w.unmount();vi.unstubAllGlobals();document.body.innerHTML='';delete (Element.prototype as any).scrollIntoView})

describe('new account focus RED',()=>{
 it('D1 marks only the target row with the exact border, background and label',async()=>{
  const w=await open({focusedAccountIds:['501']});expect(marked(w)).toHaveLength(1);const r=marked(w)[0]!
  expect(r.text()).toContain('账号501');for(const c of ['bg-primary/[0.10]','outline','outline-2','-outline-offset-2','outline-primary/70'])expect(r.classes()).toContain(c)
  const label=r.findAll('span').find(e=>e.text()==='要找的账号')!;expect(label.attributes('title')).toBe('从分组倍率跳转过来要找的账号；换到其他分组后标记消失。只是标记，不会修改主站。')
  expect(w.findAll('tbody tr').filter(e=>e.text().includes('账号502')||e.text().includes('账号503')).every(e=>!e.attributes('data-focused-account'))).toBe(true)
 })
 it('D2 scrolls once through refresh and an accounts error recovery',async()=>{
  const w=await open({focusedAccountIds:['501']});expect(scroll).toHaveBeenCalledTimes(1);expect(scrolled[0]).toBe(marked(w)[0]!.element);expect(scroll).toHaveBeenCalledWith({block:'center'})
  await w.setProps({group:group()});await flushPromises();expect(scroll).toHaveBeenCalledTimes(1)
  await w.setProps({group:group({accounts:[],accountsError:'admin.connectionHealth.errors.network'})});await flushPromises()
  await w.setProps({group:group()});await flushPromises();expect(scroll).toHaveBeenCalledTimes(1);expect(marked(w)).toHaveLength(1)
 })
 it('D3 warns about hidden unmonitored targets then scrolls once when revealed',async()=>{
  const w=await open({focusedAccountIds:['502'],hideUnmonitoredAccounts:true});expect(w.text()).toContain(A1);expect(marked(w)).toHaveLength(0);expect(scroll).not.toHaveBeenCalled()
  await w.setProps({hideUnmonitoredAccounts:false});await flushPromises();expect(marked(w)[0]!.text()).toContain('账号502');expect(w.text()).not.toContain(A1);expect(scroll).toHaveBeenCalledTimes(1)
 })
 it('D4 warns on absent accounts and suppresses the warning on read errors',async()=>{
  const w=await open({focusedAccountIds:['999']});expect(w.text()).toContain(A2);expect(marked(w)).toHaveLength(0)
  await w.setProps({group:group({accountsError:'admin.connectionHealth.errors.network',accounts:[]})});expect(w.text()).not.toContain(A2);expect(w.find('.bg-destructive\\/10').exists()).toBe(true)
 })
 it('D6 preserves the user filter without inventing a missing account warning',async()=>{
  const w=await open({focusedAccountIds:['501']});expect(marked(w)).toHaveLength(1)
  const filter=w.findAll('button').find(b=>b.text().includes('未配置'))!;expect(filter).toBeDefined();await filter.trigger('click');await flushPromises()
  expect(marked(w)).toHaveLength(0);expect(w.find('[data-account-focus-notice]').exists()).toBe(false)
 })
 it('D7 resets the scroll flag for a new set of target IDs',async()=>{
  const w=await open({focusedAccountIds:['501']});await w.setProps({focusedAccountIds:['503']});await flushPromises();expect(scroll).toHaveBeenCalledTimes(2);expect(scrolled[1]).toBe(marked(w)[0]!.element);expect(marked(w)[0]!.text()).toContain('账号503')
 })
 it('D8 displays both independent notices',async()=>{
  const w=await open({focusedAccountIds:['502','999'],hideUnmonitoredAccounts:true});expect(w.text()).toContain(A1);expect(w.text()).toContain(A2);expect(scroll).not.toHaveBeenCalled()
 })
 it('D9 keeps the complete account name without an appended website or ID',async()=>{
  const name='完整账号名称-长期使用-保留全部后缀@example.test'
  const w=await open({focusedAccountIds:['447'],focusedAccountLabels:{'447':{name,siteNames:['上游网站甲']}}})
  expect(w.get('[data-account-focus-notice]').text()).toBe(`在分组「甲组」中没有找到账号「${name}」。请点右上角‘刷新’；仍找不到时，到分组倍率检查这条对接。`)
  expect(w.get('[data-account-focus-notice]').classes()).toContain('break-words')
  expect(w.get('[data-account-focus-notice]').text()).not.toContain('上游网站甲')
  expect(w.text()).not.toContain('账号编号');expect(w.text()).not.toContain('447');expect(w.text()).not.toContain('可能已从这个主站分组移除')
 })
 it('D10 keeps multiple complete names as text without appended websites',async()=>{
  const w=await open({focusedAccountIds:['448','447'],focusedAccountLabels:{'447':{name:'完整账号甲',siteNames:['网站甲','网站乙']},'448':{name:'完整账号<script>乙</script>',siteNames:[]}}})
  expect(w.get('[data-account-focus-notice]').text()).toContain('账号「完整账号<script>乙</script>」、账号「完整账号甲」')
  expect(w.find('script').exists()).toBe(false);expect(w.text()).not.toContain('账号编号')
 })
 it('D11 replaces the unknown name when labels arrive and removes the notice when refresh finds the account',async()=>{
  const w=await open({focusedAccountIds:['447']});expect(w.text()).toContain(A2)
  await w.setProps({focusedAccountLabels:{'447':{name:'完整账号甲',siteNames:['网站甲']}}})
  expect(w.text()).toContain('账号「完整账号甲」');expect(w.text()).not.toContain('名称暂未读到')
  await w.setProps({group:group({accounts:[account('447')]})});await flushPromises()
  expect(w.find('[data-account-focus-notice]').exists()).toBe(false);expect(marked(w)).toHaveLength(1)
 })
 it('D12 keeps the current name for hidden targets without appending the website',async()=>{
  const w=await open({focusedAccountIds:['502'],hideUnmonitoredAccounts:true,focusedAccountLabels:{'502':{name:'旧账号名称',siteNames:['网站甲']}}})
  expect(w.get('[data-account-focus-notice]').text()).toBe(A1)
  expect(w.get('[data-account-focus-notice]').text()).not.toContain('网站甲');expect(w.text()).not.toContain('旧账号名称');expect(w.text()).not.toContain('没有找到')
  await w.setProps({hideUnmonitoredAccounts:false});await flushPromises()
  expect(w.find('[data-account-focus-notice]').exists()).toBe(false);expect(marked(w)).toHaveLength(1)
 })
})
describe('old behavior preserved',()=>{
 it('D5 leaves all rows and scroll position alone without the optional property',async()=>{
  const w=await open();expect(w.findAll('tbody tr')).toHaveLength(3);expect(marked(w)).toHaveLength(0);expect(w.text()).not.toContain('要找的账号');expect(scroll).not.toHaveBeenCalled()
 })
})
