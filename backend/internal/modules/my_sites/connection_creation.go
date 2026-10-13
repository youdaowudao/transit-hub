package my_sites

import (
	"net/http"
	"sync"
)

type connectionCreationTarget struct{ user, workspace, site, group string }
type connectionCreationOperation struct{ user, workspace, operation string }

// Protect overlapping mutations in the shared backend Service. Network calls
// happen outside creationMutex, and protection ends after compensation.
func (s *Service) tryBeginConnectionCreation(user, workspace, site, group, operation string) (func(), error) {
	target := connectionCreationTarget{user, workspace, site, group}
	op := connectionCreationOperation{user, workspace, operation}
	s.creationMutex.Lock()
	if s.creationTargets[target] || operation != "" && s.creationOperations[op] {
		s.creationMutex.Unlock()
		return nil, importFailure(ErrorImportInProgress, "validation", "local", "pending", http.StatusConflict, nil)
	}
	if s.creationTargets == nil {
		s.creationTargets = make(map[connectionCreationTarget]bool)
	}
	if s.creationOperations == nil {
		s.creationOperations = make(map[connectionCreationOperation]bool)
	}
	s.creationTargets[target] = true
	if operation != "" {
		s.creationOperations[op] = true
	}
	s.creationMutex.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.creationMutex.Lock()
			delete(s.creationTargets, target)
			if operation != "" {
				delete(s.creationOperations, op)
			}
			s.creationMutex.Unlock()
		})
	}, nil
}
