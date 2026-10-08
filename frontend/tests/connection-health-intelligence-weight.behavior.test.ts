// @vitest-environment jsdom

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  getConnectionHealthAdminGroups,
  refreshConnectionHealthAdminGroups,
} from '@/modules/admin/api/connectionHealth'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const wrappers: VueWrapper[] = []

const account = (id: string): AdminGroupAccount => ({
  id,
  name: `Account ${id}`,
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  schedulable: true,
  targetId: `sub2api:ws1:${id}`,
  probeAvailable: true,
  modelHealth: [],
  assignedPolicyIds: ['policy-1'],
  assignedPolicies: [{ policyId: 'policy-1', policyName: '正式策略', enabled: true, strategyMode: 'health_probe' }],
  hasAssignedPolicy: true,
  hasEnabledPolicy: true,
  hasEnabledProbePolicy: true,
  priorityManaged: true,
  probeModelsConfigured: true,
  productionSortOrder: 0,
  todayQuestionAnswerSubmitted: 0,
  todayQuestionAnswerCorrect: 0,
})

const group = (accounts: AdminGroupAccount[]): AdminGroupHealth => ({
  id: 'group-1',
  name: '测试分组',
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
})

const mountDetail = (accounts: AdminGroupAccount[]) => {
  const wrapper = mount(AdminGroupHealthDetail, {
    props: {
      group: group(accounts),
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: [],
      actionLoading: false,
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

const refreshPayload = () => ({
  status: 'success',
  runId: 'retired-weight-run',
  revision: 1,
  groups: [{ id: 'group-1', accounts: [{ targetId: 'sub2api:ws1:account-1' }] }],
  refresh: { state: 'success', sites: [] },
})

const refreshSSE = () => new Response(
  `event: terminal\ndata: ${JSON.stringify(refreshPayload())}\n\n`,
  { status: 200, headers: { 'Content-Type': 'text/event-stream; charset=utf-8' } },
)

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('retired intelligence weight compatibility', () => {
  it('accepts admin group and refresh responses without the retired field', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([
      { id: 'group-1', accounts: [{ targetId: 'sub2api:ws1:account-1' }] },
    ]), { status: 200 })))
    const groups = await getConnectionHealthAdminGroups()
    expect(groups[0]?.accounts[0]).not.toHaveProperty('intelligenceWeight')

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(refreshPayload()), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })))
    const refreshed = await refreshConnectionHealthAdminGroups()
    expect(refreshed.groups?.[0]?.accounts?.[0]).not.toHaveProperty('intelligenceWeight')

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(refreshSSE()))
    const refreshedFromSSE = await refreshConnectionHealthAdminGroups()
    expect(refreshedFromSSE.groups?.[0]?.accounts?.[0]).not.toHaveProperty('intelligenceWeight')
  })

  it('does not render a retired editor in account rows', () => {
    const wrapper = mountDetail([account('retired')])
    expect(wrapper.find('[data-testid="account-intelligence-weight-editor"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('最近正确率')
  })
})
