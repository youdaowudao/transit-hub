import type { HubItem, HubReport } from '../../../chanshui/web/lib/hub-types'

export const restorationSettings = { timeoutSeconds: 120, totalTimeoutSeconds: 600, concurrency: 2, maxOutputTokens: 4096, retries: 0, maxAttempts: 128, memoryLimitMiB: 256, revision: 1 }
export function restorationReport(overrides: Partial<HubReport> = {}): HubReport {
  return {
    reportId: 'a'.repeat(32), targetId: 'account-a', accountName: '界面回归账号', groupId: 'group-a', groupName: '本站分组',
    model: 'gpt-6.1-sol', status: 'done', processExited: true, createdAtMs: Date.now() - 330000, error: {},
    summary: { iqScore: 86, iqMax: 100, total: { score: 66, max: 100, complete: true, parts: [
      { key: 'fingerprint', label: '模型指纹归因', applicable: true, known: true, points: 0, max_points: 10, contribution: 0, coefficient: 21 },
      { key: 'iq', label: '推理表现', applicable: true, known: true, points: 86, max_points: 100, contribution: 23, coefficient: 27 },
      { key: 'vision', label: '识图能力', applicable: true, known: true, points: 100, max_points: 100, contribution: 17, coefficient: 17 },
      { key: 'injection', label: '注入大小', applicable: true, known: true, points: 4, max_points: 10, contribution: 6, coefficient: 15 },
      { key: 'tools', label: '工具调用', applicable: true, known: true, points: 1, max_points: 1, contribution: 11, coefficient: 11 },
      { key: 'web', label: '联网检测', applicable: true, known: true, points: 1, max_points: 1, contribution: 9, coefficient: 9 },
    ] } }, ...overrides,
  }
}
export function restorationItems(count = 12): HubItem[] {
  return Array.from({ length: count }, (_, i) => ({
    itemId: `custom:${i + 1}`, sectionId: 'iq', status: 'done', imageAvailable: false,
    definition: { id: `custom:${i + 1}`, title: `紧凑题目${i + 1}`, kind: 'iq', maxScore: 6, judge: { type: 'number' }, enabled: true, order: i, template: '测试题', parameters: {} },
    prompt: `实际题面${i + 1}`, expected: '42', result: { answer: `已保存回答${i + 1}`, judgment: { status: 'known', earned: i === 0 ? 0 : 6, max: 6 } },
  }))
}
