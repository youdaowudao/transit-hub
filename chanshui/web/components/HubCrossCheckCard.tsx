"use client";

import {
  CROSS_CHECK_TEXT as text,
  checkLines,
  conclusionTitle,
  crossError,
  gradeText,
  lineText,
  peerRows,
  peersTitle,
  routeDetail,
  type CrossView,
} from '../lib/hub-crosscheck';
import type { HubItem } from '../lib/hub-types';

export default function HubCrossCheckCard({ cross, items, onRetry, onRefresh, refreshing }: {
  cross: CrossView;
  items: HubItem[];
  onRetry: () => void;
  onRefresh: () => void;
  refreshing: boolean;
}) {
  const data = cross.phase === 'ok' ? cross.data : undefined;
  const route = data && routeDetail(data, items);
  const peers = data && peerRows(data);

  return <section className="result-card">
    <div className="rc-title"><h3>{text.title}</h3></div>
    <p className="muted">{text.introduction}</p>
    {cross.phase === 'wait' && <p className="muted" role="status">{text[cross.wait ?? 'H3']}</p>}
    {cross.phase === 'loading' && <p className="muted" role="status">{text.loading}</p>}
    {cross.phase === 'error' && <>
      <p className="error-box" role="alert">{crossError(cross.error)}</p>
      <button type="button" className="btn-secondary" onClick={onRetry}>{text.retry}</button>
    </>}
    {cross.phase === 'stale' && <>
      <p className="error-box" role="alert">{text.stale}</p>
      <button type="button" className="btn-secondary" disabled={refreshing} onClick={onRefresh}>{text.refresh}</button>
    </>}
    {data && route && peers && <>
      <h4>{conclusionTitle(data.grade)}</h4>
      <p className="muted">{gradeText(data.grade, data.model)}</p>
      <h4>{text.evidenceTitle}</h4>
      <ul>{checkLines(data).map(line => <li key={`${line.code}:${line.group}`}>{lineText(line, data)}</li>)}</ul>
      <p className="muted">{text.explanation}</p>
      <details className="fold">
        <summary>{route.title}</summary>
        {route.lines.map(line => <p key={line} className="muted">{line}</p>)}
        {route.rows.length > 0 && <div className="table-wrap"><table>
          <thead><tr>{text.routeColumns.map(label => <th key={label} scope="col">{label}</th>)}</tr></thead>
          <tbody>{route.rows.map(row => <tr key={row[0]}>{row.map((value, column) => <td key={text.routeColumns[column]}>{value}</td>)}</tr>)}</tbody>
        </table></div>}
      </details>
      <details className="fold">
        <summary>{peersTitle(data.knowledge.peerReports)}</summary>
        {peers.length > 0 ? <div className="table-wrap"><table>
          <thead><tr>{text.peersColumns.map(label => <th key={label} scope="col">{label}</th>)}</tr></thead>
          <tbody>{peers.map((row, index) => <tr key={data.peers[index].reportId}>
            <td>{row.time}</td><td>{row.account}</td><td>{row.prefix}</td><td>{row.latest}</td><td>{row.vision}</td>
          </tr>)}</tbody>
        </table></div> : <p className="muted">{text.noPeers}</p>}
      </details>
      <details className="fold"><summary>{text.rulesTitle}</summary><p className="muted">{text.rules}</p></details>
    </>}
  </section>;
}
