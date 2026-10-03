// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SettingsView from '@/modules/admin/views/SettingsView.vue'
import CampaignEditorDrawer from '@/modules/admin/components/group-rate-campaigns/CampaignEditorDrawer.vue'
import CampaignDetailDrawer from '@/modules/admin/components/group-rate-campaigns/CampaignDetailDrawer.vue'
import AutoPricingConfigDrawer from '@/modules/admin/components/dashboard/AutoPricingConfigDrawer.vue'
import GroupRateCampaignsView from '@/modules/admin/views/GroupRateCampaignsView.vue'
import { t } from '@/locales'
import type { StrategySettings } from '@/modules/admin/types/settings'
import type { MySiteMapping } from '@/modules/admin/types/mySites'

const harness = vi.hoisted(() => ({
  getChannels: vi.fn(), getStrategy: vi.fn(), saveStrategy: vi.fn(), saveChannels: vi.fn(),
  testBot: vi.fn(), createCampaign: vi.fn(), getCampaign: vi.fn(),
  campaignRows: [] as Array<Record<string, unknown>>,
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {}, path: '/group-rate-campaigns' }),
  useRouter: () => ({ replace: vi.fn() }),
}))

vi.mock('@/modules/admin/composables/useGroupRateCampaigns', async () => {
  const { ref } = await import('vue')
  return { useGroupRateCampaigns: () => ({
    campaigns: ref(harness.campaignRows), total: ref(harness.campaignRows.length),
    page: ref(1), pageSize: ref(10), totalPages: ref(1), statusFilter: ref(''), notifyDefaults: ref(null),
    isLoading: ref(false), errorKey: ref(null), loadCampaigns: vi.fn(), setStatusFilter: vi.fn(),
    goToPage: vi.fn(), startCampaign: vi.fn(), endCampaign: vi.fn(), cancelCampaign: vi.fn(),
  }) }
})

vi.mock('@/modules/admin/api/settings', () => ({
  getNotificationChannelSettings: harness.getChannels,
  getStrategySettings: harness.getStrategy,
  saveStrategySettings: harness.saveStrategy,
  saveNotificationChannelSettings: harness.saveChannels,
  testNotificationChannel: harness.testBot,
  getSmtpSettings: vi.fn(async () => ({ host: '', port: 587, username: '', fromEmail: '', fromName: '', tlsMode: 'starttls', passwordConfigured: false, updatedAt: null })),
  saveSmtpSettings: vi.fn(), testSmtpEmail: vi.fn(),
}))

vi.mock('@/modules/admin/api/system', () => ({
  getSystemUpgradeStatus: vi.fn(async () => ({ state: 'idle' })),
  getSystemRestartStatus: vi.fn(async () => ({ state: 'idle' })),
  getSystemRollbackStatus: vi.fn(async () => ({ state: 'idle' })),
  getSystemVersion: vi.fn(async () => ({ version: '2.8.10' })),
  isRollbackRouteNotFound: vi.fn(() => false), isTransientSystemApiError: vi.fn(() => false),
  startSystemUpgrade: vi.fn(), startSystemRestart: vi.fn(), startSystemRollback: vi.fn(),
}))

vi.mock('@/modules/admin/api/mySites', () => ({
  getMySiteMappingOptions: vi.fn(async () => ({ ownGroups: [{ groupName: 'sample-group', multiplier: 1 }], mappings: [] })),
}))

vi.mock('@/modules/admin/api/groupRateCampaigns', () => ({
  createGroupRateCampaign: harness.createCampaign,
  getGroupRateCampaign: harness.getCampaign,
  previewGroupRateCampaign: vi.fn(async () => ({ items: [], total: 0 })),
}))

const telegram = {
  id: 'tg-original', name: 'Original receiver', enabled: true,
  botToken: 'test-token', chatId: 'test-chat', proxyUrl: '',
}
const baseStrategy: StrategySettings = {
  enableRefreshInterval: true, refreshInterval: 60, enableBalanceWarning: true,
  defaultBalanceThreshold: 10, balanceNotifyBotIds: ['tg-original'],
  balanceTemplate: 'custom {balance}', balanceTemplateFormat: 'text',
  enableMultiplierAlert: true, multiplierNotifyBotIds: ['tg-original'],
  multiplierTemplate: 'custom {newRate}', multiplierTemplateFormat: 'text',
}
const wrappers: VueWrapper[] = []
const keep = <T extends VueWrapper>(wrapper: T): T => { wrappers.push(wrapper); return wrapper }
const global = { stubs: { teleport: true, EmailTemplatesPanel: true, TestQuestionsPanel: true, NotificationTemplateEditor: true, Tooltip: true } }
const button = (wrapper: VueWrapper, text: string) => {
  const match = wrapper.findAll('button').find(item => item.text() === text)
  expect(match, text).toBeDefined()
  return match!
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  harness.getChannels.mockResolvedValue({ telegram: [telegram] })
  harness.getStrategy.mockResolvedValue(structuredClone(baseStrategy))
  harness.saveStrategy.mockImplementation(async value => value)
  harness.saveChannels.mockImplementation(async value => value)
  harness.testBot.mockResolvedValue({ success: true })
  harness.createCampaign.mockResolvedValue({ id: 'new-campaign' })
  harness.campaignRows = []
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('Telegram recipient reconciliation', () => {
  it('shows activity-list lookup unavailability without hiding its original recovery entry', async () => {
    harness.campaignRows = [{
      id: 'unavailable-campaign', name: 'Running sample', status: 'running',
      startMode: 'manual', startAt: null, endMode: 'manual', endAt: null,
      startedAt: '2099-04-01T00:00:00Z', endedAt: null,
      summary: { total: 1, applied: 1, applyFailed: 0, restored: 0, restoreFailed: 0 }, createdBy: 'operator',
      notifyEnabled: true, notifyRecipientsUnavailable: true,
    }]
    const wrapper = keep(mount(GroupRateCampaignsView, { global: { ...global, stubs: { ...global.stubs, CampaignEditorDrawer: true, CampaignDetailDrawer: true } } }))
    const row = wrapper.get('tbody tr')
    expect(button(wrapper, t('admin.groupRateCampaigns.actions.viewDetail')).attributes('disabled')).toBeUndefined()
    expect(row.text()).toContain(t('admin.groupRateCampaigns.status.running'))
    expect(row.text()).toContain(t('admin.settings.recipientsUnavailable'))
    expect(row.text()).not.toContain(t('admin.settings.recipientsInvalid'))
  })

  it.each([
    { status: 'draft', action: 'start' },
    { status: 'scheduled', action: 'cancel' },
    { status: 'running', action: 'end' },
  ])('keeps activity $action available and warns about notification lookup failure', async ({ status, action }) => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    harness.getCampaign.mockResolvedValue({
      id: 'unavailable-campaign', name: 'Operation sample', status,
      startMode: 'manual', startAt: null, endMode: 'manual', endAt: null,
      startedAt: '2099-04-01T00:00:00Z', endedAt: null,
      summary: { total: 1, applied: 1, applyFailed: 0, restored: 0, restoreFailed: 0 }, createdBy: 'operator', items: [],
      notify: { enabled: true, botIds: ['tg-original'], startTemplate: 'start', endTemplate: 'end', recipientsUnavailable: true },
    })
    const wrapper = keep(mount(CampaignDetailDrawer, { props: { open: false, campaignId: 'unavailable-campaign' }, global }))
    await wrapper.setProps({ open: true })
    await flushPromises()
    const actionButton = button(wrapper, t(`admin.groupRateCampaigns.actions.${action}`))
    expect(actionButton.attributes('disabled')).toBeUndefined()
    await actionButton.trigger('click')
    expect(wrapper.emitted(action)?.[0]).toEqual(['unavailable-campaign'])
    expect(wrapper.text()).toContain(t('admin.settings.recipientsUnavailable'))
    expect(wrapper.text()).not.toContain(t('admin.settings.recipientsInvalid'))
    expect(t('admin.settings.recipientsUnavailable')).not.toMatch(/结果已保留|通知未发送/)
  })

  it('shows unavailable activity defaults and saves their original receivers and templates', async () => {
    harness.getChannels.mockRejectedValue(new Error('admin.settings.errors.network'))
    const defaults = { enabled: true, botIds: ['tg-original'], startTemplate: 'start', endTemplate: 'end', recipientsUnavailable: true }
    const wrapper = keep(mount(CampaignEditorDrawer, { props: { open: false, notifyDefaults: defaults }, global }))
    await wrapper.setProps({ open: true })
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('sample campaign')
    const groupLabel = wrapper.findAll('label').find(item => item.text() === 'sample-group')!
    await groupLabel.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[type="number"]').setValue('1.25')
    await button(wrapper, t('admin.groupRateCampaigns.actions.confirmCreate')).trigger('click')
    await flushPromises()
    expect(harness.createCampaign).toHaveBeenCalledWith(expect.objectContaining({
      notify: { enabled: true, botIds: ['tg-original'], startTemplate: 'start', endTemplate: 'end' },
    }))
    expect(wrapper.text()).toContain(t('admin.settings.recipientsUnavailable'))
    expect(wrapper.text()).not.toContain(t('admin.settings.recipientsInvalid'))
  })

  it('retains unavailable activity defaults when the next channel read succeeds without matching receivers', async () => {
    harness.getChannels.mockResolvedValue({ telegram: [] })
    const defaults = { enabled: true, botIds: ['tg-original'], startTemplate: ' start ', endTemplate: ' end ', recipientsUnavailable: true }
    const wrapper = keep(mount(CampaignEditorDrawer, { props: { open: false, notifyDefaults: defaults }, global }))
    await wrapper.setProps({ open: true })
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('sample campaign')
    const groupLabel = wrapper.findAll('label').find(item => item.text() === 'sample-group')!
    await groupLabel.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[type="number"]').setValue('1.25')
    await button(wrapper, t('admin.groupRateCampaigns.actions.confirmCreate')).trigger('click')
    await flushPromises()
    expect(harness.createCampaign).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({
      selection: expect.objectContaining({ groups: [{ groupName: 'sample-group', campaignMultiplier: 1.25 }] }),
      notify: { enabled: true, botIds: ['tg-original'], startTemplate: ' start ', endTemplate: ' end ' },
    }))
    expect(wrapper.text()).toContain(t('admin.settings.recipientsUnavailable'))
    expect(wrapper.text()).not.toContain(t('admin.settings.recipientsInvalid'))
  })

  it.each([
    { surrounding: false, format: 'text' as const },
    { surrounding: true, format: 'html' as const },
  ])('preserves the saved current balance template and explicit format: $format / $surrounding', async ({ surrounding, format }) => {
    const currentDefault = t('admin.settings.sections.templates.balanceDefaultTemplate', { siteName: '{siteName}', balance: '{balance}', threshold: '{threshold}' })
    expect(currentDefault).not.toBe('admin.settings.sections.templates.balanceDefaultTemplate')
    const savedTemplate = surrounding ? ` \n${currentDefault}\n ` : currentDefault
    harness.getStrategy.mockResolvedValue({ ...baseStrategy, balanceTemplate: savedTemplate, balanceTemplateFormat: format })
    const wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    await button(wrapper, t('admin.settings.save')).trigger('click')
    await flushPromises()
    expect(harness.saveStrategy).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({
      balanceTemplate: savedTemplate,
      balanceTemplateFormat: format,
      defaultBalanceThreshold: 10,
      balanceNotifyBotIds: ['tg-original'],
    }))
  })

  it('keeps the original Telegram receiver in mixed strategy selections and saves unchanged thresholds and business refresh', async () => {
    harness.getStrategy.mockResolvedValue({ ...baseStrategy, balanceNotifyBotIds: ['removed-receiver', 'tg-original'], multiplierNotifyBotIds: ['removed-receiver'] })
    const wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    expect(wrapper.text()).toContain(t('admin.settings.recipientsInvalid'))
    expect(wrapper.get('input[aria-label="' + t('admin.settings.sections.thresholds.balanceWarning') + '"]').element).toHaveProperty('checked', true)
    expect(wrapper.get('input[aria-label="' + t('admin.settings.sections.thresholds.multiplierChangeWarning') + '"]').element).toHaveProperty('checked', false)
    await button(wrapper, t('admin.settings.save')).trigger('click')
    await flushPromises()
    expect(harness.saveStrategy).toHaveBeenCalledWith(expect.objectContaining({
      enableRefreshInterval: true, refreshInterval: 60, defaultBalanceThreshold: 10,
      enableBalanceWarning: true, balanceNotifyBotIds: ['tg-original'],
      enableMultiplierAlert: false, multiplierNotifyBotIds: [],
      balanceTemplate: 'custom {balance}', multiplierTemplate: 'custom {newRate}',
    }))
  })

  it('surfaces backend-invalid selections even after the removed IDs have been sanitized', async () => {
    harness.getStrategy.mockResolvedValue({ ...baseStrategy, enableBalanceWarning: false, balanceNotifyBotIds: [], balanceNotifyRecipientsInvalid: true })
    const wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    expect(wrapper.findAll('[role="status"]').some(item => item.text().includes(t('admin.settings.recipientsInvalid')))).toBe(true)
    expect(wrapper.get('input[aria-label="' + t('admin.settings.sections.thresholds.balanceWarning') + '"]').element).toHaveProperty('checked', false)
  })

  it('retains a disabled Telegram reference and does not clear selections after a failed channels read', async () => {
    harness.getChannels.mockResolvedValue({ telegram: [{ ...telegram, enabled: false }] })
    let wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    await button(wrapper, t('admin.settings.save')).trigger('click')
    await flushPromises()
    expect(harness.saveStrategy).toHaveBeenLastCalledWith(expect.objectContaining({ balanceNotifyBotIds: ['tg-original'], enableBalanceWarning: true }))
    harness.getChannels.mockRejectedValue(new Error('admin.settings.errors.network'))
    wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    await button(wrapper, t('admin.settings.save')).trigger('click')
    await flushPromises()
    expect(harness.saveStrategy).toHaveBeenLastCalledWith(expect.objectContaining({ balanceNotifyBotIds: ['tg-original'], enableBalanceWarning: true }))
  })

  it('offers only Telegram channel forms and tests the original receiver', async () => {
    const wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    await wrapper.get('#settings-tab-channels').trigger('click')
    expect(wrapper.text()).toContain(t('admin.settings.sections.channels.telegram'))
    expect(wrapper.text()).not.toMatch(/钉钉|企业微信|QQ|飞书/)
    await button(wrapper, t('admin.settings.sections.channels.testConnection')).trigger('click')
    await flushPromises()
    expect(harness.testBot).toHaveBeenCalledExactlyOnceWith({ channel: 'telegram', telegramBotToken: 'test-token', telegramChatId: 'test-chat', telegramProxyUrl: '' })
    await button(wrapper, t('admin.settings.save')).trigger('click')
    await flushPromises()
    expect(harness.saveChannels).toHaveBeenCalledExactlyOnceWith({ telegram: [telegram] })
  })

  it('clears a deleted Telegram receiver without selecting a different configured robot', async () => {
    harness.getChannels.mockResolvedValue({ telegram: [telegram, { ...telegram, id: 'tg-other', name: 'Other receiver' }] })
    const wrapper = keep(mount(SettingsView, { global }))
    await flushPromises()
    await wrapper.get('#settings-tab-channels').trigger('click')
    await wrapper.findAll('button.text-red-500')[0].trigger('click')
    await wrapper.get('#settings-tab-strategy').trigger('click')
    expect(wrapper.text()).toContain(t('admin.settings.recipientsInvalid'))
    await button(wrapper, t('admin.settings.save')).trigger('click')
    await flushPromises()
    expect(harness.saveStrategy).toHaveBeenCalledWith(expect.objectContaining({
      enableRefreshInterval: true, enableBalanceWarning: false, balanceNotifyBotIds: [],
      enableMultiplierAlert: false, multiplierNotifyBotIds: [],
    }))
  })

  it.each([
    { ids: ['removed-receiver'], enabled: false, expected: [] },
    { ids: ['removed-receiver', 'tg-original'], enabled: true, expected: ['tg-original'] },
  ])('creates an activity with only its original valid receivers: $ids', async ({ ids, enabled, expected }) => {
    const wrapper = keep(mount(CampaignEditorDrawer, { props: { open: false, notifyDefaults: { enabled: true, botIds: ids, startTemplate: 'start', endTemplate: 'end' } }, global }))
    await wrapper.setProps({ open: true })
    await flushPromises()
    expect(wrapper.text()).toContain(t('admin.settings.recipientsInvalid'))
    await wrapper.get('input[type="text"]').setValue('sample campaign')
    const groupLabel = wrapper.findAll('label').find(item => item.text() === 'sample-group')!
    await groupLabel.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[type="number"]').setValue('1.25')
    await button(wrapper, t('admin.groupRateCampaigns.actions.confirmCreate')).trigger('click')
    await flushPromises()
    expect(harness.createCampaign).toHaveBeenCalledWith(expect.objectContaining({
      name: 'sample campaign', selection: expect.objectContaining({ groups: [{ groupName: 'sample-group', campaignMultiplier: 1.25 }] }),
      notify: { enabled, botIds: expected, startTemplate: 'start', endTemplate: 'end' },
    }))
  })

  it('preserves activity notification defaults when the channels API fails', async () => {
    harness.getChannels.mockRejectedValue(new Error('admin.settings.errors.network'))
    const defaults = { enabled: true, botIds: ['tg-original'], startTemplate: 'start', endTemplate: 'end' }
    const wrapper = keep(mount(CampaignEditorDrawer, { props: { open: false, notifyDefaults: defaults }, global }))
    await wrapper.setProps({ open: true })
    await flushPromises()
    expect(wrapper.text()).not.toContain(t('admin.settings.recipientsInvalid'))
    await wrapper.get('input[type="text"]').setValue('sample campaign')
    const groupLabel = wrapper.findAll('label').find(item => item.text() === 'sample-group')!
    await groupLabel.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[type="number"]').setValue('1.25')
    await button(wrapper, t('admin.groupRateCampaigns.actions.confirmCreate')).trigger('click')
    await flushPromises()
    expect(harness.createCampaign).toHaveBeenCalledWith(expect.objectContaining({ notify: defaults }))
  })

  it('keeps auto-pricing enabled while explaining that its sanitized notification receivers must be selected again', async () => {
    const mapping: MySiteMapping = {
      ownGroup: 'sample-group', upstreamTargets: [{ siteId: 'source', groupName: 'upstream' }],
      enableAutoPricing: true, autoPricingSource: 'lowest_upstream', autoPricingStrategy: 'percentage',
      enableAutoPricingNotify: false, autoPricingNotifyBotIds: [], autoPricingNotifyRecipientsInvalid: true,
    }
    const wrapper = keep(mount(AutoPricingConfigDrawer, { props: { open: false, mapping, upstreamMultipliers: new Map([['source::upstream', 1]]), availableBots: [] }, global }))
    await wrapper.setProps({ open: true })
    expect(wrapper.text()).toContain(t('admin.settings.recipientsInvalid'))
    await button(wrapper, t('admin.groupAssociations.autoPricingDrawer.save')).trigger('click')
    expect(wrapper.emitted('save')?.[0]?.[0]).toEqual(expect.objectContaining({ enableAutoPricing: true, enableAutoPricingNotify: false, autoPricingNotifyBotIds: [] }))
  })

  it('shows temporary notification unavailability while saving the original receivers and auto-pricing settings', async () => {
    const mapping: MySiteMapping = {
      ownGroup: 'sample-group', upstreamTargets: [{ siteId: 'source', groupName: 'upstream' }],
      enableAutoPricing: true, autoPricingSource: 'lowest_upstream', autoPricingStrategy: 'percentage',
      percentageIncrease: 12.5, enableAutoPricingNotify: true, autoPricingNotifyBotIds: ['tg-original'],
      autoPricingNotifyRecipientsUnavailable: true, lastAutoPricingRun: { status: 'applied', targetMultiplier: 1.125 },
    }
    const wrapper = keep(mount(AutoPricingConfigDrawer, { props: { open: false, mapping, upstreamMultipliers: new Map([['source::upstream', 1]]), availableBots: [] }, global }))
    await wrapper.setProps({ open: true })
    expect(wrapper.text()).toContain(t('admin.settings.recipientsUnavailable'))
    expect(wrapper.text()).not.toContain(t('admin.settings.recipientsInvalid'))
    await button(wrapper, t('admin.groupAssociations.autoPricingDrawer.save')).trigger('click')
    expect(wrapper.emitted('save')?.[0]?.[0]).toEqual(expect.objectContaining({
      enableAutoPricing: true, percentageIncrease: 12.5, enableAutoPricingNotify: true, autoPricingNotifyBotIds: ['tg-original'],
    }))
    expect(wrapper.emitted('save')?.[0]?.[0]).not.toHaveProperty('autoPricingNotifyRecipientsUnavailable')
  })

  it('renders Singapore times and invalid notification recipients directly in activity list rows', async () => {
    vi.stubEnv('TZ', 'Asia/Tokyo')
    const base = {
      name: 'Scheduled sample', status: 'scheduled', startMode: 'scheduled', startAt: '2099-04-01T00:00:00Z',
      endMode: 'scheduled', endAt: '2099-04-01T01:00:00Z', startedAt: null, endedAt: null,
      summary: { total: 1, applied: 0, applyFailed: 0, restored: 0, restoreFailed: 0 }, createdBy: 'operator',
      notifyEnabled: false,
    }
    harness.campaignRows = [{ ...base, id: 'invalid', notifyRecipientsInvalid: true }, { ...base, id: 'valid', notifyRecipientsInvalid: false }]
    const wrapper = keep(mount(GroupRateCampaignsView, { global: { ...global, stubs: { ...global.stubs, CampaignEditorDrawer: true, CampaignDetailDrawer: true } } }))
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].findAll('td')[2].text()).toContain('08:00')
    expect(rows[0].findAll('td')[2].text()).not.toContain('09:00')
    expect(rows[0].findAll('td')[3].text()).toContain('09:00')
    expect(rows[0].text()).toContain(t('admin.settings.recipientsInvalid'))
    expect(rows[1].text()).not.toContain(t('admin.settings.recipientsInvalid'))
  })
})
