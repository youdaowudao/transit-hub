// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { reactive } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { readOwnGroupRates } from '@/modules/admin/api/mySites';
import { handleAuthExpired } from '@/modules/auth/api/auth';
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue';
const h = vi.hoisted(() => ({ route: null as any, calls: [] as {
        path: string;
        body: any;
        signal?: AbortSignal | null;
    }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false, siteOptions: [] as any[], ownGroups: null as any, today: null as any, totals: null as any, todayRead: null as any, totalsRead: null as any, attention: [] as any[], currentAccount: null as any }));
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (value: any) => { h.route.query = value.query; }) }) }));
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
    h.calls = [];
    h.rates = [rate('1', '分组甲')];
    h.connections = [conn('101', '1', '分组甲')];
    h.optionsFail = false;
    h.siteOptions = [{ id: 'site-a', name: '范围甲', consumption: { balance: { value: 12, display: '12' }, rechargeRate: 2 } }, { id: 'site-b', name: '范围乙' }];
    vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date('2031-01-01T00:00:00Z'));
    h.today = { workspaceAdminAccountId: 'ws-scoped', date: '2031-01-01', keys: [{siteId:'site-a', keyId:'key-101', keyIds:['key-101'], todayAmount:6.25}], sites:[{siteId:'site-a',status:'ok'}] };
    h.totals = {workspaceAdminAccountId:'ws-scoped',items:[{siteId:'site-a',keyId:'key-101',totalAmount:40,days:2,firstDate:'2030-12-30',lastDate:'2030-12-31',siteMergedDays:0}]};
    h.todayRead = async () => json(h.today); h.totalsRead = async () => json(h.totals);
    h.attention = [];
    h.ownGroups = async (workspace: string) => json({ workspaceAdminAccountId: workspace, groups: [{ id: '10', name: '新名', multiplier: 1.2, status: 'active' }] });
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
describe('account consumption boundaries', () => {
 it.each(['merged','missing-key','missing-site','excluded','yesterday'])('rejects unavailable today %s without inventing zero', async kind => {
  if(kind==='merged') h.today.keys[0].merged=true;
  if(kind==='missing-key') h.today.keys=[];
  if(kind==='missing-site') {h.today.sites[0].status='missing';h.today.sites[0].errorKey='network';}
  if(kind==='excluded') h.today.sites=[];
  if(kind==='yesterday') h.today.date='2030-12-31';
  const w=await open();expect(siteCell(w).text()).toContain('今日消费：—');expect(siteCell(w).text()).not.toContain('今日消费：0.00');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  expect(siteCell(w).find('[data-account-today-consume]').attributes('title')).toBeTruthy();
 });
 it('treats zero saved days as missing and preserves empty-status active bindings',async()=>{
  h.connections[0].status='';h.totals.items[0].days=0;const w=await open();expect(siteCell(w).text()).toContain('今日消费：6.25');expect(siteCell(w).text()).toContain('累计消费：—');expect(siteCell(w).find('[data-account-total-consume]').attributes('title')).toContain('刷新账号统计');
 });
 it('hides every account total when the site has merged history',async()=>{
  h.totals.items[0].siteMergedDays=1;
  const w=await open();expect(siteCell(w).text()).toContain('累计消费：—');
  expect(siteCell(w).find('[data-account-total-consume]').attributes('title')).toContain('这个站点有合并统计的 Key，累计无法拆分到单个账号');
 });
 it.each(['none','inactive','unknown'])('keeps balance independent of %s connections',async kind=>{
  if(kind==='none')h.connections=[];
  if(kind==='inactive')h.connections[0].status='missing';
  if(kind==='unknown')h.local=async()=>json({message:'对接资料读取超时，请刷新重试'},500);
  const w=await open();expect(siteCell(w).text()).toContain('整站余额：24.00');
  expect(siteCell(w).text()).toContain('今日消费：—');expect(siteCell(w).text()).toContain('累计消费：—');
  const title=siteCell(w).find('[data-account-today-consume]').attributes('title');
  expect(title).toContain(kind==='unknown'?'对接资料还没读到，请点‘刷新数据’':'该分组还没有对接账号');
 });
 it('sums distinct keys once and rejects a partial total',async()=>{
  h.connections.push({...conn('102','1','分组甲'),upstreamKeyId:'key-101'},conn('103','1','分组甲'));
  h.today.keys.push({...h.today.keys[0],keyId:'key-103',keyIds:['key-103'],todayAmount:1});
  h.totals.items.push({...h.totals.items[0],keyId:'key-103',totalAmount:5});
  const w=await open();expect(siteCell(w).text()).toContain('今日消费：7.25');expect(siteCell(w).text()).toContain('累计消费：45.00');expect(siteCell(w).text()).toContain('整站余额：24.00');
  h.totals.items.pop();h.today.keys.pop();await click(w,'刷新数据');expect(siteCell(w).text()).toContain('累计消费：—');expect(siteCell(w).text()).not.toContain('累计消费：40.00');expect(siteCell(w).text()).toContain('今日消费：—');expect(siteCell(w).text()).not.toContain('今日消费：6.25');
 });
 it('retains same-day today and cross-day totals on read failure with actionable reasons',async()=>{
  const w=await open();h.todayRead=async()=>json({message:'unavailable'},500);h.totalsRead=h.todayRead;
  await click(w,'刷新数据');expect(siteCell(w).text()).toContain('今日消费：6.25（上次读取）');expect(siteCell(w).text()).toContain('累计消费：40.00（上次读取）');
  expect(w.text()).toContain('Hub 读取已保存的消费缓存失败');expect(w.text()).toContain('Hub 读取已保存的每日消费记录失败');
  expect(w.text()).not.toContain('检查站点连接或登录凭证');
  vi.setSystemTime(new Date('2031-01-02T00:00:00Z'));await click(w,'刷新数据');
  expect(siteCell(w).text()).toContain('今日消费：—');expect(siteCell(w).text()).toContain('累计消费：40.00（上次读取）');
  expect(w.findAll('[role="alert"]').find(a=>a.text().startsWith('今日消费暂时无法读取'))!.text()).not.toContain('已显示的是上次读取结果');
 });
 it('updates a retained failure notice at midnight without another request',async()=>{
  vi.useFakeTimers();vi.setSystemTime(new Date('2031-01-01T15:59:58Z'));
  const w=await open();h.todayRead=async()=>json({message:'unavailable'},500);h.totalsRead=h.todayRead;
  await click(w,'刷新数据');const reads=h.calls.length;
  const todayAlert=()=>w.findAll('[role="alert"]').find(a=>a.text().startsWith('今日消费暂时无法读取'))!;
  expect(todayAlert().text()).toContain('已显示的是上次读取结果');
  await vi.advanceTimersByTimeAsync(2100);await flushPromises();
  expect(siteCell(w).text()).toContain('今日消费：—');expect(todayAlert().text()).not.toContain('已显示的是上次读取结果');
  expect(siteCell(w).find('[data-account-today-consume]').attributes('title')).not.toContain('已显示的是上次读取结果');
  expect(w.findAll('[role="alert"]').find(a=>a.text().startsWith('已记录合计暂时无法读取'))!.text()).toContain('已显示的是上次读取结果');
  expect(h.calls).toHaveLength(reads);
 });
 it.each(['today','totals'])('keeps a known malformed %s response reason',async kind=>{
  if(kind==='today')h.todayRead=async()=>json({...h.today,keys:null});else h.totalsRead=async()=>json({...h.totals,items:null});
  const w=await open();const notice=w.findAll('[role="alert"]').find(a=>a.text().startsWith(kind==='today'?'今日消费暂时无法读取':'已记录合计暂时无法读取'))!;
  expect(notice.text()).toContain(kind==='today'?'消费记录返回内容不完整':'每日消费记录返回内容不完整');
  expect(notice.text()).not.toContain('Hub 读取已保存的');expect(notice.text()).toContain('刷新数据');
 });
 it('limits an options failure to balance while new account amounts still update',async()=>{
  const w=await open();h.optionsFail=true;h.today.keys[0].todayAmount=2;h.totals.items[0].totalAmount=55;
  await click(w,'刷新数据');expect(siteCell(w).text()).toContain('今日消费：2.00');expect(siteCell(w).text()).toContain('累计消费：55.00');
  expect(siteCell(w).text()).toContain('整站余额：24.00（上次读取）');
  const notice=w.findAll('[role="alert"]').find(a=>a.text().startsWith('网站选项暂时无法加载'))!;
  expect(notice.text()).toContain('名称和整站余额');expect(notice.text()).not.toContain('消费数据');
 });
 it.each(['today','totals'])('rejects a mismatched response workspace for %s',async kind=>{
  h[kind].workspaceAdminAccountId='other';const w=await open();
  expect(siteCell(w).text()).toContain(kind==='today'?'今日消费：—':'累计消费：—');expect(w.text()).toContain('工作区已变化，请刷新重试');
 });
 it('drops superseded and old-workspace late reads and aborts them',async()=>{
  const old=deferred<Response>();h.todayRead=()=>old.promise;
  const w=await open();const first=h.calls.find(c=>c.path.includes('upstream-key-usage-today'));
  h.todayRead=async()=>json(h.today);await click(w,'刷新数据');
  expect(first?.signal?.aborted).toBe(true);
  old.resolve(json({...h.today,keys:[{...h.today.keys[0],todayAmount:999}]}));await flushPromises();expect(w.text()).not.toContain('999.00');
  const late=deferred<Response>();h.totalsRead=()=>late.promise;await click(w,'刷新数据');
  h.currentAccount.value={id:'other',platform:'sub2api'};h.totalsRead=()=>new Promise(()=>{});await flushPromises();
  late.resolve(json(h.totals));await flushPromises();expect(w.text()).not.toContain('40.00');
 });
 it('expires today at Singapore midnight while idle without requesting data and cleans timers',async()=>{
  vi.useFakeTimers();vi.setSystemTime(new Date('2031-01-01T15:59:59Z'));
  const w=await open();expect(siteCell(w).text()).toContain('今日消费：6.25');const reads=h.calls.length;
  await vi.advanceTimersByTimeAsync(1100);await flushPromises();
  expect(siteCell(w).text()).toContain('今日消费：—');expect(siteCell(w).text()).toContain('累计消费：40.00');expect(h.calls).toHaveLength(reads);
  w.unmount();wrappers.splice(wrappers.indexOf(w),1);expect(vi.getTimerCount()).toBe(0);
 });
 it('rechecks the date on becoming visible without a data request',async()=>{
  const w=await open();const reads=h.calls.length;vi.setSystemTime(new Date('2031-01-02T00:00:00Z'));
  document.dispatchEvent(new Event('visibilitychange'));await flushPromises();
  expect(siteCell(w).text()).toContain('今日消费：—');expect(h.calls).toHaveLength(reads);
 });
 it.each(['ready','loading','failed','missing','zero','cost-missing'])('shows editor group rates and matching margins for %s without changing selection or writes',async kind=>{
  h.connections[0].provisioningMode='managed';
  if(kind==='loading')h.ownGroups=()=>new Promise(()=>{});
  if(kind==='failed')h.ownGroups=async()=>json({message:'读取倍率超时，请刷新重试'},502);
  if(kind==='missing')h.ownGroups=async()=>json({workspaceAdminAccountId:'ws-scoped',groups:[]});
  if(kind==='zero')h.ownGroups=async()=>json({workspaceAdminAccountId:'ws-scoped',groups:[{id:'10',name:'原分组',multiplier:0,status:'active'}]});
  if(kind==='cost-missing')h.rates[0].currentMultiplier=null;
  const w=await open();const mainMargin=row(w).find('[data-profit-margin-line]').text();await click(w,'修改主站分组');
  const label=w.get('[role="dialog"]').findAll('label').find(l=>l.text().includes('原分组'))!;
  expect(label.get('input').element).toHaveProperty('checked',true);
  expect(label.text()).toContain('倍率');expect(label.text()).toContain('若移入此分组：利润率');
  if(kind==='ready'){expect(label.text()).toContain('倍率 1.20');expect(label.text()).toContain(mainMargin.trim());expect(label.text()).toContain('16.67%');}
  else {expect(label.text()).toContain('—');expect(label.text().length).toBeGreaterThan(25);}
  expect(w.get('[role="dialog"]').text()).toContain('利润率按（该分组倍率－当前成本倍率）÷该分组倍率估算，只用于参考，不会修改主站；选好后仍要点保存才会生效。');
  expect(h.calls.filter(c=>c.path.includes('/main-site-groups')&&c.body)).toHaveLength(0);
 });
});
afterEach(() => { for (const w of wrappers.splice(0))
    w.unmount(); vi.useRealTimers(); vi.unstubAllGlobals(); document.body.innerHTML = ''; });
const click = async (w: VueWrapper, text: string) => { await button(w, text).trigger('click'); await flushPromises(); };

const siteCell = (w: VueWrapper) => row(w).findAll('td')[0]!;
const summary = () => h.siteOptions[0].consumption;
const optionsReads = () => h.calls.filter(c => c.path.includes('/upstream-sites/options')).length;
describe('saved upstream account consumption', () => {
 it('shows distinct account amounts on the same site with a single site balance and no upstream reads', async () => {
  h.rates.push(rate('2', '分组乙')); h.connections.push(conn('102','2','分组乙'));
  h.today.keys.push({siteId:'site-a',keyId:'key-102',keyIds:['key-102'],todayAmount:1.75});
  h.totals.items.push({...h.totals.items[0],keyId:'key-102',totalAmount:9});
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：6.25');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  expect(siteCell(w).text()).toContain('整站余额：24.00');
  expect(row(w,'分组乙').findAll('td')[0]!.text()).toContain('今日消费：1.75');
  expect(row(w,'分组乙').findAll('td')[0]!.text()).toContain('累计消费：9.00');
  expect(h.calls.filter(c => c.path.includes('/upstream-sites')).map(c => c.path)).toEqual(['/api/upstream-sites/options']);
  expect(h.calls.find(c=>c.path.includes('upstream-key-usage-today'))?.path).toBe('/api/dashboard/upstream-key-usage-today?includeZero=1');
  expect(h.calls.find(c=>c.path.includes('upstream-key-recorded-totals'))?.signal).toBeInstanceOf(AbortSignal);
 });
 it('preserves zero and distinguishes legacy missing cumulative amount', async () => {
  h.today.keys[0].todayAmount=0; h.totals.items[0].totalAmount=0;
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：0.00');
  expect(siteCell(w).text()).toContain('累计消费：0.00');
  h.totals.items=[];
  await click(w,'刷新数据');
  expect(siteCell(w).text()).toContain('累计消费：—');
  expect(siteCell(w).text()).not.toContain('累计消费：0.00');
 });
 it.each([0, -1, Infinity])('does not invent money with invalid recharge rate %s', async recharge => {
  summary().rechargeRate = recharge; h.today.sites=[]; h.today.keys=[];
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：—');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  expect(siteCell(w).text()).toContain('整站余额：—');
 });
 it('keeps today failure reason and retained marker, cumulative remains readable', async () => {
  h.today.sites[0].status='retained'; h.today.sites[0].errorKey='network';
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：6.25（上次读取）');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  const daily = siteCell(w).find('[data-account-today-consume]');
  expect(daily.attributes('title')).toContain('上游');
  expect(daily.attributes('title')).toContain('上游管理');
  expect(button(w,'取消对接').element).toHaveProperty('disabled',false);
 });
 it('does not display an expired daily amount, cumulative is not cleared', async () => {
  h.today.date = '2030-12-31';
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：—');
  expect(siteCell(w).text()).not.toContain('6.25');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
 });
 it('refresh reads updated saved values, pagination and search add no money request', async () => {
  const w = await open();
  await click(w,'下一页'); expect(optionsReads()).toBe(1);
  h.totals.items[0].totalAmount = 60;
  await click(w,'刷新数据');
  expect(optionsReads()).toBe(2);
  expect(siteCell(w).text()).toContain('累计消费：60.00');
  const search = w.find('input[type="text"]');
  await search.setValue('分组'); await new Promise(r => setTimeout(r,320)); await flushPromises();
  expect(optionsReads()).toBe(2);
 });
 it('failed cache refresh keeps old amounts with warning, then recovers', async () => {
  const w=await open(); h.optionsFail=true;
  await click(w,'刷新数据');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  expect(w.findAll('[role="alert"]').some(a=>a.text().includes('网站名称缓存无法读取'))).toBe(true);
  h.optionsFail=false; h.totals.items[0].totalAmount=50;
  await click(w,'刷新数据');
  expect(siteCell(w).text()).toContain('累计消费：50.00');
 });
 it('clears prior workspace consumption while the next workspace loads', async () => {
  const w=await open();
  h.siteOptions=[]; h.totalsRead=()=>new Promise(()=>{}); h.todayRead=()=>new Promise(()=>{});
  h.currentAccount.value={ id:'other-workspace', platform:'sub2api' };
  await flushPromises();
  expect(w.text()).not.toContain('40.00');
  expect(siteCell(w).text()).toContain('累计消费：—');
 });
 it('places the explanation outside table after pagination and stacks existing buttons', async () => {
  h.connections[0].provisioningMode = 'managed';
  const w=await open();
  const hint=w.find('[data-account-consumption-hint]');
  expect(hint.text()).toBe('今日消费和累计消费是该主站账号对应上游 Key 的金额；累计只统计 Hub 按新加坡日期已保存的天数，不是上游账单全部历史；整站余额来自上游管理上次同步。页面不会重新查询上游或修改主站。今日消费、整站余额需要更新时，请先在上游管理同步，再点‘刷新数据’；累计消费需要更新时，请到首页成本区点‘刷新账号统计’，再点‘刷新数据’。');
  expect(hint.element.closest('table')).toBeNull();
  expect(hint.element.compareDocumentPosition(button(w,'下一页').element) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy();
  const edit=button(w,'修改主站分组'), disconnect=button(w,'取消对接');
  expect(edit.element.parentElement).toBe(disconnect.element.parentElement);
  expect(edit.element.parentElement!.classList.contains('flex-col')).toBe(true);
  expect(edit.element.parentElement!.classList.contains('items-stretch')).toBe(true);
  await edit.trigger('click'); await flushPromises();
  expect(w.find('[role="dialog"]').exists()).toBe(true);
 });
});
