import { describe, expect, it } from 'vitest'
import { reportPollDecision, selectHubDefaultModel, sidebarDayLabel } from '../../chanshui/web/lib/hub-policy'
import { chanshuiRequestSpec } from '../src/modules/admin/api/chanshui'

describe('模型检测默认选择和两天历史', () => {
  it.each([
    ['GPT 分组', ['first', 'gpt-6.1-sol'], 'gpt-6.1-sol'],
    ['gPt', ['first', 'GPT_6_1_SOL'], 'GPT_6_1_SOL'],
    ['Claude 分组', ['first', 'claude-opus-5-5'], 'claude-opus-5-5'],
    ['克劳德', ['first', 'Opus 5.5'], 'Opus 5.5'],
    ['其他组', ['first', 'second'], 'first'],
  ])('按%s选择实际列表中的默认模型', (group, list, expected) => {
    expect(selectHubDefaultModel(list, group).model).toBe(expected)
  })
  it('指定模型不在列表时选择首项并解释，空列表不能捏造模型', () => {
    expect(selectHubDefaultModel(['available'], 'GPT')).toEqual({ model: 'available', notice: expect.stringContaining('未在此账号列表中') })
    expect(selectHubDefaultModel([], 'Claude').model).toBe('')
  })
  it('新加坡午夜正确区分今天、昨天和不在侧栏的旧记录', () => {
    const now = Date.parse('2026-11-16T16:00:01Z')
    expect(sidebarDayLabel(Date.parse('2026-11-16T16:00:00Z'), now)).toBe('今天')
    expect(sidebarDayLabel(Date.parse('2026-11-16T15:59:59Z'), now)).toBe('昨天')
    expect(sidebarDayLabel(Date.parse('2026-11-15T15:59:59Z'), now)).toBeNull()
  })
})

describe('模型检测退出核查不提前完成', () => {
  it('报告终态但进程未退出继续读，15秒后停止自动读并指引核查', () => {
    const result = reportPollDecision({ status: 'done', processExited: false }, null, 100)
    expect(result.poll).toBe(true)
    expect(result.message).not.toContain('程序已结束')
    const timeout = reportPollDecision({ status: 'done', processExited: false }, result.terminalSince, 15100)
    expect(timeout.poll).toBe(false)
    expect(timeout.message).toContain('核查运行状态')
  })
  it('已退出但终态未保存停轮询，不能冒充仍在执行', () => {
    const result = reportPollDecision({ status: 'running', processExited: true }, null, 0)
    expect(result.poll).toBe(false)
    expect(result.message).toContain('重试保存状态')
    expect(result.message).not.toContain('正在检测')
  })
  it('只有终态和退出同时确认才显示服务器已释放', () => {
    expect(reportPollDecision({ status: 'partial', processExited: true }, null, 0)).toMatchObject({ poll: false, message: expect.stringContaining('服务器不再保留') })
  })
})

describe('检测父页只允许具体操作', () => {
  it('判读和提示词全文使用各自的只读路径', () => {
    expect(chanshuiRequestSpec('cross-check', { reportId: 'report/42' })).toEqual({ path: '/reports/report%2F42/cross-check', method: 'GET' })
    expect(chanshuiRequestSpec('prefix-prompt', { promptId: 'codex-gpt-6.2-sol' })).toEqual({ path: '/prefix-prompts/codex-gpt-6.2-sol', method: 'GET' })
  })

  it('补测固定原报告路径且停止携带轮次，不能附带新账号和模型', () => {
    expect(chanshuiRequestSpec('retry', { reportId: 'a', retryId: 'b', expectedRetryId: 'c', itemIds: ['Q2'], targetId: 'ignored', model: 'ignored' })).toEqual({ path: '/reports/a/retry', method: 'POST', body: { retryId: 'b', expectedRetryId: 'c', itemIds: ['Q2'] } })
    expect(chanshuiRequestSpec('cancel', { reportId: 'a', expectedRetryId: 'b' }).body).toEqual({ expectedRetryId: 'b' })
  })

  it('预设删除使用版本条件且目标编号被编码，不能传任意URL', () => {
    expect(chanshuiRequestSpec('delete-question', { id: 'Q1', revision: 3 })).toEqual({ path: '/questions/Q1', method: 'DELETE', revision: 3 })
    expect(chanshuiRequestSpec('question', { id: '../settings' }).path).toBe('/questions/..%2Fsettings')
    expect(() => chanshuiRequestSpec('fetch', { url: 'https://elsewhere.invalid' })).toThrow('未开放的操作')
  })
})
