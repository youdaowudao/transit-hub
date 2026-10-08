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
  todayQuestionAnswerJudged: submitted,
  todayQuestionAnswerCorrect: correct,
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

describe('C2 latest terminal question-answer results', () => {
  it('shows percentage from recent raw reviews, preserves today, and opens the projected exact batch', async () => {
    const account = makeAccount('recent', 'Recent Account', 0, 100, 100)
    Object.assign(account, {
      recentQuestionAnswer: {
        batchId: 'frozen-batch', source: 'scheduled', scheduleName: 'Frozen plan',
        createdAt: '2026-10-01T00:00:00Z', completedAt: '2026-10-01T00:01:00Z', partial: true,
        requests: { submitted: 5, inProgress: 0, succeeded: 4, failed: 1, cancelled: 0 },
        reviews: { correct: 3, incorrect: 1, unreviewed: 0 },
      },
      activeNewerQuestionAnswerBatch: true,
    })
    const wrapper = mountDetail([account])
    expect(wrapper.findAll('thead th').some(th => th.text().includes('最近正确率'))).toBe(true)
    const cell = rowFor(wrapper, account.name).findAll('td')[8]
    expect(cell.text()).toContain('75%')
    expect(cell.text()).toContain('定时测试')
    expect(cell.text()).toContain('测试中')
    expect(cell.text()).not.toContain('100%')
    expect(cell.text()).not.toMatch(/正确\s*\/|3\s*\/\s*4/)
    await cell.get('button').trigger('click')
    expect(wrapper.emitted('question-answer-view')).toEqual([[{ targetId: account.targetId, batchId: 'frozen-batch' }]])
    expect(account.todayQuestionAnswerCorrect).toBe(100)
  })

  it('distinguishes first active, unreviewed latest, and never-tested without fallback', () => {
    const active = makeAccount('active', 'First Active', 0, 10, 10)
    const unreviewed = makeAccount('unreviewed', 'Latest Unreviewed', 1, 10, 10)
    const never = makeAccount('never', 'Never Tested', 2, 0, 0)
    Object.assign(active, { recentQuestionAnswer: null, activeNewerQuestionAnswerBatch: true })
    Object.assign(never, { recentQuestionAnswer: null, activeNewerQuestionAnswerBatch: false })
    Object.assign(unreviewed, { recentQuestionAnswer: {
      batchId: 'unreviewed-batch', source: 'manual', scheduleName: null,
      createdAt: '2026-10-01T00:00:00Z', completedAt: '2026-10-01T00:01:00Z', partial: false,
      requests: { submitted: 1, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
      reviews: { correct: 0, incorrect: 0, unreviewed: 1 },
    }, activeNewerQuestionAnswerBatch: false })
    const wrapper = mountDetail([active, unreviewed, never])
    const textFor = (name: string) => rowFor(wrapper, name).findAll('td')[8].text()
    expect(textFor(active.name)).toContain('测试中')
    expect(textFor(unreviewed.name)).toContain('待人工判断')
    expect(textFor(never.name)).toContain('尚未测试')
    for (const account of [active, unreviewed, never]) {
      expect(textFor(account.name)).toContain('—')
      expect(textFor(account.name)).not.toMatch(/100%|0%/)
    }
  })
})
