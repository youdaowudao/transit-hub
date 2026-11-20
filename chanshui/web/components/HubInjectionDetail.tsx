"use client";

import { useRef, useState } from 'react';
import {
  INJECTION_TEXT as text,
  impactLine,
  injectionFactLine,
  prefixLine,
  promptFullError,
  promptFullNote,
  promptLines,
  requestCell,
  requestRoundOverhead,
  requestTable,
  type CrossView,
} from '../lib/hub-crosscheck';
import { object } from '../lib/hub-report-view';
import type { HubCall, HubItem, HubPrefixPrompt } from '../lib/hub-types';

type PromptRead = { phase: 'loading' } | { phase: 'ok'; data: HubPrefixPrompt } | { phase: 'error'; error: string };

export default function HubInjectionDetail({ item, cross, model, call }: {
  item: HubItem;
  cross: CrossView;
  model: string;
  call: HubCall;
}) {
  const requested = useRef(new Set<string>());
  const [prompts, setPrompts] = useState<Record<string, PromptRead>>({});
  const data = cross.phase === 'ok' ? cross.data : undefined;
  const impact = data && impactLine(data);
  const match = impact && data?.reference?.match;
  const prompt = match ? prompts[match.id] : undefined;
  const evidence = object(item.result?.evidence);
  const template = item.definition.template;
  const table = requestTable(evidence, template, item.result?.error, item.usableInjectionPoints ?? 0);
  const roundOverhead = requestRoundOverhead(evidence);

  async function readPrompt(id: string) {
    if (requested.current.has(id)) return;
    requested.current.add(id);
    setPrompts(current => ({ ...current, [id]: { phase: 'loading' } }));
    try {
      const response = await call<HubPrefixPrompt>('prefix-prompt', { promptId: id });
      setPrompts(current => ({ ...current, [id]: { phase: 'ok', data: response } }));
    } catch (error) {
      requested.current.delete(id);
      setPrompts(current => ({ ...current, [id]: { phase: 'error', error: promptFullError(error) } }));
    }
  }

  return <>
    <p className="muted">{injectionFactLine(evidence.points, template)}</p>
    <p className="muted">{prefixLine(cross)}</p>
    {impact && <><p className="muted">{impact}</p><p className="muted">{text.impactContext}</p></>}
    {data && promptLines(data, model).map(line => <p key={line} className="muted">{line}</p>)}
    {match && <details key={match.id} className="fold" onToggle={event => {
      if (event.currentTarget.open) void readPrompt(match.id);
    }}>
      <summary>{text.promptTitle}</summary>
      {prompt?.phase === 'loading' && <p className="muted" role="status">{text.promptLoading}</p>}
      {prompt?.phase === 'error' && <p className="error-box" role="alert">{prompt.error}</p>}
      {prompt?.phase === 'ok' && <>
        <p className="muted">{promptFullNote(match, prompt.data.source)}</p>
        <pre>{prompt.data.text}</pre>
      </>}
    </details>}
    <details className="fold">
      <summary>{text.requestsTitle}</summary>
      <div className="table-wrap"><table>
        <thead><tr>{text.requestsColumns.map(label => <th key={label} scope="col">{label}</th>)}</tr></thead>
        <tbody>{table.rows.map((row, index) => <tr key={index}>
          {row.map((value, column) => <td key={text.requestsColumns[column]}>{requestCell(value)}</td>)}
        </tr>)}</tbody>
      </table></div>
      <p className="muted">{table.note}</p>
      {roundOverhead !== null && <p className="muted">{roundOverhead}</p>}
      <p className="muted">{text.scoring}</p>
    </details>
  </>;
}
