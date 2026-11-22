package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/kiss-kedaya/chanshui/internal/hub/prefixcheck"
)

type CrossCheckProjection struct {
	Report     CrossCheckReport
	Items      []CrossCheckItem
	Injections []CrossCheckInjection
	Identity   *CrossCheckIdentity
	Peers      []CrossCheckPeer
}

type CrossCheckReport struct {
	ReportID, AccountID, Model, Status string
	CreatedAtMs, UpdatedAtMs           int64
	Prefix                             json.RawMessage
	PrefixWrongType                    bool
	KnowledgeRows                      []prefixcheck.KnowledgeRow
	Vision                             json.RawMessage
}

type CrossCheckItem struct {
	ItemID        string
	OrderIndex    int
	CompletedAtMs int64
}

type CrossCheckInjection struct {
	ItemID, Template, Error string
	OrderIndex              int
	Points                  json.RawMessage
	Reliable                *bool
}

type CrossCheckIdentity struct {
	ItemID, Status, Source string
	OrderIndex             int
	Earned, Max            *int64
}

type CrossCheckPeer struct {
	ReportID, AccountName, Status string
	CreatedAtMs                   int64
	Prefix, Vision                json.RawMessage
	KnowledgeRows                 []prefixcheck.KnowledgeRow
}

type projectedJSON struct {
	kind  sql.NullString
	value any
}

func (v projectedJSON) stringValue(kind string) (string, bool) {
	s, ok := v.value.(string)
	if !ok || v.kind.String != kind {
		return "", false
	}
	return s, true
}

func (v projectedJSON) raw(kind string) json.RawMessage {
	s, ok := v.stringValue(kind)
	if !ok {
		return nil
	}
	return json.RawMessage(s)
}

func (v projectedJSON) integer() *int64 {
	// SQLite can extract a JSON integer outside int64 as a floating-point value.
	const maxSafeInteger = 1<<53 - 1
	n, ok := v.value.(int64)
	if v.kind.String != "integer" || !ok || n < -maxSafeInteger || n > maxSafeInteger {
		return nil
	}
	return &n
}

func (s *Store) CrossCheckData(ctx context.Context, o Owner, id string) (CrossCheckProjection, error) {
	return s.crossCheckData(ctx, o, id, nil)
}

func (s *Store) crossCheckData(ctx context.Context, o Owner, id string, afterReport func()) (CrossCheckProjection, error) {
	// Every query shares one SQLite snapshot. This transaction only runs SELECTs.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CrossCheckProjection{}, err
	}
	defer tx.Rollback()

	out := CrossCheckProjection{Report: CrossCheckReport{ReportID: id}}
	report := &out.Report
	var prefix projectedJSON
	err = tx.QueryRowContext(ctx, `SELECT account_id, model, status, created_at_ms, updated_at_ms,
		json_type(summary_json,'$.prefix'), json_extract(summary_json,'$.prefix')
		FROM hub_reports WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, id).
		Scan(&report.AccountID, &report.Model, &report.Status, &report.CreatedAtMs, &report.UpdatedAtMs, &prefix.kind, &prefix.value)
	if err == sql.ErrNoRows {
		return CrossCheckProjection{}, Fail(404, "报告不存在或不属于当前工作区", "report")
	}
	if err != nil {
		return CrossCheckProjection{}, err
	}
	report.Prefix = prefix.raw("object")
	report.PrefixWrongType = prefix.kind.Valid && prefix.kind.String != "object"
	if afterReport != nil {
		afterReport()
	}
	if report.Vision, report.KnowledgeRows, err = readCrossCheckSummary(ctx, tx, id); err != nil {
		return CrossCheckProjection{}, err
	}
	if out.Items, err = readCrossCheckItems(ctx, tx, id); err != nil {
		return CrossCheckProjection{}, err
	}
	if out.Injections, err = readCrossCheckInjections(ctx, tx, id); err != nil {
		return CrossCheckProjection{}, err
	}
	if out.Identity, err = readCrossCheckIdentity(ctx, tx, id); err != nil {
		return CrossCheckProjection{}, err
	}
	if out.Peers, err = readCrossCheckPeers(ctx, tx, o, *report); err != nil {
		return CrossCheckProjection{}, err
	}
	for i := range out.Peers {
		peer := &out.Peers[i]
		if peer.Vision, peer.KnowledgeRows, err = readCrossCheckSummary(ctx, tx, peer.ReportID); err != nil {
			return CrossCheckProjection{}, err
		}
	}
	return out, nil
}

func readCrossCheckSummary(ctx context.Context, tx *sql.Tx, id string) (json.RawMessage, []prefixcheck.KnowledgeRow, error) {
	var vision []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM hub_reports, json_each(hub_reports.summary_json,'$.parts')
		WHERE hub_reports.report_id=? AND json_type(hub_reports.summary_json,'$.parts')='array'
		AND json_each.type='object' AND json_extract(value,'$.key')='vision'
		ORDER BY json_each.key LIMIT 1`, id).Scan(&vision)
	if err != nil && err != sql.ErrNoRows {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT json_extract(value,'$.id'), json_type(value,'$.id'),
		json_extract(value,'$.kind'), json_type(value,'$.kind'),
		json_extract(value,'$.event'), json_type(value,'$.event'),
		json_extract(value,'$.happened_at'), json_type(value,'$.happened_at')
		FROM hub_reports, json_each(hub_reports.summary_json,'$.knowledge.rows')
		WHERE hub_reports.report_id=? AND json_type(hub_reports.summary_json,'$.knowledge.rows')='array'
		AND json_each.type='object' ORDER BY json_each.key`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var knowledge []prefixcheck.KnowledgeRow
	for rows.Next() {
		var id, kind, event, happenedAt projectedJSON
		if err = rows.Scan(&id.value, &id.kind, &kind.value, &kind.kind, &event.value, &event.kind, &happenedAt.value, &happenedAt.kind); err != nil {
			return nil, nil, err
		}
		rowID, idOK := id.stringValue("text")
		rowKind, kindOK := kind.stringValue("text")
		dateText, _ := happenedAt.stringValue("text")
		date, dateOK := prefixcheck.KnowledgeDate(dateText)
		if idOK && kindOK && dateOK {
			name, _ := event.stringValue("text")
			knowledge = append(knowledge, prefixcheck.KnowledgeRow{ID: rowID, Kind: rowKind, Event: name, HappenedAt: date})
		}
	}
	return vision, knowledge, rows.Err()
}

func readCrossCheckItems(ctx context.Context, tx *sql.Tx, id string) ([]CrossCheckItem, error) {
	rows, err := tx.QueryContext(ctx, `SELECT item_id, order_index, completed_at_ms FROM hub_items
		WHERE report_id=? ORDER BY order_index, item_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CrossCheckItem
	for rows.Next() {
		var item CrossCheckItem
		if err = rows.Scan(&item.ItemID, &item.OrderIndex, &item.CompletedAtMs); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func readCrossCheckInjections(ctx context.Context, tx *sql.Tx, id string) ([]CrossCheckInjection, error) {
	rows, err := tx.QueryContext(ctx, `SELECT item_id, order_index,
		json_type(question_json,'$.definition.template'), json_extract(question_json,'$.definition.template'),
		json_type(result_json,'$.evidence.points'), json_extract(result_json,'$.evidence.points'),
		json_type(result_json,'$.evidence.reliable'),
		json_type(result_json,'$.error'), json_extract(result_json,'$.error'),
		json_type(result_json,'$.evidence.error'), json_extract(result_json,'$.evidence.error')
		FROM hub_items WHERE report_id=? AND section_id='injection' ORDER BY order_index, item_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var injections []CrossCheckInjection
	for rows.Next() {
		var item CrossCheckInjection
		var template, points, resultError, evidenceError projectedJSON
		var reliableKind sql.NullString
		if err = rows.Scan(&item.ItemID, &item.OrderIndex, &template.kind, &template.value, &points.kind, &points.value,
			&reliableKind, &resultError.kind, &resultError.value, &evidenceError.kind, &evidenceError.value); err != nil {
			return nil, err
		}
		item.Template, _ = template.stringValue("text")
		item.Points = points.raw("array")
		if reliableKind.String == "true" || reliableKind.String == "false" {
			reliable := reliableKind.String == "true"
			item.Reliable = &reliable
		}
		var hasError bool
		if item.Error, hasError = resultError.stringValue("text"); !hasError {
			item.Error, _ = evidenceError.stringValue("text")
		}
		injections = append(injections, item)
	}
	return injections, rows.Err()
}

func readCrossCheckIdentity(ctx context.Context, tx *sql.Tx, id string) (*CrossCheckIdentity, error) {
	var item CrossCheckIdentity
	var status, earned, maxPoints, source projectedJSON
	err := tx.QueryRowContext(ctx, `SELECT item_id, order_index,
		json_type(result_json,'$.judgment.status'), json_extract(result_json,'$.judgment.status'),
		json_type(result_json,'$.judgment.earned'), json_extract(result_json,'$.judgment.earned'),
		json_type(result_json,'$.judgment.max'), json_extract(result_json,'$.judgment.max'),
		json_type(result_json,'$.judgment.source'), json_extract(result_json,'$.judgment.source')
		FROM hub_items WHERE report_id=? AND json_extract(question_json,'$.definition.judge.type')='model_identity'
		ORDER BY order_index, item_id LIMIT 1`, id).
		Scan(&item.ItemID, &item.OrderIndex, &status.kind, &status.value, &earned.kind, &earned.value, &maxPoints.kind, &maxPoints.value, &source.kind, &source.value)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.Status, _ = status.stringValue("text")
	item.Source, _ = source.stringValue("text")
	item.Earned = earned.integer()
	item.Max = maxPoints.integer()
	return &item, nil
}

func readCrossCheckPeers(ctx context.Context, tx *sql.Tx, o Owner, report CrossCheckReport) ([]CrossCheckPeer, error) {
	window := prefixcheck.PeerWindow.Milliseconds()
	rows, err := tx.QueryContext(ctx, `SELECT report_id, account_name, status, created_at_ms,
		json_type(summary_json,'$.prefix'), json_extract(summary_json,'$.prefix')
		FROM hub_reports
		WHERE user_id=? AND workspace_id=? AND report_id<>? AND account_id<>?
		AND lower(model)=lower(?) AND status IN ('done','partial')
		AND COALESCE(json_type(summary_json,'$.singleAsk'),'')<>'true'
		AND created_at_ms BETWEEN ? AND ?
		ORDER BY abs(created_at_ms-?) ASC, report_id ASC LIMIT ?`, o.UserID, o.WorkspaceID, report.ReportID,
		report.AccountID, report.Model, report.CreatedAtMs-window, report.CreatedAtMs+window, report.CreatedAtMs, prefixcheck.PeerLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []CrossCheckPeer
	for rows.Next() {
		var peer CrossCheckPeer
		var prefix projectedJSON
		if err = rows.Scan(&peer.ReportID, &peer.AccountName, &peer.Status, &peer.CreatedAtMs, &prefix.kind, &prefix.value); err != nil {
			return nil, err
		}
		peer.Prefix = prefix.raw("object")
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}
