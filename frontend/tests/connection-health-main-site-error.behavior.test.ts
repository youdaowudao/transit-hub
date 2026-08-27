// @vitest-environment jsdom

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
} from '@/modules/admin/types/connectionHealth'

const mountedWrappers: VueWrapper[] = []

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

const makeAccount = (overrides: Partial<AdminGroupAccount>): AdminGroupAccount => ({
  id: '100',
  name: 'Account 100',
  platform: 'openai',
  type: 'subscription',
  status: 'active',
  schedulable: true,
  priority: 18,
  targetId: 'sub2api:ws1:100',
  probeAvailable: true,
  modelHealth: [],
  assignedPolicyIds: [],
  assignedPolicies: [],
  hasAssignedPolicy: false,
  hasEnabledPolicy: false,
  hasEnabledProbePolicy: false,
  priorityManaged: false,
  probeModelsConfigured: true,
  ...overrides,
})

const mountDetail = (accounts: AdminGroupAccount[]) => {
  const group: AdminGroupHealth = {
    id: 'group-1',
    name: 'Stable Group',
    platform: 'sub2api',
    status: 'enabled',
    type: 'subscription',
    isExclusive: false,
    subscriptionType: '',
    multiplier: null,
    multiplierDisplay: '-',
    accountCount: accounts.length,
    monitoredAccountCount: 0,
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
  }
  const wrapper = mount(AdminGroupHealthDetail, {
    props: {
      group,
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: [],
      actionLoading: false,
    },
  })
  mountedWrappers.push(wrapper)
  return wrapper
}

const accountRow = (wrapper: VueWrapper, accountName: string): VueWrapper => {
  const row = wrapper.findAll('tbody > tr').find(candidate => candidate.text().includes(accountName))
  if (!row) throw new Error(`missing account row: ${accountName}`)
  return row
}

describe('AdminGroupHealthDetail current main-site errors', () => {
  it.each([
    {
      label: 'active',
      account: makeAccount({
        id: '100',
        name: 'Active stale account',
        status: 'active',
        mainSiteError: 'old 403',
        schedulable: true,
        priority: 18,
        targetId: 'sub2api:ws1:100',
      }),
      statusLabel: '主站账号启用',
      schedulableLabel: '主站调度开启',
      priorityLabel: '18',
      staleReason: 'old 403',
    },
    {
      label: 'inactive',
      account: makeAccount({
        id: '101',
        name: 'Inactive stale account',
        status: 'inactive',
        mainSiteError: 'old 402',
        schedulable: false,
        priority: 19,
        targetId: 'sub2api:ws1:101',
      }),
      statusLabel: '主站账号停用',
      schedulableLabel: '主站调度关闭',
      priorityLabel: '19',
      staleReason: 'old 402',
    },
  ])('hides the stale reason for an $label account while preserving independent fields and expansion', async ({ account, statusLabel, schedulableLabel, priorityLabel, staleReason }) => {
    const wrapper = mountDetail([account])
    const row = accountRow(wrapper, account.name)

    expect(row.text()).toContain(statusLabel)
    expect(row.text()).toContain(schedulableLabel)
    expect(row.text()).toContain(priorityLabel)
    expect(row.text()).not.toContain('主站运行错误')
    expect(row.text()).not.toContain(staleReason)

    const expandButton = row.find('button[aria-label="展开模型结果"]')
    expect(expandButton.exists()).toBe(true)
    await expandButton.trigger('click')
    expect(wrapper.findAll('tbody > tr')).toHaveLength(2)
    expect(wrapper.findAll('tbody > tr')[1].text()).toContain('该目标还没有模型探活结果。')
  })

  it('shows the current error reason and normalizes a blank current-error status reason', () => {
    const wrapper = mountDetail([
      makeAccount({
        id: '200',
        name: 'Current error account',
        status: 'error',
        mainSiteError: 'upstream returned 503',
        targetId: 'sub2api:ws1:200',
      }),
      makeAccount({
        id: '201',
        name: 'Normalized error account',
        status: ' ERROR ',
        mainSiteError: '',
        targetId: 'sub2api:ws1:201',
      }),
    ])

    const currentError = accountRow(wrapper, 'Current error account').find('p.text-destructive')
    expect(currentError.text()).toContain('主站运行错误：upstream returned 503')
    expect(currentError.classes()).toContain('text-destructive')

    const normalizedError = accountRow(wrapper, 'Normalized error account').find('p.text-destructive')
    expect(normalizedError.text()).toContain('主站运行错误：原因未提供')
    expect(normalizedError.classes()).toContain('text-destructive')
  })

  it('does not treat NewAPI account errors as Sub2API main-site errors', () => {
    const wrapper = mountDetail([
      makeAccount({
        id: '300',
        name: 'NewAPI account',
        status: 'error',
        mainSiteError: 'not a main-site error',
        targetId: 'newapi:ws1:300',
      }),
    ])

    const row = accountRow(wrapper, 'NewAPI account')
    expect(row.text()).not.toContain('主站运行错误')
    expect(row.text()).not.toContain('not a main-site error')
  })
})
