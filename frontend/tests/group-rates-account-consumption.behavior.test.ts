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
    }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false, siteOptions: [] as any[], ownGroups: null as any, attention: [] as any[], currentAccount: null as any }));
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
    h.siteOptions = [{ id: 'site-a', name: '范围甲', consumption: { todayConsume: { value: 3.125, display: '3.125' }, totalConsume: { value: 20, display: '20' }, rechargeRate: 2, todayConsumeDate: '2031-01-01', todayConsumeTimezone: 'Asia/Singapore', todayConsumeStatus: 'ok' } }, { id: 'site-b', name: '范围乙' }];
    vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date('2031-01-01T00:00:00Z'));
    h.attention = [];
    h.ownGroups = async (workspace: string) => json({ workspaceAdminAccountId: workspace, groups: [{ id: '10', name: '新名', multiplier: 1.2, status: 'active' }] });
    vi.mocked(handleAuthExpired).mockClear();
    h.check = async () => json(result());
    h.local = async () => json({ workspaceAdminAccountId: h.currentAccount.value.id, items: h.connections });
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
        const path = String(url);
        const body = init.body ? JSON.parse(String(init.body)) : null;
        h.calls.push({ path, body, signal: init.signal });
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
afterEach(() => { for (const w of wrappers.splice(0))
    w.unmount(); vi.useRealTimers(); vi.unstubAllGlobals(); document.body.innerHTML = ''; });
const click = async (w: VueWrapper, text: string) => { await button(w, text).trigger('click'); await flushPromises(); };

const siteCell = (w: VueWrapper) => row(w).findAll('td')[0]!;
const summary = () => h.siteOptions[0].consumption;
const optionsReads = () => h.calls.filter(c => c.path.includes('/upstream-sites/options')).length;
describe('saved upstream account consumption', () => {
 it('shows converted daily and cumulative amounts below name without balance or extra upstream reads', async () => {
  h.rates.push(rate('2', '分组乙'));
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：6.25');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  expect(siteCell(w).text()).not.toContain('余额');
  expect(row(w,'分组乙').findAll('td')[0]!.text()).toBe(siteCell(w).text());
  expect(h.calls.filter(c => c.path.includes('/upstream-sites')).map(c => c.path)).toEqual(['/api/upstream-sites/options']);
 });
 it('preserves zero and distinguishes legacy missing cumulative amount', async () => {
  summary().todayConsume.value = 0; summary().totalConsume.value = 0;
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：0.00');
  expect(siteCell(w).text()).toContain('累计消费：0.00');
  delete summary().totalConsume;
  await click(w,'刷新数据');
  expect(siteCell(w).text()).toContain('累计消费：—');
  expect(siteCell(w).text()).not.toContain('累计消费：0.00');
 });
 it.each([0, -1, Infinity])('does not invent money with invalid recharge rate %s', async recharge => {
  summary().rechargeRate = recharge;
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：—');
  expect(siteCell(w).text()).toContain('累计消费：—');
 });
 it('keeps today failure reason and retained marker, cumulative remains readable', async () => {
  summary().todayConsumeStatus = 'unreadable';
  summary().todayConsumeErrorDetail = '账号消费读取失败：上游请求超时';
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：6.25（上次读取）');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
  const daily = siteCell(w).find('[data-account-today-consume]');
  expect(daily.attributes('title')).toContain('上游请求超时');
  expect(daily.attributes('title')).toContain('上游管理');
  expect(button(w,'取消对接').element).toHaveProperty('disabled',false);
 });
 it('does not display an expired daily amount, cumulative is not cleared', async () => {
  summary().todayConsumeDate = '2030-12-31';
  const w = await open();
  expect(siteCell(w).text()).toContain('今日消费：—');
  expect(siteCell(w).text()).not.toContain('6.25');
  expect(siteCell(w).text()).toContain('累计消费：40.00');
 });
 it('refresh reads updated saved values, pagination and search add no money request', async () => {
  const w = await open();
  await click(w,'下一页'); expect(optionsReads()).toBe(1);
  summary().totalConsume.value = 30;
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
  h.optionsFail=false; summary().totalConsume.value=25;
  await click(w,'刷新数据');
  expect(siteCell(w).text()).toContain('累计消费：50.00');
 });
 it('clears prior workspace consumption while the next workspace loads', async () => {
  const w=await open();
  h.siteOptions=[];
  h.currentAccount.value={ id:'other-workspace', platform:'sub2api' };
  await flushPromises();
  expect(w.text()).not.toContain('40.00');
  expect(siteCell(w).text()).toContain('累计消费：—');
 });
 it('places the explanation outside table after pagination and stacks existing buttons', async () => {
  h.connections[0].provisioningMode = 'managed';
  const w=await open();
  const hint=w.find('[data-account-consumption-hint]');
  expect(hint.text()).toBe('显示上游管理中该账号已保存的消费金额，不会重新查询上游或修改主站；需要更新时，请先在上游管理同步该账号，再点本页“刷新数据”。');
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
