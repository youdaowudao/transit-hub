// @vitest-environment jsdom

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const wrappers: VueWrapper[] = []
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

const account = (overrides: Partial<AdminGroupAccount> = {}): AdminGroupAccount => ({
  id: '101', name: 'Restriction account', platform: 'openai', type: 'subscription', status: 'active', schedulable: false,
  targetId: 'sub2api:ws:101', probeAvailable: true, modelHealth: [], assignedPolicyIds: [], assignedPolicies: [],
  hasAssignedPolicy: false, hasEnabledPolicy: false, hasEnabledProbePolicy: false, priorityManaged: false,
  tempUnschedulableKnown: true, tempUnschedulableUntil: null, tempUnschedulableActive: false,
  rateLimitKnown: true, rateLimitResetAt: null, rateLimitActive: false,
  overloadKnown: true, overloadUntil: null, overloadActive: false, ...overrides,
})

const group = (entry: AdminGroupAccount): AdminGroupHealth => ({
  id: '1', name: 'Restriction group', platform: 'openai', status: 'active', type: 'standard', isExclusive: false,
  subscriptionType: 'standard', multiplier: null, multiplierDisplay: '-', accountCount: 1, monitoredAccountCount: 0,
  healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0,
    suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null }, accounts: [entry],
})

const detail = (entry: AdminGroupAccount) => {
  const wrapper = mount(AdminGroupHealthDetail, { props: { group: group(entry), hideUnmonitoredAccounts: false,
    questionAnswerUnreadTargetIds: [], actionLoading: false } })
  wrappers.push(wrapper)
  return wrapper
}

describe('existing refresh main-site restrictions', () => {
  it('keeps all main-site restrictions independent of the scheduling switch and browser clock', async () => {
    const entry = account({ tempUnschedulableUntil: '2020-01-01T00:00:00Z', tempUnschedulableActive: true,
      tempUnschedulableReason: 'upstream retry backoff', rateLimitResetAt: '2020-01-01T00:01:00Z', rateLimitActive: true,
      overloadUntil: '2020-01-01T00:02:00Z', overloadActive: true })
    const wrapper = detail(entry)
    expect(wrapper.text()).toContain('主站临停至')
    expect(wrapper.text()).toContain('upstream retry backoff')
    expect(wrapper.text()).toContain('主站限流至')
    expect(wrapper.text()).toContain('主站过载至')
    expect(wrapper.text()).toContain('主站调度关闭')
    expect(wrapper.text()).not.toContain('慢速暂停')
    const button = wrapper.get('button[aria-label="恢复主站调度"]')
    await button.trigger('click')
    expect(wrapper.emitted('set-schedulable')?.[0]).toEqual([entry])
    await wrapper.setProps({ group: group({ ...entry, schedulable: true }) })
    expect(wrapper.text()).toContain('主站调度开启')
    expect(wrapper.text()).toContain('主站临停至')
    expect(wrapper.text()).toContain('主站限流至')
    expect(wrapper.text()).toContain('主站过载至')
  })

  it('shows unknown independently while hiding null and confirmed expired fields on refresh', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    const wrapper = detail(account({ tempUnschedulableKnown: false, tempUnschedulableUntil: '2099-01-01T00:00:00Z',
      rateLimitKnown: false, overloadKnown: false }))
    expect(wrapper.text()).toContain('主站临停状态未知')
    expect(wrapper.text()).toContain('主站限流状态未知')
    expect(wrapper.text()).toContain('主站过载状态未知')
    expect(wrapper.text()).not.toContain('主站临停至')
    await wrapper.setProps({ group: group(account({ rateLimitResetAt: '2020-01-01T00:00:00Z', rateLimitActive: false })) })
    expect(wrapper.findAll('[data-main-site-restriction]')).toHaveLength(0)
    expect(fetch).not.toHaveBeenCalled()
  })

  it('does not show Sub2API restrictions on an external NewAPI channel', () => {
    const wrapper = detail(account({ targetId: 'newapi:ws:101', tempUnschedulableKnown: false, rateLimitKnown: false, overloadKnown: false }))
    expect(wrapper.findAll('[data-main-site-restriction]')).toHaveLength(0)
  })

  it('shows the pending action, dispatch, phase, source and reason without changing the existing button', () => {
    const wrapper = detail(account({ remoteActionPending: { action: 'schedulable', dispatchId: 'dispatch-fixture-101', phase: 'uncertain', reason: 'legacy', source: 'manual' } }))
    expect(wrapper.text()).toContain('调度开关写入')
    expect(wrapper.text()).toContain('dispatch-fixture-101')
    expect(wrapper.text()).toContain('结果未知')
    expect(wrapper.text()).toContain('人工操作')
    expect(wrapper.text()).toContain('旧操作缺少完整发送证据')
    expect(wrapper.find('button[aria-label="恢复主站调度"]').exists()).toBe(true)
  })
})
