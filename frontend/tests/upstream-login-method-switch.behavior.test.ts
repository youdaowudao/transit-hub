// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import type { UpstreamAuthMode, ResolvedUpstreamPlatform } from '@/modules/admin/types/upstream'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

const refreshWarning = '填写的 Refresh Token 可能已被使用，重试前请到上游重新获取。'
const editUnchanged = '本次修改没有保存，原站点资料和费用保持不变，不会修改主站；处理上面的原因后可以重试。'
const identityHelp = '原站点更换域名时可以在这里修改，核实原账号后会保留原有费用和对接记录。也可以更新凭据或切换登录方式；核对不通过不会保存，不会修改主站。'
const saveProblem = '保存站点资料时出现问题，请按下面的“实际结果”处理。'
const forbiddenHelp = '上游拒绝访问（403）。具体原因请看下面的上游提示；没有提示时，请在上游网站确认账号状态和访问限制。'
const metric = { value: 1, display: '1.00' }
let wrapper: VueWrapper | undefined
let platform: ResolvedUpstreamPlatform
let authMode: UpstreamAuthMode
let payload: Record<string, unknown>
let status: number
let savedForm: Record<string, unknown> | undefined
let networkFailure: boolean
const site = () => ({ id: 'fixture-site', name: '验收-切换方式', baseUrl: 'http://fixture.invalid', platform, requestedPlatform: platform, authMode, account: authMode === 'user_key' ? '42' : 'owner@example.com', rechargeRate: 1, enabled: true, remark: '原备注', status: 'connected', errorKey: null, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })

beforeEach(() => {
  platform = 'sub2api'
  authMode = 'password'
  status = 422
  savedForm = undefined
  networkFailure = false
  payload = { message: 'admin.upstream.errors.request', failure: { errorKey: 'admin.upstream.errors.request', stage: 'verify', httpStatus: 503, upstreamMessage: '读取接口暂时不可用' } }
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
    if (options.method === 'POST' || options.method === 'PUT') {
      savedForm = JSON.parse(String(options.body)) as Record<string, unknown>
      if (networkFailure) throw new TypeError('fixture network unavailable')
      return new Response(JSON.stringify(payload), { status })
    }
    return new Response(JSON.stringify([site()]), { status: 200 })
  }))
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.unstubAllGlobals()
  localStorage.clear()
  document.body.innerHTML = ''
})

const mountView = async () => {
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  return wrapper
}
const open = async (edit: boolean) => {
  const view = await mountView()
  if (edit) {
    await view.get('div.group.bg-card').findAll('button')[2]!.trigger('click')
  } else {
    const add = view.findAll('button').find(button => button.text().includes('新增站点'))
    if (!add) throw new Error('add entry missing')
    await add.trigger('click')
    await view.get('#upstream-site-name').setValue('验收-新站点')
    await view.get('#upstream-site-url').setValue('http://fixture.invalid')
  }
  await flushPromises()
  return view
}
const selectToken = async (view: VueWrapper, refresh = true) => {
  await view.get('input[type="radio"][value="token"]').setValue(true)
  await view.get(refresh ? '#upstream-site-refresh-token' : '#upstream-site-access-token').setValue('fixture-new')
}
const selectKey = async (view: VueWrapper) => {
  await view.get('#upstream-site-platform').setValue('newapi')
  await view.get('input[type="radio"][value="user_key"]').setValue(true)
  await view.get('#upstream-site-user-key').setValue('fixture-new')
}
const submit = async (view: VueWrapper) => {
  await view.get('[role="dialog"] form').trigger('submit')
  await flushPromises()
  return view.get('[role="dialog"] [role="alert"]')
}
const failure = (stage: string, upstreamMessage = '上游具体原因', key = 'request') => {
  payload = { message: `admin.upstream.errors.${key}`, failure: { errorKey: `admin.upstream.errors.${key}`, stage, httpStatus: stage === 'check' || stage === 'save' ? 0 : 403, upstreamMessage } }
}
const selectedMode = (view: VueWrapper) => (view.get('input[type="radio"]:checked').element as HTMLInputElement).value

describe('same-site login method switching and Key-first entry', () => {
  it('defaults to Refresh Token, puts it first, and places Refresh Token before optional Access Token', async () => {
    const view = await open(false)
    expect(selectedMode(view)).toBe('token')
    const labels = view.get('[role="radiogroup"]').findAll('label')
    expect(labels[0]!.text()).toContain('Refresh Token 登录（推荐）')
    expect(labels[1]!.text()).toContain('账号密码登录')
    expect(labels[1]!.text()).toContain('使用站点账号密码登录，并自动保存会话。')
    const dialog = view.get('[role="dialog"]')
    expect(dialog.text()).toContain('推荐。粘贴 Sub2API 的 Refresh Token，系统会自动续期，不需要保存密码。平台选“自动识别”时按 Sub2API 处理，New-API 站点请先选 New-API。')
    const tokenInputs = dialog.findAll('#upstream-site-refresh-token, #upstream-site-access-token, #upstream-site-token-type')
    expect(tokenInputs.map(input => input.attributes('id'))).toEqual(['upstream-site-refresh-token', 'upstream-site-access-token', 'upstream-site-token-type'])
    expect(dialog.text()).toContain('Access Token（一般不用填）')
    expect(view.get('#upstream-site-refresh-token').attributes('placeholder')).toBe('粘贴 refresh_token，系统会先刷新一次获取最新过期时间')
    expect(view.get('#upstream-site-access-token').attributes('placeholder')).toBe('一般不用填；没有 Refresh Token 时才粘贴 access_token')
    expect(view.get('#upstream-site-refresh-token').element.parentElement!.classList.contains('sm:col-span-2')).toBe(true)
    expect(view.get('#upstream-site-access-token').element.parentElement!.classList.contains('sm:col-span-2')).toBe(false)
  })

  it('maps Key methods between platforms and preserves a deliberate password selection', async () => {
    const view = await open(false)
    for (const target of ['newapi', 'sub2api', 'newapi', 'auto']) {
      await view.get('#upstream-site-platform').setValue(target)
      expect(selectedMode(view)).toBe(target === 'newapi' ? 'user_key' : 'token')
      if (target === 'newapi') {
        const labels = view.get('[role="radiogroup"]').findAll('label')
        expect(labels[0]!.text()).toContain('用户 Key 登录（推荐）')
        expect(labels[0]!.text()).toContain('推荐。使用 New-API 个人设置中生成的系统访问令牌；新版 New-API 只需 Key，旧版还要填写用户 ID。')
        expect(view.get('[role="dialog"]').text()).toContain('系统用这个令牌读取余额、分组和使用统计；填写了用户 ID 时会一并发送给上游。')
      }
    }
    await view.get('input[type="radio"][value="password"]').setValue(true)
    for (const target of ['newapi', 'sub2api', 'auto']) {
      await view.get('#upstream-site-platform').setValue(target)
      expect(selectedMode(view)).toBe('password')
    }
  })

  it('allows a blank optional user ID and sends it as blank', async () => {
    const view = await open(false)
    await selectKey(view)
    const input = view.get('#upstream-site-user-id')
    expect((input.element as HTMLInputElement).required).toBe(false)
    const label = view.get('label[for="upstream-site-user-id"]')
    expect(label.text()).toBe('用户 ID（可不填）')
    expect(label.find('.text-red-500').exists()).toBe(false)
    expect(input.attributes('placeholder')).toBe('新版 New-API 可留空，系统按 Key 自动读取')
    expect(view.get('[role="dialog"]').text()).toContain('旧版 New-API 必须填写，在上游“个人设置”里查看。修改已有站点时留空，会沿用原来的用户 ID。')
    expect((view.get('[role="dialog"] form').element as HTMLFormElement).checkValidity()).toBe(true)
    status = 201
    payload = { ...site(), id: 'fixture-new', authMode: 'user_key', platform: 'newapi', account: '42' }
    await view.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    expect(savedForm).toMatchObject({ userId: '', authMode: 'user_key' })
    expect(view.find('[role="dialog"]').exists()).toBe(false)
  })

  it('explains same-account editing and reopens the saved new login method', async () => {
    const view = await open(true)
    expect(view.get('[role="dialog"]').text()).toContain(identityHelp)
    expect(view.get('[role="dialog"]').text()).not.toContain('更换站点地址、登录方式或账号请新增站点')
    await selectToken(view)
    status = 200
    payload = { ...site(), authMode: 'token' }
    await view.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    expect(view.find('[role="dialog"]').exists()).toBe(false)
    expect(savedForm?.authMode).toBe('token')
    expect(view.findAll('div.group.bg-card')).toHaveLength(1)
    await view.get('div.group.bg-card').findAll('button')[2]!.trigger('click')
    expect(selectedMode(view)).toBe('token')
    expect(view.find('#upstream-site-account').exists()).toBe(false)
    expect(view.find('#upstream-site-password').exists()).toBe(false)
  })

  it('shows local R1 with check wording and no HTTP or upstream-failure wording', async () => {
    const reason = '更换站点地址前，请勾选确认这是原站点的新地址。本次没有请求上游，资料和费用未保存，不会修改主站。'
    failure('check', reason, 'identityCheckFailed')
    const view = await open(true)
    await selectToken(view)
    const alert = await submit(view)
    expect(alert.text()).toContain('没有保存，原有资料保持不变。')
    expect(alert.text()).toContain('失败步骤：核对是否同一账号')
    expect(alert.text()).toContain(`原因：${reason}`)
    for (const absent of ['HTTP 状态', '上游接口请求失败', '上游提示：', refreshWarning, editUnchanged]) expect(alert.text()).not.toContain(absent)
  })

  it('keeps exactly one backend Refresh Token reminder on R5', async () => {
    failure('check', `新登录信息属于另一个账号：上游用户编号与原账号不同。请用原账号的凭据重试，或新增站点。${refreshWarning}原资料未保存，不会修改主站。`, 'identityCheckFailed')
    const view = await open(true)
    await selectToken(view)
    const alert = await submit(view)
    expect(alert.text().split(refreshWarning)).toHaveLength(2)
    expect(alert.text()).not.toContain('没能确认')
    expect(alert.text()).not.toContain('HTTP 状态')
  })

  it.each(['login', 'refresh', 'verify', 'metrics'])('adds the unchanged-site reminder to an edit upstream %s failure', async stage => {
    failure(stage, '原样保留的上游原因', 'loginRejected')
    const view = await open(true)
    await view.get('#upstream-site-password').setValue('fixture-new')
    const alert = await submit(view)
    expect(alert.text()).toContain(editUnchanged)
    expect(alert.text()).toContain('上游提示：原样保留的上游原因')
    expect(alert.text()).not.toContain(refreshWarning)
  })

  it('does not claim the original data is unchanged after an unacknowledged network failure', async () => {
    networkFailure = true
    const view = await open(true)
    await selectToken(view)
    const alert = await submit(view)
    expect(alert.text()).not.toContain(editUnchanged)
    expect(alert.text()).not.toContain(refreshWarning)
    expect(alert.text()).not.toContain('失败步骤')
  })

  it.each([
    [false, 'verify'], [false, 'metrics'], [false, 'refresh'], [false, 'save'],
    [true, 'verify'], [true, 'metrics'], [true, 'refresh'], [true, 'save'],
  ] as const)('adds the Refresh Token reminder for edit=%s stage=%s', async (edit, stage) => {
    failure(stage)
    const view = await open(edit)
    await selectToken(view)
    const alert = await submit(view)
    expect(alert.text().split(refreshWarning)).toHaveLength(2)
  })

  it.each(['verify', 'save'])('does not add Refresh Token wording for Access Token only at %s', async stage => {
    failure(stage)
    const view = await open(true)
    await selectToken(view, false)
    expect((view.get('#upstream-site-refresh-token').element as HTMLInputElement).value).toBe('')
    const alert = await submit(view)
    expect(alert.text()).not.toContain(refreshWarning)
  })

  it('does not add Refresh Token wording for a NewAPI Key save failure', async () => {
    platform = 'newapi'
    authMode = 'user_key'
    failure('save')
    const view = await open(true)
    await selectKey(view)
    const alert = await submit(view)
    expect(alert.text()).not.toContain(refreshWarning)
    expect(alert.text()).not.toContain(editUnchanged)
  })

  it.each([
    [false, '本站资料未保存，原数据库资料保留'],
    [false, '数据库保存结果尚未确认，请维护人员核查实际提交结果'],
    [false, '本站资料已保存，当前运行数据未更新'],
    [true, '本站资料未保存，原数据库资料保留'],
    [true, '数据库保存结果尚未确认，请维护人员核查实际提交结果'],
    [true, '本站资料已保存，当前运行数据未更新'],
  ] as const)('preserves the complete actual save result for edit=%s: %s', async (edit, result) => {
    failure('save', result)
    const view = await open(edit)
    await selectToken(view)
    const alert = await submit(view)
    expect(alert.findAll('p')[0]!.text()).toBe(saveProblem)
    expect(alert.text()).toContain(`实际结果：${result}`)
    for (const absent of ['上游接口请求失败', 'HTTP 状态', editUnchanged]) expect(alert.text()).not.toContain(absent)
  })

  it('shows Key auto-detection guidance and preserves the upstream reason', async () => {
    failure('verify', '无权进行此操作，未提供 New-Api-User', 'userKeyDetectFailed')
    const view = await open(false)
    await selectKey(view)
    const alert = await submit(view)
    expect(alert.text()).toContain('只用 Key 没能读到所属用户。新版 New-API 只需 Key，旧版 New-API 还需要填写用户 ID。请看下面的上游提示：提到 New-Api-User 或用户 ID 时，填写用户 ID 后重试；提到令牌无效时，请检查 Key 是否正确。')
    expect(alert.text()).toContain('上游提示：无权进行此操作，未提供 New-Api-User')
  })

  it('shows the neutral 403 wording alongside the actual upstream reason', async () => {
    failure('verify', '缺少读取个人资料权限', 'forbidden')
    const view = await open(false)
    await selectKey(view)
    const alert = await submit(view)
    expect(alert.text()).toContain(forbiddenHelp)
    expect(alert.text()).toContain('上游提示：缺少读取个人资料权限')
    expect(alert.text()).not.toContain('防护拦截')
    expect(alert.text()).not.toContain('没有说明具体原因')
  })

  it('shows the known Key mismatch as a reason without contradictory uncertainty', async () => {
    const reason = 'Key 实际属于用户编号 42，与填写的用户 ID 43 不同。请改正用户 ID，或留空让系统自动读取；没有保存，不会修改主站。'
    failure('check', reason, 'identityCheckFailed')
    const view = await open(false)
    await selectKey(view)
    await view.get('#upstream-site-user-id').setValue('43')
    const alert = await submit(view)
    expect(alert.text()).toContain('没有保存，原有资料保持不变。')
    expect(alert.text()).toContain(`原因：${reason}`)
    expect(alert.text()).not.toContain('没能确认')
    expect(alert.text()).not.toContain('HTTP 状态')
  })

  it('labels a Key credential field error as system access token', async () => {
    status = 400
    payload = { message: 'admin.upstream.errors.invalidFields', failure: { fields: ['accessToken'] } }
    const view = await open(false)
    await selectKey(view)
    const alert = await submit(view)
    expect(alert.text()).toContain('请检查字段：系统访问令牌')
    expect(alert.text()).not.toContain('Access Token')
  })
})
