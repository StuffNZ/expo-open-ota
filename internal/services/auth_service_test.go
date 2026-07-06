package services

import (
	"testing"
)

func TestLoginWithPassword(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")

	t.Run("valid password returns tokens", func(t *testing.T) {
		t.Setenv("ADMIN_PASSWORD", "correct-password")
		auth := NewAuthService(nil)
		resp, err := auth.LoginWithPassword("correct-password")
		if err != nil {
			t.Fatalf("expected login to succeed, got error: %v", err)
		}
		if resp.Token == "" || resp.RefreshToken == "" {
			t.Fatal("expected non-empty token and refresh token")
		}
	})

	t.Run("invalid password is rejected", func(t *testing.T) {
		t.Setenv("ADMIN_PASSWORD", "correct-password")
		auth := NewAuthService(nil)
		if _, err := auth.LoginWithPassword("wrong-password"); err == nil {
			t.Fatal("expected login to fail with wrong password")
		}
	})

	t.Run("empty admin password rejects all logins", func(t *testing.T) {
		t.Setenv("ADMIN_PASSWORD", "")
		auth := NewAuthService(nil)
		if _, err := auth.LoginWithPassword(""); err == nil {
			t.Fatal("expected login to fail when ADMIN_PASSWORD is unset")
		}
	})

	t.Run("password of different length is rejected", func(t *testing.T) {
		t.Setenv("ADMIN_PASSWORD", "correct-password")
		auth := NewAuthService(nil)
		if _, err := auth.LoginWithPassword("correct-password-with-suffix"); err == nil {
			t.Fatal("expected login to fail with longer password sharing a prefix")
		}
	})
}
