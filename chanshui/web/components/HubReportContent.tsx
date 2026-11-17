"use client";
import type { ReactNode } from 'react';
import ResultCards from './ResultCards';
import ProbeTimeline from './ProbeTimeline';
import HubQuestionDetails from './HubQuestionDetails';
import { hubReportView } from '../lib/hub-report-view';
import { isHubTerminal } from '../lib/hub-policy';
import type { HubCall, HubItem, HubReport } from '../lib/hub-types';

export default function HubReportContent({ report, items, complete, call }: { report: HubReport; items: HubItem[]; complete: boolean; call: HubCall }) {
  const view = hubReportView(report, items, complete);
  const questions: Record<string, ReactNode> = {};
  for (const [kind, rows] of Object.entries(view.groups)) {
    const folds = rows.map((item, index) => <HubQuestionDetails key={report.reportId + ':' + item.itemId} item={item} reportId={report.reportId} reportStatus={report.status} call={call} number={kind === 'iq' ? index + 1 : undefined} />);
    questions[kind] = kind === 'iq' ? <><p className="muted">点击题目查看本次题面、回答和判定；默认收起，不会重新检测或修改主站。</p>{folds}</> : <div className="question-list">{folds}</div>;
  }
  const sectionContent: Record<string, ReactNode> = Object.fromEntries(Object.entries(view.messages).map(([key, text]) => [key, <p key={key} className="muted">{text}</p>]));
  // Preserve the upstream omission of an unselected vision section.
  if (complete && !view.groups.vision?.length) sectionContent.vision = null;
  const ended = isHubTerminal(report.status);
  const startedAt = typeof report.startedAtMs === 'number' ? report.startedAtMs : report.status === 'queued' ? 0 : report.createdAtMs;
  const timing = startedAt && (!ended || report.updatedAtMs) ? { started_at: new Date(startedAt).toISOString(), ...(ended ? { finished_at: new Date(report.updatedAtMs!).toISOString() } : {}), total_ms: ended ? Math.max(0, report.updatedAtMs! - startedAt) : 0, last_run_ms: 0, runs: 1 } : undefined;
  return <>
    <ResultCards verdict={view.verdict} model={report.model} loading={!ended} showCache={Boolean(view.groups.cache)} savedScore totalLabel={view.totalLabel} questions={questions} sectionContent={sectionContent} />
    <ProbeTimeline probes={view.probes} status={report.status} timing={timing} progress={view.progress}>
      {!report.model.toLowerCase().startsWith('claude-') && questions.thinking}
    </ProbeTimeline>
  </>;
}
