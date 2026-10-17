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
    }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false, ownGroups: null as any, attention: [] as any[], groupRead: null as any, groupWrite: null as any, currentAccount: null as any }));
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
    h.connections[0] = { ...h.connections[0], provisioningMode: 'managed', ownGroupNames: ['原分组'] };
    h.groupRead = async () => json({ workspaceAdminAccountId: 'ws-scoped', connection: h.connections[0], account: { id: '101', name: '主站测试账号', platform: 'openai' }, currentGroups: [{id:'10',name:'原分组'}], hubGroups: [{id:'10',name:'原分组'}], groups: [{id:'10',name:'原分组',status:'active'},{id:'11',name:'新分组',status:'active'}], consistent: true });
    h.groupWrite = async () => json({});
    h.attention = [];
    h.ownGroups = async (workspace: string) => json({ workspaceAdminAccountId: workspace, groups: [{ id: '10', name: '新名', multiplier: 1.2, status: 'active' }] });
    vi.mocked(handleAuthExpired).mockClear();
    h.check = async () => json(result());
    h.local = async () => json({ workspaceAdminAccountId: h.currentAccount.value.id, items: h.connections });
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
        const path = String(url);
        const body = init.body ? JSON.parse(String(init.body)) : null;
        h.calls.push({ path, body, signal: init.signal });
        if (path.includes('/main-site-groups')) return init.method === 'POST' ? h.groupWrite(path,body) : h.groupRead();
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
const click = async (w: VueWrapper, text: string) => { expect(button(w, text)).toBeDefined(); await button(w, text).trigger('click'); await flushPromises(); };
const groups = (items: any[]) => { h.ownGroups = async (workspace: string) => json({ workspaceAdminAccountId: workspace, groups: items }); };
const fail = () => { h.ownGroups = async () => json({ message: '读取主站分组失败', detail: 'Hub 暂时无法连接主站', next: '请稍后点“刷新数据”重试；仍失败时到主站后台确认分组列表能否打开' }, 502); };
const attach = (ids: any = ['10'], names: any = ['旧名']) => { h.connections = [{ ...conn('101', '1', '分组甲'), ownGroupIds: ids, ownGroupNames: names }]; };

const editor = (w: VueWrapper) => w.get('[role="dialog"]');
const selectTarget = async (w: VueWrapper) => {
    const checkbox = (name: string) => editor(w).findAll('label').find(l => l.text().includes(name))!.get('input[type="checkbox"]');
    await checkbox('原分组').setValue(false); await checkbox('新分组').setValue(true); await flushPromises();
};
const readState = (main: string, hub: string) => ({ workspaceAdminAccountId:'ws-scoped', connection:h.connections[0], account:{id:'101',name:'主站测试账号',platform:'openai'}, currentGroups:[{id:main,name:main==='10'?'原分组':'新分组'}], hubGroups:[{id:hub,name:hub==='10'?'原分组':'新分组'}], groups:[{id:'10',name:'原分组',status:'active'},{id:'11',name:'新分组',status:'active'}], consistent:main===hub });
const groupReads = () => h.calls.filter(c => c.path.includes('/main-site-groups') && !c.body);
const groupWrites = () => h.calls.filter(c => c.path.includes('/main-site-groups') && c.body);
const groupCheckbox = (w: VueWrapper, name: string) => editor(w).findAll('label').find(l => l.text().includes(name))!.get('input[type="checkbox"]');
const mutationResult = (outcome: string, message = '主站分组没有修改') => ({ workspaceAdminAccountId: 'ws-scoped', outcome, message, detail: '主站返回的具体原因', next: '请核对后重试', mainSiteGroups: [] });

describe('main-site groups entry and read behavior', () => {
    it('V1 managed active Sub2API has a secondary action before the unchanged disconnect action', async () => {
        const w = await open(); const buttons = row(w).findAll('button').map(b => b.text().trim());
        expect(buttons.indexOf('修改主站分组')).toBeLessThan(buttons.indexOf('取消对接'));
        expect(button(w, '修改主站分组').attributes('title')).toContain('会修改主站账号');
        expect(button(w, '取消对接').element).toHaveProperty('disabled', false);
    });
    it.each(['existing', 'legacy', 'missing', 'unconnected', 'newapi', 'missing-account', 'deleted'])('V1 keeps %s outside the editor and preserves old entry', async mode => {
        if (mode === 'existing' || mode === 'legacy') h.connections[0].provisioningMode = mode;
        if (mode === 'missing') h.connections[0].status = 'missing';
        if (mode === 'unconnected') h.connections = [];
        if (mode === 'newapi') h.connections[0].adminPlatform = 'newapi';
        if (mode === 'missing-account') h.connections[0].adminAccountId = '';
        if (mode === 'deleted') h.rates[0].deleted = true;
        const w = await open(); expect(button(w, '修改主站分组')).toBeUndefined(); expect(groupReads()).toHaveLength(0);
        if (mode === 'missing') expect(button(w, '清理本地失效对接')).toBeDefined();
        else if (mode === 'unconnected') expect(button(w, '配置对接')).toBeDefined();
        else if (mode !== 'deleted') expect(button(w, '取消对接')).toBeDefined();
    });
    it('V1 status empty remains visible with the same existing operation guard as disconnect', async () => {
        h.connections[0].status = ''; const w = await open(); expect(button(w, '修改主站分组')).toBeDefined();
        expect((button(w, '修改主站分组').element as HTMLButtonElement).disabled).toBe((button(w, '取消对接').element as HTMLButtonElement).disabled);
        expect(groupWrites()).toHaveLength(0);
    });
    it('V1 unconfirmed account disables the editor with P3 and cannot send reads', async () => {
        h.check = async () => json(result([])); const w = await open(); const action = button(w, '修改主站分组');
        expect(action.element).toHaveProperty('disabled', true);
        expect(action.attributes('title')).toBe('这个账号正在检查或本次未能确认，暂时不能导入、解绑或修改主站分组；请等待检查结束，或点击刷新完成核对。');
        await action.trigger('click'); await flushPromises(); expect(groupReads()).toHaveLength(0);
        expect(w.find('[role="dialog"]').exists()).toBe(false);
    });
    it('V1 does not expose an operation before connection data has arrived', async () => {
        const pending = deferred<Response>(); let first = true;
        h.local = () => { if (first) { first = false; return pending.promise; } return Promise.resolve(json({ workspaceAdminAccountId: 'ws-scoped', items: h.connections })); };
        const w = await open(); expect(button(w, '修改主站分组')).toBeUndefined();
        expect(button(w, '配置对接')).toBeUndefined(); expect(button(w, '取消对接')).toBeUndefined();
        pending.resolve(json({ workspaceAdminAccountId: 'ws-scoped', items: h.connections })); await flushPromises();
        expect(button(w, '修改主站分组')).toBeDefined(); expect(groupReads()).toHaveLength(0);
    });
    it('V2 defaults to live groups, keeps immutable fields readable and rereads in ready state', async () => {
        const w = await open(); await click(w, '修改主站分组');
        expect(groupCheckbox(w, '原分组').element).toHaveProperty('checked', true);
        expect(groupCheckbox(w, '新分组').element).toHaveProperty('checked', false);
        for (const text of ['主站测试账号（编号 101）', '上游分组（不能在这里修改）：范围甲 · 分组甲', '主站账号平台（不能在这里修改）：openai', '取消对接', '删除账号和 Key', '同时移除旧分组的调价数据源', '新分组会自动加上本上游分组作为调价数据源', '现在所在', '选择的分组与主站当前分组相同']) expect(editor(w).text()).toContain(text);
        expect(editor(w).findAll('select')).toHaveLength(0);
        expect(button(w, '保存主站分组').element).toHaveProperty('disabled', true);
        expect(groupCheckbox(w, '同时移除旧分组').element).toHaveProperty('checked', true);
        await selectTarget(w); await groupCheckbox(w, '同时移除旧分组').setValue(false);
        await click(w, '重新读取'); expect(groupReads()).toHaveLength(2);
        expect(groupCheckbox(w, '原分组').element).toHaveProperty('checked', true);
        expect(groupCheckbox(w, '同时移除旧分组').element).toHaveProperty('checked', true);
        expect(groupWrites()).toHaveLength(0);
    });
    it.each(['server', 'network', 'invalid'])('V2 failed %s reads show their direct cause and recover through reread', async mode => {
        if (mode === 'server') h.groupRead = async () => json({ message: '读取主站资料失败', detail: '主站账号详情读取超时', next: '请检查主站账号详情能否打开' }, 502);
        if (mode === 'network') h.groupRead = async () => { throw new TypeError('offline'); };
        if (mode === 'invalid') h.groupRead = async () => json({ ...readState('10', '10'), workspaceAdminAccountId: 'other' });
        const w = await open(); await click(w, '修改主站分组');
        expect(editor(w).text()).toContain('没能读取主站账号当前的分组'); expect(editor(w).text()).toContain('读取本身不会修改任何内容');
        expect(editor(w).text()).toContain(mode === 'server' ? '主站账号详情读取超时；请检查主站账号详情能否打开' : mode === 'network' ? 'Hub 后端没有回复' : '不属于当前工作区');
        expect(button(w, '保存主站分组')).toBeUndefined(); h.groupRead = async () => json(readState('10', '10'));
        await click(w, '重新读取'); expect(groupCheckbox(w, '原分组').element).toHaveProperty('checked', true);
        expect(editor(w).text()).not.toContain('没能读取'); expect(groupWrites()).toHaveLength(0);
    });
    it('V12 lists inactive marks, no available groups and only pricing-enabled connection offers mapping removal', async () => {
        h.connections[0].pricingMappingEnabled = false;
        h.groupRead = async () => { const data = readState('10', '10'); data.groups[0].status = 'disabled'; return json(data); };
        const w = await open(); await click(w, '修改主站分组');
        expect(groupCheckbox(w, '原分组').element).toHaveProperty('checked', true); expect(editor(w).text()).toContain('已停用');
        expect(editor(w).text()).not.toContain('同时移除旧分组的调价数据源');
        h.groupRead = async () => json({ ...readState('10', '10'), groups: [] }); await click(w, '重新读取');
        expect(editor(w).text()).toContain('主站没有这个平台的分组可选；请先在主站创建分组，再点“重新读取”');
    });
    it('V12 distinguishes single-group and multiple-group cost and purchase impact and refuses empty or unchanged selection', async () => {
        const w = await open(); await click(w, '修改主站分组');
        expect(editor(w).text()).toContain('这把上游 Key 今天的全部成本会算到新分组');
        expect(editor(w).text()).toContain('保存后的分组与关联时的分组相同，扣减照常');
        expect(editor(w).text()).not.toContain('这个对接不能再新关联买号');
        await groupCheckbox(w, '新分组').setValue(true); await flushPromises();
        expect(editor(w).text()).toContain('成本不按分组计入首页的分组利润'); expect(editor(w).text()).toContain('这个对接不能再新关联买号');
        expect(editor(w).text()).not.toContain('今天的全部成本会算到新分组');
        await groupCheckbox(w, '原分组').setValue(false); await groupCheckbox(w, '新分组').setValue(false); await flushPromises();
        expect(editor(w).text()).toContain('至少选择一个主站分组'); expect(button(w, '保存主站分组').element).toHaveProperty('disabled', true);
        expect(groupWrites()).toHaveLength(0);
    });
});

describe('main-site groups result and lifecycle behavior', () => {
    it.each(['unchanged', 'main_changed_hub_not_saved', 'main_unknown', 'hub_unknown'])('V5 %s preserves result and automatically rereads exactly once without premature list refresh', async outcome => {
        const notices: Record<string, string> = { unchanged: '主站分组没有修改', main_changed_hub_not_saved: '主站已改为 新分组，但 Hub 记录没有更新', main_unknown: '已向主站发出修改，但没能确认主站现在的分组', hub_unknown: '主站已改为 新分组；Hub 保存结果未能确认' };
        h.groupWrite = async () => json(mutationResult(outcome, notices[outcome]));
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w); await groupCheckbox(w, '同时移除旧分组').setValue(false); await click(w, '保存主站分组');
        expect(groupReads()).toHaveLength(2); expect(groupWrites()).toHaveLength(1); expect(groupWrites()[0].body.removeOldPricingMapping).toBe(false);
        expect(editor(w).text()).toContain(notices[outcome]); expect(editor(w).text()).toContain('主站返回的具体原因；请核对后重试');
        expect(h.connections[0].ownGroupIds).toEqual(['10']); expect(salesCalls()).toHaveLength(1);
        expect(editor(w).text()).not.toContain('变更已保存');
        await click(w, '取消'); expect(salesCalls()).toHaveLength(2);
        await flushPromises(); expect(salesCalls()).toHaveLength(2);
    });
    it.each(['B1','B2','B3','B4','B5','B6a','B6b','B7','B8a','B8b','B9','B10','B11','B12','B13','B14','B15','B16','B17','B18'])('V3 %s server block retains message, detail and next and rereads without declaring success', async code => {
        h.groupWrite = async () => json({ message: code + ' 不能执行这次修改', detail: '原组没有其他可用账号，本次未修改任何内容', next: '请先补充账号或保留原组' }, 409);
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w); await click(w, '保存主站分组');
        expect(editor(w).text()).toContain(code + ' 不能执行这次修改；原组没有其他可用账号，本次未修改任何内容；请先补充账号或保留原组');
        expect(groupReads()).toHaveLength(2); expect(groupWrites()).toHaveLength(1); expect(h.connections[0].ownGroupIds).toEqual(['10']);
        expect(editor(w).text()).not.toContain('变更已保存');
    });
    it.each(['network', 'invalid', 'workspace'])('V10 %s response stays uncertain and automatically rereads', async mode => {
        h.groupWrite = async () => {
            if (mode === 'network') throw new TypeError('offline');
            return mode === 'invalid' ? json({ workspaceAdminAccountId: 'ws-scoped', outcome: 'success' }) : json({ ...mutationResult('completed'), workspaceAdminAccountId: 'other' });
        };
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w); await click(w, '保存主站分组');
        expect(editor(w).text()).toContain(mode === 'network' ? '请求没有得到 Hub 的回复，主站和 Hub 记录是否已改无法确认' : 'Hub 返回的结果格式不正确，无法确认是否已修改');
        expect(groupReads()).toHaveLength(2); expect(salesCalls()).toHaveLength(1); expect(h.connections[0].ownGroupIds).toEqual(['10']);
        expect(editor(w).text()).not.toContain('主站分组没有修改');
    });
    it('V12 P26 and mixed-channel P27 preserve the main-site reason without claiming a completed mutation', async () => {
        h.groupWrite = async () => json({ ...mutationResult('unchanged'), message: '主站分组没有修改', detail: '主站拒绝了这次修改：目标分组里已有另一种渠道的账号，Anthropic 与 Antigravity 账号不能放在同一分组。请改选其他分组。主站原文：mixed_channel_warning；Hub 记录和调价数据源也没有改动', next: '请改选其他分组' });
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w); await click(w, '保存主站分组');
        expect(editor(w).text()).toContain('主站分组没有修改'); expect(editor(w).text()).toContain('Anthropic 与 Antigravity 账号不能放在同一分组');
        expect(editor(w).text()).toContain('主站原文：mixed_channel_warning'); expect(editor(w).text()).toContain('Hub 记录和调价数据源也没有改动');
        expect(editor(w).text()).not.toContain('主站已改为'); expect(editor(w).text()).not.toContain('变更已保存'); expect(h.connections[0].ownGroupIds).toEqual(['10']);
    });
    it.each(['save', 'sync'])('V10 %s disables every editor control and Escape until settled', async mode => {
        if (mode === 'sync') h.groupRead = async () => json(readState('11', '10'));
        const pending = deferred<Response>(); h.groupWrite = () => pending.promise;
        const w = await open(); await click(w, '修改主站分组'); if (mode === 'save') await selectTarget(w);
        await click(w, mode === 'save' ? '保存主站分组' : '按主站当前分组更新 Hub 记录');
        for (const control of editor(w).findAll('button,input')) expect(control.element).toHaveProperty('disabled', true);
        expect(editor(w).text()).toContain(mode === 'save' ? '正在修改主站分组…' : '正在更新 Hub 记录…');
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await flushPromises();
        expect(w.find('[role="dialog"]').exists()).toBe(true); expect(groupWrites()).toHaveLength(1);
        pending.resolve(json(mutationResult('unchanged'))); await flushPromises(); expect(button(w, '取消').element).toHaveProperty('disabled', false);
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await flushPromises();
        expect(w.find('[role="dialog"]').exists()).toBe(false); expect(salesCalls()).toHaveLength(2);
    });
    it('V10 loading can close without a mutation refresh and late read is ignored', async () => {
        const pending = deferred<Response>(); h.groupRead = () => pending.promise;
        const w = await open(); await click(w, '修改主站分组'); expect(editor(w).text()).toContain('正在读取主站账号当前的分组');
        expect(button(w, '下一页').element).toHaveProperty('disabled', true);
        for (const select of w.findAll('select')) expect(select.element).toHaveProperty('disabled', true);
        await click(w, '取消'); pending.resolve(json(readState('10', '10'))); await flushPromises();
        expect(w.find('[role="dialog"]').exists()).toBe(false); expect(groupWrites()).toHaveLength(0); expect(salesCalls()).toHaveLength(1);
    });
    it.each(['workspace', 'platform'])('V10 changing %s closes editor and ignores a late mutation result', async mode => {
        const pending = deferred<Response>(); h.groupWrite = () => pending.promise;
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w); await click(w, '保存主站分组');
        h.currentAccount.value = mode === 'workspace' ? { id: 'other-workspace', platform: 'sub2api' } : { id: 'ws-scoped', platform: 'newapi' };
        await flushPromises(); const count = salesCalls().length;
        expect(w.find('[role="dialog"]').exists()).toBe(false);
        pending.resolve(json(mutationResult('completed'))); await flushPromises();
        expect(w.find('[role="dialog"]').exists()).toBe(false); expect(salesCalls()).toHaveLength(count); expect(w.text()).not.toContain('主站返回的具体原因');
    });
    it('V10 old workspace read cannot overwrite a newly opened editor', async () => {
        const pending = deferred<Response>(); h.groupRead = () => pending.promise;
        const w = await open(); await click(w, '修改主站分组');
        h.currentAccount.value = { id: 'new-workspace', platform: 'sub2api' }; await flushPromises();
        h.groupRead = async () => json({ ...readState('11', '10'), workspaceAdminAccountId: 'new-workspace' });
        await click(w, '修改主站分组'); expect(groupCheckbox(w, '新分组').element).toHaveProperty('checked', true);
        pending.resolve(json(readState('10', '10'))); await flushPromises();
        expect(groupCheckbox(w, '新分组').element).toHaveProperty('checked', true); expect(groupCheckbox(w, '原分组').element).toHaveProperty('checked', false);
        expect(groupWrites()).toHaveLength(0);
    });
    it('V10 unmount prevents late mutation completion from refreshing any page', async () => {
        const pending = deferred<Response>(); h.groupWrite = () => pending.promise;
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w); await click(w, '保存主站分组');
        w.unmount(); wrappers.splice(wrappers.indexOf(w), 1);
        const count = h.calls.length; pending.resolve(json(mutationResult('completed'))); await flushPromises();
        expect(h.calls).toHaveLength(count); expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
    it('V10 keeps the result visible while automatic reread is loading', async () => {
        const pending = deferred<Response>(); const w = await open(); await click(w, '修改主站分组'); await selectTarget(w);
        h.groupRead = () => pending.promise; h.groupWrite = async () => json(mutationResult('main_unknown', '已向主站发出修改，结果还无法确认'));
        await click(w, '保存主站分组'); expect(editor(w).text()).toContain('已向主站发出修改，结果还无法确认'); expect(editor(w).text()).toContain('正在读取主站账号当前的分组');
        pending.resolve(json(readState('11', '10'))); await flushPromises();
        expect(editor(w).text()).toContain('已向主站发出修改，结果还无法确认'); expect(editor(w).text()).toContain('两边不一致'); expect(editor(w).text()).toContain('只更新 Hub 记录和调价数据源，不修改主站');
    });
    it('V10 completed refresh failure states the saved mutation rather than hiding it', async () => {
        const w = await open(); await click(w, '修改主站分组'); await selectTarget(w);
        h.groupWrite = async () => { h.connections[0] = { ...h.connections[0], ownGroupIds: ['11'], ownGroupNames: ['新分组'] }; return json({ ...mutationResult('completed'), connection: h.connections[0] }); };
        h.local = async () => json({ message: '本地对接资料读取超时', next: '请刷新' }, 502);
        await click(w, '保存主站分组'); expect(w.find('[role="dialog"]').exists()).toBe(false);
        expect(w.text()).toContain('变更已保存，但列表刷新失败'); expect(salesCalls()).toHaveLength(2);
    });
    it('V11 opening and closing the editor leaves cancel modes and group type editing intact', async () => {
        const w = await open(); await click(w, '修改主站分组'); await click(w, '取消');
        await click(w, '取消对接'); expect(editor(w).text()).toContain('仅取消关联'); expect(editor(w).text()).toContain('删除账号和 Key');
        expect(editor(w).text()).not.toContain('保存主站分组'); await click(w, '取消');
        const typeAction = row(w).findAll('button').find(b => b.attributes('aria-label')?.includes('修改'));
        expect(typeAction).toBeDefined(); await typeAction!.trigger('click'); await flushPromises();
        expect(editor(w).find('select').exists()).toBe(true); expect(editor(w).text()).not.toContain('保存主站分组'); expect(groupWrites()).toHaveLength(0);
    });
});
describe('main-site groups critical business expectations', () => {
    it('V7 partial main save rereads and synchronizes using latest expected values in same dialog', async () => {
        let main='10';
        h.groupRead=async()=>json(readState(main,h.connections[0].ownGroupIds[0]));
        h.groupWrite=async(path: string,body: any)=>{
            if(path.endsWith('/hub-record')) {
                expect(body.expectedMainSiteGroupIds).toEqual(['11']);
                expect(body.expectedConnection.ownGroupIds).toEqual(['10']);
                h.connections[0]={...h.connections[0],ownGroupIds:['11'],ownGroupNames:['新分组']};
                return json({workspaceAdminAccountId:'ws-scoped',outcome:'completed',message:'已完成',detail:'',next:'',mainSiteGroups:[{id:'11',name:'新分组'}],connection:h.connections[0]});
            }
            expect(body.targetGroupIds).toEqual(['11']); main='11';
            return json({workspaceAdminAccountId:'ws-scoped',outcome:'main_changed_hub_not_saved',message:'主站已改为 新分组，但 Hub 记录没有更新',detail:'本地保存失败',next:'请按主站当前分组更新 Hub 记录',mainSiteGroups:[{id:'11',name:'新分组'}]});
        };
        const w=await open(); await click(w,'修改主站分组'); await selectTarget(w); await click(w,'保存主站分组');
        expect(editor(w).text()).toContain('Hub 记录没有更新'); expect(editor(w).text()).toContain('两边不一致');
        expect(h.connections[0].ownGroupIds).toEqual(['10']);
        await click(w,'按主站当前分组更新 Hub 记录');
        expect(w.find('[role="dialog"]').exists()).toBe(false);
        expect(h.calls.filter(c=>c.path.includes('/main-site-groups')&&c.body)).toHaveLength(2);
        expect(salesCalls()).toHaveLength(2);
    });
    it.each(['completed','main_changed_hub_not_saved','main_unknown'])('V13 %s followed by sales read failure never denies previous mutation',async(outcome)=>{
        h.groupWrite=async()=>{
            if(outcome==='completed') h.connections[0]={...h.connections[0],ownGroupIds:['11'],ownGroupNames:['新分组']};
            return json({workspaceAdminAccountId:'ws-scoped',outcome,message:outcome==='main_unknown'?'已向主站发出修改，但没能确认主站现在的分组':'主站已改为 新分组',detail:'本次结果说明',next:'请在下面核对',mainSiteGroups:outcome==='main_unknown'?[]:[{id:'11',name:'新分组'}],...(outcome==='completed'?{connection:h.connections[0]}:{})});
        };
        const w=await open(); await click(w,'修改主站分组'); await selectTarget(w); fail(); await click(w,'保存主站分组');
        if(outcome!=='completed') { expect(editor(w).exists()).toBe(true); await click(w,'取消'); }
        expect(salesCalls()).toHaveLength(2);
        expect(w.text()).toContain('这次销售倍率读取本身不会修改主站');
        expect(w.text()).toContain('Hub 暂时无法连接主站'); expect(w.text()).toContain('刷新数据');
        expect(w.text()).not.toContain('本次没有修改主站');
        expect(cells(w)[0]!.text()).toBe(outcome==='completed'?'新分组':'原分组');
    });
    it.each(['main_changed_hub_not_saved','main_unknown','hub_unknown'])('V10 %s result survives automatic reread failure',async(outcome)=>{
        const w=await open(); await click(w,'修改主站分组'); await selectTarget(w);
        h.groupWrite=async()=>json({workspaceAdminAccountId:'ws-scoped',outcome,message:'保存结果必须保留',detail:'实际结果具体原因',next:'核对后再操作',mainSiteGroups:[]});
        h.groupRead=async()=>json({message:'读取主站资料失败',detail:'主站读取超时',next:'请稍后重新读取'},502);
        await click(w,'保存主站分组'); expect(editor(w).text()).toContain('保存结果必须保留');
        expect(editor(w).text()).toContain('读取本身不会修改任何内容'); expect(editor(w).text()).toContain('主站读取超时');
        expect(editor(w).text()).not.toContain('本次没有修改主站');
    });
});
