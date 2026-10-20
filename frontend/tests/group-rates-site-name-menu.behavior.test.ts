// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { reactive, ref, defineComponent, h as vueH, Transition } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { handleAuthExpired } from '@/modules/auth/api/auth';
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue';
const h = vi.hoisted(() => ({ route: null as any, push: vi.fn(), calls: [] as {
        path: string;
        body: any;
        signal?: AbortSignal | null;
    }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false, siteOptions: [] as any[], ownGroups: null as any, today: null as any, totals: null as any, todayRead: null as any, totalsRead: null as any, attention: [] as any[], currentAccount: null as any }));
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: h.push, replace: vi.fn(async (value: any) => { h.route.query = value.query; }) }) }));
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); h.currentAccount = ref({ id: 'ws-scoped', platform: 'sub2api' }); return { useAdminAccounts: () => ({ currentAccount: h.currentAccount }) }; });
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number, payload: any) => status === 401 || payload.message === 'auth.errors.unauthorized', authUnauthorizedErrorKey: 'auth.errors.unauthorized' }));
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const deferred = <T>() => { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r; }); return { promise, resolve }; };
const rate = (id: string, name = id, site = 'site-a') => ({ siteId: site, siteName: site === 'site-a' ? '范围甲' : '范围乙', groupId: id, groupName: name, platform: 'sub2api', type: 'openai', mapped: true, pricingMapped: true, deleted: false, currentMultiplier: 1, delta: null, deltaPercent: null, updatedAt: '2026-10-12T00:00:00Z' });
const conn = (id: string, group = '1', name = group, status = 'active') => ({ id, upstreamSiteId: 'site-a', upstreamGroupId: group, upstreamGroupName: name, upstreamKeyId: 'key-' + id, adminAccountId: id, adminAccountName: id, ownGroupIds: ['10'], groupType: 'openai', status, adminPlatform: 'sub2api', pricingMappingEnabled: true, canDeleteRemote: status !== 'missing', createdAt: '2026-10-12T00:00:00Z' });
const result = (confirmed = h.connections) => ({ workspaceAdminAccountId: h.currentAccount.value.id, total: h.connections.length, confirmedConnectionIds: confirmed.map(c => c.id), unconfirmedConnectionIds: h.connections.filter(c => !confirmed.includes(c)).map(c => c.id), skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: Object.fromEntries(confirmed.map(c => [c.id, c.status || 'active'])), issues: [], storageUncertain: false });
const wrappers: VueWrapper[] = [];
const open = async () => { const w = mount(GroupRatesView, { attachTo: document.body }); wrappers.push(w); await flushPromises(); return w; };
const row = (w: VueWrapper, name = '分组甲') => w.findAll('tbody tr').find(r => r.text().includes(name))!;
const button = (w: VueWrapper, label: string) => w.findAll('button').find(b => b.text().trim() === label)!;
const checks = () => h.calls.filter(c => c.path.includes('/check'));
beforeEach(() => {
    h.currentAccount.value = { id: 'ws-scoped', platform: 'sub2api' };
    h.route = reactive({ query: {} });
    h.calls = []; h.push.mockClear();
    h.rates = [rate('1', '分组甲')];
    h.connections = [{...conn('501', '1', '分组甲'), provisioningMode:'managed', ownGroupIds:['11','12'], ownGroupNames:['甲组','乙组']}];
    h.optionsFail = false;
    h.siteOptions = [{ id: 'site-a', name: '范围甲', baseUrl:'https://up.example.test:8443/path', consumption: { balance: { value: 12, display: '12' }, rechargeRate: 2 } }, { id: 'site-b', name: '范围乙' }];
    vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date('2031-01-01T00:00:00Z'));
    h.today = { workspaceAdminAccountId: 'ws-scoped', date: '2031-01-01', keys: [{siteId:'site-a', keyId:'key-101', keyIds:['key-101'], todayAmount:6.25}], sites:[{siteId:'site-a',status:'ok'}] };
    h.totals = {workspaceAdminAccountId:'ws-scoped',items:[{siteId:'site-a',keyId:'key-101',totalAmount:40,days:2,firstDate:'2030-12-30',lastDate:'2030-12-31',siteMergedDays:0}]};
    h.todayRead = async () => json(h.today); h.totalsRead = async () => json(h.totals);
    h.attention = [];
    h.ownGroups = async (workspace: string) => json({ workspaceAdminAccountId: workspace, groups: [{ id: '11', name: '甲组', multiplier:1.2, status:'active' }, {id:'12',name:'乙组',multiplier:1.3,status:'active'}] });
    vi.mocked(handleAuthExpired).mockClear();
    h.check = async () => json(result());
    h.local = async () => json({ workspaceAdminAccountId: h.currentAccount.value.id, items: h.connections });
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
        const path = String(url);
        const body = init.body ? JSON.parse(String(init.body)) : null;
        h.calls.push({ path, body, signal: init.signal });
        if (path.includes('/upstream-key-usage-today')) return h.todayRead();
        if (path.includes('/upstream-key-recorded-totals')) return h.totalsRead();
        if (path.includes('/main-site-groups')) return json({workspaceAdminAccountId:'ws-scoped',connection:h.connections[0],account:{id:'101',name:'测试账号',platform:'openai'},currentGroups:[{id:'10',name:'原分组'}],hubGroups:[{id:'10',name:'原分组'}],groups:[{id:'10',name:'原分组',status:'active'},{id:'11',name:'新分组',status:'active'}],consistent:true});
        if (path.includes('/own-group-rates'))
            return h.ownGroups(new URL(path, 'http://local').searchParams.get('workspaceAdminAccountId'));
        if (path.includes('/needs-attention'))
            return json({ workspaceAdminAccountId: h.currentAccount.value.id, items: h.attention, total: h.attention.length, page: 1, pageSize: 30, totalPages: h.attention.length ? 1 : 0 });
        if (path.includes('/real-disconnect')) {
            h.connections = [];
            return json({ ok: true });
        }
        if (path.includes('/real-connections/check'))
            return h.check(body);
        if (path.includes('/real-connections/list-scoped'))
            return h.local(body);
        if (path.endsWith('/real-connections'))
            return json(h.connections);
        if (path.includes('/upstream-sites/options'))
            return h.optionsFail ? json({ message: '网站名称缓存无法读取，请刷新重试' }, 500) : json({ workspaceAdminAccountId: h.currentAccount.value.id, items: h.siteOptions });
        if (path.includes('/upstream-sites'))
            return json([{ id: 'site-a', name: '范围甲' }, { id: 'site-b', name: '范围乙' }]);
        if (path.includes('/group-rates?')) {
            const q = new URL(path, 'http://local').searchParams;
            return json({ workspaceAdminAccountId: h.currentAccount.value.id, items: h.rates, total: 65, page: q.has('focusGroupId') ? 3 : Number(q.get('page') || 1), pageSize: 30, totalPages: 3, focusFound: q.has('focusGroupId') ? true : undefined, types: ['openai'], platforms: ['sub2api'], statusCounts: { all: 65, mapped: 1, unmapped: 64, deleted: 0 } });
        }
        if (path.includes('/group-rates/type'))
            return json({ success: true });
        if (path.includes('/group-rates/history'))
            return json([]);
        if (path.includes('mapping-options'))
            return json({ ownGroups: [{ id: '10', groupName: '弹窗分组', platform: 'openai', multiplier: 1 }], mappings: [] });
        if (path.includes('/dashboard'))
            return json({ platform: 'sub2api' });
        return json({});
    }));
});
const P1 = '点击选择：去分组健康查看对接的主站分组，或在新标签页打开上游网站；只跳转，不会修改主站'
const P5 = '点站点名称可以跳到分组健康查看对接的主站分组，或在新标签页打开上游网站；只跳转，不会修改主站。'
const trigger = (w: VueWrapper, index = 0) => w.findAll('[data-site-name-trigger]')[index]!
const menu = () => document.querySelector<HTMLElement>('[role="menu"]')!
const item = (label: string) => Array.from(menu().querySelectorAll<HTMLElement>('[role="menuitem"]')).find(e => e.textContent?.includes(label))!
const openMenu = async (w: VueWrapper, index = 0) => { await trigger(w,index).trigger('click'); await flushPromises() }
const pick = async (label: string) => { item(label).click(); await flushPromises() }
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('new site menu RED', () => {
 it('F1 lists exactly the connected groups and website with P2 P3 P4', async () => {
  const w = await open(); await openMenu(w)
  expect(Array.from(menu().querySelectorAll('[role="menuitem"]')).map(e => e.textContent)).toHaveLength(3)
  expect(Array.from(menu().querySelectorAll('button[role="menuitem"]')).map(e=>e.firstElementChild?.textContent)).toEqual(row(w).findAll('[data-own-group-line]').map(e=>'去分组健康：'+e.text()))
  expect(menu().textContent).toContain('只跳转，不会修改主站。')
  expect(menu().textContent).toContain('在分组健康选中这个主站分组，并标出对应账号')
  expect(menu().textContent).toContain('在新标签页打开 up.example.test:8443'); expect(menu().textContent).not.toContain('/path')
 })
 it('F2 navigates with four focus parameters and adds no requests', async () => {
  const w = await open(); const n = h.calls.length; await openMenu(w); await pick('去分组健康：乙组')
  expect(h.push).toHaveBeenCalledWith({name:'AdminConnectionHealth',query:{focusGroupId:'12',focusGroupName:'乙组',focusAccountIds:'501',focusWorkspace:'ws-scoped'}})
  expect(menu()).toBeNull(); expect(h.calls).toHaveLength(n)
 })
 it('F2 keeps the existing page transition working when navigating away', async () => {
  const showRates=ref(true)
  const host=defineComponent({setup:()=>()=>vueH(Transition,{mode:'out-in'}, {default:()=>showRates.value?vueH(GroupRatesView):vueH('div',{'data-health-destination':''},'分组健康')})})
  const w=mount(host,{attachTo:document.body,global:{stubs:{transition:false}}});wrappers.push(w);await flushPromises()
  h.push.mockImplementationOnce(()=>{showRates.value=false})
  await openMenu(w);await pick('去分组健康：乙组');await new Promise(r=>setTimeout(r,100));await flushPromises()
  expect(w.find('[data-health-destination]').exists()).toBe(true)
 })
 it('F3 opens the saved website in a separate tab without requests', async () => {
  const w = await open(); const n = h.calls.length; await openMenu(w)
  const link = item('打开上游网站'); expect(link.tagName).toBe('A'); expect(link.getAttribute('href')).toBe('https://up.example.test:8443/path')
  expect(link.getAttribute('target')).toBe('_blank'); expect(link.getAttribute('rel')).toBe('noopener noreferrer')
  link.addEventListener('click', e => e.preventDefault()); await pick('打开上游网站'); expect(menu()).toBeNull(); expect(h.calls).toHaveLength(n)
 })
 it.each([
  ['H1','对接资料还没读到，请点‘刷新数据’后再试。'],
  ['H2','对接的主站账号已不存在，分组健康里看不到它；可点右侧‘清理本地失效对接’。'],
  ['H2a','对接的主站账号已不存在，这个上游分组也已删除，分组健康里没有可查看的内容。'],
  ['H3','这个上游分组还没对接主站账号，分组健康里没有它；需要时点右侧‘配置对接’。'],
  ['H3a','这个上游分组已删除，也没有对接主站账号，分组健康里没有可查看的内容。'],
  ['H4','对接记录里没有主站分组，无法定位；可点右侧‘修改主站分组’补上。'],
  ['H4a','对接记录里没有主站分组，无法直接定位；请到分组健康，在左侧搜索框输入这个主站账号的名称查找。'],
 ])('F4 disables %s with its exact reason', async (kind,reason) => {
  if(kind==='H1')h.local=async()=>json({message:'对接资料读取超时，请刷新重试'},500)
  if(kind.startsWith('H2'))h.connections[0].status='missing'
  if(kind.startsWith('H3'))h.connections=[]
  if(kind.startsWith('H4')){h.connections[0].ownGroupIds=[];h.connections[0].ownGroupNames=[];if(kind==='H4a')h.connections[0].provisioningMode='existing'}
  const w=await open();await openMenu(w);if(kind==='H2a'||kind==='H3a'){(w.vm as any).siteMenuRate.deleted=true;await flushPromises()}const e=item('去分组健康');expect(e.getAttribute('aria-disabled')).toBe('true');expect(e.textContent).toContain(reason)
  if(kind.endsWith('a'))expect(e.textContent).not.toContain('点右侧')
  e.click();await flushPromises();expect(h.push).not.toHaveBeenCalled()
 })
 it.each(['W1','W2','empty','script','credentials'])('F5 disables website %s without an unsafe link',async kind=>{
  if(kind==='W1')h.optionsFail=true
  else if(kind==='W2')h.siteOptions=[]
  else h.siteOptions[0].baseUrl=kind==='empty'?'':kind==='script'?'javascript:alert(1)':'https://u:p@up.example.test'
  const w=await open();await openMenu(w);const e=item('打开上游网站');expect(e.getAttribute('aria-disabled')).toBe('true');expect(e.hasAttribute('href')).toBe(false)
  const reason=kind==='W1'?w.findAll('[role="alert"]').find(a=>a.text().startsWith('网站选项暂时无法加载'))!.text():kind==='W2'?'网站资料里没有这个网站，可能还在读取，或已在上游管理删除。请稍后点‘刷新数据’；仍没有时到上游管理检查。':'上游管理里这个网站的网址为空或格式不对（只支持 http/https 开头的网址），无法打开；请到上游管理修改网址。'
  expect(e.textContent).toContain(reason);expect(document.body.innerHTML).not.toContain('href="javascript:');expect(menu().textContent).not.toContain('u:p')
 })
 it('F6 retains the last successful URL after an options refresh failure',async()=>{
  const w=await open();h.optionsFail=true;await button(w,'刷新数据').trigger('click');await flushPromises();await openMenu(w)
  expect(item('打开上游网站').getAttribute('href')).toBe('https://up.example.test:8443/path')
 })
 it('F7 F8 closes outside, by Escape with focus restored, toggles and switches rows',async()=>{
  h.rates.push(rate('2','分组乙','site-b'));const w=await open()
  await openMenu(w);document.body.click();await flushPromises();expect(menu()).toBeNull()
  await openMenu(w);document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'}));await flushPromises();expect(menu()).toBeNull();expect(document.activeElement).toBe(trigger(w).element)
  await openMenu(w);await openMenu(w);expect(menu()).toBeNull()
  await openMenu(w);await openMenu(w,1);expect(document.querySelectorAll('[role="menu"]')).toHaveLength(1);expect(menu().textContent).toContain('范围乙 · 分组乙')
 })
 it('F9 supplies exact accessible help and P5 after the existing explanation',async()=>{
  const w=await open();expect(trigger(w).attributes('title')).toBe(P1);expect(trigger(w).attributes('aria-label')).toBe(P1);expect(trigger(w).attributes('aria-haspopup')).toBe('menu');expect(trigger(w).attributes('aria-expanded')).toBe('false')
  const hint=w.get('[data-site-name-menu-hint]');expect(hint.text()).toBe(P5);expect(hint.element.previousElementSibling).toBe(w.get('[data-account-consumption-hint]').element)
  await openMenu(w);expect(trigger(w).attributes('aria-expanded')).toBe('true')
 })
 it('F10 allows navigation while automatic checking is pending',async()=>{
  const pending=deferred<Response>();h.check=()=>pending.promise;const w=await open()
  expect(button(w,'刷新数据').element).toHaveProperty('disabled',true);expect(trigger(w).element).toHaveProperty('disabled',false)
  await openMenu(w);await pick('去分组健康：甲组');expect(h.push).toHaveBeenCalled();pending.resolve(json(result()));await flushPromises()
 })
 it('F11 trims, deduplicates and sorts accounts and omits absent accounts',async()=>{
  const base=h.connections[0];h.connections=[{...base,adminAccountId:' 502 ',ownGroupIds:['11'],ownGroupNames:['甲组']},{...base,id:'b',adminAccountId:'501',ownGroupIds:['11'],ownGroupNames:['甲组']},{...base,id:'c',adminAccountId:'501',ownGroupIds:['11'],ownGroupNames:['甲组']},{...base,id:'d',adminAccountId:'',ownGroupIds:['12'],ownGroupNames:['乙组']}]
  const w=await open();await openMenu(w);await pick('去分组健康：甲组');expect(h.push.mock.calls.at(-1)![0].query.focusAccountIds).toBe('501,502')
  await openMenu(w);await pick('去分组健康：乙组');expect(h.push.mock.calls.at(-1)![0].query.focusAccountIds).toBeUndefined()
 })
 it.each(['scroll','resize','data','workspace'])('F12 closes on %s without an outside click',async kind=>{
  const w=await open();await openMenu(w);expect(menu()).not.toBeNull();await flushPromises();expect(menu()).not.toBeNull()
  const clicks=vi.fn();document.addEventListener('click',clicks)
  if(kind==='scroll')w.element.dispatchEvent(new Event('scroll'))
  if(kind==='resize')window.dispatchEvent(new Event('resize'))
  if(kind==='data')h.route.query={siteId:'site-b'}
  if(kind==='workspace')h.currentAccount.value={id:'ws-other',platform:'sub2api'}
  await flushPromises();expect(clicks).not.toHaveBeenCalled();expect(menu()).toBeNull();document.removeEventListener('click',clicks)
 })
 it('F12 unregisters all four listeners on unmount using the same function and capture',async()=>{
  const add=vi.spyOn(document,'addEventListener'),remove=vi.spyOn(document,'removeEventListener'),wa=vi.spyOn(window,'addEventListener'),wr=vi.spyOn(window,'removeEventListener')
  const w=await open();await openMenu(w);w.unmount();wrappers.splice(wrappers.indexOf(w),1)
  for(const type of ['click','keydown','scroll']){const call=add.mock.calls.find(c=>c[0]===type)!;expect(call).toBeDefined();expect(remove.mock.calls).toContainEqual(call);if(type==='scroll')expect(call[2]).toBe(true)}
  expect(wr.mock.calls).toContainEqual(wa.mock.calls.find(c=>c[0]==='resize'));expect(menu()).toBeNull()
 })
 const geometry=(height:number,top:number,bottom:number,left=20,contentHeight=800)=>{
  vi.stubGlobal('innerHeight',height);vi.stubGlobal('innerWidth',1000)
  vi.spyOn(Element.prototype,'getBoundingClientRect').mockImplementation(function(this:Element){return {x:left,y:top,left,top,bottom,right:left+100,width:this.getAttribute('role')==='menu'?288:100,height:this.getAttribute('role')==='menu'?contentHeight:bottom-top,toJSON:()=>({})} as DOMRect})
 }
 it.each([400,1200])('F13 limits height and keeps internal scroll open for viewport %s',async height=>{
  h.connections[0].ownGroupIds=Array.from({length:15},(_,i)=>String(i+11));h.connections[0].ownGroupNames=Array.from({length:15},(_,i)=>'组'+i)
  geometry(height,height===400?180:100,height===400?200:120);const w=await open();await openMenu(w)
  expect(menu().classList.contains('overflow-y-auto')).toBe(true);expect(menu().querySelectorAll('[role="menuitem"]')).toHaveLength(16)
  expect(parseFloat(menu().style.maxHeight)).toBe(height===400?188:384)
  menu().dispatchEvent(new Event('scroll'));await flushPromises();expect(menu()).not.toBeNull()
 })
 it.each([[360,380,20,152],[20,40,20,48],[20,40,-20,48]])('F14 places menu from trigger %s',async(top,bottom,left,y)=>{
  geometry(400,top,bottom,left,200);const w=await open();await openMenu(w)
  expect(parseFloat(menu().style.top)).toBe(y);expect(parseFloat(menu().style.left)).toBe(Math.max(8,left))
 })
})

describe('old behavior preserved',()=>{
 it('F1 F9 retains consumption rows and the original explanation',async()=>{
  const w=await open();const td=row(w).findAll('td')[0]!
  for(const label of ['今日消费：','累计消费：','整站余额：'])expect(td.text()).toContain(label)
  expect(w.get('[data-account-consumption-hint]').text()).toBe('今日消费和累计消费是该主站账号对应上游 Key 的金额；累计只统计 Hub 按新加坡日期已保存的天数，不是上游账单全部历史；整站余额来自上游管理上次同步。页面不会重新查询上游或修改主站。今日消费、整站余额需要更新时，请先在上游管理同步，再点‘刷新数据’；累计消费需要更新时，请到首页成本区点‘刷新账号统计’，再点‘刷新数据’。')
 })
 it('F11 retains own group, sales multiplier and margin columns',async()=>{
  const w=await open();expect(row(w).findAll('[data-own-group-line]').map(e=>e.text())).toEqual(['乙组','甲组'])
  expect(row(w).findAll('[data-sales-line]').map(e=>e.text().trim())).toEqual(['1.3x','1.2x'])
  expect(row(w).findAll('[data-profit-margin-line]').map(e=>e.text().trim())).toEqual(['23.08%','16.67%'])
 })
})
