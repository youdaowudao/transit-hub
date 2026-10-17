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
    }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false, ownGroups: null as any, attention: [] as any[], currentAccount: null as any }));
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
            return h.optionsFail ? json({ message: '网站名称缓存无法读取，请刷新重试' }, 500) : json({ workspaceAdminAccountId: h.currentAccount.value.id, items: [{ id: 'site-a', name: '范围甲' }, { id: 'site-b', name: '范围乙' }] });
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
const salesCalls = () => h.calls.filter(c => c.path.includes('/own-group-rates'));
const cells = (w: VueWrapper, name = '分组甲') => { expect(w.findAll('thead th').map(x => x.text())).toContain('销售倍率'); const td = row(w, name).findAll('td'); return [td[5]!, td[6]!]; };
const profitCell = (w: VueWrapper, name = '分组甲') => row(w, name).findAll('td')[7]!;
const profitLines = (w: VueWrapper, name = '分组甲') => profitCell(w, name).findAll('[data-profit-margin-line]');
const click = async (w: VueWrapper, text: string) => { expect(button(w, text)).toBeDefined(); await button(w, text).trigger('click'); await flushPromises(); };
const groups = (items: any[]) => { h.ownGroups = async (workspace: string) => json({ workspaceAdminAccountId: workspace, groups: items }); };
const fail = () => { h.ownGroups = async () => json({ message: '读取主站分组失败', detail: 'Hub 暂时无法连接主站', next: '请稍后点“刷新数据”重试；仍失败时到主站后台确认分组列表能否打开' }, 502); };
const attach = (ids: any = ['10'], names: any = ['旧名']) => { h.connections = [{ ...conn('101', '1', '分组甲'), ownGroupIds: ids, ownGroupNames: names }]; };
describe('sales columns confirmed contract', () => {
    it.each(['none', 'pricing', 'missing'])('V1 blank columns for %s', async (mode) => { h.connections = mode === 'missing' ? [conn('101', '1', '分组甲', 'missing')] : []; h.rates[0].pricingMapped = mode === 'pricing'; const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['', '']); if (mode === 'missing')
        expect(row(w).text()).toContain('主站账号已删除'); });
    it('V2 current name and multiplier', async () => { attach(); const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['新名', '1.2x']); expect(cells(w)[0]!.text()).not.toContain('旧名'); });
    it('V3 deduplicates, sorts and aligns multiple groups', async () => { attach(['10', '11']); h.connections.push({ ...conn('102', '1', '分组甲'), ownGroupIds: ['11', '12'] }); groups([{ id: '10', name: '乙', multiplier: 1, status: '' }, { id: '11', name: '甲', multiplier: 2, status: '' }, { id: '12', name: '丙', multiplier: 3, status: '' }]); const w = await open(); expect(cells(w)[0]!.findAll('[data-own-group-line]').map(c => c.text())).toEqual(['甲', '乙', '丙'].sort((a, b) => a.localeCompare(b))); const order = cells(w)[0]!.findAll('[data-own-group-line]').map(c => c.text()); expect(cells(w)[1]!.findAll('[data-sales-line]').map(c => c.text())).toEqual(order.map(n => ({ 甲: '2x', 乙: '1x', 丙: '3x' })[n])); });
    it('V4 null rates and inactive suffixes', async () => { attach(['10', '11', '12']); groups([{ id: '10', name: 'A', multiplier: null, status: 'active' }, { id: '11', name: 'B', multiplier: .8, status: 'inactive' }, { id: '12', name: 'C', multiplier: null, status: 'inactive' }]); const w = await open(); expect(cells(w)[1]!.findAll('[data-sales-line]').map(c => c.text())).toEqual(['主站未返回倍率', '0.8x（已停用）', '主站未返回倍率（已停用）']); expect(cells(w)[1]!.text()).not.toContain('0x'); });
    it('V5 absent group preserves recorded name', async () => { attach(['13'], ['历史组']); const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['历史组', '读取时主站没有这个分组']); });
    it.each([[null, null, '对接记录里没有主站分组', ''], [[], [' 只有名字 '], '只有名字', '对接记录没有分组编号'], [['14'], [], '分组编号 14', '读取时主站没有这个分组']])('V6 incomplete records %j', async (ids, names, name, value) => { attach(ids, names); const w = await open(); expect(cells(w).map(c => c.text())).toEqual([name, value]); });
    it('V7 pending read keeps recorded name', async () => { attach(); const p = deferred<Response>(); h.ownGroups = () => p.promise; const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['旧名', '读取中']); p.resolve(json({ workspaceAdminAccountId: 'ws-scoped', groups: [{ id: '10', name: '新名', multiplier: 1.2, status: 'active' }] })); await flushPromises(); expect(cells(w)[1]!.text()).toBe('1.2x'); });
    it('V8 failure preserves list, recorded name and actions', async () => { attach(); fail(); const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['旧名', '暂时读不到']); expect(w.findAll('[role="alert"]').map(c => c.text())).toContain('销售倍率暂时读不到：Hub 暂时无法连接主站。销售倍率先不显示；这次销售倍率读取本身不会修改主站。请稍后点“刷新数据”重试；仍失败时到主站后台确认分组列表能否打开'); expect(button(w, '取消对接').element).toHaveProperty('disabled', false); });
    it('V9 unknown local connections', async () => { h.local = async () => json({ message: '读取被中断' }, 500); const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['暂时未知', '暂时未知']); });
    it('V10 rejects another workspace response', async () => { h.ownGroups = async () => json({ workspaceAdminAccountId: 'other', groups: [] }); const w = await open(); expect(w.text()).toContain('返回的资料属于另一个工作区，已忽略'); expect(w.text()).toContain('请重新进入本页'); expect(cells(w)[1]!.text()).toBe('暂时读不到'); });
    it.each(['success', 'failure'])('V11 ignores old workspace late %s', async (mode) => { attach(); const p = deferred<Response>(); h.ownGroups = () => p.promise; const w = await open(); const oldSignal = salesCalls()[0]!.signal!; expect(oldSignal.aborted).toBe(false); groups([{ id: '10', name: 'B分组', multiplier: 2, status: '' }]); h.currentAccount.value = { id: 'ws-b', platform: 'sub2api' }; await flushPromises(); expect(oldSignal.aborted).toBe(true); p.resolve(mode === 'success' ? json({ workspaceAdminAccountId: 'ws-scoped', groups: [{ id: '10', name: 'A分组', multiplier: 99, status: '' }] }) : json({ message: 'A失败不得显示' }, 500)); await flushPromises(); expect(cells(w).map(c => c.text())).toEqual(['B分组', '2x']); expect(profitCell(w).text()).toBe('50.00%'); expect(w.text()).not.toContain('A失败不得显示'); expect(w.text()).not.toContain('A分组'); });
    it('V12 local controls do not reread, refresh does', async () => { const w = await open(); await click(w, '下一页'); await w.get('input[name="groupRateSearch"]').setValue('分组'); await new Promise(r => setTimeout(r, 350)); await flushPromises(); await w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!.setValue('groupNameAsc'); await flushPromises(); const filter = w.findAll('button').find(b => b.text().startsWith('未对接')); if (filter)
        await filter.trigger('click'); h.route.query = { siteId: 'site-b' }; await flushPromises(); expect(salesCalls()).toHaveLength(1); await click(w, '刷新数据'); expect(salesCalls()).toHaveLength(2); });
    it('V13 row unlink rereads, maintenance unlink does not', async () => { h.attention = [{ connection: { ...conn('202', 'gone', '维护组'), ownGroupNames: ['组'] }, reason: 'group_not_found', detail: '保存资料未找到分组', lastSyncedAt: null }]; const w = await open(); await click(w, '取消对接'); await click(w, '确定'); expect(salesCalls()).toHaveLength(2); h.attention = [{ connection: { ...conn('202', 'gone', '维护组'), ownGroupNames: ['组'] }, reason: 'group_not_found', detail: '保存资料未找到分组', lastSyncedAt: null }]; await click(w, '刷新数据'); const before = salesCalls().length; await click(w, '查看已有对接'); await click(w, '处理对接'); await click(w, '确定'); expect(salesCalls()).toHaveLength(before); });
    it('V14 unavailable saved login never expires Hub', async () => { h.ownGroups = async () => json({ message: '读取主站分组失败', detail: 'Hub 没有这个工作区可用的主站登录资料', next: '请到“我的站点”重新连接主站，再点“刷新数据”' }, 409); const w = await open(); expect(w.text()).toContain('Hub 没有这个工作区可用的主站登录资料'); expect(w.text()).toContain('我的站点'); expect(handleAuthExpired).not.toHaveBeenCalled(); });
    it('V15 mapping options remain lazy and independent', async () => { h.connections = []; const w = await open(); expect(h.calls.filter(c => c.path.includes('mapping-options'))).toHaveLength(0); await click(w, '配置对接'); expect(h.calls.filter(c => c.path.includes('mapping-options'))).toHaveLength(1); expect(w.get('[role="dialog"]').text()).toContain('弹窗分组'); });
    it.each(['network', 'multiplier', 'message'])('V16 errors %s', async (mode) => { h.ownGroups = mode === 'network' ? async () => { throw new TypeError('offline'); } : mode === 'message' ? async () => json({ message: '读取分组遇到问题' }, 500) : async () => json({ workspaceAdminAccountId: 'ws-scoped', groups: [{ id: '10', name: '组', multiplier: '1.2', status: '' }] }); const w = await open(); expect(w.text()).toContain(mode === 'network' ? 'Hub 后端没有响应' : mode === 'message' ? '读取分组遇到问题' : 'Hub 返回的主站分组资料不完整，无法确认'); expect(w.text()).toContain(mode === 'network' ? '请确认 Hub 服务正在运行，再点“刷新数据”' : '请点“刷新数据”重试'); });
    it.each(['none', 'missing', 'active', 'mixed', 'pricing', 'unknown'])('V17 tag and columns agree: %s', async (mode) => { h.connections = mode === 'active' || mode === 'mixed' ? [conn('101', '1', '分组甲')] : []; if (mode === 'missing' || mode === 'mixed')
        h.connections.push(conn('102', '1', '分组甲', 'missing')); if (mode === 'unknown')
        h.local = async () => json({}, 500); const w = await open(); const connected = row(w).findAll('span').some(s => s.text() === '已对接'); expect(connected).toBe(['active', 'mixed'].includes(mode)); if (connected)
        expect(cells(w)[0]!.text()).not.toBe('');
    else if (mode !== 'unknown')
        expect(cells(w).map(c => c.text())).toEqual(['', '']); });
    it('V18 exact help text and no developer words', async () => { const w = await open(); expect(w.text()).toContain('“对接的主站分组”按 Hub 保存的对接记录，显示这个上游分组导入或关联的主站账号所在的分组；在主站后台直接改了账号分组，这里不会跟着变。没有对接的分组这三列留空，只标“已用于调价”的也留空。'); expect(w.text()).toContain('“销售倍率”是这些分组在主站上的默认倍率，可与左边的换算后成本倍率对比；不含单独给用户设置的专属倍率，上游和主站的模型标准价不同时只能粗略对比。进入本页或点“刷新数据”时从主站读取，翻页和筛选不重新读取；自动调价或调价活动改价后，请点“刷新数据”查看。读取只查看，不会修改主站。'); expect(w.text()).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/); });
    it('V19 retains original columns and inserts two after cost', async () => { const w = await open(); expect(w.findAll('thead th').map(c => c.text())).toEqual(['站点名称', '分组名称', '分组类型', '站点平台', '换算后成本倍率', '对接的主站分组', '销售倍率', '利润率', '涨跌幅', '更新时间', '操作']); });
    it.each(['missing-workspace', 'null-workspace', 'number-workspace', 'groups', 'duplicate', 'empty-id', 'long-id', 'control-id', 'name', 'status', 'missing-multiplier', 'infinite'])('V20 malformed payload %s', async (mode) => { const p: any = { workspaceAdminAccountId: 'ws-scoped', groups: [{ id: '10', name: '组', multiplier: 1.2, status: '' }] }; const g = p.groups[0]; switch (mode) {
        case 'missing-workspace':
            delete p.workspaceAdminAccountId;
            break;
        case 'null-workspace':
            p.workspaceAdminAccountId = null;
            break;
        case 'number-workspace':
            p.workspaceAdminAccountId = 1;
            break;
        case 'groups':
            p.groups = {};
            break;
        case 'duplicate':
            p.groups.push({ ...g });
            break;
        case 'empty-id':
            g.id = '';
            break;
        case 'long-id':
            g.id = 'x'.repeat(129);
            break;
        case 'control-id':
            g.id = 'x\u0001';
            break;
        case 'name':
            g.name = null;
            break;
        case 'status':
            g.status = [];
            break;
        case 'missing-multiplier':
            delete g.multiplier;
            break;
    } ; h.ownGroups = async () => mode === 'infinite' ? new Response('{"workspaceAdminAccountId":"ws-scoped","groups":[{"id":"10","name":"组","multiplier":1e400,"status":""}]}') : json(p); const w = await open(); expect(w.text()).toContain('Hub 返回的主站分组资料不完整，无法确认'); expect(cells(w)[1]!.text()).toBe('暂时读不到'); });
    it.each(['success', 'failure'])('V21 latest refresh owns result after old %s', async (mode) => { const w = await open(); const p = deferred<Response>(); h.ownGroups = () => p.promise; await click(w, '刷新数据'); const oldSignal = salesCalls().at(-1)!.signal!; expect(oldSignal.aborted).toBe(false); groups([{ id: '10', name: '最新', multiplier: 2, status: '' }]); await click(w, '刷新数据'); expect(oldSignal.aborted).toBe(true); p.resolve(mode === 'success' ? json({ workspaceAdminAccountId: 'ws-scoped', groups: [{ id: '10', name: '迟到', multiplier: 99, status: '' }] }) : json({ message: '迟到错误' }, 500)); await flushPromises(); expect(cells(w).map(c => c.text())).toEqual(['最新', '2x']); expect(profitCell(w).text()).toBe('50.00%'); expect(w.text()).not.toContain('迟到'); });
    it.each(['success', 'failure'])('V22 unmount aborts and never adopts late %s', async mode => {
        const pending = deferred<Response>(); h.ownGroups = () => pending.promise;
        const w = await open(); const signal = salesCalls()[0]!.signal!;
        const vm = w.vm as unknown as { ownGroupRatesState: string; ownGroupRatesById: Map<string, unknown>; ownGroupRatesError: string };
        const state = () => ({ state: vm.ownGroupRatesState, groups: Array.from(vm.ownGroupRatesById), error: vm.ownGroupRatesError });
        expect(state().state).toBe('loading'); expect(signal.aborted).toBe(false);
        w.unmount(); expect(signal.aborted).toBe(true);
        const cleared = state(); expect(cleared).toEqual({ state: 'idle', groups: [], error: '' });
        const calls = h.calls.length;
        pending.resolve(mode === 'success'
            ? json({ workspaceAdminAccountId: 'ws-scoped', groups: [{ id: '10', name: '不得采用', multiplier: 99, status: 'active' }] })
            : json({ message: '卸载后不得显示的错误', detail: '卸载后不得采用的原因', next: '不应出现' }, 502));
        await flushPromises(); expect(state()).toEqual(cleared);
        expect(h.calls).toHaveLength(calls); expect(document.querySelector('[data-sales-line]')).toBeNull(); expect(document.querySelector('[data-profit-margin-line]')).toBeNull();
    });
    it.each(['initial', 'lost'])('S10 no request of any kind without workspace: %s', async mode => {
        if (mode === 'initial') h.currentAccount.value = null;
        const w = await open();
        const before = vi.mocked(fetch).mock.calls.length;
        if (mode === 'lost') {
            h.currentAccount.value = null; await flushPromises();
            expect(vi.mocked(fetch).mock.calls).toHaveLength(before);
        }
        if (mode === 'initial') expect(before).toBe(0);
        await click(w, '刷新数据');
        await w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!.setValue('groupNameAsc');
        await flushPromises(); h.route.query = { siteId: 'site-b' }; await flushPromises();
        expect(vi.mocked(fetch).mock.calls).toHaveLength(before);
        expect(w.text()).toContain('尚未选定当前工作区'); expect(w.find('tbody tr').exists()).toBe(false);
    });
    it.each(['pending', 'failed'])('V23 no reread on site or deferred route after %s', async (mode) => { const p = deferred<Response>(); if (mode === 'pending')
        h.ownGroups = () => p.promise;
    else
        fail(); h.connections = []; const w = await open(); h.route.query = { siteId: 'site-b' }; await flushPromises(); expect(salesCalls()).toHaveLength(1); await click(w, '配置对接'); h.route.query = { siteId: 'site-a' }; await flushPromises(); await click(w, '取消'); expect(salesCalls()).toHaveLength(1); groups([]); await click(w, '刷新数据'); expect(salesCalls()).toHaveLength(2); p.resolve(json({ workspaceAdminAccountId: 'ws-scoped', groups: [] })); await flushPromises(); });
    it.each([true, false])('V24 pending sales does not block actions connected=%s', async (connected) => { h.ownGroups = () => deferred<Response>().promise; if (!connected)
        h.connections = []; const w = await open(); expect(button(w, connected ? '取消对接' : '配置对接').element).toHaveProperty('disabled', false); });
    it.each(['import', 'bind'])('V25 %s success rereads sales', async (mode) => {
        h.connections = [];
        const original = fetch;
        vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
            const path = String(input);
            if (path.includes('/upstream-keys?'))
                return json([{ id: '207', name: '既有 Key', status: 'active', groupId: '1', groupName: '分组甲' }]);
            if (path.includes('/admin-resources?'))
                return json([{ id: '101', name: '既有主站账号', platform: 'openai', type: 'apikey', status: 'active', groupIds: ['10'] }]);
            if (path.endsWith('/real-connect') || path.endsWith('/real-bind')) {
                const request = JSON.parse(String(init?.body));
                h.calls.push({ path, body: request });
                const connection = { ...conn('101', '1', '分组甲'), id: 'c'.repeat(32), upstreamKeyId: '207', ownGroupIds: ['10'], ownGroupNames: ['弹窗分组'], provisioningMode: mode === 'bind' ? 'existing' : 'managed' };
                h.connections = [connection];
                return json({ workspaceAdminAccountId: h.currentAccount.value.id, configurationStatus: 'confirmed', connection, configuration: { observation: 'creation', adminAccountId: '101', name: '已创建账号', platform: 'openai', priority: 100, concurrency: 50, passthrough: false, poolMode: true, upstreamBillingProbeEnabled: true, ownGroups: [{ id: '10', name: '弹窗分组' }], modelState: 'synced', models: ['verified-model'] } });
            }
            return original(input, init);
        }));
        const w = await open();
        expect(salesCalls()).toHaveLength(1);
        await click(w, '配置对接');
        const dialog = w.get('[role="dialog"]');
        if (mode === 'import')
            await dialog.findAll('label').find(l => l.text().includes('弹窗分组'))!.get('input[type="checkbox"]').setValue(true);
        else {
            await dialog.findAll('button').find(b => b.text().includes('使用已有资源'))!.trigger('click');
            await flushPromises();
            await dialog.get('input[type="radio"]').setValue(true);
            await dialog.get('#existing-admin-group').setValue('10');
            await flushPromises();
            await dialog.findAll('input[type="radio"]').at(-1)!.setValue(true);
        }
        await dialog.get('form').trigger('submit');
        await flushPromises();
        expect(salesCalls()).toHaveLength(2);
        expect(h.calls.filter(c => c.path.endsWith(mode === 'import' ? '/real-connect' : '/real-bind'))).toHaveLength(1);
        expect(row(w).text()).toContain('已对接');
        expect(profitCell(w).text()).toBe('16.67%');
    });
    it('V25 missing-row cleanup rereads sales', async () => { h.connections[0].status = 'missing'; const w = await open(); await click(w, '清理本地失效对接'); await click(w, '确定'); expect(salesCalls()).toHaveLength(2); });
    it('V26 trims IDs and status, deduplicates name-only entries', async () => { attach([' 10 ', '', '11'], ['旧', '同名', '旧乙']); h.connections.push({ ...conn('102', '1', '分组甲'), ownGroupIds: [], ownGroupNames: ['同名'] }); groups([{ id: '10', name: 'A', multiplier: 1, status: ' Inactive ' }, { id: '11', name: 'B', multiplier: 2, status: 'ACTIVE' }]); const w = await open(); expect(cells(w)[0]!.findAll('[data-own-group-line]').map(c => c.text())).toEqual(['A', 'B', '同名']); expect(cells(w)[1]!.findAll('[data-sales-line]').map(c => c.text())).toEqual(['1x（已停用）', '2x', '对接记录没有分组编号']); });
    it('V27 both failures remain consistent', async () => { fail(); h.local = async () => json({}, 500); const w = await open(); expect(cells(w).map(c => c.text())).toEqual(['暂时未知', '暂时未知']); expect(w.text()).toContain('本页对接资料暂时无法读取'); expect(w.text()).toContain('销售倍率暂时读不到'); expect(w.text()).not.toContain('仍按 Hub 记录显示'); });
    it.each(['admin.mySites.errors.request', 'admin.nonexistent.key'])('V28 translates or falls back error keys %s', async (message) => { h.ownGroups = async () => json({ message }, 500); const w = await open(); expect(w.text()).not.toContain(message); expect(w.text()).toContain(message.includes('nonexistent') ? '主站分组读取没有完成' : '导入参数无效，请检查平台和分组后重试。'); });
    it.each(['json401', 'empty401', 'text401', 'key403'])('V29 Hub auth expiry %s', async (mode) => { vi.stubGlobal('fetch', vi.fn(async () => mode === 'empty401' ? new Response('', { status: 401 }) : mode === 'text401' ? new Response('bad', { status: 401 }) : json({ message: 'auth.errors.unauthorized' }, mode === 'key403' ? 403 : 401))); await expect(readOwnGroupRates('ws-scoped')).rejects.toThrow('auth.errors.unauthorized'); expect(handleAuthExpired).toHaveBeenCalledTimes(1); });
});

describe('group rates profit margin uses pricing mapping convention', () => {
    it.each([
        [0.8, '20.00%', 'text-emerald-600'],
        [1, '0.00%', 'text-warning'],
        [1.2, '-20.00%', 'text-destructive'],
        [2.5, '-150.00%', 'text-destructive'],
        [0, '100.00%', 'text-emerald-600'],
        [0.999999, '0.00%', 'text-emerald-600'],
        [1.000001, '-0.00%', 'text-destructive'],
    ])('T1 cost %s yields %s with its unrounded sign', async (cost, text, tone) => {
        h.rates[0].currentMultiplier = cost;
        groups([{ id: '10', name: '销售组', multiplier: 1, status: 'active' }]);
        const w = await open();
        expect(profitLines(w).map(line => line.text())).toEqual([text]);
        expect(profitLines(w)[0]!.classes()).toContain(tone);
        expect(profitCell(w).find('button').exists()).toBe(false);
    });

    it('T2 uses the existing rounded converted cost without another conversion', async () => {
        Object.assign(h.rates[0], { currentMultiplier: 0.8, upstreamMultiplier: 0.2666, rechargeRate: 3 });
        groups([{ id: '10', name: '销售组', multiplier: 1, status: '' }]);
        const w = await open();
        expect(profitCell(w).text()).toBe('20.00%');
        expect(profitCell(w).text()).not.toContain('20.02%');
    });

    it.each([
        ['missing sale', null, 1], ['zero sale', 0, 1], ['negative sale', -1, 1],
        ['missing cost', 1, null], ['overflow', 1e-308, 1e308],
    ])('T3 unavailable %s remains a neutral placeholder', async (_case, sale, cost) => {
        h.rates[0].currentMultiplier = cost;
        groups([{ id: '10', name: '销售组', multiplier: sale, status: '' }]);
        const w = await open();
        expect(profitCell(w).text()).toBe('—');
        expect(profitLines(w)[0]!.classes()).toContain('text-muted-foreground');
        expect(profitCell(w).text()).not.toMatch(/NaN|Infinity|∞|%/);
    });

    it('T3 retains inactive suffixes for both numeric and missing margins', async () => {
        h.rates[0].currentMultiplier = 0.8;
        attach(['10', '11']);
        groups([{ id: '10', name: 'A', multiplier: 1, status: ' inactive ' }, { id: '11', name: 'B', multiplier: null, status: 'inactive' }]);
        const w = await open();
        expect(profitLines(w).map(line => line.text())).toEqual(['20.00%（已停用）', '—（已停用）']);
        expect(cells(w)[1]!.findAll('[data-sales-line]').map(line => line.text())).toEqual(['1x（已停用）', '主站未返回倍率（已停用）']);
    });

    it('T4 deduplicates IDs, retains equal names and aligns margins for each upstream row', async () => {
        h.rates = [{ ...rate('1', '分组甲'), currentMultiplier: 0.8 }, { ...rate('2', '分组乙'), currentMultiplier: 1.2 }];
        attach(['12', '10']);
        h.connections.push({ ...conn('102', '1', '分组甲'), ownGroupIds: ['10', '11'] }, conn('103', '2', '分组乙'));
        groups([{ id: '12', name: 'B', multiplier: 4, status: '' }, { id: '11', name: 'A', multiplier: 2, status: '' }, { id: '10', name: 'A', multiplier: 1, status: '' }]);
        const w = await open();
        expect(cells(w)[0]!.findAll('[data-own-group-line]').map(line => line.text())).toEqual(['A', 'A', 'B']);
        expect(cells(w)[1]!.findAll('[data-sales-line]').map(line => line.text())).toEqual(['1x', '2x', '4x']);
        expect(profitLines(w).map(line => line.text())).toEqual(['20.00%', '60.00%', '80.00%']);
        expect(profitLines(w, '分组乙').map(line => line.text())).toEqual(['-20.00%']);
    });

    it.each(['none', 'pricing', 'missing'])('T5 no effective connection stays entirely blank: %s', async mode => {
        h.connections = mode === 'missing' ? [conn('101', '1', '分组甲', 'missing')] : [];
        h.rates[0].pricingMapped = mode === 'pricing';
        const w = await open();
        expect(profitCell(w).text()).toBe('');
        expect(profitLines(w)).toHaveLength(0);
    });

    it.each(['no groups', 'name only', 'absent group', 'unknown'])('T5 incomplete %s cannot produce a percentage', async mode => {
        if (mode === 'no groups') attach([], []);
        if (mode === 'name only') attach([], ['只有名字']);
        if (mode === 'absent group') attach(['missing'], ['不存在的组']);
        if (mode === 'unknown') h.local = async () => json({ message: '对接资料读取被中断' }, 500);
        const w = await open();
        expect(profitCell(w).text()).toBe('—');
        expect(profitCell(w).text()).not.toContain('%');
        if (mode === 'unknown') expect(cells(w).map(cell => cell.text())).toEqual(['暂时未知', '暂时未知']);
    });

    it('T5 missing connections do not add a margin beside an active connection', async () => {
        h.rates[0].currentMultiplier = 0.8;
        h.connections.push({ ...conn('102', '1', '分组甲', 'missing'), ownGroupIds: ['11'] });
        groups([{ id: '10', name: '有效组', multiplier: 1, status: '' }, { id: '11', name: '失效组', multiplier: 99, status: '' }]);
        const w = await open();
        expect(profitLines(w).map(line => line.text())).toEqual(['20.00%']);
        expect(cells(w)[0]!.text()).not.toContain('失效组');
    });

    it('T6 refresh removes old percentages while reading, failing and recovering', async () => {
        h.rates[0].currentMultiplier = 0.8;
        groups([{ id: '10', name: '销售组', multiplier: 1, status: '' }]);
        const w = await open();
        expect(profitCell(w).text()).toBe('20.00%');
        const pending = deferred<Response>();
        h.ownGroups = () => pending.promise;
        await click(w, '刷新数据');
        expect(profitCell(w).text()).toBe('—');
        expect(cells(w)[1]!.text()).toBe('读取中');
        pending.resolve(json({ message: '读取主站分组失败', detail: '验收读取超时', next: '请点“刷新数据”重试' }, 504));
        await flushPromises();
        expect(profitCell(w).text()).toBe('—');
        expect(w.text()).toContain('验收读取超时');
        expect(button(w, '取消对接').element).toHaveProperty('disabled', false);
        h.rates[0].currentMultiplier = 1.2;
        groups([{ id: '10', name: '销售组', multiplier: 1, status: '' }]);
        await click(w, '刷新数据');
        expect(profitCell(w).text()).toBe('-20.00%');
        expect(profitCell(w).text()).not.toBe('20.00%');
    });

    it('T6 previously known connections becoming unreadable hide the old margin', async () => {
        const w = await open();
        expect(profitCell(w).text()).toBe('16.67%');
        h.local = async () => json({ message: '本页资料已无法读取' }, 500);
        await click(w, '刷新数据');
        expect(profitCell(w).text()).toBe('—');
        expect(w.text()).toContain('本页对接资料暂时无法读取');
    });

    it('T9 renders exactly one new column and the agreed help', async () => {
        const w = await open();
        expect(w.findAll('thead th').map(th => th.text())).toEqual(['站点名称', '分组名称', '分组类型', '站点平台', '换算后成本倍率', '对接的主站分组', '销售倍率', '利润率', '涨跌幅', '更新时间', '操作']);
        expect(w.findAll('thead th')[7]!.find('button').exists()).toBe(false);
        expect(w.text()).toContain('利润率按（销售倍率－换算后成本倍率）÷销售倍率估算，与调价映射的口径相同。销售倍率为0、为负数或资料不全时显示“—”，可结合旁边的销售倍率和页面提示查看原因。只查看不会修改主站；上游改价后请先同步对应网站，主站改价后请点“刷新数据”。');
        expect(w.text()).toContain('没有对接的分组这三列留空，只标“已用于调价”的也留空。');
    });
});
