package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"expo-open-ota/internal/services"
)

func runAuthMiddleware(t *testing.T, configure func(r *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	authService := services.NewAuthService(nil)
	handler := NewAuthMiddleware(authService)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/api/settings", nil)
	configure(r)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAuthMiddleware(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ADMIN_PASSWORD", "test-password")

	t.Run("cli auth rejected on app-agnostic route", func(t *testing.T) {
		// Use-Cli-Auth needs an APP_ID path variable to anchor the tenant;
		// without one the request must be rejected, not forwarded.
		w := runAuthMiddleware(t, func(r *http.Request) {
			r.Header.Set("Use-Cli-Auth", "true")
			r.Header.Set("Authorization", "Bearer some-api-key")
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for cli auth without app scope, got %d", w.Code)
		}
	})

	t.Run("valid admin JWT is accepted", func(t *testing.T) {
		resp, err := services.NewAuthService(nil).LoginWithPassword("test-password")
		if err != nil {
			t.Fatalf("login failed: %v", err)
		}
		w := runAuthMiddleware(t, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+resp.Token)
		})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 with valid JWT, got %d", w.Code)
		}
	})

	t.Run("invalid bearer token is rejected", func(t *testing.T) {
		w := runAuthMiddleware(t, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer not-a-jwt")
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with invalid token, got %d", w.Code)
		}
	})

	t.Run("missing Authorization header is rejected", func(t *testing.T) {
		w := runAuthMiddleware(t, func(r *http.Request) {})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with no Authorization header, got %d", w.Code)
		}
	})
}
