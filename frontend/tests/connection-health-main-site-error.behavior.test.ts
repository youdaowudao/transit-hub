// @vitest-environment jsdom

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import ConnectionHealthEventsDialog from '@/modules/admin/components/dashboard/ConnectionHealthEventsDialog.vue'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
  ConnectionHealthEvent,
  ModelHealth,
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
    platform: 'openai',
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
  it('shows every member of one group together without paging controls', () => {
    const accounts = Array.from({ length: 140 }, (_, index) => makeAccount({
      id: String(index + 1),
      name: `Member-${String(index + 1).padStart(4, '0')}`,
      targetId: `sub2api:ws1:${index + 1}`,
    }))
    const wrapper = mountDetail(accounts)
    const rows = wrapper.findAll('tbody > tr')
    expect(rows).toHaveLength(140)
    for (const account of accounts) expect(accountRow(wrapper, account.name).exists()).toBe(true)
    expect(wrapper.findAll('button').some(button => /上一页|下一页/.test(button.text()))).toBe(false)
    expect(wrapper.text()).toContain('Stable Group')
  })

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


describe('current protocol result and invalid attempt', () => {
  it.each([
    ['daily_probe_budget_exhausted', '当日探活预算已耗尽'],
    ['cooldown', '健康冷却中'],
  ])('keeps the independent %s explanation visible for an unverified model', async (blockedReason, label) => {
    const model: ModelHealth = {
      modelName: 'pending-model', providerFamily: 'openai', configured: true, state: 'healthy',
      currentWeight: 100, consecutiveFailures: 0, consecutiveSuccesses: 1,
      lastProbeAt: null, lastSuccessAt: null, lastFailureAt: null, lastLatencyMs: null,
      lastErrorKey: '', lastErrorDetail: '', lastRemoteAction: '', updatedAt: '2026-10-03T00:00:00Z',
      currentHealthResult: { status: 'unverified', protocol: 'responses' }, blockedReason,
    }
    const wrapper = mountDetail([makeAccount({ hasEnabledProbePolicy: true, modelHealth: [model] })])
    await accountRow(wrapper, 'Account 100').find('button[aria-label="展开模型结果"]').trigger('click')
    expect(wrapper.text()).toContain('当前协议健康依据待验证')
    expect(wrapper.text()).toContain('超过 5 秒仍属于慢响应')
    expect(wrapper.text()).toContain('暂停或观察中的账号需要正常成功并满足原恢复条件')
    expect(wrapper.text()).toContain(label)
    expect(wrapper.text()).not.toContain('probeBlockedReasons.admin.')
    expect(wrapper.text()).not.toContain(`admin.connectionHealth.probeBlockedReasons.${blockedReason}`)
  })

  it.each(['sub2api:ws1:100', ''])('keeps applied health separate from invalid and stale history in event mode %s', async (selectedConnectionId) => {
    const model: ModelHealth = {
      modelName: 'gpt-fixture', providerFamily: 'openai', configured: true, state: 'suspended',
      currentWeight: 0, consecutiveFailures: 3, consecutiveSuccesses: 0,
      lastFailureAt: '2026-10-02T10:00:00Z', lastProbeAt: '2026-10-02T10:05:00Z', lastSuccessAt: null,
      lastLatencyMs: 12, lastErrorKey: 'admin.connectionHealth.errors.probeAuth', lastErrorDetail: 'applied failure t1',
      lastRemoteAction: '', updatedAt: '2026-10-02T10:05:00Z',
      currentHealthResult: { status: 'failure', at: '2026-10-02T10:00:00Z', protocol: 'responses', errorKey: 'admin.connectionHealth.errors.probeAuth', errorDetail: 'applied failure t1' },
      lastAttempt: { at: '2026-10-02T10:05:00Z', protocol: 'responses', probeTimeoutSeconds: 30, disposition: 'invalid', errorDetail: 'invalid attempt t2' },
    }
    const group = mountDetail([makeAccount({ modelHealth: [model] })]).props('group')
    const common: ConnectionHealthEvent = {
      id: 't1', connectionId: 'sub2api:ws1:100', modelName: 'gpt-fixture', ownGroupName: 'Stable Group',
      upstreamSiteId: 'sub2api', upstreamGroupName: 'Stable Group', result: 'auth_error',
      fromState: 'degraded', toState: 'suspended', latencyMs: 12, errorKey: 'admin.connectionHealth.errors.probeAuth',
      errorDetail: 'applied failure t1', remoteAction: '', createdAt: '2026-10-02T10:00:00Z',
      requestProtocol: 'responses', requestTimeoutSeconds: 30, probeDisposition: 'applied',
    }
    const wrapper = mount(ConnectionHealthEventsDialog, {
      props: {
        open: true, selectedConnectionId, groups: [], adminGroups: [group], siteName: (id: string) => id,
        events: [
          { ...common, id: 'stale', result: 'slow_response', latencyMs: 33333, probeDisposition: 'stale', createdAt: '2026-10-02T10:06:00Z' },
          { ...common, id: 't2', result: 'invalid_response', latencyMs: 22222, errorDetail: 'invalid attempt t2', probeDisposition: 'invalid', createdAt: '2026-10-02T10:05:00Z' },
          common,
        ],
      },
      global: { stubs: { Teleport: true, Transition: false } },
    })
    mountedWrappers.push(wrapper)

    expect(wrapper.text()).toContain('当前失败')
    expect(wrapper.text()).toContain('applied failure t1')
    expect(wrapper.text()).toContain('invalid attempt t2')
    expect(wrapper.text()).toContain('Responses / 30s')
    expect(wrapper.text()).toContain('12ms')
    expect(wrapper.text()).not.toContain('33333ms')
    expect(wrapper.text()).not.toContain('22222ms')
    expect(wrapper.text()).not.toContain('其中 1 次为高延迟成功')
    expect(wrapper.findAll('[title]').some(node => node.attributes('title')?.includes('过期尝试'))).toBe(true)
    expect(wrapper.text()).not.toContain('请求在超时前成功返回，也可能尚未达到健康恢复条件')

    await wrapper.setProps({ adminGroups: [{ ...group, accounts: [makeAccount({ modelHealth: [{
      ...model, currentHealthResult: { status: 'unverified', protocol: 'responses' },
      lastAttempt: { disposition: 'applied', result: 'slow_response', protocol: 'responses', probeTimeoutSeconds: 30, at: '2026-10-02T10:10:00Z' },
    }] })] }] })
    expect(wrapper.text()).toContain('当前协议健康依据待验证')
    expect(wrapper.text()).toContain('超过 5 秒仍属于慢响应')
    expect(wrapper.text()).toContain('暂停或观察中的账号需要正常成功并满足原恢复条件')

    await wrapper.setProps({ adminGroups: [{ ...group, accounts: [makeAccount({ modelHealth: [{
      ...model, state: 'healthy', lastErrorKey: '', lastErrorDetail: '', lastSuccessAt: '2026-10-02T10:10:00Z',
      currentHealthResult: { status: 'success', protocol: 'responses', at: '2026-10-02T10:10:00Z' },
      lastAttempt: { disposition: 'applied', protocol: 'responses', at: '2026-10-02T10:10:00Z' },
    }] })] }] })
    expect(wrapper.text()).not.toContain('当前失败')
    expect(wrapper.text()).not.toContain('invalid attempt t2')
    expect(wrapper.text()).not.toContain('applied failure t1')
    expect(wrapper.text()).not.toContain('请求在超时前成功返回，也可能尚未达到健康恢复条件')
  })

  it('retains the applied failure after a newer invalid attempt and only clears it for an applied success', async () => {
    const model = {
      modelName: 'gpt-fixture', providerFamily: 'openai', configured: true, state: 'suspended' as const,
      currentWeight: 0, consecutiveFailures: 3, consecutiveSuccesses: 0,
      lastFailureAt: '2026-10-02T10:00:00Z', lastProbeAt: '2026-10-02T10:05:00Z', lastSuccessAt: null,
      lastLatencyMs: 12, lastErrorKey: 'admin.connectionHealth.errors.probeAuth', lastErrorDetail: 'applied failure t1',
      lastRemoteAction: '', updatedAt: '2026-10-02T10:05:00Z',
      currentHealthResult: { status: 'failure' as const, at: '2026-10-02T10:00:00Z', errorKey: 'admin.connectionHealth.errors.probeAuth', errorDetail: 'applied failure t1' },
      lastAttempt: { at: '2026-10-02T10:05:00Z', protocol: 'responses' as const, disposition: 'invalid' as const, errorDetail: 'invalid attempt t2' },
    }
    const wrapper = mountDetail([makeAccount({ hasEnabledProbePolicy: true, modelHealth: [model] })])
    await accountRow(wrapper, 'Account 100').find('button[aria-label="展开模型结果"]').trigger('click')
    expect(wrapper.text()).toContain('applied failure t1')
    expect(wrapper.text()).toContain('invalid attempt t2')
    await wrapper.setProps({ group: { ...wrapper.props('group'), accounts: [makeAccount({ hasEnabledProbePolicy: true, modelHealth: [{ ...model, state: 'healthy', currentHealthResult: { status: 'success' }, lastAttempt: { disposition: 'applied', protocol: 'responses' } }] })] } })
    expect(wrapper.text()).not.toContain('applied failure t1')
    expect(wrapper.text()).not.toContain('invalid attempt t2')
  })
})
