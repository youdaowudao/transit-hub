// @vitest-environment jsdom

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const wrappers: VueWrapper[] = []

const makeAccount = (
  id: string,
  name: string,
  productionSortOrder: number,
  submitted: number,
  correct: number,
): AdminGroupAccount => ({
  id,
  name,
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  schedulable: true,
  targetId: `sub2api:ws1:${id}`,
  probeAvailable: true,
  modelHealth: [{
    modelName: 'gpt-5.6-sol',
    providerFamily: 'openai',
    configured: true,
    state: 'healthy',
    currentWeight: 100,
    consecutiveFailures: 0,
    consecutiveSuccesses: 1,
    lastProbeAt: null,
    lastSuccessAt: null,
    lastFailureAt: null,
    lastLatencyMs: 100,
    lastSuccessLatencyMs: 100,
    lastErrorKey: '',
    lastErrorDetail: '',
    lastRemoteAction: '',
    probeResult: 'ok',
    updatedAt: '2026-08-31T08:00:00Z',
  }],
  assignedPolicyIds: ['policy-1'],
  assignedPolicies: [{ policyId: 'policy-1', policyName: '正式策略', enabled: true, strategyMode: 'health_probe' }],
  hasAssignedPolicy: true,
  hasEnabledPolicy: true,
  hasEnabledProbePolicy: true,
  priorityManaged: true,
  probeModelsConfigured: true,
  productionSortOrder,
  todayQuestionAnswerSubmitted: submitted,
  todayQuestionAnswerCorrect: correct,
  intelligenceWeight: null,
} as AdminGroupAccount)

const makeGroup = (accounts: AdminGroupAccount[]): AdminGroupHealth => ({
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
    healthyModels: accounts.length,
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
      group: makeGroup(accounts),
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: [],
      actionLoading: false,
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

const accountOrder = (wrapper: VueWrapper, accounts: AdminGroupAccount[]): string[] =>
  wrapper.findAll('tbody > tr')
    .map(row => accounts.find(account => row.text().includes(account.name))?.name)
    .filter((name): name is string => Boolean(name))

const rowFor = (wrapper: VueWrapper, accountName: string): VueWrapper => {
  const row = wrapper.findAll('tbody > tr').find(candidate => candidate.text().includes(accountName))
  if (!row) throw new Error(`missing account row: ${accountName}`)
  return row
}

const buttonByAria = (wrapper: VueWrapper, label: string): VueWrapper => {
  const button = wrapper.findAll('button').find(candidate => candidate.attributes('aria-label') === label)
  if (!button) throw new Error(`missing button: ${label}`)
  return button
}

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

describe('AdminGroupHealthDetail today question-answer accuracy', () => {
  it('keeps production order until clicked, formats values, sorts by exact ratio, and always leaves no-data last', async () => {
    const accounts = [
      makeAccount('prod-first', 'Prod First', 0, 3, 2),
      makeAccount('prod-second', 'Prod Second', 1, 4, 3),
      makeAccount('beta-tie', 'Beta Tie', 2, 4, 2),
      makeAccount('alpha-tie', 'Alpha Tie', 3, 2, 1),
      makeAccount('zero', 'Zero', 4, 5, 0),
      makeAccount('none', 'None', 5, 0, 0),
    ]
    const wrapper = mountDetail(accounts)

    expect(accountOrder(wrapper, accounts)).toEqual([
      'Prod First', 'Prod Second', 'Beta Tie', 'Alpha Tie', 'Zero', 'None',
    ])

    expect(rowFor(wrapper, 'Prod Second').findAll('td')[8].text()).toContain('75%')
    expect(rowFor(wrapper, 'Prod Second').findAll('td')[8].text()).toContain('3/4')
    expect(rowFor(wrapper, 'Prod First').findAll('td')[8].text()).toContain('66.7%')
    expect(rowFor(wrapper, 'Zero').findAll('td')[8].text()).toContain('0%')
    expect(rowFor(wrapper, 'Zero').findAll('td')[8].text()).toContain('0/5')
    expect(rowFor(wrapper, 'None').findAll('td')[8].text()).toBe('-')

    const intelligenceHeader = wrapper.findAll('thead th').find(header => header.text().includes('智商权重'))
    if (!intelligenceHeader) throw new Error('missing intelligence weight header')
    expect(intelligenceHeader.find('button').exists()).toBe(false)
    expect(rowFor(wrapper, 'Prod Second').findAll('td')[9].text()).toContain('未评分')

    const accuracyHeader = wrapper.findAll('thead th').find(header => header.text().includes('今日正确率'))
    if (!accuracyHeader) throw new Error('missing today accuracy header')
    await accuracyHeader.get('button').trigger('click')
    expect(accuracyHeader.attributes('aria-sort')).toBe('descending')
    expect(accountOrder(wrapper, accounts)).toEqual([
      'Prod Second', 'Prod First', 'Alpha Tie', 'Beta Tie', 'Zero', 'None',
    ])

    await accuracyHeader.get('button').trigger('click')
    expect(accuracyHeader.attributes('aria-sort')).toBe('ascending')
    expect(accountOrder(wrapper, accounts)).toEqual([
      'Zero', 'Alpha Tie', 'Beta Tie', 'Prod First', 'Prod Second', 'None',
    ])
  })

  it('places the existing actions in two rows without changing any emitted action', async () => {
    const account = makeAccount('actions', 'Action Account', 0, 1, 1)
    const wrapper = mountDetail([account])
    const row = rowFor(wrapper, account.name)
    expect(row.findAll('td')).toHaveLength(11)
    const primary = row.get('.account-actions-primary')
    const secondary = row.get('.account-actions-secondary')

    expect(primary.findAll('button')).toHaveLength(3)
    expect(secondary.findAll('button')).toHaveLength(2)

    await buttonByAria(primary, '设置账号策略').trigger('click')
    await buttonByAria(primary, '手动探活').trigger('click')
    await buttonByAria(primary, '一键正式探活：gpt-5.6-sol').trigger('click')
    await buttonByAria(secondary, '关闭主站调度').trigger('click')
    await buttonByAria(secondary, '查看事件').trigger('click')

    expect(wrapper.emitted('assign-policy')).toEqual([[account]])
    expect(wrapper.emitted('probe')).toEqual([[account]])
    expect(wrapper.emitted('quick-probe')).toEqual([[account]])
    expect(wrapper.emitted('set-schedulable')).toEqual([[account]])
    expect(wrapper.emitted('view-events')).toEqual([[account]])
  })
})
