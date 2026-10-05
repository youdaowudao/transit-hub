// @vitest-environment jsdom
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { AdminGroupAccount, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'
import { collectPrioritySyncBlockers, resolvePrioritySyncBlockReasonMessage } from '@/modules/admin/utils/connectionHealthMultiplier'

const wrappers: VueWrapper[] = []
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); document.body.innerHTML = '' })

const mountReason = (reason: string) => {
  const account: AdminGroupAccount = {
    id: '100', name: '验收-倍率账号', platform: 'openai', type: 'subscription', status: 'active',
    schedulable: true, priority: 18, targetId: 'sub2api:ws1:100', probeAvailable: true, modelHealth: [],
    assignedPolicyIds: [], assignedPolicies: [], hasAssignedPolicy: false, hasEnabledPolicy: false,
    hasEnabledProbePolicy: false, priorityManaged: false, probeModelsConfigured: true,
    prioritySyncBlocked: true, prioritySyncBlockReason: reason as AdminGroupAccount['prioritySyncBlockReason'],
  }
  const group: AdminGroupHealth = {
    id: 'group-1', name: '验收-分组', platform: 'openai', status: 'enabled', type: 'subscription',
    isExclusive: false, subscriptionType: '', multiplier: null, multiplierDisplay: '-',
    accountCount: 1, monitoredAccountCount: 0,
    healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0,
      degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null },
    accounts: [account],
  }
  const wrapper = mount(AdminGroupHealthDetail, {
    props: { group, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false },
  })
  wrappers.push(wrapper)
  return { wrapper, group }
}

describe('home cost multiplier refusal reasons', () => {
  it.each([
    ['key_deleted', '上游 Key 已删除'],
    ['admin.upstream.errors.announcementAckRequired', '上游要求先在网页上确认新公告，确认前无法读取数据。'],
    ['admin.upstream.errors.upstreamInsufficientBalance', '上游账户余额不足，上游不允许读取。'],
    ['admin.upstream.errors.upstreamKeyQuotaExhausted', '该 Key 在上游的额度已用尽。'],
    ['admin.upstream.errors.upstreamKeyExpired', '该 Key 在上游已过期。'],
    ['admin.upstream.errors.refreshTokenRejected', '刷新令牌已失效或已被使用，请重新获取后填写。'],
    ['admin.upstream.errors.accessTokenRejected', '访问令牌无效，可能已在上游重新生成。'],
  ])('shows the safe reason %s in the account and preserves it in Priority blockers', (reason, text) => {
    const { wrapper, group } = mountReason(reason)
    expect(wrapper.text()).toContain(text)
    expect(wrapper.text()).not.toContain('上游当前找不到该 Key')
    expect(collectPrioritySyncBlockers([group])[0]?.reason).toBe(reason)
    expect(resolvePrioritySyncBlockReasonMessage(reason, '', '').key).toBe(reason)
  })

  it('preserves partial snapshot absence and rejects unapproved reason keys', () => {
    const { wrapper } = mountReason('key_missing')
    expect(wrapper.text()).toContain('上游当前找不到该 Key')
    expect(wrapper.text()).not.toContain('上游 Key 已删除')
    expect(resolvePrioritySyncBlockReasonMessage('admin.upstream.errors.untrustedMessage', '', '').key).toBe('unknown')
  })
})
