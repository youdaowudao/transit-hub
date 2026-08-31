// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import GroupHealthSetupDrawer from '@/modules/admin/components/dashboard/GroupHealthSetupDrawer.vue'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
  AdminGroupPolicyConfiguration,
  ConnectionHealthPolicy,
  TargetPolicyAssignmentSummary,
} from '@/modules/admin/types/connectionHealth'

const harness = vi.hoisted(() => ({
  createPolicy: vi.fn(),
  loadConfiguration: vi.fn(),
  saveConfiguration: vi.fn(),
  updatePolicy: vi.fn(),
}))

vi.mock('@/modules/admin/composables/useConnectionHealth', () => ({
  connectionHealthMessageKey: (key: string) => key,
  useConnectionHealth: () => ({
    createPolicyForSetup: harness.createPolicy,
    loadAdminGroupPolicyConfiguration: harness.loadConfiguration,
    saveAdminGroupPolicyConfiguration: harness.saveConfiguration,
    updatePolicyForSetup: harness.updatePolicy,
  }),
}))

const now = '2026-08-27T00:00:00Z'
const targetId = 'sub2api:ws1:100'

const makePolicy = (
  id: string,
  name: string,
  overrides: Partial<ConnectionHealthPolicy> = {},
): ConnectionHealthPolicy => ({
  id,
  name,
  enabled: true,
  ownGroupId: '',
  ownGroupName: '',
  modelPattern: '',
  probeMode: 'chat_completions',
  probeIntervalSeconds: 60,
  continueProbeWhenUnschedulable: true,
  unschedulableProbeIntervalMinutes: 60,
  failureThreshold: 3,
  successThreshold: 2,
  cooldownSeconds: 300,
  observationSeconds: 300,
  recoveryStepPercent: 25,
  autoDegradeEnabled: true,
  autoRemoteActionEnabled: false,
  priorityMode: 'none',
  strategyMode: 'health_probe',
  dailyProbeBudget: 1000,
  createdAt: now,
  updatedAt: now,
  modelTargets: [{
    id: `${id}-model`,
    policyId: id,
    modelName: 'gpt-test',
    providerFamily: 'openai',
    enabled: true,
    probePrompt: '',
    maxProbeTokens: 1,
    createdAt: now,
    updatedAt: now,
  }],
  ...overrides,
})

const policySummary = (policy: ConnectionHealthPolicy): TargetPolicyAssignmentSummary => ({
  policyId: policy.id,
  policyName: policy.name,
  enabled: policy.enabled,
  priorityMode: policy.priorityMode,
  strategyMode: policy.strategyMode,
  autoRemoteActionEnabled: policy.autoRemoteActionEnabled,
})

const makeAccount = (
  id: string,
  name: string,
  overrides: Partial<AdminGroupAccount> = {},
): AdminGroupAccount => ({
  id,
  name,
  platform: 'openai',
  type: 'relay',
  status: 'active',
  schedulable: true,
  models: 'gpt-test',
  targetId: id === '100' ? targetId : `sub2api:ws1:${id}`,
  probeAvailable: true,
  modelHealth: [],
  assignedPolicyIds: [],
  assignedPolicies: [],
  effectivePolicyIds: [],
  effectivePolicies: [],
  hasAssignedPolicy: false,
  hasEnabledPolicy: false,
  hasEnabledProbePolicy: false,
  policyAssignmentSource: 'none',
  excludedFromGroupPolicy: false,
  probeModelsConfigured: false,
  intelligenceWeight: null,
  ...overrides,
})

const makeGroup = (
  id: string,
  name: string,
  accounts: AdminGroupAccount[],
  overrides: Partial<AdminGroupHealth> = {},
): AdminGroupHealth => ({
  id,
  name,
  platform: 'sub2api',
  status: 'active',
  type: 'public',
  isExclusive: false,
  subscriptionType: '',
  multiplier: 1,
  multiplierDisplay: '1x',
  accountCount: accounts.length,
  monitoredAccountCount: accounts.filter(account => account.hasEnabledProbePolicy).length,
  excludedAccountCount: accounts.filter(account => account.excludedFromGroupPolicy).length,
  assignedPolicyIds: [],
  assignedPolicies: [],
  hasAssignedPolicy: false,
  hasEnabledPolicy: false,
  hasEnabledProbePolicy: false,
  healthSummary: {
    totalAccounts: accounts.length,
    probeableAccounts: accounts.length,
    unprobeableAccounts: 0,
    healthyModels: 0,
    degradedModels: 0,
    suspendedModels: 0,
    disabledModels: 0,
    unconfiguredModels: accounts.length,
    lastProbeAt: null,
  },
  accounts,
  ...overrides,
})

const groupAccount = (
  policy: ConnectionHealthPolicy,
  overrides: Partial<AdminGroupAccount> = {},
): AdminGroupAccount => makeAccount('100', '待排除账号', {
  effectivePolicyIds: [policy.id],
  effectivePolicies: [policySummary(policy)],
  hasAssignedPolicy: true,
  hasEnabledPolicy: true,
  hasEnabledProbePolicy: policy.enabled && policy.strategyMode !== 'multiplier_only',
  policyAssignmentSource: 'group',
  probeModelsConfigured: policy.enabled && policy.strategyMode !== 'multiplier_only',
  ...overrides,
})

const directAccount = (
  policy: ConnectionHealthPolicy,
  inheritedPolicy?: ConnectionHealthPolicy,
  overrides: Partial<AdminGroupAccount> = {},
): AdminGroupAccount => makeAccount('100', '待排除账号', {
  // 后端 assigned* 会合并账号独立策略和当前分组策略；只有 effective* 已完成独立策略覆盖裁决。
  assignedPolicyIds: [policy.id, ...(inheritedPolicy ? [inheritedPolicy.id] : [])],
  assignedPolicies: [policySummary(policy), ...(inheritedPolicy ? [policySummary(inheritedPolicy)] : [])],
  effectivePolicyIds: [policy.id],
  effectivePolicies: [policySummary(policy)],
  hasAssignedPolicy: true,
  hasEnabledPolicy: policy.enabled,
  hasEnabledProbePolicy: policy.enabled && policy.strategyMode !== 'multiplier_only',
  policyAssignmentSource: 'target',
  probeModelsConfigured: policy.enabled && policy.strategyMode !== 'multiplier_only',
  ...overrides,
})

const configuration = (
  group: AdminGroupHealth,
  policies: ConnectionHealthPolicy[],
  excludedTargetIds: string[] = [],
): AdminGroupPolicyConfiguration => ({
  adminGroupId: group.id,
  adminGroupName: group.name,
  policyIds: policies.map(policy => policy.id),
  policies: policies.map(policySummary),
  excludedTargetIds,
  probeSortFallbackMultiplier: null,
  prioritySyncStatus: 'success',
})

const mountedWrappers: VueWrapper[] = []

const mountDrawer = async (input: {
  group: AdminGroupHealth
  allGroups: AdminGroupHealth[]
  policies: ConnectionHealthPolicy[]
  excludedTargetIds?: string[]
}) => {
  const currentPolicies = input.group.assignedPolicyIds
    ?.flatMap(id => input.policies.filter(policy => policy.id === id)) ?? []
  harness.loadConfiguration.mockResolvedValueOnce({
    configuration: configuration(input.group, currentPolicies, input.excludedTargetIds),
  })
  const wrapper = mount(GroupHealthSetupDrawer, {
    props: {
      open: false,
      group: input.group,
      policies: input.policies,
      allGroups: input.allGroups,
    },
    global: { stubs: { Teleport: true } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

const accountCheckbox = (wrapper: VueWrapper, accountName = '待排除账号') => {
  const row = wrapper.findAll('label').find(label => label.text().includes(accountName))
  if (!row) throw new Error(`missing account row: ${accountName}\n${wrapper.html()}`)
  return row.get('input[type="checkbox"]')
}

const findButton = (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => item.text().trim() === label || item.text().includes(label))
  if (!button) throw new Error(`missing button: ${label}`)
  return button
}

const uncheckAccount = async (wrapper: VueWrapper) => {
  await accountCheckbox(wrapper).setValue(false)
  await nextTick()
}

const enterConfirmation = async (wrapper: VueWrapper) => {
  await findButton(wrapper, '下一步').trigger('click')
  await nextTick()
  await findButton(wrapper, '下一步').trigger('click')
  await nextTick()
}

beforeEach(() => {
  harness.createPolicy.mockReset()
  harness.loadConfiguration.mockReset()
  harness.saveConfiguration.mockReset()
  harness.updatePolicy.mockReset()
  localStorage.clear()
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('group health setup exclusion outcome behavior', () => {
  it('shows that unchecking only excludes the account from this group while another group keeps monitoring it', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const mixedPolicy = makePolicy('policy-mixed', '混合探活策略')
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const mixedGroup = makeGroup('group-mixed', 'GPT混合分组', [groupAccount(mixedPolicy)], {
      assignedPolicyIds: [mixedPolicy.id],
      assignedPolicies: [policySummary(mixedPolicy)],
    })
    const wrapper = await mountDrawer({ group: currentGroup, allGroups: [currentGroup, mixedGroup], policies: [currentPolicy, mixedPolicy] })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('仅从当前分组排除')
    expect(wrapper.text()).toContain('仍由“GPT混合分组”监控：混合探活策略')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
    expect(wrapper.text()).not.toContain('取消勾选的目标不会自动探活、自动降级或调整优先级')
  })

  it('shows an effective direct policy without also presenting inherited groups', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const directPolicy = makePolicy('policy-direct', '账号专用探活')
    const inheritedPolicy = makePolicy('policy-inherited', '不应显示的分组策略')
    const currentGroup = makeGroup('group-current', '当前分组', [directAccount(directPolicy, currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const inheritedGroup = makeGroup('group-inherited', '不应显示的其他分组', [directAccount(directPolicy, inheritedPolicy)], {
      assignedPolicyIds: [inheritedPolicy.id],
      assignedPolicies: [policySummary(inheritedPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, inheritedGroup],
      policies: [currentPolicy, directPolicy, inheritedPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('仍由账号独立策略“账号专用探活”监控')
    expect(wrapper.text()).not.toContain('不应显示的其他分组')
    expect(wrapper.text()).not.toContain('不应显示的分组策略')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
  })

  it('shows automatic probing will stop only when the complete group snapshot has no remaining source', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const wrapper = await mountDrawer({ group: currentGroup, allGroups: [currentGroup], policies: [currentPolicy] })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('从本分组排除后，将停止自动探活')
    expect(wrapper.text()).not.toContain('仍由账号独立策略')
  })

  it('does not claim probing will stop when another group account inventory is incomplete', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const incompleteGroup = makeGroup('group-incomplete', '资料读取失败分组', [], {
      accountsError: 'admin.connectionHealth.errors.accountsFetch',
    })
    const wrapper = await mountDrawer({ group: currentGroup, allGroups: [currentGroup, incompleteGroup], policies: [currentPolicy] })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('当前资料不完整，暂无法确认是否会停止自动探活')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
  })

  it('does not claim probing will stop when a remaining group policy is missing from the full policy list', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const missingPolicy = makePolicy('policy-missing', '辅助请求未返回的分组策略')
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const remainingGroup = makeGroup('group-remaining', '策略资料不完整分组', [groupAccount(missingPolicy)], {
      assignedPolicyIds: [missingPolicy.id],
      assignedPolicies: [policySummary(missingPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, remainingGroup],
      policies: [currentPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('当前资料不完整，暂无法确认是否会停止自动探活')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
  })

  it('does not misreport a direct policy when its full policy details are missing', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const missingDirectPolicy = makePolicy('policy-direct-missing', '辅助请求未返回的账号独立策略')
    const currentGroup = makeGroup('group-current', '当前分组', [directAccount(missingDirectPolicy, currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup],
      policies: [currentPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('当前资料不完整，暂无法确认是否会停止自动探活')
    expect(wrapper.text()).not.toContain('账号独立策略“辅助请求未返回的账号独立策略”当前不产生自动探活')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
  })

  it('shows the policy setting that keeps probing after main-site scheduling is closed', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const continuingPolicy = makePolicy('policy-continuing', '关闭调度后继续探活', {
      continueProbeWhenUnschedulable: true,
      unschedulableProbeIntervalMinutes: 60,
    })
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy, { schedulable: false })], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const remainingGroup = makeGroup('group-remaining', '保留监控分组', [groupAccount(continuingPolicy, { schedulable: false })], {
      assignedPolicyIds: [continuingPolicy.id],
      assignedPolicies: [policySummary(continuingPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, remainingGroup],
      policies: [currentPolicy, continuingPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('仍由“保留监控分组”监控：关闭调度后继续探活')
    expect(wrapper.text()).toContain('主站关闭调度后继续自动探活（每 60 分钟）')
    expect(wrapper.text()).not.toContain('主站关闭调度期间不会自动探活')
  })

  it('distinguishes a remaining policy binding from probing while main-site scheduling is closed', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const stoppingPolicy = makePolicy('policy-stopping', '关闭调度时停止探活', {
      continueProbeWhenUnschedulable: false,
    })
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy, { schedulable: false })], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const remainingGroup = makeGroup('group-remaining', '保留策略分组', [groupAccount(stoppingPolicy, { schedulable: false })], {
      assignedPolicyIds: [stoppingPolicy.id],
      assignedPolicies: [policySummary(stoppingPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, remainingGroup],
      policies: [currentPolicy, stoppingPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('仍由“保留策略分组”监控：关闭调度时停止探活')
    expect(wrapper.text()).toContain('仍绑定上述策略，但主站关闭调度期间不会自动探活')
    expect(wrapper.text()).not.toContain('主站关闭调度后继续自动探活')
  })

  it('keeps an enabled direct multiplier-only policy authoritative without falling back to groups', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const multiplierOnly = makePolicy('policy-multiplier-only', '仅倍率策略', {
      strategyMode: 'multiplier_only',
      priorityMode: 'multiplier',
      modelTargets: [],
    })
    const inheritedPolicy = makePolicy('policy-inherited', '不应生效的其他分组探活')
    const currentGroup = makeGroup('group-current', '当前分组', [directAccount(multiplierOnly, currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const inheritedGroup = makeGroup('group-inherited', '不应回退的其他分组', [directAccount(multiplierOnly, inheritedPolicy)], {
      assignedPolicyIds: [inheritedPolicy.id],
      assignedPolicies: [policySummary(inheritedPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, inheritedGroup],
      policies: [currentPolicy, multiplierOnly, inheritedPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('账号独立策略“仅倍率策略”当前不产生自动探活')
    expect(wrapper.text()).not.toContain('不应回退的其他分组')
    expect(wrapper.text()).not.toContain('不应生效的其他分组探活')
    expect(wrapper.text()).not.toContain('仍由账号独立策略“仅倍率策略”监控')
  })

  it('keeps a direct health policy with no applicable model authoritative without falling back to groups', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const directPolicy = makePolicy('policy-direct-no-model', '账号独立探活但模型不适用', {
      modelTargets: [{
        id: 'direct-model-target',
        policyId: 'policy-direct-no-model',
        modelName: 'direct-only-model',
        providerFamily: 'openai',
        enabled: true,
        probePrompt: '',
        maxProbeTokens: 1,
        createdAt: now,
        updatedAt: now,
      }],
    })
    const inheritedPolicy = makePolicy('policy-inherited', '不应回退的适用分组探活', {
      modelTargets: [{
        id: 'inherited-model-target',
        policyId: 'policy-inherited',
        modelName: 'gpt-test',
        providerFamily: 'openai',
        enabled: true,
        probePrompt: '',
        maxProbeTokens: 1,
        createdAt: now,
        updatedAt: now,
      }],
    })
    const currentGroup = makeGroup('group-current', '当前分组', [directAccount(directPolicy, currentPolicy, {
      probeModelsConfigured: false,
    })], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const inheritedGroup = makeGroup('group-inherited', '不应回退的其他分组', [directAccount(directPolicy, inheritedPolicy, {
      probeModelsConfigured: false,
    })], {
      assignedPolicyIds: [inheritedPolicy.id],
      assignedPolicies: [policySummary(inheritedPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, inheritedGroup],
      policies: [currentPolicy, directPolicy, inheritedPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('账号独立策略“账号独立探活但模型不适用”当前不产生自动探活')
    expect(wrapper.text()).not.toContain('不应回退的其他分组')
    expect(wrapper.text()).not.toContain('不应回退的适用分组探活')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
  })

  it('filters disabled, multiplier-only and no-applicable-model group policies from remaining probe sources', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const disabledPolicy = makePolicy('policy-disabled', '停用策略', { enabled: false })
    const multiplierOnly = makePolicy('policy-multiplier-only', '其他分组仅倍率', {
      strategyMode: 'multiplier_only',
      priorityMode: 'multiplier',
      modelTargets: [],
    })
    const noModelPolicy = makePolicy('policy-no-model', '没有适用模型的策略')
    const currentGroup = makeGroup('group-current', '当前分组', [groupAccount(currentPolicy)], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const disabledGroup = makeGroup('group-disabled', '停用策略分组', [makeAccount('100', '待排除账号')], {
      assignedPolicyIds: [disabledPolicy.id],
      assignedPolicies: [policySummary(disabledPolicy)],
    })
    const multiplierGroup = makeGroup('group-multiplier', '仅倍率分组', [groupAccount(multiplierOnly)], {
      assignedPolicyIds: [multiplierOnly.id],
      assignedPolicies: [policySummary(multiplierOnly)],
    })
    const noModelGroup = makeGroup('group-no-model', '无适用模型分组', [groupAccount(noModelPolicy, {
      probeModelsConfigured: false,
    })], {
      assignedPolicyIds: [noModelPolicy.id],
      assignedPolicies: [policySummary(noModelPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, disabledGroup, multiplierGroup, noModelGroup],
      policies: [currentPolicy, disabledPolicy, multiplierOnly, noModelPolicy],
    })

    await uncheckAccount(wrapper)

    expect(wrapper.text()).toContain('从本分组排除后，将停止自动探活')
    expect(wrapper.text()).not.toContain('停用策略分组')
    expect(wrapper.text()).not.toContain('仅倍率分组')
    expect(wrapper.text()).not.toContain('无适用模型分组')
  })

  it('lists the newly excluded account outcome in confirmation and keeps the existing save payload', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const remainingPolicy = makePolicy('policy-remaining', '剩余探活策略')
    const affected = groupAccount(currentPolicy)
    const keeper = makeAccount('200', '保留账号', {
      effectivePolicyIds: [currentPolicy.id],
      effectivePolicies: [policySummary(currentPolicy)],
      hasAssignedPolicy: true,
      hasEnabledPolicy: true,
      hasEnabledProbePolicy: true,
      policyAssignmentSource: 'group',
      probeModelsConfigured: true,
    })
    const currentGroup = makeGroup('group-current', '当前分组', [affected, keeper], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const remainingGroup = makeGroup('group-remaining', '剩余分组', [groupAccount(remainingPolicy)], {
      assignedPolicyIds: [remainingPolicy.id],
      assignedPolicies: [policySummary(remainingPolicy)],
    })
    const savedConfiguration = configuration(currentGroup, [currentPolicy], [targetId])
    harness.saveConfiguration.mockResolvedValueOnce({ configuration: savedConfiguration })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup, remainingGroup],
      policies: [currentPolicy, remainingPolicy],
    })

    await uncheckAccount(wrapper)
    await enterConfirmation(wrapper)

    expect(wrapper.text()).toContain('本次账号变更')
    expect(wrapper.text()).toContain('待排除账号')
    expect(wrapper.text()).toContain('仅从当前分组排除')
    expect(wrapper.text()).toContain('仍由“剩余分组”监控：剩余探活策略')
    expect(wrapper.text()).toContain('选择 1 个，排除 1 个')
    expect(wrapper.text()).toContain('由已有策略决定')

    await findButton(wrapper, '保存分组策略').trigger('click')
    await flushPromises()

    expect(harness.saveConfiguration).toHaveBeenCalledWith('group-current', {
      policyIds: ['policy-current'],
      excludedTargetIds: [targetId],
      probeSortFallbackMultiplier: null,
    })
    expect(wrapper.emitted('saved')?.[0]).toEqual([savedConfiguration])
  })

  it('shows a previously excluded account as restored to the current group in confirmation', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const affected = groupAccount(currentPolicy, { excludedFromGroupPolicy: true })
    const keeper = makeAccount('200', '保留账号', {
      effectivePolicyIds: [currentPolicy.id],
      effectivePolicies: [policySummary(currentPolicy)],
      hasAssignedPolicy: true,
      hasEnabledPolicy: true,
      hasEnabledProbePolicy: true,
      policyAssignmentSource: 'group',
      probeModelsConfigured: true,
    })
    const currentGroup = makeGroup('group-current', '当前分组', [affected, keeper], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    const wrapper = await mountDrawer({
      group: currentGroup,
      allGroups: [currentGroup],
      policies: [currentPolicy],
      excludedTargetIds: [targetId],
    })

    await accountCheckbox(wrapper).setValue(true)
    await nextTick()
    await enterConfirmation(wrapper)

    expect(wrapper.text()).toContain('本次账号变更')
    expect(wrapper.text()).toContain('待排除账号')
    expect(wrapper.text()).toContain('重新纳入当前分组：当前分组策略')
    expect(wrapper.text()).not.toContain('从本分组排除后，将停止自动探活')
  })

  it('keeps the confirmation and exclusion outcome visible when saving fails', async () => {
    const currentPolicy = makePolicy('policy-current', '当前分组策略')
    const affected = groupAccount(currentPolicy)
    const keeper = makeAccount('200', '保留账号', {
      effectivePolicyIds: [currentPolicy.id],
      effectivePolicies: [policySummary(currentPolicy)],
      hasAssignedPolicy: true,
      hasEnabledPolicy: true,
      hasEnabledProbePolicy: true,
      policyAssignmentSource: 'group',
      probeModelsConfigured: true,
    })
    const currentGroup = makeGroup('group-current', '当前分组', [affected, keeper], {
      assignedPolicyIds: [currentPolicy.id],
      assignedPolicies: [policySummary(currentPolicy)],
    })
    harness.saveConfiguration.mockResolvedValueOnce({ errorKey: 'admin.connectionHealth.errors.request' })
    const wrapper = await mountDrawer({ group: currentGroup, allGroups: [currentGroup], policies: [currentPolicy] })

    await uncheckAccount(wrapper)
    await enterConfirmation(wrapper)
    await findButton(wrapper, '保存分组策略').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('本次账号变更')
    expect(wrapper.text()).toContain('从本分组排除后，将停止自动探活')
    expect(findButton(wrapper, '保存分组策略').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('saved')).toBeUndefined()
  })
})
