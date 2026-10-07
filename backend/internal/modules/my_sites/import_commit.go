package my_sites

import "errors"

const (
	CommitCommitted             = "committed"
	CommitConfirmedNotCommitted = "confirmed_not_committed"
	CommitUncertain             = "uncertain"
)

// Only the transaction's actual stage may establish that it did not commit.
// A subsequent Rollback or the content of an error message provides no proof.
type ConnectionCommitError struct {
	Outcome string
	Cause   error
}

func (e *ConnectionCommitError) Error() string { return e.Cause.Error() }
func (e *ConnectionCommitError) Unwrap() error { return e.Cause }
func connectionCommitOutcome(err error) string {
	if err == nil {
		return CommitCommitted
	}
	var outcome *ConnectionCommitError
	if errors.As(err, &outcome) && outcome.Outcome == CommitConfirmedNotCommitted {
		return CommitConfirmedNotCommitted
	}
	return CommitUncertain
}
