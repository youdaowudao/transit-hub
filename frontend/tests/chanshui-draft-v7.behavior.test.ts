// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import { catalogModels } from '../../chanshui/web/lib/hub-draft'
import type { HubCatalog } from '../../chanshui/web/lib/hub-types'
import { chanshuiCatalog, chanshuiCatalogSession, clearChanshuiCatalogForSession, publishChanshuiCatalog } from '../src/modules/admin/composables/chanshuiCatalog'
import type { AdminGroupHealth } from '../src/modules/admin/types/connectionHealth'
import { approveFixtureStart } from './fixtures/chanshui-transfer'

vi.mock('../../chanshui/web/components/Select', () => ({
  default: (props: any) => (globalThis as any).React.createElement('select', props, props.children),
}))

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
const groups: HubCatalog['groups'] = [
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

async function boot(catalog: HubCatalog = { workspaceId: 'workspace-v7-fixture', groups }) {
  const workspaceId = 'workspace-v7-fixture'
  const calls: string[] = []
  const respond = (data: unknown) => window.dispatchEvent(new MessageEvent('message', { data, source: window, origin: location.origin }))
  const server = (event: MessageEvent) => {
    const request = event.data
    if (request?.kind === 'chanshui.READY') {
      respond({ kind: 'chanshui.BOOT', v: 1, generation: 1, workspaceId, visible: true, target: {}, catalog })
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

it('原名单为空时用本组本账号的探活模型，仍允许对真正的空名单手输', async () => {
  const model = (modelName: string, modelControlClosed = false) => ({ modelName, modelControlClosed })
  const member = (targetId: string, models: string | undefined, modelHealth = [model('gpt-6.1-sol')], unprobedModels = [model('gpt-6.1-sol'), model(' gpt-5.6-sol '), model('已关闭', true), model(' ')]) => ({
    targetId, id: targetId, name: targetId, models, modelHealth, unprobedModels,
  })
  const source = [
    { id: '10', name: 'GPT混合分组', accounts: [
      member('439', undefined),
      member('existing', ' local-b , local-a '),
      member('unprobed', ' , ', [], [model('待探活')]),
      member('none', '', [model('已关闭', true)], []),
      member('empty', undefined, [], []),
    ] },
    { id: '20', name: '另一分组', accounts: [member('439', '', [], [model('route-a,route-b'), model('route-b')])] },
  ] as AdminGroupHealth[]
  const workspaceId = 'workspace-v7-fixture'
  try {
    publishChanshuiCatalog(workspaceId, source, chanshuiCatalogSession())
    const catalog = chanshuiCatalog.value!
    const accountFor = (groupId: string, targetId: string) => catalog.groups.find(group => group.id === groupId)?.accounts.find(account => account.targetId === targetId)
    const modelsFor = (groupId: string, targetId: string) => catalogModels(accountFor(groupId, targetId))
    expect(modelsFor('10', '439')).toEqual(['gpt-6.1-sol', 'gpt-5.6-sol'])
    expect(modelsFor('10', 'existing')).toEqual(['local-b', 'local-a'])
    expect(modelsFor('10', 'unprobed')).toEqual(['待探活'])
    expect(modelsFor('10', 'none')).toEqual([])
    expect(modelsFor('10', 'empty')).toEqual([])
    expect(modelsFor('20', '439')).toEqual(['route-a,route-b', 'route-b'])
    const modelsForOldFrame = (groupId: string, targetId: string) => (accountFor(groupId, targetId)?.models ?? '').split(',').map(model => model.trim()).filter(Boolean)
    expect(modelsForOldFrame('10', '439')).toEqual([])
    expect(modelsForOldFrame('10', 'none')).toEqual([])
    expect(modelsForOldFrame('10', 'existing')).toEqual(['local-b', 'local-a'])

    const calls = await boot(catalog)
    const choose = async (label: string, value: string) => {
      const select = [...node.querySelectorAll('label')].find(item => item.firstChild?.textContent?.trim() === label)!.querySelector('select')!
      await act(() => { select.value = value; select.dispatchEvent(new Event('change', { bubbles: true })) })
    }
    await choose('分组', '10')
    await choose('账号', '439')
    expect(node.querySelector<HTMLInputElement>('#model')?.value).toBe('gpt-6.1-sol')
    expect(node.textContent).not.toContain('当前名单里没有此账号的模型记录')
    await choose('账号', 'empty')
    const input = node.querySelector<HTMLInputElement>('#model')!
    expect(input.value).toBe('')
    expect(input.disabled).toBe(false)
    expect(node.textContent).toContain('当前名单里没有此账号的模型记录，可以手动输入模型名称。')
    await choose('分组', '20')
    await choose('账号', '439')
    expect(node.querySelector<HTMLInputElement>('#model')?.value).toBe('route-a,route-b')
    expect(calls.filter(op => ['targets', 'context', 'models', 'start'].includes(op))).toEqual([])
  } finally {
    clearChanshuiCatalogForSession()
  }
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
