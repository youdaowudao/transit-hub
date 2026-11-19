// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import { chanshuiCatalog, chanshuiCatalogSession, clearChanshuiCatalogForSession, publishChanshuiCatalog } from '../src/modules/admin/composables/chanshuiCatalog'
import type { AdminGroupHealth } from '../src/modules/admin/types/connectionHealth'
import { approveFixtureStart } from './fixtures/chanshui-transfer'

vi.mock('../../chanshui/web/components/Select', () => ({
  default: (props: any) => (globalThis as any).React.createElement('select', props, props.children),
}))

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
const groups = [
  { id: 'z', name: 'Z', accounts: [{ targetId: 'first', accountId: '13', accountName: '较优先账号', priority: 0, models: 'local-model', protocol: 'chat' }] },
  { id: 'a', name: 'A', accounts: [] },
]
const prefKey = 'transithub.chanshui.sections.v1:workspace-v7-fixture'
let root: ReturnType<typeof createRoot>
let node: HTMLDivElement
let removeServer: () => void

const act = async (work?: () => unknown) => React.act(async () => {
  await work?.()
  await new Promise(resolve => setTimeout(resolve, 0))
})
const settle = async () => { for (let i = 0; i < 5; i++) await act() }

beforeEach(() => {
  vi.stubGlobal('React', React)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  node = document.createElement('div')
  document.body.append(node)
  root = createRoot(node)
  localStorage.removeItem(prefKey)
})

afterEach(async () => {
  await act(() => root.unmount())
  removeServer?.()
  node.remove()
  localStorage.removeItem(prefKey)
  vi.unstubAllGlobals()
})

async function boot() {
  const workspaceId = 'workspace-v7-fixture'
  const calls: string[] = []
  const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
  const server = (event: MessageEvent) => {
    const request = event.data
    if (request?.kind === 'chanshui.READY') {
      respond({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId, visible: true, target: {}, catalog: { workspaceId, groups } })
    }
    if (request?.kind !== 'chanshui.request') return
    if (!approveFixtureStart(request)) return
    calls.push(request.op)
    let value: unknown = {}
    if (request.op === 'runtime') value = { enabled: true, available: true, active: null }
    if (request.op === 'reports') value = { items: [] }
    if (request.op === 'targets') value = { items: [{ targetId: 'remote', accountName: '远端账号', groups: [{ id: 'remote', name: '远端组' }] }] }
    const context = { workspaceId, targetId: request.params.targetId, groupId: request.params.groupId, baseURL: 'https://fixture.invalid', protocol: 'chat' }
    if (request.op === 'context') value = { ...context, canRun: true, expectedContext: context }
    if (request.op === 'models') value = { models: ['remote-model'] }
    if (request.op === 'start') value = { reportId: request.params.reportId, status: 'queued' }
    if (request.op === 'report') value = { reportId: request.params.reportId, status: 'done', processExited: true, model: 'local-model', createdAtMs: Date.now() }
    if (request.op === 'items') value = { items: [] }
    respond({ kind: 'chanshui.response', v: 1, generation: 1, id: request.id, ok: true, value })
  }
  window.addEventListener('message', server)
  removeServer = () => window.removeEventListener('message', server)
  await act(() => root.render(React.createElement(HubApp)))
  await settle()
  return calls
}

it('从工作区恢复全部取消，勾选后当场保存', async () => {
  localStorage.setItem(prefKey, '[]')
  await boot()
  const sections = [...node.querySelectorAll<HTMLInputElement>('.hub-checkbox input')]
  expect(sections.length).toBeGreaterThan(0)
  expect(sections.every(box => !box.checked)).toBe(true)
  await act(() => sections[0].click())
  expect(JSON.parse(localStorage.getItem(prefKey)!)).toEqual(['iq'])
})

it('按分组健康名单顺序选组和模型，按下开始后才核验账号', async () => {
  const calls = await boot()
  const field = (label: string) => [...node.querySelectorAll('label')].find(l => l.firstChild?.textContent?.trim() === label)?.querySelector('select') as HTMLSelectElement
  const choose = async (label: string, value: string) => {
    const input = field(label)
    await act(() => { input.value = value; input.dispatchEvent(new Event('change', { bubbles: true })) })
    await settle()
  }
  const targetRead = () => calls.filter(op => ['targets', 'context', 'models', 'start'].includes(op))
  expect([...field('分组').options].map(o => o.value)).toEqual(['', 'z', 'a'])
  await choose('分组', 'z')
  await choose('账号', 'first')
  expect(node.querySelector<HTMLInputElement>('#model')?.value).toEqual('local-model')
  expect(targetRead()).toEqual([])
  const start = [...node.querySelectorAll('button')].find(button => button.textContent?.trim() === '开始检测')!
  expect(start?.disabled).toBe(false)
  await act(() => start.click())
  await settle()
  expect(targetRead()).toEqual(['context', 'start'])
})

it('分组健康名单保留左栏顺序，按数字 Priority 排列并隔离旧登录', () => {
  const member = (id: string, priority?: number) => ({ targetId: id, id, name: id, priority })
  const source = [
    { id: 'z', name: '先展示的组', accounts: [member('missing'), member('10', 10), member('b', 2), member('a', 2), member('zero', 0), member('b', 2)] },
    { id: 'a', name: '账号读取失败的组', accountsError: 'fixture.error', accounts: [member('not-current', 0)] },
  ] as AdminGroupHealth[]
  const originalSession = chanshuiCatalogSession()
  try {
    publishChanshuiCatalog('workspace-catalog', source, originalSession)
    expect(chanshuiCatalog.value?.groups.map(group => group.id)).toEqual(['z', 'a'])
    expect(chanshuiCatalog.value?.groups[0].accounts.map(account => account.targetId)).toEqual(['zero', 'a', 'b', '10', 'missing'])
    expect(chanshuiCatalog.value?.groups[1]).toMatchObject({ accountsError: true, accounts: [] })
    clearChanshuiCatalogForSession()
    publishChanshuiCatalog('workspace-catalog', source, originalSession)
    expect(chanshuiCatalog.value).toBeNull()
    publishChanshuiCatalog('next-workspace', source.slice(0, 1), chanshuiCatalogSession())
    expect(chanshuiCatalog.value?.workspaceId).toBe('next-workspace')
  } finally {
    clearChanshuiCatalogForSession()
  }
})
