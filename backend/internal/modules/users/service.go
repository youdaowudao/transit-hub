package users

import "context"

type Service struct {
	repository userRepository
}

type userRepository interface {
	FindByID(ctx context.Context, userID string) ([]User, error)
}

func NewService(repository userRepository) *Service {
	return &Service{repository: repository}
}

func (s *Service) FindAll(ctx context.Context, userID string) ([]User, error) {
	return s.repository.FindByID(ctx, userID)
}
