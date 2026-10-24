package signup_risk

import (
	"context"
	"log"
	"time"
)

func (s *Service) Start(parent context.Context) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[signup-risk] %s", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.cancel == nil {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.cancel()
	done := s.done
	s.lifecycleMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
