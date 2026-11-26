'use client';
import type { HubBatchDetail, HubBatchFilter, HubBatchRow } from '../lib/hub-types';
import { batchResultParts, batchStateLabel, duration, memberTimes, phaseLabel, resultCell, validBatchCounts } from '../lib/hub-batch-view';
import { displayTime } from '../lib/hub-crosscheck';
import { reportError } from '../lib/hub-report-view';

export type HubStopIntent = { reportId: string; expectedRetryId: string; ack: 'pending' | 'accepted' | 'unknown'; confirmationDeadlineMs: number };
export const retryRound = (row: {retry?: {id: string}; retryId?: string}) => row.retry?.id ?? row.retryId ?? '';
export function stopAction(row: HubBatchRow, now: number, intent?: HubStopIntent | null): { label: string; disabled: boolean } | null {
  const matching = intent?.reportId === row.reportId && intent.expectedRetryId === retryRound(row) ? intent : undefined;
  const anotherPending = Boolean(intent && !matching && (intent.ack === 'pending' || Date.now() < intent.confirmationDeadlineMs));
  if (row.phase === 'queued' && !matching) return { label: '取消等待', disabled: anotherPending };
  if (['preparing', 'running'].includes(row.phase) && !matching && !row.stopConfirmDeadlineMs) return { label: '停止检测', disabled: anotherPending };
  if (matching || row.phase === 'stopping' || row.stopConfirmDeadlineMs) {
    const localNow = Date.now(), serverDeadlineReached = !row.stopConfirmDeadlineMs || now >= row.stopConfirmDeadlineMs;
    const expired = matching ? localNow >= matching.confirmationDeadlineMs : serverDeadlineReached;
    if (row.phase === 'ended' || row.phase === 'saving_end' || row.phase === 'saving_cancel' || row.phase === 'paused') return null;
    return { label: expired ? '再次停止' : '正在停止…', disabled: !expired || matching?.ack === 'pending' || row.stopCallPending === true || !row.stopConfirmDeadlineMs || !serverDeadlineReached };
  }
  return null;
}
export default function HubBatchView({ batch, filter, loading, message, stopIntent, onFilter, onReport, onStop, onRefresh }: {
  batch: HubBatchDetail | null; filter: HubBatchFilter; loading: boolean; message: string; stopIntent: HubStopIntent | null;
  onFilter: (next: HubBatchFilter) => void; onReport: (id: string) => void; onStop: (row: HubBatchRow) => void; onRefresh: () => void;
}) {
  const change = (patch: Partial<HubBatchFilter>) => onFilter({ ...filter, ...patch, offset: 0 });
  if (!batch) return <section className="hero-card hub-card"><h1>任务与结果</h1><p>{message || '正在读取批次详情…'}</p><button type="button" className="btn-secondary" disabled={loading} onClick={onRefresh}>核查运行状态</button></section>;
  const c = batch.counts, valid = validBatchCounts(c), now = batch.serverNow;
  const columns = [...batchResultParts, { id: 'knowledge', label: '知识事件' }, { id: 'total', label: '总分' }];
  return <div className="hub-batch-view">
    <section className="hero-card hub-card"><div className="section-heading"><h1>批次详情</h1><button type="button" className="btn-secondary" disabled={loading} onClick={onRefresh}>核查运行状态</button></div>
      <p>批次编号：<b>{batch.batchId}</b> <button type="button" className="hub-text-button" onClick={() => { if (navigator.clipboard) void navigator.clipboard.writeText(batch.batchId); }}>复制批次编号</button></p>
      <p className="muted">创建于 {displayTime(batch.createdAtMs, true)} · 最后确认 {displayTime(batch.observedAt, true)}</p>
      <p className="muted">共同检测范围：{batch.sections?.map(s => `${s.name}（${s.count}项）`).join('、') || '请查看各份原报告'}</p>
      <h2>{valid ? batchStateLabel(batch.state) : '状态待确认'}{batch.firstFinishedAtMs && c.retrying > 0 ? ' · 补测' : ''}</h2>
      {valid ? <><h2>已结束{c.ended}/{c.total}，尚余{c.total - c.ended}份</h2><progress max={c.total} value={c.ended} aria-label="已结束任务数"/><dl className="hub-batch-counts"><div><dt>总任务数</dt><dd>{c.total}</dd></div><div><dt>执行中</dt><dd>{c.active}</dd></div><div><dt>等待</dt><dd>{c.waiting}</dd></div><div><dt>待确认</dt><dd>{c.unconfirmed}</dd></div><div><dt>完整结束</dt><dd>{c.complete}</dd></div><div><dt>部分未确认</dt><dd>{c.partial}</dd></div><div><dt>异常</dt><dd>{c.abnormal}</dd></div><div><dt>取消</dt><dd>{c.cancelled}</dd></div></dl></> : <p className="hub-error">最新批次计数不一致，请核查状态，不能确认已经结束。</p>}
      <p className="muted">已结束包含部分未确认、异常和取消，不代表检测全部通过。当前一次只执行一份任务。</p>
      {batch.firstFinishedAtMs ? <p>首轮已结束，首次历时 {duration(batch.firstFinishedAtMs - batch.createdAtMs)}{c.retrying > 0 && `，当前补测${c.retrying}份`}。</p> : <p>已耗时 {duration(now - batch.createdAtMs)}{['unknown','paused','saving_end'].includes(batch.state) && '；首轮结束尚未确认。'}</p>}
      {batch.eta ? <p>预计剩余 {duration(batch.eta.lowMs)}～{duration(batch.eta.highMs)}</p> : batch.state !== 'ended' && <p>暂无法可靠估计剩余时间：{batch.etaReason || '状态待核查或样本不足'}。</p>}
      <p className="muted">已耗时包含排队；预计剩余依据本批相近任务粗略估计。自动模式可能走不同接口，站点差异、重试和故障会让误差变大。</p>
      {message && <p role="status" className="hub-error">{message}</p>}

    </section>
    <section className="hero-card hub-card"><div className="actions hub-wrap"><button type="button" className={filter.view === 'progress' ? 'btn-primary' : 'btn-secondary'} onClick={() => change({ view: 'progress' })}>执行进度</button><button type="button" className={filter.view === 'results' ? 'btn-primary' : 'btn-secondary'} onClick={() => change({ view: 'results' })}>检测结果</button></div>
      {filter.view === 'results' ? <p className="muted">这里只汇总已有板块结论和分数；没有板块汇总请查看报告。完整回答按需读，不重新评分或修改主站。</p> : <p className="muted">只停止或取消这一份任务，不修改主站；其他等待任务在结束处理完成后继续。已保存回答保留。</p>}
      <form className="hub-form-grid" onSubmit={e => { e.preventDefault(); const data = new FormData(e.currentTarget); change({ targetId: String(data.get('targetId') || '').trim(), model: String(data.get('model') || '').trim(), status: String(data.get('status') || '') }); }} key={`${batch.batchId}:${filter.targetId}:${filter.model}:${filter.status}`}>
        <div className="field"><label>账号筛选<input name="targetId" defaultValue={filter.targetId} placeholder="选择账号或输入完整编号" list="batch-account-options" /></label><datalist id="batch-account-options">{Array.from(new Map(batch.items.map(row => [row.targetId, row.accountName])).entries()).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</datalist></div>
        <div className="field"><label>模型筛选<input name="model" defaultValue={filter.model} placeholder="输入模型原名" list="batch-model-options" /></label><datalist id="batch-model-options">{Array.from(new Set(batch.items.map(row => row.model))).map(model => <option key={model} value={model} />)}</datalist></div>
        <div className="field"><label>阶段筛选<select name="status" defaultValue={filter.status}><option value="">全部阶段</option>{(['queued','preparing','running','stopping','saving_end','saving_cancel','paused','unknown','ended'] as const).map(p => <option key={p} value={p}>{phaseLabel(p)}</option>)}</select></label></div>
        <div className="actions"><button type="submit" className="btn-secondary">应用筛选</button><button type="button" className="btn-secondary" onClick={() => change({ targetId: '', model: '', status: '' })}>清除筛选</button></div>
      </form>
      <div className="table-wrap"><table className="hub-batch-table"><thead><tr><th>账号 / 分组</th><th>模型</th><th>阶段</th>{filter.view === 'progress' ? <><th>等待时间</th><th>检测耗时</th></> : columns.map(col => <th key={col.id}>{col.label}</th>)}<th>操作</th></tr></thead><tbody>{batch.items.map(row => {
        const member = batch.memberStates?.find(m => m.reportId === row.reportId), times = memberTimes(member, row.createdAtMs, now);
        const control = stopAction(row, now, stopIntent), retrying = Boolean(row.retryId) && row.phase !== 'ended';
        const rowError = reportError(row.error) || row.recoveryReason || row.diagnostic;
        return <tr key={row.reportId} data-batch-report-id={row.reportId}><td>{row.accountName}{row.accountId ? ` #${row.accountId}` : ''}<br/><span className="muted">{row.groupName}</span></td><td>{row.model}</td><td>{phaseLabel(row.phase)}{retrying && ' · 补测'}{rowError && <p className="hub-error">{rowError}</p>}</td>{filter.view === 'progress' ? <><td>{times.wait}</td><td>{times.elapsed}{times.budget && <p className="muted">{times.budget}</p>}</td></> : retrying ? <td colSpan={columns.length}>补测中，结果待更新</td> : columns.map(col => <td key={col.id}>{resultCell(row.summary, col.id)}</td>)}<td><div className="actions hub-wrap"><button type="button" className="btn-secondary hub-small" onClick={() => onReport(row.reportId)}>查看报告</button>{control && <button type="button" className="btn-secondary hub-small" disabled={control.disabled} onClick={() => onStop(row)}>{control.label}</button>}</div>{control?.label === '再次停止' && <p className="muted">只有上次停止调用已返回且确认时间已过，才可再次停止这一份；会重新等待15秒确认，不影响其他任务。</p>}</td></tr>;
      })}</tbody></table></div>
      {!batch.items.length && <p className="muted">没有符合当前筛选的任务，可清除筛选；全批统计仍如上。</p>}
      <p className="muted">只查看已保存的任务和结果，不会重新检测或修改主站。</p>
      <div className="actions hub-wrap">{filter.offset > 0 && <button type="button" className="btn-secondary" onClick={() => onFilter({ ...filter, offset: 0 })}>返回首页</button>}{typeof batch.nextOffset === 'number' && batch.nextOffset > filter.offset && <button type="button" className="btn-secondary" onClick={() => onFilter({ ...filter, offset: batch.nextOffset! })}>下一页任务</button>}</div>
    </section>
  </div>;
}
