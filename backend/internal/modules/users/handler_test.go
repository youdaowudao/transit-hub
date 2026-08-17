package users

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

type fakeUserRepository struct {
	requestedUserID string
	users           []User
}

func (r *fakeUserRepository) FindByID(_ context.Context, userID string) ([]User, error) {
	r.requestedUserID = userID
	return r.users, nil
}

func TestUsersEndpointReturnsOnlyAuthenticatedUser(t *testing.T) {
	repository := &fakeUserRepository{users: []User{{ID: "user-1", Email: "one@example.com"}}}
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewService(repository))

	request := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-1"))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/users status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if repository.requestedUserID != "user-1" {
		t.Fatalf("repository user ID = %q, want user-1", repository.requestedUserID)
	}
	var response []User
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response) != 1 || response[0].Email != "one@example.com" {
		t.Fatalf("response = %#v", response)
	}
}

func TestUsersEndpointRejectsMissingAuthenticationContext(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewService(&fakeUserRepository{}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/users", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/users status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestFindCurrentUserSQLScopesByID(t *testing.T) {
	if !strings.Contains(findUserByIDSQL, "WHERE id = $1") {
		t.Fatalf("find user SQL is not scoped by id: %s", findUserByIDSQL)
	}
}
