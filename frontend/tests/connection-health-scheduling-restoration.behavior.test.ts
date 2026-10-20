// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ConnectionHealthEventsDialog from '@/modules/admin/components/dashboard/ConnectionHealthEventsDialog.vue'
import type { ConnectionHealthEvent } from '@/modules/admin/types/connectionHealth'

describe('main-site scheduling closed action evidence', () => {
 it('explains that automatic shutdown was skipped while preserving restoration', async () => {
  const event: ConnectionHealthEvent = { id: 'closed-fixture', connectionId: 'sub2api:ws1:a', modelName: 'gpt-test', ownGroupName: '隔离组', upstreamSiteId: '', upstreamGroupName: '隔离组', result: 'server_error', fromState: 'suspended', toState: 'suspended', latencyMs: null, errorKey: 'server_error', remoteAction: 'skipped_upstream_scheduling_disabled', source: 'scheduled', createdAt: '2031-02-03T04:05:06Z' }
  const wrapper = mount(ConnectionHealthEventsDialog, { props: { open: true, events: [event], groups: [], adminGroups: [], selectedConnectionId: event.connectionId, siteName: () => '' }, global: { stubs: { Teleport: true } } })
  try { await flushPromises(); expect(wrapper.text()).toContain('主站调度关闭，未执行自动关停'); expect(wrapper.text()).not.toContain('主站调度关闭，未执行自动远端动作') }
  finally { wrapper.unmount() }
 })
})
