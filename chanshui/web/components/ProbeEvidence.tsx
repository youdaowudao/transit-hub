import type { CacheSample } from '@/lib/api';
import { useI18n } from '@/lib/i18n';
import type { HubItem } from '../lib/hub-types';

// Shared with the original ResultCards: missing usage is not a reported zero.
export const cacheToken = (value?: number | null, reported?: boolean): string | number => reported === false || value == null || !Number.isFinite(value) ? '—' : value;
export const cacheRate = (value?: number | null): string => value == null || !Number.isFinite(value) ? '—' : value.toFixed(2) + '%';
export const capabilityLabel = (pass?: boolean | null): string => pass === true ? '通过' : pass === false ? '未通过' : '未经验证';
const object = (value: unknown): Record<string, unknown> => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
const rows = (value: unknown): Record<string, unknown>[] => Array.isArray(value) ? value.map(object) : [];
const number = (value: unknown): number | undefined => typeof value === 'number' && Number.isFinite(value) ? value : undefined;
const text = (value: unknown): string => typeof value === 'string' ? value : '';
const bool = (value: unknown): boolean | undefined => typeof value === 'boolean' ? value : undefined;
const percent = (value: unknown) => number(value) === undefined ? '—' : `${(Number(value) * 100).toFixed(1)}%`;
export function cacheRoundInput(sample?: Partial<CacheSample>): number | undefined {
  return sample?.input_reported === false ? undefined : number(sample?.total);
}

export function cacheSampleHitRate(sample?: Partial<CacheSample>): number | undefined {
  const total = cacheRoundInput(sample);
  const read = number(sample?.read);
  if (sample?.error || sample?.read_reported === false || total == null || total <= 0 ||
    read == null || read < 0 || read > total) return undefined;
  return read / total * 100;
}

export function cacheValidationReason(reason: string): string {
  return reason.replace(/末轮缓存率是末轮读取除以上一轮读加写(?:，相等为 100%)?。/, '');
}

export function CacheTransitionNote({ samples, ratio, baseline }: {
  samples?: Partial<CacheSample>[];
  ratio?: number | null;
  baseline?: number;
}) {
  const { t: tr } = useI18n();
  const last = samples?.at(-1);
  const previous = samples?.at(-2);
  if (number(ratio) == null || !last || !previous || last.error || previous.error ||
    last.read_reported === false || previous.read_reported === false || previous.write_reported === false) return null;
  const read = number(last.read);
  const previousRead = number(previous.read);
  const previousWrite = number(previous.write);
  const base = number(baseline) ?? (previousRead == null || previousWrite == null ? undefined : previousRead + previousWrite);
  if (read == null || base == null || base <= 0) return null;
  return <p className="muted">{tr('末轮读取与上一轮读加写的比值')} {cacheRate(ratio)} · {tr('相差 {delta} token', { delta: Math.abs(read - base) })}</p>;
}

const safeURL = (value: unknown) => { try { const url = new URL(String(value)); return ['http:', 'https:'].includes(url.protocol) ? url.href : ''; } catch { return ''; } };

export function readableReportError(value: unknown): string {
  if (typeof value === 'string') return value;
  const e = object(value);
  return [text(e.message), text(e.detail)].filter((s, i, all) => s && all.indexOf(s) === i).join('。');
}

export function HubProbeEvidence({ item, verifiedCache = false, previous = false, inlineCache = false }: { item: HubItem; verifiedCache?: boolean; previous?: boolean; inlineCache?: boolean }) {
  const e = object(item.result?.evidence), kind = item.definition.kind;
  const judgment = object(item.result?.judgment);
  const judgmentStatus = typeof item.result?.judgment === 'string' ? item.result.judgment : text(judgment.status);
  if (['not_asked', 'not_applicable'].includes(judgmentStatus)) return null;
  const has = Object.keys(e).length > 0;
  if ((item.status === 'queued' || item.status === 'running') && !(verifiedCache && previous)) return <p className="muted">{item.status === 'queued' ? '本项尚未执行；当前没有结果。' : '本项正在检测，等待上游结果。'}</p>;
  if (!has) return ['iq', 'vision', 'fingerprint_behavior'].includes(kind) ? null : <p className="muted">本项尚无可确认的检测证据，请查看本项错误或报告状态。</p>;
  if (kind === 'tools' || kind === 'web') {
    const details = object(e.evidence), sources = Array.isArray(details.sources) ? details.sources : [];
    return <div><div className="metric-line"><strong className={e.pass === true ? 'good' : 'warn'}>{capabilityLabel(bool(e.pass))}</strong></div><p className="muted">{text(e.reason) || '没有收到足够的结构化证据，本项未经验证。'}</p>{kind === 'tools' && <div className="mini-stats"><span>结构化调用 <b>{Array.isArray(details.calls) ? details.calls.length : '—'}</b></span><span>工具结果往返 <b>{details.roundtrip === true ? '已确认' : details.roundtrip === false ? '未确认' : '—'}</b></span></div>}{kind === 'web' && sources.length > 0 && <ul>{sources.map((source, index) => { const value = typeof source === 'string' ? source : object(source).url; const url = safeURL(value); return url ? <li key={index}><a href={url} target="_blank" rel="noreferrer">{typeof source === 'object' ? text(object(source).title) || url : url}</a></li> : null; })}</ul>}</div>;
  }
  if (kind === 'cache') {
    const samples = rows(e.samples) as Partial<CacheSample>[];
    const manual = verifiedCache && judgment.source === 'manual';
    const pass = manual ? (number(judgment.earned) ?? 0) > 0 : verifiedCache && judgmentStatus !== 'known' ? undefined : bool(e.pass);
    const label = manual ? pass ? '人工判为通过' : '人工判为不通过'
      : verifiedCache && pass === undefined ? '尚未确认' : !verifiedCache && pass === false ? '未对齐' : capabilityLabel(pass);
    return <div>
      {verifiedCache && samples.length > 0 && <p className="muted">本题已保存样本{previous ? '（上次结果）' : ''}</p>}
      {(!inlineCache || manual) && <div className="metric-line"><strong className={pass === true ? 'good' : 'warn'}>{label}</strong></div>}
      <p className="muted">{manual && '自动检测记录：'}{cacheValidationReason(text(e.reason))}</p>
      <div className="cache-rates">
        <div><span>末轮缓存命中率</span><strong>{cacheRate(cacheSampleHitRate(samples.at(-1)))}</strong></div>
        <div><span>已记录轮数</span><strong>{samples.length} / 6</strong></div>
      </div>
      <div className="table-wrap"><table className="cache-table">
        <thead><tr><th>请求</th><th>读取</th><th>写入</th><th>{verifiedCache ? '总输入' : '输入'}</th><th>输出</th><th>命中率</th>{verifiedCache && <th>状态</th>}</tr></thead>
        <tbody>{samples.map((sample, i) => <tr key={sample.index ?? i}>
          <td>#{String(sample.index ?? i + 1)}</td>
          <td>{cacheToken(sample.read, sample.read_reported)}</td>
          <td>{cacheToken(sample.write, sample.write_reported)}</td>
          <td>{cacheToken(verifiedCache ? sample.total : sample.input, sample.input_reported)}</td>
          <td>{cacheToken(sample.output, sample.output_reported)}</td>
          <td>{cacheRate(cacheSampleHitRate(sample))}</td>
          {verifiedCache && <td>{sample.error || (sample.index && sample.index > 1 && sample.read_reported === true && sample.write_reported === true && sample.read === 0 && Number(sample.write) > 64 ? '整段重写' : '')}</td>}
        </tr>)}</tbody>
      </table></div>
      <p className="muted">命中率按当轮缓存读取除以当轮总输入计算。“—”表示没有提供或无法计算。</p>
      <details className="fold"><summary>多轮缓存明细与衔接</summary>
        <CacheTransitionNote samples={samples} ratio={number(e.third_read_ratio)} baseline={number(e.third_read_baseline)} />
        <p className="muted">这里比较相邻两轮的缓存用量。失败或重试影响可靠性时，以本项说明为准。</p>
      </details>
    </div>;
  }
  if (kind === 'injection') return <div><div className="metric-line"><strong>{e.reliable === true ? cacheToken(number(e.inj)) : '未确认'}</strong><span>{e.reliable === true ? '估计 token' : ''}</span></div><div className="mini-stats"><span>有效样本 <b>{rows(e.points).length} / 2</b></span><span>斜率 <b>{number(e.slope)?.toFixed(3) ?? '—'}</b></span><span>得分 <b>{e.reliable === true && number(e.score) !== undefined ? `${e.score} / 10` : '尚未确认'}</b></span></div><p className="muted">{text(e.note)} 这是固定协议开销的近似值，不能据此判断恶意注入。</p></div>;
  if (kind === 'thinking') return <div><div className="metric-line"><strong className={e.pass === true ? 'good' : 'warn'}>{capabilityLabel(bool(e.pass))}</strong></div><p className="muted">{text(e.reason)}</p><div className="mini-stats"><span>摘要字符 <b>{cacheToken(number(e.think_len))}</b></span><span>签名字符 <b>{cacheToken(number(e.sig_len))}</b></span><span>思维用量 <b>{cacheToken(number(e.thinking_tokens))}</b></span></div>{text(e.thinking_text) && <details className="fold"><summary>实际返回的可见摘要</summary><pre>{text(e.thinking_text)}</pre></details>}<p className="muted">只判断接口返回的可见摘要，不代表完整内部推理。</p></div>;
  if (kind === 'knowledge') { const row=Object.keys(object(e.row)).length?object(e.row):e; return <div><div className="metric-line"><strong>{({ know: '知道', wrong: '答错', unknown: '不知道', invalid: '无法判定', error: '请求失败' } as Record<string, string>)[text(row.kind)] ?? '未确认'}</strong></div><p className="muted">{text(row.error)||text(row.reason)||text(row.answer)}</p></div>; }
  if (kind === 'fingerprint_numeric') {
    const attr = object(e.attribution), candidates = rows(attr.results).slice(0, 3);
    return <div><div className="metric-line"><strong>{e.compatible === false ? '参考不适用' : attr.lowConfidence === true || !text(attr.topModel) ? '尚未确认' : text(attr.topModel)}</strong></div><p className="muted">{text(e.note)}</p><div className="mini-stats"><span>有效样本 <b>{number(e.usedOutputs) ?? 0} / {number(e.sampleLimit) ?? 10}</b></span><span>候选内概率 <b>{e.compatible !== false ? percent(attr.topProb) : '—'}</b></span></div>{e.compatible !== false && candidates.length > 0 && <div className="table-wrap"><table><thead><tr><th>参考候选</th><th>候选内概率</th></tr></thead><tbody>{candidates.map((c, i) => <tr key={i}><td>{text(c.modelId) || text(c.modelID) || text(c.id)}</td><td>{percent(c.prob)}</td></tr>)}</tbody></table></div>}<p className="muted">概率只在当前参考库候选间比较，不单独证明模型身份；提示模板失配时不作原库归因。</p><details className="fold"><summary>每次采样的题干与回答</summary>{rows(e.samples).map((sample, i) => <div key={i}><h4>第{i + 1}次 · {sample.usable === true ? '有效样本' : '未确认有效'}</h4><pre>{text(sample.prompt)}</pre><pre>{text(sample.answer) || text(sample.error) || '未返回正文'}</pre></div>)}</details></div>;
  }
  return null;
}

export function HubReportSummary({ summary }: { summary: Record<string, unknown> }) {
  const total = object(summary.total), parts = rows(total.parts), applicable = parts.filter(part => part.applicable === true);
  const complete = number(total.score) !== undefined && applicable.length > 0 && applicable.every(part => part.known === true) && total.complete !== false;
  const noAutoScore = Object.keys(total).length > 0 && applicable.length === 0;
  const knowledge = object(summary.knowledge), behavior = object(summary.behavior);
  const knowledgeUnknown = ['unknown', 'contradiction'].includes(text(knowledge.status)) || knowledge.valid === false;
  const knowledgeRangeKnown = knowledge.status === 'range' && !knowledgeUnknown;
  return <div><div className="metric-line"><strong>{complete ? total.score as number : noAutoScore ? '本次不计总分' : '尚未完成'}</strong>{complete && <span>/ {number(total.max) ?? 100}</span>}</div><p className="muted">{text(total.reason) || text(summary.note) || '仅在所有适用计分项已确认时显示总分；未知不当0分。'}</p>{parts.length > 0 && <div className="table-wrap"><table><thead><tr><th>检测板块</th><th>结果</th><th>计入总分</th></tr></thead><tbody>{parts.map((part, i) => <tr key={i}><td>{text(part.label)}</td><td>{part.applicable !== true ? '不适用／未选择' : part.known !== true ? '尚未确认' : `${String(part.points)}/${String(part.max_points ?? part.maxPoints)}`}</td><td>{part.applicable !== true ? '不计分' : part.known !== true ? '尚未计分' : `${String(part.contribution)}/${String(part.coefficient)}`}</td></tr>)}</tbody></table></div>}{number(summary.iqScore) !== undefined && <p>推理题：{String(summary.iqScore)} / 100</p>}{Object.keys(knowledge).length > 0 && knowledge.status !== 'not_selected' && <div className="hub-note"><strong>{knowledgeRangeKnown ? '本次事件题推测范围' : '本次知识事件检测'}</strong><p>{knowledgeUnknown ? '答案存在矛盾或无法判定，无法确认日期范围。请查看逐题观测证据。' : text(knowledge.note) || text(knowledge.label) || '请结合已观测事件阅读此范围，不代表实际训练截止日。'}</p>{knowledgeRangeKnown && text(knowledge.lower) && <p>已知覆盖下界：{text(knowledge.lower)}</p>}{knowledgeRangeKnown && text(knowledge.upper) && <p>未知或错误上界：{text(knowledge.upper)}</p>}</div>}{Object.keys(behavior).length > 0 && <div className="hub-note"><strong>行为参考</strong><p>{text(behavior.note) || '仅供风格参考，不参与推理得分。'}</p>{rows(behavior.top3).map((b, i) => <p key={i}>{text(b.displayName) || text(b.modelId)} · 相似度 {percent(b.similarity)}</p>)}</div>}</div>;
}
