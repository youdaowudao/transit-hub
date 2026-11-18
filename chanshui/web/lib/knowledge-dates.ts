import type { KnowledgeVerdict } from './api';
import { modelFamily } from '@/lib/report-view';

// Preserve the date precision used by each publisher.
export const gptDates = [
  ['GPT-6.2 Astra', '2026-05-18'],
  ['GPT-6.2 Sol', '2026-06-01'],
  ['GPT-6.1 Sol', '2026-04-30'],
  ['GPT-6.1 Terra', '2026-06-01'],
  ['GPT-6 Astra', '2026-04-30'],
  ['GPT-6 Sol', '2026-04-20'],
  ['GPT-6 Luna', '2026-05-18'],
  ['GPT-5.6 Sol', '2026-02-16'],
  ['GPT-5.6 Terra', '2026-02-16'],
  ['GPT-5.6 Luna', '2026-02-16'],
  ['Chat Latest（chat-latest）', '2026-04-20'],
] as const;

export const claudeDates = [
  ['Claude Opus 5.5', '2026-06'],
  ['Claude Opus 5', '2026-05'],
  ['Claude Opus 4.8', '2026-01'],
  ['Claude Opus 4.7', '2026-01'],
  ['Claude Sonnet 5.5', '2026-06'],
  ['Claude Sonnet 5', '2026-01'],
  ['Claude Sonnet 4.6', '2025-08'],
  ['Claude Sonnet 4.5', '2025-01'],
  ['Claude Sonnet 4', '2025-01'],
  ['Claude Haiku 5.5', '2026-06'],
  ['Claude Haiku 4.5', '2025-02'],
] as const;

export const claudeFableMythosDates = [
  ['Claude Fable 5.2', '2026-06'],
  ['Claude Fable 5.1', '2026-06'],
  ['Claude Fable 5', '2026-01'],
  ['Claude Mythos 5.2', '2026-06'],
  ['Claude Mythos 5.1', '2026-06'],
  ['Claude Mythos 5', '2026-01'],
] as const;

export function knowledgeDateComparison(model: string, knowledge: KnowledgeVerdict): string | undefined {
  if (knowledge.status === 'not_selected' || knowledge.status === 'not_applicable') return;

  const normalized = model.trim().toLowerCase().replace(/^[^/]+\//, '').replace(/[^a-z0-9]/g, '');
  const reviewedModels = ['gpt6sol','gpt6astra','gpt61sol','gpt6luna','gpt61terra','gpt62astra','gpt62sol'];
  if (reviewedModels.includes(normalized)) {
    if (!knowledge.boundary_review) return;
    const match = gptDates.find(([name]) => name.toLowerCase().replace(/[^a-z0-9]/g, '') === normalized);
    if (!match) return;
    const [name, date] = match;
    const reference = name + ' 的公开知识截止日期为 ' + date + '。';
    const rows = knowledge.rows || [];
    const unknownDates = rows.filter(row => row.kind === 'unknown' && !row.error).map(row => row.happened_at).sort();
    const known = rows.filter(row => row.kind === 'know' && !row.error);
    const conflict = unknownDates.length > 0 && known.some(row => row.happened_at >= unknownDates[0]);
    const allowed = ['no_anchor','outside_window','two_unknown','no_question','limit'].includes(knowledge.boundary_review.reason);
    const later = new Set(known.filter(row => row.happened_at > date).map(row => row.happened_at));
    const ids = new Set(known.filter(row => row.happened_at > date).map(row => row.id));
    const safe = knowledge.status === 'estimated' && !knowledge.web_suspect &&
      !rows.some(row => row.error || row.kind === 'invalid' || row.kind === 'error') && !conflict && allowed;
    return reference + (safe && later.size >= 2 && ids.size >= 2
      ? '本轮答对了多道晚于所声明型号公开参考日期的事件。请核对上游型号和是否补充资料。' : '');
  }

  const name = model.trim().toLowerCase().replace(/^[^/]+\//, '');
  const family = name === 'chat-latest' ? 'gpt' : modelFamily(model);
  if (family !== 'gpt' && family !== 'claude') return;
  const provider = family === 'gpt' ? 'GPT' : 'Claude';

  if (knowledge.status === 'custom') return '本轮使用自定义事件题，暂无法按官方知识日期对照。';
  if (knowledge.status === 'invalid' || knowledge.web_suspect || knowledge.rows?.some(row => row.kind === 'invalid')) {
    return `本轮有无效回答或疑似联网，暂无法对照 ${provider} 型号。`;
  }

  const rows = knowledge.rows?.filter(row => !row.error && /^\d{4}-\d{2}-\d{2}$/.test(row.happened_at)) ?? [];
  const known = Array.from(new Set(rows.filter(row => row.kind === 'know').map(row => row.happened_at))).sort();
  const unknown = Array.from(new Set(rows.filter(row => row.kind === 'unknown').map(row => row.happened_at))).sort();
  const lastKnown = known[known.length - 1];
  const firstUnknown = unknown[0];
  if (!lastKnown || !firstUnknown) return `本轮事件题证据不足，暂无法判断接近哪款 ${provider} 模型。`;
  if (lastKnown >= firstUnknown) return `本轮事件回答的日期边界互相冲突，暂无法对照 ${provider} 型号。`;

  const dates = family === 'gpt' ? gptDates : [...claudeDates, ...claudeFableMythosDates];
  const matches = dates.filter(([, date]) => {
    if (date.length === 7) return date >= lastKnown.slice(0, 7) && `${date}-01` < firstUnknown;
    return date >= lastKnown && date < firstUnknown;
  });
  if (matches.length === 0) return `参考名单中没有 ${provider} 型号的官方知识截止日期落在本轮区间。`;
  if (matches.length === 1) {
    const [candidate] = matches[0];
    if (known.length < 2 || unknown.length < 2) return `只有 ${candidate} 的官方知识截止日期落在本轮区间，仍需更多题确认。`;
    return `本轮知识表现最接近 ${candidate} 的公开知识日期。`;
  }
  const names = matches.slice(0, 3).map(([candidate]) => candidate).join('、');
  const candidates = matches.length > 3 ? `${names} 等 ${matches.length} 款模型` : names;
  return `本轮知识表现接近 ${candidates}，暂无法区分。`;
}

export function boundaryReviewText(reason: string): string {
  const reasons: Record<string,string> = {
    two_unknown: '这两道事件均未确认。可展开查看题目与回答。',
    no_question: '这个窗口只有一道可用题，日期边界未确认。',
    limit: '本轮已问满 10 道，日期边界未确认。',
    conflict: '较晚的事件答对了，单次不知道不足以划出日期范围。',
    interrupted: '检测已停止，已保存回答可查看。',
    invalid: '本轮回答无效或疑似联网，请查看逐题原因。',
    answer_missing: '回答为空、被截断或未明确表示不知道，请查看该题响应。',
    main_error: '知识题请求未完成，请查看逐题错误并补测失败项。',
    check_known: '第二道事件答对了，与第一次回答不同。',
    check_wrong: '第二道题的答案与参考答案不符，请查看逐题判定。',
    check_error: '第二道请求失败，可补测本题。',
    observations_changed: '已补测，原日期边界不再适用，请查看最新回答。',
    retry_pending: '正在补测知识题，日期边界暂未确认。已保存的回答仍可查看。',
  };
  return reasons[reason] || '';
}
