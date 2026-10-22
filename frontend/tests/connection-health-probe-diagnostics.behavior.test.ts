// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import PolicyConfigDrawer from '@/modules/admin/components/dashboard/PolicyConfigDrawer.vue'
import PolicyRunFlowDialog from '@/modules/admin/components/dashboard/PolicyRunFlowDialog.vue'
import ConnectionHealthLinkDetailCard from '@/modules/admin/components/dashboard/ConnectionHealthLinkDetailCard.vue'
import { HelpTooltip } from '@/components/ui/tooltip'
import { t } from '@/locales'
import { c32Model } from './fixtures/c32ModelControl'

const harness = vi.hoisted(() => ({ discoverModels: vi.fn(), manualProbeTarget: vi.fn(), runManualProbeOnce: vi.fn() }))
vi.mock('@/modules/admin/composables/useConnectionHealth', async original => ({
  ...await original<typeof import('@/modules/admin/composables/useConnectionHealth')>(),
  useConnectionHealth: () => ({ ...harness, errorKey: { value: '' } }),
}))
const wrappers: VueWrapper[] = []
const track = (w: VueWrapper) => { wrappers.push(w); return w }
const global = { stubs: { Teleport: true, Transition: false, HealthRuleSettings: true, HealthRulePresetFields: true } }
const help = '这个开关控制是否自动停用或恢复账号。关闭后，已启用的健康优先级管理仍可能修改主站 Priority；只想检查一次且不改主站，请使用‘一次性测试’。'
const degrade = '自动降级按所选规则更新健康状态；已由系统管理健康优先级的账号，状态变化仍可能改变主站 Priority。自动远端动作另外控制账号是否自动停用或恢复。关闭自动远端动作不会停止健康优先级管理；要只看一次测试结果且不改主站，请使用‘一次性测试’。'
const history = '历史记录可能把错误包耗时写成首字，或显示‘上游未返回流式’；这些旧说明不能证明上游不支持流式，请用‘一次性测试’查看最新结果。一次性测试仍可能产生上游调用费用。'
const prefix = '模型 fixture-model 的 Responses 测试'
const general = '尚未确认本次能正常回答；这条错误不足以确定是上游服务还是转发网关的问题。请按记录时间核查对应日志，处理后用‘一次性测试’重试。'
const validationCauses = ['流式事件不是合法 JSON', '回答结束但没有有效正文', '响应未完成', '回答已结束，但回复格式或正文无效', '回复格式或正文无效，未能识别有效回答', '回复超过 1 MiB，无法继续校验']
const diagnostics = [
  prefix + '在等待回答时收到接口错误（HTTP 503）。上游提示：Service temporarily unavailable。' + general,
  prefix + '在开始接收回答时收到错误（HTTP 200）。上游提示：test parameter rejected。上游不接受这次测试使用的参数，本次不增加健康失败次数。请检查分组测试配置和该模型的请求要求，再用‘一次性测试’重试。',
  prefix + '在读取接口回复时中断（HTTP 200）。已知原因：fixture read reset。' + general,
  ...validationCauses.map(cause => prefix + '已收到回复（HTTP 201），但未能确认有效回答。已知原因：' + cause + '。' + general),
]
beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({ models: [{ id: 'fixture-model', name: 'fixture-model' }] })
  harness.manualProbeTarget.mockReset(); harness.runManualProbeOnce.mockReset()
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); document.body.innerHTML = '' })
const click = async (w: VueWrapper, text: string) => {
  const b = w.findAll('button').find(b => b.text().trim() === text)
  if (!b) throw new Error('Missing button: ' + text)
  await b.trigger('click'); await flushPromises()
}
const openProbe = async () => {
  const w = track(mount(ManualOneTimeProbeDialog, { props: { open: false, target: { targetId: 'sub2api:ws1:fixture', accountName: '探活诊断夹具', platform: 'sub2api', type: 'apikey', status: 'active', groupName: '诊断分组', formalModels: [{ id: 'fixture-model', name: 'fixture-model', providerFamily: 'openai' }] } }, global }))
  await w.setProps({ open: true }); await flushPromises(); return w
}

describe('probe diagnostics in existing result surfaces', () => {
  it.each(diagnostics)('once and formal show the full safe cause, with no fake first timings: %s', async detail => {
    harness.runManualProbeOnce.mockResolvedValue({ results: [{ modelName: 'fixture-model', result: 'invalid_response', healthy: false, latencyMs: 18, firstTokenMs: null, firstEventMs: null, protocol: 'responses', probeTimeoutSeconds: 30, errorDetail: detail, probedAt: '2026-10-22T00:00:00Z' }] })
    harness.manualProbeTarget.mockResolvedValue([{ ...c32Model('fixture-model', { state: 'suspended', firstTokenMs: 9000, firstEventMs: 8000 }), probeResult: 'invalid_response', requestErrorDetail: detail, requestProtocol: 'responses', requestTimeoutSeconds: 30, requestLatencyMs: 18, requestFirstTokenMs: null, requestFirstEventMs: null }])
    for (const mode of ['once', 'formal']) {
      const w = await openProbe()
      await click(w, mode === 'once' ? '一次性测试' : '正式手动探活')
      await click(w, mode === 'once' ? '开始测试' : '开始正式探活')
      const row = w.findAll('li').find(r => r.text().includes(detail))
      expect(row).toBeDefined(); expect(row!.text()).toContain('18ms')
      expect(row!.text()).not.toContain('首字'); expect(row!.text()).not.toContain('首个事件')
      expect(row!.text()).not.toContain('上游未返回流式'); expect(row!.text()).not.toContain('9000ms')
      if (detail.includes('已知原因：')) expect(row!.text()).not.toContain('上游没有提供可安全显示的具体原因')
      if (detail.includes('未能确认有效回答')) expect(row!.text()).not.toContain('开始接收回答时收到错误')
      expect(w.emitted('completed')?.length ?? 0).toBe(mode === 'formal' ? 1 : 0)
      w.unmount(); wrappers.pop()
    }
  })

  it('event details retain real SSE first event and omit both readings on HTTP failure', async () => {
    const record = { id: 'probe-error-event', connectionId: 'fixture', result: 'server_error', errorDetail: diagnostics[0], latencyMs: 18, firstTokenMs: null, firstEventMs: null, createdAt: '2026-10-22T00:00:00Z', requestProtocol: 'responses', requestTimeoutSeconds: 30, ruleVersion: 'v2' }
    const w = track(mount(ConnectionHealthLinkDetailCard, { props: { siteLabel: '站点', upstreamGroupName: '分组', accountName: '账号', modelName: 'fixture-model', provider: 'openai', isActionCard: false, state: 'suspended', latestLatencyMs: 18, lastProbeAt: record.createdAt, lastSuccessAt: null, lastFailureAt: record.createdAt, lastErrorKey: 'server_error', lastErrorDetail: diagnostics[0]!, failureFromLoadedRecords: false, elapsedSeconds: null, effectiveIntervalSeconds: 60, effectivePolicySources: [], budgetPolicyId: '', availabilityPct: 0, records: [record as never], nextProbeText: '', actionSource: '', actionAt: null, remoteAction: '' } }))
    expect(w.text()).toContain(diagnostics[0]); let title = w.get('span[title]').attributes('title')
    expect(title).toContain('探活耗时：18ms'); expect(title).not.toContain('首字'); expect(title).not.toContain('首个事件')
    await w.setProps({ records: [{ ...record, firstEventMs: 12, errorDetail: diagnostics[1] } as never] })
    title = w.get('span[title]').attributes('title'); expect(title).toContain('首个事件：12ms'); expect(title).not.toContain('首字')
  })
})

describe('Sub2API help boundaries and workspace changes', () => {
  it('drawer selects the two Sub2API help strings and passes the existing platform into run flow', async () => {
    const w = track(mount(PolicyConfigDrawer, { props: { open: false, policy: null, ownGroupOptions: [], workspacePlatform: 'sub2api' }, global }))
    await w.setProps({ open: true }); await flushPromises()
    expect(w.text()).toContain(help)
    expect(w.findAllComponents(HelpTooltip).some(t => t.props('text') === help)).toBe(true)
    await click(w, '运行流程')
    expect(w.text()).toContain(degrade); expect(w.text()).toContain(history)
    expect(w.text()).not.toContain('绝不调用任何上游接口')
    await w.setProps({ workspacePlatform: 'newapi' })
    expect(w.text()).not.toContain(help); expect(w.text()).not.toContain(degrade); expect(w.text()).not.toContain(history)
    expect(w.text()).toContain(t('admin.connectionHealth.policyDrawer.autoRemoteActionHelp'))
    expect(w.text()).toContain(t('admin.connectionHealth.policyDrawer.runFlow.steps.autoDegradeVsRemoteAction.description'))
    expect(w.emitted('save')).toBeUndefined()
  })
  it.each(['newapi', '', undefined])('run flow keeps old text when platform is %s', platform => {
    const w = track(mount(PolicyRunFlowDialog, { props: { open: true, workspacePlatform: platform }, global }))
    expect(w.text()).toContain(t('admin.connectionHealth.policyDrawer.runFlow.steps.autoDegradeVsRemoteAction.description'))
    expect(w.text()).not.toContain(degrade); expect(w.text()).not.toContain(history)
  })
})
