// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupHealth, ModelHealth } from '@/modules/admin/types/connectionHealth'

const T7 = '系统已改为每 24 小时 自动试一次，成功后自动恢复正常探活。请检查上游 Key 或账号，修好后可在分组健康点“一键正式探活”立即验证。'
const T8 = '因为分组测试配置冲突或无法确认，系统目前无法探活，也不会自动恢复。请到分组设置把这个账号所在各分组的测试协议和超时统一，未被禁止探活的模型才会继续自动探活。若模型显示“健康已禁用”，请联系管理员核查该模型的禁用记录；正式探活不能解除禁用。'
const T9 = '因为主站调度已关闭、且策略设为调度关闭时不继续探活，系统目前不会自动探活。请在分组健康检查模型状态：若有模型显示“健康已禁用”，请先联系管理员核查禁用记录，正式探活不能解除禁用；否则可点“一键正式探活”验证。仍处于关停状态的模型要按各自预设要求连续成功，全部受控模型恢复健康后，账号才会改回原状态；调度开关仍关闭，要接收请求请在主站手动打开。'
const model = (extra: Record<string, unknown> = {}) => ({
 modelName: 'm1', configured: true, state: 'suspended', currentWeight: 0, consecutiveFailures: 9, consecutiveSuccesses: 0,
 lastProbeAt: '2031-02-03T00:00:00Z', lastSuccessAt: null, lastFailureAt: '2031-02-03T00:00:00Z', failingSince: '2031-02-01T00:00:00Z',
 lastLatencyMs: null, lastSuccessLatencyMs: null, lastErrorKey: 'auth', lastErrorDetail: '', lastRemoteAction: '', elapsedSeconds: 90000,
 nextProbeAt: '2031-02-04T00:00:00Z', blockedReason: 'long_failure', longFailure: true, longFailureRetry: 'continuing',
 longFailureAfterSeconds: 86400, longFailureIntervalSeconds: 86400, ...extra,
}) as unknown as ModelHealth
const group = (models: ModelHealth[]): AdminGroupHealth => ({
 id: 'g1', name: '隔离长期失败组', platform: 'openai', status: 'active', type: 'standard', isExclusive: false, subscriptionType: '', multiplier: 1, multiplierDisplay: '1x', accountCount: 1, monitoredAccountCount: 1,
 healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0, suspendedModels: 1, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
 accounts: [{ id: 'a', name: '隔离账号', targetId: 'sub2api:ws1:a', platform: 'openai', type: 'subscription', status: 'active', schedulable: true, priority: 100, concurrency: 1, loadFactor: 1, accountTier: 1, probeAvailable: true, modelHealth: models, hasAssignedPolicy: true, hasEnabledPolicy: true, hasEnabledProbePolicy: true }],
})
const wrappers: VueWrapper[] = []
const open = async (models: ModelHealth[] = [model()]) => {
 const wrapper = mount(AdminGroupHealthDetail, { props: { group: group(models), workspacePlatform: 'sub2api', actionLoading: false, questionAnswerUnreadTargetIds: [], hideUnmonitoredAccounts: false } })
 wrappers.push(wrapper); await flushPromises(); await wrapper.get('tbody tr button').trigger('click'); await flushPromises(); return wrapper
}
const notice = (wrapper: VueWrapper) => wrapper.findAll('p').filter(p => p.text().startsWith('从 ') && p.text().includes('起连续失败超过'))
beforeEach(() => vi.stubGlobal('fetch', vi.fn(async () => new Response('[]'))))
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.unstubAllGlobals() })
describe('long failure model response drives the existing detail', () => {
 it('shows the long reason, origin, daily retry and emits the existing formal probe action', async () => {
  const wrapper = await open()
  expect(wrapper.text()).toContain('长期失败，按长期间隔自动重试')
  expect(notice(wrapper)).toHaveLength(1)
  expect(notice(wrapper)[0]!.text()).toContain('起连续失败超过 24 小时。')
  expect(notice(wrapper)[0]!.text()).toContain(T7)
  const quick = wrapper.findAll('button').find(b => b.attributes('aria-label') === '一键正式探活：m1')!
  expect(quick).toBeDefined(); expect(quick.attributes('disabled')).toBeUndefined(); await quick.trigger('click')
  expect(wrapper.emitted('quick-probe')).toHaveLength(1)
  await wrapper.setProps({ group: group([model({ longFailure: false, state: 'healthy', blockedReason: '', failingSince: null })]) })
  expect(notice(wrapper)).toHaveLength(0)
 })
 it.each([['config_blocked', T8], ['scheduling_blocked', T9]])('shows %s without inventing one account recovery count', async (kind, message) => {
  const wrapper = await open([model({ longFailureRetry: kind })])
  expect(notice(wrapper)).toHaveLength(1); const text = notice(wrapper)[0]!.text(); expect(text).toContain(message)
  expect(text).not.toMatch(/连续成功\s*[23]\s*次|\{.*\}/)
  if (kind === 'config_blocked') expect(text).not.toContain('一键正式探活')
  expect(text).toContain('正式探活不能解除禁用')
 })
 it('formats non-hour durations as minutes', async () => {
  const wrapper = await open([model({ longFailureAfterSeconds: 5400, longFailureIntervalSeconds: 9000 })])
  expect(notice(wrapper)[0]!.text()).toContain('连续失败超过 90 分钟。')
  expect(notice(wrapper)[0]!.text()).toContain('每 150 分钟 自动试一次')
 })
 it.each(['not-long', 'missing', 'disabled'])('keeps %s without a long-failure instruction', async kind => {
  const item = model({ longFailure: false, state: kind === 'disabled' ? 'disabled' : 'healthy', blockedReason: kind === 'disabled' ? 'health_disabled' : '' }) as any
  if (kind === 'missing') for (const key of ['longFailure', 'longFailureRetry', 'longFailureAfterSeconds', 'longFailureIntervalSeconds']) delete item[key]
  const wrapper = await open([item]); expect(notice(wrapper)).toHaveLength(0); expect(wrapper.text()).not.toContain('系统已改为每')
  if (kind === 'disabled') expect(wrapper.text()).toContain('健康已禁用')
 })
 it('uses model-specific timing with no shared recovery count for multiple models', async () => {
  const wrapper = await open([model({ modelName: 'm1', longFailureRetry: 'scheduling_blocked' }), model({ modelName: 'm2', longFailureRetry: 'scheduling_blocked', longFailureAfterSeconds: 7200 })])
  expect(notice(wrapper)).toHaveLength(2)
  expect(notice(wrapper)[1]!.text()).toContain('连续失败超过 2 小时。')
  for (const paragraph of notice(wrapper)) { expect(paragraph.text()).toContain('按各自预设要求连续成功'); expect(paragraph.text()).not.toMatch(/连续成功\s*[23]\s*次|\{.*\}/) }
 })
})
