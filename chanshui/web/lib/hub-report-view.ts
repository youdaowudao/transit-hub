import type { DetectVerdict, Probe, TotalPart } from './api';
import type { HubItem, HubReport } from './hub-types';
import { isHubTerminal, reportStatus } from './hub-policy';

export const object = (value: unknown): Record<string, any> => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, any> : {};
const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value);
export function itemJudgment(item: HubItem) {
  return typeof item.result?.judgment === 'string' ? { status: item.result.judgment } : item.result?.judgment ?? {};
}
export function itemLabel(item: HubItem, parentStatus: string): string {
  const j = itemJudgment(item);
  if (j.status === 'not_asked') return '本次未提问';
  if (j.status === 'not_applicable') return '不适用';
  if (isHubTerminal(parentStatus) && ['queued', 'running'].includes(item.status)) return '未完成';
  if (['queued', 'running'].includes(item.status)) return item.status === 'queued' ? '等待中' : '检测中';
  if (item.status === 'error' || item.result?.error) return '请求失败';
  if (j.status === 'unreviewed' || item.definition.judge.type === 'manual' && ['iq', 'vision'].includes(item.definition.kind)) return '待人工查看';
  if (j.status === 'unknown') return '尚未确认';
  return reportStatus(item.status);
}
export function itemPoints(item: HubItem): string {
  const j = itemJudgment(item), earned = item.result?.earned ?? j.earned, max = item.result?.max ?? j.max ?? item.definition.maxScore;
  return j.status === 'known' && finite(earned) && finite(max) ? `${earned} / ${max}${j.half || item.result?.half ? '（半分）' : ''}` : '';
}
export function reportError(value: unknown): string {
  if (typeof value === 'string') return value.trim();
  const e = object(value);
  if (!Object.keys(e).length) return '';
  return Array.from(new Set([e.message, e.detail].filter(v => typeof v === 'string' && v.trim()))).join('。') || '报告有异常记录，但未提供具体原因；请核查运行状态和逐项结果。';
}
export const reportVersion = (report: HubReport) => `${report.updatedAtMs ?? ''}:${report.status}:${JSON.stringify(report.itemCounts ?? {})}`;

/** Only adapt saved observations to the upstream presentation shape. */
export function hubReportView(report: HubReport, items: HubItem[], complete: boolean) {
  const summary = object(report.summary), total = object(summary.total);
  const parts: TotalPart[] = Array.isArray(total.parts) ? total.parts : [];
  const applicable = parts.filter(p => p.applicable);
  const scoreKnown = finite(total.score) && applicable.length > 0 && applicable.every(p => p.known) && total.complete !== false;
  const totalLabel = scoreKnown ? '' : Object.keys(total).length && !applicable.length ? '本次不计总分' : '尚未完成';
  const groups: Record<string, HubItem[]> = {};
  for (const item of items) {
    const kind = item.definition.kind.startsWith('fingerprint_') ? 'fingerprint' : item.definition.kind;
    (groups[kind] ??= []).push(item);
  }
  const messages: Record<string, string> = {};
  const evidence = (kind: string) => {
    const rows = groups[kind] ?? [];
    if (!rows.length) { messages[kind] = complete ? '本次未选择此项，或没有已保存的结果。' : '正在读取本次已保存的结果…'; return null; }
    const item = rows[0], e = object(item.result?.evidence), j = itemJudgment(item);
    if (item.result?.error || item.status === 'error') { messages[kind] = item.result?.error || '本项请求失败，请展开查看已保存结果。'; return null; }
    if (['queued', 'running'].includes(item.status) || ['unknown', 'not_applicable', 'not_asked', 'unreviewed'].includes(j.status ?? '')) {
      messages[kind] = `${itemLabel(item, report.status)}${j.reason ? '：' + j.reason : ''}`; return null;
    }
    return e;
  };
  const v: DetectVerdict = { injection: null, hidden: null, fingerprint: null, thinking: null, iq: null,
    total: { score: scoreKnown ? total.score : 0, max: total.max ?? 100, reason: total.reason ?? '', parts },
  };
  v.iq = { score: finite(summary.iqScore) ? summary.iqScore : null, max: 100, paper_max: finite(summary.iqMax) ? summary.iqMax : 0, complete: finite(summary.iqScore), per_question: [], note: total.reason ?? '' };
  if (complete && !groups.iq?.length) messages.iq = '本次没有推理题，不计算推理分。';
  else if (complete && groups.iq?.every(item => item.definition.judge.type === 'manual')) messages.iq = '本次推理题仅供人工查看，不计算推理分。';
  const numeric = (groups.fingerprint ?? []).find(i => i.definition.kind === 'fingerprint_numeric');
  if (numeric) {
    const e = object(numeric.result?.evidence), a = object(e.attribution), behavior = object(summary.behavior);
    if (numeric.result?.error || e.compatible === false || !finite(a.topProb) || !finite(a.familyProb) || !finite(e.usedOutputs ?? a.usedOutputs)) messages.fingerprint = numeric.result?.error || e.note || '数字指纹尚未确认，不能据此判断模型身份。';
    else v.fingerprint = {
      family: a.family ?? '', family_name: a.familyName ?? '', family_prob: a.familyProb ?? 0, top_model: a.topModel ?? '', top_prob: a.topProb,
      results: (Array.isArray(a.results) ? a.results : []).map((r: any) => ({ model: r.modelId, display: r.displayName, probability: r.prob, similarity: r.jsSimilarity ?? 0 })),
      low_confidence: a.lowConfidence !== false, used_outputs: e.usedOutputs ?? a.usedOutputs ?? 0, sample_limit: e.sampleLimit ?? 10,
      requests: Array.isArray(e.samples) ? e.samples.length : undefined, note: e.note ?? '', behavioral_top3: behavior.compatible === false ? undefined : behavior.top3,
      behavior_note: behavior.note, evidence: (Array.isArray(e.samples) ? e.samples : []).map((s: any, i: number) => ({ id: String(i + 1), expected_count: s.expectedCount, parsed_count: s.parsedCount, answer: s.answer ?? '', error: s.error ?? '', ms: s.ms ?? 0 })),
    };
  } else messages.fingerprint = complete ? '本次没有可用的数字指纹结果。' : '正在读取本次已保存的结果…';
  const injection = evidence('injection');
  if (injection && injection.reliable === true && finite(injection.score) && finite(injection.inj) && finite(injection.slope) && Array.isArray(injection.points)) v.injection = injection as DetectVerdict['injection'];
  else if (!messages.injection) messages.injection = injection?.note || '注入大小尚未确认。';
  for (const kind of ['tools', 'web'] as const) {
    const e = evidence(kind);
    if (e) v[kind] = { status: typeof e.status === 'string' ? e.status : 'unknown', pass: typeof e.pass === 'boolean' ? e.pass : null, reason: e.reason ?? '没有收到足够的结构化证据，本项未经验证。', protocol: e.protocol ?? '', evidence: e.evidence };
  }
  const cache = evidence('cache');
  if (cache) v.cache = { ...cache, pass: typeof cache.pass === 'boolean' ? cache.pass : null } as DetectVerdict['cache'];
  const thinking = evidence('thinking');
  if (thinking && typeof thinking.pass === 'boolean' && finite(thinking.summary_score ?? thinking.score) && finite(thinking.think_len)) v.thinking = { ...thinking, summary_score: thinking.summary_score ?? thinking.score } as DetectVerdict['thinking'];
  else if (!messages.thinking) messages.thinking = '思维摘要尚未确认。';
  const visions = groups.vision ?? [];
  if (visions.length === 1) {
    const item = visions[0], j = itemJudgment(item), earned = item.result?.earned ?? j.earned, max = item.result?.max ?? j.max ?? item.definition.maxScore;
    if (item.definition.judge.type !== 'manual' && item.status === 'done' && !item.result?.error && j.status === 'known' && finite(earned) && finite(max) && max > 0 && typeof item.result?.recognized === 'boolean') {
      const recognized = item.result.recognized;
      v.vision = { score: null, max: 100, recognized, recognized_score: recognized ? 50 : 0, recognized_max: 50,
        answer_score: Math.floor((earned * 50 + max / 2) / max), answer_max: 50, correct: earned === max, earned, question_max: max,
        note: '依据本次已保存的识图与答案判定展示。' };
    } else messages.vision = `${itemLabel(item, report.status) === '完成' ? '尚未确认' : itemLabel(item, report.status)}${item.result?.error ? '：' + item.result.error : ''}${item.definition.judge.type === 'manual' ? '；不参与自动总分。' : ''}`;
  } else if (visions.length > 1) messages.vision = `本次有${visions.length}道识图题，请展开逐题查看。`;
  const k = object(summary.knowledge);
  if (k.status === 'not_applicable') messages.knowledge = k.note || '本次没有适用的已发生事件；本次未提问。';
  else if (Object.keys(k).length && k.status !== 'not_selected') {
    const range = k.status === 'range' && k.valid !== false;
    v.knowledge = { status: k.status ?? 'unknown', label: range ? '本次事件题推测范围' : '本次知识事件检测', reason: range ? [k.note || k.label || '本次事件题推测范围，不证明模型训练日期。', k.lower ? `已知覆盖下界：${k.lower}` : '', k.upper ? `未知或错误上界：${k.upper}` : ''].filter(Boolean).join('\n') : ['答案存在矛盾或无法判定，无法确认日期范围。请查看逐题观测证据。', k.note || k.label].filter(Boolean).join('\n'), protocol: report.protocol ?? '', rows: [] };
  } else messages.knowledge = (groups.knowledge?.length ?? 0) ? '请展开逐题查看本次事件题观测；日期范围尚未确认。' : complete ? '本次未选择知识事件检测。' : '正在读取本次已保存的结果…';
  const probes: Probe[] = items.map(item => {
    const label = itemLabel(item, report.status), j = itemJudgment(item);
    const status = ['not_asked', 'not_applicable'].includes(j.status ?? '') ? 'queued' : label === '未完成' ? 'cancelled' : item.status;
    return { id: item.itemId, name: item.definition.title, status, ms: typeof item.result?.ms === 'number' ? item.result.ms : Math.max(0, (item.completedAtMs ?? 0) - (item.startedAtMs ?? 0)), error: item.result?.error };
  });
  const statusTexts = Object.fromEntries(items.map(item => [item.itemId, itemLabel(item, report.status)]));
  const counts = report.itemCounts;
  const totalCount = counts ? Object.values(counts).reduce((n, count) => n + count, 0) : complete ? items.length : null;
  const finished = counts ? (counts.done ?? 0) + (counts.error ?? 0) : items.filter(i => ['done', 'error'].includes(i.status)).length;
  const unfinished = isHubTerminal(report.status) && totalCount !== null ? Math.max(0, totalCount - finished) : 0;
  return { verdict: v, totalLabel, groups, messages, probes, progress: { total: totalCount, finished, statusTexts, unfinished } };
}

export interface ItemReadState { cursor: string; batches: number; ids: Set<string>; cursors: Set<string>; complete: boolean }
export const newItemRead = (): ItemReadState => ({ cursor: '', batches: 0, ids: new Set(), cursors: new Set(), complete: false });
export async function readReportItems(call: import('./hub-types').HubCall, id: string, state: ItemReadState, current: () => boolean, onPage: (items: HubItem[]) => void) {
  while (current()) {
    const cursor = state.cursor;
    if (state.cursors.has(cursor)) throw new Error('报告回答的读取位置重复，已停止继续读取；请核查运行状态后重新读取。');
    const page = await call<import('./hub-types').HubPage<HubItem>>('items', { reportId: id, cursor, limit: 5 });
    if (!current()) return;
    const rows = page.items ?? [], fresh = rows.filter(i => !state.ids.has(i.itemId));
    if (state.ids.size + fresh.length > 128) throw new Error('报告超过128项，已保留已读取内容；请核查此报告的数据来源后重试读取。');
    if (page.nextCursor && (!fresh.length || state.cursors.has(page.nextCursor) || page.nextCursor === cursor)) throw new Error('报告回答没有前进到下一批，已停止继续读取；已读内容仍可查看，请核查运行状态后重试读取。');
    state.cursors.add(cursor); rows.forEach(i => state.ids.add(i.itemId)); state.batches++;
    onPage(rows);
    if (!page.nextCursor) { state.complete = true; return; }
    state.cursor = page.nextCursor;
  }
}
