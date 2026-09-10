// @vitest-environment jsdom

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import PriorityCandidatePreview from '@/modules/admin/components/dashboard/PriorityCandidatePreview.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const wrappers: VueWrapper[] = []

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

const capacity = (region: 'normal' | 'hot_standby', healthBand: 'healthy' | 'recovering' | 'degraded', start: number, end: number, actual: number) => ({
  region,
  healthBand,
  start,
  end,
  capacity: end - start + 1,
  actual,
  remaining: Math.max(0, end - start + 1 - actual),
  overflow: actual > end - start + 1,
})

const capacities = (normalHealthy = 0) => [
  capacity('normal', 'healthy', 10, 99, normalHealthy),
  capacity('normal', 'recovering', 100, 999, 0),
  capacity('normal', 'degraded', 1000, 9999, 0),
  capacity('hot_standby', 'healthy', 10000, 39999, 1),
  capacity('hot_standby', 'recovering', 40000, 69999, 0),
  capacity('hot_standby', 'degraded', 70000, 99999, 0),
]

const account = (overrides: Partial<AdminGroupAccount> & { id: string; targetId: string; name: string }): AdminGroupAccount => ({
  platform: 'openai',
  type: 'subscription',
  status: 'active',
  accountTier: 1,
  schedulable: true,
  priority: 20,
  probeAvailable: true,
  modelHealth: [],
  assignedPolicyIds: [],
  assignedPolicies: [],
  hasAssignedPolicy: true,
  hasEnabledPolicy: true,
  hasEnabledProbePolicy: true,
  priorityManaged: true,
  probeModelsConfigured: true,
  effectiveMultiplier: 1,
  multiplierSource: 'upstream_key',
  ...overrides,
})

const group = (id: string, accounts: AdminGroupAccount[], summary: Record<string, unknown>): AdminGroupHealth => ({
  id,
  name: id,
  platform: 'sub2api',
  status: 'active',
  type: 'subscription',
  isExclusive: false,
  subscriptionType: '',
  multiplier: null,
  multiplierDisplay: '-',
  accountCount: accounts.length,
  monitoredAccountCount: accounts.length,
  healthSummary: {
    totalAccounts: accounts.length,
    probeableAccounts: accounts.length,
    unprobeableAccounts: 0,
    healthyModels: 0,
    degradedModels: 0,
    suspendedModels: 0,
    disabledModels: 0,
    unconfiguredModels: 0,
    lastProbeAt: null,
  },
  accounts,
  priorityCandidateSummary: summary,
} as unknown as AdminGroupHealth)

const mountPreview = (groups: AdminGroupHealth[], loaded = true, platform = 'sub2api') => {
  const wrapper = mount(PriorityCandidatePreview, { props: { groups, loaded, platform } as never })
  wrappers.push(wrapper)
  return wrapper
}

// Regression mutation guarded: rendering group projections directly would show duplicate global ranks and hide the actual compare inputs.
describe('read-only Priority candidate preview', () => {
  it('deduplicates targets, shows strict order inputs and all capacity intervals, then reveals protected blockers by interaction', async () => {
    const primary = account({
      id: 'primary', name: 'Primary healthy', targetId: 'sub2api:ws1:primary', accountTier: 1,
      effectiveMultiplier: 99, priorityCandidate: {
        state: 'candidate', rank: 1, priority: 10, region: 'normal', healthBand: 'healthy',
        successLatencyMs: 12, multiplier: 0.25, priorityEvidence: 'last_applied', blocksTakeover: false,
      },
    } as never)
    const standby = account({
      id: 'standby', name: 'Second standby', targetId: 'sub2api:ws1:standby', accountTier: 2,
      effectiveMultiplier: 0.1, priorityCandidate: {
        state: 'candidate', rank: 2, priority: 10000, region: 'hot_standby', healthBand: 'healthy',
        successLatencyMs: 3, multiplier: 0.1, priorityEvidence: 'confirmed_pending', blocksTakeover: false,
      },
    } as never)
    const manual = account({
      id: 'manual', name: 'Protected manual', targetId: 'sub2api:ws1:manual', accountTier: 1,
      priority: 7, priorityManaged: false, priorityCandidate: {
        state: 'out_of_scope', reason: 'protected_priority_1_9', priorityEvidence: 'unknown', blocksTakeover: true,
      },
    } as never)
    const summary = {
      mode: 'first_active', candidatePriorityReady: true, candidateCount: 2, outOfScopeCount: 1,
      blockerCount: 1, capacities: capacities(1),
    }
    const wrapper = mountPreview([
      group('g1', [primary, standby, manual], summary),
      group('g2', [{ ...primary, effectiveMultiplier: 0.25 }], summary),
    ])

    expect(wrapper.text()).toContain('只读候选排序')
    expect(wrapper.text()).toContain('第一层生效，第二层热备')
    expect(wrapper.text()).toContain('未写入 Sub2API')
    expect(wrapper.findAll('[data-testid="priority-candidate-row"]')).toHaveLength(2)
    expect(wrapper.findAll('[data-testid="priority-candidate-row"]').filter(row => row.text().includes('Primary healthy'))).toHaveLength(1)
    expect(wrapper.findAll('[data-testid="priority-capacity-row"]')).toHaveLength(6)
    const firstRow = wrapper.findAll('[data-testid="priority-candidate-row"]')[0]!
    expect(firstRow.text()).toContain('#1')
    expect(firstRow.text()).toContain('候选 Priority 10')
    expect(firstRow.text()).toContain('健康')
		expect(firstRow.text()).toContain('第一层')
		expect(firstRow.text()).toContain('lastApplied 已确认')
    expect(firstRow.text()).toContain('0.25x')
    expect(firstRow.text()).not.toContain('99x')
    expect(wrapper.text().match(/0\.25x/g)).toHaveLength(1)
    expect(firstRow.text()).toContain('12 ms')
    expect(firstRow.text()).toContain('sub2api:ws1:primary')
		const secondRow = wrapper.findAll('[data-testid="priority-candidate-row"]')[1]!
		expect(secondRow.text()).toContain('热备')
		expect(secondRow.text()).toContain('第二层')
		expect(secondRow.text()).toContain('pending 已确认')
    expect(wrapper.text()).toContain('10-99')
    expect(wrapper.text()).toContain('1 / 90')
    expect(wrapper.text()).toContain('剩余 89')
    expect(wrapper.findAll('[data-testid="priority-excluded-row"]')).toHaveLength(0)

    await wrapper.get('button[aria-label="查看未纳入账号"]').trigger('click')
    expect(wrapper.findAll('[data-testid="priority-excluded-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('Protected manual')
		expect(wrapper.text()).toContain('当前 Priority 7')
		expect(wrapper.text()).toContain('来源未知')
    expect(wrapper.text()).toContain('人工或来源未知的 Priority 1-9')
    expect(wrapper.text()).toContain('阻断切换')
    expect(wrapper.findAll('button').some(button => /写入|应用|批准/.test(button.text()))).toBe(false)
  })

  // Regression mutation guarded: a safety lock and a successful takeover must never be visible at the same time.
  it('shows an evidence lock without a writable priority or contradictory second-layer takeover claim', () => {
    const unknown = account({
      id: 'unknown', name: 'Unknown first', targetId: 'sub2api:ws1:unknown', accountTier: 1,
      priorityCandidate: { state: 'safety_lock', reason: 'health_incomplete', priorityEvidence: 'last_applied', blocksTakeover: false },
    } as never)
    const second = account({
      id: 'second', name: 'Waiting second', targetId: 'sub2api:ws1:second', accountTier: 2,
      priorityCandidate: {
        state: 'candidate', rank: 1, region: 'normal', healthBand: 'healthy', successLatencyMs: 8,
        priorityEvidence: 'last_applied', blocksTakeover: false,
      },
    } as never)
    const wrapper = mountPreview([group('locked', [unknown, second], {
      mode: 'safety_lock', candidatePriorityReady: false, safetyReason: 'health_incomplete',
      candidateCount: 1, outOfScopeCount: 0, blockerCount: 0, capacities: capacities(),
    })])

    expect(wrapper.text()).toContain('安全锁定')
    expect(wrapper.text()).toContain('健康或成功延迟证据不完整')
    expect(wrapper.text()).toContain('无法生成可写候选 Priority')
    expect(wrapper.text()).not.toContain('第二层已接管')
    expect(wrapper.text()).not.toContain('候选 Priority 10')
  })

  // Regression mutation guarded: leaving a recovered second layer in normal range would erase the layer boundary.
  it('distinguishes confirmed second-layer takeover from hot standby', () => {
    const second = account({
      id: 'second', name: 'Promoted second', targetId: 'sub2api:ws1:second', accountTier: 2,
      priorityCandidate: {
        state: 'candidate', rank: 1, priority: 10, region: 'normal', healthBand: 'healthy',
        successLatencyMs: 8, priorityEvidence: 'last_applied', blocksTakeover: false,
      },
    } as never)
    const wrapper = mountPreview([group('takeover', [second], {
      mode: 'second_active', candidatePriorityReady: true, candidateCount: 1,
      outOfScopeCount: 0, blockerCount: 0, capacities: capacities(1),
    })])

    expect(wrapper.text()).toContain('第一层已明确不可用，第二层进入正常候选区')
    expect(wrapper.text()).toContain('候选 Priority 10')
    expect(wrapper.text()).not.toContain('第二层热备')
    expect(wrapper.text()).not.toContain('安全锁定')
  })

  it('shows all fixed capacities only for a successfully loaded empty Sub2API workspace', () => {
    const wrapper = mountPreview([])
    expect(wrapper.text()).toContain('只读候选排序')
    expect(wrapper.text()).toContain('安全锁定')
    expect(wrapper.text()).toContain('第一层没有可安全判定的纳管目标')
    const rows = wrapper.findAll('[data-testid="priority-capacity-row"]')
    expect(rows).toHaveLength(6)
    const expected = [
      ['正常区', '健康', '10-99', '0 / 90', '剩余 90'],
      ['正常区', '恢复中', '100-999', '0 / 900', '剩余 900'],
      ['正常区', '降级/观察', '1000-9999', '0 / 9000', '剩余 9000'],
      ['热备', '健康', '10000-39999', '0 / 30000', '剩余 30000'],
      ['热备', '恢复中', '40000-69999', '0 / 30000', '剩余 30000'],
      ['热备', '降级/观察', '70000-99999', '0 / 30000', '剩余 30000'],
    ]
    expected.forEach((values, index) => {
      for (const value of values) expect(rows[index]!.text()).toContain(value)
    })
    expect(wrapper.findAll('[data-testid="priority-candidate-row"]')).toHaveLength(0)
    expect(wrapper.findAll('button').some(button => /写入|应用|批准/.test(button.text()))).toBe(false)
  })

  it('does not fabricate an empty-workspace summary before success or for another platform', () => {
    expect(mountPreview([], false, 'sub2api').find('section').exists()).toBe(false)
    expect(mountPreview([], true, 'newapi').find('section').exists()).toBe(false)
  })
})
