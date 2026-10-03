// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import LotteryAdminPage from '@/modules/lottery/LotteryAdminPage.vue'
import CampaignEditorDrawer from '@/modules/admin/components/group-rate-campaigns/CampaignEditorDrawer.vue'
import EmbedTicketList from '@/modules/embed/tickets/components/EmbedTicketList.vue'
import MassEmailView from '@/modules/admin/views/MassEmailView.vue'
import { t } from '@/locales'
import type { LotteryCampaign } from '@/modules/lottery/types'

const harness = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), update: vi.fn(), create: vi.fn(), massUsers: vi.fn() }))
vi.mock('@/modules/lottery/api/lottery', () => ({
  listLotteryCampaigns: harness.list, getLotteryCampaign: harness.get, updateLotteryCampaign: harness.update,
  listLotteryEntries: vi.fn(async () => ({ items: [] })), listLotteryAudit: vi.fn(async () => ({ items: [] })),
  listLotterySubscriptionGroups: vi.fn(async () => ({ items: [] })), getLotteryEmbedConfig: vi.fn(async () => null),
  cancelLotteryCampaign: vi.fn(), closeLotteryCampaign: vi.fn(), completeManualLotteryReward: vi.fn(),
  createLotteryCampaign: vi.fn(), drawLotteryCampaign: vi.fn(), publishLotteryCampaign: vi.fn(),
  retryLotteryReward: vi.fn(), rotateLotteryEmbedToken: vi.fn(),
}))
vi.mock('@/modules/admin/api/mySites', () => ({
  getMySiteMappingOptions: vi.fn(async () => ({ ownGroups: [{ groupName: 'sample-group', multiplier: 1 }], mappings: [] })),
}))
vi.mock('@/modules/admin/api/settings', () => ({ getNotificationChannelSettings: vi.fn(async () => ({ telegram: [] })), getEmailTemplates: vi.fn(async () => []) }))
vi.mock('@/modules/admin/api/massEmail', () => ({
  listMassEmailUsers: harness.massUsers,
  listMassEmailBatches: vi.fn(async () => ({ items: [] })),
  listMassEmailBatchItems: vi.fn(async () => ({ items: [] })),
  getMassEmailBatch: vi.fn(), cancelMassEmailBatch: vi.fn(), createMassEmailBatch: vi.fn(),
}))
vi.mock('@/modules/admin/api/groupRateCampaigns', () => ({ createGroupRateCampaign: harness.create, previewGroupRateCampaign: vi.fn() }))

const campaign: LotteryCampaign = {
  id: 'isolated-time-fixture', name: 'Original time', description: 'Preserve schedule', status: 'draft',
  registrationStart: '2099-04-01T00:00:00Z', registrationEnd: '2099-04-01T01:00:00Z', drawAt: '2099-04-01T02:00:00Z',
  drawMode: 'scheduled', publicWinners: true, algorithmVersion: 'v1', entryCount: 0, winnerCount: 0,
  prizes: [{ id: 'prize', campaignId: 'isolated-time-fixture', type: 'balance', name: 'Unchanged amount', quantity: 1, sortOrder: 1, balanceAmount: '700.25', deliveryMode: 'sub2api_auto', valueMarker: 700.25 }],
  createdAt: '2099-03-01T00:00:00Z', updatedAt: '2099-03-01T00:00:00Z',
}
const wrappers: VueWrapper[] = []
const keep = <T extends VueWrapper>(wrapper: T): T => { wrappers.push(wrapper); return wrapper }
const global = { stubs: { teleport: true, Tooltip: { template: '<span><slot /></span>' } } }
const click = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => item.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
}
beforeEach(() => {
  vi.clearAllMocks()
  harness.list.mockResolvedValue({ items: [campaign] })
  harness.get.mockResolvedValue(campaign)
  harness.update.mockResolvedValue(campaign)
  harness.create.mockResolvedValue({ id: 'scheduled-fixture' })
  harness.massUsers.mockResolvedValue({ items: [], total: 0, page: 1, pageSize: 20, totalPages: 1 })
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  vi.unstubAllEnvs()
})

describe('Singapore schedule inputs independent of browser timezone', () => {
  it.each(['Asia/Tokyo', 'America/New_York'])('queries mass-email users with Singapore timezone from %s', async timezone => {
    vi.stubEnv('TZ', timezone)
    keep(mount(MassEmailView, { global }))
    await flushPromises()
    expect(harness.massUsers).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ timezone: 'Asia/Singapore', page: 1, pageSize: 20 }))
  })

  it.each(['Asia/Tokyo', 'America/New_York'])('renders ticket business timestamps in Singapore from %s', timezone => {
    vi.stubEnv('TZ', timezone)
    const wrapper = keep(mount(EmbedTicketList, {
      props: { tickets: [{ id: 'timestamp-fixture', title: 'Business date', status: 'open', category: 'other', priority: 'normal', manualEmail: '', lastMessageAt: '2099-04-01T00:00:00Z' }], isLoading: false, errorKey: null },
      global: { stubs: { EmbedTicketCreateModal: true } },
    }))
    expect(wrapper.text()).toContain('08:00')
    expect(wrapper.text()).not.toContain(timezone === 'Asia/Tokyo' ? '09:00' : '20:00')
  })

  it.each(['Asia/Tokyo', 'America/New_York'])('keeps lottery schedule unchanged when edited from %s', async timezone => {
    vi.stubEnv('TZ', timezone)
    expect(new Date('2099-04-01T00:00:00Z').getTimezoneOffset()).toBe(timezone === 'Asia/Tokyo' ? -540 : 240)
    const wrapper = keep(mount(LotteryAdminPage, { global }))
    await flushPromises()
    await wrapper.get('button[aria-label="' + t('admin.lottery.actions.edit') + '"]').trigger('click')
    const inputs = wrapper.findAll('input[type="datetime-local"]')
    expect(inputs.map(input => (input.element as HTMLInputElement).value)).toEqual(['2099-04-01T08:00', '2099-04-01T09:00', '2099-04-01T10:00'])
    await wrapper.get('form[role="dialog"]').trigger('submit')
    await flushPromises()
    expect(harness.update).toHaveBeenCalledWith(campaign.id, expect.objectContaining({
      registrationStart: '2099-04-01T08:00:00', registrationEnd: '2099-04-01T09:00:00', drawAt: '2099-04-01T10:00:00',
      prizes: [expect.objectContaining({ balanceAmount: '700.25', quantity: 1 })],
    }))
  })

  it.each(['Asia/Tokyo', 'America/New_York'])('submits campaign start and end as Singapore instants from %s', async timezone => {
    vi.stubEnv('TZ', timezone)
    expect(new Date('2099-04-01T00:00:00Z').getTimezoneOffset()).toBe(timezone === 'Asia/Tokyo' ? -540 : 240)
    const wrapper = keep(mount(CampaignEditorDrawer, { props: { open: false, notifyDefaults: { enabled: false, botIds: [], startTemplate: '', endTemplate: '' } }, global }))
    await wrapper.setProps({ open: true })
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('Scheduled in Singapore')
    await wrapper.findAll('label').find(label => label.text() === 'sample-group')!.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[type="number"]').setValue('1.25')
    await click(wrapper, t('admin.groupRateCampaigns.editor.startScheduled'))
    await click(wrapper, t('admin.groupRateCampaigns.editor.endScheduled'))
    const inputs = wrapper.findAll('input[type="datetime-local"]')
    await inputs[0].setValue('2099-04-01T08:00')
    await inputs[1].setValue('2099-04-01T09:00')
    await click(wrapper, t('admin.groupRateCampaigns.actions.confirmCreate'))
    await flushPromises()
    expect(harness.create).toHaveBeenCalledWith(expect.objectContaining({
      selection: expect.objectContaining({ groups: [{ groupName: 'sample-group', campaignMultiplier: 1.25 }] }),
      schedule: { startMode: 'scheduled', startAt: '2099-04-01T00:00:00.000Z', endMode: 'scheduled', endAt: '2099-04-01T01:00:00.000Z' },
    }))
  })
})
