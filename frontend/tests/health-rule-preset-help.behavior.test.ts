// @vitest-environment jsdom
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import HealthRulePresetFields from '@/modules/admin/components/dashboard/HealthRulePresetFields.vue'
import { defaultHealthRulePreset } from '@/modules/admin/utils/healthRulePresets'
import { t } from '@/locales'
const wrappers: VueWrapper[] = []
afterEach(() => wrappers.splice(0).forEach(w => w.unmount()))
describe('long failure preset help', () => {
 it('shows a daily interval for a new preset before any saved preset exists', () => {
  const wrapper = mount(HealthRulePresetFields, { props: { modelValue: defaultHealthRulePreset(), ruleVersion: 'v2' } }); wrappers.push(wrapper)
  expect((wrapper.get('[data-testid="preset-longFailureIntervalSeconds"]').element as HTMLInputElement).value).toBe('1440')
 })
 it('renders both complete explanations and retains the editable interval', async () => {
  const wrapper = mount(HealthRulePresetFields, { props: { modelValue: { ...defaultHealthRulePreset(), longFailureIntervalSeconds: 86400 }, ruleVersion: 'v2' } }); wrappers.push(wrapper)
  expect(wrapper.text()).toContain('从第一次失败起，中间没有任何一次成功；延迟也算成功。达到这个时长后，探活改用下面的长期失败间隔，并发出站内提醒。')
  expect(wrapper.text()).toContain('连续失败达到长期时长后，按此间隔自动重试，成功即恢复正常探活；推荐 24 小时。包括调度站关停和主站关闭调度的账号。')
  expect((wrapper.get('[data-testid="preset-longFailureIntervalSeconds"]').element as HTMLInputElement).value).toBe('1440')
  await wrapper.get('[data-testid="preset-longFailureIntervalSeconds"]').setValue(120)
  expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toMatchObject({ longFailureIntervalSeconds: 7200 })
 })
 it('renders the existing policy explanation with daily retry and main-site switch boundaries', () => {
  const wrapper = mount({ setup: () => ({ text: t('admin.connectionHealth.policyDrawer.runFlow.steps.dueCheck.description') }), template: '<p>{{ text }}</p>' }); wrappers.push(wrapper)
  expect(wrapper.text()).toContain('达到长期失败时长的账号按长期失败间隔（推荐 24 小时）自动重试，并发出站内提醒；修好后可点“一键正式探活”立即验证。主站调度关闭时，系统仍会把自己关停、已恢复健康的账号改回原状态，但从不改动调度开关，也不会在调度关闭期间关停账号。')
 })
})
