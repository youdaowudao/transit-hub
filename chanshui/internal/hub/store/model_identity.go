package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

func addModelIdentityDefault(ctx context.Context, conn *sql.Conn) error {
	q, _ := questions.Preset("Q10")
	enabled, err := json.Marshal(q)
	if err != nil {
		return err
	}
	q.Enabled = false
	disabled, err := json.Marshal(q)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT OR IGNORE INTO hub_questions
		SELECT s.user_id,s.workspace_id,'Q10',
		CASE WHEN (SELECT count(*) FROM hub_questions q WHERE q.user_id=s.user_id AND q.workspace_id=s.workspace_id
		AND json_extract(q.definition_json,'$.kind')='iq' AND json_extract(q.definition_json,'$.enabled')=1)>=32 THEN ? ELSE ? END,1
		FROM hub_settings s`, string(disabled), string(enabled))
	return err
}
