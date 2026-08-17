package auth

import (
	"context"
	"testing"
	"time"
)

type fakeAuthRepository struct {
	userCount       int
	createdEmail    string
	createdPassword string
}

func (r *fakeAuthRepository) EnsureSchema(context.Context) error { return nil }
func (r *fakeAuthRepository) CountUsers(context.Context) (int, error) {
	return r.userCount, nil
}
func (r *fakeAuthRepository) CreateUser(_ context.Context, email, passwordHash string) error {
	r.createdEmail = email
	r.createdPassword = passwordHash
	return nil
}
func (r *fakeAuthRepository) PasswordHashByEmail(context.Context, string) (string, error) {
	return "", nil
}
func (r *fakeAuthRepository) UserIDBySessionToken(context.Context, string) (string, error) {
	return "", nil
}
func (r *fakeAuthRepository) UserIDByEmail(context.Context, string) (string, error) {
	return "", nil
}
func (r *fakeAuthRepository) CreateSession(context.Context, string, string, time.Time) error {
	return nil
}

func TestBootstrapAdminRejectsPublishedPlaceholderCredentialsOnEmptyDatabase(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
	}{
		{name: "published email", email: "admin@example.com", password: "a-real-password"},
		{name: "published password", email: "owner@example.net", password: "change-this-admin-password"},
		{name: "placeholder prefix case insensitive", email: "owner@example.net", password: " Change-This-Immediately "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeAuthRepository{}
			service := NewService(repository)
			if err := service.BootstrapAdmin(context.Background(), test.email, test.password); err == nil {
				t.Fatal("BootstrapAdmin() accepted published placeholder credentials")
			}
			if repository.createdEmail != "" || repository.createdPassword != "" {
				t.Fatal("BootstrapAdmin() created a user with placeholder credentials")
			}
		})
	}
}

func TestBootstrapAdminCreatesUserWithExplicitCredentials(t *testing.T) {
	repository := &fakeAuthRepository{}
	service := NewService(repository)
	if err := service.BootstrapAdmin(context.Background(), " Owner@Example.net ", "correct horse battery staple"); err != nil {
		t.Fatalf("BootstrapAdmin() error = %v", err)
	}
	if repository.createdEmail != "owner@example.net" {
		t.Fatalf("created email = %q", repository.createdEmail)
	}
	if repository.createdPassword == "correct horse battery staple" || !verifyPassword(repository.createdPassword, "correct horse battery staple") {
		t.Fatal("created password was not securely hashed")
	}
}
