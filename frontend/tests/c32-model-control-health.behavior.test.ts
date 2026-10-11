// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Detail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import Accounts from '@/modules/admin/components/dashboard/AdminGroupAccountsDialog.vue'
import LinkCard from '@/modules/admin/components/dashboard/ConnectionHealthLinkDetailCard.vue'
import Associations from '@/modules/admin/components/dashboard/GroupAssociationsPanel.vue'
import { useConnectionHealth, judgedModelHealth, judgedUnprobedModels } from '@/modules/admin/composables/useConnectionHealth'
import type { ConnectionHealthEvent } from '@/modules/admin/types/connectionHealth'
import { c32Groups, c32Model } from './fixtures/c32ModelControl'
import { now, targetId } from './fixtures/v2911ModelControl'
const api = vi.hoisted(() => ({ getConnectionHealthAdminGroups: vi.fn(), setAdminGroupTestConfiguration: vi.fn(), getMySiteMappingOptions: vi.fn(), listRealConnections: vi.fn(), listAllGroupRates: vi.fn(), listUpstreamSites: vi.fn(), getNotificationChannelSettings: vi.fn() }))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
vi.mock('@/modules/admin/api/mySites', async original => ({ ...await original<typeof import('@/modules/admin/api/mySites')>(), getMySiteMappingOptions: api.getMySiteMappingOptions, listRealConnections: api.listRealConnections }))
vi.mock('@/modules/admin/api/groupRates', async original => ({ ...await original<typeof import('@/modules/admin/api/groupRates')>(), listAllGroupRates: api.listAllGroupRates }))
vi.mock('@/modules/admin/api/upstream', async original => ({ ...await original<typeof import('@/modules/admin/api/upstream')>(), listUpstreamSites: api.listUpstreamSites }))
vi.mock('@/modules/admin/api/settings', async original => ({ ...await original<typeof import('@/modules/admin/api/settings')>(), getNotificationChannelSettings: api.getNotificationChannelSettings }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
 const { ref } = await import('vue')
 return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws1' }) }) }
})
const service = useConnectionHealth(), wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(w: T): T => { wrappers.push(w); return w }
const detail = (group = c32Groups()[0]!) => track(mount(Detail, { props: { group, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false } }))
const accounts = (group = c32Groups()[0]!) => track(mount(Accounts, { props: { open: true, group }, global: { stubs: { Teleport: true } } }))
beforeEach(() => { vi.resetAllMocks(); service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('ws1'); api.getConnectionHealthAdminGroups.mockResolvedValue(c32Groups()) })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); service.setAdminGroupsWorkspace(''); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.innerHTML = '' })
describe('C3.2 closed probes remain visible but stop deciding whole-account health', () => {
 it('returns new filtered arrays without deleting full display arrays', () => {
  const a = c32Groups()[0]!.accounts[0]!; expect(judgedModelHealth(a).map(m => m.modelName)).toEqual(['open']); expect(judgedUnprobedModels(a)).toEqual([]); expect(a.modelHealth).toHaveLength(2); expect(a.unprobedModels).toHaveLength(1)
  delete a.modelHealth[0]!.modelControlClosed; expect(judgedModelHealth(a)).toHaveLength(2)
 })
 it('uses open health for badge, latency and stability; preserves expanded closed and unprobed models and probe choices', async () => {
  const w = detail(), row = w.get('tbody tr'); expect(row.text()).toContain('健康'); expect(row.text()).not.toContain('暂停'); expect(row.text()).toContain('10 ms'); expect(row.text()).not.toContain('9000 ms'); expect(row.text()).not.toContain('×2')
  await row.findAll('button')[0]!.trigger('click'); expect(w.findAll('[data-testid="model-control-closed-probe"]')).toHaveLength(2); expect(w.text()).toContain('closed-unprobed'); expect(w.text()).toContain('closed'); expect(w.text()).toContain('已关闭，不计入整号')
  const quick = w.findAll('button').find(x => (x.attributes('aria-label') ?? '').includes('closed')); if (!quick) throw new Error('full closed probe model unavailable'); await quick.trigger('click'); expect(w.emitted('quick-probe')![0]![0]).toMatchObject({ targetId, modelHealth: expect.arrayContaining([expect.objectContaining({ modelName: 'closed', modelControlClosed: true })]) })
  const a = c32Groups()[0]!.accounts[0]!, groups = c32Groups(); groups[0]!.accounts[0] = a; const dialog = accounts(groups[0]!); expect(dialog.get('tbody tr').text()).toContain('健康'); expect(dialog.get('tbody tr').text()).not.toContain('暂停'); expect(dialog.text()).toContain('closed · 已关闭，不计入整号')
 })
 it('does not count or filter a closed unverified model as pending, unconfigured or suspended', async () => {
  const g = c32Groups()[0]!; g.accounts[0]!.modelHealth[0]!.currentHealthResult = { status: 'unverified' }
  const w = detail(g); expect(w.get('tbody tr').text()).not.toContain('协议未验证'); expect(w.get('tbody tr').text()).not.toContain('未探活')
  const buttons = w.findAll('button[aria-pressed]'), suspended = buttons.find(x => x.text().includes('暂停'))
  if (!suspended) throw new Error(`missing suspended filter: ${w.text()}`)
  await suspended.trigger('click'); expect(w.get('tbody tr').text()).not.toContain('账号A'); expect(w.get('tbody tr').text()).toContain('当前筛选')
  const clear = w.findAll('button').find(x => x.text().includes('清除')); if (clear) await clear.trigger('click')
  const healthy = w.findAll('button[aria-pressed]').find(x => x.text().includes('健康')); if (!healthy) throw new Error('missing healthy filter'); await healthy.trigger('click'); expect(w.findAll('tbody tr')).toHaveLength(1)
 })
 it.each([['延迟', true, ['账号A', '账号B']], ['延迟', false, ['账号B', '账号A']], ['最近中断', true, ['账号B', '账号A']], ['最近中断', false, ['账号A', '账号B']]] as const)('sorts %s with closed=%s using only judged models', async (label, closed, names) => {
  const g = c32Groups()[0]!, first = g.accounts[0]!
  if (!closed) delete first.modelHealth[0]!.modelControlClosed
  g.accounts.push({ ...first, id: 'b', name: '账号B', targetId: 'sub2api:ws1:b', modelHealth: [c32Model('other', { currentWeight: 50, lastSuccessLatencyMs: 20, lastFailureAt: now, elapsedSeconds: 1 })], unprobedModels: [] })
  const w = detail(g), sort = w.findAll('thead button').find(button => button.text() === label); if (!sort) throw new Error(`missing sort ${label}`)
  await sort.trigger('click'); expect(w.findAll('tbody tr').map(row => names.find(name => row.text().includes(name)))).toEqual(names)
 })
 it.each([true, false])('all excluded probeAvailable=%s shows no-active badge in both lists, hints and zero unconfigured overview', async probeAvailable => {
  const groups = c32Groups(), g = groups[0]!, a = g.accounts[0]!
  a.modelHealth = a.modelHealth.filter(x => x.modelControlClosed); a.modelControlNoActiveProbe = true; a.probeAvailable = probeAvailable; g.healthSummary.healthyModels = 0
  const w = detail(g), d = accounts(g)
  for (const view of [w, d]) { expect(view.get('[data-testid="model-control-no-active-probe"]').text()).toBe('在用模型没有探活'); expect(view.get('[data-testid="model-control-no-active-probe-hint"]').text()).toContain('请到健康策略里改测其他模型。Priority 保持当前值。'); expect(view.get('tbody tr').text()).not.toContain('未探活') }
  api.getConnectionHealthAdminGroups.mockResolvedValue(groups); await service.loadAdminGroups(); await service.loadOverview(); expect(service.overview.value).toMatchObject({ totalConnections: 1, healthy: 0, suspended: 0, unconfigured: 0 })
  a.priorityUsesMultiplierOnly = true; await w.setProps({ group: { ...g, accounts: [{ ...a }] } }); expect(w.get('[data-testid="model-control-no-active-probe-hint"]').text()).not.toContain('Priority 保持当前值。')
 })
 it.each([true, false])('group associations excludes closed failures only when marker exists: closed=%s', async closed => {
  const groups = c32Groups(), group = groups[0]!
  if (!closed) delete group.accounts[0]!.modelHealth[0]!.modelControlClosed
  api.getConnectionHealthAdminGroups.mockResolvedValue(groups)
  api.getMySiteMappingOptions.mockResolvedValue({ ownGroups: [{ id: group.id, groupName: group.name, siteName: '主站', multiplier: 1, platform: 'sub2api', status: 'active', isExclusive: false, subscriptionType: '' }], mappings: [{ ownGroup: group.name, upstreamTargets: [{ siteId: 'upstream', groupName: '上游组' }] }] })
  api.listRealConnections.mockResolvedValue([{ id: 'connection', upstreamSiteId: 'upstream', upstreamGroupId: 'ug1', upstreamGroupName: '上游组', upstreamKeyId: '', adminAccountId: group.accounts[0]!.id, adminAccountName: '账号A', ownGroupIds: [group.id], ownGroupNames: [group.name], groupType: 'subscription', createdAt: now }])
  api.listAllGroupRates.mockResolvedValue([]); api.listUpstreamSites.mockResolvedValue([]); api.getNotificationChannelSettings.mockResolvedValue({ telegram: [] })
  const w = track(mount(Associations)); await flushPromises()
  const row = w.get('tbody tr'), health = row.findAll('td')[3]!
  expect(health.text()).toBe(closed ? '健康可用' : '部分停用'); expect(health.text()).not.toContain(closed ? '部分停用' : '健康可用')
 })
 it('counts another group with an open probe for the same account and returns to old judgments when marker is absent', async () => {
  const groups = c32Groups(), first = groups[0]!, a = first.accounts[0]!
  a.modelHealth = a.modelHealth.filter(x => x.modelControlClosed); a.modelControlNoActiveProbe = true
  groups.push({ ...first, id: 'g2', accounts: [{ ...a, modelControlNoActiveProbe: undefined, modelHealth: [c32Model('open')] }] })
  api.getConnectionHealthAdminGroups.mockResolvedValue(groups); await service.loadAdminGroups(); await service.loadOverview(); expect(service.overview.value).toMatchObject({ totalConnections: 1, healthy: 1, unconfigured: 0 })
  const old = c32Groups()[0]!; delete old.accounts[0]!.modelHealth[0]!.modelControlClosed; delete old.accounts[0]!.unprobedModels![0]!.modelControlClosed; const w = detail(old); expect(w.get('tbody tr').text()).toContain('暂停'); expect(w.get('tbody tr').text()).toContain('9000 ms'); expect(w.get('tbody tr').text()).toContain('×2'); expect(w.find('[data-testid="model-control-closed-probe"]').exists()).toBe(false)
 })
 it('retains markers and never decrements closed-model counts when test configuration changes', async () => {
  const groups = c32Groups(); api.getConnectionHealthAdminGroups.mockResolvedValue(groups); await service.loadAdminGroups()
  const next = { status: 'configured' as const, protocol: 'responses' as const, probeTimeoutSeconds: 30, sourceGroups: [] }
  api.setAdminGroupTestConfiguration.mockResolvedValue({ accounts: [{ targetId, testConfiguration: next }] })
  let resolveReload!: (value: typeof groups) => void; const waiting = new Promise<typeof groups>(done => { resolveReload = done }); api.getConnectionHealthAdminGroups.mockReturnValue(waiting)
  const saving = service.saveAdminGroupTestConfiguration('g1', { protocol: 'responses', probeTimeoutSeconds: 30 }); await flushPromises()
  const g = service.adminGroups.value[0]!, a = g.accounts[0]!
  expect(a.modelHealth).toHaveLength(2); expect(a.modelHealth[0]!.modelControlClosed).toBe(true); expect(a.unprobedModels![0]!.modelControlClosed).toBe(true); expect(g.healthSummary.healthyModels).toBe(0); expect(g.healthSummary.suspendedModels).toBe(0); expect(g.healthSummary.unconfiguredModels).toBe(1)
  resolveReload([g]); await saving
 })
 it('renders a rejected scheduling action after reloading without a group placeholder or guessed blocking group', async () => {
  await service.loadAdminGroups(); const w = detail(service.adminGroups.value[0]!), groups = c32Groups()
  Object.assign(groups[0]!.accounts[0]!, { lastSchedulableAction: 'skipped_sub2api_group_last_model_source', lastSchedulableActionAt: now, lastSchedulableActionResult: 'schedulable_user_action_failed', lastSchedulableActionErrorKey: 'admin.connectionHealth.errors.sub2apiGroupLastModelSource' })
  api.getConnectionHealthAdminGroups.mockResolvedValueOnce(groups); await service.loadAdminGroups(); await w.setProps({ group: service.adminGroups.value[0]! })
  const row = w.get('tbody tr'); expect(row.text()).toContain('所属分组里有已关闭的模型只剩这个账号还开着，不能关闭或删除。是哪个模型见账号行的“保留账号”提示。'); expect(row.text()).not.toContain('{group}'); expect(row.text()).not.toContain('分组 核心组 里有已关闭的模型只剩它还开着。')
 })
 it('shows reservation only while supplied and resolves the new action with its blocking group', async () => {
  const g = c32Groups()[0]!, a = g.accounts[0]!; a.modelFloorLastSources = [{ groupId: 'g1', groupName: '保留组', modelKey: 'long-model-key' }]; const w = detail(g)
  expect(w.get('[data-testid="model-control-last-source"]').text()).toBe('保留账号：分组 保留组 里，模型 long-model-key 只剩这个账号还开着。停用、关调度或删除它会被拦下。'); expect(w.get('[data-testid="model-control-last-source"] span').attributes('title')).toContain('long-model-key')
  await w.setProps({ group: { ...g, accounts: [{ ...a, modelFloorLastSources: undefined }] } }); expect(w.find('[data-testid="model-control-last-source"]').exists()).toBe(false)
  const event = { id: 'guard-event', connectionId: targetId, modelName: 'closed', ownGroupName: '真正拦下组', upstreamSiteId: '', upstreamGroupName: '真正拦下组', result: 'auth', fromState: 'degraded', toState: 'suspended', latencyMs: 1, errorKey: '', remoteAction: 'skipped_sub2api_group_last_model_source', createdAt: now } as ConnectionHealthEvent
  const c = track(mount(LinkCard, { props: { siteLabel: '', upstreamGroupName: '其他展示组', accountName: '账号A', modelName: 'closed', provider: 'openai', isActionCard: false, state: 'suspended', latestLatencyMs: 1, lastProbeAt: now, lastSuccessAt: now, lastFailureAt: now, lastErrorKey: '', lastErrorDetail: '', failureFromLoadedRecords: false, elapsedSeconds: 0, effectiveIntervalSeconds: 60, effectivePolicySources: [], budgetPolicyId: '', availabilityPct: 0, records: [event], nextProbeText: '', actionSource: 'health_probe', actionAt: now, remoteAction: event.remoteAction } }))
  expect(c.text()).toContain('没有停用这个账号：分组 真正拦下组 里有已关闭的模型只剩它还开着。')
  await c.setProps({ records: [] }); expect(c.text()).toContain('所属分组里有已关闭的模型只剩这个账号还开着，不能关闭或删除。'); expect(c.text()).not.toContain('分组 其他展示组 里有已关闭的模型'); expect(c.text()).not.toContain('{group}')
 })
})
