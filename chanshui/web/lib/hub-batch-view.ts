import type { HubBatchCounts, HubBatchMember, HubBatchState, HubPhase } from './hub-types';
import { object } from './hub-report-view';

export const batchStateLabel = (state: HubBatchState) => ({ ended: '已结束', running: '执行中', unknown: '状态待确认', saving_end: '结束处理中', paused: '异常暂停', queued: '等待执行' }[state] ?? '状态待确认');
export const phaseLabel = (phase?: HubPhase) => ({ queued: '等待执行', preparing: '准备中', running: '检测中', stopping: '停止确认中', saving_end: '正在保存结束状态', saving_cancel: '正在保存取消状态', paused: '异常暂停', unknown: '状态待确认', ended: '已结束' }[phase ?? 'unknown'] ?? '状态待确认');
export const emptyBatchFilter = { targetId: '', model: '', status: '', view: 'progress' as const, offset: 0 };
export const validBatchCounts = (counts: HubBatchCounts | undefined): counts is HubBatchCounts => Boolean(counts && Object.values(counts).every(n => Number.isInteger(n) && n >= 0) && counts.total >= 1 && counts.total <= 32 && counts.active + counts.waiting + counts.ended + counts.unconfirmed === counts.total && counts.complete + counts.partial + counts.abnormal + counts.cancelled === counts.ended);
export const duration = (ms: number) => {
  if (!Number.isFinite(ms) || ms < 0) return '未确认';
  const seconds = Math.floor(ms / 1000), hour = Math.floor(seconds / 3600), minute = Math.floor(seconds % 3600 / 60), second = seconds % 60;
  return `${hour ? `${hour}小时` : ''}${minute ? `${minute}分` : ''}${second}秒`;
};
function stamp(value: unknown) { return typeof value === 'string' ? Date.parse(value) : typeof value === 'number' ? value : NaN; }
export function memberTimes(member: HubBatchMember | undefined, createdAt: number, now: number) {
  const unknown = { wait: '未确认', elapsed: '未确认', budget: '' };
  if (!member) return unknown;
  const timing = object(member.timing), started = stamp(timing.started_at), finished = stamp(timing.finished_at);
  const retry = Boolean(member.retryId), accepted = retry ? member.retryAcceptedAtMs ?? NaN : createdAt;
  const startedCurrent = Number.isFinite(started) && started >= accepted && started <= now;
  const total = typeof timing.total_ms === 'number' && timing.total_ms >= 0 ? timing.total_ms : NaN;
  const queued = ['queued', 'preparing'].includes(member.phase);
  const elapsed = startedCurrent && !queued && Number.isFinite(total) && finished >= started ? duration(total) : startedCurrent && ['running','stopping'].includes(member.phase) && !Number.isFinite(finished) ? duration(Math.max(0, now - started) + (Number.isFinite(total) ? total : 0)) : retry && Number.isFinite(total) ? `以前累计 ${duration(total)}；本轮待确认` : queued ? '尚未开始' : '未确认';
  const wait = startedCurrent && !queued ? duration(started - accepted) : queued && Number.isFinite(accepted) ? duration(Math.max(0, now - accepted)) : member.finishConfirmed && !startedCurrent ? '未开始检测，历史等待时长未记录' : '未记录';
  const budget = member.phase === 'running' || member.phase === 'stopping' || member.phase === 'preparing' ? startedCurrent && Number.isFinite(member.totalTimeoutSeconds) ? `本轮执行时间还剩${duration(Math.max(0, member.totalTimeoutSeconds! * 1000 - (now - started)))}；这不是预计完成时间，到时会结束本轮检测。` : '本轮尚未开始或起点待确认' : '';
  return { wait: retry && startedCurrent ? `本轮 ${wait}（首轮等待未记录）` : wait, elapsed, budget };
}
export const batchResultParts = [
  { id: 'iq', label: '推理' }, { id: 'vision', label: '识图' }, { id: 'tools', label: '工具' }, { id: 'cache', label: '缓存' }, { id: 'injection', label: '注入' }, { id: 'web', label: '联网' }, { id: 'thinking', label: '思维' }, { id: 'fingerprint', label: '数字/行为指纹' },
];
export function resultCell(summary: unknown, key: string): string {
  const value = object(summary), total = object(value.total), parts = Array.isArray(total.parts) ? total.parts : [];
  if (key === 'total' && total.complete === true && value.totalScore === null && parts.length && !parts.some(part => object(part).applicable === true)) return '本次不计总分';
  if (key === 'total') return total.complete === true && typeof value.totalScore === 'number' ? `${value.totalScore} 分` : total.complete === true && typeof total.score === 'number' && value.totalScore !== null ? `${total.score} 分` : `总分尚未确认${typeof total.reason === 'string' ? `（${total.reason}）` : ''}`;
  if (key === 'knowledge') {
    const knowledge = object(value.knowledge);
    if (!Object.keys(knowledge).length) return '尚无板块汇总，请查看报告';
    if (knowledge.status === 'not_selected') return '未选择';
    const label = typeof knowledge.label === 'string' ? knowledge.label : '尚未确认';
    return `${label}${typeof knowledge.correct === 'number' && typeof knowledge.total === 'number' ? ` · ${knowledge.correct}/${knowledge.total}` : ''}`;
  }
  const part = parts.map(object).find(part => part.key === key);
  if (!part) return '尚无板块汇总，请查看报告';
  if (part.applicable !== true) return '不适用／未选择';
  if (part.known !== true) return key === 'cache' && part.points === 0 ? '尚未确认，暂计0分' : '尚未确认';
  if (typeof part.points !== 'number' || typeof part.max_points !== 'number') return '尚无可靠分数，请查看报告';
  return `${part.points}/${part.max_points}${typeof part.contribution === 'number' ? `（计入总分 ${part.contribution}）` : ''}`;
}
