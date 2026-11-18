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

// Same observations the original report sections consume, including inconclusive
// measurements: an unknown score must not erase the actual sample records.
export function detailedRestorationItems(): HubItem[] {
  const make = (id: string, kind: HubItem['definition']['kind'], evidence: unknown, status = 'known'): HubItem => ({
    itemId: id, sectionId: kind, status: 'done', prompt: '已保存题目', expected: '42',
    definition: { id, kind, title: id, maxScore: 10, enabled: true, order: 0, template: '原题', parameters: {}, judge: { type: 'legacy' } },
    result: { judgment: { status }, evidence },
  })
  const samples = Array.from({ length: 6 }, (_, i) => ({ index: i + 1, read: 4096 + i, write: 1800 + i, input: 160 + i, output: 8, total: 6060 + i, hit_rate: 96.57, attempts: 1, read_reported: true, write_reported: true, input_reported: true, output_reported: true }))
  return [
    make('数字指纹', 'fingerprint_numeric', { compatible: true, attribution: { family: 'gpt', familyName: 'GPT', familyProb: 1, topModel: 'gpt-6.1-sol', topProb: 0.97, lowConfidence: false, results: [{ modelId: 'gpt-6.1-sol', displayName: 'GPT 原版名称', prob: 0.97, jsSimilarity: 0.8 }] }, usedOutputs: 3, sampleMin: 3, sampleLimit: 10, samples: Array.from({ length: 3 }, () => ({ expectedCount: 300, parsedCount: 300, answer: '1,2,3', ms: 12 })) }),
    make('注入测量', 'injection', { reliable: false, score: null, max_score: 10, inj: 12, slope: 0.5, note: '采样不足，保留已取得的观测', points: [{ n: 100, input: 50, total: 52 }], cache_header: 'cached observation', probe: { sse_ok: true, sse_frames: 4 } }, 'unknown'),
    make('缓存验证', 'cache', { status: 'unknown', pass: null, reason: '缺少可靠衔接，仍保留六轮用量', samples, transitions: [{ from: 5, to: 6, aligned: false }], third_read_ratio: null, third_read_baseline: 6230 }, 'unknown'),
    make('工具检测', 'tools', { status: 'unsupported', pass: null, protocol: 'chat', reason: '当前接口没有提供结构化证据', evidence: { observed: 'tool-observation-kept' } }, 'not_applicable'),
    make('事件1', 'knowledge', { kind: 'know', reason: '与参考答案一致' }, 'reference'),
    make('事件2', 'knowledge', { kind: 'unknown', reason: '明确回答不知道' }, 'reference'),
    make('事件3', 'knowledge', {}, 'not_asked'),
  ].map(item => {
    if (item.definition.kind === 'knowledge') { item.definition.parameters = { date: item.itemId === '事件1' ? '2026-01-01' : '2026-02-22', event: item.itemId + '实际事件' }; item.result!.answer = item.itemId + '回答' }
    return item
  })
}
