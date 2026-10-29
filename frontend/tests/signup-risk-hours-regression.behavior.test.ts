// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import SignupRiskNotificationSettings from '@/modules/admin/components/settings/SignupRiskNotificationSettings.vue';
import NotificationSummary from '@/modules/admin/components/notifications/NotificationSummary.vue';
import NotificationBell from '@/modules/admin/components/notifications/NotificationBell.vue';
import { useNotifications, type NotificationController } from '@/modules/admin/composables/useNotifications';
import { NotificationRequestError } from '@/modules/admin/api/notifications';
import type { NotificationSummaryResponse, SiteNotification } from '@/modules/admin/types/notifications';
const api = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), summary: vi.fn(), progress: vi.fn(), history: vi.fn() }));
vi.mock('@/modules/admin/api/notifications', async (original) => ({ ...await original<typeof import('@/modules/admin/api/notifications')>(), getSignupRiskPreferences: api.get, saveSignupRiskPreferences: api.save, getNotificationSummary: api.summary, saveNotificationProgress: api.progress, listNotifications: api.history }));
const when = '2031-01-01T01:00:00Z';
const tomorrow = '2031-01-02T01:00:00Z';
const pref = (enabled = false) => ({ workspaceAdminAccountId: 'a', enabled, initialized: false, updatedAt: when, workStart: '09:00', workEnd: '18:00', timeZone: 'Asia/Singapore', serverTime: when, activeUntil: enabled ? '2031-01-01T10:00:00Z' : null, nextStartAt: enabled ? tomorrow : null });
const fact = (id: string, risk = false): SiteNotification => ({ id, adminAccountId: 'a', kind: risk ? 'sub2api.signup_risk' : 'fixture.fact', subjectType: 'admin_account', subjectId: 'a', subjectActive: true, subjectInactiveMessage: '', level: 3, discoveredAt: when, createdAt: when, readAt: null, reminderCompletedAt: null, content: { title: risk ? '测试风险用户' : '其他重要提醒', summary: '隔离通知', details: [] } });
function snapshot(risk: SiteNotification[] = [], until: string | null = '2031-01-01T10:00:00Z'): NotificationSummaryResponse {
    return { workspaceAdminAccountId: 'a', serverTime: new Date().toISOString(), intervalSeconds: 300, unreadCount: risk.length, pendingCriticalCount: 0, displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] }, active: { attention: { items: [], totalCount: 0, nextCursor: null }, critical: { items: [], totalCount: 0, nextCursor: null }, important: { items: [...risk, fact('ordinary')], totalCount: risk.length + 1, nextCursor: null } }, signupRisk: { enabled: true, items: risk, ...{ activeUntil: until, nextStartAt: tomorrow } } };
}
const wrappers: VueWrapper[] = [];
function controller() { let value!: NotificationController; const w = mount(defineComponent({ setup() { value = useNotifications({ workspaceId: ref('a'), isSwitching: ref(false) }); return () => h(NotificationSummary, { controller: value }); } })); wrappers.push(w); return { value, w }; }
function settings() { const w = mount(SignupRiskNotificationSettings, { props: { workspaceId: 'a', platform: 'sub2api' } }); wrappers.push(w); return w; }
const button = (w: VueWrapper, text: string) => w.findAll('button').find(b => b.text() === text)!;
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date(when)); vi.clearAllMocks(); vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible'); api.get.mockResolvedValue(pref()); api.save.mockImplementation(async (body) => ({ ...pref(body.enabled), ...body })); api.summary.mockImplementation(async () => snapshot()); });
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks(); });
describe('registration hours and persisted settings regression', () => {
    it('waits a full minute between automatic registration reads', async () => { controller(); await flushPromises(); expect(api.summary).toHaveBeenCalledTimes(1); await vi.advanceTimersByTimeAsync(59999); await flushPromises(); expect(api.summary).toHaveBeenCalledTimes(1); await vi.advanceTimersByTimeAsync(1); await flushPromises(); expect(api.summary).toHaveBeenCalledTimes(2); });
    it('hides both risk surfaces when the permitted period ends even if the network fails', async () => { api.summary.mockResolvedValue(snapshot([fact('risk', true)], '2031-01-01T01:00:30Z')); const { w } = controller(); await flushPromises(); expect(w.text()).toContain('测试风险用户'); api.summary.mockRejectedValue(new Error('隔离网络中断')); await vi.advanceTimersByTimeAsync(30001); await flushPromises(); expect(w.text()).not.toContain('测试风险用户'); expect(w.text()).toContain('其他重要提醒'); expect(api.progress).not.toHaveBeenCalled(); });
    it('does not restore risk through ordinary important when outside hours', async () => { api.summary.mockResolvedValue(snapshot([fact('risk', true)], null)); const { w } = controller(); await flushPromises(); expect(w.text()).not.toContain('测试风险用户'); expect(w.text()).toContain('其他重要提醒'); });
    it('keeps the saved disabled state while editing and saves the complete configuration', async () => { const w = settings(); await flushPromises(); expect(w.text()).toContain('已关闭'); await w.get('input[type="checkbox"]').setValue(true); expect(w.text()).toContain('有修改尚未保存'); expect(w.text()).toContain('已关闭'); expect(api.save).not.toHaveBeenCalled(); const times = w.findAll('input[type="time"]'); expect(times).toHaveLength(2); await times[0]!.setValue('10:00'); await button(w, '保存并开启提醒').trigger('click'); await flushPromises(); expect(api.save).toHaveBeenCalledWith({ workspaceAdminAccountId: 'a', enabled: true, workStart: '10:00', workEnd: '18:00', timeZone: 'Asia/Singapore' }, expect.anything()); expect(w.text()).not.toContain('有修改尚未保存'); expect(w.emitted('settings-changing')?.map(args => args[0])).toEqual([true, false]); });
    it('does not claim old disabled is current after an uncertain save and failed reread', async () => { const w = settings(); await flushPromises(); await w.get('input[type="checkbox"]').setValue(true); api.save.mockRejectedValue(new Error('提交响应中断')); api.get.mockRejectedValue(new Error('核对连接中断')); await (button(w, '保存并开启提醒') ?? button(w, '保存高风险注册提醒')).trigger('click'); await flushPromises(); expect(w.emitted('settings-changing')?.map(args => args[0])).toEqual([true, false]); expect(w.text()).toContain('保存结果尚无法确认'); expect(w.text()).toContain('核对连接中断'); expect(w.text()).not.toContain('当前在工作时间内'); expect(w.emitted('settings-changing')?.map(args => args[0])).toEqual([true, false]); expect(w.findAll('button').filter(b => b.text().startsWith('保存')).every(b => b.attributes('disabled') !== undefined)).toBe(true); });
});
describe('settings save lifecycle and registration eligibility', () => {
    it('pauses risk before PUT, keeps ordinary reminders running, and stays paused when the outcome cannot be confirmed', async () => {
        api.get.mockResolvedValue(pref(true));
        let rejectSave!: (reason: unknown) => void;
        api.save.mockReturnValue(new Promise((_, reject) => { rejectSave = reject; }));
        api.summary.mockImplementation(async () => snapshot([fact('risk', true)]));
        let value!: NotificationController;
        const w = mount(defineComponent({ setup() {
                value = useNotifications({ workspaceId: ref('a'), isSwitching: ref(false) });
                return () => h('div', [
                    h(SignupRiskNotificationSettings, { workspaceId: 'a', platform: 'sub2api',
                        onSettingsChanging: (saving: boolean) => (value as NotificationController & {
                            setSignupRiskSettingsSaving(saving: boolean): void;
                        }).setSignupRiskSettingsSaving(saving) }),
                    h(NotificationSummary, { controller: value }),
                ]);
            } }));
        wrappers.push(w);
        await flushPromises();
        expect(w.text()).toContain('测试风险用户');
        await w.get('input[type="checkbox"]').setValue(false);
        await (button(w, '保存并关闭提醒') ?? button(w, '保存高风险注册提醒')).trigger('click');
        await flushPromises();
        expect(w.text()).not.toContain('测试风险用户');
        expect(w.text()).toContain('其他重要提醒');
        const withCritical = snapshot([fact('risk', true)]);
        withCritical.active.critical = { items: [{ ...fact('urgent'), level: 4 }], totalCount: 1, nextCursor: null };
        withCritical.pendingCriticalCount = 1;
        api.summary.mockResolvedValue(withCritical);
        await vi.advanceTimersByTimeAsync(299999);
        expect(value.criticalOpen.value).toBe(false);
        await vi.advanceTimersByTimeAsync(1);
        await flushPromises();
        expect(value.criticalOpen.value).toBe(true);
        expect(w.text()).not.toContain('测试风险用户');
        api.get.mockRejectedValue(new Error('核对连接中断'));
        api.summary.mockRejectedValue(new Error('摘要连接中断'));
        rejectSave(new Error('提交响应中断'));
        await flushPromises();
        expect(w.text()).toContain('保存结果尚无法确认');
        expect(w.text()).not.toContain('测试风险用户');
    });
    it('does not let an older summary restore a risk after a close attempt has started', async () => {
        api.get.mockResolvedValue(pref(true));
        api.summary.mockResolvedValue(snapshot([fact('risk', true)]));
        let value!: NotificationController;
        const w = mount(defineComponent({ setup() {
                value = useNotifications({ workspaceId: ref('a'), isSwitching: ref(false) });
                return () => h('div', [h(SignupRiskNotificationSettings, { workspaceId: 'a', platform: 'sub2api',
                        onSettingsChanging: (saving: boolean) => (value as NotificationController & {
                            setSignupRiskSettingsSaving(saving: boolean): void;
                        }).setSignupRiskSettingsSaving(saving) }), h(NotificationSummary, { controller: value })]);
            } }));
        wrappers.push(w);
        await flushPromises();
        let resolveOld!: (result: NotificationSummaryResponse) => void;
        api.summary.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve; }));
        const old = value.refreshSignupRisk();
        await flushPromises();
        api.summary.mockRejectedValue(new Error('新摘要读取失败'));
        await w.get('input[type="checkbox"]').setValue(false);
        await (button(w, '保存并关闭提醒') ?? button(w, '保存高风险注册提醒')).trigger('click');
        await flushPromises();
        resolveOld(snapshot([fact('risk', true)]));
        await old;
        await flushPromises();
        expect(w.text()).not.toContain('测试风险用户');
        expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ enabled: false }), expect.anything());
    });
});
describe('final review regression paths', () => {
    it('keeps a second important page registration outside the independent first twenty after the minute read', async () => {
        const firstTwenty = Array.from({ length: 20 }, (_, i) => fact(`risk-${i}`, true));
        const secondRisk = { ...fact('second-risk', true), content: { title: '第二页独有注册消息', summary: '隔离消息', details: [] } };
        const first = snapshot(firstTwenty);
        first.active.important = { items: firstTwenty, totalCount: 22, nextCursor: 'page-2' };
        const second = snapshot(firstTwenty);
        second.active.important = { items: [secondRisk], totalCount: 22, nextCursor: 'page-3' };
        api.summary.mockResolvedValueOnce(first).mockResolvedValue(second);
        api.progress.mockImplementation(async (request) => ({ workspaceAdminAccountId: 'a', items: request.ids.map((id: string) => ({ id, readAt: null, reminderCompletedAt: when })) }));
        const { value, w } = controller();
        await flushPromises();
        await value.nextSummaryPage(3);
        await flushPromises();
        expect(w.text()).toContain('第二页独有注册消息');
        await vi.advanceTimersByTimeAsync(60000);
        await flushPromises();
        expect(value.importantItems.value.map(item => item.id)).toEqual(['second-risk']);
        expect(w.text()).toContain('第二页独有注册消息');
        expect(value.summary.value?.active.important.nextCursor).toBe('page-3');
        await value.dismissImportant();
        await flushPromises();
        expect(api.progress).toHaveBeenCalledWith(expect.objectContaining({ action: 'dismiss', ids: ['second-risk'] }), expect.anything());
        expect(value.signupRiskItems.value.map(item => item.id)).toEqual(firstTwenty.map(item => item.id));
    });
    it('does not resurrect a stale total after the last risk history item is marked read and refresh fails', async () => {
        const risk = fact('risk', true);
        api.summary.mockResolvedValue(snapshot([risk], '2031-01-01T01:00:30Z'));
        api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [risk], nextCursor: null });
        api.progress.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [{ id: 'risk', readAt: when, reminderCompletedAt: when }] });
        const { value, w } = controller();
        await flushPromises();
        expect(w.text()).toContain('共有2条待提醒');
        api.summary.mockRejectedValue(new Error('摘要持续不可读'));
        await vi.advanceTimersByTimeAsync(30001);
        await flushPromises();
        expect(w.text()).not.toContain('共有2条待提醒');
        await value.openDrawer();
        await flushPromises();
        await value.markPageRead();
        await flushPromises();
        expect(w.text()).not.toContain('共有2条待提醒');
        expect(w.text()).toContain('其他重要提醒');
        expect(w.text()).toContain('本摘要1条');
    });
    it.each(['GET', 'PUT'])('does not claim working time after a slow %s response crossed the end', async (method) => {
        vi.setSystemTime(new Date('2031-01-01T01:00:50Z'));
        const result = { ...pref(true), workEnd: '09:01', serverTime: '2031-01-01T01:00:50Z', activeUntil: '2031-01-01T01:01:00Z' };
        let resolve!: (value: typeof result) => void;
        const held = new Promise<typeof result>(yes => { resolve = yes; });
        if (method === 'GET')
            api.get.mockReturnValue(held);
        else
            api.get.mockResolvedValue({ ...pref(true), serverTime: result.serverTime });
        const w = settings();
        await flushPromises();
        if (method === 'PUT') {
            await w.findAll('input[type="time"]')[1]!.setValue('09:01');
            api.save.mockReturnValue(held);
            await button(w, '保存工作时间').trigger('click');
            await flushPromises();
        }
        await vi.advanceTimersByTimeAsync(20000);
        resolve(result);
        await flushPromises();
        expect(w.text()).not.toContain('已开启，当前在工作时间内');
        expect(w.text()).toContain('已开启，当前不在工作时间内');
    });
    it.each([false, true])('confirms an uncertain 4xx result by reading, including saved=%s', async (savedSuccessfully) => {
        const w = settings();
        await flushPromises();
        await w.get('input[type="checkbox"]').setValue(true);
        api.save.mockRejectedValue(new NotificationRequestError('响应内容无法读取，是否已经保存尚无法确认', 'HTTP响应不是完整JSON', '请重新读取核对', true, 'a', 403));
        if (savedSuccessfully)
            api.get.mockResolvedValue(pref(true));
        else
            api.get.mockRejectedValue(new Error('重读连接中断'));
        await button(w, '保存并开启提醒').trigger('click');
        await flushPromises();
        expect(api.get).toHaveBeenCalledTimes(2);
        expect(w.text()).not.toContain('已保存设置未改变');
        if (savedSuccessfully)
            expect(w.text()).toContain('注册提醒设置已保存');
        else
            expect(w.text()).toContain('保存结果尚无法确认');
    });
});

describe('working hours bell count regression', () => {
    it.each([true, false])('keeps only reliable important counts after the work end, risk contributed=%s', async (withRisk) => {
        const result = snapshot(withRisk ? [fact('risk', true)] : [], '2031-01-01T01:00:30Z');
        result.unreadCount = withRisk ? 2 : 1;
        api.summary.mockResolvedValue(result);
        const w = mount(defineComponent({setup() {
            const value = useNotifications({workspaceId: ref('a'), isSwitching: ref(false)});
            return () => h(NotificationBell, {controller: value});
        }}));
        wrappers.push(w);
        await flushPromises();
        expect(w.text()).toContain(`${withRisk ? 2 : 1}条重要提醒待处理`);
        api.summary.mockRejectedValue(new Error('下班后摘要读取失败'));
        await vi.advanceTimersByTimeAsync(30001);
        await flushPromises();
        if (withRisk) {
            expect(w.text()).not.toContain('2条重要提醒待处理');
            expect(w.text()).toContain('有未读消息');
        } else expect(w.text()).toContain('1条重要提醒待处理');
    });
});
