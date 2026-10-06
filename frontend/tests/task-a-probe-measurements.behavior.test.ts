// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import AdminGroupAccountsDialog from '@/modules/admin/components/dashboard/AdminGroupAccountsDialog.vue'
import type { AdminGroupHealth, HealthRuleVersion, ModelHealth } from '@/modules/admin/types/connectionHealth'

const harness = vi.hoisted(() => ({ discoverModels: vi.fn(), manualProbeTarget: vi.fn(), runManualProbeOnce: vi.fn() }))
vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  return { ...actual, useConnectionHealth: () => ({ ...harness, errorKey: { value: '' } }) }
})

const wrappers: VueWrapper[] = []
const model = (ruleVersion: HealthRuleVersion): ModelHealth => ({
  modelName: 'gpt-measurement-fixture', providerFamily: 'openai', configured: true, state: 'healthy',
  currentWeight: 100, consecutiveFailures: 0, consecutiveSuccesses: 0, lastProbeAt: '2026-10-07T00:00:00Z',
  lastSuccessAt: '2026-10-07T00:00:00Z', lastFailureAt: null, lastLatencyMs: 7000, lastSuccessLatencyMs: 7000,
  lastErrorKey: '', lastErrorDetail: '', lastRemoteAction: '', updatedAt: '2026-10-07T00:00:00Z',
  ruleVersion, firstTokenMs: 6500, firstEventMs: 6000, probeResult: 'slow_response', probeDisposition: 'applied',
  requestProtocol: 'chat_completions', requestTimeoutSeconds: 30, requestLatencyMs: 7000,
  requestAt: '2026-10-07T00:00:00Z', requestErrorKey: '', requestErrorDetail: '健康结果不得标为失败',
})
beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({ models: [{ id: 'gpt-measurement-fixture', name: 'gpt-measurement-fixture' }] })
  harness.manualProbeTarget.mockReset(); harness.runManualProbeOnce.mockReset()
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); document.body.innerHTML = '' })
const openManual = async () => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: { open: false, target: { targetId: 'sub2api:ws1:measurement-fixture', accountName: '测量夹具', platform: 'sub2api', type: 'subscription', status: 'active', groupName: '测量分组', formalModels: [{ id: 'gpt-measurement-fixture', name: 'gpt-measurement-fixture', providerFamily: 'openai' }] } },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises(); return wrapper
}
const clickButton = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().trim() === label)
  if (!button) throw new Error(`Missing fixture button: ${label}`)
  await button.trigger('click'); await flushPromises()
}
const checkHealthyMeasurement = (wrapper: VueWrapper, ruleVersion: HealthRuleVersion) => {
  const row = wrapper.findAll('li').find(candidate => candidate.text().includes('首字：6500ms'))
  expect(row).toBeDefined()
  expect(row!.text()).toContain('首个事件：6000ms')
  expect(row!.text()).toContain('7000ms')
  expect(row!.text()).toContain(ruleVersion === 'v2' ? '延迟' : '慢响应')
  expect(row!.text()).not.toContain(ruleVersion === 'v2' ? '慢响应' : '延迟')
  expect(row!.text()).not.toContain('健康结果不得标为失败')
  expect(row!.find('.lucide-circle-x').exists()).toBe(false)
}

describe('task A manual measurement display', () => {
  it.each(['v2', 'legacy'] as const)('formal %s slow success maps both measurements and preserves the healthy result', async (ruleVersion) => {
    harness.manualProbeTarget.mockResolvedValue([{ ...model(ruleVersion), firstTokenMs: 1000, firstEventMs: 900, requestFirstTokenMs: 6500, requestFirstEventMs: 6000 }])
    const wrapper = await openManual()
    await clickButton(wrapper, '正式手动探活'); await clickButton(wrapper, '开始正式探活')
    checkHealthyMeasurement(wrapper, ruleVersion)
    expect(harness.manualProbeTarget).toHaveBeenCalledExactlyOnceWith('sub2api:ws1:measurement-fixture', ['gpt-measurement-fixture'], expect.any(Object), expect.any(Function))
    expect(harness.runManualProbeOnce).not.toHaveBeenCalled()
    expect(wrapper.emitted('completed')).toHaveLength(1)
  })

  it('formal failure displays this request measurement rather than the retained successful-state measurement', async () => {
    harness.manualProbeTarget.mockResolvedValue([{
      ...model('v2'), state: 'suspect', firstTokenMs: 1000, firstEventMs: 900,
      requestFirstTokenMs: 8000, requestFirstEventMs: 7000, requestLatencyMs: 10000,
      probeResult: 'network_fluctuation', requestErrorKey: 'network_fluctuation', requestErrorDetail: '本次回答中途断开',
    }])
    const wrapper = await openManual()
    await clickButton(wrapper, '正式手动探活'); await clickButton(wrapper, '开始正式探活')
    const row = wrapper.findAll('li').find(candidate => candidate.text().includes('本次回答中途断开'))!
    expect(row).toBeDefined()
    expect(row.text()).toContain('首字：8000ms')
    expect(row.text()).toContain('首个事件：7000ms')
    expect(row.text()).toContain('10000ms')
    expect(row.text()).not.toContain('首字：1000ms')
    expect(row.text()).not.toContain('首个事件：900ms')
    expect(row.find('.lucide-circle-x').exists()).toBe(true)
    expect(harness.runManualProbeOnce).not.toHaveBeenCalled()
  })

  it('formal failure with no request first timings does not fall back to retained success timings', async () => {
    harness.manualProbeTarget.mockResolvedValue([{
      ...model('v2'), state: 'suspect', firstTokenMs: 1000, firstEventMs: 900,
      requestFirstTokenMs: null, requestFirstEventMs: null, requestLatencyMs: 30000,
      probeResult: 'network_fluctuation', requestErrorKey: 'network_fluctuation', requestErrorDetail: '本次连接未取得响应',
    }])
    const wrapper = await openManual()
    await clickButton(wrapper, '正式手动探活'); await clickButton(wrapper, '开始正式探活')
    const row = wrapper.findAll('li').find(candidate => candidate.text().includes('本次连接未取得响应'))!
    expect(row).toBeDefined()
    expect(row.text()).toContain('30000ms')
    expect(row.text()).not.toContain('首字')
    expect(row.text()).not.toContain('首个事件')
    expect(row.find('.lucide-circle-x').exists()).toBe(true)
  })

  it.each(['v2', 'legacy'] as const)('once %s slow success displays both measurements without entering the formal path', async (ruleVersion) => {
    harness.runManualProbeOnce.mockResolvedValue({ results: [{ modelName: 'gpt-measurement-fixture', result: 'slow_response', healthy: true, latencyMs: 7000, firstTokenMs: 6500, firstEventMs: 6000, ruleVersion, protocol: 'chat_completions', probeTimeoutSeconds: 30, errorKey: '', errorDetail: '健康结果不得标为失败', probedAt: '2026-10-07T00:00:00Z' }] })
    const wrapper = await openManual()
    await clickButton(wrapper, '一次性测试')
    await clickButton(wrapper, '开始测试')
    checkHealthyMeasurement(wrapper, ruleVersion)
    expect(harness.runManualProbeOnce).toHaveBeenCalledExactlyOnceWith('sub2api:ws1:measurement-fixture', ['gpt-measurement-fixture'], expect.any(Object))
    expect(harness.manualProbeTarget).not.toHaveBeenCalled()
    expect(wrapper.emitted('completed')).toBeUndefined()
  })
})

describe('task A retained account dialog measurement display', () => {
  it('renders suspect and both measurements, then removes those readings when the authoritative model changes', async () => {
    const group: AdminGroupHealth = {
      id: 'measurement-group', name: '测量分组', platform: 'openai', status: 'active', type: 'public', isExclusive: false,
      subscriptionType: '', multiplier: 1, multiplierDisplay: '1', accountCount: 1,
      healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 1, suspectModels: 1, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: '2026-10-07T00:00:00Z' },
      accounts: [{ id: 'measurement-fixture', name: '测量账号', platform: 'openai', type: 'subscription', status: 'active', schedulable: true, priority: 10, targetId: 'sub2api:ws1:measurement-fixture', probeAvailable: true, modelHealth: [{ ...model('v2'), state: 'suspect' }] }],
    }
    const wrapper = mount(AdminGroupAccountsDialog, { props: { open: true, group }, global: { stubs: { Teleport: true, Transition: false } } })
    wrappers.push(wrapper)
    expect(wrapper.get('tbody').text()).toContain('疑似')
    expect(wrapper.get('tbody').text()).toContain('首字：6500ms')
    expect(wrapper.get('tbody').text()).toContain('首个事件：6000ms')
    expect(wrapper.get('tbody').text()).not.toContain('尚未探活')
    const controls = wrapper.get('tbody').findAll('button').length
    expect(controls).toBeGreaterThan(0)
    await wrapper.setProps({ group: { ...group, accounts: [{ ...group.accounts[0]!, modelHealth: [{ ...model('v2'), state: 'healthy', firstTokenMs: null, firstEventMs: null }] }] } })
    expect(wrapper.get('tbody').text()).toContain('健康')
    expect(wrapper.get('tbody').text()).not.toContain('疑似')
    expect(wrapper.get('tbody').text()).not.toContain('首字')
    expect(wrapper.get('tbody').text()).not.toContain('首个事件')
    expect(wrapper.get('tbody').findAll('button')).toHaveLength(controls)
  })
})
