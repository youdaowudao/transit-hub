package connection_health

// Both card and account-summary queries keep the original 1,000-record bound.
// Group first by the stored question ID, including all-failed/cancelled questions.
func modelControlRoundAggregateQuery(bounded string) string {
	return `SELECT batch_id,min(created_at) created_at,max(completed_at) completed_at,max(evidence_at) evidence_at,bool_or(running) running,
 sum(correct) correct,sum(incorrect) incorrect,sum(unreviewed) unreviewed,sum(failed) failed,sum(cancelled) cancelled,
 jsonb_agg(jsonb_build_object('questionId',question_id,'questionName',question_name,'questionBody',question_body,'correct',correct,'incorrect',incorrect,'unreviewed',unreviewed,'failed',failed,'cancelled',cancelled) ORDER BY question_id) questions
 FROM(SELECT batch_id,question_id,(array_agg(question_name ORDER BY created_at,id))[1] question_name,(array_agg(question_body ORDER BY created_at,id))[1] question_body,
 min(created_at) created_at,max(completed_at) completed_at,max(GREATEST(COALESCE(completed_at,updated_at),updated_at)) evidence_at,bool_or(status IN('pending','running')) running,
 count(*) FILTER(WHERE status='succeeded' AND answer_judgment='correct') correct,count(*) FILTER(WHERE status='succeeded' AND answer_judgment='incorrect') incorrect,
 count(*) FILTER(WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN('correct','incorrect'))) unreviewed,
 count(*) FILTER(WHERE status='failed') failed,count(*) FILTER(WHERE status='cancelled') cancelled
 FROM(` + bounded + `) bounded GROUP BY batch_id,question_id) questions
 GROUP BY batch_id ORDER BY min(created_at) DESC,batch_id DESC LIMIT 20`
}
